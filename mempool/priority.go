package mempool

import (
	"math"

	"nhbchain/consensus"
	"nhbchain/core/types"
)

// Lanes groups transactions into the POS-priority and normal scheduling queues.
type Lanes struct {
	POS    []*types.Transaction
	Normal []*types.Transaction
}

const (
	assetLabelNHB   = "nhb"
	assetLabelZNHB  = "znhb"
	assetLabelOther = "other"
)

// IsPOSLaneEligible reports whether the transaction should be routed through
// the POS-priority lane. Only NHB and ZNHB transfers carrying a POS intent
// qualify for the reserved scheduling window.
func IsPOSLaneEligible(tx *types.Transaction) bool {
	if tx == nil || len(tx.IntentRef) == 0 {
		return false
	}
	switch tx.Type {
	case types.TxTypeTransfer, types.TxTypeTransferZNHB:
		return true
	default:
		return false
	}
}

func assetLabel(tx *types.Transaction) string {
	if tx == nil {
		return assetLabelOther
	}
	switch tx.Type {
	case types.TxTypeTransfer:
		return assetLabelNHB
	case types.TxTypeTransferZNHB:
		return assetLabelZNHB
	default:
		return assetLabelOther
	}
}

// Classify separates transactions into POS-priority and normal lanes based on
// whether they carry an intent reference.
func Classify(txs []*types.Transaction) Lanes {
	lanes := Lanes{POS: make([]*types.Transaction, 0, len(txs)), Normal: make([]*types.Transaction, 0, len(txs))}
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if IsPOSLaneEligible(tx) {
			lanes.POS = append(lanes.POS, tx)
			continue
		}
		lanes.Normal = append(lanes.Normal, tx)
	}
	return lanes
}

// Usage captures how much of the reserved POS lane capacity is expected to be
// consumed for an upcoming proposal.
type Usage struct {
	// Target is the number of slots that should be reserved for POS
	// transactions based on the configured quota.
	Target int
	// Used is the actual number of POS transactions scheduled inside the
	// reserved window.
	Used int
	// TotalPOS is the total number of POS transactions currently pending.
	TotalPOS int
	// POSByAsset captures the pending POS lane backlog segmented by asset
	// label (e.g. nhb, znhb).
	POSByAsset map[string]int
}

// Schedule interleaves the classified transactions so that the first maxTxs
// entries respect the POS reservation policy. The returned slice contains all
// transactions with the prioritized ordering applied.
func Schedule(lanes Lanes, maxTxs int, quota consensus.POSQuota) ([]*types.Transaction, Usage) {
	total := len(lanes.POS) + len(lanes.Normal)
	if total == 0 {
		return nil, Usage{}
	}

	if maxTxs <= 0 || maxTxs > total {
		maxTxs = total
	}

	target := quota.ReservedSlots(maxTxs)
	if target > maxTxs {
		target = maxTxs
	}

	posTake := int(math.Min(float64(target), float64(len(lanes.POS))))
	normalTake := maxTxs - posTake
	if normalTake > len(lanes.Normal) {
		normalTake = len(lanes.Normal)
	}

	remaining := maxTxs - (posTake + normalTake)
	if remaining > 0 {
		extraPos := len(lanes.POS) - posTake
		if extraPos > 0 {
			take := remaining
			if take > extraPos {
				take = extraPos
			}
			posTake += take
			remaining -= take
		}
		if remaining > 0 {
			extraNormal := len(lanes.Normal) - normalTake
			if extraNormal > 0 {
				take := remaining
				if take > extraNormal {
					take = extraNormal
				}
				normalTake += take
				remaining -= take
			}
		}
	}

	ordered := make([]*types.Transaction, 0, total)
	ordered = append(ordered, lanes.POS[:posTake]...)
	ordered = append(ordered, lanes.Normal[:normalTake]...)
	ordered = append(ordered, lanes.POS[posTake:]...)
	ordered = append(ordered, lanes.Normal[normalTake:]...)

	breakdown := make(map[string]int, len(lanes.POS))
	for _, tx := range lanes.POS {
		breakdown[assetLabel(tx)]++
	}

	usage := Usage{
		Target:     target,
		Used:       posTake,
		TotalPOS:   len(lanes.POS),
		POSByAsset: breakdown,
	}
	return ordered, usage
}
