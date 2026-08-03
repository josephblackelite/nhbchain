package core

import (
	"crypto/ecdsa"
	"math/big"
	"strings"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	swapv1 "nhbchain/proto/swap/v1"
)

func TestApplySwapPayoutReceiptRequiresRecoverableSignature(t *testing.T) {
	sp, _ := newTestStateProcessor(t)

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

// Authorization now comes from the recovered signer holding
// RoleSwapPayoutAttestor, not from the payload's self-declared Authority
// string -- these two tests prove that directly: a signer WITH the role is
// accepted regardless of what it claims in Authority, and a signer WITHOUT
// it is rejected even when it claims to be "treasury" (the exact bug this
// fix closes -- see applySwapPayoutReceipt's doc comment).

func TestApplySwapPayoutReceiptAcceptsAuthorizedAttestor(t *testing.T) {
	sp, _ := newTestStateProcessor(t)

	attestorKey, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate attestor key: %v", err)
	}
	attestorAddr := ethcrypto.PubkeyToAddress(attestorKey.PublicKey).Bytes()
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetRole(RoleSwapPayoutAttestor, attestorAddr); err != nil {
		t.Fatalf("grant attestor role: %v", err)
	}

	tx := buildSignedSwapPayoutReceiptTx(t, "treasury", attestorKey)
	err = sp.applySwapPayoutReceipt(tx)
	if err == nil {
		t.Fatalf("expected missing intent error")
	}
	if strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("unexpected authority rejection: %v", err)
	}
}

func TestApplySwapPayoutReceiptRejectsUnauthorizedAttestor(t *testing.T) {
	sp, _ := newTestStateProcessor(t)

	fraudsterKey, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate fraudster key: %v", err)
	}

	// Claiming to be "treasury" in the payload must not matter -- this
	// signer holds no role at all.
	tx := buildSignedSwapPayoutReceiptTx(t, "treasury", fraudsterKey)
	err = sp.applySwapPayoutReceipt(tx)
	if err == nil {
		t.Fatalf("expected unauthorized attestor error")
	}
	if !strings.Contains(err.Error(), "unauthorized payout attestor") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func buildSignedSwapPayoutReceiptTx(t *testing.T, authority string, key *ecdsa.PrivateKey) *types.Transaction {
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
	if err := tx.Sign(key); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	return tx
}
