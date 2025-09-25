package exports

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"nhbchain/consensus/potso/rewards"
)

func sampleEntry(amount int64) *rewards.RewardEntry {
	var addr [20]byte
	addr[19] = byte(amount)
	return &rewards.RewardEntry{
		Epoch:       1,
		Address:     addr,
		Amount:      big.NewInt(amount),
		Currency:    "ZNHB",
		Status:      rewards.RewardStatusReady,
		GeneratedAt: time.Unix(1700, 0).UTC(),
		Checksum:    rewards.EntryChecksum(1, addr, big.NewInt(amount)),
	}
}

func TestRewardsCSV(t *testing.T) {
	entries := []*rewards.RewardEntry{sampleEntry(10)}
	data, checksum, err := RewardsCSV(entries)
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(data) == 0 || checksum == "" {
		t.Fatalf("expected data and checksum")
	}
	output := string(data)
	if !strings.Contains(output, "epoch,address,amount,currency,status,generated_at,checksum") {
		t.Fatalf("missing header: %s", output)
	}
	if !strings.Contains(output, "ZNHB") {
		t.Fatalf("missing currency: %s", output)
	}
}

func TestRewardsJSONL(t *testing.T) {
	entries := []*rewards.RewardEntry{sampleEntry(25)}
	data, checksum, err := RewardsJSONL(entries)
	if err != nil {
		t.Fatalf("jsonl: %v", err)
	}
	if len(data) == 0 || checksum == "" {
		t.Fatalf("expected data and checksum")
	}
	output := string(data)
	if !strings.Contains(output, "\"epoch\":1") {
		t.Fatalf("unexpected payload: %s", output)
	}
	if !strings.Contains(output, "\"status\":\"ready\"") {
		t.Fatalf("missing status: %s", output)
	}
}
