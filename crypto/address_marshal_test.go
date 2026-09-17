package crypto

import (
	"encoding/json"
	"testing"
)

// TestAddressMarshalTextRoundTrip locks in the fix for Address never
// serializing to JSON (previously rendered as "{}" via any bare
// json.Marshal, since prefix/bytes are unexported) -- gov_proposal's
// Submitter and lend_getMarket's DeveloperOwner/DeveloperFeeCollector all
// depend on this.
func TestAddressMarshalTextRoundTrip(t *testing.T) {
	original := MustNewAddress(NHBPrefix, make([]byte, 20))
	for i := range original.bytes {
		original.bytes[i] = byte(i + 1)
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wantQuoted string
	if err := json.Unmarshal(encoded, &wantQuoted); err != nil {
		t.Fatalf("encoded value is not a plain JSON string: %v (got %s)", err, encoded)
	}
	if wantQuoted != original.String() {
		t.Fatalf("marshaled text = %q, want %q (Address.String())", wantQuoted, original.String())
	}

	var decoded Address
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.String() != original.String() {
		t.Fatalf("round-trip mismatch: got %s, want %s", decoded.String(), original.String())
	}
}

// TestAddressMarshalTextZeroValue confirms the zero-value Address{} (real
// in production -- e.g. a lending market's DeveloperFeeCollector when
// developer fees are disabled for that pool) marshals to an empty string,
// not the syntactically-valid-but-meaningless empty-HRP bech32 string
// bech32.Encode("", nil) would otherwise produce.
func TestAddressMarshalTextZeroValue(t *testing.T) {
	var zero Address

	encoded, err := json.Marshal(zero)
	if err != nil {
		t.Fatalf("marshal zero value: %v", err)
	}
	if string(encoded) != `""` {
		t.Fatalf(`marshal(Address{}) = %s, want ""`, encoded)
	}

	var decoded Address
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal empty string: %v", err)
	}
	if len(decoded.bytes) != 0 || decoded.prefix != "" {
		t.Fatalf("round-tripping an empty string must produce the zero value, got prefix=%q bytes=%v", decoded.prefix, decoded.bytes)
	}
}

// TestAddressMarshalTextInsideStruct confirms the fix works through a
// real containing struct exactly the way native/governance/types.go's
// Proposal.Submitter and native/lending/types.go's Market.DeveloperOwner
// actually use it -- a plain (non-pointer) Address field.
func TestAddressMarshalTextInsideStruct(t *testing.T) {
	type wrapper struct {
		Submitter Address `json:"submitter"`
	}
	addr := MustNewAddress(NHBPrefix, []byte{
		1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20,
	})
	encoded, err := json.Marshal(wrapper{Submitter: addr})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Submitter string `json:"submitter"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Submitter != addr.String() {
		t.Fatalf("submitter field = %q, want %q -- this is exactly the gov_proposal/gov_list bug (used to render as {})", decoded.Submitter, addr.String())
	}
}

// TestAddressUnmarshalTextInvalidRejectsGracefully confirms a malformed
// bech32 string is a real decode error, not a silent zero-value or panic.
func TestAddressUnmarshalTextInvalidRejectsGracefully(t *testing.T) {
	var decoded Address
	err := json.Unmarshal([]byte(`"not-a-real-address"`), &decoded)
	if err == nil {
		t.Fatalf("expected an error decoding a malformed address, got nil")
	}
}
