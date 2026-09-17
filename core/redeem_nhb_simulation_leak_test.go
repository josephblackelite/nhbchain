package core

import (
	"bytes"
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// TestValidateTransactionSimulation_DoesNotLeakRedemptionRequest reproduces
// the live incident reported 2026-09-17: a redeem (TxTypeRedeemNHB) that is
// only ever run through the mempool admission-time dry-run simulation
// (stateCopy.ExecuteTransaction against a StateProcessor.Copy(), exactly
// what node.go's validateTransaction does) must leave ZERO trace in the
// real, original StateProcessor once the copy is discarded -- same as
// every other side effect of that transaction. If PutRedemptionRequest's
// write escapes the copy, the account's balance/nonce correctly stay
// untouched forever (since real application never ran) while the
// requestID is permanently "already exists", making that exact
// transaction unresubmittable -- money genuinely stuck with no burn ever
// having happened.
func TestValidateTransactionSimulation_DoesNotLeakRedemptionRequest(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()
	if err := sp.setAccount(userAddr, &types.Account{
		BalanceNHB:  nhbAmount(1_000),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	seedTokenSupply(t, sp, nhbAmount(1_000))

	tx := redeemNHBTx(t, 0, nhbAmount(400), "usdttrc20", "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb")
	if err := tx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	txHash, err := tx.Hash()
	if err != nil {
		t.Fatalf("compute tx hash: %v", err)
	}
	requestID := nhbstate.RedemptionRequestID(userAddr, txHash)

	// Exactly what node.go's validateTransaction does: copy, then execute
	// the transaction against the COPY only, and discard the copy.
	stateCopy, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy state processor: %v", err)
	}
	if _, err := stateCopy.ExecuteTransaction(tx); err != nil {
		t.Fatalf("simulate transaction against copy: %v", err)
	}
	// stateCopy is now discarded -- never committed, never referenced
	// again, exactly like validateTransaction's local variable going out
	// of scope when the function returns.

	// The ORIGINAL processor must show zero effect from that simulation.
	user, err := sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user from ORIGINAL processor: %v", err)
	}
	if user.BalanceNHB.Cmp(nhbAmount(1_000)) != 0 {
		t.Fatalf("BUG: original balance changed by a discarded simulation copy: got %s, want unchanged 1000", user.BalanceNHB)
	}
	if user.Nonce != 0 {
		t.Fatalf("BUG: original nonce changed by a discarded simulation copy: got %d, want unchanged 0", user.Nonce)
	}

	manager := nhbstate.NewManager(sp.Trie)
	_, exists, err := manager.GetRedemptionRequest(requestID)
	if err != nil {
		t.Fatalf("check redemption request on ORIGINAL processor: %v", err)
	}
	if exists {
		t.Fatalf("BUG REPRODUCED: redemption request %s leaked into the ORIGINAL StateProcessor from a discarded simulation copy -- this permanently blocks any real resubmission of this exact transaction with no burn ever having happened", requestID)
	}

	// Sanity check the leak theory is even plausible: re-running the SAME
	// transaction for real (via ApplyTransaction on the original, not a
	// copy) must succeed cleanly if nothing leaked.
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("re-applying the same transaction for real should succeed if nothing leaked, got: %v", err)
	}
	user, err = sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user after real application: %v", err)
	}
	if user.Nonce != 1 {
		t.Fatalf("expected nonce 1 after the real (non-simulated) application, got %d", user.Nonce)
	}
}

// TestMultipleDiscardedCopies_DoNotLeakRedemptionRequest reproduces
// buildProposalState's own retry pattern (core/node.go's CreateBlock): when
// ANY transaction in a candidate batch is pruned/skipped, the ENTIRE
// stateCopy built for that attempt -- including every OTHER transaction
// that was successfully applied into it, redeem included -- is discarded
// wholesale (buildProposalState returns a nil *StateProcessor), and the
// caller immediately builds a SECOND, fresh stateCopy from n.state and
// retries the (now-smaller) batch. This creates TWO SEPARATE
// StateProcessor.Copy() calls off the SAME parent in quick succession,
// where the FIRST one genuinely, successfully ran applyRedeemNHB to
// completion (burn + PutRedemptionRequest) before being thrown away
// unused. If anything about that first copy's PutRedemptionRequest write
// is visible to the second, independently-created copy, that's the leak.
func TestMultipleDiscardedCopies_DoNotLeakRedemptionRequest(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()
	if err := sp.setAccount(userAddr, &types.Account{
		BalanceNHB:  nhbAmount(1_000),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	seedTokenSupply(t, sp, nhbAmount(1_000))

	tx := redeemNHBTx(t, 0, nhbAmount(400), "usdttrc20", "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb")
	if err := tx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	txHash, err := tx.Hash()
	if err != nil {
		t.Fatalf("compute tx hash: %v", err)
	}
	requestID := nhbstate.RedemptionRequestID(userAddr, txHash)

	// Attempt #1: a fresh copy off the real, unmutated sp. Redeem applies
	// successfully within it. Then this ENTIRE copy is discarded, exactly
	// as buildProposalState discards a whole attempt when some OTHER
	// transaction in the same batch was pruned/skipped -- never committed
	// back to sp, never referenced again.
	firstAttempt, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy state processor (attempt 1): %v", err)
	}
	if err := firstAttempt.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply redeem tx in first attempt: %v", err)
	}
	// firstAttempt is now discarded, matching buildProposalState returning
	// nil for the *StateProcessor while some other tx in the batch failed.

	// Attempt #2: a SEPARATE, independently-created copy off the SAME
	// original sp (which itself was never mutated by attempt #1). This is
	// exactly what CreateBlock's retry loop does next.
	secondAttempt, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy state processor (attempt 2): %v", err)
	}
	if err := secondAttempt.ApplyTransaction(tx); err != nil {
		t.Fatalf("BUG REPRODUCED: second independent copy rejected the redeem tx (likely \"request already exists\"), even though the first copy that applied it successfully was discarded and never committed: %v", err)
	}

	user, err := secondAttempt.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user from second attempt: %v", err)
	}
	if user.Nonce != 1 {
		t.Fatalf("expected nonce 1 in the second attempt's own copy, got %d", user.Nonce)
	}

	// The ORIGINAL sp must still show zero effect from either discarded/
	// independent attempt.
	origUser, err := sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user from original: %v", err)
	}
	if origUser.Nonce != 0 {
		t.Fatalf("BUG: original processor's nonce changed to %d from copies that were never committed", origUser.Nonce)
	}
	manager := nhbstate.NewManager(sp.Trie)
	_, exists, err := manager.GetRedemptionRequest(requestID)
	if err != nil {
		t.Fatalf("check redemption request on original: %v", err)
	}
	if exists {
		t.Fatalf("BUG: redemption request leaked into the original processor from a discarded/independent copy")
	}
}

// TestRedeemNHB_DifferentSendersSameParams_HashCollides is a regression
// guard for the confirmed root cause of the live 2026-09-17 incident:
// core/types/transaction.go's V3 tx.Hash() never includes the sender or
// signature -- only ChainID, Type, Nonce, To, Value, Data, gas fields, etc.
// Every redeem's `to` is always the zero address and gas fields are
// constant defaults, so two DIFFERENT accounts that happen to submit a
// redeem with the same nonce (0, for anyone's first-ever transaction), the
// same amount, and the same destination payload produce the IDENTICAL
// tx.Hash() -- confirmed live via nhb_getTransactionReceipt: the exact
// requestID a real incident's error cited belonged to a real, unrelated,
// already-settled transaction from a different account entirely, so the
// second (innocent) account's redeem was rejected with no burn ever
// applied and its NHB permanently unreachable via that exact
// (account, nonce, amount, destination) combination.
//
// The fix scopes RedemptionRequestID to the SENDER as well as the tx hash
// (core/state/redemption.go), not tx.Hash() itself (which is used far
// beyond redemptions and would be a much larger, riskier change) -- so
// tx.Hash() colliding across senders is still expected and asserted here,
// but it must no longer matter: both accounts' independently-signed,
// structurally-identical redemptions must now succeed.
func TestRedeemNHB_DifferentSendersSameParams_HashCollides(t *testing.T) {
	sp := newStakingStateProcessor(t)

	aliceKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate alice key: %v", err)
	}
	aliceAddr := aliceKey.PubKey().Address().Bytes()
	if err := sp.setAccount(aliceAddr, &types.Account{
		BalanceNHB:  nhbAmount(1_000),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed alice: %v", err)
	}

	bobKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate bob key: %v", err)
	}
	bobAddr := bobKey.PubKey().Address().Bytes()
	if err := sp.setAccount(bobAddr, &types.Account{
		BalanceNHB:  nhbAmount(1_000),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed bob: %v", err)
	}
	seedTokenSupply(t, sp, nhbAmount(2_000))

	// Two DIFFERENT accounts, both on their first-ever transaction (nonce
	// 0), redeeming the identical amount to the identical destination --
	// entirely plausible for two separate first-time testers who both
	// picked a round test amount and, say, a shared/example destination
	// address.
	aliceTx := redeemNHBTx(t, 0, nhbAmount(10), "usdttrc20", "TSameDestinationAddressXXXXXXXXXXXX")
	if err := aliceTx.Sign(aliceKey.PrivateKey); err != nil {
		t.Fatalf("sign alice tx: %v", err)
	}
	bobTx := redeemNHBTx(t, 0, nhbAmount(10), "usdttrc20", "TSameDestinationAddressXXXXXXXXXXXX")
	if err := bobTx.Sign(bobKey.PrivateKey); err != nil {
		t.Fatalf("sign bob tx: %v", err)
	}

	aliceHash, err := aliceTx.Hash()
	if err != nil {
		t.Fatalf("hash alice tx: %v", err)
	}
	bobHash, err := bobTx.Hash()
	if err != nil {
		t.Fatalf("hash bob tx: %v", err)
	}
	if !bytes.Equal(aliceHash, bobHash) {
		t.Fatalf("expected alice's and bob's transaction hashes to collide (proving Hash() ignores sender), got alice=%x bob=%x", aliceHash, bobHash)
	}

	// Alice's redeem applies for real -- she is first.
	if err := sp.ApplyTransaction(aliceTx); err != nil {
		t.Fatalf("apply alice's redeem: %v", err)
	}
	alice, err := sp.getAccount(aliceAddr)
	if err != nil {
		t.Fatalf("load alice: %v", err)
	}
	if alice.BalanceNHB.Cmp(nhbAmount(990)) != 0 {
		t.Fatalf("expected alice's balance to drop by 10, got %s", alice.BalanceNHB)
	}

	// Bob's structurally-identical (but genuinely his own, independently
	// signed) redeem must now succeed -- RedemptionRequestID folds the
	// sender in, so alice's and bob's requests get different IDs despite
	// their colliding tx.Hash(). Before the fix, this was rejected with
	// "redeemNHB: request ... already exists", exactly the live incident.
	if err := sp.ApplyTransaction(bobTx); err != nil {
		t.Fatalf("bob's independent redeem must not be rejected as a false duplicate of alice's unrelated transaction, got: %v", err)
	}

	bob, err := sp.getAccount(bobAddr)
	if err != nil {
		t.Fatalf("load bob: %v", err)
	}
	if bob.Nonce != 1 {
		t.Fatalf("expected bob's nonce incremented to 1 after his own successful burn, got %d", bob.Nonce)
	}
	if bob.BalanceNHB.Cmp(nhbAmount(990)) != 0 {
		t.Fatalf("expected bob's balance to drop by 10 same as alice's, got %s", bob.BalanceNHB)
	}

	// Both requests must be independently tracked under DIFFERENT IDs.
	aliceRequestID := nhbstate.RedemptionRequestID(aliceAddr, aliceHash)
	bobRequestID := nhbstate.RedemptionRequestID(bobAddr, bobHash)
	if aliceRequestID == bobRequestID {
		t.Fatalf("expected alice's and bob's requestIDs to differ despite colliding tx hashes, both got %s", aliceRequestID)
	}
	manager := nhbstate.NewManager(sp.Trie)
	if _, ok, err := manager.GetRedemptionRequest(aliceRequestID); err != nil || !ok {
		t.Fatalf("expected alice's request to exist under her own ID: ok=%v err=%v", ok, err)
	}
	if _, ok, err := manager.GetRedemptionRequest(bobRequestID); err != nil || !ok {
		t.Fatalf("expected bob's request to exist under his own ID: ok=%v err=%v", ok, err)
	}
}
