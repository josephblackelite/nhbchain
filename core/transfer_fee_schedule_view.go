package core

import (
	nhbstate "nhbchain/core/state"
	"nhbchain/native/governance"
)

// TransferFeeScheduleEntry is one fee schedule as the read RPC shows it.
type TransferFeeScheduleEntry struct {
	NHBFeeBps        uint32
	ZNHBFeeBps       uint32
	FreeAllowanceWei string
	// Source is "config" for the values the validators carry in their configuration and
	// "governance" for a schedule a passed proposal set.
	Source string
	// ProposalID and FromHeight are set for a governed schedule: the proposal that set it and
	// the block height it governs from.
	ProposalID uint64
	FromHeight uint64
}

// TransferFeeScheduleView is what a node reports about the transfer fee schedule: the one the
// NEXT block will charge, the one waiting for its height (if any), and the two chain rules.
type TransferFeeScheduleView struct {
	NextBlockHeight uint64
	InForce         TransferFeeScheduleEntry
	Pending         *TransferFeeScheduleEntry
	// BlocksUntilPending is the number of blocks from the next block to the first block that
	// charges the pending schedule (0 when none is pending).
	BlocksUntilPending uint64

	UniformDomainFeeActive           bool
	UniformDomainFeeActivationHeight uint64

	// StrictAllowanceActive is true when the next block charges the strict free allowance (the fee is on the part of a
	// transfer above what is left of the allowance); false means a transfer is free in full while any allowance is left.
	StrictAllowanceActive           bool
	StrictAllowanceActivationHeight uint64

	GovernanceActive           bool
	GovernanceActivationHeight uint64
	// GovernanceDelayBlocks is the delay a proposal's schedule waits after its execution,
	// the configured value raised to the floor.
	GovernanceDelayBlocks uint64
	MaxFeeBps             uint32
	MaxFreeAllowanceWei   string
}

// TransferFeeSchedule reports the fee schedule in force for the next block and any pending change.
// It reads the chain state; it changes nothing.
func (n *Node) TransferFeeSchedule() (TransferFeeScheduleView, error) {
	policy := n.TransferGasPolicy()
	next := n.GetHeight() + 1
	view := TransferFeeScheduleView{
		NextBlockHeight: next,
		InForce: TransferFeeScheduleEntry{
			NHBFeeBps:        policy.FeeBps,
			ZNHBFeeBps:       policy.FeeBpsZNHB,
			FreeAllowanceWei: policy.FreeSpendLimitWei.String(),
			Source:           "config",
		},
		MaxFeeBps:           governance.MaxTransferFeeBps,
		MaxFreeAllowanceWei: governance.MaxFreeAllowanceWei().String(),
	}
	err := n.WithState(func(m *nhbstate.Manager) error {
		view.UniformDomainFeeActivationHeight = n.uniformTransferFeeHeight
		view.UniformDomainFeeActive = n.uniformTransferFeeHeight != 0 && next >= n.uniformTransferFeeHeight
		view.StrictAllowanceActivationHeight = n.transferFeeStrictAllowanceHeight
		view.StrictAllowanceActive = n.transferFeeStrictAllowanceHeight != 0 && next >= n.transferFeeStrictAllowanceHeight
		view.GovernanceActivationHeight = n.transferFeeGovHeight
		view.GovernanceActive = n.transferFeeGovHeight != 0 && next >= n.transferFeeGovHeight
		delay := n.transferFeeGovDelayBlocks
		if delay < governance.MinFeeScheduleDelayBlocks {
			delay = governance.MinFeeScheduleDelayBlocks
		}
		view.GovernanceDelayBlocks = delay
		if !view.GovernanceActive {
			return nil
		}
		raw, ok, err := m.ParamStoreGet(governance.ParamKeyTransferFeeSchedule)
		if err != nil || !ok {
			return err
		}
		record, perr := governance.ParseFeeScheduleRecord(raw)
		if perr != nil || record == nil {
			return nil
		}
		if governed := record.InForce(next); governed != nil {
			view.InForce = governedEntry(governed)
		}
		if record.Pending != nil && next < record.Pending.EffectiveHeight {
			pending := governedEntry(record.Pending)
			view.Pending = &pending
			view.BlocksUntilPending = record.Pending.EffectiveHeight - next
		}
		return nil
	})
	return view, err
}

func governedEntry(s *governance.ScheduledFeeValues) TransferFeeScheduleEntry {
	return TransferFeeScheduleEntry{
		NHBFeeBps:        s.NHBFeeBps,
		ZNHBFeeBps:       s.ZNHBFeeBps,
		FreeAllowanceWei: s.FreeAllowanceWei,
		Source:           "governance",
		ProposalID:       s.ProposalID,
		FromHeight:       s.EffectiveHeight,
	}
}

// EffectiveTransferGasPolicy is the transfer policy the NEXT block will charge: the configured one, with
// the governed schedule in force at the next height laid over it once
// TransferFeeGovernanceActivationHeight is reached. fees_getTransferQuote and fees_getTransferStatus
// read it, so a quote is the fee a transfer will really pay.
func (n *Node) EffectiveTransferGasPolicy() (TransferGasPolicy, error) {
	if n == nil {
		return TransferGasPolicy{}, nil
	}
	policy := n.TransferGasPolicy()
	next := n.GetHeight() + 1
	var schedule *governance.ScheduledFeeValues
	err := n.WithState(func(m *nhbstate.Manager) error {
		if n.transferFeeGovHeight == 0 || next < n.transferFeeGovHeight {
			return nil
		}
		var rerr error
		schedule, rerr = governedFeeScheduleAt(m, next)
		return rerr
	})
	if err != nil {
		return TransferGasPolicy{}, err
	}
	return overlayGovernedFees(policy, schedule), nil
}
