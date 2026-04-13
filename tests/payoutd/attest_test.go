package payoutd_test

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nhbchain/native/swap"
	"nhbchain/services/payoutd"
	"nhbchain/services/payoutd/wallet"
)

type trackingWallet struct {
	mu        sync.Mutex
	transfers int
	waits     int
}

func (w *trackingWallet) Transfer(ctx context.Context, asset, dest string, amount *big.Int) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.transfers++
	return fmt.Sprintf("tx-%d", w.transfers), nil
}

func (w *trackingWallet) WaitForConfirmations(ctx context.Context, tx string, confs int, interval time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.waits++
	return nil
}

type recordingAttestor struct {
	mu       sync.Mutex
	receipts []payoutd.Receipt
	aborts   []struct {
		ID     string
		Reason string
	}
}

func (a *recordingAttestor) SubmitReceipt(ctx context.Context, receipt payoutd.Receipt) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.receipts = append(a.receipts, receipt)
	return nil
}

func (a *recordingAttestor) AbortIntent(ctx context.Context, intentID, reason string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.aborts = append(a.aborts, struct {
		ID     string
		Reason string
	}{ID: intentID, Reason: reason})
	return nil
}

func TestSuccessfulPayout_AttestsOnce(t *testing.T) {
	policies := []payoutd.Policy{{
		Asset:         "USDC",
		DailyCap:      big.NewInt(2_000_000_000),
		SoftInventory: big.NewInt(2_000_000_000),
		Confirmations: 1,
	}}
	enforcer, err := payoutd.NewPolicyEnforcer(policies)
	require.NoError(t, err)
	enforcer.SetInventory("USDC", big.NewInt(2_000_000_000))

	wallet := &trackingWallet{}
	attestor := &recordingAttestor{}
	baseTime := time.Unix(1700000000, 0)
	processor := payoutd.NewProcessor(enforcer,
		payoutd.WithWallet(wallet),
		payoutd.WithAttestor(attestor),
		payoutd.WithClock(func() time.Time { return baseTime }),
	)

	intent := &swap.CashOutIntent{
		IntentID:     "intent-abc",
		StableAsset:  swap.StableAsset("USDC"),
		StableAmount: big.NewInt(100_000_000),
		NhbAmount:    big.NewInt(100_000_000),
	}

	err = processor.Process(context.Background(), payoutd.CashOutRequest{Intent: intent, Destination: "0xabc"})
	require.NoError(t, err)

	// Replay the same intent to ensure idempotency.
	err = processor.Process(context.Background(), payoutd.CashOutRequest{Intent: intent, Destination: "0xabc"})
	require.NoError(t, err)

	require.Equal(t, 1, wallet.transfers)
	require.Equal(t, 1, wallet.waits)
	require.Len(t, attestor.receipts, 1)
	require.Equal(t, "intent-abc", attestor.receipts[0].IntentID)
}

func TestAbort_ReturnsNHB(t *testing.T) {
	policies := []payoutd.Policy{{
		Asset:         "USDC",
		DailyCap:      big.NewInt(1_000_000_000),
		SoftInventory: big.NewInt(1_000_000_000),
		Confirmations: 1,
	}}
	enforcer, err := payoutd.NewPolicyEnforcer(policies)
	require.NoError(t, err)
	enforcer.SetInventory("USDC", big.NewInt(1_000_000_000))

	wallet := &trackingWallet{}
	attestor := &recordingAttestor{}
	processor := payoutd.NewProcessor(enforcer,
		payoutd.WithWallet(wallet),
		payoutd.WithAttestor(attestor),
		payoutd.WithClock(func() time.Time { return time.Unix(1700000000, 0) }),
	)

	err = processor.Abort(context.Background(), "intent-abort", "fraudulent")
	require.NoError(t, err)
	require.Len(t, attestor.aborts, 1)
	require.Equal(t, "intent-abort", attestor.aborts[0].ID)
	require.Equal(t, "fraudulent", attestor.aborts[0].Reason)

	intent := &swap.CashOutIntent{
		IntentID:     "intent-abort",
		StableAsset:  swap.StableAsset("USDC"),
		StableAmount: big.NewInt(50_000_000),
		NhbAmount:    big.NewInt(50_000_000),
	}

	err = processor.Process(context.Background(), payoutd.CashOutRequest{Intent: intent, Destination: "0xabc"})
	require.ErrorIs(t, err, payoutd.ErrIntentAborted)
	require.Equal(t, 0, wallet.transfers, "wallet should not execute transfers for aborted intents")
}

var _ wallet.ERC20Wallet = (*trackingWallet)(nil)
