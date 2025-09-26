package creator

import "math/big"

// Content represents a piece of media or experience published by a creator.
type Content struct {
	ID          string   `json:"id"`
	Creator     [20]byte `json:"creator"`
	URI         string   `json:"uri"`
	Metadata    string   `json:"metadata"`
	PublishedAt int64    `json:"publishedAt"`
	TotalTips   *big.Int `json:"totalTips"`
	TotalStake  *big.Int `json:"totalStake"`
}

// Tip captures a fan contribution directed at a specific piece of content.
type Tip struct {
	ContentID string   `json:"contentId"`
	Creator   [20]byte `json:"creator"`
	Fan       [20]byte `json:"fan"`
	Amount    *big.Int `json:"amount"`
	TippedAt  int64    `json:"tippedAt"`
}

// Stake records a fan staking position that accrues payouts for the creator.
type Stake struct {
	Creator     [20]byte `json:"creator"`
	Fan         [20]byte `json:"fan"`
	Amount      *big.Int `json:"amount"`
	Shares      *big.Int `json:"shares"`
	StakedAt    int64    `json:"stakedAt"`
	LastAccrual int64    `json:"lastAccrual"`
}

// PayoutLedger maintains the cumulative payout accounting for a creator.
type PayoutLedger struct {
	Creator             [20]byte `json:"creator"`
	TotalTips           *big.Int `json:"totalTips"`
	TotalStakingYield   *big.Int `json:"totalStakingYield"`
	PendingDistribution *big.Int `json:"pendingDistribution"`
	LastPayout          int64    `json:"lastPayout"`
}

// Clone returns a deep copy of the payout ledger.
func (p *PayoutLedger) Clone() *PayoutLedger {
	if p == nil {
		return nil
	}
	clone := *p
	if p.TotalTips != nil {
		clone.TotalTips = new(big.Int).Set(p.TotalTips)
	}
	if p.TotalStakingYield != nil {
		clone.TotalStakingYield = new(big.Int).Set(p.TotalStakingYield)
	}
	if p.PendingDistribution != nil {
		clone.PendingDistribution = new(big.Int).Set(p.PendingDistribution)
	}
	return &clone
}
