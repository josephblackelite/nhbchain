package errors

import stderrors "errors"

var (
	ErrNotDue         = stderrors.New("stake: claim not yet due")
	ErrStakingPaused  = stderrors.New("stake: staking paused")
	ErrCapHit         = stderrors.New("stake: emission cap hit")
	ErrNothingAccrued = stderrors.New("stake: nothing accrued")
)
