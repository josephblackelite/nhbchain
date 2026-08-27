package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
)

// partiallyPaidGraceWindow is how long a headless payment sits in
// NOWPayments' partially_paid status, with no further update from
// NOWPayments at all (see ListPaymentsByStatusOlderThan), before the
// reconciler treats it as final and settles it for whatever actually
// landed. NOWPayments does not auto-advance partially_paid to finished on
// its own -- confirmed against their own docs, this is by design, since
// they don't want to unilaterally decide a merchant is fine with a short
// payment -- so without this, an underpaid deposit with no buyer follow-up
// would sit unresolved forever. The window exists to give a buyer who
// notices the shortfall (e.g. from the checkout UI's own error messaging)
// a real chance to send a top-up to the same deposit address before we
// lock in the lower amount; it is not a security control.
//
// 3 minutes (not 15): long enough for a real TRC20/on-chain top-up to
// broadcast and confirm if the buyer acts right away, short enough that
// the checkout UI's new partial-payment countdown (see
// CryptoCheckoutModal.svelte) doesn't leave someone staring at a stuck
// screen for a quarter of an hour once we've already detected their money
// arrived.
const partiallyPaidGraceWindow = 3 * time.Minute

// reconcileInterval is how often the sweep runs. Cheap and safe to run
// frequently: each tick only touches rows that are already stale.
const reconcileInterval = 1 * time.Minute

// runPaymentReconciler is the background half of "mint whatever nets to
// us, no manual review, ever" -- the webhook path alone only fires when
// NOWPayments tells us something changed, so a payment that goes quiet in
// partially_paid (the buyer never tops up, or already gave up) would
// never get another look without this. Runs until ctx is cancelled (see
// main.go's shutdown handling).
func (s *Server) runPaymentReconciler(ctx context.Context) {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileStalePartialPayments(ctx)
		}
	}
}

func (s *Server) reconcileStalePartialPayments(ctx context.Context) {
	cutoff := s.nowFn().UTC().Add(-partiallyPaidGraceWindow)
	stale, err := s.store.ListPaymentsByStatusOlderThan(ctx, "partially_paid", cutoff)
	if err != nil {
		log.Printf("payments-gateway: reconciler: list stale partial payments: %v", err)
		return
	}
	for i := range stale {
		s.settleStalePartialPayment(ctx, &stale[i])
	}
}

// settleStalePartialPayment re-checks a single stale partially_paid
// payment against NOWPayments' live status -- never trusting the local
// snapshot that made it eligible, since a webhook may have landed (a
// top-up, or a transition all the way to finished) in the time between
// the sweep's list query and this call -- and settles it for whatever
// actually nets to us. Mirrors handlePaymentWebhook's settlement logic
// exactly (same settlePayment call), just reached from a timer instead of
// an inbound webhook.
func (s *Server) settleStalePartialPayment(ctx context.Context, payment *PaymentRecord) {
	current, err := s.store.GetPayment(ctx, payment.ID)
	if err != nil {
		log.Printf("payments-gateway: reconciler: re-read payment %s: %v", payment.ID, err)
		return
	}
	if current == nil || !strings.EqualFold(current.Status, "partially_paid") {
		// Already moved on (minted by a concurrent webhook, expired,
		// errored, or deleted) since the sweep listed it -- nothing to do.
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	latest, err := s.nowPayments.GetPayment(reqCtx, current.NowID)
	if err != nil {
		log.Printf("payments-gateway: reconciler: refresh payment %s (nowpayments id %s): %v", current.ID, current.NowID, err)
		return
	}
	livePendingStatus := strings.ToLower(strings.TrimSpace(latest.PaymentStatus))
	if livePendingStatus != "partially_paid" && !latest.Finished() {
		// NOWPayments' live view no longer agrees this is settleable --
		// record whatever it now says and let the normal webhook/next
		// sweep handle it from there.
		if livePendingStatus == "" {
			livePendingStatus = "pending"
		}
		if actuallyPaid := strings.TrimSpace(string(latest.ActuallyPaid)); actuallyPaid != "" {
			_ = s.store.UpdatePaymentStatus(ctx, current.ID, livePendingStatus, nil, actuallyPaid)
		} else {
			_ = s.store.UpdatePaymentStatus(ctx, current.ID, livePendingStatus, nil)
		}
		return
	}

	quote, err := s.store.GetQuote(ctx, current.QuoteID)
	if err != nil {
		log.Printf("payments-gateway: reconciler: load quote for payment %s: %v", current.ID, err)
		return
	}
	if quote == nil {
		log.Printf("payments-gateway: reconciler: quote %s missing for payment %s -- skipping", current.QuoteID, current.ID)
		return
	}

	outcomeAmount := strings.TrimSpace(string(latest.OutcomeAmount))
	if outcomeAmount == "" {
		outcomeAmount = strings.TrimSpace(string(latest.ActuallyPaid))
	}
	txHash, mintAmount, err := s.settlePayment(reqCtx, current, quote, outcomeAmount)
	if err != nil {
		if errors.Is(err, ErrMintDuplicate) {
			// A concurrent webhook already settled this exact payment
			// between our re-read above and this call -- our own
			// settlePayment attempt got rejected by the chain's replay
			// protection (same onChainID already minted). Expected under
			// normal operation, not a real failure: leave status alone
			// rather than overwriting whatever the webhook path already
			// wrote with "error".
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("payments-gateway: reconciler: settle payment %s timed out: %v", current.ID, err)
			return
		}
		log.Printf("payments-gateway: reconciler: settle payment %s failed: %v", current.ID, err)
		_ = s.store.UpdatePaymentStatus(ctx, current.ID, "error", nil)
		return
	}
	var txHashPtr *string
	if txHash != "" {
		txHashPtr = &txHash
	}
	finalStatus := finalPaymentStatus(mintAmount)
	_ = s.store.UpdatePaymentStatus(ctx, current.ID, finalStatus, txHashPtr)
	log.Printf("payments-gateway: reconciler: settled stale partially_paid payment %s status=%s for %s %s (tx=%s)", current.ID, finalStatus, mintAmount, quote.MintAsset, txHash)
}
