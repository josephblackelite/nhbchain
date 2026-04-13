package rewards

import "math/big"

// Payout captures the per-account breakdown for an epoch settlement.
type Payout struct {
	Account    []byte
	Total      *big.Int
	Validators *big.Int
	Stakers    *big.Int
	Engagement *big.Int
}

// Clone returns a deep copy of the payout entry.
func (p Payout) Clone() Payout {
	return Payout{
		Account:    append([]byte(nil), p.Account...),
		Total:      copyBigInt(p.Total),
		Validators: copyBigInt(p.Validators),
		Stakers:    copyBigInt(p.Stakers),
		Engagement: copyBigInt(p.Engagement),
	}
}

// EpochSettlement summarises the reward distribution for a single epoch.
type EpochSettlement struct {
	Epoch    uint64
	Height   uint64
	ClosedAt int64
	Blocks   uint64

	PlannedTotal      *big.Int
	PaidTotal         *big.Int
	ValidatorsPlanned *big.Int
	ValidatorsPaid    *big.Int
	StakersPlanned    *big.Int
	StakersPaid       *big.Int
	EngagementPlanned *big.Int
	EngagementPaid    *big.Int

	Payouts []Payout
}

// Clone returns a deep copy of the settlement.
func (s EpochSettlement) Clone() EpochSettlement {
	payouts := make([]Payout, len(s.Payouts))
	for i := range s.Payouts {
		payouts[i] = s.Payouts[i].Clone()
	}
	return EpochSettlement{
		Epoch:             s.Epoch,
		Height:            s.Height,
		ClosedAt:          s.ClosedAt,
		Blocks:            s.Blocks,
		PlannedTotal:      copyBigInt(s.PlannedTotal),
		PaidTotal:         copyBigInt(s.PaidTotal),
		ValidatorsPlanned: copyBigInt(s.ValidatorsPlanned),
		ValidatorsPaid:    copyBigInt(s.ValidatorsPaid),
		StakersPlanned:    copyBigInt(s.StakersPlanned),
		StakersPaid:       copyBigInt(s.StakersPaid),
		EngagementPlanned: copyBigInt(s.EngagementPlanned),
		EngagementPaid:    copyBigInt(s.EngagementPaid),
		Payouts:           payouts,
	}
}

// UnusedValidators returns the undistributed portion of the validator pool.
func (s EpochSettlement) UnusedValidators() *big.Int {
	return difference(s.ValidatorsPlanned, s.ValidatorsPaid)
}

// UnusedStakers returns the undistributed portion of the staker pool.
func (s EpochSettlement) UnusedStakers() *big.Int {
	return difference(s.StakersPlanned, s.StakersPaid)
}

// UnusedEngagement returns the undistributed portion of the engagement pool.
func (s EpochSettlement) UnusedEngagement() *big.Int {
	return difference(s.EngagementPlanned, s.EngagementPaid)
}

// UnusedTotal returns the total undistributed amount.
func (s EpochSettlement) UnusedTotal() *big.Int {
	unused := difference(s.PlannedTotal, s.PaidTotal)
	if unused == nil {
		return big.NewInt(0)
	}
	return unused
}

func difference(planned, paid *big.Int) *big.Int {
	if planned == nil {
		return big.NewInt(0)
	}
	value := new(big.Int).Set(planned)
	if paid != nil {
		value.Sub(value, paid)
	}
	if value.Sign() < 0 {
		return big.NewInt(0)
	}
	return value
}

func copyBigInt(value *big.Int) *big.Int {
	if value == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(value)
}
