package evidence

import (
	"bytes"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// signedDowntime builds a DOWNTIME report (no proof needed) signed by a fresh
// reporter key, with the heights, details and timestamp given.
func signedDowntime(t *testing.T, heights []uint64, details []byte, timestamp int64) (*Evidence, [32]byte) {
	t.Helper()
	priv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var reporter [20]byte
	copy(reporter[:], ethcrypto.PubkeyToAddress(priv.PublicKey).Bytes())
	e := &Evidence{
		Type:      TypeDowntime,
		Offender:  [20]byte{9},
		Heights:   heights,
		Details:   details,
		Reporter:  reporter,
		Timestamp: timestamp,
	}
	hash, err := e.CanonicalHash()
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	sig, err := ethcrypto.Sign(e.SigningDigest(hash), priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	e.ReporterSig = sig
	return e, hash
}

func ascendingHeights(n int) []uint64 {
	heights := make([]uint64, n)
	for i := range heights {
		heights[i] = uint64(i + 1)
	}
	return heights
}

// TestValidateEvidenceBoundsPayloadSize: a record is stored for its whole
// retention window, so a report is bounded in size. The limit itself is
// accepted; one past it is refused before any signature work is done.
func TestValidateEvidenceBoundsPayloadSize(t *testing.T) {
	cases := []struct {
		name    string
		heights []uint64
		details []byte
		want    RejectReason
	}{
		{"max heights", ascendingHeights(MaxHeightsPerEvidence), nil, ""},
		{"one height too many", ascendingHeights(MaxHeightsPerEvidence + 1), nil, RejectReasonOversized},
		{"max details", []uint64{1}, bytes.Repeat([]byte{'d'}, MaxDetailsBytes), ""},
		{"details one byte too long", []uint64{1}, bytes.Repeat([]byte{'d'}, MaxDetailsBytes+1), RejectReasonOversized},
	}
	for _, tc := range cases {
		e, hash := signedDowntime(t, tc.heights, tc.details, 1)
		verr := ValidateEvidence(e, hash, 1_000, 0, nil)
		switch {
		case tc.want == "" && verr != nil:
			t.Errorf("%s: expected acceptance, got %v", tc.name, verr)
		case tc.want != "" && (verr == nil || verr.Reason != tc.want):
			t.Errorf("%s: expected reason %q, got %v", tc.name, tc.want, verr)
		}
	}
}

func TestValidateEvidenceRejectsNegativeTimestamp(t *testing.T) {
	e, hash := signedDowntime(t, []uint64{1}, nil, -1)
	verr := ValidateEvidence(e, hash, 10, 0, nil)
	if verr == nil || verr.Reason != RejectReasonInvalidTimestamp {
		t.Fatalf("expected %q, got %v", RejectReasonInvalidTimestamp, verr)
	}
	if e, hash = signedDowntime(t, []uint64{1}, nil, 0); ValidateEvidence(e, hash, 10, 0, nil) != nil {
		t.Fatalf("expected a zero timestamp to remain valid")
	}
}
