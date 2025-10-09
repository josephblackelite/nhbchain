package potso_test

import (
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/native/potso"
	"nhbchain/observability/metrics"
	"nhbchain/storage"
)

func TestPotsoRewardConfigRejectsZeroEmission(t *testing.T) {
	db := storage.NewMemDB()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	node, err := core.NewNode(db, key, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	cfg := potso.RewardConfig{
		EpochLengthBlocks:  10,
		EmissionPerEpoch:   big.NewInt(0),
		TreasuryAddress:    bech32Addr(t, key),
		MinPayoutWei:       big.NewInt(0),
		MaxWinnersPerEpoch: 0,
	}
	if err := node.SetPotsoRewardConfig(cfg); err == nil {
		t.Fatalf("expected zero-emission config to be rejected")
	}
}

func TestPotsoHeartbeatRateLimitAndMetrics(t *testing.T) {
	db := storage.NewMemDB()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	node, err := core.NewNode(db, key, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	treasury := bech32Addr(t, key)
	cfg := potso.RewardConfig{
		EpochLengthBlocks:  5,
		EmissionPerEpoch:   big.NewInt(100),
		TreasuryAddress:    treasury,
		MinPayoutWei:       big.NewInt(0),
		MaxWinnersPerEpoch: 0,
	}
	if err := node.SetPotsoRewardConfig(cfg); err != nil {
		t.Fatalf("set reward config: %v", err)
	}
	if err := node.SetPotsoEngineParams(potso.EngineParams{MaxHeartbeatsPerEpoch: 2}); err != nil {
		t.Fatalf("set engine params: %v", err)
	}

	block, err := node.Chain().GetBlockByHeight(0)
	if err != nil {
		t.Fatalf("load genesis block: %v", err)
	}
	hash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash block: %v", err)
	}
	addr := key.PubKey().Address()
	var participant [20]byte
	copy(participant[:], addr.Bytes())

	ts := time.Now().UTC().Unix()
	metrics.Potso().ResetHeartbeatMetrics()

	meter, delta, err := node.PotsoHeartbeat(participant, block.Header.Height, hash, ts)
	if err != nil {
		t.Fatalf("first heartbeat: %v", err)
	}
	if delta == 0 {
		t.Fatalf("expected positive uptime delta on first heartbeat")
	}
	firstUptime := meter.UptimeSeconds

	secondTS := ts + potso.HeartbeatIntervalSeconds + 5
	meter, delta, err = node.PotsoHeartbeat(participant, block.Header.Height, hash, secondTS)
	if err != nil {
		t.Fatalf("second heartbeat: %v", err)
	}
	if delta == 0 {
		t.Fatalf("expected positive uptime delta on second heartbeat")
	}
	secondUptime := meter.UptimeSeconds
	if secondUptime <= firstUptime {
		t.Fatalf("expected uptime to increase")
	}

	thirdTS := secondTS + potso.HeartbeatIntervalSeconds + 5
	if _, _, err = node.PotsoHeartbeat(participant, block.Header.Height, hash, thirdTS); !errors.Is(err, potso.ErrHeartbeatRateLimited) {
		t.Fatalf("expected rate limit error, got %v", err)
	}

	latest, err := node.PotsoUserMeters(participant, meter.Day)
	if err != nil {
		t.Fatalf("load meter: %v", err)
	}
	if latest.UptimeSeconds != secondUptime {
		t.Fatalf("expected uptime to remain %d after rate limit, got %d", secondUptime, latest.UptimeSeconds)
	}

	epochLabel := "0"
	addrLabel := strings.ToLower("0x" + hex.EncodeToString(participant[:]))
	potsoMetrics := metrics.Potso()

	if got := testutil.ToFloat64(potsoMetrics.HeartbeatCounterVec().WithLabelValues(epochLabel, addrLabel)); got != 2 {
		t.Fatalf("expected 2 accepted heartbeats, got %f", got)
	}
	if got := testutil.ToFloat64(potsoMetrics.HeartbeatRateLimitedVec().WithLabelValues(epochLabel, addrLabel)); got != 1 {
		t.Fatalf("expected 1 rate-limited heartbeat, got %f", got)
	}
	if got := testutil.ToFloat64(potsoMetrics.HeartbeatUniquePeersGauge().WithLabelValues(epochLabel)); got != 1 {
		t.Fatalf("expected 1 unique peer, got %f", got)
	}
	avg := testutil.ToFloat64(potsoMetrics.HeartbeatAvgSessionGauge().WithLabelValues(epochLabel))
	expectedAvg := float64(secondUptime) / 2.0
	if diff := avg - expectedAvg; diff > 0.1 || diff < -0.1 {
		t.Fatalf("unexpected avg session: got %f want %f", avg, expectedAvg)
	}
	if got := testutil.ToFloat64(potsoMetrics.HeartbeatWashVec().WithLabelValues(epochLabel, addrLabel)); got != 0 {
		t.Fatalf("expected no wash engagement, got %f", got)
	}
}

func bech32Addr(t *testing.T, key *crypto.PrivateKey) [20]byte {
	t.Helper()
	addr := key.PubKey().Address()
	var out [20]byte
	copy(out[:], addr.Bytes())
	return out
}
