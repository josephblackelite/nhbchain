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

// LogEmailer is the only Emailer this repository ships. It writes the address a
// code was meant for to the log and DELIVERS NOTHING: the code itself is never
// logged, so no user receives it. It exists for development and tests; a
// deployment that needs real sign-ups supplies its own Emailer.
type LogEmailer struct {
	Logger *log.Logger
}

// SendVerification logs the email payload.
func (l *LogEmailer) SendVerification(ctx context.Context, msg VerificationMessage) error {
	logger := l.Logger
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf("identity-gateway: verification code for %s (alias hint=%s, expires=%s) was NOT delivered: this build has no email provider and only logs the address", msg.Email, msg.AliasHint, msg.ExpiresAt)
	return nil
}
