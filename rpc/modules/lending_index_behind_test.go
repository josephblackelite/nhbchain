package modules

// A lending read that needs the index of lending transactions must never answer
// from a part of the chain: what a position adds up to over some of the blocks
// is a wrong answer, not a slow one, so a read that finds the index behind the
// chain asks to be retried (HTTP 503) until the index has examined every block.
// These use only the module's public reads, the index's clock and the full walk
// kept in the legacy_* test files.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"testing"
	"time"
)

// oneBlockPerStep makes a module whose index reads a clock that moves by the
// whole budget of a step with every look, so each step of catching up examines
// exactly one block, however fast the machine is. The clock is read only while
// the index's lock is held.
func oneBlockPerStep(lc *lendingChain) *LendingModule {
	module := NewLendingModule(lc.node)
	tick := time.Unix(1_700_000_000, 0)
	step := lendingScanBudget
	module.replayHeights.now = func() time.Time {
		tick = tick.Add(step)
		return tick
	}
	return module
}

// examined is how many blocks, counting from the first, the index has looked at.
func (m *LendingModule) examined() uint64 {
	m.replayHeights.mu.Lock()
	defer m.replayHeights.mu.Unlock()
	return m.replayHeights.scanned
}

func showRead(t *testing.T, parts ...any) string {
	t.Helper()
	raw, err := json.Marshal(parts)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// TestAReadNeverAnswersFromAPartlyExaminedIndex: with an index that has examined
// only the first blocks of a chain whose every block holds lending transactions,
// a partial sum is not what the pool or the account holds, so each of the three
// reads must ask to be retried until the index has examined the whole chain, and
// then answer what the full walk of the chain answers.
func TestAReadNeverAnswersFromAPartlyExaminedIndex(t *testing.T) {
	lc := newLendingChain(t, 91)
	lc.extend(t, 60, 1) // every block holds lending transactions
	tip := lc.node.GetHeight()
	if tip < 20 {
		t.Fatalf("the test chain has only %d blocks", tip)
	}

	// The accounts the full walk finds in the default pool; one of them is asked for.
	reference := NewLendingModule(lc.node)
	wantMarket, wantUsers, err := reference.legacyReplayCommittedPoolState(defaultLendingPoolID)
	if err != nil || wantMarket == nil || len(wantUsers) == 0 {
		t.Fatalf("the full walk found no lending position to ask for: %v %v %d", wantMarket, err, len(wantUsers))
	}
	lenders := make([][20]byte, 0, len(wantUsers))
	for addr := range wantUsers {
		lenders = append(lenders, addr)
	}
	sort.Slice(lenders, func(i, j int) bool { return bytes.Compare(lenders[i][:], lenders[j][:]) < 0 })
	lender := lenders[0]

	for _, tc := range []struct {
		name string
		read func(m *LendingModule) (string, *ModuleError)
		want func() (string, *ModuleError)
	}{
		{
			name: "GetMarket",
			read: func(m *LendingModule) (string, *ModuleError) {
				market, params, moduleErr := m.GetMarket(defaultLendingPoolID)
				return showRead(t, market, params), moduleErr
			},
			want: func() (string, *ModuleError) {
				market, params, moduleErr := reference.legacyGetMarket(defaultLendingPoolID)
				return showRead(t, market, params), moduleErr
			},
		},
		{
			name: "GetPools",
			read: func(m *LendingModule) (string, *ModuleError) {
				pools, params, moduleErr := m.GetPools()
				return showRead(t, pools, params), moduleErr
			},
			want: func() (string, *ModuleError) {
				pools, params, moduleErr := reference.legacyGetPools()
				return showRead(t, pools, params), moduleErr
			},
		},
		{
			name: "GetUserAccount",
			read: func(m *LendingModule) (string, *ModuleError) {
				account, moduleErr := m.GetUserAccount(defaultLendingPoolID, lender)
				return showRead(t, account), moduleErr
			},
			want: func() (string, *ModuleError) {
				account, moduleErr := reference.legacyGetUserAccount(defaultLendingPoolID, lender)
				return showRead(t, account), moduleErr
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			want, wantErr := tc.want()
			if wantErr != nil {
				t.Fatalf("full walk: %v", wantErr)
			}
			module := oneBlockPerStep(lc)
			answered := false
			for call := 1; call <= int(tip)+2 && !answered; call++ {
				got, moduleErr := tc.read(module)
				seen := module.examined()
				switch {
				case moduleErr != nil && moduleErr.HTTPStatus == http.StatusServiceUnavailable:
					if seen >= tip {
						t.Fatalf("call %d asked to retry although the index has examined all %d blocks", call, tip)
					}
				case moduleErr != nil:
					t.Fatalf("call %d: %v", call, moduleErr)
				default:
					if seen < tip {
						t.Fatalf("call %d answered %s from an index that had examined %d of %d blocks", call, got, seen, tip)
					}
					if got != want {
						t.Fatalf("call %d answered something other than the full walk\nwant %s\n got %s", call, want, got)
					}
					answered = true
				}
			}
			if !answered {
				t.Fatalf("no answer after %d calls over a chain of %d blocks", tip+2, tip)
			}
		})
	}
}
