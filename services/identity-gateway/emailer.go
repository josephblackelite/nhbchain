package identitygateway

import (
	"context"
	"log"
)

// VerificationMessage captures the payload dispatched to email providers.
type VerificationMessage struct {
	Email     string
	Code      string
	AliasHint string
	ExpiresAt string
}

// Emailer abstracts delivery of verification codes.
type Emailer interface {
	SendVerification(ctx context.Context, msg VerificationMessage) error
}

// LogEmailer logs verification messages to stdout. Intended for development and testing.
type LogEmailer struct {
	Logger *log.Logger
}

// SendVerification logs the email payload.
func (l *LogEmailer) SendVerification(ctx context.Context, msg VerificationMessage) error {
	logger := l.Logger
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf("identity-gateway: deliver verification code to %s (alias hint=%s, expires=%s)", msg.Email, msg.AliasHint, msg.ExpiresAt)
	return nil
}
