package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// RedemptionNotifier lets the redeem watcher report a redemption's
// CONFIRMED on-chain outcome (paid or failed) to an external service --
// payments-gateway has no user/email table of its own (only the on-chain
// wallet address), so it cannot notify a redeemer directly. nhbportal is
// the only deployment target today (its own Wallet<->User linkage resolves
// an address to an email -- see resolvePayerEmail's doc comment on that
// side), reached via HTTPRedemptionNotifier below.
type RedemptionNotifier interface {
	Notify(ctx context.Context, event RedemptionOutcomeEvent) error
}

// RedemptionOutcomeEvent is the payload posted to the configured notify
// URL. Field names are the wire contract with nhbportal's
// /api/internal/redemption-notify handler -- keep both sides in sync.
type RedemptionOutcomeEvent struct {
	RequestID       string `json:"requestId"`
	Account         string `json:"account"`
	Outcome         string `json:"outcome"`
	NHBAmountWei    string `json:"nhbAmountWei"`
	PayoutReference string `json:"payoutReference,omitempty"`
	FailureReason   string `json:"failureReason,omitempty"`
	Refunded        bool   `json:"refunded"`
}

// HTTPRedemptionNotifier posts RedemptionOutcomeEvent to a configured URL,
// authenticated with a dedicated bearer secret (least-privilege: this
// credential can only ever trigger one notification email, nothing else --
// mirrors the isolation already used for every other cross-service
// credential in this package, e.g. AttestorKMSEnv vs MinterKMSEnv).
//
// Best-effort by design -- see RedeemWatcher.processAttesting's call site:
// Notify's own error is logged there and never retried, and must never be
// allowed to affect the on-chain-critical state machine. A redemption's
// terminal on-chain state (and this deployment's own refund, when failed)
// is already fully durable by the time Notify is ever called; a missed or
// failed notification email is a real but strictly lesser gap than the
// money-moving state machine it must never be allowed to influence.
type HTTPRedemptionNotifier struct {
	url    string
	secret string
	http   *http.Client
}

// NewHTTPRedemptionNotifier constructs a notifier. Returns nil (a valid,
// nil-safe RedemptionNotifier -- Notify on a nil receiver is a no-op) if
// url or secret is blank, so main.go can construct this unconditionally and
// only wire it in when both are actually configured.
func NewHTTPRedemptionNotifier(url, secret string) *HTTPRedemptionNotifier {
	trimmedURL := strings.TrimSpace(url)
	trimmedSecret := strings.TrimSpace(secret)
	if trimmedURL == "" || trimmedSecret == "" {
		return nil
	}
	return &HTTPRedemptionNotifier{
		url:    trimmedURL,
		secret: trimmedSecret,
		http:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Notify posts event to the configured URL. Safe to call on a nil receiver.
func (n *HTTPRedemptionNotifier) Notify(ctx context.Context, event RedemptionOutcomeEvent) error {
	if n == nil {
		return nil
	}
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode redemption notify payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+n.secret)
	resp, err := n.http.Do(req)
	if err != nil {
		return fmt.Errorf("redemption notify request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("redemption notify failed: status=%d", resp.StatusCode)
	}
	return nil
}
