package payoutd_test

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nhbchain/native/swap"
	"nhbchain/services/payoutd"
	"nhbchain/services/payoutd/wallet"
)

type mockWallet struct {
	transfers int
}

func (m *mockWallet) Transfer(context.Context, string, string, *big.Int) (string, error) {
	m.transfers++
	return "tx-hash", nil
}

func (m *mockWallet) WaitForConfirmations(context.Context, string, int, time.Duration) error {
	return nil
}

type noopAttestor struct{}

func (noopAttestor) SubmitReceipt(context.Context, payoutd.Receipt) error { return nil }
func (noopAttestor) AbortIntent(context.Context, string, string) error    { return nil }

func TestOverCap_Reject(t *testing.T) {
	policies := []payoutd.Policy{{
		Asset:         "USDC",
		DailyCap:      big.NewInt(1_000_000_000),
		SoftInventory: big.NewInt(2_000_000_000),
		Confirmations: 1,
	}}
	enforcer, err := payoutd.NewPolicyEnforcer(policies)
	require.NoError(t, err)
	enforcer.SetInventory("USDC", big.NewInt(2_000_000_000))

	mock := &mockWallet{}
	processor := payoutd.NewProcessor(enforcer,
		payoutd.WithWallet(mock),
		payoutd.WithAttestor(noopAttestor{}),
		payoutd.WithClock(func() time.Time { return time.Unix(1700000000, 0) }),
	)

	intent := &swap.CashOutIntent{
		IntentID:     "intent-1",
		StableAsset:  swap.StableAsset("USDC"),
		StableAmount: big.NewInt(900_000_000),
		NhbAmount:    big.NewInt(900_000_000),
	}

	err = processor.Process(context.Background(), payoutd.CashOutRequest{
		Intent:      intent,
		Destination: "0xrecipient",
	})
	require.NoError(t, err)
	require.Equal(t, 1, mock.transfers)

	next := &swap.CashOutIntent{
		IntentID:     "intent-2",
		StableAsset:  swap.StableAsset("USDC"),
		StableAmount: big.NewInt(200_000_000),
		NhbAmount:    big.NewInt(200_000_000),
	}

	err = processor.Process(context.Background(), payoutd.CashOutRequest{
		Intent:      next,
		Destination: "0xrecipient",
	})
	require.ErrorIs(t, err, payoutd.ErrDailyCapExceeded)
	require.Equal(t, 1, mock.transfers, "should not execute second transfer when over cap")
}

var _ wallet.ERC20Wallet = (*mockWallet)(nil)
