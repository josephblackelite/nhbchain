package state

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"
)

func TestEncodeUsernameIndexRoundTrip(t *testing.T) {
	original := map[string][]byte{
		"validator": {0x01, 0x02, 0x03},
		"delegator": nil,
	}
	encoded, err := EncodeUsernameIndex(original)
	if err != nil {
		t.Fatalf("encode username index: %v", err)
	}
	decoded, err := DecodeUsernameIndex(encoded)
	if err != nil {
		t.Fatalf("decode username index: %v", err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("decoded entries mismatch: got %d want %d", len(decoded), len(original))
	}
	for key, want := range original {
		got, ok := decoded[key]
		if !ok {
			t.Fatalf("missing username %q", key)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("username %q address mismatch: got %x want %x", key, got, want)
		}
	}
}

func TestEncodeUsernameIndexDeterministic(t *testing.T) {
	index := map[string][]byte{
		"charlie": {0x03},
		"alpha":   {0x01},
		"bravo":   {0x02},
	}
	encoded, err := EncodeUsernameIndex(index)
	if err != nil {
		t.Fatalf("encode username index: %v", err)
	}
	expectedEntries := []usernameIndexEntry{
		{Username: "alpha", Address: []byte{0x01}},
		{Username: "bravo", Address: []byte{0x02}},
		{Username: "charlie", Address: []byte{0x03}},
	}
	expected, err := rlp.EncodeToBytes(expectedEntries)
	if err != nil {
		t.Fatalf("encode expected entries: %v", err)
	}
	if !bytes.Equal(encoded, expected) {
		t.Fatalf("encoded bytes not deterministic: got %x want %x", encoded, expected)
	}
}

func TestDecodeUsernameIndexEmpty(t *testing.T) {
	decoded, err := DecodeUsernameIndex(nil)
	if err != nil {
		t.Fatalf("decode nil: %v", err)
	}
	if len(decoded) != 0 {
		t.Fatalf("expected empty map for nil input, got %d entries", len(decoded))
	}
}
