package events

import (
	"math/big"
	"strconv"

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
	// TypeStakeEmissionCapHit signals that the annual emission cap prevented a full payout.
	TypeStakeEmissionCapHit = "stake.emissionCapHit"
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
	Account     [20]byte
	Minted      *big.Int
	Periods     uint64
	LastIndex   *big.Int
	Shares      *big.Int
	EmissionYTD *big.Int
}

// EventType satisfies the Event interface.
func (StakeRewardsClaimed) EventType() string { return TypeStakeRewardsClaimed }

// Event converts the structured payload into a broadcastable event.
func (e StakeRewardsClaimed) Event() *types.Event {
	attrs := map[string]string{
		"addr":        crypto.MustNewAddress(crypto.NHBPrefix, e.Account[:]).String(),
		"minted":      formatAmount(e.Minted),
		"lastIndex":   formatAmount(e.LastIndex),
		"emissionYTD": formatAmount(e.EmissionYTD),
	}
	if e.Periods > 0 {
		attrs["periods"] = strconv.FormatUint(e.Periods, 10)
	}
	if e.Shares != nil && e.Shares.Sign() > 0 {
		attrs["shares"] = e.Shares.String()
	}
	return &types.Event{Type: TypeStakeRewardsClaimed, Attributes: attrs}
}

// LegacyEvent renders the backwards compatible alias for reward claims.
func (e StakeRewardsClaimed) LegacyEvent() *types.Event {
	attrs := map[string]string{
		"addr":        crypto.MustNewAddress(crypto.NHBPrefix, e.Account[:]).String(),
		"minted":      formatAmount(e.Minted),
		"lastIndex":   formatAmount(e.LastIndex),
		"emissionYTD": formatAmount(e.EmissionYTD),
	}
	if e.Periods > 0 {
		attrs["periods"] = strconv.FormatUint(e.Periods, 10)
	}
	if e.Shares != nil && e.Shares.Sign() > 0 {
		attrs["shares"] = e.Shares.String()
	}
	return &types.Event{Type: TypeStakeRewardsClaimedLegacy, Attributes: attrs}
}

// StakeEmissionCapHit indicates that the annual emission cap limited a reward claim.
type StakeEmissionCapHit struct {
	Year      uint32
	Minted    *big.Int
	Remaining *big.Int
}

// EventType satisfies the Event interface.
func (StakeEmissionCapHit) EventType() string { return TypeStakeEmissionCapHit }

// Event converts the structured payload into a broadcastable event.
func (e StakeEmissionCapHit) Event() *types.Event {
	attrs := map[string]string{
		"year":      strconv.FormatUint(uint64(e.Year), 10),
		"minted":    formatAmount(e.Minted),
		"remaining": formatAmount(e.Remaining),
	}
	return &types.Event{Type: TypeStakeEmissionCapHit, Attributes: attrs}
}
