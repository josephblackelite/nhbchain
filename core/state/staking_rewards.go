package state

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	stakeerrors "nhbchain/core/errors"
	"nhbchain/native/governance"
)

const (
	basisPointsDenom        = 10_000
	secondsPerDay           = 24 * 60 * 60
	secondsPerYear          = 365 * secondsPerDay
	defaultStakingAprBps    = 1_250
	defaultPayoutPeriodDays = 30
)

var (
	uq128Unit          = new(big.Int).Lsh(big.NewInt(1), 128)
	accrualDenominator = new(big.Int).Mul(big.NewInt(secondsPerYear), big.NewInt(basisPointsDenom))
)

// RewardEngine manages staking reward state transitions backed by the state manager.
type RewardEngine struct {
	mgr *Manager
}

// ErrNotReady is returned when a reward operation cannot be completed yet.
var ErrNotReady = errors.New("reward engine not ready")

// NewRewardEngine constructs a RewardEngine bound to the provided state manager.
func NewRewardEngine(mgr *Manager) *RewardEngine {
	return &RewardEngine{mgr: mgr}
}

// updateGlobalIndex advances the global reward index snapshot.
func (e *RewardEngine) updateGlobalIndex(aprBps, payoutDays uint64, now time.Time) {
	if e == nil || e.mgr == nil {
		return
	}

	snapshot, err := e.mgr.GetGlobalIndex()
	if err != nil {
		return
	}
	if snapshot == nil {
		snapshot = &GlobalIndex{}
	}

	current := decodeUQ128x128(snapshot.UQ128x128)
	ts := now.UTC().Unix()

	if ts <= 0 {
		snapshot.LastUpdateUnix = ts
		snapshot.UQ128x128 = encodeUQ128x128(current)
		_ = e.mgr.PutGlobalIndex(snapshot)
		return
	}

	if snapshot.LastUpdateUnix == 0 {
		snapshot.LastUpdateUnix = ts
		snapshot.UQ128x128 = encodeUQ128x128(current)
		_ = e.mgr.PutGlobalIndex(snapshot)
		return
	}

	delta := ts - snapshot.LastUpdateUnix
	if delta <= 0 {
		snapshot.LastUpdateUnix = ts
		snapshot.UQ128x128 = encodeUQ128x128(current)
		_ = e.mgr.PutGlobalIndex(snapshot)
		return
	}

	if payoutDays > 0 {
		maxDelta := int64(payoutDays) * secondsPerDay
		if maxDelta > 0 && delta > maxDelta {
			delta = maxDelta
		}
	}

	if aprBps > 0 && delta > 0 {
		deltaBig := new(big.Int).SetInt64(delta)
		aprBig := new(big.Int).SetUint64(aprBps)
		increment := new(big.Int).Set(current)
		increment.Mul(increment, aprBig)
		increment.Mul(increment, deltaBig)
		increment.Quo(increment, accrualDenominator)

		if increment.Sign() > 0 {
			current.Add(current, increment)
		}
	}

	snapshot.LastUpdateUnix = ts
	snapshot.UQ128x128 = encodeUQ128x128(current)
	_ = e.mgr.PutGlobalIndex(snapshot)
}

// UpdateGlobalIndex is a public wrapper around updateGlobalIndex to allow other packages
// to advance the global staking index. It delegates to the internal implementation to
// avoid exposing additional state management details.
func (e *RewardEngine) UpdateGlobalIndex(aprBps, payoutDays uint64, now time.Time) {
	e.updateGlobalIndex(aprBps, payoutDays, now)
}

// accrue processes pending rewards for the provided account address.
func (e *RewardEngine) accrue(addr []byte) error {
	if e == nil || e.mgr == nil {
		return fmt.Errorf("reward engine unavailable")
	}
	if len(addr) == 0 {
		return fmt.Errorf("address required")
	}

	snap, err := e.mgr.GetStakingSnap(addr)
	if err != nil {
		return err
	}
	if snap == nil {
		snap = &AccountSnap{}
	}

	global, err := e.mgr.GetGlobalIndex()
	if err != nil {
		return err
	}
	if global == nil {
		global = &GlobalIndex{}
	}

	currentIndex := decodeUQ128x128(global.UQ128x128)
	lastIndex := decodeUQ128x128(snap.LastIndexUQ128x128)
	delta := new(big.Int).Sub(currentIndex, lastIndex)

	if delta.Sign() > 0 {
		account, err := e.mgr.GetAccount(addr)
		if err != nil {
			return err
		}
		if account != nil && account.LockedZNHB != nil && account.LockedZNHB.Sign() > 0 {
			reward := new(big.Int).Mul(delta, account.LockedZNHB)
			reward.Quo(reward, uq128Unit)
			if snap.AccruedZNHB == nil {
				snap.AccruedZNHB = big.NewInt(0)
			}
			snap.AccruedZNHB.Add(snap.AccruedZNHB, reward)
		}
	}

	snap.LastIndexUQ128x128 = encodeUQ128x128(currentIndex)
	return e.mgr.PutStakingSnap(addr, snap)
}

// settleOnDelegate records a delegation event for the given account address.
func (e *RewardEngine) settleOnDelegate(addr []byte, amount *big.Int) error {
	if e == nil || e.mgr == nil {
		return fmt.Errorf("reward engine unavailable")
	}
	if len(addr) == 0 {
		return fmt.Errorf("address required")
	}
	if err := e.accrue(addr); err != nil {
		return err
	}
	if amount == nil || amount.Sign() == 0 {
		return nil
	}
	if amount.Sign() < 0 {
		return fmt.Errorf("amount must be non-negative")
	}
	account, err := e.mgr.GetAccount(addr)
	if err != nil {
		return err
	}
	if account.LockedZNHB == nil {
		account.LockedZNHB = big.NewInt(0)
	}
	delta := new(big.Int).Set(amount)
	account.LockedZNHB.Add(account.LockedZNHB, delta)
	return e.mgr.PutAccountMetadata(addr, account)
}

// settleOnUndelegate records an undelegation event for the given account address.
func (e *RewardEngine) settleOnUndelegate(addr []byte, amount *big.Int) error {
	if e == nil || e.mgr == nil {
		return fmt.Errorf("reward engine unavailable")
	}
	if len(addr) == 0 {
		return fmt.Errorf("address required")
	}
	if err := e.accrue(addr); err != nil {
		return err
	}
	if amount == nil || amount.Sign() == 0 {
		return nil
	}
	if amount.Sign() < 0 {
		return fmt.Errorf("amount must be non-negative")
	}
	account, err := e.mgr.GetAccount(addr)
	if err != nil {
		return err
	}
	if account.LockedZNHB == nil || account.LockedZNHB.Sign() == 0 {
		return fmt.Errorf("insufficient stake")
	}
	if account.LockedZNHB.Cmp(amount) < 0 {
		return fmt.Errorf("insufficient stake")
	}
	delta := new(big.Int).Set(amount)
	account.LockedZNHB.Sub(account.LockedZNHB, delta)
	return e.mgr.PutAccountMetadata(addr, account)
}

// Claim finalizes rewards for the specified account at the provided timestamp.
func (e *RewardEngine) Claim(addr common.Address, now time.Time) (paid *big.Int, periods int, next int64, err error) {
	if e == nil || e.mgr == nil {
		return nil, 0, 0, fmt.Errorf("reward engine unavailable")
	}

	addrBytes := addr.Bytes()
	snapshotTime := now.UTC()
	if snapshotTime.IsZero() {
		snapshotTime = time.Now().UTC()
	}

	if err := e.accrue(addrBytes); err != nil {
		return nil, 0, 0, err
	}

	snap, err := e.mgr.GetStakingSnap(addrBytes)
	if err != nil {
		return nil, 0, 0, err
	}
	if snap == nil {
		snap = &AccountSnap{AccruedZNHB: big.NewInt(0)}
	}

	global, err := e.mgr.GetGlobalIndex()
	if err != nil {
		return nil, 0, 0, err
	}
	if global == nil {
		global = &GlobalIndex{}
	}

	aprBps, payoutDays, err := e.stakingParams()
	if err != nil {
		return nil, 0, 0, err
	}

	if payoutDays == 0 {
		return nil, 0, 0, fmt.Errorf("staking rewards: payout period not configured")
	}
	if payoutDays > math.MaxInt64/secondsPerDay {
		return nil, 0, 0, fmt.Errorf("staking rewards: payout period too large")
	}

	periodSeconds := int64(payoutDays) * secondsPerDay
	nowUnix := snapshotTime.Unix()
	lastPayout := snap.LastPayoutUnix
	nextEligible := lastPayout + periodSeconds

	if nowUnix <= lastPayout {
		return big.NewInt(0), 0, nextEligible, stakeerrors.ErrNotDue
	}

	elapsed := nowUnix - lastPayout
	if elapsed < periodSeconds {
		return big.NewInt(0), 0, nextEligible, stakeerrors.ErrNotDue
	}

	periods64 := elapsed / periodSeconds
	if periods64 <= 0 {
		return big.NewInt(0), 0, nextEligible, stakeerrors.ErrNotDue
	}
	if periods64 > int64(math.MaxInt) {
		return nil, 0, 0, fmt.Errorf("staking rewards: eligible period overflow")
	}

	account, err := e.mgr.GetAccount(addrBytes)
	if err != nil {
		return nil, 0, 0, err
	}

	stakeBalance := big.NewInt(0)
	if account != nil && account.LockedZNHB != nil {
		stakeBalance = new(big.Int).Set(account.LockedZNHB)
	}

	expected := big.NewInt(0)
	if stakeBalance.Sign() > 0 && aprBps > 0 {
		expected = new(big.Int).Set(stakeBalance)
		expected.Mul(expected, new(big.Int).SetUint64(aprBps))
		expected.Mul(expected, big.NewInt(periods64))
		denom := big.NewInt(basisPointsDenom * 12)
		expected.Quo(expected, denom)
	}

	accrued := big.NewInt(0)
	if snap.AccruedZNHB != nil {
		accrued = new(big.Int).Set(snap.AccruedZNHB)
	}

	payout := big.NewInt(0)
	if accrued.Cmp(expected) > 0 {
		payout.Set(expected)
	} else {
		payout.Set(accrued)
	}

	if snap.AccruedZNHB == nil {
		snap.AccruedZNHB = big.NewInt(0)
	}
	snap.AccruedZNHB.Sub(snap.AccruedZNHB, payout)
	if snap.AccruedZNHB.Sign() < 0 {
		snap.AccruedZNHB.SetInt64(0)
	}

	advance := periods64 * periodSeconds
	snap.LastPayoutUnix += advance
	nextEligible = snap.LastPayoutUnix + periodSeconds

	if err := e.mgr.PutStakingSnap(addrBytes, snap); err != nil {
		return nil, 0, 0, fmt.Errorf("staking rewards: update snapshot: %w", err)
	}

	if payout.Sign() > 0 {
		account.BalanceZNHB.Add(account.BalanceZNHB, payout)
		if err := e.mgr.PutAccountMetadata(addrBytes, account); err != nil {
			return nil, 0, 0, fmt.Errorf("staking rewards: credit account: %w", err)
		}
	}

	if global.YTDEmissions == nil {
		global.YTDEmissions = big.NewInt(0)
	}
	global.YTDEmissions.Add(global.YTDEmissions, payout)
	if err := e.mgr.PutGlobalIndex(global); err != nil {
		return nil, 0, 0, fmt.Errorf("staking rewards: update global index: %w", err)
	}

	if payout.Sign() > 0 {
		if _, err := e.mgr.IncrementStakingEmissionYTD(uint32(snapshotTime.Year()), payout); err != nil {
			return nil, 0, 0, fmt.Errorf("staking rewards: update ytd: %w", err)
		}
	}

	return new(big.Int).Set(payout), int(periods64), nextEligible, nil
}

func (e *RewardEngine) stakingParams() (aprBps uint64, payoutDays uint64, err error) {
	aprBps = defaultStakingAprBps
	payoutDays = defaultPayoutPeriodDays

	if e == nil || e.mgr == nil {
		return aprBps, payoutDays, fmt.Errorf("reward engine unavailable")
	}

	if raw, ok, getErr := e.mgr.ParamStoreGet(governance.ParamKeyStakingAprBps); getErr != nil {
		return 0, 0, fmt.Errorf("load staking apr: %w", getErr)
	} else if ok {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed != "" {
			value, parseErr := strconv.ParseUint(trimmed, 10, 64)
			if parseErr != nil {
				return 0, 0, fmt.Errorf("parse staking apr: %w", parseErr)
			}
			aprBps = value
		}
	}

	if raw, ok, getErr := e.mgr.ParamStoreGet(governance.ParamKeyStakingPayoutPeriodDays); getErr != nil {
		return 0, 0, fmt.Errorf("load staking payout period: %w", getErr)
	} else if ok {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed != "" {
			value, parseErr := strconv.ParseUint(trimmed, 10, 64)
			if parseErr != nil {
				return 0, 0, fmt.Errorf("parse staking payout period: %w", parseErr)
			}
			if value > 0 {
				payoutDays = value
			}
		}
	}

	return aprBps, payoutDays, nil
}

// Claim exposes the staking reward claim flow to external callers. The
// exported wrapper maintains backwards compatibility while allowing higher
// layers to interact with the reward engine using the eventual return values
// of the internal implementation once it is wired up.
func (e *RewardEngine) Claim(addr []byte, now time.Time) (*big.Int, int, int64, error) {
	paid, err := e.claim(addr, now)
	if err != nil {
		return nil, 0, 0, err
	}
	if paid == nil {
		paid = big.NewInt(0)
	}
	return paid, 0, 0, nil
}

// AccrueAccount exposes the account accrual helper for external callers.
func (e *RewardEngine) AccrueAccount(addr []byte) error {
	return e.accrue(addr)
}

// SettleDelegate applies delegation changes to the staking ledger while updating reward snapshots.
func (e *RewardEngine) SettleDelegate(addr []byte, amount *big.Int) error {
	return e.settleOnDelegate(addr, amount)
}

// SettleUndelegate applies undelegation changes to the staking ledger while updating reward snapshots.
func (e *RewardEngine) SettleUndelegate(addr []byte, amount *big.Int) error {
	return e.settleOnUndelegate(addr, amount)
}

// encodeUQ128x128 encodes a big integer into a UQ128x128 fixed-point representation.
func encodeUQ128x128(value *big.Int) []byte {
	if value == nil || value.Sign() <= 0 {
		value = new(big.Int).Set(uq128Unit)
	}
	encoded := make([]byte, 32)
	value.FillBytes(encoded)
	return encoded
}

// decodeUQ128x128 decodes a UQ128x128 fixed-point representation into a big integer.
func decodeUQ128x128(data []byte) *big.Int {
	if len(data) == 0 {
		return new(big.Int).Set(uq128Unit)
	}
	return new(big.Int).SetBytes(data)
}
