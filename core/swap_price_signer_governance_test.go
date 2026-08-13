package core

import (
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	swap "nhbchain/native/swap"
)

// govProposePayload/govFinalizePayload/govQueuePayload/govExecutePayload
// mirror the anonymous decode-side payload shapes in
// core/governance_tx.go's applyGovProposeTransaction/applyGovFinalizeTransaction/
// applyGovQueueTransaction/applyGovExecuteTransaction -- RLP encodes/decodes
// structs positionally, so these only need to match structurally.
type govProposePayload struct {
	Kind    string
	Payload string
	Deposit *big.Int
}

type govProposalIDPayload struct {
	ProposalID uint64
}

// submitGovTx builds, signs (with key, nonce 0 -- every helper here uses a
// freshly generated throwaway key per call, since finalize/queue/execute
// have no caller-identity requirement at the engine level and propose's
// deposit-holding account is seeded fresh per test), and commits a single
// governance transaction in its own block, driving the real
// AddTransaction -> mempool -> CreateBlock -> CommitBlock path instead of
// the removed Node.GovernancePropose/Queue/Execute direct-write methods.
func submitGovTx(t *testing.T, node *Node, key *crypto.PrivateKey, txType types.TxType, payload interface{}) {
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
		GasLimit: 100_000,
		GasPrice: big.NewInt(0),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign governance tx: %v", err)
	}
	if err := node.AddTransaction(tx); err != nil {
		t.Fatalf("add governance tx (type %d): %v", txType, err)
	}
	block, err := node.CreateBlock(append([]*types.Transaction(nil), node.mempool...))
	if err != nil {
		t.Fatalf("create block for governance tx: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block for governance tx: %v", err)
	}
}

// submitGovProposeTx submits a real signed TxTypeGovPropose transaction from
// a fresh key (funded with enough ZNHB to cover deposit) and returns the
// resulting proposal's ID, found by listing proposals after commit rather
// than assuming a specific sequential ID.
func submitGovProposeTx(t *testing.T, node *Node, kind, payload string, deposit *big.Int) (uint64, *crypto.PrivateKey) {
	t.Helper()
	proposerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate proposer key: %v", err)
	}
	proposerAddr := toAddress(proposerKey)
	if err := node.WithState(func(m *nhbstate.Manager) error {
		return m.PutAccount(proposerAddr[:], &types.Account{
			BalanceZNHB: big.NewInt(1_000_000),
			BalanceNHB:  big.NewInt(0),
			Stake:       big.NewInt(0),
		})
	}); err != nil {
		t.Fatalf("seed proposer: %v", err)
	}

	before, _, err := node.GovernanceListProposals(0, 100)
	if err != nil {
		t.Fatalf("list proposals before propose: %v", err)
	}
	seen := make(map[uint64]struct{}, len(before))
	for _, p := range before {
		seen[p.ID] = struct{}{}
	}

	submitGovTx(t, node, proposerKey, types.TxTypeGovPropose, govProposePayload{Kind: kind, Payload: payload, Deposit: deposit})

	after, _, err := node.GovernanceListProposals(0, 100)
	if err != nil {
		t.Fatalf("list proposals after propose: %v", err)
	}
	for _, p := range after {
		if _, ok := seen[p.ID]; !ok {
			return p.ID, proposerKey
		}
	}
	t.Fatalf("no new proposal found after submitting propose tx")
	return 0, nil
}

func submitGovQueueTx(t *testing.T, node *Node, proposalID uint64) {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate queue key: %v", err)
	}
	addr := toAddress(key)
	if err := node.WithState(func(m *nhbstate.Manager) error {
		return m.PutAccount(addr[:], &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)})
	}); err != nil {
		t.Fatalf("seed queue sender: %v", err)
	}
	submitGovTx(t, node, key, types.TxTypeGovQueue, govProposalIDPayload{ProposalID: proposalID})
}

func submitGovExecuteTx(t *testing.T, node *Node, proposalID uint64) {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate execute key: %v", err)
	}
	addr := toAddress(key)
	if err := node.WithState(func(m *nhbstate.Manager) error {
		return m.PutAccount(addr[:], &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)})
	}); err != nil {
		t.Fatalf("seed execute sender: %v", err)
	}
	submitGovTx(t, node, key, types.TxTypeGovExecute, govProposalIDPayload{ProposalID: proposalID})
}

// TestSwapPriceSignerGovernanceProposalEndToEndMint closes gap 2a end to
// end: it proves a governance proposal -- not a bespoke direct-write RPC --
// can register a swap.PriceProof signer against the node's real production
// state backend (*nhbstate.Manager), and that once registered, a properly
// signed price proof from that signer lets a real TxTypeSwapVoucherMint
// voucher mint successfully all the way through
// SwapSubmitVoucher -> AddTransaction -> mempool -> CreateBlock ->
// CommitBlock -> balance. This is the exact same SwapPriceSigner state
// native/swap.PriceProofEngine.Verify consults inside
// applySwapVoucherMintTransaction (core/swap_voucher_tx.go) -- so this test
// demonstrates the governance path is a genuine substitute for the
// registerSwapPriceSignerCore test-only helper used elsewhere in this file,
// not just a parallel code path that happens to also write the same key.
//
// The proposal is driven to Passed by directly setting proposal.Status
// (bypassing POTSO-weighted vote tallying) -- the same isolation pattern
// native/governance/engine_test.go's own TestExecuteRoleAllowlistProposal
// and TestExecuteSlashingPolicyProposal already use to test "does Execute
// correctly apply this payload" independently from "does the quorum/vote
// mechanism work" (a separate, already-covered concern).
func TestSwapPriceSignerGovernanceProposalEndToEndMint(t *testing.T) {
	node, minterKey, _ := setupSwapVoucherTestNode(t)

	oracleKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("oracle key: %v", err)
	}
	var oracleAddr [20]byte
	copy(oracleAddr[:], oracleKey.PubKey().Address().Bytes())

	// setupSwapVoucherTestNode already registered a "nowpayments" signer via
	// the test-only direct-write helper; register a SECOND, independent
	// provider ("otc-gateway") purely through the governance proposal path
	// so this test's assertions are unambiguously about the governance
	// mechanism, not incidentally reusing the helper's registration.
	const provider = "otc-gateway"

	payload := fmt.Sprintf(`{"provider":%q,"signerAddress":%q,"memo":"gov e2e test"}`,
		provider, crypto.MustNewAddress(crypto.NHBPrefix, oracleAddr[:]).String())

	proposalID, _ := submitGovProposeTx(t, node, governance.ProposalKindSwapPriceSignerUpdate, payload, big.NewInt(0))

	// Confirm it is NOT registered yet -- the proposal must actually be
	// executed before it takes effect, not merely submitted.
	if err := node.WithState(func(m *nhbstate.Manager) error {
		if _, ok, err := m.SwapPriceSigner(provider); err != nil {
			return err
		} else if ok {
			t.Fatalf("signer must not be registered before proposal execution")
		}
		return nil
	}); err != nil {
		t.Fatalf("pre-execution check: %v", err)
	}

	markProposalPassed(t, node, proposalID)

	submitGovQueueTx(t, node, proposalID)

	clearProposalTimelock(t, node, proposalID)

	submitGovExecuteTx(t, node, proposalID)

	// Confirm registration landed in the exact state SwapPriceSigner reads,
	// via the same governance.Engine.Execute -> nhbstate.Manager.SwapSetPriceSigner
	// call path a real validator would run.
	var gotSigner [20]byte
	if err := node.WithState(func(m *nhbstate.Manager) error {
		signer, ok, err := m.SwapPriceSigner(provider)
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("expected registered signer for provider %q", provider)
		}
		gotSigner = signer
		return nil
	}); err != nil {
		t.Fatalf("post-execution check: %v", err)
	}
	if gotSigner != oracleAddr {
		t.Fatalf("unexpected registered signer: got %x want %x", gotSigner, oracleAddr)
	}

	// --- Now prove the registration actually gates a real mint. ---
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	recipient := toAddress(recipientKey)

	voucher := swapVoucherTestVoucher(node.chain.ChainID(), recipient, "0.05", "ORDER-GOV-SIGNER")
	sig := signSwapVoucherCore(t, minterKey, voucher)
	proof := signedPriceProofCore(t, oracleKey, provider, "0.05", time.Now())

	submission := &swap.VoucherSubmission{
		Voucher:      &voucher,
		Signature:    sig,
		Provider:     provider,
		ProviderTxID: "PROVIDER-GOV-SIGNER-1",
		PriceProof:   proof,
	}
	if _, _, err := node.SwapSubmitVoucher(submission); err != nil {
		t.Fatalf("submit voucher: %v", err)
	}
	block, err := node.CreateBlock(append([]*types.Transaction(nil), node.mempool...))
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	account, err := node.GetAccount(recipient[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.BalanceZNHB.Cmp(voucher.Amount) != 0 {
		t.Fatalf("expected ZNHB balance %s, got %s", voucher.Amount, account.BalanceZNHB)
	}
}

// TestSwapPriceSignerGovernanceProposalRevoke proves the companion revoke
// path removes a previously-registered signer, and that a subsequent
// submission using that now-revoked signer's key is correctly rejected
// (ErrSwapPriceProofSignerUnknown) -- i.e. revocation actually closes the
// gate, it does not merely leave stale state that happens not to be checked.
func TestSwapPriceSignerGovernanceProposalRevoke(t *testing.T) {
	node, minterKey, oracleKey := setupSwapVoucherTestNode(t)

	// Sanity: the signer registered by setupSwapVoucherTestNode works before
	// the revoke.
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	recipient := toAddress(recipientKey)

	revokePayload := `{"provider":"nowpayments","revoke":true}`
	proposalID, _ := submitGovProposeTx(t, node, governance.ProposalKindSwapPriceSignerUpdate, revokePayload, big.NewInt(0))
	markProposalPassed(t, node, proposalID)
	submitGovQueueTx(t, node, proposalID)
	clearProposalTimelock(t, node, proposalID)
	submitGovExecuteTx(t, node, proposalID)

	if err := node.WithState(func(m *nhbstate.Manager) error {
		if _, ok, err := m.SwapPriceSigner("nowpayments"); err != nil {
			return err
		} else if ok {
			t.Fatalf("expected signer to be removed after revoke")
		}
		return nil
	}); err != nil {
		t.Fatalf("post-revoke check: %v", err)
	}

	voucher := swapVoucherTestVoucher(node.chain.ChainID(), recipient, "0.05", "ORDER-GOV-REVOKED")
	sig := signSwapVoucherCore(t, minterKey, voucher)
	proof := signedPriceProofCore(t, oracleKey, "nowpayments", "0.05", time.Now())
	submission := &swap.VoucherSubmission{
		Voucher:      &voucher,
		Signature:    sig,
		Provider:     "nowpayments",
		ProviderTxID: "PROVIDER-GOV-REVOKED-1",
		PriceProof:   proof,
	}
	if _, _, err := node.SwapSubmitVoucher(submission); !errors.Is(err, ErrSwapPriceProofSignerUnknown) {
		t.Fatalf("expected ErrSwapPriceProofSignerUnknown after revoke, got %v", err)
	}
}

func markProposalPassed(t *testing.T, node *Node, proposalID uint64) {
	t.Helper()
	if err := node.WithState(func(m *nhbstate.Manager) error {
		proposal, ok, err := m.GovernanceGetProposal(proposalID)
		if err != nil {
			return err
		}
		if !ok || proposal == nil {
			return fmt.Errorf("proposal %d not found", proposalID)
		}
		proposal.Status = governance.ProposalStatusPassed
		return m.GovernancePutProposal(proposal)
	}); err != nil {
		t.Fatalf("mark proposal passed: %v", err)
	}
}

func clearProposalTimelock(t *testing.T, node *Node, proposalID uint64) {
	t.Helper()
	if err := node.WithState(func(m *nhbstate.Manager) error {
		proposal, ok, err := m.GovernanceGetProposal(proposalID)
		if err != nil {
			return err
		}
		if !ok || proposal == nil {
			return fmt.Errorf("proposal %d not found", proposalID)
		}
		proposal.TimelockEnd = time.Now().Add(-time.Second)
		return m.GovernancePutProposal(proposal)
	}); err != nil {
		t.Fatalf("clear proposal timelock: %v", err)
	}
}
