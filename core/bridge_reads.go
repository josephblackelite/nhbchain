package core

import (
	"encoding/hex"
	"fmt"
	"math/big"

	nhbstate "nhbchain/core/state"
)

// Read-only views of single bridge locks and release records (ledger PL-BC-C): what was locked, for which destination,
// and what became of a burn id (paid, waiting, cancelled, or never recorded). Nothing here writes, takes part in
// consensus or adds a setting, so the settings digest and every state root are unchanged.

const (
	// BridgeListLocksDefault is how many locks a listing returns when the caller names no limit.
	BridgeListLocksDefault = 100
	// BridgeListLocksMax is the most locks one listing returns, whatever the caller asks for.
	BridgeListLocksMax = 200
)

// What became of a burn id, as BridgeRelease reports it.
const (
	BridgeReleaseNone      = "none"      // no release was ever recorded for the id: it is not spent
	BridgeReleasePaid      = "paid"      // the ZNHB was paid to the recipient
	BridgeReleasePending   = "pending"   // queued and waiting out its delay
	BridgeReleaseCancelled = "cancelled" // cancelled by the guardian: the id stays spent, the ZNHB stays in the escrow
)

// BridgeLockView is one lock, as the chain recorded it when the ZNHB entered the escrow.
type BridgeLockView struct {
	ID        uint64
	Sender    string // the sender's nhb1 account
	AmountWei *big.Int
	DestChain string // eip155:<id>
	Dest      string // the 0x address on that chain, lower case as stored
	Height    uint64 // the block the lock was applied in
}

// BridgeReleaseView is what is on record for one burn id.
type BridgeReleaseView struct {
	BurnID       string // hex
	Status       string // one of the BridgeRelease* values
	Recipient    string // nhb1 account; empty for none
	AmountWei    *big.Int
	Height       uint64 // paid or cancelled: the block of the record; pending: the block it was queued in
	ExecutableAt uint64 // pending only: the first block anyone may execute it
}

func bridgeLockView(m *nhbstate.Manager, id uint64) (*BridgeLockView, error) {
	if id == 0 {
		return nil, nil // ids start at 1
	}
	var rec bridgeLockRecord
	found, err := m.KVGet(bridgeLockKey(id), &rec)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	amount := new(big.Int)
	if rec.Amount != nil {
		amount.Set(rec.Amount)
	}
	return &BridgeLockView{ID: id, Sender: bridgeAddrString(rec.Sender), AmountWei: amount, DestChain: rec.DestChain, Dest: rec.Dest, Height: rec.Height}, nil
}

// BridgeLock reads lock id. It returns nil, nil when there is no such lock.
func (sp *StateProcessor) BridgeLock(id uint64) (*BridgeLockView, error) {
	return bridgeLockView(nhbstate.NewManager(sp.Trie), id)
}

// BridgeLocksAfter lists the locks with an id above after, in id order, at most limit of them (0 or less means
// BridgeListLocksDefault, more than BridgeListLocksMax means BridgeListLocksMax). lastID is the id of the newest lock
// (0 when there is none), so a caller knows whether more are waiting.
func (sp *StateProcessor) BridgeLocksAfter(after uint64, limit int) (locks []BridgeLockView, lastID uint64, err error) {
	manager := nhbstate.NewManager(sp.Trie)
	lastID, err = loadBridgeSeq(manager)
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = BridgeListLocksDefault
	}
	if limit > BridgeListLocksMax {
		limit = BridgeListLocksMax
	}
	locks = []BridgeLockView{}
	if after >= lastID { // also keeps after+1 from wrapping around
		return locks, lastID, nil
	}
	for id := after + 1; id <= lastID && len(locks) < limit; id++ {
		v, err := bridgeLockView(manager, id)
		if err != nil {
			return nil, 0, err
		}
		if v == nil {
			// Ids are assigned one after the other and never deleted: a hole means the state is damaged.
			return nil, 0, fmt.Errorf("bridge: lock %d is missing although %d locks were assigned", id, lastID)
		}
		locks = append(locks, *v)
	}
	return locks, lastID, nil
}

// BridgeRelease reports what is on record for a burn id: paid, waiting, cancelled, or nothing (the id is not spent).
func (sp *StateProcessor) BridgeRelease(burnID []byte) (*BridgeReleaseView, error) {
	if err := bridgeBurnIDOK(burnID); err != nil {
		return nil, err
	}
	manager := nhbstate.NewManager(sp.Trie)
	view := &BridgeReleaseView{BurnID: hex.EncodeToString(burnID), Status: BridgeReleaseNone}
	var rel bridgeReleaseRecord
	found, err := manager.KVGet(bridgeReleaseKey(burnID), &rel)
	if err != nil {
		return nil, err
	}
	if found {
		switch rel.Status {
		case bridgeStatusPaid:
			view.Status = BridgeReleasePaid
		case bridgeStatusCancelled:
			view.Status = BridgeReleaseCancelled
		default:
			return nil, fmt.Errorf("bridge: the record of burn id %x has the unknown status %d", burnID, rel.Status)
		}
		view.Recipient = bridgeAddrString(rel.Recipient)
		view.AmountWei = new(big.Int)
		if rel.Amount != nil {
			view.AmountWei.Set(rel.Amount)
		}
		view.Height = rel.Height
		return view, nil
	}
	var pend bridgePendingRecord
	found, err = manager.KVGet(bridgePendingKey(burnID), &pend)
	if err != nil {
		return nil, err
	}
	if found {
		view.Status = BridgeReleasePending
		view.Recipient = bridgeAddrString(pend.Recipient)
		view.AmountWei = new(big.Int)
		if pend.Amount != nil {
			view.AmountWei.Set(pend.Amount)
		}
		view.Height = pend.QueuedAt
		view.ExecutableAt = pend.ExecutableAt
	}
	return view, nil
}

// BridgeLock reads one lock of the node's current state (nil when there is none).
func (n *Node) BridgeLock(id uint64) (*BridgeLockView, error) {
	sp, err := n.readProcessor()
	if err != nil {
		return nil, err
	}
	return sp.BridgeLock(id)
}

// BridgeLocksAfter lists locks of the node's current state; see StateProcessor.BridgeLocksAfter.
func (n *Node) BridgeLocksAfter(after uint64, limit int) ([]BridgeLockView, uint64, error) {
	sp, err := n.readProcessor()
	if err != nil {
		return nil, 0, err
	}
	return sp.BridgeLocksAfter(after, limit)
}

// BridgeRelease reports what is on record for a burn id in the node's current state.
func (n *Node) BridgeRelease(burnID []byte) (*BridgeReleaseView, error) {
	sp, err := n.readProcessor()
	if err != nil {
		return nil, err
	}
	return sp.BridgeRelease(burnID)
}
