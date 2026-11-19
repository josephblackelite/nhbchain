package core

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// Stage 1 of the bridge trust plan (ledger PL-BC-C): the release path. A release of ZNHB out of the escrow needs a
// quorum of independent signers, is bounded in size and in total per window, can be made to wait before it pays, and
// can be stopped by a guardian that can do nothing else. What each piece is for:
//
//   - Threshold signers: no single key, here or on the EVM side, can release ZNHB. Each signer signs a digest of the
//     EVM burn it saw (BridgeReleaseDigest); SignerThreshold distinct signatures are checked on chain.
//   - Caps: one release is at most MaxReleaseWei and the releases of one window of WindowBlocks blocks are at most
//     WindowCapWei, counted when a release is accepted (paid or queued), so a flood of queued releases cannot pass the cap
//     and the loss from a quorum of stolen signing keys is bounded per window, not by the escrow.
//   - Waiting: a release above DelayAboveWei is queued and can be executed by anyone after DelayBlocks; that is the
//     time in which a person looks. The escrow still holds the ZNHB meanwhile.
//   - Guardian: pauses the bridge (locks, releases and executions stop), unpauses it, and cancels a waiting release. It
//     cannot release, mint or move ZNHB, so losing it can stop the bridge but never empty it. A cancelled burn id stays
//     spent: the ZNHB stays in the escrow, and paying the holder of the burned token is a decision for the signers with a
//     new burn id once the guardian's concern is settled.
//   - Rotations: the signers, the guardian and the limits can be replaced from a scheduled height (settings of the file,
//     identical on every validator), so a leaked signer is answered without a hard fork.
//
// What stays true from the first stage: no path creates or destroys ZNHB; a release never exceeds what was locked and
// not yet released, whatever the escrow holds; a burn id is spent once; and from before its height the type is unknown.

// BridgeReleaseDomainV1 is the domain separator of a release attestation, distinct from every other signed message of
// the chain so a signature over one can never be replayed as another.
const BridgeReleaseDomainV1 = "NHB_BRIDGE_RELEASE_V1"

// bridgeNetworkID is the chain id a release attestation names: the running network's own id once it is known, else the
// legacy constant (a bare test processor).
func bridgeNetworkID(sp *StateProcessor) uint64 {
	if sp != nil && sp.swapVoucherChainID != 0 {
		return sp.swapVoucherChainID
	}
	return types.NHBChainID().Uint64()
}

// BridgeReleaseDigest is the 32 bytes each signer signs to attest that the EVM burn named burnID happened and that
// amount ZNHB wei is owed to recipient on this network:
//
//	keccak256( domain | networkChainID (8 bytes) | escrow address (20) | len(burnID) (2 bytes) | burnID | recipient (20) | amount (32 bytes) )
//
// Every field has a fixed width or a length prefix, so two different attestations never share a digest. The signature is
// the usual 65 bytes (r, s, v) over the digest itself, with v 0/1 or 27/28.
func BridgeReleaseDigest(networkChainID uint64, burnID, recipient []byte, amount *big.Int) ([32]byte, error) {
	var out [32]byte
	if len(burnID) == 0 || len(burnID) > 64 {
		return out, fmt.Errorf("%w: the burn id must be 1 to 64 bytes", ErrBridgeInvalidPayload)
	}
	if len(recipient) != 20 {
		return out, fmt.Errorf("%w: the recipient must be 20 bytes", ErrBridgeInvalidPayload)
	}
	if amount == nil || amount.Sign() <= 0 || amount.BitLen() > 256 {
		return out, fmt.Errorf("%w: the amount must be positive and fit 256 bits", ErrBridgeInvalidPayload)
	}
	var buf bytes.Buffer
	buf.WriteString(BridgeReleaseDomainV1)
	var u64 [8]byte
	binary.BigEndian.PutUint64(u64[:], networkChainID)
	buf.Write(u64[:])
	buf.Write(bridgeEscrowAddress())
	var u16 [2]byte
	binary.BigEndian.PutUint16(u16[:], uint16(len(burnID)))
	buf.Write(u16[:])
	buf.Write(burnID)
	buf.Write(recipient)
	var amt [32]byte
	amount.FillBytes(amt[:])
	buf.Write(amt[:])
	copy(out[:], ethcrypto.Keccak256(buf.Bytes()))
	return out, nil
}

// verifyBridgeSignatures requires at least p.Threshold distinct signers of p to have signed digest. Any signature that
// is malformed, or from an address that is not a signer in force, fails the whole release: the submitter that builds the
// bundle is responsible for it, and a strict rule is one every validator evaluates the same way.
func verifyBridgeSignatures(p *BridgeParams, digest [32]byte, signatures [][]byte) error {
	if len(signatures) == 0 {
		return fmt.Errorf("%w: no signatures", ErrBridgeBadSignatures)
	}
	if len(signatures) > len(p.Signers) {
		return fmt.Errorf("%w: more signatures (%d) than signers (%d)", ErrBridgeBadSignatures, len(signatures), len(p.Signers))
	}
	allowed := make(map[[20]byte]struct{}, len(p.Signers))
	for _, s := range p.Signers {
		allowed[s] = struct{}{}
	}
	seen := make(map[[20]byte]struct{}, len(signatures))
	for i, sig := range signatures {
		if len(sig) != 65 {
			return fmt.Errorf("%w: signature %d is not 65 bytes", ErrBridgeBadSignatures, i)
		}
		buf := append([]byte(nil), sig...)
		if buf[64] >= 27 {
			buf[64] -= 27
		}
		// Only the canonical recovery ids are accepted: the library behind SigToPub masks the compressed-key flag, so
		// a v of 5 would recover the same key as a v of 1 and one signature would have several valid encodings.
		if buf[64] > 1 {
			return fmt.Errorf("%w: signature %d has an invalid recovery id", ErrBridgeBadSignatures, i)
		}
		pub, err := ethcrypto.SigToPub(digest[:], buf)
		if err != nil {
			return fmt.Errorf("%w: signature %d does not recover: %v", ErrBridgeBadSignatures, i, err)
		}
		var signer [20]byte
		copy(signer[:], ethcrypto.PubkeyToAddress(*pub).Bytes())
		if _, ok := allowed[signer]; !ok {
			return fmt.Errorf("%w: signature %d is not from a signer in force", ErrBridgeBadSignatures, i)
		}
		seen[signer] = struct{}{}
	}
	if len(seen) < int(p.Threshold) {
		return fmt.Errorf("%w: %d distinct signers, %d needed", ErrBridgeBadSignatures, len(seen), p.Threshold)
	}
	return nil
}

func bridgePendingKey(id []byte) []byte { return []byte("bridge/pend/" + hex.EncodeToString(id)) }

func bridgeWindowKey(blocks, idx uint64) []byte {
	return []byte("bridge/win/" + strconv.FormatUint(blocks, 10) + "/" + strconv.FormatUint(idx, 10))
}

type bridgeWindowRecord struct {
	Used *big.Int
}

// loadBridgeWindowUsed is what the window containing height has already released (paid or queued).
func loadBridgeWindowUsed(m *nhbstate.Manager, windowBlocks, height uint64) (*big.Int, error) {
	if windowBlocks == 0 {
		return new(big.Int), nil
	}
	var rec bridgeWindowRecord
	if _, err := m.KVGet(bridgeWindowKey(windowBlocks, height/windowBlocks), &rec); err != nil {
		return nil, err
	}
	if rec.Used == nil {
		return new(big.Int), nil
	}
	return rec.Used, nil
}

func (sp *StateProcessor) bridgeBurnIDSpent(m *nhbstate.Manager, id []byte) (bool, error) {
	var rel bridgeReleaseRecord
	if found, err := m.KVGet(bridgeReleaseKey(id), &rel); err != nil || found {
		return found, err
	}
	var pend bridgePendingRecord
	return m.KVGet(bridgePendingKey(id), &pend)
}

func bridgeBurnIDOK(id []byte) error {
	if len(id) == 0 || len(id) > 64 {
		return fmt.Errorf("%w: the burn id must be 1 to 64 bytes", ErrBridgeInvalidPayload)
	}
	return nil
}

func bridgeAddrString(raw []byte) string {
	return crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), raw...)).String()
}

// applyBridgeReleaseTransaction runs one operation of a TxTypeBridgeRelease: a signed release (paid at once or queued),
// the execution of a queued one, a cancel, a pause or an unpause.
func (sp *StateProcessor) applyBridgeReleaseTransaction(tx *types.Transaction, sender []byte) error {
	var payload bridgeReleasePayload
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrBridgeInvalidPayload, err)
	}
	if tx.Value != nil && tx.Value.Sign() != 0 {
		return fmt.Errorf("%w: a release names its amount in the payload and carries no value", ErrBridgeInvalidPayload)
	}
	height := sp.blockHeight()
	params := sp.bridge.paramsAt(height)
	if params == nil {
		return fmt.Errorf("%w: %d", ErrUnknownTransactionType, tx.Type)
	}
	switch payload.Op {
	case BridgeOpRelease:
		return sp.bridgeSignedRelease(payload, params, height, sender)
	case BridgeOpExecute:
		return sp.bridgeExecuteRelease(payload, height, sender)
	case BridgeOpCancel, BridgeOpPause, BridgeOpUnpause:
		if !bytes.Equal(sender, params.Guardian[:]) {
			return ErrBridgeNotGuardian
		}
		switch payload.Op {
		case BridgeOpCancel:
			return sp.bridgeCancelRelease(payload, sender)
		default:
			return sp.bridgeSetPaused(payload.Op == BridgeOpPause, height, sender)
		}
	}
	return fmt.Errorf("%w: unknown operation %d", ErrBridgeInvalidPayload, payload.Op)
}

func (sp *StateProcessor) bridgeSignedRelease(payload bridgeReleasePayload, params *BridgeParams, height uint64, sender []byte) error {
	if err := bridgeBurnIDOK(payload.BurnID); err != nil {
		return err
	}
	if len(payload.Recipient) != 20 {
		return fmt.Errorf("%w: the recipient must be 20 bytes", ErrBridgeInvalidPayload)
	}
	amount := payload.Amount
	if amount == nil || amount.Sign() <= 0 || amount.BitLen() > 256 {
		return fmt.Errorf("%w: the amount must be positive", ErrBridgeInvalidPayload)
	}
	escrowAddr := bridgeEscrowAddress()
	if sp.bridgeIsAdminWallet(payload.Recipient) || bytes.Equal(payload.Recipient, escrowAddr) {
		return ErrBridgeAdminWallet
	}
	manager := nhbstate.NewManager(sp.Trie)
	if pause, err := loadBridgePause(manager); err != nil {
		return err
	} else if pause.Paused {
		return ErrBridgePaused
	}
	if amount.Cmp(params.MaxReleaseWei) > 0 {
		return ErrBridgeAboveMaxRelease
	}
	if spent, err := sp.bridgeBurnIDSpent(manager, payload.BurnID); err != nil {
		return err
	} else if spent {
		return ErrBridgeAlreadyReleased
	}
	digest, err := BridgeReleaseDigest(bridgeNetworkID(sp), payload.BurnID, payload.Recipient, amount)
	if err != nil {
		return err
	}
	if err := verifyBridgeSignatures(params, digest, payload.Signatures); err != nil {
		return err
	}
	used, err := loadBridgeWindowUsed(manager, params.WindowBlocks, height)
	if err != nil {
		return err
	}
	if new(big.Int).Add(used, amount).Cmp(params.WindowCapWei) > 0 {
		return ErrBridgeWindowCap
	}
	totals, err := loadBridgeTotals(manager)
	if err != nil {
		return err
	}
	escrow, err := sp.getAccount(escrowAddr)
	if err != nil {
		return err
	}
	if znhbBalanceOf(escrow).Cmp(amount) < 0 {
		return ErrBridgeEscrowShort
	}
	// A release can never exceed what was locked, not yet released and not already spoken for by a waiting release,
	// whatever the escrow holds (a plain transfer to the escrow address is a gift to the bridge, not a lock).
	free := new(big.Int).Sub(new(big.Int).Sub(totals.Locked, totals.Released), totals.Pending)
	if free.Cmp(amount) < 0 {
		return ErrBridgeEscrowShort
	}

	// Everything is checked; now write.
	if err := manager.KVPut(bridgeWindowKey(params.WindowBlocks, height/params.WindowBlocks), bridgeWindowRecord{Used: new(big.Int).Add(used, amount)}); err != nil {
		return err
	}
	if params.DelayAboveWei != nil && amount.Cmp(params.DelayAboveWei) > 0 {
		pend := bridgePendingRecord{
			Recipient: append([]byte(nil), payload.Recipient...), Amount: new(big.Int).Set(amount),
			QueuedAt: height, ExecutableAt: height + params.DelayBlocks,
		}
		if err := manager.KVPut(bridgePendingKey(payload.BurnID), pend); err != nil {
			return err
		}
		totals.Pending = new(big.Int).Add(totals.Pending, amount)
		if err := manager.KVPut([]byte("bridge/totals"), totals); err != nil {
			return err
		}
		sp.bridgeEvent("bridge.release.queued", map[string]string{
			"burnId":       hex.EncodeToString(payload.BurnID),
			"recipient":    bridgeAddrString(payload.Recipient),
			"amountWei":    amount.String(),
			"executableAt": strconv.FormatUint(pend.ExecutableAt, 10),
		})
		return sp.incrementNativeAccountNonce(sender)
	}
	if err := sp.bridgePay(manager, totals, payload.BurnID, payload.Recipient, amount, false); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(sender)
}

// bridgePay moves amount from the escrow to the recipient, spends the burn id and books the release. A waiting release
// being executed is taken out of Pending first.
func (sp *StateProcessor) bridgePay(manager *nhbstate.Manager, totals bridgeTotals, burnID, recipientAddr []byte, amount *big.Int, wasPending bool) error {
	escrowAddr := bridgeEscrowAddress()
	escrow, err := sp.getAccount(escrowAddr)
	if err != nil {
		return err
	}
	escrow.BalanceZNHB = new(big.Int).Sub(znhbBalanceOf(escrow), amount)
	if err := sp.setAccount(escrowAddr, escrow); err != nil {
		return err
	}
	recipient, err := sp.getAccount(recipientAddr)
	if err != nil {
		return err
	}
	recipient.BalanceZNHB = new(big.Int).Add(znhbBalanceOf(recipient), amount)
	if err := sp.setAccount(recipientAddr, recipient); err != nil {
		return err
	}
	rec := bridgeReleaseRecord{Recipient: append([]byte(nil), recipientAddr...), Amount: new(big.Int).Set(amount), Height: sp.blockHeight(), Status: bridgeStatusPaid}
	if err := manager.KVPut(bridgeReleaseKey(burnID), rec); err != nil {
		return err
	}
	if wasPending {
		if err := manager.KVDelete(bridgePendingKey(burnID)); err != nil {
			return err
		}
		totals.Pending = new(big.Int).Sub(totals.Pending, amount)
	}
	totals.Released = new(big.Int).Add(totals.Released, amount)
	if err := manager.KVPut([]byte("bridge/totals"), totals); err != nil {
		return err
	}
	attrs := map[string]string{
		"burnId":    hex.EncodeToString(burnID),
		"recipient": bridgeAddrString(recipientAddr),
		"amountWei": amount.String(),
	}
	if wasPending {
		attrs["executed"] = "true" // a release that waited out its delay; an immediate one carries no such attribute
	}
	sp.bridgeEvent("bridge.released", attrs)
	return nil
}

// bridgeExecuteRelease pays a waiting release once its time has come. Anyone may send it: it can pay only the recipient and
// the amount that were signed for.
func (sp *StateProcessor) bridgeExecuteRelease(payload bridgeReleasePayload, height uint64, sender []byte) error {
	if err := bridgeBurnIDOK(payload.BurnID); err != nil {
		return err
	}
	manager := nhbstate.NewManager(sp.Trie)
	if pause, err := loadBridgePause(manager); err != nil {
		return err
	} else if pause.Paused {
		return ErrBridgePaused
	}
	var pend bridgePendingRecord
	if found, err := manager.KVGet(bridgePendingKey(payload.BurnID), &pend); err != nil {
		return err
	} else if !found {
		return ErrBridgeNotPending
	}
	if height < pend.ExecutableAt {
		return ErrBridgeNotYet
	}
	totals, err := loadBridgeTotals(manager)
	if err != nil {
		return err
	}
	escrow, err := sp.getAccount(bridgeEscrowAddress())
	if err != nil {
		return err
	}
	if znhbBalanceOf(escrow).Cmp(pend.Amount) < 0 {
		return ErrBridgeEscrowShort
	}
	if err := sp.bridgePay(manager, totals, payload.BurnID, pend.Recipient, pend.Amount, true); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(sender)
}

// bridgeCancelRelease drops a waiting release. The burn id stays spent and the ZNHB stays in the escrow.
func (sp *StateProcessor) bridgeCancelRelease(payload bridgeReleasePayload, sender []byte) error {
	if err := bridgeBurnIDOK(payload.BurnID); err != nil {
		return err
	}
	manager := nhbstate.NewManager(sp.Trie)
	var pend bridgePendingRecord
	if found, err := manager.KVGet(bridgePendingKey(payload.BurnID), &pend); err != nil {
		return err
	} else if !found {
		return ErrBridgeNotPending
	}
	totals, err := loadBridgeTotals(manager)
	if err != nil {
		return err
	}
	if err := manager.KVDelete(bridgePendingKey(payload.BurnID)); err != nil {
		return err
	}
	rec := bridgeReleaseRecord{Recipient: pend.Recipient, Amount: pend.Amount, Height: sp.blockHeight(), Status: bridgeStatusCancelled}
	if err := manager.KVPut(bridgeReleaseKey(payload.BurnID), rec); err != nil {
		return err
	}
	totals.Pending = new(big.Int).Sub(totals.Pending, pend.Amount)
	if err := manager.KVPut([]byte("bridge/totals"), totals); err != nil {
		return err
	}
	sp.bridgeEvent("bridge.release.cancelled", map[string]string{
		"burnId":    hex.EncodeToString(payload.BurnID),
		"recipient": bridgeAddrString(pend.Recipient),
		"amountWei": pend.Amount.String(),
	})
	return sp.incrementNativeAccountNonce(sender)
}

func (sp *StateProcessor) bridgeSetPaused(paused bool, height uint64, sender []byte) error {
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.KVPut(bridgePauseKey(), bridgePauseRecord{Paused: paused, Height: height}); err != nil {
		return err
	}
	kind := "bridge.unpaused"
	if paused {
		kind = "bridge.paused"
	}
	sp.bridgeEvent(kind, map[string]string{"height": strconv.FormatUint(height, 10)})
	return sp.incrementNativeAccountNonce(sender)
}
