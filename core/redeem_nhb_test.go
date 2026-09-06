package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
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

// seedTokenSupply seeds the tracked NHB total supply so that a subsequent
// burn doesn't underflow AdjustTokenSupply's non-negative invariant.
// newStakingStateProcessor seeds account balances directly via setAccount
// (bypassing MintToken), so without this the tracked supply starts at zero
// even though the seeded account already holds a balance -- exactly the gap
// applyRedeemNHB's new supply-tracking call would otherwise trip over.
func seedTokenSupply(t *testing.T, sp *StateProcessor, amount *big.Int) {
	t.Helper()
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetTokenSupply("NHB", amount); err != nil {
		t.Fatalf("seed token supply: %v", err)
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
	seedTokenSupply(t, sp, big.NewInt(1_000))

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
	seedTokenSupply(t, sp, big.NewInt(1_000))
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
	seedTokenSupply(t, sp, big.NewInt(1_000))
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

func TestApplyAttestRedemption_MarksFailedAndRefundsBurn(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()
	if err := sp.setAccount(userAddr, &types.Account{BalanceNHB: big.NewInt(1_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	seedTokenSupply(t, sp, big.NewInt(1_000))
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

	// The burn IS automatically reversed on failure -- see
	// applyAttestRedemption's doc comment. The user's balance returns to
	// 1000 (the 400 burned is credited straight back).
	user, err := sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.BalanceNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected burn to be refunded back to balance 1000, got %s", user.BalanceNHB)
	}

	supply, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("load supply: %v", err)
	}
	if supply.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected token supply restored to 1000 after refund, got %s", supply)
	}

	// The refund must be independently auditable via its own event, not
	// just inferable from the balance/supply delta.
	var sawRefundEvent, sawRefundSupplyReason bool
	for _, evt := range sp.Events() {
		if evt.Type == events.TypeRedemptionRefunded {
			sawRefundEvent = true
			if evt.Attributes["requestId"] != requestID || evt.Attributes["nhbAmount"] != "400" {
				t.Fatalf("unexpected RedemptionRefunded event attributes: %+v", evt.Attributes)
			}
		}
		if evt.Type == events.TypeTokenSupply && evt.Attributes["reason"] == events.SupplyReasonRedeemRefund {
			sawRefundSupplyReason = true
		}
	}
	if !sawRefundEvent {
		t.Fatalf("expected a RedemptionRefunded event to be emitted")
	}
	if !sawRefundSupplyReason {
		t.Fatalf("expected a token.supply event with reason=redeem_refund")
	}
}

// TestApplyAttestRedemption_PaidNeverRefunds is the mirror-image safety
// check: a "paid" outcome must never trigger the refund path (the redeemer
// already received real off-chain funds -- crediting NHB back on top would
// be an outright double-spend). Guards against a future refactor
// accidentally widening the status == RedemptionStatusFailed check in
// applyAttestRedemption.
func TestApplyAttestRedemption_PaidNeverRefunds(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()
	if err := sp.setAccount(userAddr, &types.Account{BalanceNHB: big.NewInt(1_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	seedTokenSupply(t, sp, big.NewInt(1_000))
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

	user, err := sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.BalanceNHB.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("expected balance to remain 600 (burn stays burned) after a paid attestation, got %s", user.BalanceNHB)
	}
	supply, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("load supply: %v", err)
	}
	if supply.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("expected supply to remain 600 after a paid attestation, got %s", supply)
	}
}

// TestApplyAttestRedemption_CannotDoubleRefund is the core anti-cheat check
// for this feature: once a request has been attested failed (and refunded),
// no subsequent attestRedemption transaction for the same requestID -- paid
// or failed -- may ever credit the account again. This must hold even
// though the account's balance has changed since the first attestation
// (guards against a refund path that keyed off "does the request still look
// pending" rather than the request's own terminal status).
func TestApplyAttestRedemption_CannotDoubleRefund(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()
	if err := sp.setAccount(userAddr, &types.Account{BalanceNHB: big.NewInt(1_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	seedTokenSupply(t, sp, big.NewInt(1_000))
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

	firstAttest := attestRedemptionTx(t, 0, requestID, "failed", "", "nowpayments: destination address rejected")
	if err := firstAttest.Sign(attestorKey.PrivateKey); err != nil {
		t.Fatalf("sign first attest: %v", err)
	}
	if err := sp.ApplyTransaction(firstAttest); err != nil {
		t.Fatalf("apply first attestation: %v", err)
	}

	user, err := sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user after first attestation: %v", err)
	}
	if user.BalanceNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected balance 1000 after first (refunding) attestation, got %s", user.BalanceNHB)
	}

	// User spends some of the refunded balance elsewhere before the second,
	// illegitimate attestation attempt arrives -- the double-refund guard
	// must hold regardless of what the balance has since become; it must
	// never depend on the balance still looking "un-refunded".
	spendTx := redeemNHBTx(t, 1, big.NewInt(300), "usdttrc20", "dest2")
	if err := spendTx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign spend: %v", err)
	}
	if err := sp.ApplyTransaction(spendTx); err != nil {
		t.Fatalf("apply spend: %v", err)
	}
	user, err = sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user after spend: %v", err)
	}
	if user.BalanceNHB.Cmp(big.NewInt(700)) != 0 {
		t.Fatalf("expected balance 700 after spending 300 of the refund, got %s", user.BalanceNHB)
	}

	// A second attestation for the ORIGINAL requestID -- whether a replayed
	// "failed" (attempting a second refund) or a late/conflicting "paid" --
	// must be rejected outright, with zero balance/supply effect.
	secondAttest := attestRedemptionTx(t, 1, requestID, "failed", "", "replayed")
	if err := secondAttest.Sign(attestorKey.PrivateKey); err != nil {
		t.Fatalf("sign second attest: %v", err)
	}
	if err := sp.ApplyTransaction(secondAttest); err == nil {
		t.Fatalf("expected rejection when re-attesting an already-failed (refunded) request")
	}

	user, err = sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user after rejected second attestation: %v", err)
	}
	if user.BalanceNHB.Cmp(big.NewInt(700)) != 0 {
		t.Fatalf("expected balance to remain 700 (no double refund), got %s", user.BalanceNHB)
	}

	supply, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("load supply: %v", err)
	}
	// 1000 seeded -> 400 burned (600) -> 400 refunded (1000) -> 300 burned
	// again (700). A double refund would push this to 1100.
	if supply.Cmp(big.NewInt(700)) != 0 {
		t.Fatalf("expected token supply 700 (no double refund), got %s", supply)
	}
}

// TestApplyRedeemNHB_DecrementsTokenSupply covers the supply-tracking gap:
// applyRedeemNHB must call AdjustTokenSupply (mirroring MintToken's call on
// the mint side) with events.SupplyReasonBurn, so a burn actually shrinks
// the tracked NHB total supply rather than only debiting the sender.
func TestApplyRedeemNHB_DecrementsTokenSupply(t *testing.T) {
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
	seedTokenSupply(t, sp, big.NewInt(1_000))

	manager := nhbstate.NewManager(sp.Trie)
	before, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("load supply before burn: %v", err)
	}
	if before.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected seeded supply 1000, got %s", before)
	}

	tx := redeemNHBTx(t, 0, big.NewInt(400), "usdttrc20", "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb")
	if err := tx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	after, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("load supply after burn: %v", err)
	}
	if after.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("expected supply to drop to 600 after burning 400, got %s", after)
	}
}

// TestApplyRedeemNHB_PendingIndexAppendedAndRemovedOnAttestation covers the
// pending-request index: a new burn must make the request discoverable via
// the index (what payments-gateway's watcher will poll), and attesting it
// paid must remove it again so the index doesn't grow unbounded.
func TestApplyRedeemNHB_PendingIndexAppendedAndRemovedOnAttestation(t *testing.T) {
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
	seedTokenSupply(t, sp, big.NewInt(1_000))

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

	manager := nhbstate.NewManager(sp.Trie)
	pendingIDs, err := manager.PendingRedemptionRequestIDs()
	if err != nil {
		t.Fatalf("list pending ids: %v", err)
	}
	if !containsString(pendingIDs, requestID) {
		t.Fatalf("expected request %s in pending index, got %v", requestID, pendingIDs)
	}
	pendingRequests, err := manager.PendingRedemptionRequests()
	if err != nil {
		t.Fatalf("list pending requests: %v", err)
	}
	if len(pendingRequests) != 1 || pendingRequests[0].RequestID != requestID {
		t.Fatalf("expected exactly one pending request %s, got %+v", requestID, pendingRequests)
	}

	attestorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate attestor key: %v", err)
	}
	attestorAddr := attestorKey.PubKey().Address().Bytes()
	if err := sp.setAccount(attestorAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed attestor: %v", err)
	}
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

	pendingIDs, err = manager.PendingRedemptionRequestIDs()
	if err != nil {
		t.Fatalf("list pending ids after attestation: %v", err)
	}
	if containsString(pendingIDs, requestID) {
		t.Fatalf("expected request %s removed from pending index after attestation, got %v", requestID, pendingIDs)
	}
}

// TestApplyRedeemNHB_PauseGuardBlocksNewBurnsButNotAttestation covers the
// pause guard: once moduleSwapRedeem is paused, a brand-new burn must be
// rejected, but an already-pending request (burned before the pause) must
// still be attestable -- payments-gateway needs to be able to close out
// in-flight payouts even while new burns are frozen for an incident.
func TestApplyRedeemNHB_PauseGuardBlocksNewBurnsButNotAttestation(t *testing.T) {
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
	seedTokenSupply(t, sp, big.NewInt(1_000))

	// First burn happens while the module is unpaused, so there's an
	// already-pending request to attest later.
	firstBurn := redeemNHBTx(t, 0, big.NewInt(400), "usdttrc20", "dest")
	if err := firstBurn.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign first burn: %v", err)
	}
	if err := sp.ApplyTransaction(firstBurn); err != nil {
		t.Fatalf("apply first burn: %v", err)
	}
	firstBurnHash, err := firstBurn.Hash()
	if err != nil {
		t.Fatalf("hash first burn: %v", err)
	}
	requestID := nhbstate.RedemptionRequestID(firstBurnHash)

	// Now pause new redemptions.
	sp.SetPauseView(pauseViewStub{modules: map[string]bool{moduleSwapRedeem: true}})

	secondBurn := redeemNHBTx(t, 1, big.NewInt(100), "usdttrc20", "dest")
	if err := secondBurn.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign second burn: %v", err)
	}
	if err := sp.ApplyTransaction(secondBurn); err == nil {
		t.Fatalf("expected second burn to be rejected while moduleSwapRedeem is paused")
	}

	user, err := sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.BalanceNHB.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("expected balance to remain 600 after rejected second burn, got %s", user.BalanceNHB)
	}
	if user.Nonce != 1 {
		t.Fatalf("expected nonce to remain 1 after rejected second burn, got %d", user.Nonce)
	}

	// Attesting the already-pending first request must still succeed despite
	// the module-wide pause -- the pause guard only lives in applyRedeemNHB.
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
		t.Fatalf("expected attestation to succeed despite moduleSwapRedeem pause, got error: %v", err)
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
}

// TestApplyRedeemNHB_RejectsAboveConfiguredPerTxMax covers the redeem-side
// circuit breaker's per-transaction ceiling.
func TestApplyRedeemNHB_RejectsAboveConfiguredPerTxMax(t *testing.T) {
	sp := newStakingStateProcessor(t)
	// The redeem-side per-tx ceiling is governance-controlled now
	// (core/swap_risk_params.go) -- seed it directly into the param store,
	// mirroring what a policy.swapRiskParams proposal's execution would
	// leave behind.
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.ParamStoreSet(governance.ParamKeySwapRiskRedeemPerTxMaxWei, []byte("300")); err != nil {
		t.Fatalf("seed redeem per-tx max: %v", err)
	}

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
	seedTokenSupply(t, sp, big.NewInt(1_000))

	tx := redeemNHBTx(t, 0, big.NewInt(400), "usdttrc20", "dest")
	if err := tx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("expected rejection for amount 400 exceeding configured PerTxMaxWei 300")
	}

	user, err := sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.BalanceNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected balance unchanged at 1000 after rejected burn, got %s", user.BalanceNHB)
	}

	supply, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("load supply: %v", err)
	}
	if supply.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected supply unchanged at 1000 after rejected burn, got %s", supply)
	}
}

// TestApplyRedeemNHB_RejectsAboveConfiguredDailyCap covers the redeem-side
// circuit breaker's per-address daily cap: a first redemption within the cap
// succeeds, and a second redemption from the same address on the same day
// that would push cumulative usage over the daily cap is rejected.
func TestApplyRedeemNHB_RejectsAboveConfiguredDailyCap(t *testing.T) {
	sp := newStakingStateProcessor(t)
	// The redeem-side per-tx ceiling and daily cap are governance-controlled
	// now (core/swap_risk_params.go) -- seed them directly into the param
	// store, mirroring what a policy.swapRiskParams proposal's execution
	// would leave behind.
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.ParamStoreSet(governance.ParamKeySwapRiskRedeemPerTxMaxWei, []byte("1000")); err != nil {
		t.Fatalf("seed redeem per-tx max: %v", err)
	}
	if err := manager.ParamStoreSet(governance.ParamKeySwapRiskRedeemPerAddressDailyCapWei, []byte("500")); err != nil {
		t.Fatalf("seed redeem daily cap: %v", err)
	}

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
	seedTokenSupply(t, sp, big.NewInt(1_000))

	firstTx := redeemNHBTx(t, 0, big.NewInt(300), "usdttrc20", "dest")
	if err := firstTx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign first transaction: %v", err)
	}
	if err := sp.ApplyTransaction(firstTx); err != nil {
		t.Fatalf("expected first redemption of 300 within the daily cap to succeed, got error: %v", err)
	}

	secondTx := redeemNHBTx(t, 1, big.NewInt(300), "usdttrc20", "dest")
	if err := secondTx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign second transaction: %v", err)
	}
	if err := sp.ApplyTransaction(secondTx); err == nil {
		t.Fatalf("expected second redemption of 300 (cumulative 600) to exceed the daily cap of 500")
	}

	user, err := sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.BalanceNHB.Cmp(big.NewInt(700)) != 0 {
		t.Fatalf("expected balance 700 after one successful 300 burn, got %s", user.BalanceNHB)
	}
	if user.Nonce != 1 {
		t.Fatalf("expected nonce 1 after only the first burn applied, got %d", user.Nonce)
	}

	supply, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("load supply: %v", err)
	}
	if supply.Cmp(big.NewInt(700)) != 0 {
		t.Fatalf("expected supply 700 after only the first burn applied, got %s", supply)
	}
}
