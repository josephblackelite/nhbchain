package core

import (
	"fmt"
	"math/big"
	"strings"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/fees"
	"nhbchain/native/governance"
)

// W11: one fee policy across the whole chain, under governance.
//
// Two chain rules, both off (zero) unless a height is configured:
//
//   - UniformTransferFeeActivationHeight. From the height a transfer that names a fee
//     domain (the pos, p2p and otc domains of [global.Fees]) pays exactly what an ordinary
//     transfer pays: the same free allowance, the same rate, never the domain's own
//     merchant rate on top. The fee still goes to the domain's route wallet when one is
//     configured for the asset. See applyUniformDomainFee.
//
//   - TransferFeeGovernanceActivationHeight (with TransferFeeGovernanceDelayBlocks). From
//     the height a passed policy.feeParams proposal can set the NHB and ZNHB rates and the
//     free allowance, and the schedule it stores, once its own height is reached, replaces
//     the configured values (effectiveTransferGasPolicy).
//
// Neither touches the fee collector, the allowance window or the monthly free tier.

// SetUniformTransferFee sets the first block height of the UniformTransferFeeActivationHeight
// rule. Zero, the default, leaves the domain fee as it is at every height. A chain rule: the
// same value on every validator, set before the chain reaches the height.
func (sp *StateProcessor) SetUniformTransferFee(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.uniformFeeFromHeight = fromHeight
}

// SetTransferFeeGovernance sets the first block height of the
// TransferFeeGovernanceActivationHeight rule and the configured delay, in blocks, between
// the block that executes a fee proposal and the block that first charges its schedule
// (raised to governance.MinFeeScheduleDelayBlocks when lower). Zero for the height, the
// default, leaves the configured fees in force at every height and the proposal kind
// unavailable. A chain rule: the same values on every validator.
func (sp *StateProcessor) SetTransferFeeGovernance(fromHeight, delayBlocks uint64) {
	if sp == nil {
		return
	}
	sp.feeGovFromHeight = fromHeight
	sp.feeGovDelayBlocks = delayBlocks
}

func (sp *StateProcessor) uniformFeeActive() bool {
	return sp != nil && sp.uniformFeeFromHeight != 0 && sp.blockHeight() >= sp.uniformFeeFromHeight
}

func (sp *StateProcessor) feeGovernanceActive() bool {
	return sp != nil && sp.feeGovFromHeight != 0 && sp.blockHeight() >= sp.feeGovFromHeight
}

// feeScheduleHost is the block context the proposal engine needs for policy.feeParams.
func (sp *StateProcessor) feeScheduleHost() governance.FeeScheduleHost {
	if sp == nil {
		return governance.FeeScheduleHost{}
	}
	return governance.FeeScheduleHost{Enabled: sp.feeGovernanceActive(), Height: sp.blockHeight(), DelayBlocks: sp.feeGovDelayBlocks}
}

// governedFeeScheduleAt reads the schedule record from the parameter store and returns the
// schedule that governs a block at the given height, nil when governance has set none or the
// stored value is not an acceptable record (an unreadable record is no record, on every
// validator alike).
func governedFeeScheduleAt(manager *nhbstate.Manager, height uint64) (*governance.ScheduledFeeValues, error) {
	raw, ok, err := manager.ParamStoreGet(governance.ParamKeyTransferFeeSchedule)
	if err != nil {
		return nil, fmt.Errorf("fees: read transfer fee schedule: %w", err)
	}
	if !ok {
		return nil, nil
	}
	record, perr := governance.ParseFeeScheduleRecord(raw)
	if perr != nil || record == nil {
		return nil, nil
	}
	return record.InForce(height), nil
}

// overlayGovernedFees returns base with the governed schedule's rates and allowance in
// place of the configured ones. The collector and the window stay as configured. The policy is
// enabled exactly when the allowance is positive, as buildTransferGasPolicyFromConfig makes it.
func overlayGovernedFees(base TransferGasPolicy, schedule *governance.ScheduledFeeValues) TransferGasPolicy {
	if schedule == nil {
		return base
	}
	out := base.Clone()
	out.FeeBps = schedule.NHBFeeBps
	out.FeeBpsZNHB = schedule.ZNHBFeeBps
	out.FreeSpendLimitWei = schedule.Allowance()
	out.Enabled = out.FreeSpendLimitWei.Sign() > 0
	return out
}

// effectiveTransferGasPolicy is the transfer policy that governs the block being executed:
// the configured one until TransferFeeGovernanceActivationHeight, and from it the configured
// one with the governed schedule in force at this height laid over it. With the rule off no
// state is read.
func (sp *StateProcessor) effectiveTransferGasPolicy() (TransferGasPolicy, error) {
	base := sp.TransferGasPolicy()
	if !sp.feeGovernanceActive() {
		return base, nil
	}
	schedule, err := governedFeeScheduleAt(nhbstate.NewManager(sp.Trie), sp.blockHeight())
	if err != nil {
		return TransferGasPolicy{}, err
	}
	return overlayGovernedFees(base, schedule), nil
}

// uniformFeeRouting says how a transfer that names a fee domain is treated once
// UniformTransferFeeActivationHeight is reached.
type uniformFeeRouting struct {
	domain string
	// route is the domain's wallet for the asset; the zero address when the domain has none,
	// in which case the fee goes to the transfer fee collector as for any transfer.
	route [20]byte
}

// uniformFeeRouting returns the routing for tx, nil when the transfer is treated as before: the
// rule is not in force at this height, the transfer names no domain, or the domain is not one
// the node has a policy for (the same cases in which applyTransactionFee charged nothing).
func (sp *StateProcessor) uniformFeeRouting(tx *types.Transaction, asset string) *uniformFeeRouting {
	if tx == nil || !sp.uniformFeeActive() {
		return nil
	}
	domain := strings.TrimSpace(tx.MerchantAddress)
	if domain == "" {
		return nil
	}
	cfg, ok := sp.feePolicy.DomainConfig(domain)
	if !ok {
		return nil
	}
	routing := &uniformFeeRouting{domain: domain}
	if assetCfg, ok := cfg.AssetConfig(asset); ok {
		routing.route = assetCfg.OwnerWallet
	}
	return routing
}

// applyUniformDomainFee is applyTransactionFee for a transfer under the uniform rule. The
// transfer's own fee has already been debited from the sender at the ordinary rate, after the
// ordinary free allowance; nothing more is charged for naming a domain. charged is that debited
// fee (zero while the transfer was inside the allowance or a sponsor pays it). When routedHere
// is set the transfer path did not credit it to the fee collector and this call credits it to the
// domain's route wallet (through the same split and aliasing rules as a domain fee). The
// domain's fee totals and the fee event are kept as before; the domain's own free-tier counters,
// which counted the old per-domain allowance, are no longer advanced.
func (sp *StateProcessor) applyUniformDomainFee(tx *types.Transaction, sender []byte, fromAcc, toAcc *types.Account, routing *uniformFeeRouting, asset string, charged *big.Int, routedHere bool, bps uint32) error {
	if sp == nil || tx == nil || routing == nil {
		return nil
	}
	if len(sender) != 20 {
		return nil
	}
	var payer [20]byte
	copy(payer[:], sender)
	gross := big.NewInt(0)
	if tx.Value != nil {
		gross = new(big.Int).Set(tx.Value)
	}
	manager := nhbstate.NewManager(sp.Trie)
	routed := big.NewInt(0)
	if routedHere && charged != nil && charged.Sign() > 0 {
		routed = new(big.Int).Set(charged)
		if err := sp.creditDomainFee(manager, asset, routed, routing.route, sender, tx.To, fromAcc, toAcc); err != nil {
			return err
		}
	}
	net := new(big.Int).Sub(gross, routed)
	if net.Sign() < 0 {
		net = big.NewInt(0)
	}
	if err := manager.FeesAccumulateTotals(routing.domain, asset, routing.route, gross, routed, net); err != nil {
		return err
	}
	sp.AppendEvent(events.FeeApplied{
		Payer:           payer,
		Domain:          fees.NormalizeDomain(routing.domain),
		Asset:           asset,
		Gross:           new(big.Int).Set(gross),
		Fee:             new(big.Int).Set(routed),
		Net:             new(big.Int).Set(net),
		PolicyVersion:   sp.feePolicy.Version,
		OwnerWallet:     routing.route,
		FreeTierApplied: charged == nil || charged.Sign() == 0,
		FeeBasisPoints:  bps,
	}.Event())
	return nil
}
