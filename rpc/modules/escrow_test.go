package modules

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"nhbchain/crypto"
	"nhbchain/native/escrow"
)

func marshalOrFail(t *testing.T, v interface{}) string {
	t.Helper()
	bytes, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(bytes)
}

func testBech32(t *testing.T, fill byte) string {
	t.Helper()
	var addr [20]byte
	for i := range addr {
		addr[i] = fill
	}
	return crypto.MustNewAddress(crypto.NHBPrefix, addr[:]).String()
}

func TestFormatRealmResultIncludesMetadata(t *testing.T) {
	metadata := &escrow.EscrowRealmMetadata{
		Scope:              escrow.EscrowRealmScopeMarketplace,
		ProviderProfile:    "alpha-ops",
		ArbitrationFeeBps:  150,
		FeeRecipientBech32: testBech32(t, 0x11),
	}
	realm := &escrow.EscrowRealm{
		ID:              "realm-ops",
		Version:         2,
		NextPolicyNonce: 5,
		CreatedAt:       100,
		UpdatedAt:       200,
		Arbitrators: &escrow.ArbitratorSet{
			Scheme:    escrow.ArbitrationSchemeCommittee,
			Threshold: 2,
			Members:   [][20]byte{{1}, {2}},
		},
		Metadata: metadata,
	}
	result := formatRealmResult(realm)
	payload := marshalOrFail(t, result)
	if !strings.Contains(payload, "\"metadata\"") {
		t.Fatalf("expected metadata in payload: %s", payload)
	}
	if !strings.Contains(payload, "\"scope\":\"marketplace\"") {
		t.Fatalf("expected scope in payload: %s", payload)
	}
	if !strings.Contains(payload, metadata.FeeRecipientBech32) {
		t.Fatalf("expected fee recipient in payload: %s", payload)
	}
}

func TestFormatSnapshotResultIncludesFrozenMetadata(t *testing.T) {
	feeRecipient := testBech32(t, 0x22)
	frozenMeta := &escrow.EscrowRealmMetadata{
		Scope:              escrow.EscrowRealmScopePlatform,
		ProviderProfile:    "core",
		ArbitrationFeeBps:  0,
		FeeRecipientBech32: "",
	}
	frozen := &escrow.FrozenArb{
		RealmID:      "realm-ops",
		RealmVersion: 3,
		PolicyNonce:  7,
		Scheme:       escrow.ArbitrationSchemeCommittee,
		Threshold:    2,
		Members:      [][20]byte{{3}, {4}},
		FrozenAt:     300,
		Metadata:     frozenMeta,
	}
	esc := &escrow.Escrow{
		ID:        [32]byte{0x01},
		Payer:     [20]byte{0x02},
		Payee:     [20]byte{0x03},
		Token:     "NHB",
		Amount:    big.NewInt(500),
		FeeBps:    25,
		Deadline:  400,
		CreatedAt: 250,
		Nonce:     8,
		Status:    escrow.EscrowFunded,
		MetaHash:  [32]byte{0xFF},
		RealmID:   "realm-ops",
		FrozenArb: frozen,
	}
	frozen.Metadata.FeeRecipientBech32 = feeRecipient
	result := formatSnapshotResult(esc)
	payload := marshalOrFail(t, result)
	if !strings.Contains(payload, "\"metadata\"") {
		t.Fatalf("expected metadata in payload: %s", payload)
	}
	if !strings.Contains(payload, feeRecipient) {
		t.Fatalf("expected fee recipient in payload: %s", payload)
	}
	if !strings.Contains(payload, "\"scope\":\"platform\"") {
		t.Fatalf("expected scope in payload: %s", payload)
	}
}
