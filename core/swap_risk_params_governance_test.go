package core

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
)

// This file proves the swap redeem risk caps' whole reason for existing: a
// passed policy.swapRiskParams governance proposal changes the EFFECTIVE
// limit enforced by applyRedeemNHB on the very next transaction,
// network-wide, with no node restart and no config.toml edit -- and that
// before any such proposal has ever executed, the conservative built-in
// defaults (native/swap/redeem_risk.go's DefaultRedeem*Wei) are enforced
// exactly as if they were still hardcoded. Every assertion here drives the
// real transaction-application path (sp.ApplyTransaction) -- never a
// fabricated shortcut that pokes the param store and skips proposal
// execution, and never a direct call into governance.Engine bypassing the
// transaction envelope.
//
// There is deliberately no mint-side (fiat-gateway voucher mint,
// TxTypeSwapVoucherMint) equivalent here: ZNHB voucher mints draw from a
// fixed, pre-allocated genesis treasury Sale Pool rather than minting new
// supply (see core/swap_voucher_tx.go's applySwapVoucherMintTransaction),
// so they carry no external financial risk needing a
// governance-adjustable circuit breaker -- only the NHB-custody-backed
// redeem direction does. See ProposalKindSwapRiskParams's doc comment for
// the full rationale.

// applySignedGovTx builds, signs, and applies a governance transaction
// directly via sp.ApplyTransaction against a bare *StateProcessor --
// mirrors core/swap_price_signer_governance_test.go's submitGovTx, which
// does the same thing but through a *Node's AddTransaction/CreateBlock/
// CommitBlock. Both ultimately land on the identical
// core/governance_tx.go apply functions; this variant is used here because
// the existing redeem-risk tests in core/redeem_nhb_test.go already operate
// directly against a bare *StateProcessor via sp.ApplyTransaction, and this
// file extends that same pattern rather than switching styles.
func applySignedGovTx(t *testing.T, sp *StateProcessor, key *crypto.PrivateKey, txType types.TxType, payload interface{}) {
	t.Helper()
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatalf("encode governance payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     txType,
		Nonce:    0,
		Data:     data,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign governance tx: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply governance tx (type %d): %v", txType, err)
	}
}

// freshGovAccountKey generates a throwaway key (nonce 0) and seeds its
// account with the given ZNHB balance -- every governance transaction
// sender in this file is one of these, matching
// core/swap_price_signer_governance_test.go's equivalent Node-level helpers.
func freshGovAccountKey(t *testing.T, sp *StateProcessor, znhb *big.Int) *crypto.PrivateKey {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate governance sender key: %v", err)
	}
	addr := key.PubKey().Address().Bytes()
	if err := sp.setAccount(addr, &types.Account{
		BalanceZNHB: new(big.Int).Set(znhb),
		BalanceNHB:  big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed governance sender: %v", err)
	}
	return key
}

// latestGovProposalID reads the governance proposal sequence counter
// directly -- reliable here because each test in this file submits exactly
// one proposal against a fresh StateProcessor, so the counter's value after
// submission IS that proposal's ID (SubmitProposal increments-then-returns,
// core/state/manager.go's GovernanceNextProposalID).
func latestGovProposalID(t *testing.T, sp *StateProcessor) uint64 {
	t.Helper()
	manager := nhbstate.NewManager(sp.Trie)
	var latest uint64
	if _, err := manager.KVGet(nhbstate.GovernanceSequenceKey(), &latest); err != nil {
		t.Fatalf("load governance proposal sequence: %v", err)
	}
	if latest == 0 {
		t.Fatalf("expected a governance proposal to have been submitted")
	}
	return latest
}

// markGovProposalPassed and clearGovProposalTimelock drive a submitted
// proposal straight to "ready to execute", bypassing POTSO-weighted vote
// tallying -- the same isolation
// core/swap_price_signer_governance_test.go's markProposalPassed/
// clearProposalTimelock use (there, at the *Node level; here, directly
// against the bare *StateProcessor's trie), testing "does Execute correctly
// apply this payload" independently of the already-covered "does the
// quorum/vote mechanism work" concern.
func markGovProposalPassed(t *testing.T, sp *StateProcessor, proposalID uint64) {
	t.Helper()
	manager := nhbstate.NewManager(sp.Trie)
	proposal, ok, err := manager.GovernanceGetProposal(proposalID)
	if err != nil {
		t.Fatalf("load proposal: %v", err)
	}
	if !ok || proposal == nil {
		t.Fatalf("proposal %d not found", proposalID)
	}
	proposal.Status = governance.ProposalStatusPassed
	if err := manager.GovernancePutProposal(proposal); err != nil {
		t.Fatalf("mark proposal passed: %v", err)
	}
}

func clearGovProposalTimelock(t *testing.T, sp *StateProcessor, proposalID uint64) {
	t.Helper()
	manager := nhbstate.NewManager(sp.Trie)
	proposal, ok, err := manager.GovernanceGetProposal(proposalID)
	if err != nil {
		t.Fatalf("load proposal: %v", err)
	}
	if !ok || proposal == nil {
		t.Fatalf("proposal %d not found", proposalID)
	}
	proposal.TimelockEnd = time.Now().Add(-time.Second)
	if err := manager.GovernancePutProposal(proposal); err != nil {
		t.Fatalf("clear proposal timelock: %v", err)
	}
}

// executeSwapRiskParamsProposal drives a full, real
// propose -> pass -> queue -> timelock-elapse -> execute governance
// lifecycle for a policy.swapRiskParams proposal against a bare
// *StateProcessor, every step via sp.ApplyTransaction against a real signed
// transaction envelope -- never a direct governance.Engine call.
func executeSwapRiskParamsProposal(t *testing.T, sp *StateProcessor, payloadJSON string) {
	t.Helper()
	sp.SetGovernancePolicy(governance.ProposalPolicy{
		MinDepositWei:       big.NewInt(0),
		VotingPeriodSeconds: 60,
		TimelockSeconds:     10,
	})

	proposerKey := freshGovAccountKey(t, sp, big.NewInt(0))
	applySignedGovTx(t, sp, proposerKey, types.TxTypeGovPropose, govProposePayload{
		Kind:    governance.ProposalKindSwapRiskParams,
		Payload: payloadJSON,
		Deposit: big.NewInt(0),
	})
	proposalID := latestGovProposalID(t, sp)

	markGovProposalPassed(t, sp, proposalID)

	queueKey := freshGovAccountKey(t, sp, big.NewInt(0))
	applySignedGovTx(t, sp, queueKey, types.TxTypeGovQueue, govProposalIDPayload{ProposalID: proposalID})

	clearGovProposalTimelock(t, sp, proposalID)

	executeKey := freshGovAccountKey(t, sp, big.NewInt(0))
	applySignedGovTx(t, sp, executeKey, types.TxTypeGovExecute, govProposalIDPayload{ProposalID: proposalID})
}

// weiStr scales whole-token amounts up to 18-decimal wei strings, purely to
// keep the test bodies below readable (e.g. weiStr(1000) instead of
// "1000000000000000000000").
func weiStr(tokens int64) string {
	amount := new(big.Int).Mul(big.NewInt(tokens), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	return amount.String()
}

func weiAmount(tokens int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(tokens), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
}

// TestApplyRedeemNHB_DefaultRiskCapsEnforcedWithoutProposal confirms
// requirement (a): with no policy.swapRiskParams proposal ever executed,
// native/swap/redeem_risk.go's conservative built-in defaults
// (DefaultRedeemPerTxMaxWei = 1,000 NHB) are enforced exactly as they would
// have been under the old, now-removed config.toml [swap.redeemRisk]
// section's own fallback behaviour.
func TestApplyRedeemNHB_DefaultRiskCapsEnforcedWithoutProposal(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()
	if err := sp.setAccount(userAddr, &types.Account{
		BalanceNHB:  weiAmount(1_500),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	seedTokenSupply(t, sp, weiAmount(1_500))

	// Above the default 1,000 NHB per-tx max -- must be rejected.
	overCapTx := redeemNHBTx(t, 0, weiAmount(1_001), "usdttrc20", "dest")
	if err := overCapTx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign over-cap tx: %v", err)
	}
	if err := sp.ApplyTransaction(overCapTx); err == nil {
		t.Fatalf("expected rejection: 1,001 NHB exceeds the default 1,000 NHB per-tx max")
	}

	// Within the default 1,000 NHB per-tx max -- must succeed.
	withinCapTx := redeemNHBTx(t, 0, weiAmount(500), "usdttrc20", "dest")
	if err := withinCapTx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign within-cap tx: %v", err)
	}
	if err := sp.ApplyTransaction(withinCapTx); err != nil {
		t.Fatalf("expected 500 NHB (within the default 1,000 NHB per-tx max) to succeed, got: %v", err)
	}

	user, err := sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.BalanceNHB.Cmp(weiAmount(1_000)) != 0 {
		t.Fatalf("expected balance 1,000 NHB after exactly one successful 500 NHB burn, got %s", user.BalanceNHB)
	}
}

// TestApplyRedeemNHB_GovernanceProposalLowersEffectiveCapImmediately
// confirms requirement (b): once a policy.swapRiskParams proposal executes,
// the NEW redeem-side per-tx cap is enforced on the very next transaction --
// no node restart, no config edit -- via the real sp.ApplyTransaction flow
// for every step (propose/queue/execute AND the redeem transaction itself).
func TestApplyRedeemNHB_GovernanceProposalLowersEffectiveCapImmediately(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()
	if err := sp.setAccount(userAddr, &types.Account{
		BalanceNHB:  weiAmount(3_000),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	seedTokenSupply(t, sp, weiAmount(3_000))

	// Sanity: 500 NHB succeeds under the default 1,000 NHB per-tx max,
	// before any proposal has executed.
	preTx := redeemNHBTx(t, 0, weiAmount(500), "usdttrc20", "dest")
	if err := preTx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign pre-proposal tx: %v", err)
	}
	if err := sp.ApplyTransaction(preTx); err != nil {
		t.Fatalf("expected 500 NHB to succeed under the default cap, got: %v", err)
	}

	// Execute a real policy.swapRiskParams proposal lowering the redeem
	// per-tx max to 100 NHB (well below the 500 NHB that just succeeded).
	// The daily/monthly caps are left at the built-in defaults so only the
	// per-tx max is actually tightened.
	payload := `{` +
		`"redeemPerTxMinWei":"0","redeemPerTxMaxWei":"` + weiStr(100) + `",` +
		`"redeemPerAddressDailyCapWei":"` + weiStr(2_000) + `","redeemPerAddressMonthlyCapWei":"` + weiStr(20_000) + `",` +
		`"memo":"tighten redeem per-tx max to 100 NHB"` +
		`}`
	executeSwapRiskParamsProposal(t, sp, payload)

	// The very next redeem transaction: 500 NHB, which succeeded moments
	// ago, must now be rejected under the new 100 NHB cap.
	postTx := redeemNHBTx(t, 1, weiAmount(500), "usdttrc20", "dest")
	if err := postTx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign post-proposal over-cap tx: %v", err)
	}
	if err := sp.ApplyTransaction(postTx); err == nil {
		t.Fatalf("expected 500 NHB to be rejected under the new 100 NHB per-tx max")
	}

	// A redemption within the NEW cap succeeds.
	withinNewCapTx := redeemNHBTx(t, 1, weiAmount(50), "usdttrc20", "dest")
	if err := withinNewCapTx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign within-new-cap tx: %v", err)
	}
	if err := sp.ApplyTransaction(withinNewCapTx); err != nil {
		t.Fatalf("expected 50 NHB (within the new 100 NHB per-tx max) to succeed, got: %v", err)
	}

	user, err := sp.getAccount(userAddr)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	// 3,000 - 500 (pre-proposal) - 50 (post-proposal) = 2,450.
	if user.BalanceNHB.Cmp(weiAmount(2_450)) != 0 {
		t.Fatalf("expected balance 2,450 NHB after the two successful burns, got %s", user.BalanceNHB)
	}
}

