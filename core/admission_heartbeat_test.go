package core

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"nhbchain/core/types"
)

// PL-EXT-71 (admission part): a heartbeat dated far from the node's clock is refused before any work.

func heartbeatPayloadJSON(t *testing.T, ts int64) []byte {
	t.Helper()
	data, err := json.Marshal(types.HeartbeatPayload{DeviceID: "pl-ext-71", Timestamp: ts})
	if err != nil {
		t.Fatalf("marshal heartbeat payload: %v", err)
	}
	return data
}

func TestAdmissionHeartbeatWindow(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	n := now.Unix()
	day := int64(heartbeatAdmissionWindowSeconds)

	cases := []struct {
		name    string
		ts      int64
		refused bool
	}{
		{"the node's own clock", n, false},
		{"zero means the block time", 0, false},
		{"exactly one day ahead", n + day, false},
		{"one second beyond a day ahead", n + day + 1, true},
		{"exactly one day behind", n - day, false},
		{"one second beyond a day behind", n - day - 1, true},
		{"the reporter's year 9999 (2.9 million days)", 253402300799, true},
		{"the year 1 (the backdated first heartbeat)", -62135596800, true},
		{"the largest timestamp", math.MaxInt64, true},
		{"the smallest timestamp", math.MinInt64, true},
		{"one hour ahead (a clock that is a little fast)", n + 3600, false},
		// The window itself is pinned by plain numbers, not by the constant, so that changing the constant fails here.
		{"twelve hours ahead", n + 12*3600, false},
		{"twelve hours behind", n - 12*3600, false},
		{"thirty-six hours ahead", n + 36*3600, true},
		{"thirty-six hours behind", n - 36*3600, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := &types.Transaction{Type: types.TxTypeHeartbeat, Data: heartbeatPayloadJSON(t, tc.ts)}
			err := admissionHeartbeatError(tx, now)
			if tc.refused {
				if err == nil || !errors.Is(err, ErrInvalidTransaction) || !strings.Contains(err.Error(), "heartbeat timestamp") {
					t.Fatalf("timestamp %d must be refused as ErrInvalidTransaction, got %v", tc.ts, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("timestamp %d must be admitted, got %v", tc.ts, err)
			}
		})
	}
}

func TestAdmissionHeartbeatLeavesOtherTransactionsAlone(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	far := heartbeatPayloadJSON(t, 253402300799)

	if err := admissionHeartbeatError(&types.Transaction{Type: types.TxTypeTransfer, Data: far}, now); err != nil {
		t.Fatalf("only a heartbeat is judged by its timestamp: %v", err)
	}
	if err := admissionHeartbeatError(nil, now); err != nil {
		t.Fatalf("nil transaction: %v", err)
	}
	if err := admissionHeartbeatError(&types.Transaction{Type: types.TxTypeHeartbeat}, now); err != nil {
		t.Fatalf("a heartbeat with no payload means the block time: %v", err)
	}
	if err := admissionHeartbeatError(&types.Transaction{Type: types.TxTypeHeartbeat, Data: []byte("not a payload")}, now); err != nil {
		t.Fatalf("an undecodable payload is the execution's to report: %v", err)
	}
}

// PL-EXT-93: a heartbeat whose device id is longer than the limit is refused; the limit is pinned by plain numbers.

func heartbeatPayloadWithDevice(t *testing.T, device string, ts int64) []byte {
	t.Helper()
	data, err := json.Marshal(types.HeartbeatPayload{DeviceID: device, Timestamp: ts})
	if err != nil {
		t.Fatalf("marshal heartbeat payload: %v", err)
	}
	return data
}

func TestAdmissionHeartbeatDeviceIDLimit(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		device  string
		ts      int64
		refused bool
	}{
		{"no device id", "", now.Unix(), false},
		{"a uuid", "123e4567-e89b-12d3-a456-426614174000", now.Unix(), false},
		{"a host name", "validator-host-1.internal.example", now.Unix(), false},
		{"exactly 256 bytes", strings.Repeat("d", 256), now.Unix(), false},
		{"257 bytes", strings.Repeat("d", 257), now.Unix(), true},
		{"1,000 bytes", strings.Repeat("d", 1000), now.Unix(), true},
		{"the reporter's 204,800 bytes", strings.Repeat("d", 204800), now.Unix(), true},
		{"over the limit with timestamp 0 (the block time)", strings.Repeat("d", 300), 0, true},
		{"256 bytes with timestamp 0", strings.Repeat("d", 256), 0, false},
		{"multi-byte characters count as bytes: 129 two-byte runes is 258 bytes", strings.Repeat("é", 129), now.Unix(), true},
		{"128 two-byte runes is exactly 256 bytes", strings.Repeat("é", 128), now.Unix(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := &types.Transaction{Type: types.TxTypeHeartbeat, Data: heartbeatPayloadWithDevice(t, tc.device, tc.ts)}
			err := admissionHeartbeatError(tx, now)
			if tc.refused {
				if err == nil || !errors.Is(err, ErrInvalidTransaction) || !strings.Contains(err.Error(), "device id") {
					t.Fatalf("a device id of %d bytes must be refused as ErrInvalidTransaction naming the device id, got %v", len(tc.device), err)
				}
				return
			}
			if err != nil {
				t.Fatalf("a device id of %d bytes must be admitted, got %v", len(tc.device), err)
			}
		})
	}
}

func TestAdmissionHeartbeatDeviceIDLimitLeavesOtherTransactionsAlone(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	big := heartbeatPayloadWithDevice(t, strings.Repeat("d", 5000), now.Unix())
	if err := admissionHeartbeatError(&types.Transaction{Type: types.TxTypeTransfer, Data: big}, now); err != nil {
		t.Fatalf("only a heartbeat is judged by its device id: %v", err)
	}
}

// Through the real admission path: a heartbeat with a huge device id is refused before any state copy.
func TestAddTransactionRefusesAHeartbeatWithAHugeDeviceID(t *testing.T) {
	for _, simulation := range []bool{true, false} {
		name := "simulation on"
		if !simulation {
			name = "simulation off"
		}
		t.Run(name, func(t *testing.T) {
			node := newTestNode(t)
			node.SetTransactionSimulationEnabled(simulation)
			key := livenessKey(t)
			fundWithNonce(t, node, key.PubKey().Address().Bytes(), 1_000_000, 0, 0)

			before := simulationsRun(node)
			data := heartbeatPayloadWithDevice(t, strings.Repeat("d", 100_000), time.Now().Unix())
			err := node.AddTransaction(livenessSign(t, key, types.TxTypeHeartbeat, 0, data, nil, 0))
			if err == nil || !errors.Is(err, ErrInvalidTransaction) || !strings.Contains(err.Error(), "device id") {
				t.Fatalf("want the device id refusal, got %v", err)
			}
			if got := simulationsRun(node); got != before {
				t.Fatalf("the refusal must come before the state copy: simulations %d -> %d", before, got)
			}
			if got := node.MempoolSize(); got != 0 {
				t.Fatalf("a refused heartbeat must not enter the mempool, %d are pending", got)
			}
		})
	}
}

// Through the real admission path: the far-dated heartbeat is refused with the state untouched and without a
// simulation, with the simulation on and with it off, and an honest heartbeat still reaches the simulation.
func TestAddTransactionRefusesAFarDatedHeartbeatBeforeAnyWork(t *testing.T) {
	for _, simulation := range []bool{true, false} {
		name := "simulation on"
		if !simulation {
			name = "simulation off"
		}
		t.Run(name, func(t *testing.T) {
			node := newTestNode(t)
			node.SetTransactionSimulationEnabled(simulation)
			key := livenessKey(t)
			addr := key.PubKey().Address().Bytes()
			fundWithNonce(t, node, addr, 1_000_000, 0, 0)

			for _, ts := range []int64{253402300799, -62135596800, time.Now().Unix() + 2*24*3600} {
				before := simulationsRun(node)
				err := node.AddTransaction(livenessSign(t, key, types.TxTypeHeartbeat, 0, heartbeatPayloadJSON(t, ts), nil, 0))
				if err == nil || !errors.Is(err, ErrInvalidTransaction) || !strings.Contains(err.Error(), "heartbeat timestamp") {
					t.Fatalf("timestamp %d: want the heartbeat refusal, got %v", ts, err)
				}
				if got := simulationsRun(node); got != before {
					t.Fatalf("timestamp %d: the refusal must come before the state copy: simulations %d -> %d", ts, before, got)
				}
			}
			if got := node.MempoolSize(); got != 0 {
				t.Fatalf("a refused heartbeat must not enter the mempool, %d are pending", got)
			}
		})
	}

	t.Run("an honest heartbeat is admitted and simulated once", func(t *testing.T) {
		node := newTestNode(t)
		key := livenessKey(t)
		fundWithNonce(t, node, key.PubKey().Address().Bytes(), 1_000_000, 0, 0)
		before := simulationsRun(node)
		err := node.AddTransaction(livenessSign(t, key, types.TxTypeHeartbeat, 0, heartbeatPayloadJSON(t, time.Now().Unix()), nil, 0))
		if err != nil && strings.Contains(err.Error(), "heartbeat timestamp") {
			t.Fatalf("an honest heartbeat was refused by the timestamp check: %v", err)
		}
		if got := simulationsRun(node) - before; got != 1 {
			t.Fatalf("an honest heartbeat reaches the simulation exactly once, got %d (err %v)", got, err)
		}
	})
}
