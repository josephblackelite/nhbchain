package bank

import (
	"errors"
	"math/big"

	"nhbchain/core/state"
)

type Slasher interface {
	Slash(addr [20]byte, amount *big.Int) error
}

type NoopSlasher struct {
	enabled bool
}

func NewNoopSlasher(enabled bool) *NoopSlasher {
	return &NoopSlasher{enabled: enabled}
}

func (s *NoopSlasher) Slash(addr [20]byte, amount *big.Int) error {
	if amount == nil || amount.Sign() == 0 {
		return nil
	}
	if amount.Sign() < 0 {
		return errors.New("bank: slash amount cannot be negative")
	}
	if !s.enabled {
		return errors.New("bank: slashing disabled")
	}
	return nil
}

// ValidatorSlasher applies penalty deductions directly to a validator's
// bonded ZNHB and credits the forfeited amount to a treasury address.
//
// ZNHB is a hard-fixed-supply asset (no protocol path to mint more of it
// after genesis) -- forfeited stake is moved to the treasury's liquid
// BalanceZNHB, never burned/discarded and never routed through any
// supply-adjustment call, so total ZNHB supply is unaffected by slashing
// either way (the locked ZNHB being moved was already counted in supply).
// No admin action selects the destination -- it is the same protocol-level
// treasury address genesis/config.toml already designates for ZNHB fee
// routing, so slashing cannot be "gamed" by an operator choosing where
// penalties land.
type ValidatorSlasher struct {
	mgr      *state.Manager
	treasury [20]byte
}

// Ensure ValidatorSlasher implements Slasher
var _ Slasher = (*ValidatorSlasher)(nil)

func NewValidatorSlasher(mgr *state.Manager, treasury [20]byte) *ValidatorSlasher {
	return &ValidatorSlasher{mgr: mgr, treasury: treasury}
}

func (s *ValidatorSlasher) Slash(addr [20]byte, amount *big.Int) error {
	if s.mgr == nil {
		return errors.New("bank: slasher requires state manager")
	}
	if amount == nil || amount.Sign() == 0 {
		return nil
	}
	if amount.Sign() < 0 {
		return errors.New("bank: slash amount cannot be negative")
	}

	account, err := s.mgr.GetAccount(addr[:])
	if err != nil {
		return err
	}

	if account.LockedZNHB == nil {
		account.LockedZNHB = big.NewInt(0)
	}

	// We only slash the locked/bonded ZNHB for this specific address (the validator's self-stake/escrow).
	penalty := new(big.Int).Set(amount)
	if account.LockedZNHB.Cmp(penalty) < 0 {
		penalty.Set(account.LockedZNHB)
	}
	account.LockedZNHB.Sub(account.LockedZNHB, penalty)

	if account.Stake != nil && account.Stake.Sign() > 0 {
		if account.Stake.Cmp(penalty) < 0 {
			account.Stake.SetInt64(0)
		} else {
			account.Stake.Sub(account.Stake, penalty)
		}
	}

	if err := s.mgr.PutAccount(addr[:], account); err != nil {
		return err
	}

	if penalty.Sign() <= 0 {
		return nil
	}

	// A validator slashing itself (self-stake) would otherwise re-read the
	// same account it just wrote above -- refetch rather than reuse the
	// in-memory `account` value so the credit below is applied on top of
	// the just-persisted debit, not a stale pre-debit snapshot.
	treasuryAccount, err := s.mgr.GetAccount(s.treasury[:])
	if err != nil {
		return err
	}
	if treasuryAccount.BalanceZNHB == nil {
		treasuryAccount.BalanceZNHB = big.NewInt(0)
	}
	treasuryAccount.BalanceZNHB.Add(treasuryAccount.BalanceZNHB, penalty)

	return s.mgr.PutAccount(s.treasury[:], treasuryAccount)
}
