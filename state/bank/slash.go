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
	return errors.New("bank: slashing not implemented")
}

// ValidatorSlasher applies penalty deductions directly to a validator's bonded ZNHB.
type ValidatorSlasher struct {
	mgr *state.Manager
}

// Ensure ValidatorSlasher implements Slasher
var _ Slasher = (*ValidatorSlasher)(nil)

func NewValidatorSlasher(mgr *state.Manager) *ValidatorSlasher {
	return &ValidatorSlasher{mgr: mgr}
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

	return s.mgr.PutAccount(addr[:], account)
}

