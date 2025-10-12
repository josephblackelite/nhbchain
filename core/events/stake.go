package events

import (
	"math/big"
	"strconv"
	"strings"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypeStakeDelegated captures share accrual triggered by a delegation.
	TypeStakeDelegated = "stake.delegated"
	// TypeStakeUndelegated captures share adjustments triggered by undelegation.
	TypeStakeUndelegated = "stake.undelegated"
	// TypeStakeRewardsClaimed is emitted when staking rewards are claimed and minted to an account.
	TypeStakeRewardsClaimed = "stake.rewardsClaimed"
	// TypeStakeRewardsClaimedLegacy aliases the rewards claim event for existing indexers.
	TypeStakeRewardsClaimedLegacy = "stake.claimed"
	// TypeStakeCapHit signals that the annual emission cap prevented a full payout.
	TypeStakeCapHit = "stake.emissionCapHit"
	// TypeStakeEmissionCapHit aliases the cap hit type for backwards compatibility.
	TypeStakeEmissionCapHit = TypeStakeCapHit
	// TypeStakePaused is emitted when staking mutations are rejected due to a pause toggle.
	TypeStakePaused = "stake.paused"

	// StakeOperationDelegate identifies the delegation flow.
	StakeOperationDelegate = "delegate"
	// StakeOperationUndelegate identifies the undelegation flow.
	StakeOperationUndelegate = "undelegate"
	// StakeOperationClaim identifies unbond claims.
	StakeOperationClaim = "claim"
	// StakeOperationClaimRewards identifies reward claims.
	StakeOperationClaimRewards = "claimRewards"
)

// StakeDelegated captures the share delta realised when delegating stake.
type StakeDelegated struct {
	Account     [20]byte
	SharesAdded *big.Int
	NewShares   *big.Int
	LastIndex   *big.Int
	Validator   [20]byte
	Amount      *big.Int
	Locked      *big.Int
}

// EventType satisfies the Event interface.
func (StakeDelegated) EventType() string { return TypeStakeDelegated }

// Event converts the structured payload into a broadcastable event.
func (e StakeDelegated) Event() *types.Event {
	attrs := map[string]string{
		"addr":        crypto.MustNewAddress(crypto.NHBPrefix, e.Account[:]).String(),
		"sharesAdded": formatAmount(e.SharesAdded),
		"newShares":   formatAmount(e.NewShares),
	}
	if e.LastIndex != nil {
		attrs["lastIndex"] = e.LastIndex.String()
	}
	if !zeroAddress(e.Validator) {
		attrs["validator"] = crypto.MustNewAddress(crypto.NHBPrefix, e.Validator[:]).String()
	}
	if e.Amount != nil {
		attrs["amount"] = formatAmount(e.Amount)
	}
	if e.Locked != nil {
		attrs["locked"] = formatAmount(e.Locked)
	}
	return &types.Event{Type: TypeStakeDelegated, Attributes: attrs}
}

// StakeUndelegated captures the share delta realised when undelegating stake.
type StakeUndelegated struct {
	Account       [20]byte
	SharesRemoved *big.Int
	NewShares     *big.Int
	LastIndex     *big.Int
	Validator     [20]byte
	Amount        *big.Int
	ReleaseTime   uint64
	UnbondingID   uint64
}

// EventType satisfies the Event interface.
func (StakeUndelegated) EventType() string { return TypeStakeUndelegated }

// Event converts the structured payload into a broadcastable event.
func (e StakeUndelegated) Event() *types.Event {
	attrs := map[string]string{
		"addr":          crypto.MustNewAddress(crypto.NHBPrefix, e.Account[:]).String(),
		"sharesRemoved": formatAmount(e.SharesRemoved),
		"newShares":     formatAmount(e.NewShares),
	}
	if e.LastIndex != nil {
		attrs["lastIndex"] = e.LastIndex.String()
	}
	if !zeroAddress(e.Validator) {
		attrs["validator"] = crypto.MustNewAddress(crypto.NHBPrefix, e.Validator[:]).String()
	}
	if e.Amount != nil {
		attrs["amount"] = formatAmount(e.Amount)
	}
	if e.ReleaseTime > 0 {
		attrs["releaseTime"] = strconv.FormatUint(e.ReleaseTime, 10)
	}
	if e.UnbondingID > 0 {
		attrs["unbondingId"] = strconv.FormatUint(e.UnbondingID, 10)
	}
	return &types.Event{Type: TypeStakeUndelegated, Attributes: attrs}
}

// StakeRewardsClaimed captures the staking reward payout for an account.
type StakeRewardsClaimed struct {
	Addr             [20]byte
	PaidZNHB         *big.Int
	Periods          uint64
	AprBps           uint64
	NextEligibleUnix uint64
}

// EventType satisfies the Event interface.
func (StakeRewardsClaimed) EventType() string { return TypeStakeRewardsClaimed }

// Event converts the structured payload into a broadcastable event.
func (e StakeRewardsClaimed) Event() *types.Event {
	addr := crypto.MustNewAddress(crypto.NHBPrefix, e.Addr[:]).String()
	attrs := map[string]string{
		"addr":     addr,
		"paidZNHB": formatAmount(e.PaidZNHB),
	}
	if e.Periods > 0 {
		attrs["periods"] = strconv.FormatUint(e.Periods, 10)
	}
	if e.AprBps > 0 {
		attrs["aprBps"] = strconv.FormatUint(e.AprBps, 10)
	}
	if e.NextEligibleUnix > 0 {
		attrs["nextEligibleUnix"] = strconv.FormatUint(e.NextEligibleUnix, 10)
	}
	return &types.Event{Type: TypeStakeRewardsClaimed, Attributes: attrs}
}

// LegacyEvent renders the backwards compatible alias for reward claims.
func (e StakeRewardsClaimed) LegacyEvent() *types.Event {
	addr := crypto.MustNewAddress(crypto.NHBPrefix, e.Addr[:]).String()
	attrs := map[string]string{
		"addr":   addr,
		"minted": formatAmount(e.PaidZNHB),
	}
	if e.Periods > 0 {
		attrs["periods"] = strconv.FormatUint(e.Periods, 10)
	}
	if e.AprBps > 0 {
		attrs["aprBps"] = strconv.FormatUint(e.AprBps, 10)
	}
	if e.NextEligibleUnix > 0 {
		attrs["nextEligibleUnix"] = strconv.FormatUint(e.NextEligibleUnix, 10)
	}
	return &types.Event{Type: TypeStakeRewardsClaimedLegacy, Attributes: attrs}
}

// StakeCapHit indicates that the annual emission cap limited a reward claim.
type StakeCapHit struct {
	AttemptedZNHB *big.Int
	YTD           *big.Int
	Cap           *big.Int
}

// EventType satisfies the Event interface.
func (StakeCapHit) EventType() string { return TypeStakeCapHit }

// Event converts the structured payload into a broadcastable event.
func (e StakeCapHit) Event() *types.Event {
	attrs := map[string]string{
		"attemptedZNHB": formatAmount(e.AttemptedZNHB),
		"ytd":           formatAmount(e.YTD),
		"cap":           formatAmount(e.Cap),
	}
	return &types.Event{Type: TypeStakeCapHit, Attributes: attrs}
}

// StakePaused captures a staking request rejected due to a governance pause.
type StakePaused struct {
	Account     [20]byte
	Operation   string
	Reason      string
	UnbondingID uint64
}

// EventType satisfies the Event interface.
func (StakePaused) EventType() string { return TypeStakePaused }

// Event converts the structured payload into a broadcastable event.
func (e StakePaused) Event() *types.Event {
	attrs := make(map[string]string)
	if !zeroAddress(e.Account) {
		attrs["addr"] = crypto.MustNewAddress(crypto.NHBPrefix, e.Account[:]).String()
	}
	if op := strings.TrimSpace(e.Operation); op != "" {
		attrs["operation"] = op
	}
	if reason := strings.TrimSpace(e.Reason); reason != "" {
		attrs["reason"] = reason
	}
	if e.UnbondingID > 0 {
		attrs["unbondingId"] = strconv.FormatUint(e.UnbondingID, 10)
	}
	if len(attrs) == 0 {
		return nil
	}
	return &types.Event{Type: TypeStakePaused, Attributes: attrs}
}
