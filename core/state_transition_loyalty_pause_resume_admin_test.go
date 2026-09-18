package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// TestLoyaltyPauseResumeProgramViaRoleLoyaltyAdmin proves the ROLE_LOYALTY_ADMIN
// grant path actually authorizes pause/resume for a program the admin key does
// NOT own -- TestLoyaltyPauseResumeProgramLifecycle (state_transition_loyalty_test.go)
// already covers the program-owner path, but never exercises a caller that is
// neither the owner nor the merchant, only a RoleLoyaltyAdmin holder. This is
// the exact path nhbportal's admin backend now uses (NHB-AUDIT-W13b): the
// portal never holds a business's own owner key, only a platform-operated key
// granted ROLE_LOYALTY_ADMIN via governance (see native/governance/engine.go's
// applyRoleAllowlist, the same generic role-grant mechanism RoleSwapAdmin
// already uses in production).
func TestLoyaltyPauseResumeProgramViaRoleLoyaltyAdmin(t *testing.T) {
	sp := newLoyaltyTestProcessor(t)
	_, owner := newLoyaltyTestKey(t, sp)
	merchantAddr, merchant := newLoyaltyTestKey(t, sp)
	adminAddr, admin := newLoyaltyTestKey(t, sp)
	poolAddr, _ := newLoyaltyTestKey(t, sp)

	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetRole(RoleLoyaltyAdmin, adminAddr.Bytes()); err != nil {
		t.Fatalf("grant loyalty admin role: %v", err)
	}

	businessID := loyaltyCreateBusinessTx(t, sp, owner, "Acme Coffee")
	if err := loyaltyAddMerchantTx(t, sp, owner, businessID, merchantAddr); err != nil {
		t.Fatalf("owner add merchant: %v", err)
	}

	programID := mustProgramID(t, "prog-admin-pause")
	businessIDHex := "0x" + hexString(businessID[:])
	createTx := &types.Transaction{Type: types.TxTypeCreateLoyaltyProgram, Data: loyaltyProgramPayloadFor(t, businessIDHex, hexString(programID[:]), poolAddr, "1000")}
	merchantAcc, err := sp.getAccount(merchant)
	if err != nil {
		t.Fatalf("get merchant account: %v", err)
	}
	if err := sp.handleNativeTransaction(createTx, merchant, merchantAcc); err != nil {
		t.Fatalf("create program: %v", err)
	}

	program, _, err := sp.LoyaltyProgramByID(programID)
	if err != nil {
		t.Fatalf("load program: %v", err)
	}
	var adminArr [20]byte
	copy(adminArr[:], admin)
	if program.Owner == adminArr {
		t.Fatalf("sanity check failed: admin must not be the program owner")
	}

	pauseTx := &types.Transaction{Type: types.TxTypePauseLoyaltyProgram, Data: mustJSON(t, map[string]string{"id": hexString(programID[:])})}
	adminAcc, err := sp.getAccount(admin)
	if err != nil {
		t.Fatalf("get admin account: %v", err)
	}
	if err := sp.handleNativeTransaction(pauseTx, admin, adminAcc); err != nil {
		t.Fatalf("RoleLoyaltyAdmin holder pause: %v", err)
	}
	program, _, err = sp.LoyaltyProgramByID(programID)
	if err != nil {
		t.Fatalf("load program: %v", err)
	}
	if program.Active {
		t.Fatalf("expected program paused by a RoleLoyaltyAdmin holder that is not the owner")
	}

	resumeTx := &types.Transaction{Type: types.TxTypeResumeLoyaltyProgram, Data: mustJSON(t, map[string]string{"id": hexString(programID[:])})}
	adminAcc2, err := sp.getAccount(admin)
	if err != nil {
		t.Fatalf("get admin account: %v", err)
	}
	if err := sp.handleNativeTransaction(resumeTx, admin, adminAcc2); err != nil {
		t.Fatalf("RoleLoyaltyAdmin holder resume: %v", err)
	}
	program, _, err = sp.LoyaltyProgramByID(programID)
	if err != nil {
		t.Fatalf("load program: %v", err)
	}
	if !program.Active {
		t.Fatalf("expected program resumed by a RoleLoyaltyAdmin holder that is not the owner")
	}
}

// TestLoyaltyPauseProgramApply_IndependentStatesAgree is
// TestSwapVoucherReverseApply_IndependentStatesAgree's (core/swap_admin_tx_test.go)
// counterpart for TxTypePauseLoyaltyProgram: applying the exact same
// RoleLoyaltyAdmin-authorized transaction against two independently-built
// StateProcessor instances, seeded identically and never sharing memory, must
// produce byte-identical resulting state roots. Unlike the swap-admin family,
// PauseProgram/ResumeProgram (native/loyalty/registry_program.go) never read
// wall-clock time or iterate a map, so there is no separate "deterministic
// clock" concern here -- this test only needs to confirm the ordinary,
// already-shipped apply path really is trie-only and side-effect-free beyond
// that, exactly as every other validator would observe it.
func TestLoyaltyPauseProgramApply_IndependentStatesAgree(t *testing.T) {
	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	merchantKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate merchant key: %v", err)
	}
	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	poolKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate pool key: %v", err)
	}

	owner := ownerKey.PubKey().Address().Bytes()
	merchantAddr := merchantKey.PubKey().Address()
	merchant := merchantAddr.Bytes()
	adminAddr := adminKey.PubKey().Address()
	admin := adminAddr.Bytes()
	poolAddr := poolKey.PubKey().Address()

	programID := mustProgramID(t, "prog-determinism")

	buildSeededProcessor := func(t *testing.T) *StateProcessor {
		t.Helper()
		sp := newLoyaltyTestProcessor(t)

		var ownerArr, merchantArr, adminArr [20]byte
		copy(ownerArr[:], owner)
		copy(merchantArr[:], merchant)
		copy(adminArr[:], admin)
		zero := func() *types.Account {
			return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
		}
		writeAccount(t, sp, ownerArr, zero())
		writeAccount(t, sp, merchantArr, zero())
		writeAccount(t, sp, adminArr, zero())

		manager := nhbstate.NewManager(sp.Trie)
		if err := manager.SetRole(RoleLoyaltyAdmin, adminArr[:]); err != nil {
			t.Fatalf("grant loyalty admin role: %v", err)
		}

		businessID := loyaltyCreateBusinessTx(t, sp, owner, "Acme Coffee")
		if err := loyaltyAddMerchantTx(t, sp, owner, businessID, merchantAddr); err != nil {
			t.Fatalf("owner add merchant: %v", err)
		}
		businessIDHex := "0x" + hexString(businessID[:])
		createTx := &types.Transaction{Type: types.TxTypeCreateLoyaltyProgram, Data: loyaltyProgramPayloadFor(t, businessIDHex, hexString(programID[:]), poolAddr, "1000")}
		merchantAcc, err := sp.getAccount(merchant)
		if err != nil {
			t.Fatalf("get merchant account: %v", err)
		}
		if err := sp.handleNativeTransaction(createTx, merchant, merchantAcc); err != nil {
			t.Fatalf("create program: %v", err)
		}
		return sp
	}

	spA := buildSeededProcessor(t)
	spB := buildSeededProcessor(t)

	preRootA := spA.Trie.Hash()
	preRootB := spB.Trie.Hash()
	if preRootA != preRootB {
		t.Fatalf("expected identical starting roots after identical seeding, got %s vs %s", preRootA, preRootB)
	}

	pauseTx := &types.Transaction{Type: types.TxTypePauseLoyaltyProgram, Data: mustJSON(t, map[string]string{"id": hexString(programID[:])})}

	for _, sp := range []*StateProcessor{spA, spB} {
		adminAcc, err := sp.getAccount(admin)
		if err != nil {
			t.Fatalf("get admin account: %v", err)
		}
		if err := sp.handleNativeTransaction(pauseTx, admin, adminAcc); err != nil {
			t.Fatalf("apply pause transaction: %v", err)
		}
	}

	postRootA := spA.Trie.Hash()
	postRootB := spB.Trie.Hash()
	if postRootA != postRootB {
		t.Fatalf("state roots diverged after applying the identical pause transaction: %s vs %s", postRootA, postRootB)
	}
	if postRootA == preRootA {
		t.Fatalf("expected the pause to actually change state -- root did not move")
	}

	for _, sp := range []*StateProcessor{spA, spB} {
		program, _, err := sp.LoyaltyProgramByID(programID)
		if err != nil {
			t.Fatalf("load program: %v", err)
		}
		if program.Active {
			t.Fatalf("expected program paused on every independently-constructed state instance")
		}
	}
}

// TestLoyaltyPauseProgramApply_RejectsNonAdminNonOwnerSignerAtApplyTime is
// TestSwapVoucherReverseApply_RejectsNonAdminSignerAtApplyTime's counterpart:
// a signer that is neither the program owner nor a RoleLoyaltyAdmin holder
// must be rejected by the SAME apply-time path nhbportal's new admin-signed
// transaction goes through, not merely by an HTTP-layer check. This overlaps
// TestLoyaltyPauseResumeProgramLifecycle's "attacker" case but pins it down as
// its own named regression test, matching the swap-admin family's convention
// of a dedicated unauthorized-signer test.
func TestLoyaltyPauseProgramApply_RejectsNonAdminNonOwnerSignerAtApplyTime(t *testing.T) {
	sp := newLoyaltyTestProcessor(t)
	_, owner := newLoyaltyTestKey(t, sp)
	merchantAddr, merchant := newLoyaltyTestKey(t, sp)
	_, attacker := newLoyaltyTestKey(t, sp)
	poolAddr, _ := newLoyaltyTestKey(t, sp)

	businessID := loyaltyCreateBusinessTx(t, sp, owner, "Acme Coffee")
	if err := loyaltyAddMerchantTx(t, sp, owner, businessID, merchantAddr); err != nil {
		t.Fatalf("owner add merchant: %v", err)
	}
	programID := mustProgramID(t, "prog-reject-attacker")
	businessIDHex := "0x" + hexString(businessID[:])
	createTx := &types.Transaction{Type: types.TxTypeCreateLoyaltyProgram, Data: loyaltyProgramPayloadFor(t, businessIDHex, hexString(programID[:]), poolAddr, "1000")}
	merchantAcc, err := sp.getAccount(merchant)
	if err != nil {
		t.Fatalf("get merchant account: %v", err)
	}
	if err := sp.handleNativeTransaction(createTx, merchant, merchantAcc); err != nil {
		t.Fatalf("create program: %v", err)
	}

	preRoot := sp.Trie.Hash()

	pauseTx := &types.Transaction{Type: types.TxTypePauseLoyaltyProgram, Data: mustJSON(t, map[string]string{"id": hexString(programID[:])})}
	attackerAcc, err := sp.getAccount(attacker)
	if err != nil {
		t.Fatalf("get attacker account: %v", err)
	}
	applyErr := sp.handleNativeTransaction(pauseTx, attacker, attackerAcc)
	if applyErr == nil {
		t.Fatalf("SECURITY REGRESSION: expected an unauthorized signer to be rejected at apply time")
	}

	program, _, err := sp.LoyaltyProgramByID(programID)
	if err != nil {
		t.Fatalf("load program: %v", err)
	}
	if !program.Active {
		t.Fatalf("program must remain active after a rejected pause attempt")
	}
	if postRoot := sp.Trie.Hash(); postRoot != preRoot {
		t.Fatalf("a rejected pause attempt must not mutate state: root moved from %s to %s", preRoot, postRoot)
	}
}
