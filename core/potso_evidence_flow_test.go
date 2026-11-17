package core

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nhbchain/core/genesis"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// evidenceFlow is a two-validator harness for the evidence/slash path: a
// proposer and a peer, each an independent Node (own MemDB) built from one
// shared genesis, driven through the real CreateBlock / ValidateBlock /
// CommitBlock entry points in the order the BFT engine uses them. Every
// balance the scenarios need comes from the genesis file, and every state
// change after that is a real signed transaction: a Node that validates a
// block it did not propose resets any pending trie writes back to its last
// committed root (see newBuybackConsensusHarness), so direct trie seeding
// would be silently wiped.
type evidenceFlow struct {
	t           *testing.T
	proposer    *Node
	peer        *Node
	genesisPath string
	fixedTime   time.Time
	admin       *crypto.PrivateKey
	offender    *crypto.PrivateKey
	reporter    *crypto.PrivateKey
	bystander   *crypto.PrivateKey
	chain       []*types.Block
}

func evidenceZNHB(whole int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(whole), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
}

func newEvidenceFlow(t *testing.T) *evidenceFlow {
	t.Helper()
	newKey := func() *crypto.PrivateKey {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		return key
	}
	f := &evidenceFlow{
		t:         t,
		fixedTime: time.Unix(1_800_000_000, 0).UTC(),
		admin:     newKey(),
		offender:  newKey(),
		reporter:  newKey(),
		bystander: newKey(),
	}
	validatorA, validatorB := newKey(), newKey()
	spec := genesis.GenesisSpec{
		GenesisTime:  "2024-01-01T00:00:00Z",
		NativeTokens: []genesis.NativeTokenSpec{{Symbol: "NHB", Name: "NHBCoin", Decimals: 18}, {Symbol: "ZNHB", Name: "zNHBCoin", Decimals: 18}},
		Validators: []genesis.ValidatorSpec{
			{Address: validatorA.PubKey().Address().String(), Power: 11440},
			{Address: validatorB.PubKey().Address().String(), Power: 11336},
		},
		Alloc: map[string]map[string]string{
			f.admin.PubKey().Address().String():     {"NHB": "0", "ZNHB": znhbExpectedTotalSupplyWei.String()},
			f.offender.PubKey().Address().String():  {"NHB": "0", "ZNHB": evidenceZNHB(1_000).String()},
			f.reporter.PubKey().Address().String():  {"NHB": "0", "ZNHB": evidenceZNHB(30_000).String()},
			f.bystander.PubKey().Address().String(): {"NHB": "0", "ZNHB": evidenceZNHB(1_000).String()},
		},
		AdminWallet: f.admin.PubKey().Address().String(),
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal genesis: %v", err)
	}
	f.genesisPath = filepath.Join(t.TempDir(), "genesis.json")
	if err := os.WriteFile(f.genesisPath, data, 0o644); err != nil {
		t.Fatalf("write genesis: %v", err)
	}
	f.proposer = f.newNode()
	f.peer = f.newNode()
	return f
}

// newNode builds one more independent validator on the shared genesis: a node
// that restarted, newly joined, or is syncing the chain from scratch.
func (f *evidenceFlow) newNode() *Node {
	f.t.Helper()
	db := storage.NewMemDB()
	f.t.Cleanup(func() { db.Close() })
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		f.t.Fatalf("generate node key: %v", err)
	}
	node, err := NewNode(db, key, f.genesisPath, false, false)
	if err != nil {
		f.t.Fatalf("new node: %v", err)
	}
	fixed := f.fixedTime
	node.SetTimeSource(func() time.Time { return fixed })
	return node
}

// advance builds a block from txs on the proposer and commits it on both
// validators, failing if any of them rejects it.
func (f *evidenceFlow) advance(txs ...*types.Transaction) *types.Block {
	f.t.Helper()
	block, err := f.proposer.CreateBlock(txs)
	if err != nil {
		f.t.Fatalf("create block: %v", err)
	}
	if len(block.Transactions) != len(txs) {
		f.t.Fatalf("expected all %d transactions in the block, got %d", len(txs), len(block.Transactions))
	}
	commitBlockOnBoth(f.t, f.proposer, f.peer, block)
	f.chain = append(f.chain, block)
	return block
}

func (f *evidenceFlow) nonce(key *crypto.PrivateKey) uint64 {
	f.t.Helper()
	acct, err := f.proposer.GetAccount(key.PubKey().Address().Bytes())
	if err != nil {
		f.t.Fatalf("load account: %v", err)
	}
	return acct.Nonce
}

// stake self-stakes whole ZNHB from key in one block of its own.
func (f *evidenceFlow) stake(key *crypto.PrivateKey, whole int64) {
	f.t.Helper()
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeStake,
		Nonce:    f.nonce(key),
		Value:    evidenceZNHB(whole),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		f.t.Fatalf("sign stake: %v", err)
	}
	f.advance(tx)
}

// evidenceTx builds a signed TxTypeSubmitEvidence carrying a genuine
// equivocation proof against offender, reported (and signed) by reporter.
func (f *evidenceFlow) evidenceTx(offender, reporter *crypto.PrivateKey, height uint64) *types.Transaction {
	f.t.Helper()
	return f.evidenceTxAt(offender, reporter, height, f.nonce(reporter))
}

// evidenceTxAt is evidenceTx with an explicit nonce, for several reports from
// one reporter in the same block.
func (f *evidenceFlow) evidenceTxAt(offender, reporter *crypto.PrivateKey, height, nonce uint64) *types.Transaction {
	f.t.Helper()
	offenderKey := &evidenceTestKey{priv: offender.PrivateKey}
	reporterKey := &evidenceTestKey{priv: reporter.PrivateKey}
	ev, _ := buildGenuineEquivocationEvidence(f.t, offenderKey, reporterKey, height)
	return signedSubmitEvidenceTx(f.t, ev, reporterKey, nonce)
}

// evidenceAccount is the parts of an account the slash scenarios track.
type evidenceAccount struct {
	stake, locked, balance *big.Int
}

func (f *evidenceFlow) account(node *Node, key *crypto.PrivateKey) evidenceAccount {
	f.t.Helper()
	acct, err := node.GetAccount(key.PubKey().Address().Bytes())
	if err != nil {
		f.t.Fatalf("load account: %v", err)
	}
	return evidenceAccount{
		stake:   new(big.Int).Set(acct.Stake),
		locked:  new(big.Int).Set(acct.LockedZNHB),
		balance: new(big.Int).Set(acct.BalanceZNHB),
	}
}

// totalZNHB sums every ZNHB unit held anywhere in node's accounts: liquid
// balance, locked stake, and anything sitting in a pending unbond. A slash only
// moves ZNHB between accounts, so this must not change across one.
func totalZNHB(t *testing.T, node *Node) *big.Int {
	t.Helper()
	node.stateMu.RLock()
	defer node.stateMu.RUnlock()
	manager := nhbstate.NewManager(node.state.Trie)
	addrs, err := manager.AccountList()
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	total := big.NewInt(0)
	for _, addr := range addrs {
		acct, err := manager.GetAccount(addr[:])
		if err != nil {
			t.Fatalf("load account %x: %v", addr, err)
		}
		total.Add(total, acct.BalanceZNHB)
		total.Add(total, acct.LockedZNHB)
		for _, unbond := range acct.PendingUnbonds {
			total.Add(total, unbond.Amount)
		}
	}
	return total
}

// totalNHB sums every NHB unit held in node's accounts. Evidence moves no NHB
// and charges no fee, so this must not change across any evidence transaction.
func totalNHB(t *testing.T, node *Node) *big.Int {
	t.Helper()
	node.stateMu.RLock()
	defer node.stateMu.RUnlock()
	manager := nhbstate.NewManager(node.state.Trie)
	addrs, err := manager.AccountList()
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	total := big.NewInt(0)
	for _, addr := range addrs {
		acct, err := manager.GetAccount(addr[:])
		if err != nil {
			t.Fatalf("load account %x: %v", addr, err)
		}
		total.Add(total, acct.BalanceNHB)
	}
	return total
}

func nodeRewardPoolBalance(t *testing.T, node *Node) *big.Int {
	t.Helper()
	node.stateMu.RLock()
	defer node.stateMu.RUnlock()
	balance, err := nhbstate.NewManager(node.state.Trie).ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("load reward pool: %v", err)
	}
	return balance
}

func (f *evidenceFlow) checkInvariant(node *Node, when string) {
	f.t.Helper()
	node.stateMu.RLock()
	defer node.stateMu.RUnlock()
	if err := node.state.CheckZNHBSupplyInvariant(); err != nil {
		f.t.Fatalf("ZNHB supply invariant broken %s: %v", when, err)
	}
}

// bootstrapped runs the empty blocks that let the sale/reward pools bootstrap
// and returns the flow, so scenarios start from a state where the supply
// invariant is really being enforced.
func (f *evidenceFlow) bootstrapped() *evidenceFlow {
	f.t.Helper()
	f.advance()
	f.advance()
	return f
}

// TestEvidenceSlashSurvivesRepeatedProvisionalBuilds is the direct regression
// for a slash being defeated by the node's own trial builds. CreateBlock is
// run several times over the identical pending evidence -- as a proposer does
// whenever a round fails or restarts -- and only one of the resulting blocks
// is ever committed. Every build must produce the identical block, the block
// must validate on the node that built it, and the slash must land exactly
// once when it is committed. With the applied-penalty record kept in the
// node's own memory, the first build consumed the slash for every later one:
// later builds silently omitted it, the proposer's own ValidateBlock failed
// with a state root mismatch, and the offender kept their stake.
func TestEvidenceSlashSurvivesRepeatedProvisionalBuilds(t *testing.T) {
	f := newEvidenceFlow(t).bootstrapped()
	f.stake(f.offender, 500)
	f.stake(f.reporter, 10_000)
	before := f.account(f.proposer, f.offender)
	if before.locked.Cmp(evidenceZNHB(500)) != 0 {
		t.Fatalf("expected the offender to have 500 ZNHB bonded before the evidence, got %s", before.locked)
	}

	tx := f.evidenceTx(f.offender, f.reporter, 1)
	first, err := f.proposer.CreateBlock([]*types.Transaction{tx})
	if err != nil {
		t.Fatalf("create block 1: %v", err)
	}
	for i := 2; i <= 4; i++ {
		again, err := f.proposer.CreateBlock([]*types.Transaction{tx})
		if err != nil {
			t.Fatalf("create block %d: %v", i, err)
		}
		if string(again.Header.StateRoot) != string(first.Header.StateRoot) {
			t.Fatalf("build %d of the same pending evidence produced state root %x, want %x", i, again.Header.StateRoot, first.Header.StateRoot)
		}
	}
	if err := f.proposer.ValidateBlock(first); err != nil {
		t.Fatalf("the proposer's own validation of the block it just built failed: %v", err)
	}
	if err := f.peer.ValidateBlock(first); err != nil {
		t.Fatalf("peer validation failed: %v", err)
	}
	commitBlockOnBoth(t, f.proposer, f.peer, first)

	for name, node := range map[string]*Node{"proposer": f.proposer, "peer": f.peer} {
		after := f.account(node, f.offender)
		if after.stake.Sign() != 0 || after.locked.Sign() != 0 {
			t.Fatalf("%s: expected the offender's stake fully slashed, got stake=%s locked=%s", name, after.stake, after.locked)
		}
	}
}

// TestEvidenceSlashAgreesAcrossProposerPeerAndFreshReplay drives the whole
// flow the way a live network does -- proposer builds and self-validates, the
// peer validates and commits, both commit -- and then has a node that has never
// seen the chain (a restart from an empty data directory, a new validator, a
// syncing full node) replay every committed block. All three must land on the
// same state root and the same balances: what a block does cannot depend on
// which node executes it, or on how many times that node executed it before.
func TestEvidenceSlashAgreesAcrossProposerPeerAndFreshReplay(t *testing.T) {
	f := newEvidenceFlow(t).bootstrapped()
	f.stake(f.offender, 500)
	f.stake(f.reporter, 10_000)

	block, err := f.proposer.CreateBlock([]*types.Transaction{f.evidenceTx(f.offender, f.reporter, 1)})
	if err != nil {
		t.Fatalf("create evidence block: %v", err)
	}
	if err := f.proposer.ValidateBlock(block); err != nil {
		t.Fatalf("proposer self-validation: %v", err)
	}
	commitBlockOnBoth(t, f.proposer, f.peer, block)
	f.chain = append(f.chain, block)
	f.advance()
	f.advance()

	fresh := f.newNode()
	for i, b := range f.chain {
		if err := fresh.CommitBlock(b); err != nil {
			t.Fatalf("fresh node replay of block %d (height %d) failed: %v", i+1, b.Header.Height, err)
		}
	}
	proposerRoot := f.proposer.state.PendingRoot()
	if peerRoot := f.peer.state.PendingRoot(); peerRoot != proposerRoot {
		t.Fatalf("peer root %s differs from proposer root %s", peerRoot, proposerRoot)
	}
	if freshRoot := fresh.state.PendingRoot(); freshRoot != proposerRoot {
		t.Fatalf("fresh replay root %s differs from proposer root %s", freshRoot, proposerRoot)
	}
	after := f.account(fresh, f.offender)
	if after.stake.Sign() != 0 || after.locked.Sign() != 0 {
		t.Fatalf("expected the offender slashed on the replaying node too, got stake=%s locked=%s", after.stake, after.locked)
	}
}

// TestEvidenceRestakeAfterSlashIsNotSlashedAgain proves the applied-penalty
// record is chain state, not something a node remembers: once the offender has
// been slashed, funds they stake afterwards are not slashed by the same
// evidence on any node -- including one that replays the chain from scratch and
// so has no memory of the earlier block. With the record held in process memory
// the replaying node saw an unapplied penalty at the next block, slashed the
// fresh stake, and disagreed with every other validator.
func TestEvidenceRestakeAfterSlashIsNotSlashedAgain(t *testing.T) {
	f := newEvidenceFlow(t).bootstrapped()
	f.stake(f.offender, 400)
	f.stake(f.reporter, 10_000)
	f.advance(f.evidenceTx(f.offender, f.reporter, 1))
	if got := f.account(f.proposer, f.offender); got.locked.Sign() != 0 {
		t.Fatalf("expected the offender slashed, got locked=%s", got.locked)
	}

	f.stake(f.offender, 250)
	f.advance()

	fresh := f.newNode()
	for i, b := range f.chain {
		if err := fresh.CommitBlock(b); err != nil {
			t.Fatalf("fresh node replay of block %d (height %d) failed: %v", i+1, b.Header.Height, err)
		}
	}
	for name, node := range map[string]*Node{"proposer": f.proposer, "peer": f.peer, "fresh": fresh} {
		got := f.account(node, f.offender)
		if got.locked.Cmp(evidenceZNHB(250)) != 0 {
			t.Fatalf("%s: expected the re-staked 250 ZNHB untouched, got locked=%s", name, got.locked)
		}
	}
}

// TestEvidencePenaltyEventIsIdenticalOnEveryExecution: the penalty event a block
// emits (which reports the offender's weight after the penalty) is the same each
// time the block is executed, on any node -- it is derived from chain state, not
// from what a node has seen before.
func TestEvidencePenaltyEventIsIdenticalOnEveryExecution(t *testing.T) {
	f := newEvidenceFlow(t).bootstrapped()
	f.stake(f.offender, 500)
	f.stake(f.reporter, 10_000)
	tx := f.evidenceTx(f.offender, f.reporter, 1)
	height := f.proposer.GetHeight() + 1

	penaltyEvents := func(node *Node) []map[string]string {
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		scratch, err := node.state.Copy()
		if err != nil {
			t.Fatalf("copy state: %v", err)
		}
		scratch.BeginBlock(height, f.fixedTime)
		defer scratch.EndBlock()
		if err := scratch.ApplyTransaction(tx); err != nil {
			t.Fatalf("apply evidence: %v", err)
		}
		if err := node.processPendingEvidenceForState(scratch, height); err != nil {
			t.Fatalf("process evidence: %v", err)
		}
		var out []map[string]string
		for _, evt := range scratch.events {
			if evt.Type == "potso.penalty.applied" {
				out = append(out, evt.Attributes)
			}
		}
		return out
	}

	first := penaltyEvents(f.proposer)
	if len(first) != 1 {
		t.Fatalf("expected exactly one penalty event, got %d", len(first))
	}
	if first[0]["idempotent"] != "false" || first[0]["slashAmt"] != evidenceZNHB(500).String() {
		t.Fatalf("unexpected penalty event %v", first[0])
	}
	for name, node := range map[string]*Node{"proposer, again": f.proposer, "peer": f.peer} {
		again := penaltyEvents(node)
		if len(again) != 1 {
			t.Fatalf("%s: expected exactly one penalty event, got %d", name, len(again))
		}
		for key, want := range first[0] {
			if again[0][key] != want {
				t.Fatalf("%s: event attribute %q is %q, want %q", name, key, again[0][key], want)
			}
		}
	}
}

// TestEvidenceSlashKeepsZNHBSupplyInvariant covers every way the accounts
// involved in a slash can coincide: the offender, the reporter and the admin
// wallet (which is also where a slash's forfeited stake lands) may each be a
// separate account or the same one. For each combination the slash must only
// MOVE ZNHB -- the total across all accounts is unchanged -- and the block
// after it must still build and commit, because the supply invariant that the
// block lifecycle checks (sale pool + reward pool == the admin wallet's own
// tracked ZNHB) has to hold with the slash in place. Before the slash was
// mirrored into the reward pool, the forfeited stake landed on the admin wallet
// alone and the very next block failed to build.
func TestEvidenceSlashKeepsZNHBSupplyInvariant(t *testing.T) {
	cases := []struct {
		name string
		// who returns the offender and reporter for the flow's keys.
		who func(f *evidenceFlow) (offender, reporter *crypto.PrivateKey)
		// slashed is the stake, in whole ZNHB, the offender has bonded.
		offenderStake int64
		reporterStake int64
	}{
		{"all_distinct", func(f *evidenceFlow) (*crypto.PrivateKey, *crypto.PrivateKey) { return f.offender, f.reporter }, 500, 10_000},
		{"offender_is_admin", func(f *evidenceFlow) (*crypto.PrivateKey, *crypto.PrivateKey) { return f.admin, f.reporter }, 500, 10_000},
		{"reporter_is_admin", func(f *evidenceFlow) (*crypto.PrivateKey, *crypto.PrivateKey) { return f.offender, f.admin }, 500, 10_000},
		{"reporter_is_offender", func(f *evidenceFlow) (*crypto.PrivateKey, *crypto.PrivateKey) { return f.reporter, f.reporter }, 10_000, 0},
		{"reporter_and_offender_are_admin", func(f *evidenceFlow) (*crypto.PrivateKey, *crypto.PrivateKey) { return f.admin, f.admin }, 10_000, 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := newEvidenceFlow(t).bootstrapped()
			offender, reporter := tc.who(f)
			f.stake(offender, tc.offenderStake)
			if tc.reporterStake > 0 {
				f.stake(reporter, tc.reporterStake)
			}
			f.checkInvariant(f.proposer, "before the evidence")

			offenderBefore := f.account(f.proposer, offender)
			totalBefore := totalZNHB(t, f.proposer)
			nhbBefore := totalNHB(t, f.proposer)
			f.advance(f.evidenceTx(offender, reporter, 1))

			for name, node := range map[string]*Node{"proposer": f.proposer, "peer": f.peer} {
				if total := totalZNHB(t, node); total.Cmp(totalBefore) != 0 {
					t.Fatalf("%s: total ZNHB changed across the slash: before=%s after=%s", name, totalBefore, total)
				}
				if total := totalNHB(t, node); total.Cmp(nhbBefore) != 0 {
					t.Fatalf("%s: total NHB changed across the evidence: before=%s after=%s", name, nhbBefore, total)
				}
				f.checkInvariant(node, name+" after the slash")
			}
			offenderAfter := f.account(f.proposer, offender)
			if offenderAfter.locked.Sign() != 0 || offenderAfter.stake.Sign() != 0 {
				t.Fatalf("expected the offender's stake fully slashed, got stake=%s locked=%s (was stake=%s locked=%s)",
					offenderAfter.stake, offenderAfter.locked, offenderBefore.stake, offenderBefore.locked)
			}

			// The block after the slash must still build and commit.
			f.advance()
			f.checkInvariant(f.proposer, "one block after the slash")
		})
	}
}

// TestEvidenceSlashCreditsRewardPoolByExactlyTheForfeitedStake pins the
// accounting: when the offender is not the admin wallet, the forfeited stake
// lands on the admin wallet's balance and the same amount is added to the reward
// pool, no more and no less. It also covers an offender whose Stake exceeds their
// own locked ZNHB (a third party delegated to them): the slash is capped at what
// the offender actually had locked, and the pool follows that, not the larger
// requested figure.
func TestEvidenceSlashCreditsRewardPoolByExactlyTheForfeitedStake(t *testing.T) {
	f := newEvidenceFlow(t).bootstrapped()
	f.stake(f.offender, 300)
	f.stake(f.reporter, 10_000)

	delegate := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeStake,
		Nonce:    f.nonce(f.bystander),
		Value:    evidenceZNHB(200),
		Data:     mustEncodeStakePayload(t, stakePayload{Validator: f.offender.PubKey().Address().Bytes()}),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := delegate.Sign(f.bystander.PrivateKey); err != nil {
		t.Fatalf("sign delegation: %v", err)
	}
	f.advance(delegate)

	offenderBefore := f.account(f.proposer, f.offender)
	if offenderBefore.stake.Cmp(evidenceZNHB(500)) != 0 || offenderBefore.locked.Cmp(evidenceZNHB(300)) != 0 {
		t.Fatalf("expected stake=500 (300 own + 200 delegated) locked=300, got stake=%s locked=%s", offenderBefore.stake, offenderBefore.locked)
	}
	adminBefore := f.account(f.proposer, f.admin)
	poolBefore := nodeRewardPoolBalance(t, f.proposer)

	f.advance(f.evidenceTx(f.offender, f.reporter, 1))

	forfeited := evidenceZNHB(300)
	adminAfter := f.account(f.proposer, f.admin)
	if got := new(big.Int).Sub(adminAfter.balance, adminBefore.balance); got.Cmp(forfeited) != 0 {
		t.Fatalf("expected the admin wallet credited exactly %s, got %s", forfeited, got)
	}
	if got := new(big.Int).Sub(nodeRewardPoolBalance(t, f.proposer), poolBefore); got.Cmp(forfeited) != 0 {
		t.Fatalf("expected the reward pool to grow by exactly %s, got %s", forfeited, got)
	}
	f.checkInvariant(f.proposer, "after the slash")
}

// TestEvidenceWithoutAdminWalletCreditsTheSameTreasuryOnEveryNode covers a
// chain with no admin wallet, where a node's fallback treasury is its own
// validator address. The forfeited stake must not follow that: two validators
// with different keys have to credit the same account, or their state roots
// diverge on the block that carries the slash.
func TestEvidenceWithoutAdminWalletCreditsTheSameTreasuryOnEveryNode(t *testing.T) {
	genesisPath := writeSwapAdminGenesis(t)
	nodeA := buildSwapAdminTestNode(t, genesisPath)
	nodeB := buildSwapAdminTestNode(t, genesisPath)
	offenderKey := newEvidenceTestKey(t)
	reporterKey := newEvidenceTestKey(t)
	stake := big.NewInt(1_000)
	for _, node := range []*Node{nodeA, nodeB} {
		seedEvidenceOffenderStake(t, node, offenderKey.address(), stake)
		seedEvidenceReporterBond(t, node, reporterKey.address())
	}

	ev, _ := buildGenuineEquivocationEvidence(t, offenderKey, reporterKey, 50)
	tx := signedSubmitEvidenceTx(t, ev, reporterKey, 0)
	blockTime := time.Unix(1_700_000_000, 0).UTC()
	for _, node := range []*Node{nodeA, nodeB} {
		node.stateMu.Lock()
		node.state.BeginBlock(200, blockTime)
		err := node.state.ApplyTransaction(tx)
		if err == nil {
			err = node.processPendingEvidenceForState(node.state, 200)
		}
		node.state.EndBlock()
		node.stateMu.Unlock()
		if err != nil {
			t.Fatalf("apply and process evidence: %v", err)
		}
	}
	if rootA, rootB := nodeA.state.PendingRoot(), nodeB.state.PendingRoot(); rootA != rootB {
		t.Fatalf("two validators diverged after the same slash: %s vs %s", rootA, rootB)
	}
	for _, node := range []*Node{nodeA, nodeB} {
		own := node.SelfValidatorAddress()
		acct, err := node.GetAccount(own[:])
		if err != nil {
			t.Fatalf("load validator account: %v", err)
		}
		if acct.BalanceZNHB.Sign() != 0 {
			t.Fatalf("the forfeited stake was credited to the executing validator's own address (%s)", acct.BalanceZNHB)
		}
	}
}
