package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

func redeemNHBTx(t *testing.T, nonce uint64, amount *big.Int, destAsset, destAddr string) *types.Transaction {
	t.Helper()
	payload := struct {
		DestinationAsset   string `json:"destinationAsset"`
		DestinationAddress string `json:"destinationAddress"`
	}{DestinationAsset: destAsset, DestinationAddress: destAddr}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeRedeemNHB,
		Nonce:    nonce,
		Value:    amount,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
		Data:     data,
	}
}

func attestRedemptionTx(t *testing.T, nonce uint64, requestID, status, payoutReference, failureReason string) *types.Transaction {
	t.Helper()
	payload := struct {
		RequestID       string `json:"requestId"`
		Status          string `json:"status"`
		PayoutReference string `json:"payoutReference,omitempty"`
		FailureReason   string `json:"failureReason,omitempty"`
	}{RequestID: requestID, Status: status, PayoutReference: payoutReference, FailureReason: failureReason}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeAttestRedemption,
		Nonce:    nonce,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
		Data:     data,
	}
}

func TestApplyRedeemNHB_BurnsBalanceAndRecordsPendingRequest(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()
	if err := sp.setAccount(userAddr, &types.Account{
		BalanceNHB:  big.NewInt(1_000),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	tx := redeemNHBTx(t, 0, big.NewInt(400), "usdttrc20", "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb")
	if err := tx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	user, err := sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.BalanceNHB.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("expected user NHB balance 600 after burn, got %s", user.BalanceNHB)
	}
	if user.Nonce != 1 {
		t.Fatalf("expected nonce incremented, got %d", user.Nonce)
	}

	txHash, err := tx.Hash()
	if err != nil {
		t.Fatalf("compute tx hash: %v", err)
	}
	requestID := nhbstate.RedemptionRequestID(txHash)
	manager := nhbstate.NewManager(sp.Trie)
	request, ok, err := manager.GetRedemptionRequest(requestID)
	if err != nil {
		t.Fatalf("load request: %v", err)
	}
	if !ok {
		t.Fatalf("expected request %s to be recorded", requestID)
	}
	if request.Status != string(nhbstate.RedemptionStatusPending) {
		t.Fatalf("expected pending status, got %s", request.Status)
	}
	if request.NHBAmountWei != "400" {
		t.Fatalf("expected recorded amount 400, got %s", request.NHBAmountWei)
	}
	if request.DestinationAsset != "USDTTRC20" {
		t.Fatalf("expected uppercased destination asset, got %s", request.DestinationAsset)
	}
}

func TestApplyRedeemNHB_InsufficientBalanceRejected(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()
	if err := sp.setAccount(userAddr, &types.Account{
		BalanceNHB:  big.NewInt(100),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	tx := redeemNHBTx(t, 0, big.NewInt(400), "usdttrc20", "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb")
	if err := tx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("expected rejection for insufficient balance")
	}

	user, err := sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.BalanceNHB.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("balance must be unchanged on rejection, got %s", user.BalanceNHB)
	}
}

func TestApplyRedeemNHB_RequiresDestination(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()
	if err := sp.setAccount(userAddr, &types.Account{
		BalanceNHB:  big.NewInt(1_000),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	tx := redeemNHBTx(t, 0, big.NewInt(400), "", "")
	if err := tx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("expected rejection when destination asset/address are missing")
	}
}

func TestApplyAttestRedemption_RejectsUnauthorizedSigner(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()
	if err := sp.setAccount(userAddr, &types.Account{BalanceNHB: big.NewInt(1_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	burnTx := redeemNHBTx(t, 0, big.NewInt(400), "usdttrc20", "dest")
	if err := burnTx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign burn: %v", err)
	}
	if err := sp.ApplyTransaction(burnTx); err != nil {
		t.Fatalf("apply burn: %v", err)
	}
	burnHash, err := burnTx.Hash()
	if err != nil {
		t.Fatalf("hash burn: %v", err)
	}
	requestID := nhbstate.RedemptionRequestID(burnHash)

	impostorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate impostor key: %v", err)
	}
	impostorAddr := impostorKey.PubKey().Address().Bytes()
	if err := sp.setAccount(impostorAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed impostor: %v", err)
	}
	attestTx := attestRedemptionTx(t, 0, requestID, "paid", "np-payout-1", "")
	if err := attestTx.Sign(impostorKey.PrivateKey); err != nil {
		t.Fatalf("sign attest: %v", err)
	}
	if err := sp.ApplyTransaction(attestTx); err == nil {
		t.Fatalf("expected rejection for signer without RoleSwapPayoutAttestor")
	}

	manager := nhbstate.NewManager(sp.Trie)
	request, ok, err := manager.GetRedemptionRequest(requestID)
	if err != nil {
		t.Fatalf("load request: %v", err)
	}
	if !ok {
		t.Fatalf("expected request to still exist")
	}
	if request.Status != string(nhbstate.RedemptionStatusPending) {
		t.Fatalf("expected request to remain pending after rejected attestation, got %s", request.Status)
	}
}

func TestApplyAttestRedemption_AuthorizedAttestorMarksPaid(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()
	if err := sp.setAccount(userAddr, &types.Account{BalanceNHB: big.NewInt(1_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	burnTx := redeemNHBTx(t, 0, big.NewInt(400), "usdttrc20", "dest")
	if err := burnTx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign burn: %v", err)
	}
	if err := sp.ApplyTransaction(burnTx); err != nil {
		t.Fatalf("apply burn: %v", err)
	}
	burnHash, err := burnTx.Hash()
	if err != nil {
		t.Fatalf("hash burn: %v", err)
	}
	requestID := nhbstate.RedemptionRequestID(burnHash)

	attestorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate attestor key: %v", err)
	}
	attestorAddr := attestorKey.PubKey().Address().Bytes()
	if err := sp.setAccount(attestorAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed attestor: %v", err)
	}
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetRole(RoleSwapPayoutAttestor, attestorAddr); err != nil {
		t.Fatalf("grant attestor role: %v", err)
	}

	attestTx := attestRedemptionTx(t, 0, requestID, "paid", "np-payout-1", "")
	if err := attestTx.Sign(attestorKey.PrivateKey); err != nil {
		t.Fatalf("sign attest: %v", err)
	}
	if err := sp.ApplyTransaction(attestTx); err != nil {
		t.Fatalf("apply attestation: %v", err)
	}

	request, ok, err := manager.GetRedemptionRequest(requestID)
	if err != nil {
		t.Fatalf("load request: %v", err)
	}
	if !ok {
		t.Fatalf("expected request to exist")
	}
	if request.Status != string(nhbstate.RedemptionStatusPaid) {
		t.Fatalf("expected paid status, got %s", request.Status)
	}
	if request.PayoutReference != "np-payout-1" {
		t.Fatalf("expected payout reference recorded, got %s", request.PayoutReference)
	}

	// A settled request cannot be re-attested.
	secondAttest := attestRedemptionTx(t, 1, requestID, "failed", "", "duplicate")
	if err := secondAttest.Sign(attestorKey.PrivateKey); err != nil {
		t.Fatalf("sign second attest: %v", err)
	}
	if err := sp.ApplyTransaction(secondAttest); err == nil {
		t.Fatalf("expected rejection when re-attesting an already-settled request")
	}
}

func TestApplyAttestRedemption_MarksFailedWithoutRefund(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()
	if err := sp.setAccount(userAddr, &types.Account{BalanceNHB: big.NewInt(1_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	burnTx := redeemNHBTx(t, 0, big.NewInt(400), "usdttrc20", "dest")
	if err := burnTx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign burn: %v", err)
	}
	if err := sp.ApplyTransaction(burnTx); err != nil {
		t.Fatalf("apply burn: %v", err)
	}
	burnHash, err := burnTx.Hash()
	if err != nil {
		t.Fatalf("hash burn: %v", err)
	}
	requestID := nhbstate.RedemptionRequestID(burnHash)

	attestorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate attestor key: %v", err)
	}
	attestorAddr := attestorKey.PubKey().Address().Bytes()
	if err := sp.setAccount(attestorAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed attestor: %v", err)
	}
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetRole(RoleSwapPayoutAttestor, attestorAddr); err != nil {
		t.Fatalf("grant attestor role: %v", err)
	}

	attestTx := attestRedemptionTx(t, 0, requestID, "failed", "", "nowpayments: destination address rejected")
	if err := attestTx.Sign(attestorKey.PrivateKey); err != nil {
		t.Fatalf("sign attest: %v", err)
	}
	if err := sp.ApplyTransaction(attestTx); err != nil {
		t.Fatalf("apply attestation: %v", err)
	}

	request, ok, err := manager.GetRedemptionRequest(requestID)
	if err != nil {
		t.Fatalf("load request: %v", err)
	}
	if !ok {
		t.Fatalf("expected request to exist")
	}
	if request.Status != string(nhbstate.RedemptionStatusFailed) {
		t.Fatalf("expected failed status, got %s", request.Status)
	}

	// The burn is not automatically reversed on failure -- by design, see
	// nhbstate.RedemptionStatusFailed's doc comment. The user's balance stays
	// at 600 (1000 - 400 burned), not refunded to 1000.
	user, err := sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.BalanceNHB.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("expected burn to remain unreversed at balance 600, got %s", user.BalanceNHB)
	}
}
