package core

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
)

// Ledger PL-EXT-71: a heartbeat whose payload timestamp is far ahead of the block made every
// validator walk the engagement rollover one day at a time. From
// HeartbeatTimestampBoundActivationHeight a timestamp more than two minutes ahead is refused.

func heartbeatBoundFixture(t *testing.T, activation, height uint64) (*StateProcessor, time.Time) {
	t.Helper()
	sp := newStakingStateProcessor(t)
	sp.SetValidatorRules(ValidatorRules{HeartbeatTimestampBoundActivationHeight: activation})
	blockTime := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	sp.BeginBlock(height, blockTime)
	t.Cleanup(sp.EndBlock)
	return sp, blockTime
}

func heartbeatTx(t *testing.T, timestamp int64) *types.Transaction {
	t.Helper()
	data, err := json.Marshal(types.HeartbeatPayload{DeviceID: "device-1", Timestamp: timestamp})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &types.Transaction{Type: types.TxTypeHeartbeat, Data: data}
}

func freshHeartbeatAccount() *types.Account {
	return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
}

func TestHeartbeatTimestampBoundRefusesATimestampMoreThanTwoMinutesAhead(t *testing.T) {
	sp, blockTime := heartbeatBoundFixture(t, 100, 100)
	sender := make([]byte, 20)
	sender[0] = 1
	account := freshHeartbeatAccount()

	err := sp.applyHeartbeat(heartbeatTx(t, blockTime.Unix()+heartbeatMaxAheadSeconds+1), sender, account)
	if !errors.Is(err, ErrHeartbeatTooSoon) {
		t.Fatalf("a timestamp 121 seconds ahead: error %v, want ErrHeartbeatTooSoon", err)
	}
	if account.EngagementLastHeartbeat != 0 || account.EngagementMinutes != 0 || account.Nonce != 0 || account.EngagementDay != "" {
		t.Fatalf("a refused heartbeat changed the account: %+v", account)
	}

	// The same account, a far-future year: refused at once, no walk over days.
	far := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	start := time.Now()
	if err := sp.applyHeartbeat(heartbeatTx(t, far), sender, account); !errors.Is(err, ErrHeartbeatTooSoon) {
		t.Fatalf("a timestamp in year 9999: error %v, want ErrHeartbeatTooSoon", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("the refusal took %s; it must not walk the days", elapsed)
	}
}

func TestHeartbeatTimestampBoundAllowsTheEdgeTheBlockTimeAndThePast(t *testing.T) {
	for _, tc := range []struct {
		name      string
		timestamp func(block time.Time) int64
	}{
		{"exactly two minutes ahead", func(b time.Time) int64 { return b.Unix() + heartbeatMaxAheadSeconds }},
		{"the block time", func(b time.Time) int64 { return b.Unix() }},
		{"an hour ago", func(b time.Time) int64 { return b.Unix() - 3600 }},
		{"zero means the block time", func(b time.Time) int64 { return 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp, blockTime := heartbeatBoundFixture(t, 100, 100)
			sender := make([]byte, 20)
			sender[0] = 2
			if err := sp.applyHeartbeat(heartbeatTx(t, tc.timestamp(blockTime)), sender, freshHeartbeatAccount()); err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}
}

func TestHeartbeatTimestampBoundIsOffBeforeItsHeightAndWhenZero(t *testing.T) {
	far := time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	for _, tc := range []struct {
		name              string
		activation, block uint64
	}{
		{"rule off (0)", 0, 1_000_000},
		{"height below the activation", 100, 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp, _ := heartbeatBoundFixture(t, tc.activation, tc.block)
			sender := make([]byte, 20)
			sender[0] = 3
			account := freshHeartbeatAccount()
			if err := sp.applyHeartbeat(heartbeatTx(t, far), sender, account); err != nil {
				t.Fatalf("a far-future timestamp was refused while the rule is off: %v", err)
			}
			if account.EngagementLastHeartbeat != uint64(far) {
				t.Fatalf("the heartbeat was not recorded as before: last heartbeat %d", account.EngagementLastHeartbeat)
			}
		})
	}
}
