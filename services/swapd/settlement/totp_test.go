package settlement

import (
	"encoding/base32"
	"testing"
	"time"
)

// TestGenerateTOTP_RFC6238Vectors reproduces RFC 6238 Appendix B's official
// SHA1 test vectors, truncated to 6 digits. The RFC publishes 8-digit
// reference values; truncating a decimal number to its last N digits is
// exactly value % 10^N, so taking the last 6 digits of each published
// 8-digit vector is a faithful derivation, not an approximation. The RFC's
// raw 20-byte ASCII seed is base32-encoded here since generateTOTP takes a
// base32 secret (matching how NOWPayments/authenticator apps distribute
// one), not a raw key.
func TestGenerateTOTP_RFC6238Vectors(t *testing.T) {
	secret := base32.StdEncoding.EncodeToString([]byte("12345678901234567890"))
	cases := []struct {
		unixSeconds int64
		want        string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}
	for _, tc := range cases {
		got, err := generateTOTP(secret, time.Unix(tc.unixSeconds, 0).UTC())
		if err != nil {
			t.Fatalf("generateTOTP(%d): %v", tc.unixSeconds, err)
		}
		if got != tc.want {
			t.Errorf("generateTOTP(%d) = %s, want %s", tc.unixSeconds, got, tc.want)
		}
	}
}

func TestGenerateTOTP_RejectsEmptySecret(t *testing.T) {
	if _, err := generateTOTP("", time.Now()); err == nil {
		t.Fatalf("expected error for empty secret")
	}
	if _, err := generateTOTP("   ", time.Now()); err == nil {
		t.Fatalf("expected error for whitespace-only secret")
	}
}

func TestGenerateTOTP_RejectsInvalidBase32(t *testing.T) {
	if _, err := generateTOTP("not-valid-base32!!!", time.Now()); err == nil {
		t.Fatalf("expected error for invalid base32 secret")
	}
}

// TestGenerateTOTP_AcceptsUnpaddedRealWorldSecret confirms a secret shaped
// exactly like what NOWPayments/authenticator apps actually display (16
// unpadded base32 characters) is accepted without error.
func TestGenerateTOTP_AcceptsUnpaddedRealWorldSecret(t *testing.T) {
	code, err := generateTOTP("GURWY2B4MVEEOQLT", time.Now())
	if err != nil {
		t.Fatalf("unexpected error for unpadded real-world-shaped secret: %v", err)
	}
	if len(code) != totpDigits {
		t.Fatalf("expected a %d-digit code, got %q", totpDigits, code)
	}
}

func TestGenerateTOTP_StepChangesEveryPeriod(t *testing.T) {
	secret := base32.StdEncoding.EncodeToString([]byte("12345678901234567890"))
	a, err := generateTOTP(secret, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("generate at t=0: %v", err)
	}
	b, err := generateTOTP(secret, time.Unix(29, 0).UTC())
	if err != nil {
		t.Fatalf("generate at t=29: %v", err)
	}
	if a != b {
		t.Fatalf("expected the same code within one 30s window, got %s vs %s", a, b)
	}
	c, err := generateTOTP(secret, time.Unix(30, 0).UTC())
	if err != nil {
		t.Fatalf("generate at t=30: %v", err)
	}
	if a == c {
		t.Fatalf("expected a different code in the next 30s window, both were %s", a)
	}
}
