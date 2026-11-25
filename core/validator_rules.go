package core

import (
	"bytes"
	"math/big"
	"reflect"

	"nhbchain/config"
	"nhbchain/core/types"
)

// ValidatorRules groups the chain rules of the validator-set, slashing and staking
// package (ledger PL-R5T-25, 26, 09, 27, PL-VAL-01 and the POTSO treasury). Each field
// is an activation height, off at 0 (the shipped and live value) and read from the
// config setting of the same name. A chain rule: the same value on every validator,
// above the chain tip, with all of them restarted together
// (docs/consensus/activation-policy.md).
//
// The group is carried as one value by the node, the state processor and its copies,
// so a rule added here cannot be left out of a copy: a test sets every field and checks
// each of them survives the copy and the rebuild.
type ValidatorRules struct {
	// SlashEligibilityRefreshActivationHeight (PL-R5T-25): from the height, a validator
	// slashed for equivocation has its eligibility entry and its place in the validator
	// set re-evaluated in the block that slashes it, instead of keeping the stake it had
	// until its account is next written.
	SlashEligibilityRefreshActivationHeight uint64

	// SlashPendingUnbondsActivationHeight (PL-R5T-26 part 1): from the height, a
	// proven equivocation also forfeits the offender's own unclaimed unbonds (the ones
	// it made out of its stake in itself), after its locked balance, and the slash is
	// sized on its stake plus those unbonds. Before, ZNHB moved into an unbond before
	// the report landed was out of reach and the offense was still marked penalised
	// with a slash of zero. Stake other wallets delegated to the validator is not
	// reached: that is the owner's decision (docs/consensus/invariants.md).
	SlashPendingUnbondsActivationHeight uint64

	// EvidenceIntakeGuardActivationHeight (PL-R5T-09): from the height the evidence
	// intake refuses reports that fill its 256 slots without a proof behind them: a
	// DOWNTIME or INVALID_BLOCK_PROPOSAL report (ErrEvidenceTypeNotAccepted), an
	// EQUIVOCATION report whose offender holds less than the minimum validator stake in
	// staking stake plus its own pending unbonds (ErrEvidenceOffenderNotBonded), and a
	// report whose reporter's staking stake, not its locked ZNHB, is under the minimum
	// (ErrEvidenceReporterNotBonded).
	EvidenceIntakeGuardActivationHeight uint64

	// StakeAPRFromStateActivationHeight (PL-R5T-27): from the height the reward rate
	// used when a stake, unstake or claim executes is the on-chain staking.aprBps read
	// at execution (the fixed default 1250 when none is stored or it cannot be read),
	// never the value cached at start-up from the node's own config. A change of the
	// parameter is settled at the old rate in the block that executes it.
	StakeAPRFromStateActivationHeight uint64

	// ValidatorAdmissionGuardActivationHeight and ValidatorAdmissionProbationSeconds
	// (PL-VAL-01): from the height a validator that is new to the set that votes is
	// admitted at an epoch boundary only while the established validators (in the set for
	// the whole probation, or in it when the guard started) hold strictly more than two
	// thirds of the power of the established validators plus the newcomers. Nothing is
	// removed by the rule. A probation of zero leaves the guard without effect.
	ValidatorAdmissionGuardActivationHeight uint64
	ValidatorAdmissionProbationSeconds      uint64

	// PotsoTreasuryShortfallActivationHeight (PL-DC-29): from the height a POTSO reward
	// epoch that the reward treasury cannot cover is detected before anything is written
	// and degrades to a recorded zero payout, instead of quietly paying a capped epoch.
	PotsoTreasuryShortfallActivationHeight uint64

	// DelegatorHoldWeightActivationHeight (PL-EXT-25): from the height each indexed
	// delegator's share of a validator's staker pool is weighed by its locked ZNHB less the
	// ZNHB its own pending POS authorizations hold, instead of by the whole locked balance
	// (a hold moves ZNHB into that balance, refundably). A delegator whose locked balance
	// is all held counts for nothing. See splitStakeShare.
	DelegatorHoldWeightActivationHeight uint64

	// EscrowEnvelopeChainBindingActivationHeight (PL-EXT-07): from the height the signed
	// payload of a delegated escrow create, release, refund or dispute and the decision
	// of an arbitration quorum must carry the network's own chain id ("chainId", a
	// decimal string), and one that does not, or that carries another network's, is
	// refused. Until then those payloads signed for one network verify on every other
	// network that holds an escrow with the same id. See core/escrow_envelope_chain.go.
	EscrowEnvelopeChainBindingActivationHeight uint64

	// PotsoAbuseGuardsActivationHeight (PL-EXT-39, PL-PO-01): from the height the five
	// POTSO abuse-guard values of the node's own file are applied as configured:
	// [potso.weights] MinStakeToWinWei and MinEngagementToWin, [potso.abuse]
	// MinStakeToEarnWei, QuadraticTxDampenAfter and MaxUserShareBps. Before it they are
	// applied as 0 whatever the file says, so an operator can set them
	// on every validator ahead of the height and the history already on the chain stays
	// replayable. 0 = never, the values are applied as 0, which is what both live
	// validators run with (all five are 0 in their files).
	PotsoAbuseGuardsActivationHeight uint64

	// HeartbeatTimestampBoundActivationHeight (PL-EXT-71): from the height a heartbeat
	// transaction whose payload timestamp is more than two minutes after the block's own
	// time is refused (a timestamp of 0 means the block time and is never refused). Before
	// it any timestamp is accepted: one dated far in the future makes every validator walk
	// the engagement rollover one day at a time, with an event for each day. 0 = never,
	// which is what both live validators run with. See applyHeartbeat.
	HeartbeatTimestampBoundActivationHeight uint64

	// LendingClosedDayClampActivationHeight (PL-EXT-83): from the height the next cycle of a
	// fixed-term loan's auto-debit or of a fixed-term deposit's payout is put on the day of
	// the scheduling block when its own due day is already behind it. The settlement scans
	// the days after the last one it closed up to today and never goes back, so before the
	// height such a cycle sits on a closed day and is never attempted. 0 = never, which is
	// what both live validators run with. See lendingDueDay.
	LendingClosedDayClampActivationHeight uint64

	// SaleReferenceActivationHeight, SaleReferenceMaxAgeSeconds and
	// SaleReferenceMaxStepBps (PL-BC-D): from the height the price of every sale from the
	// treasury Sale Pool is the higher of the Genesis curve's cost and the latest signed
	// outside reference price times the amount; a missing reference, or one older than the
	// maximum age, pauses sales. The step bound refuses a reference that moves further
	// than that many basis points from a fresh previous one (0 = no bound). See
	// core/sale_reference.go.
	SaleReferenceActivationHeight uint64
	SaleReferenceMaxAgeSeconds    uint64
	SaleReferenceMaxStepBps       uint64
}

// heartbeatMaxAheadSeconds is how far after the block's own time a heartbeat's timestamp may
// be once HeartbeatTimestampBoundActivationHeight has passed (two minutes, the same skew the
// validator activation check uses for a validator's last heartbeat).
const heartbeatMaxAheadSeconds = 120

// ruleActiveAt reports whether a rule whose first height is from applies at height. A
// zero from is off at every height.
func ruleActiveAt(from, height uint64) bool {
	return from != 0 && height >= from
}

// ValidatorRulesFromConfig reads the rules from the settings of the same names. A
// test fails when a field of ValidatorRules has no setting of its name in the config.
func ValidatorRulesFromConfig(cfg *config.Config) ValidatorRules {
	var rules ValidatorRules
	if cfg == nil {
		return rules
	}
	src := reflect.ValueOf(*cfg)
	dst := reflect.ValueOf(&rules).Elem()
	for i := 0; i < dst.NumField(); i++ {
		field := src.FieldByName(dst.Type().Field(i).Name)
		if field.IsValid() && field.Kind() == reflect.Uint64 {
			dst.Field(i).SetUint(field.Uint())
		}
	}
	return rules
}

// SetValidatorRules sets the rules. Zero values leave every rule off.
func (sp *StateProcessor) SetValidatorRules(rules ValidatorRules) {
	if sp == nil {
		return
	}
	sp.validatorRules = rules
}

// ValidatorRulesInForce returns the rules the processor was given.
func (sp *StateProcessor) ValidatorRulesInForce() ValidatorRules {
	if sp == nil {
		return ValidatorRules{}
	}
	return sp.validatorRules
}

// SetValidatorRules sets the rules on the node and on its state processor. A rebuilt
// state processor carries them (rebuildStateProcessorLocked).
func (n *Node) SetValidatorRules(rules ValidatorRules) {
	if n == nil {
		return
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	n.validatorRules = rules
	if n.state != nil {
		n.state.SetValidatorRules(rules)
	}
}

// refreshValidatorEligibility re-evaluates a validator's place after its stake was
// changed outside setAccount (PL-R5T-25): it reads the account and writes it back
// through setAccount, the one place the eligible-validator map and the validator set
// are recomputed. An address that is in neither map is left alone, so slashing an
// ordinary account writes nothing new.
func (sp *StateProcessor) refreshValidatorEligibility(addr []byte) error {
	key := string(addr)
	_, eligible := sp.EligibleValidators[key]
	_, voting := sp.ValidatorSet[key]
	if !eligible && !voting {
		return nil
	}
	account, err := sp.getAccount(addr)
	if err != nil {
		return err
	}
	return sp.setAccount(addr, account)
}

// ownPendingUnbonds is the total of the account's pending unbonds that were made out
// of its stake in itself (entry.Validator is the account): the part of a validator's
// own bond that is on its way out and that a slash can still reach
// (SlashPendingUnbondsActivationHeight).
func ownPendingUnbonds(account *types.Account, addr [20]byte) *big.Int {
	total := big.NewInt(0)
	if account == nil {
		return total
	}
	for _, entry := range account.PendingUnbonds {
		if entry.Amount != nil && entry.Amount.Sign() > 0 && bytes.Equal(entry.Validator, addr[:]) {
			total.Add(total, entry.Amount)
		}
	}
	return total
}
