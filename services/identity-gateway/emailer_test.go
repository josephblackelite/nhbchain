package identitygateway

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
)

// The log emailer delivers nothing, and its log line must say so rather than
// suggest that a code was sent (PL-DC-42). The code itself is never logged.
func TestLogEmailerSaysNothingWasDelivered(t *testing.T) {
	var buf bytes.Buffer
	emailer := &LogEmailer{Logger: log.New(&buf, "", 0)}
	err := emailer.SendVerification(context.Background(), VerificationMessage{
		Email: "user@example.invalid", Code: "123456", AliasHint: "al", ExpiresAt: "soon",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "NOT delivered") {
		t.Fatalf("the log line must say the code was not delivered, got %q", out)
	}
	if strings.Contains(out, "123456") {
		t.Fatalf("the verification code must not be logged, got %q", out)
	}
	if !strings.Contains(out, "user@example.invalid") {
		t.Fatalf("the log line should still name the address, got %q", out)
	}
}
