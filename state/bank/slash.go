package bank

import (
	"bytes"
	"errors"
	"math/big"

	"nhbchain/core/state"
	"nhbchain/core/types"
)

type Slasher interface {
	Slash(addr [20]byte, amount *big.Int) error
}

// AppliedSlasher is a Slasher that can say how much it actually forfeited,
// which can be less than the amount it was asked for.
type AppliedSlasher interface {
	Slasher
	SlashApplied(addr [20]byte, amount *big.Int) (*big.Int, error)
}

// SlashReporting slashes through s and returns the amount actually forfeited.
// A slasher that cannot report it is taken to have forfeited what it was asked
// for.
func SlashReporting(s Slasher, addr [20]byte, amount *big.Int) (*big.Int, error) {
	if reporter, ok := s.(AppliedSlasher); ok {
		return reporter.SlashApplied(addr, amount)
	}
	if err := s.Slash(addr, amount); err != nil {
		return nil, err
	}
	if amount == nil {
		return big.NewInt(0), nil
	}
	return new(big.Int).Set(amount), nil
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
	// reachUnbonds makes the slasher continue into the offender's own pending
	// unbonds once its locked balance is used up (PL-R5T-26). Off by default: it
	// changes what a slash leaves behind, so it is switched on only from an
	// activation height (core.ValidatorRules.SlashPendingUnbondsActivationHeight).
	reachUnbonds bool
}

// Ensure ValidatorSlasher implements Slasher
var _ Slasher = (*ValidatorSlasher)(nil)

func NewValidatorSlasher(mgr *state.Manager, treasury [20]byte) *ValidatorSlasher {
	return &ValidatorSlasher{mgr: mgr, treasury: treasury}
}

// ReachingPendingUnbonds returns a copy of the slasher that, after the offender's
// locked balance, forfeits the offender's own pending unbonds (the entries it made
// out of its stake in itself) in the order they were made, up to the amount asked
// for (PL-R5T-26). ZNHB the offender moved into a pending unbond before the report
// landed is otherwise out of reach: an unbond lasts 7 days by default and the
// evidence window is 8,640 blocks. The receiver is not changed.
func (s *ValidatorSlasher) ReachingPendingUnbonds() *ValidatorSlasher {
	c := *s
	c.reachUnbonds = true
	return &c
}

func (s *ValidatorSlasher) Slash(addr [20]byte, amount *big.Int) error {
	_, err := s.SlashApplied(addr, amount)
	return err
}

// SlashApplied forfeits up to amount of the offender's bonded ZNHB to the
// treasury and returns what it actually took. The penalty is bounded by what
// the offender really has: its recorded Stake (when it has one) and the ZNHB it
// holds locked, whichever is smaller. The forfeited ZNHB comes out of
// LockedZNHB, so it is bounded by that balance whatever the stake says, and
// Stake goes down by the same amount, never further than it can.
func (s *ValidatorSlasher) SlashApplied(addr [20]byte, amount *big.Int) (*big.Int, error) {
	if s.mgr == nil {
		return nil, errors.New("bank: slasher requires state manager")
	}
	if amount == nil || amount.Sign() == 0 {
		return big.NewInt(0), nil
	}
	if amount.Sign() < 0 {
		return nil, errors.New("bank: slash amount cannot be negative")
	}

	account, err := s.mgr.GetAccount(addr[:])
	if err != nil {
		return nil, err
	}

	if account.LockedZNHB == nil {
		account.LockedZNHB = big.NewInt(0)
	}

	// We only slash the locked/bonded ZNHB for this specific address (the validator's self-stake/escrow).
	penalty := new(big.Int).Set(amount)
	if account.Stake != nil && account.Stake.Sign() > 0 && account.Stake.Cmp(penalty) < 0 {
		penalty.Set(account.Stake)
	}
	if account.LockedZNHB.Cmp(penalty) < 0 {
		penalty.Set(account.LockedZNHB)
	}
	account.LockedZNHB.Sub(account.LockedZNHB, penalty)

	if account.Stake != nil && account.Stake.Sign() > 0 {
		account.Stake.Sub(account.Stake, penalty)
	}

	// What is still owed after the locked balance goes to the offender's own
	// pending unbonds, when the slasher was asked to reach them. Stake is not
	// lowered again for this part: it left the stake when it was unbonded.
	if s.reachUnbonds {
		owed := new(big.Int).Sub(amount, penalty)
		penalty.Add(penalty, takeOwnUnbonds(account, addr, owed))
	}

	if err := s.mgr.PutAccount(addr[:], account); err != nil {
		return nil, err
	}

	if penalty.Sign() <= 0 {
		return big.NewInt(0), nil
	}

	// A validator slashing itself (self-stake) would otherwise re-read the
	// same account it just wrote above -- refetch rather than reuse the
	// in-memory `account` value so the credit below is applied on top of
	// the just-persisted debit, not a stale pre-debit snapshot.
	treasuryAccount, err := s.mgr.GetAccount(s.treasury[:])
	if err != nil {
		return nil, err
	}
	if treasuryAccount.BalanceZNHB == nil {
		treasuryAccount.BalanceZNHB = big.NewInt(0)
	}
	treasuryAccount.BalanceZNHB.Add(treasuryAccount.BalanceZNHB, penalty)

	if err := s.mgr.PutAccount(s.treasury[:], treasuryAccount); err != nil {
		return nil, err
	}
	return penalty, nil
}

// takeOwnUnbonds removes up to owed from the account's pending unbonds that were made
// out of its stake in itself (entry.Validator is the account), oldest first, and
// returns what it took. Entries that hold no positive amount are left alone; an entry
// emptied by the slash is dropped from the list.
func takeOwnUnbonds(account *types.Account, addr [20]byte, owed *big.Int) *big.Int {
	taken := big.NewInt(0)
	if owed.Sign() <= 0 {
		return taken
	}
	remaining := new(big.Int).Set(owed)
	kept := account.PendingUnbonds[:0:0]
	for _, entry := range account.PendingUnbonds {
		if remaining.Sign() > 0 && entry.Amount != nil && entry.Amount.Sign() > 0 && bytes.Equal(entry.Validator, addr[:]) {
			take := new(big.Int).Set(entry.Amount)
			if take.Cmp(remaining) > 0 {
				take.Set(remaining)
			}
			entry.Amount = new(big.Int).Sub(entry.Amount, take)
			remaining.Sub(remaining, take)
			taken.Add(taken, take)
			if entry.Amount.Sign() == 0 {
				continue
			}
		}
		kept = append(kept, entry)
	}
	account.PendingUnbonds = kept
	return taken
}
