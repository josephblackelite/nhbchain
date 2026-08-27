package settlement

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// totpDigits/totpPeriod are RFC 6238's defaults, matching the standard
// Google-Authenticator-compatible scheme NOWPayments' TOTP setup (QR code +
// base32 manual key) uses -- confirmed empirically against the live account
// during 2FA setup on 2026-08-24 (a code generated with exactly these
// parameters was accepted by NOWPayments' own confirmation modal).
const (
	totpDigits = 6
	totpPeriod = 30 * time.Second
)

// generateTOTP computes the current RFC 6238 TOTP code for a base32-encoded
// secret, the same format an authenticator app's QR code/manual-entry key
// uses. at is the moment to generate the code for -- pass time.Now() in
// production, a fixed time in tests.
func generateTOTP(secretBase32 string, at time.Time) (string, error) {
	trimmed := strings.ToUpper(strings.TrimSpace(secretBase32))
	if trimmed == "" {
		return "", fmt.Errorf("settlement: totp secret required")
	}
	// Authenticator-app secrets are conventionally shown unpadded;
	// base32.StdEncoding requires padding to a multiple of 8 characters.
	if rem := len(trimmed) % 8; rem != 0 {
		trimmed += strings.Repeat("=", 8-rem)
	}
	key, err := base32.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return "", fmt.Errorf("settlement: decode totp secret: %w", err)
	}
	counter := uint64(at.Unix()) / uint64(totpPeriod.Seconds())
	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(counterBytes[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0F
	truncated := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7FFFFFFF

	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	code := truncated % mod
	return fmt.Sprintf("%0*d", totpDigits, code), nil
}
