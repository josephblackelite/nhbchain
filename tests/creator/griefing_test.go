package creator_test

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"lukechampine.com/blake3"
	"nhbchain/native/creator"
)

func TestStakeCapPerEpoch(t *testing.T) {
	state := newTestState()
	engine := creator.NewEngine()
	engine.SetState(state)
	payoutVault := addr(0xC1)
	rewards := addr(0xC2)
	engine.SetPayoutVault(payoutVault)
	engine.SetRewardsTreasury(rewards)
	state.setAccountBig(addr(0x10), big.NewInt(2_000_000_000_000))
	state.setAccount(payoutVault, 0)
	state.setAccount(rewards, 0)

	fan := addr(0x10)
	creatorAddr := addr(0x11)
	engine.SetNowFunc(func() int64 { return 1000 })

	cap := big.NewInt(1_000_000_000_000)
	nearCap := new(big.Int).Sub(cap, big.NewInt(5_000))
	if _, _, err := engine.StakeCreator(fan, creatorAddr, nearCap); err != nil {
		t.Fatalf("expected first stake within cap, got %v", err)
	}
	exceed := big.NewInt(10_000)
	if _, _, err := engine.StakeCreator(fan, creatorAddr, exceed); err == nil || !strings.Contains(err.Error(), "per-epoch stake cap exceeded") {
		t.Fatalf("expected stake cap error, got %v", err)
	}
}

func TestTipRateLimitBlocksBurst(t *testing.T) {
	state := newTestState()
	engine := creator.NewEngine()
	engine.SetState(state)
	payoutVault := addr(0xD1)
	rewards := addr(0xD2)
	engine.SetPayoutVault(payoutVault)
	engine.SetRewardsTreasury(rewards)
	state.setAccount(payoutVault, 0)
	state.setAccount(rewards, 0)

	creatorAddr := addr(0x20)
	fan := addr(0x21)
	state.setAccount(fan, 10_000)

	current := int64(5000)
	engine.SetNowFunc(func() int64 { return current })

	content, err := engine.PublishContent(creatorAddr, "tip-test", "https://example.com/video", "hello world")
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	amount := big.NewInt(100)
	for i := 0; i < 5; i++ {
		if _, err := engine.TipContent(fan, content.ID, amount); err != nil {
			t.Fatalf("tip %d failed: %v", i, err)
		}
	}
	if _, err := engine.TipContent(fan, content.ID, amount); err == nil || !strings.Contains(err.Error(), "tip rate limit exceeded") {
		t.Fatalf("expected tip rate limit, got %v", err)
	}
}

func TestPublishContentValidationAndHash(t *testing.T) {
	state := newTestState()
	engine := creator.NewEngine()
	engine.SetState(state)

	creatorAddr := addr(0x30)

	if _, err := engine.PublishContent(creatorAddr, "invalid", "ftp://example.com", "bad"); err == nil {
		t.Fatalf("expected invalid URI error")
	}
	badMetadata := string([]byte{0xff, 0xfe})
	if _, err := engine.PublishContent(creatorAddr, "invalid-meta", "https://example.com", badMetadata); err == nil {
		t.Fatalf("expected invalid metadata error")
	}

	content, err := engine.PublishContent(creatorAddr, "valid", "https://example.com/resource", "lorem ipsum")
	if err != nil {
		t.Fatalf("publish valid failed: %v", err)
	}
	expected := blake3.Sum256([]byte("lorem ipsum"))
	expectedHex := hex.EncodeToString(expected[:])
	if !strings.EqualFold(content.Hash, expectedHex) {
		t.Fatalf("unexpected content hash: got %s want %s", content.Hash, expectedHex)
	}
}
