package state

import (
	"fmt"
	"math/big"
	"sort"
	"time"
)

const rollingFeesDateFormat = "20060102"

var (
	feesDayPrefixBytes   = []byte("fees/day/")
	feesDayIndexKeyBytes = []byte("fees/day/index")
)

type RollingFees struct {
	manager *Manager
}

func NewRollingFees(manager *Manager) *RollingFees {
	return &RollingFees{manager: manager}
}

type storedRollingFees struct {
	NetNHB  *big.Int
	NetZNHB *big.Int
}

func (r *RollingFees) AddDay(tsDay time.Time, netFeesNHB, netFeesZNHB *big.Int) error {
	if r == nil || r.manager == nil {
		return fmt.Errorf("rolling fees: state manager not initialised")
	}
	if tsDay.IsZero() {
		return fmt.Errorf("rolling fees: day timestamp required")
	}
	day := dayStartUTC(tsDay)
	dayID := day.Format(rollingFeesDateFormat)
	key := rollingFeeBucketKey(dayID)

	var stored storedRollingFees
	ok, err := r.manager.KVGet(key, &stored)
	if err != nil {
		return fmt.Errorf("rolling fees: load bucket: %w", err)
	}
	if !ok {
		stored.NetNHB = big.NewInt(0)
		stored.NetZNHB = big.NewInt(0)
	} else {
		if stored.NetNHB == nil {
			stored.NetNHB = big.NewInt(0)
		}
		if stored.NetZNHB == nil {
			stored.NetZNHB = big.NewInt(0)
		}
	}

	if netFeesNHB != nil {
		stored.NetNHB = new(big.Int).Add(stored.NetNHB, netFeesNHB)
	}
	if netFeesZNHB != nil {
		stored.NetZNHB = new(big.Int).Add(stored.NetZNHB, netFeesZNHB)
	}

	if err := r.manager.KVPut(key, stored); err != nil {
		return fmt.Errorf("rolling fees: persist bucket: %w", err)
	}

	if err := r.updateIndex(dayID); err != nil {
		return err
	}
	return nil
}

func (r *RollingFees) Get7dNetFeesNHB(tsNow time.Time) (*big.Int, error) {
	return r.sumWindow(tsNow, func(stored *storedRollingFees) *big.Int {
		return stored.NetNHB
	})
}

func (r *RollingFees) Get7dNetFeesZNHB(tsNow time.Time) (*big.Int, error) {
	return r.sumWindow(tsNow, func(stored *storedRollingFees) *big.Int {
		return stored.NetZNHB
	})
}

func (r *RollingFees) sumWindow(tsNow time.Time, selector func(*storedRollingFees) *big.Int) (*big.Int, error) {
	if r == nil || r.manager == nil {
		return nil, fmt.Errorf("rolling fees: state manager not initialised")
	}
	if tsNow.IsZero() {
		return big.NewInt(0), nil
	}
	nowDay := dayStartUTC(tsNow)
	windowStart := nowDay.AddDate(0, 0, -6)

	var index []string
	if err := r.manager.KVGetList(rollingFeeIndexKey(), &index); err != nil {
		return nil, fmt.Errorf("rolling fees: load index: %w", err)
	}

	total := big.NewInt(0)
	for _, dayID := range index {
		day, err := parseRollingFeesDay(dayID)
		if err != nil {
			return nil, fmt.Errorf("rolling fees: parse index entry %q: %w", dayID, err)
		}
		if day.Before(windowStart) || day.After(nowDay) {
			continue
		}
		key := rollingFeeBucketKey(dayID)
		var stored storedRollingFees
		ok, err := r.manager.KVGet(key, &stored)
		if err != nil {
			return nil, fmt.Errorf("rolling fees: load bucket %q: %w", dayID, err)
		}
		if !ok {
			continue
		}
		value := selector(&stored)
		if value == nil {
			continue
		}
		total.Add(total, value)
	}
	return total, nil
}

func (r *RollingFees) updateIndex(dayID string) error {
	var index []string
	if err := r.manager.KVGetList(rollingFeeIndexKey(), &index); err != nil {
		return fmt.Errorf("rolling fees: load index: %w", err)
	}

	filtered := make([]string, 0, len(index)+1)
	for _, existing := range index {
		if existing != dayID {
			filtered = append(filtered, existing)
		}
	}
	filtered = append(filtered, dayID)
	sort.Strings(filtered)
	if len(filtered) > 7 {
		filtered = filtered[len(filtered)-7:]
	}
	if err := r.manager.KVPut(rollingFeeIndexKey(), filtered); err != nil {
		return fmt.Errorf("rolling fees: persist index: %w", err)
	}
	return nil
}

func rollingFeeBucketKey(dayID string) []byte {
	key := make([]byte, len(feesDayPrefixBytes)+len(dayID))
	copy(key, feesDayPrefixBytes)
	copy(key[len(feesDayPrefixBytes):], dayID)
	return key
}

func rollingFeeIndexKey() []byte {
	return append([]byte(nil), feesDayIndexKeyBytes...)
}

func parseRollingFeesDay(dayID string) (time.Time, error) {
	if len(dayID) != len(rollingFeesDateFormat) {
		return time.Time{}, fmt.Errorf("invalid day format")
	}
	return time.ParseInLocation(rollingFeesDateFormat, dayID, time.UTC)
}

func dayStartUTC(ts time.Time) time.Time {
	if ts.IsZero() {
		return time.Time{}
	}
	utc := ts.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}
