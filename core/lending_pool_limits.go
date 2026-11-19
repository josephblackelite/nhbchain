package core

import (
	"errors"
	"fmt"

	nhbstate "nhbchain/core/state"
	nativecommon "nhbchain/native/common"
)

// Lending pool creation limits (ledger PL-R5T-13). See
// StateProcessor.SetLendingPoolLimitsActivationHeight and the settings
// LendingMaxPools and LendingPoolIDMaxLength.

var (
	// ErrLendingPoolIDInvalid: the pool id uses a character outside letters,
	// digits, '.', '_' and '-', or is longer than LendingPoolIDMaxLength. It is a
	// pure function of the transaction's own payload and the configured limit, so
	// it can never become valid and classifyProposalError prunes it.
	ErrLendingPoolIDInvalid = errors.New("lending: pool id is not allowed")
	// ErrLendingPoolLimitReached: LendingMaxPools pools exist already. Pools are
	// never removed, so it cannot become valid later; pruned.
	ErrLendingPoolLimitReached = errors.New("lending: the pool limit has been reached")
	// ErrLendingPoolNotFound: a lending transaction named a pool that has not been
	// created. An earlier transaction of the same proposal or a later block may
	// still create it, so it is skipped, not pruned.
	ErrLendingPoolNotFound = errors.New("lending: pool not found")
)

// validLendingPoolID reports whether id may name a pool created from the pool
// limits height: letters, digits, '.', '_' and '-' only (so it can never contain
// the separator its state keys are built with), at most maxLen long when maxLen is
// not zero. It does not apply to the default pool, which the chain creates itself.
func validLendingPoolID(id string, maxLen uint64) error {
	if id == "" {
		return fmt.Errorf("%w: empty", ErrLendingPoolIDInvalid)
	}
	if maxLen > 0 && uint64(len(id)) > maxLen {
		return fmt.Errorf("%w: %d characters, the limit is %d", ErrLendingPoolIDInvalid, len(id), maxLen)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return fmt.Errorf("%w: character %q at %d", ErrLendingPoolIDInvalid, c, i)
		}
	}
	return nil
}

// checkLendingPoolCreation applies the pool limits to a TxTypeLendingCreatePool for
// poolID, from the pool limits height. Before it, it checks nothing, as in every
// earlier release.
func (sp *StateProcessor) checkLendingPoolCreation(manager *nhbstate.Manager, poolID string) error {
	if !sp.lendingPoolLimitsActive() {
		return nil
	}
	if err := nativecommon.Guard(sp.pauses, moduleLending); err != nil {
		return err
	}
	if err := validLendingPoolID(poolID, sp.lendingPoolIDMaxLength); err != nil {
		return err
	}
	if sp.lendingMaxPools > 0 {
		ids, err := manager.LendingListPoolIDs()
		if err != nil {
			return fmt.Errorf("lendingCreatePool: count pools: %w", err)
		}
		if uint64(len(ids)) >= sp.lendingMaxPools {
			return fmt.Errorf("%w: %d pools exist, the limit is %d", ErrLendingPoolLimitReached, len(ids), sp.lendingMaxPools)
		}
	}
	return nil
}

// requireExistingLendingPool refuses, from the pool limits height, a lending
// transaction that names a pool other than the default one that has not been
// created. It is checked before anything else is done for the pool, so a made-up
// name costs one lookup: no sweep of accounts, no market written.
func (sp *StateProcessor) requireExistingLendingPool(manager *nhbstate.Manager, poolID string) error {
	if !sp.lendingPoolLimitsActive() || poolID == defaultLendingPoolID {
		return nil
	}
	_, ok, err := manager.LendingGetMarket(poolID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %q", ErrLendingPoolNotFound, poolID)
	}
	return nil
}
