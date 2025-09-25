package bank

import (
	"errors"
	"math/big"
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
