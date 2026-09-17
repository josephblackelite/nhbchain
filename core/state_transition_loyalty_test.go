package core

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/loyalty"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

// newLoyaltyTestProcessor returns a plain StateProcessor with the NHB token
// registered (required by CreateProgram's TokenExists check).
func newLoyaltyTestProcessor(t *testing.T) *StateProcessor {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("create trie: %v", err)
	}
	sp, err := NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	manager := nhbstate.NewManager(trie)
	if err := manager.RegisterToken("NHB", "Native", 18); err != nil {
		t.Fatalf("register NHB: %v", err)
	}
	return sp
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return data
}

func newLoyaltyTestKey(t *testing.T, sp *StateProcessor) (crypto.Address, []byte) {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address()
	var accountAddr [20]byte
	copy(accountAddr[:], addr.Bytes())
	writeAccount(t, sp, accountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	return addr, addr.Bytes()
}

func loyaltyCreateBusinessTx(t *testing.T, sp *StateProcessor, sender []byte, name string) loyalty.BusinessID {
	t.Helper()
	tx := &types.Transaction{Type: types.TxTypeCreateLoyaltyBusiness, Data: mustJSON(t, map[string]string{"name": name})}
	acc, err := sp.getAccount(sender)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if err := sp.handleNativeTransaction(tx, sender, acc); err != nil {
		t.Fatalf("create business: %v", err)
	}
	var owner [20]byte
	copy(owner[:], sender)
	ids, err := sp.LoyaltyBusinessesByOwner(owner)
	if err != nil {
		t.Fatalf("list businesses: %v", err)
	}
	if len(ids) == 0 {
		t.Fatalf("expected at least one business for owner")
	}
	return ids[len(ids)-1]
}

func loyaltyAddMerchantTx(t *testing.T, sp *StateProcessor, sender []byte, businessID loyalty.BusinessID, merchant crypto.Address) error {
	t.Helper()
	payload := struct {
		BusinessID string `json:"businessId"`
		Merchant   string `json:"merchant"`
	}{BusinessID: "0x" + hexString(businessID[:]), Merchant: merchant.String()}
	tx := &types.Transaction{Type: types.TxTypeLoyaltyAddMerchant, Data: mustJSON(t, payload)}
	acc, err := sp.getAccount(sender)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	return sp.handleNativeTransaction(tx, sender, acc)
}

func hexString(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}

func TestLoyaltyCreateBusinessLifecycle(t *testing.T) {
	sp := newLoyaltyTestProcessor(t)
	ownerAddr, owner := newLoyaltyTestKey(t, sp)

	businessID := loyaltyCreateBusinessTx(t, sp, owner, "Acme Coffee")

	business, ok, err := sp.LoyaltyBusinessByID(businessID)
	if err != nil {
		t.Fatalf("load business: %v", err)
	}
	if !ok {
		t.Fatalf("expected business to exist")
	}
	if business.Name != "Acme Coffee" {
		t.Fatalf("unexpected business name: %s", business.Name)
	}
	var ownerBytes [20]byte
	copy(ownerBytes[:], ownerAddr.Bytes())
	if business.Owner != ownerBytes {
		t.Fatalf("business owner mismatch: got %x want %x", business.Owner, ownerBytes)
	}
	if len(business.Merchants) != 0 {
		t.Fatalf("expected no merchants on a freshly created business")
	}
}

func TestLoyaltySetPaymasterRequiresOwnership(t *testing.T) {
	sp := newLoyaltyTestProcessor(t)
	_, owner := newLoyaltyTestKey(t, sp)
	_, attacker := newLoyaltyTestKey(t, sp)
	adminAddr, admin := newLoyaltyTestKey(t, sp)
	paymasterAddr, _ := newLoyaltyTestKey(t, sp)

	businessID := loyaltyCreateBusinessTx(t, sp, owner, "Acme Coffee")

	setPaymasterTx := func(sender []byte, paymaster crypto.Address) error {
		payload := struct {
			BusinessID string `json:"businessId"`
			Paymaster  string `json:"paymaster"`
		}{BusinessID: "0x" + hexString(businessID[:]), Paymaster: paymaster.String()}
		tx := &types.Transaction{Type: types.TxTypeLoyaltySetPaymaster, Data: mustJSON(t, payload)}
		acc, err := sp.getAccount(sender)
		if err != nil {
			t.Fatalf("get account: %v", err)
		}
		return sp.handleNativeTransaction(tx, sender, acc)
	}

	if err := setPaymasterTx(attacker, paymasterAddr); !errors.Is(err, loyalty.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized for non-owner caller, got %v", err)
	}
	business, _, err := sp.LoyaltyBusinessByID(businessID)
	if err != nil {
		t.Fatalf("load business: %v", err)
	}
	var zero [20]byte
	if business.Paymaster != zero {
		t.Fatalf("paymaster must be unchanged after unauthorized attempt")
	}

	if err := setPaymasterTx(owner, paymasterAddr); err != nil {
		t.Fatalf("owner set paymaster: %v", err)
	}
	business, _, err = sp.LoyaltyBusinessByID(businessID)
	if err != nil {
		t.Fatalf("load business: %v", err)
	}
	var wantPaymaster [20]byte
	copy(wantPaymaster[:], paymasterAddr.Bytes())
	if business.Paymaster != wantPaymaster {
		t.Fatalf("paymaster not set by owner")
	}

	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetRole(RoleLoyaltyAdmin, adminAddr.Bytes()); err != nil {
		t.Fatalf("grant loyalty admin role: %v", err)
	}
	altPaymasterAddr, _ := newLoyaltyTestKey(t, sp)
	if err := setPaymasterTx(admin, altPaymasterAddr); err != nil {
		t.Fatalf("admin set paymaster: %v", err)
	}
}

// TestLoyaltyAddRemoveMerchantRequiresOwnership is the single most important
// test in this file: native/loyalty's AddMerchantAddress/RemoveMerchantAddress
// take no caller parameter and perform NO authorization check of their own
// (see applyLoyaltyAddMerchant/applyLoyaltyRemoveMerchant's doc comments) --
// this proves the dispatch-layer's manual check actually closes that gap,
// not merely that some error surfaces.
func TestLoyaltyAddRemoveMerchantRequiresOwnership(t *testing.T) {
	sp := newLoyaltyTestProcessor(t)
	_, owner := newLoyaltyTestKey(t, sp)
	_, attacker := newLoyaltyTestKey(t, sp)
	merchantAddr, _ := newLoyaltyTestKey(t, sp)

	businessID := loyaltyCreateBusinessTx(t, sp, owner, "Acme Coffee")

	if err := loyaltyAddMerchantTx(t, sp, attacker, businessID, merchantAddr); err == nil {
		t.Fatalf("expected unauthorized attacker to be rejected")
	}
	business, _, err := sp.LoyaltyBusinessByID(businessID)
	if err != nil {
		t.Fatalf("load business: %v", err)
	}
	if len(business.Merchants) != 0 {
		t.Fatalf("attacker must not be able to add a merchant -- got %d merchants", len(business.Merchants))
	}

	if err := loyaltyAddMerchantTx(t, sp, owner, businessID, merchantAddr); err != nil {
		t.Fatalf("owner add merchant: %v", err)
	}
	business, _, err = sp.LoyaltyBusinessByID(businessID)
	if err != nil {
		t.Fatalf("load business: %v", err)
	}
	if len(business.Merchants) != 1 {
		t.Fatalf("expected exactly one merchant after owner add, got %d", len(business.Merchants))
	}

	removeTx := func(sender []byte) error {
		payload := struct {
			BusinessID string `json:"businessId"`
			Merchant   string `json:"merchant"`
		}{BusinessID: "0x" + hexString(businessID[:]), Merchant: merchantAddr.String()}
		tx := &types.Transaction{Type: types.TxTypeLoyaltyRemoveMerchant, Data: mustJSON(t, payload)}
		acc, err := sp.getAccount(sender)
		if err != nil {
			t.Fatalf("get account: %v", err)
		}
		return sp.handleNativeTransaction(tx, sender, acc)
	}

	if err := removeTx(attacker); err == nil {
		t.Fatalf("expected unauthorized attacker to be rejected on remove")
	}
	business, _, err = sp.LoyaltyBusinessByID(businessID)
	if err != nil {
		t.Fatalf("load business: %v", err)
	}
	if len(business.Merchants) != 1 {
		t.Fatalf("attacker must not be able to remove a merchant")
	}

	if err := removeTx(owner); err != nil {
		t.Fatalf("owner remove merchant: %v", err)
	}
	business, _, err = sp.LoyaltyBusinessByID(businessID)
	if err != nil {
		t.Fatalf("load business: %v", err)
	}
	if len(business.Merchants) != 0 {
		t.Fatalf("expected merchant removed by owner")
	}
}

func loyaltyProgramPayloadFor(t *testing.T, businessID string, id string, pool crypto.Address, dailyCapProgram string) []byte {
	t.Helper()
	return mustJSON(t, loyaltyProgramPayload{
		BusinessID:      businessID,
		ID:              id,
		Pool:            pool.String(),
		TokenSymbol:     "NHB",
		AccrualBps:      100,
		DailyCapProgram: strPtr(dailyCapProgram),
	})
}

func strPtr(s string) *string { return &s }

func TestLoyaltyCreateProgramRequiresMerchantMembership(t *testing.T) {
	sp := newLoyaltyTestProcessor(t)
	_, owner := newLoyaltyTestKey(t, sp)
	merchantAddr, merchant := newLoyaltyTestKey(t, sp)
	_, outsider := newLoyaltyTestKey(t, sp)
	adminAddr, admin := newLoyaltyTestKey(t, sp)
	poolAddr, _ := newLoyaltyTestKey(t, sp)

	businessID := loyaltyCreateBusinessTx(t, sp, owner, "Acme Coffee")
	if err := loyaltyAddMerchantTx(t, sp, owner, businessID, merchantAddr); err != nil {
		t.Fatalf("owner add merchant: %v", err)
	}

	businessIDHex := "0x" + hexString(businessID[:])

	createProgram := func(sender []byte, programID string) error {
		tx := &types.Transaction{Type: types.TxTypeCreateLoyaltyProgram, Data: loyaltyProgramPayloadFor(t, businessIDHex, programID, poolAddr, "1000")}
		acc, err := sp.getAccount(sender)
		if err != nil {
			t.Fatalf("get account: %v", err)
		}
		return sp.handleNativeTransaction(tx, sender, acc)
	}

	if err := createProgram(outsider, mustProgramIDHex(t, "prog-outsider")); err == nil {
		t.Fatalf("expected non-merchant, non-admin caller to be rejected")
	}

	if err := createProgram(merchant, mustProgramIDHex(t, "prog-merchant")); err != nil {
		t.Fatalf("registered merchant should be allowed to create a program: %v", err)
	}
	program, ok, err := sp.LoyaltyProgramByID(mustProgramID(t, "prog-merchant"))
	if err != nil || !ok {
		t.Fatalf("expected program to exist: ok=%v err=%v", ok, err)
	}
	var merchantBytes [20]byte
	copy(merchantBytes[:], merchant)
	if program.Owner != merchantBytes {
		t.Fatalf("program owner must be bound to sender")
	}

	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetRole(RoleLoyaltyAdmin, adminAddr.Bytes()); err != nil {
		t.Fatalf("grant loyalty admin role: %v", err)
	}
	if err := createProgram(admin, mustProgramIDHex(t, "prog-admin")); err != nil {
		t.Fatalf("loyalty admin should bypass merchant-membership check: %v", err)
	}
}

func mustProgramID(t *testing.T, seed string) loyalty.ProgramID {
	t.Helper()
	var id loyalty.ProgramID
	copy(id[:], []byte(seed))
	return id
}

func mustProgramIDHex(t *testing.T, seed string) string {
	t.Helper()
	id := mustProgramID(t, seed)
	return hexString(id[:])
}

func TestLoyaltyCreateProgramRequiresProgramWideCap(t *testing.T) {
	sp := newLoyaltyTestProcessor(t)
	_, owner := newLoyaltyTestKey(t, sp)
	merchantAddr, merchant := newLoyaltyTestKey(t, sp)
	poolAddr, _ := newLoyaltyTestKey(t, sp)

	businessID := loyaltyCreateBusinessTx(t, sp, owner, "Acme Coffee")
	if err := loyaltyAddMerchantTx(t, sp, owner, businessID, merchantAddr); err != nil {
		t.Fatalf("owner add merchant: %v", err)
	}

	payload := loyaltyProgramPayload{
		BusinessID:  "0x" + hexString(businessID[:]),
		ID:          mustProgramIDHex(t, "prog-nocap"),
		Pool:        poolAddr.String(),
		TokenSymbol: "NHB",
		AccrualBps:  100,
		// Deliberately no DailyCapProgram/EpochCapProgram -- must be rejected.
	}
	tx := &types.Transaction{Type: types.TxTypeCreateLoyaltyProgram, Data: mustJSON(t, payload)}
	acc, err := sp.getAccount(merchant)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if err := sp.handleNativeTransaction(tx, merchant, acc); !errors.Is(err, loyalty.ErrInvalidProgram) {
		t.Fatalf("expected ErrInvalidProgram for a program with no program-wide cap, got %v", err)
	}
}

func TestLoyaltyUpdateProgramRequiresOwnershipAndPreservesIdentity(t *testing.T) {
	sp := newLoyaltyTestProcessor(t)
	_, owner := newLoyaltyTestKey(t, sp)
	merchantAddr, merchant := newLoyaltyTestKey(t, sp)
	_, attacker := newLoyaltyTestKey(t, sp)
	adminAddr, admin := newLoyaltyTestKey(t, sp)
	poolAddr, _ := newLoyaltyTestKey(t, sp)

	businessID := loyaltyCreateBusinessTx(t, sp, owner, "Acme Coffee")
	if err := loyaltyAddMerchantTx(t, sp, owner, businessID, merchantAddr); err != nil {
		t.Fatalf("owner add merchant: %v", err)
	}
	programID := mustProgramID(t, "prog-update")
	businessIDHex := "0x" + hexString(businessID[:])
	createTx := &types.Transaction{Type: types.TxTypeCreateLoyaltyProgram, Data: loyaltyProgramPayloadFor(t, businessIDHex, hexString(programID[:]), poolAddr, "1000")}
	acc, err := sp.getAccount(merchant)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if err := sp.handleNativeTransaction(createTx, merchant, acc); err != nil {
		t.Fatalf("create program: %v", err)
	}

	updatePayload := func(accrualBps uint32) []byte {
		return mustJSON(t, loyaltyProgramPayload{
			ID:              hexString(programID[:]),
			Pool:            poolAddr.String(),
			TokenSymbol:     "NHB",
			AccrualBps:      accrualBps,
			DailyCapProgram: strPtr("2000"),
		})
	}
	updateTx := func(sender []byte, accrualBps uint32) error {
		tx := &types.Transaction{Type: types.TxTypeUpdateLoyaltyProgram, Data: updatePayload(accrualBps)}
		acc, err := sp.getAccount(sender)
		if err != nil {
			t.Fatalf("get account: %v", err)
		}
		return sp.handleNativeTransaction(tx, sender, acc)
	}

	if err := updateTx(attacker, 500); err == nil {
		t.Fatalf("expected unauthorized caller to be rejected")
	}

	if err := updateTx(merchant, 500); err != nil {
		t.Fatalf("owner update: %v", err)
	}
	program, ok, err := sp.LoyaltyProgramByID(programID)
	if err != nil || !ok {
		t.Fatalf("expected program to exist: ok=%v err=%v", ok, err)
	}
	if program.AccrualBps != 500 {
		t.Fatalf("expected accrualBps updated to 500, got %d", program.AccrualBps)
	}
	var merchantBytes [20]byte
	copy(merchantBytes[:], merchant)
	if program.ID != programID || program.Owner != merchantBytes {
		t.Fatalf("update must never change ID or Owner")
	}

	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetRole(RoleLoyaltyAdmin, adminAddr.Bytes()); err != nil {
		t.Fatalf("grant loyalty admin role: %v", err)
	}
	if err := updateTx(admin, 700); err != nil {
		t.Fatalf("admin update: %v", err)
	}
	program, ok, err = sp.LoyaltyProgramByID(programID)
	if err != nil || !ok {
		t.Fatalf("expected program to exist: ok=%v err=%v", ok, err)
	}
	if program.Owner != merchantBytes {
		t.Fatalf("admin-invoked update must not reassign ownership to the admin")
	}
}

func TestLoyaltyPauseResumeProgramLifecycle(t *testing.T) {
	sp := newLoyaltyTestProcessor(t)
	_, owner := newLoyaltyTestKey(t, sp)
	merchantAddr, merchant := newLoyaltyTestKey(t, sp)
	_, attacker := newLoyaltyTestKey(t, sp)
	poolAddr, _ := newLoyaltyTestKey(t, sp)

	businessID := loyaltyCreateBusinessTx(t, sp, owner, "Acme Coffee")
	if err := loyaltyAddMerchantTx(t, sp, owner, businessID, merchantAddr); err != nil {
		t.Fatalf("owner add merchant: %v", err)
	}
	programID := mustProgramID(t, "prog-pause")
	businessIDHex := "0x" + hexString(businessID[:])
	createTx := &types.Transaction{Type: types.TxTypeCreateLoyaltyProgram, Data: loyaltyProgramPayloadFor(t, businessIDHex, hexString(programID[:]), poolAddr, "1000")}
	acc, err := sp.getAccount(merchant)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if err := sp.handleNativeTransaction(createTx, merchant, acc); err != nil {
		t.Fatalf("create program: %v", err)
	}

	pauseTx := func(sender []byte) error {
		tx := &types.Transaction{Type: types.TxTypePauseLoyaltyProgram, Data: mustJSON(t, map[string]string{"id": hexString(programID[:])})}
		acc, err := sp.getAccount(sender)
		if err != nil {
			t.Fatalf("get account: %v", err)
		}
		return sp.handleNativeTransaction(tx, sender, acc)
	}
	resumeTx := func(sender []byte) error {
		tx := &types.Transaction{Type: types.TxTypeResumeLoyaltyProgram, Data: mustJSON(t, map[string]string{"id": hexString(programID[:])})}
		acc, err := sp.getAccount(sender)
		if err != nil {
			t.Fatalf("get account: %v", err)
		}
		return sp.handleNativeTransaction(tx, sender, acc)
	}

	if err := pauseTx(attacker); err == nil {
		t.Fatalf("expected unauthorized caller to be rejected on pause")
	}

	if err := pauseTx(merchant); err != nil {
		t.Fatalf("owner pause: %v", err)
	}
	program, _, err := sp.LoyaltyProgramByID(programID)
	if err != nil {
		t.Fatalf("load program: %v", err)
	}
	if program.Active {
		t.Fatalf("expected program paused")
	}

	if err := pauseTx(merchant); err != nil {
		t.Fatalf("idempotent re-pause should not error: %v", err)
	}

	if err := resumeTx(attacker); err == nil {
		t.Fatalf("expected unauthorized caller to be rejected on resume")
	}

	if err := resumeTx(merchant); err != nil {
		t.Fatalf("owner resume: %v", err)
	}
	program, _, err = sp.LoyaltyProgramByID(programID)
	if err != nil {
		t.Fatalf("load program: %v", err)
	}
	if !program.Active {
		t.Fatalf("expected program resumed")
	}
}
