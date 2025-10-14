package core

import (
	"math/big"
	"strings"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"nhbchain/core/types"
	swapv1 "nhbchain/proto/swap/v1"
)

func TestApplySwapPayoutReceiptRequiresRecoverableSignature(t *testing.T) {
	sp, _ := newTestStateProcessor(t)
	sp.SetSwapPayoutAuthorities([]string{"treasury"})

	receipt := &swapv1.PayoutReceipt{
		ReceiptId:    "rcpt-1",
		IntentId:     "intent-1",
		StableAsset:  "USDC",
		StableAmount: "1000",
		NhbAmount:    "1000",
		TxHash:       "0xdeadbeef",
		EvidenceUri:  "https://example.com/receipt",
		SettledAt:    1,
	}
	msg := &swapv1.MsgPayoutReceipt{Authority: "treasury", Receipt: receipt}
	packed, err := anypb.New(msg)
	if err != nil {
		t.Fatalf("pack payout receipt: %v", err)
	}
	raw, err := proto.Marshal(packed)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSwapPayoutReceipt,
		GasPrice: big.NewInt(0),
		Data:     raw,
	}
	err = sp.applySwapPayoutReceipt(tx)
	if err == nil {
		t.Fatalf("expected signature recovery error")
	}
	if !strings.Contains(err.Error(), "transaction missing signature") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApplySwapPayoutReceiptAcceptsAuthorizedAuthority(t *testing.T) {
	sp, _ := newTestStateProcessor(t)
	sp.SetSwapPayoutAuthorities([]string{"treasury"})

	tx := buildSignedSwapPayoutReceiptTx(t, "treasury")
	err := sp.applySwapPayoutReceipt(tx)
	if err == nil {
		t.Fatalf("expected missing intent error")
	}
	if strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("unexpected authority rejection: %v", err)
	}
}

func TestApplySwapPayoutReceiptRejectsUnauthorizedAuthority(t *testing.T) {
	sp, _ := newTestStateProcessor(t)
	sp.SetSwapPayoutAuthorities([]string{"treasury"})

	tx := buildSignedSwapPayoutReceiptTx(t, "fraudster")
	err := sp.applySwapPayoutReceipt(tx)
	if err == nil {
		t.Fatalf("expected unauthorized authority error")
	}
	if !strings.Contains(err.Error(), "unauthorized payout authority") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func buildSignedSwapPayoutReceiptTx(t *testing.T, authority string) *types.Transaction {
	t.Helper()
	receipt := &swapv1.PayoutReceipt{
		ReceiptId:    "rcpt-1",
		IntentId:     "intent-1",
		StableAsset:  "USDC",
		StableAmount: "1000",
		NhbAmount:    "1000",
		TxHash:       "0xdeadbeef",
		EvidenceUri:  "https://example.com/receipt",
		SettledAt:    1,
	}
	msg := &swapv1.MsgPayoutReceipt{Authority: authority, Receipt: receipt}
	packed, err := anypb.New(msg)
	if err != nil {
		t.Fatalf("pack payout receipt: %v", err)
	}
	raw, err := proto.Marshal(packed)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSwapPayoutReceipt,
		Nonce:    1,
		GasPrice: big.NewInt(0),
		Data:     raw,
	}
	key, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if err := tx.Sign(key); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	return tx
}
