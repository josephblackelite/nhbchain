package auth

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// A limit the operator configured that the authenticator lowers to its fixed
// maximum must be said, or it looks in force and is not.
func TestNewAuthenticatorSaysWhenItLowersAConfiguredLimit(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(previous)

	NewAuthenticator(map[string]string{"k": "s"}, time.Minute, 5*time.Minute, 1000, nil, nil)
	if buf.Len() != 0 {
		t.Fatalf("values inside the limits were reported: %s", buf.String())
	}

	NewAuthenticator(map[string]string{"k": "s"}, time.Hour, time.Hour, 1_000_000, nil, nil)
	got := buf.String()
	for _, want := range []string{"timestamp skew", "nonce TTL", "nonce capacity"} {
		if !strings.Contains(got, want) {
			t.Fatalf("no warning about the %s being lowered: %s", want, got)
		}
	}
}
