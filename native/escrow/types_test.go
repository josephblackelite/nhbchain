package escrow

import (
	"strings"
	"testing"

	"nhbchain/crypto"
)

func stubAddress(fill byte) [20]byte {
	var addr [20]byte
	for i := range addr {
		addr[i] = fill
	}
	return addr
}

func stubMetadata() *EscrowRealmMetadata {
	return &EscrowRealmMetadata{Scope: EscrowRealmScopePlatform, ProviderProfile: "ops-team", ArbitrationFeeBps: 0}
}

func TestSanitizeEscrowRealmRequiresMetadata(t *testing.T) {
	realm := &EscrowRealm{
		ID:              "realm-1",
		Version:         1,
		NextPolicyNonce: 1,
		Arbitrators: &ArbitratorSet{
			Scheme:    ArbitrationSchemeSingle,
			Threshold: 1,
			Members:   [][20]byte{stubAddress(0x01)},
		},
	}
	if _, err := SanitizeEscrowRealm(realm); err == nil {
		t.Fatalf("expected metadata requirement error")
	}
	realm.Metadata = stubMetadata()
	if _, err := SanitizeEscrowRealm(realm); err != nil {
		t.Fatalf("unexpected sanitize error: %v", err)
	}
}

func TestSanitizeEscrowRealmMetadataValidation(t *testing.T) {
	valid := stubMetadata()
	if _, err := SanitizeEscrowRealmMetadata(valid); err != nil {
		t.Fatalf("unexpected error for valid metadata: %v", err)
	}
	missingProfile := valid.Clone()
	missingProfile.ProviderProfile = ""
	if _, err := SanitizeEscrowRealmMetadata(missingProfile); err == nil {
		t.Fatalf("expected provider profile requirement error")
	}
	tooLong := valid.Clone()
	tooLong.ProviderProfile = strings.Repeat("a", EscrowRealmMaxProviderProfileLength+1)
	if _, err := SanitizeEscrowRealmMetadata(tooLong); err == nil {
		t.Fatalf("expected provider profile length error")
	}
	missingRecipient := valid.Clone()
	missingRecipient.ArbitrationFeeBps = 10
	missingRecipient.FeeRecipientBech32 = ""
	if _, err := SanitizeEscrowRealmMetadata(missingRecipient); err == nil {
		t.Fatalf("expected fee recipient requirement error")
	}
	invalidRecipient := valid.Clone()
	invalidRecipient.ArbitrationFeeBps = 10
	invalidRecipient.FeeRecipientBech32 = "invalid"
	if _, err := SanitizeEscrowRealmMetadata(invalidRecipient); err == nil {
		t.Fatalf("expected invalid bech32 recipient error")
	}
	validRecipient := valid.Clone()
	validRecipient.ArbitrationFeeBps = 25
	addr := stubAddress(0xAB)
	validRecipient.FeeRecipientBech32 = crypto.MustNewAddress(crypto.NHBPrefix, addr[:]).String()
	if _, err := SanitizeEscrowRealmMetadata(validRecipient); err != nil {
		t.Fatalf("unexpected error for metadata with recipient: %v", err)
	}
}

func TestSanitizeFrozenArbRequiresMetadata(t *testing.T) {
	frozen := &FrozenArb{
		RealmID:      "realm-1",
		RealmVersion: 1,
		PolicyNonce:  1,
		Scheme:       ArbitrationSchemeSingle,
		Threshold:    1,
		Members:      [][20]byte{stubAddress(0xFF)},
		FrozenAt:     123,
	}
	if _, err := SanitizeFrozenArb(frozen); err == nil {
		t.Fatalf("expected frozen metadata requirement error")
	}
	frozen.Metadata = stubMetadata()
	if _, err := SanitizeFrozenArb(frozen); err != nil {
		t.Fatalf("unexpected sanitize error: %v", err)
	}
}
