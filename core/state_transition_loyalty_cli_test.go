package core

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/loyalty"
)

// TestLoyaltyCLIPayloadsRoundTripThroughApplyTransaction proves cmd/nhb-cli's
// rewritten loyalty write commands (loyaltyCreateBusiness, loyaltySetPaymaster,
// loyaltyModifyMerchant, loyaltyCreateProgram, loyaltyUpdateProgram,
// loyaltyLifecycle) produce JSON payloads the real transaction-application
// path actually accepts. Each payload below is built with the exact same
// json.Marshal(map[string]string{...}) shape those CLI functions use --
// copied here rather than imported, since cmd/nhb-cli is package main and
// its functions also perform real HTTP calls (fetchAccount/sendTransaction)
// this test can't and shouldn't make. This drives every transaction through
// the public StateProcessor.ApplyTransaction entry point (real signature
// recovery via tx.Sign/tx.From, real nonce validation), not the
// handleNativeTransaction test shortcut the other loyalty tests use --
// the same path a live node's nhb_sendTransaction handler uses.
func TestLoyaltyCLIPayloadsRoundTripThroughApplyTransaction(t *testing.T) {
	sp := newLoyaltyTestProcessor(t)

	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	ownerAddr := ownerKey.PubKey().Address()
	var ownerAccountAddr [20]byte
	copy(ownerAccountAddr[:], ownerAddr.Bytes())
	writeAccount(t, sp, ownerAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	var nonce uint64

	send := func(txType types.TxType, data []byte) {
		t.Helper()
		tx := &types.Transaction{
			ChainID:  types.NHBChainID(),
			Type:     txType,
			Nonce:    nonce,
			Data:     data,
			Value:    big.NewInt(0),
			GasLimit: 50000,
			GasPrice: big.NewInt(1),
		}
		if err := tx.Sign(ownerKey.PrivateKey); err != nil {
			t.Fatalf("sign tx (type %d): %v", txType, err)
		}
		if err := sp.ApplyTransaction(tx); err != nil {
			t.Fatalf("ApplyTransaction (type %d): %v", txType, err)
		}
		nonce++
	}

	// Step 1: loyaltyCreateBusiness's exact payload shape.
	businessName := "Joe's Coffee"
	data, err := json.Marshal(map[string]string{"name": businessName})
	if err != nil {
		t.Fatalf("marshal create-business payload: %v", err)
	}
	send(types.TxTypeCreateLoyaltyBusiness, data)

	// Step 2: loyaltyListBusinesses' RPC path, exercised directly here via
	// the same StateProcessor accessor the RPC handler calls.
	businessIDs, err := sp.LoyaltyBusinessesByOwner(ownerAccountAddr)
	if err != nil {
		t.Fatalf("list businesses: %v", err)
	}
	if len(businessIDs) != 1 {
		t.Fatalf("expected exactly one business, got %d", len(businessIDs))
	}
	businessID := businessIDs[0]
	businessIDHex := "0x" + hex.EncodeToString(businessID[:])

	business, ok, err := sp.LoyaltyBusinessByID(businessID)
	if err != nil || !ok {
		t.Fatalf("load business: ok=%v err=%v", ok, err)
	}
	if business.Name != businessName {
		t.Fatalf("business name mismatch: got %q want %q", business.Name, businessName)
	}

	// Step 3: loyaltySetPaymaster's exact payload shape.
	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	paymasterAddr := paymasterKey.PubKey().Address()
	data, err = json.Marshal(map[string]string{
		"businessId": businessIDHex,
		"paymaster":  paymasterAddr.String(),
	})
	if err != nil {
		t.Fatalf("marshal set-paymaster payload: %v", err)
	}
	send(types.TxTypeLoyaltySetPaymaster, data)

	business, ok, err = sp.LoyaltyBusinessByID(businessID)
	if err != nil || !ok {
		t.Fatalf("reload business: ok=%v err=%v", ok, err)
	}
	var wantPaymaster [20]byte
	copy(wantPaymaster[:], paymasterAddr.Bytes())
	if business.Paymaster != wantPaymaster {
		t.Fatalf("paymaster not set via CLI-shaped payload")
	}

	// Step 4: loyaltyModifyMerchant's exact payload shape (add).
	merchantKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate merchant key: %v", err)
	}
	merchantAddr := merchantKey.PubKey().Address()
	data, err = json.Marshal(map[string]string{
		"businessId": businessIDHex,
		"merchant":   merchantAddr.String(),
	})
	if err != nil {
		t.Fatalf("marshal add-merchant payload: %v", err)
	}
	send(types.TxTypeLoyaltyAddMerchant, data)

	business, ok, err = sp.LoyaltyBusinessByID(businessID)
	if err != nil || !ok {
		t.Fatalf("reload business: ok=%v err=%v", ok, err)
	}
	if len(business.Merchants) != 1 {
		t.Fatalf("expected merchant added via CLI-shaped payload, got %d merchants", len(business.Merchants))
	}

	// Step 5: loyaltyCreateProgram's exact merge-businessId-into-spec logic,
	// including the auto-generated "id" field the CLI fills in when the
	// caller's spec omits one.
	specWithoutID := `{"pool":"` + merchantAddr.String() + `","tokenSymbol":"NHB","accrualBps":500,"dailyCapProgram":"1000000000000000000000"}`
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(specWithoutID), &fields); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	businessIDJSON, err := json.Marshal(businessIDHex)
	if err != nil {
		t.Fatalf("marshal businessId: %v", err)
	}
	fields["businessId"] = businessIDJSON
	programIDHex := "0x" + hex.EncodeToString(func() []byte {
		id := make([]byte, 32)
		id[31] = 0x01
		return id
	}())
	idJSON, err := json.Marshal(programIDHex)
	if err != nil {
		t.Fatalf("marshal generated id: %v", err)
	}
	fields["id"] = idJSON
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal create-program payload: %v", err)
	}
	// The program is created by the merchant, matching applyCreateLoyaltyProgram's
	// merchant-membership requirement -- switch the signing key.
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeCreateLoyaltyProgram,
		Nonce:    0,
		Data:     data,
		Value:    big.NewInt(0),
		GasLimit: 50000,
		GasPrice: big.NewInt(1),
	}
	var merchantAccountAddr [20]byte
	copy(merchantAccountAddr[:], merchantAddr.Bytes())
	writeAccount(t, sp, merchantAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	if err := tx.Sign(merchantKey.PrivateKey); err != nil {
		t.Fatalf("sign create-program tx: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("ApplyTransaction create-program: %v", err)
	}

	var programID loyalty.ProgramID
	programIDBytes, err := hex.DecodeString(programIDHex[2:])
	if err != nil || len(programIDBytes) != 32 {
		t.Fatalf("decode program id: %v", err)
	}
	copy(programID[:], programIDBytes)
	program, ok, err := sp.LoyaltyProgramByID(programID)
	if err != nil || !ok {
		t.Fatalf("load program: ok=%v err=%v", ok, err)
	}
	if program.AccrualBps != 500 {
		t.Fatalf("unexpected accrualBps: %d", program.AccrualBps)
	}

	// Step 6: loyaltyUpdateProgram's pass-through-as-is payload shape (full
	// replace -- id must be included, businessId must NOT be).
	updateSpec := `{"id":"` + programIDHex + `","pool":"` + merchantAddr.String() + `","tokenSymbol":"NHB","accrualBps":750,"dailyCapProgram":"2000000000000000000000"}`
	updateTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeUpdateLoyaltyProgram,
		Nonce:    1,
		Data:     []byte(updateSpec),
		Value:    big.NewInt(0),
		GasLimit: 50000,
		GasPrice: big.NewInt(1),
	}
	if err := updateTx.Sign(merchantKey.PrivateKey); err != nil {
		t.Fatalf("sign update-program tx: %v", err)
	}
	if err := sp.ApplyTransaction(updateTx); err != nil {
		t.Fatalf("ApplyTransaction update-program: %v", err)
	}
	program, ok, err = sp.LoyaltyProgramByID(programID)
	if err != nil || !ok {
		t.Fatalf("reload program: ok=%v err=%v", ok, err)
	}
	if program.AccrualBps != 750 {
		t.Fatalf("update via CLI-shaped payload did not apply: got %d", program.AccrualBps)
	}

	// Step 7: loyaltyLifecycle's exact payload shape (pause then resume).
	idPayload, err := json.Marshal(map[string]string{"id": programIDHex})
	if err != nil {
		t.Fatalf("marshal lifecycle payload: %v", err)
	}
	pauseTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypePauseLoyaltyProgram,
		Nonce:    2,
		Data:     idPayload,
		Value:    big.NewInt(0),
		GasLimit: 50000,
		GasPrice: big.NewInt(1),
	}
	if err := pauseTx.Sign(merchantKey.PrivateKey); err != nil {
		t.Fatalf("sign pause tx: %v", err)
	}
	if err := sp.ApplyTransaction(pauseTx); err != nil {
		t.Fatalf("ApplyTransaction pause: %v", err)
	}
	program, ok, err = sp.LoyaltyProgramByID(programID)
	if err != nil || !ok {
		t.Fatalf("reload program after pause: ok=%v err=%v", ok, err)
	}
	if program.Active {
		t.Fatalf("expected program paused via CLI-shaped payload")
	}

	resumeTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeResumeLoyaltyProgram,
		Nonce:    3,
		Data:     idPayload,
		Value:    big.NewInt(0),
		GasLimit: 50000,
		GasPrice: big.NewInt(1),
	}
	if err := resumeTx.Sign(merchantKey.PrivateKey); err != nil {
		t.Fatalf("sign resume tx: %v", err)
	}
	if err := sp.ApplyTransaction(resumeTx); err != nil {
		t.Fatalf("ApplyTransaction resume: %v", err)
	}
	program, ok, err = sp.LoyaltyProgramByID(programID)
	if err != nil || !ok {
		t.Fatalf("reload program after resume: ok=%v err=%v", ok, err)
	}
	if !program.Active {
		t.Fatalf("expected program resumed via CLI-shaped payload")
	}

	// Step 8: loyaltyModifyMerchant's exact payload shape (remove), back on
	// the owner's nonce counter.
	data, err = json.Marshal(map[string]string{
		"businessId": businessIDHex,
		"merchant":   merchantAddr.String(),
	})
	if err != nil {
		t.Fatalf("marshal remove-merchant payload: %v", err)
	}
	send(types.TxTypeLoyaltyRemoveMerchant, data)
	business, ok, err = sp.LoyaltyBusinessByID(businessID)
	if err != nil || !ok {
		t.Fatalf("reload business after remove: ok=%v err=%v", ok, err)
	}
	if len(business.Merchants) != 0 {
		t.Fatalf("expected merchant removed via CLI-shaped payload, got %d", len(business.Merchants))
	}
}
