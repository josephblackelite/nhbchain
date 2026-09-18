package core

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/consensus/potso/evidence"
	"nhbchain/core/genesis"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// TestSubmitEvidenceTxTypeByteValue pins down the new TxType byte value so a
// future merge can never silently reassign or collide it, mirroring
// TestSwapAdminTxTypeByteValues' precedent (core/swap_admin_tx_test.go).
func TestSubmitEvidenceTxTypeByteValue(t *testing.T) {
	if types.TxTypeSubmitEvidence != 0x4C {
		t.Fatalf("expected TxTypeSubmitEvidence == 0x4C, got 0x%02X", byte(types.TxTypeSubmitEvidence))
	}
	// Senderless -- evidence.ValidateEvidence already recovers and verifies
	// Evidence.ReporterSig against Evidence.Reporter (see the TxType's doc
	// comment in core/types/transaction.go for why no separate envelope
	// signature is required).
	if types.RequiresSignature(types.TxTypeSubmitEvidence) {
		t.Fatalf("expected TxTypeSubmitEvidence to be senderless (RequiresSignature=false)")
	}
}

// evidenceTestKey is a minimal secp256k1 keypair helper for building and
// signing test evidence, independent of consensus/potso/penalty's own
// c10TestKey (different package, same idea).
type evidenceTestKey struct {
	priv *ecdsa.PrivateKey
}

func newEvidenceTestKey(t *testing.T) *evidenceTestKey {
	t.Helper()
	priv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return &evidenceTestKey{priv: priv}
}

func (k *evidenceTestKey) address() [20]byte {
	addr := ethcrypto.PubkeyToAddress(k.priv.PublicKey)
	var out [20]byte
	copy(out[:], addr.Bytes())
	return out
}

// equivocationVotePayloadMirror independently reproduces
// consensus/potso/evidence's own unexported equivocationVotePayload --
// exactly the same field names/JSON tags/types/order that package hashes
// via sha256(json.Marshal(...)) before recovering a signer. This codebase's
// established convention (see evidence/equivocation.go's own doc comment)
// is independent, parity-tested mirrors instead of exporting internals
// across a package boundary purely for tests.
type equivocationVotePayloadMirror struct {
	BlockHash []byte                        `json:"blockHash"`
	Round     int                           `json:"round"`
	Type      evidence.EquivocationVoteType `json:"type"`
	Height    uint64                        `json:"height"`
}

func (k *evidenceTestKey) signVote(blockHash []byte, round int, voteType evidence.EquivocationVoteType, height uint64) evidence.EquivocationSignedVote {
	payload := &equivocationVotePayloadMirror{BlockHash: blockHash, Round: round, Type: voteType, Height: height}
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(b)
	sig, err := ethcrypto.Sign(digest[:], k.priv)
	if err != nil {
		panic(err)
	}
	return evidence.EquivocationSignedVote{
		BlockHash: "0x" + hex.EncodeToString(blockHash),
		Signature: "0x" + hex.EncodeToString(sig),
	}
}

// buildGenuineEquivocationEvidence builds a TypeEquivocation Evidence report,
// signed by reporter, whose embedded proof genuinely proves offenderKey
// signed two conflicting votes at the same (height, round, type) -- i.e. one
// that evidence.ValidateEvidence must accept.
func buildGenuineEquivocationEvidence(t *testing.T, offenderKey, reporterKey *evidenceTestKey, height uint64) (evidence.Evidence, [32]byte) {
	t.Helper()
	proof := &evidence.EquivocationProof{
		Height:   height,
		Round:    1,
		VoteType: evidence.EquivocationVotePrevote,
		VoteA:    offenderKey.signVote([]byte{0x01}, 1, evidence.EquivocationVotePrevote, height),
		VoteB:    offenderKey.signVote([]byte{0x02}, 1, evidence.EquivocationVotePrevote, height),
	}
	return buildAndSignEvidenceTx(t, offenderKey.address(), reporterKey, proof, height)
}

// buildForgedEquivocationEvidence builds a TypeEquivocation Evidence report
// whose embedded "proof" is entirely self-signed by the reporter -- never
// touching offenderKey at all -- i.e. one evidence.ValidateEvidence must
// reject.
func buildForgedEquivocationEvidence(t *testing.T, offenderKey, reporterKey *evidenceTestKey, height uint64) (evidence.Evidence, [32]byte) {
	t.Helper()
	proof := &evidence.EquivocationProof{
		Height:   height,
		Round:    1,
		VoteType: evidence.EquivocationVotePrevote,
		VoteA:    reporterKey.signVote([]byte{0x01}, 1, evidence.EquivocationVotePrevote, height),
		VoteB:    reporterKey.signVote([]byte{0x02}, 1, evidence.EquivocationVotePrevote, height),
	}
	return buildAndSignEvidenceTx(t, offenderKey.address(), reporterKey, proof, height)
}

func buildAndSignEvidenceTx(t *testing.T, offender [20]byte, reporterKey *evidenceTestKey, proof *evidence.EquivocationProof, height uint64) (evidence.Evidence, [32]byte) {
	t.Helper()
	details, err := json.Marshal(proof)
	if err != nil {
		t.Fatalf("marshal proof: %v", err)
	}
	ev := evidence.Evidence{
		Type:      evidence.TypeEquivocation,
		Offender:  offender,
		Heights:   []uint64{height},
		Details:   details,
		Reporter:  reporterKey.address(),
		Timestamp: 1,
	}
	hash, err := ev.CanonicalHash()
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	sig, err := ethcrypto.Sign(ev.SigningDigest(hash), reporterKey.priv)
	if err != nil {
		t.Fatalf("sign evidence: %v", err)
	}
	ev.ReporterSig = sig
	return ev, hash
}

// writeEvidenceTestGenesis writes a fixed genesis spec declaring an explicit
// AdminWallet (the slashing treasury target), so two nodes built from it
// share the SAME treasury address -- otherwise each node's escrowTreasury
// would default to its own random validator address (see NewNode's treasury
// fallback), making their post-slash tries diverge merely because the
// credited account differs, not because of anything this fix is meant to
// prove.
func writeEvidenceTestGenesis(t *testing.T) (path string, treasuryAddr [20]byte) {
	t.Helper()
	genesisValidatorKeyA, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate genesis validator key A: %v", err)
	}
	genesisValidatorKeyB, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate genesis validator key B: %v", err)
	}
	treasuryKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate treasury key: %v", err)
	}
	copy(treasuryAddr[:], treasuryKey.PubKey().Address().Bytes())
	spec := genesis.GenesisSpec{
		GenesisTime:  "2024-01-01T00:00:00Z",
		NativeTokens: []genesis.NativeTokenSpec{{Symbol: "NHB", Name: "NHBCoin", Decimals: 18}, {Symbol: "ZNHB", Name: "zNHBCoin", Decimals: 18}},
		AdminWallet:  treasuryKey.PubKey().Address().String(),
		Validators: []genesis.ValidatorSpec{
			{Address: genesisValidatorKeyA.PubKey().Address().String(), Power: 11440},
			{Address: genesisValidatorKeyB.PubKey().Address().String(), Power: 11336},
		},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal genesis spec: %v", err)
	}
	genesisPath := filepath.Join(t.TempDir(), "genesis.json")
	if err := os.WriteFile(genesisPath, data, 0o644); err != nil {
		t.Fatalf("write genesis file: %v", err)
	}
	return genesisPath, treasuryAddr
}

// seedEvidenceOffenderStake gives offender a real bonded stake (Stake) and
// matching LockedZNHB -- both consumed by processPendingEvidenceForState's
// EnsureBaseline call and state/bank.ValidatorSlasher.Slash respectively
// (see slash.go: Slash caps the penalty at LockedZNHB, so a zero LockedZNHB
// would silently slash nothing even with a real Stake).
func seedEvidenceOffenderStake(t *testing.T, node *Node, offender [20]byte, amount *big.Int) {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	manager := nhbstate.NewManager(node.state.Trie)
	account, err := manager.GetAccount(offender[:])
	if err != nil {
		t.Fatalf("load offender account: %v", err)
	}
	account.Stake = new(big.Int).Set(amount)
	account.LockedZNHB = new(big.Int).Set(amount)
	if err := manager.PutAccount(offender[:], account); err != nil {
		t.Fatalf("seed offender stake: %v", err)
	}
}

// TestSubmitEvidenceApply_IndependentStatesAgree is the direct regression
// test for the consensus-safety property NHB-AUDIT-C10's follow-up
// establishes: applying the identical TxTypeSubmitEvidence transaction
// against two independently-constructed nodes (seeded identically, never
// sharing memory), then independently running processPendingEvidenceForState
// on each, produces byte-identical resulting state roots AND identical
// slashing outcomes. Before this fix, evidence only ever entered a
// node-local evidence.Store via a direct RPC call -- a validator that never
// independently received that exact call would never learn of it at all.
func TestSubmitEvidenceApply_IndependentStatesAgree(t *testing.T) {
	genesisPath, treasuryAddr := writeEvidenceTestGenesis(t)
	nodeA := buildSwapAdminTestNode(t, genesisPath)
	nodeB := buildSwapAdminTestNode(t, genesisPath)

	offenderKey := newEvidenceTestKey(t)
	reporterKey := newEvidenceTestKey(t)
	offender := offenderKey.address()

	const evidenceHeight = 50
	const applyHeight = 200

	stakeAmount := big.NewInt(1_000)
	seedEvidenceOffenderStake(t, nodeA, offender, stakeAmount)
	seedEvidenceOffenderStake(t, nodeB, offender, stakeAmount)

	preRootA := nodeA.state.PendingRoot()
	preRootB := nodeB.state.PendingRoot()
	if preRootA != preRootB {
		t.Fatalf("expected identical starting roots after identical seeding, got %s vs %s", preRootA, preRootB)
	}

	ev, hash := buildGenuineEquivocationEvidence(t, offenderKey, reporterKey, evidenceHeight)
	payload, err := encodeSubmitEvidenceTransaction(ev)
	if err != nil {
		t.Fatalf("encode evidence tx: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSubmitEvidence,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}

	blockTime := time.Unix(1_700_000_000, 0).UTC()
	for _, node := range []*Node{nodeA, nodeB} {
		node.stateMu.Lock()
		node.state.BeginBlock(applyHeight, blockTime)
		applyErr := node.state.ApplyTransaction(tx)
		node.state.EndBlock()
		node.stateMu.Unlock()
		if applyErr != nil {
			t.Fatalf("apply evidence transaction: %v", applyErr)
		}
	}

	postSubmitRootA := nodeA.state.PendingRoot()
	postSubmitRootB := nodeB.state.PendingRoot()
	if postSubmitRootA != postSubmitRootB {
		t.Fatalf("state roots diverged after applying the identical evidence transaction: %s vs %s", postSubmitRootA, postSubmitRootB)
	}
	if postSubmitRootA == preRootA {
		t.Fatalf("expected recording the evidence to actually change state -- root did not move")
	}

	// Both nodes must now independently agree the evidence exists, purely
	// from having applied the same transaction -- never from a shared store.
	for _, node := range []*Node{nodeA, nodeB} {
		record, ok, err := node.PotsoEvidenceByHash(hash)
		if err != nil || !ok {
			t.Fatalf("expected evidence record to be recorded: ok=%v err=%v", ok, err)
		}
		if record.Evidence.Offender != offender {
			t.Fatalf("recorded offender mismatch: got %x want %x", record.Evidence.Offender, offender)
		}
	}

	for _, node := range []*Node{nodeA, nodeB} {
		node.stateMu.Lock()
		node.state.BeginBlock(applyHeight, blockTime)
		procErr := node.processPendingEvidenceForState(node.state, applyHeight)
		node.state.EndBlock()
		node.stateMu.Unlock()
		if procErr != nil {
			t.Fatalf("process pending evidence: %v", procErr)
		}
	}

	postPenaltyRootA := nodeA.state.PendingRoot()
	postPenaltyRootB := nodeB.state.PendingRoot()
	if postPenaltyRootA != postPenaltyRootB {
		t.Fatalf("state roots diverged after independently processing the identical evidence: %s vs %s", postPenaltyRootA, postPenaltyRootB)
	}
	if postPenaltyRootA == postSubmitRootA {
		t.Fatalf("expected processing the evidence to actually apply a penalty -- root did not move")
	}

	// Slashing outcome must match exactly, on both independently-processed
	// nodes: 100% of the offender's stake (EquivocationSlashBps=10000)
	// moved from the offender's Stake/LockedZNHB into the shared treasury.
	for _, node := range []*Node{nodeA, nodeB} {
		node.stateMu.Lock()
		manager := nhbstate.NewManager(node.state.Trie)
		offenderAcct, err := manager.GetAccount(offender[:])
		if err != nil {
			node.stateMu.Unlock()
			t.Fatalf("load offender account: %v", err)
		}
		treasuryAcct, err := manager.GetAccount(treasuryAddr[:])
		node.stateMu.Unlock()
		if err != nil {
			t.Fatalf("load treasury account: %v", err)
		}
		if offenderAcct.Stake == nil || offenderAcct.Stake.Sign() != 0 {
			t.Fatalf("expected offender stake fully slashed to zero, got %v", offenderAcct.Stake)
		}
		if offenderAcct.LockedZNHB == nil || offenderAcct.LockedZNHB.Sign() != 0 {
			t.Fatalf("expected offender LockedZNHB fully slashed to zero, got %v", offenderAcct.LockedZNHB)
		}
		if treasuryAcct.BalanceZNHB == nil || treasuryAcct.BalanceZNHB.Cmp(stakeAmount) != 0 {
			t.Fatalf("expected treasury credited exactly %s ZNHB, got %v", stakeAmount, treasuryAcct.BalanceZNHB)
		}
	}
}

// TestSubmitEvidenceApply_RejectsForgedEvidence proves a forged equivocation
// report (the reporter self-signs both "votes", never touching the
// offender's key) is rejected at ApplyTransaction time -- it never reaches
// the trie, never appears via PotsoEvidenceByHash, and never moves the state
// root -- exercising evidence.ValidateEvidence's existing
// VerifyEquivocationProof gate through the new transaction-apply path
// rather than replacing or bypassing it.
func TestSubmitEvidenceApply_RejectsForgedEvidence(t *testing.T) {
	genesisPath, _ := writeEvidenceTestGenesis(t)
	node := buildSwapAdminTestNode(t, genesisPath)

	offenderKey := newEvidenceTestKey(t)
	reporterKey := newEvidenceTestKey(t)

	const evidenceHeight = 50
	const applyHeight = 200

	ev, hash := buildForgedEquivocationEvidence(t, offenderKey, reporterKey, evidenceHeight)
	payload, err := encodeSubmitEvidenceTransaction(ev)
	if err != nil {
		t.Fatalf("encode evidence tx: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSubmitEvidence,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}

	preRoot := node.state.PendingRoot()

	node.stateMu.Lock()
	node.state.BeginBlock(applyHeight, time.Unix(1_700_000_000, 0).UTC())
	applyErr := node.state.ApplyTransaction(tx)
	node.state.EndBlock()
	node.stateMu.Unlock()

	if applyErr == nil {
		t.Fatalf("expected a forged equivocation proof to be rejected at apply time")
	}
	var verr *evidence.ValidationError
	if !errors.As(applyErr, &verr) {
		t.Fatalf("expected *evidence.ValidationError, got %T: %v", applyErr, applyErr)
	}
	if verr.Reason != evidence.RejectReasonInvalidEquivocationProof {
		t.Fatalf("expected reject reason %q, got %q", evidence.RejectReasonInvalidEquivocationProof, verr.Reason)
	}

	postRoot := node.state.PendingRoot()
	if postRoot != preRoot {
		t.Fatalf("expected rejected evidence to leave state root unchanged: pre=%s post=%s", preRoot, postRoot)
	}
	if _, ok, err := node.PotsoEvidenceByHash(hash); err == nil && ok {
		t.Fatalf("expected forged evidence to never be recorded")
	}
}

// TestPotsoSubmitEvidenceGoesThroughTransactionPipeline proves the kept RPC
// convenience path (Node.PotsoSubmitEvidence) no longer mutates state
// directly: right after submission the evidence must NOT yet be visible via
// PotsoEvidenceByHash (only mempool-admitted, exactly like
// SwapReverseVoucher's identical "enqueue now, consensus mutates later"
// contract) -- it only becomes visible once a block carrying that exact
// transaction is actually committed.
func TestPotsoSubmitEvidenceGoesThroughTransactionPipeline(t *testing.T) {
	// Uses writeSwapAdminGenesis (no declared AdminWallet), not
	// writeEvidenceTestGenesis: this single-node test has no need for a
	// shared treasury address across nodes, and declaring one triggers an
	// unrelated first-block ZNHB sale/reward pool bootstrap requirement
	// (the admin wallet must hold a positive ZNHB balance) that has nothing
	// to do with what this test is proving.
	genesisPath := writeSwapAdminGenesis(t)
	node := buildSwapAdminTestNode(t, genesisPath)

	offenderKey := newEvidenceTestKey(t)
	reporterKey := newEvidenceTestKey(t)
	const evidenceHeight = 1

	ev, hash := buildGenuineEquivocationEvidence(t, offenderKey, reporterKey, evidenceHeight)

	receipt, err := node.PotsoSubmitEvidence(ev)
	if err != nil {
		t.Fatalf("submit evidence: %v", err)
	}
	if receipt.Status != evidence.ReceiptStatusAccepted {
		t.Fatalf("expected Accepted status, got %s", receipt.Status)
	}
	if receipt.Hash != hash {
		t.Fatalf("receipt hash mismatch: got %x want %x", receipt.Hash, hash)
	}

	// Not yet applied for real -- only admitted to the mempool.
	if _, ok, err := node.PotsoEvidenceByHash(hash); err == nil && ok {
		t.Fatalf("expected evidence to NOT be recorded before any block commits it")
	}

	pending := node.GetMempool()
	if len(pending) != 1 || pending[0].Type != types.TxTypeSubmitEvidence {
		t.Fatalf("expected exactly one pending TxTypeSubmitEvidence transaction, got %+v", pending)
	}

	block, err := node.CreateBlock(pending)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	record, ok, err := node.PotsoEvidenceByHash(hash)
	if err != nil || !ok {
		t.Fatalf("expected evidence to be recorded after commit: ok=%v err=%v", ok, err)
	}
	if record.Evidence.Offender != offenderKey.address() {
		t.Fatalf("recorded offender mismatch after commit")
	}

	// Resubmitting the identical, already-recorded evidence must now be
	// reported as idempotent, not accepted again.
	idempotentReceipt, err := node.PotsoSubmitEvidence(ev)
	if err != nil {
		t.Fatalf("resubmit evidence: %v", err)
	}
	if idempotentReceipt.Status != evidence.ReceiptStatusIdempotent {
		t.Fatalf("expected Idempotent status on resubmission, got %s", idempotentReceipt.Status)
	}
}
