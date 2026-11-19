package core

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// The ZNHB bridge escrow (ledger PL-BC-C): a user locks ZNHB into a module-owned escrow account and names a
// destination chain and address; the same amount can then be minted there as a wrapped token by whoever runs the
// off-chain side. When that wrapped token is burned the bridge authority releases the ZNHB back, once per burn id.
//
// What it must never do, and how each is held:
//   - Create or destroy ZNHB: a lock moves ZNHB between two accounts, a release moves it back; no mint path, no
//     pool counter touched. The admin wallet can neither lock nor receive a release, so ZNHB leaves it only by the
//     curve or the swap-voucher path, as before. CheckZNHBSupplyInvariant is unaffected by construction and the tests
//     run it after every operation.
//   - Release twice: a burn id is a key in state and is written with the release.
//   - Release what was not locked: a release fails while the escrow holds less than the amount.
//   - Be reachable before its height: from BridgeActivationHeight only; before it the two bytes are unknown types.

var (
	// ErrBridgeInvalidPayload: the payload does not decode or names an unusable destination, recipient or burn id,
	// or the amount is not positive. A pure function of the transaction's own bytes; pruned.
	ErrBridgeInvalidPayload = errors.New("bridge: invalid payload")
	// ErrBridgeAdminWallet: the admin wallet may neither lock nor receive a release (ZNHB leaves it only through the
	// curve or the swap-voucher path). Pruned.
	ErrBridgeAdminWallet = errors.New("bridge: the admin wallet cannot use the bridge")
	// ErrBridgeAboveMax: the lock is above BridgeConfig.MaxLockZNHBWei. A chain rule that does not change; pruned.
	ErrBridgeAboveMax = errors.New("bridge: the amount is above the per-lock cap")
	// ErrBridgeNotGuardian: a pause, an unpause or a cancel from anyone but the guardian in force. Pruned.
	ErrBridgeNotGuardian = errors.New("bridge: only the guardian may do this")
	// ErrBridgeBadSignatures: the signatures on a release do not make a quorum of the signers in force (a wrong length,
	// an unknown signer, too few distinct signers). A pure function of the transaction and the committed settings; pruned.
	ErrBridgeBadSignatures = errors.New("bridge: the release is not signed by enough of the bridge signers")
	// ErrBridgeAboveMaxRelease: the release is above MaxReleaseZNHBWei. A chain rule; pruned.
	ErrBridgeAboveMaxRelease = errors.New("bridge: the amount is above the per-release cap")
	// ErrBridgeAlreadyReleased: this burn id was released, queued or cancelled before. Pruned: it can never become valid again.
	ErrBridgeAlreadyReleased = errors.New("bridge: this burn id was already used")
	// ErrBridgePaused: the guardian has paused the bridge. Skipped: it ends when the guardian unpauses.
	ErrBridgePaused = errors.New("bridge: the bridge is paused")
	// ErrBridgeWindowCap: the release would take the window past WindowCapZNHBWei. Skipped: the next window has room.
	ErrBridgeWindowCap = errors.New("bridge: the release would exceed the cap for this window")
	// ErrBridgeNotPending: no release is waiting under this burn id. Skipped: it may be queued by a later transaction.
	ErrBridgeNotPending = errors.New("bridge: no release is waiting under this burn id")
	// ErrBridgeNotYet: the waiting release cannot be executed before its height. Skipped.
	ErrBridgeNotYet = errors.New("bridge: the waiting release cannot be executed yet")
	// ErrBridgeInsufficientBalance: the sender holds less ZNHB than it locks. A balance can grow, so skipped.
	ErrBridgeInsufficientBalance = errors.New("bridge: the sender holds less ZNHB than the lock")
	// ErrBridgeEscrowShort: the escrow holds less than the release. Locks can still arrive, so skipped.
	ErrBridgeEscrowShort = errors.New("bridge: the escrow holds less than the release")
)

var (
	bridgeDestChainPattern = regexp.MustCompile(`^eip155:[1-9][0-9]{0,18}$`)
	bridgeDestAddrPattern  = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
)

type bridgeLockPayload struct {
	DestChain string
	Dest      string
}

// Operations of a TxTypeBridgeRelease. One transaction type carries all five so the transaction registry, the mempool
// classes and the poison matrix stay as they were; who may send which is checked per operation.
const (
	BridgeOpRelease uint8 = 0 // anyone sends it; the signatures are what authorizes it
	BridgeOpExecute uint8 = 1 // anyone sends it, once a waiting release is due
	BridgeOpCancel  uint8 = 2 // the guardian only
	BridgeOpPause   uint8 = 3 // the guardian only
	BridgeOpUnpause uint8 = 4 // the guardian only
)

type bridgeReleasePayload struct {
	Op         uint8
	BurnID     []byte
	Recipient  []byte
	Amount     *big.Int
	Signatures [][]byte
}

type bridgeTotals struct {
	Locked   *big.Int
	Released *big.Int
	// Pending is the amount of the releases that are waiting out their delay: still in the escrow, already spoken for.
	Pending *big.Int
}

type bridgeLockRecord struct {
	Sender    []byte
	Amount    *big.Int
	DestChain string
	Dest      string
	Height    uint64
}

// Release record statuses: a burn id with a record is spent whichever it is.
const (
	bridgeStatusPaid      uint8 = 1
	bridgeStatusCancelled uint8 = 2
)

type bridgeReleaseRecord struct {
	Recipient []byte
	Amount    *big.Int
	Height    uint64
	Status    uint8
}

// bridgePendingRecord is a release that is waiting out its delay.
type bridgePendingRecord struct {
	Recipient    []byte
	Amount       *big.Int
	QueuedAt     uint64
	ExecutableAt uint64
}

type bridgePauseRecord struct {
	Paused bool
	Height uint64
}

func bridgeEscrowAddress() []byte {
	return deriveModuleAddress("module/bridge/escrow", crypto.ZNHBPrefix).Bytes()
}

func bridgeLockKey(id uint64) []byte    { return []byte("bridge/lock/" + strconv.FormatUint(id, 10)) }
func bridgeReleaseKey(id []byte) []byte { return []byte("bridge/rel/" + hex.EncodeToString(id)) }

func loadBridgeTotals(m *nhbstate.Manager) (bridgeTotals, error) {
	var t bridgeTotals
	found, err := m.KVGet([]byte("bridge/totals"), &t)
	if err != nil {
		return t, err
	}
	if !found || t.Locked == nil || t.Released == nil {
		return bridgeTotals{Locked: new(big.Int), Released: new(big.Int), Pending: new(big.Int)}, nil
	}
	if t.Pending == nil {
		t.Pending = new(big.Int)
	}
	return t, nil
}

func bridgePauseKey() []byte { return []byte("bridge/pause") }

func loadBridgePause(m *nhbstate.Manager) (bridgePauseRecord, error) {
	var rec bridgePauseRecord
	if _, err := m.KVGet(bridgePauseKey(), &rec); err != nil {
		return rec, err
	}
	return rec, nil
}

func loadBridgeSeq(m *nhbstate.Manager) (uint64, error) {
	var seq uint64
	if _, err := m.KVGet([]byte("bridge/seq"), &seq); err != nil {
		return 0, err
	}
	return seq, nil
}

func (sp *StateProcessor) bridgeIsAdminWallet(addr []byte) bool {
	return sp.hasAdminWallet && bytes.Equal(addr, sp.adminWallet[:])
}

func znhbBalanceOf(account *types.Account) *big.Int {
	if account == nil || account.BalanceZNHB == nil {
		return new(big.Int)
	}
	return account.BalanceZNHB
}

func (sp *StateProcessor) bridgeEvent(kind string, attrs map[string]string) {
	sp.AppendEvent(&types.Event{Type: kind, Attributes: attrs})
}

// applyBridgeLockTransaction locks tx.Value ZNHB in the escrow for the destination the payload names.
func (sp *StateProcessor) applyBridgeLockTransaction(tx *types.Transaction, sender []byte) error {
	var payload bridgeLockPayload
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrBridgeInvalidPayload, err)
	}
	if !bridgeDestChainPattern.MatchString(payload.DestChain) {
		return fmt.Errorf("%w: destination chain must look like eip155:<id>", ErrBridgeInvalidPayload)
	}
	if !bridgeDestAddrPattern.MatchString(payload.Dest) {
		return fmt.Errorf("%w: destination must be a 0x address of 20 bytes", ErrBridgeInvalidPayload)
	}
	if tx.Value == nil || tx.Value.Sign() <= 0 {
		return fmt.Errorf("%w: the amount must be positive", ErrBridgeInvalidPayload)
	}
	if sp.bridgeIsAdminWallet(sender) {
		return ErrBridgeAdminWallet
	}
	if max := sp.bridge.MaxLockWei; max != nil && tx.Value.Cmp(max) > 0 {
		return ErrBridgeAboveMax
	}
	manager := nhbstate.NewManager(sp.Trie)
	if pause, err := loadBridgePause(manager); err != nil {
		return err
	} else if pause.Paused {
		return ErrBridgePaused
	}
	account, err := sp.getAccount(sender)
	if err != nil {
		return err
	}
	if znhbBalanceOf(account).Cmp(tx.Value) < 0 {
		return ErrBridgeInsufficientBalance
	}
	escrowAddr := bridgeEscrowAddress()
	totals, err := loadBridgeTotals(manager)
	if err != nil {
		return err
	}
	seq, err := loadBridgeSeq(manager)
	if err != nil {
		return err
	}

	// Everything is checked; now write: sender debit, escrow credit, record, totals, event, nonce.
	account.BalanceZNHB = new(big.Int).Sub(znhbBalanceOf(account), tx.Value)
	if err := sp.setAccount(sender, account); err != nil {
		return err
	}
	escrow, err := sp.getAccount(escrowAddr)
	if err != nil {
		return err
	}
	escrow.BalanceZNHB = new(big.Int).Add(znhbBalanceOf(escrow), tx.Value)
	if err := sp.setAccount(escrowAddr, escrow); err != nil {
		return err
	}
	seq++
	record := bridgeLockRecord{Sender: append([]byte(nil), sender...), Amount: new(big.Int).Set(tx.Value), DestChain: payload.DestChain, Dest: strings.ToLower(payload.Dest), Height: sp.blockHeight()}
	if err := manager.KVPut(bridgeLockKey(seq), record); err != nil {
		return err
	}
	if err := manager.KVPut([]byte("bridge/seq"), seq); err != nil {
		return err
	}
	totals.Locked = new(big.Int).Add(totals.Locked, tx.Value)
	if err := manager.KVPut([]byte("bridge/totals"), totals); err != nil {
		return err
	}
	sp.bridgeEvent("bridge.locked", map[string]string{
		"id":        strconv.FormatUint(seq, 10),
		"sender":    crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...)).String(),
		"amountWei": tx.Value.String(),
		"destChain": record.DestChain,
		"dest":      record.Dest,
	})
	return sp.incrementNativeAccountNonce(sender)
}

// BridgeStatusView is what the read-only bridge_getStatus call reports. It is the escrow proof: the escrow holds
// at least what is outstanding (locked and not released), nothing is released without a quorum of signers, and the
// limits that bound a loss are in the open.
type BridgeStatusView struct {
	Active           bool
	ActivationHeight uint64
	EscrowAddress    string
	EscrowBalanceWei *big.Int
	LockedWei        *big.Int
	ReleasedWei      *big.Int
	OutstandingWei   *big.Int
	NextLockID       uint64
	MaxLockWei       *big.Int

	// EscrowCovered is true while the escrow holds at least OutstandingWei (it may hold more: a plain transfer to the
	// escrow address is a gift).
	EscrowCovered bool
	Paused        bool
	PendingWei    *big.Int
	Signers       []string
	Threshold     uint32
	Guardian      string
	MaxReleaseWei *big.Int
	WindowBlocks  uint64
	WindowCapWei  *big.Int
	WindowUsedWei *big.Int
	DelayAboveWei *big.Int // nil = releases do not wait
	DelayBlocks   uint64
	NextRotation  uint64 // the height of the next scheduled rotation, 0 = none
}

// BridgeStatus reads the bridge's totals and escrow balance. It writes nothing.
func (sp *StateProcessor) BridgeStatus() (*BridgeStatusView, error) {
	manager := nhbstate.NewManager(sp.Trie)
	totals, err := loadBridgeTotals(manager)
	if err != nil {
		return nil, err
	}
	seq, err := loadBridgeSeq(manager)
	if err != nil {
		return nil, err
	}
	escrowAddr := bridgeEscrowAddress()
	escrow, err := sp.getAccount(escrowAddr)
	if err != nil {
		return nil, err
	}
	pause, err := loadBridgePause(manager)
	if err != nil {
		return nil, err
	}
	view := &BridgeStatusView{
		Active:           sp.bridge.ActivationHeight != 0,
		ActivationHeight: sp.bridge.ActivationHeight,
		EscrowAddress:    crypto.MustNewAddress(crypto.ZNHBPrefix, escrowAddr).String(),
		EscrowBalanceWei: new(big.Int).Set(znhbBalanceOf(escrow)),
		LockedWei:        totals.Locked,
		ReleasedWei:      totals.Released,
		OutstandingWei:   new(big.Int).Sub(totals.Locked, totals.Released),
		NextLockID:       seq + 1,
		Paused:           pause.Paused,
		PendingWei:       new(big.Int).Set(totals.Pending),
	}
	view.EscrowCovered = view.EscrowBalanceWei.Cmp(view.OutstandingWei) >= 0
	if sp.bridge.MaxLockWei != nil {
		view.MaxLockWei = new(big.Int).Set(sp.bridge.MaxLockWei)
	}
	height := sp.blockHeight()
	for i := range sp.bridge.Params {
		if sp.bridge.Params[i].FromHeight > height {
			view.NextRotation = sp.bridge.Params[i].FromHeight
			break
		}
	}
	if p := sp.bridge.paramsAt(height); p != nil {
		for _, s := range p.Signers {
			view.Signers = append(view.Signers, crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), s[:]...)).String())
		}
		view.Threshold = p.Threshold
		view.Guardian = crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), p.Guardian[:]...)).String()
		view.MaxReleaseWei = new(big.Int).Set(p.MaxReleaseWei)
		view.WindowBlocks = p.WindowBlocks
		view.WindowCapWei = new(big.Int).Set(p.WindowCapWei)
		view.WindowUsedWei, err = loadBridgeWindowUsed(manager, p.WindowBlocks, height)
		if err != nil {
			return nil, err
		}
		if p.DelayAboveWei != nil {
			view.DelayAboveWei = new(big.Int).Set(p.DelayAboveWei)
		}
		view.DelayBlocks = p.DelayBlocks
	}
	return view, nil
}

// CheckBridgeInvariant reports an error when the escrow holds less than what was locked and not yet released.
// (It may hold more: a plain transfer to the escrow address is a gift.) It reads state and writes nothing.
func (sp *StateProcessor) CheckBridgeInvariant() error {
	view, err := sp.BridgeStatus()
	if err != nil {
		return err
	}
	if view.EscrowBalanceWei.Cmp(view.OutstandingWei) < 0 {
		return fmt.Errorf("bridge invariant: escrow holds %s, outstanding is %s", view.EscrowBalanceWei, view.OutstandingWei)
	}
	if view.OutstandingWei.Sign() < 0 {
		return fmt.Errorf("bridge invariant: released %s exceeds locked %s", view.ReleasedWei, view.LockedWei)
	}
	if view.PendingWei.Cmp(view.OutstandingWei) > 0 {
		return fmt.Errorf("bridge invariant: %s is waiting to be released but only %s is outstanding", view.PendingWei, view.OutstandingWei)
	}
	return nil
}
