package loyalty

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"
)

// legacyProgram mirrors Program's exact field set and order before
// RewardMode/FixedRewardWei were appended, standing in for a value already
// persisted on a live chain before this change shipped.
type legacyProgram struct {
	ID                 ProgramID
	Owner              [20]byte
	Pool               [20]byte
	TokenSymbol        string
	AccrualBps         uint32
	MinSpendWei        *big.Int
	CapPerTx           *big.Int
	DailyCapUser       *big.Int
	DailyCapProgram    *big.Int
	EpochCapProgram    *big.Int
	EpochLengthSeconds uint64
	IssuanceCapUser    *big.Int
	StartTime          uint64
	EndTime            uint64
	Active             bool
}

// TestProgramRLPBackwardCompatibility proves that a Program value encoded
// before RewardMode/FixedRewardWei existed still decodes cleanly into the
// current struct -- with the two new fields defaulting to RewardModeBps and
// nil respectively -- rather than erroring on a list-length mismatch or
// (far worse) silently misdecoding a later field into the wrong slot. This
// is the exact guarantee every already-persisted Program on any live chain
// depends on.
func TestProgramRLPBackwardCompatibility(t *testing.T) {
	var id ProgramID
	id[31] = 0xAA
	var owner, pool [20]byte
	owner[19] = 0x11
	pool[19] = 0x22

	legacy := legacyProgram{
		ID:                 id,
		Owner:              owner,
		Pool:               pool,
		TokenSymbol:        "ZNHB",
		AccrualBps:         500,
		MinSpendWei:        big.NewInt(100),
		CapPerTx:           big.NewInt(500),
		DailyCapUser:       big.NewInt(1000),
		DailyCapProgram:    big.NewInt(5000),
		EpochCapProgram:    big.NewInt(0),
		EpochLengthSeconds: 0,
		IssuanceCapUser:    big.NewInt(0),
		StartTime:          100,
		EndTime:            0,
		Active:             true,
	}

	encoded, err := rlp.EncodeToBytes(&legacy)
	if err != nil {
		t.Fatalf("encode legacy program: %v", err)
	}

	var decoded Program
	if err := rlp.DecodeBytes(encoded, &decoded); err != nil {
		t.Fatalf("decode legacy-shaped bytes into current Program: %v", err)
	}

	if decoded.RewardMode != RewardModeBps {
		t.Fatalf("expected RewardMode to default to RewardModeBps, got %d", decoded.RewardMode)
	}
	if decoded.FixedRewardWei != nil {
		t.Fatalf("expected FixedRewardWei to default to nil, got %v", decoded.FixedRewardWei)
	}
	if decoded.AccrualBps != legacy.AccrualBps {
		t.Fatalf("AccrualBps mismatch: got %d want %d", decoded.AccrualBps, legacy.AccrualBps)
	}
	if decoded.TokenSymbol != legacy.TokenSymbol {
		t.Fatalf("TokenSymbol mismatch: got %q want %q", decoded.TokenSymbol, legacy.TokenSymbol)
	}
	if decoded.Owner != legacy.Owner || decoded.Pool != legacy.Pool || decoded.ID != legacy.ID {
		t.Fatalf("identity fields mismatch after decode")
	}
	if decoded.Active != legacy.Active || decoded.StartTime != legacy.StartTime {
		t.Fatalf("lifecycle fields mismatch after decode")
	}

	// And the reverse direction: a Program with the new fields populated
	// must still round-trip through itself normally.
	fixed := Program{
		ID:              id,
		Owner:           owner,
		Pool:            pool,
		TokenSymbol:     "ZNHB",
		RewardMode:      RewardModeFixed,
		FixedRewardWei:  big.NewInt(10),
		DailyCapProgram: big.NewInt(1000),
		Active:          true,
	}
	fixedEncoded, err := rlp.EncodeToBytes(&fixed)
	if err != nil {
		t.Fatalf("encode fixed-mode program: %v", err)
	}
	var fixedDecoded Program
	if err := rlp.DecodeBytes(fixedEncoded, &fixedDecoded); err != nil {
		t.Fatalf("decode fixed-mode program: %v", err)
	}
	if fixedDecoded.RewardMode != RewardModeFixed {
		t.Fatalf("expected RewardMode to round-trip as Fixed, got %d", fixedDecoded.RewardMode)
	}
	if fixedDecoded.FixedRewardWei == nil || fixedDecoded.FixedRewardWei.String() != "10" {
		t.Fatalf("expected FixedRewardWei to round-trip as 10, got %v", fixedDecoded.FixedRewardWei)
	}
}
