package core

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nhbchain/core/genesis"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// livenessPair is two independent validators started from one genesis file
// (each its own database), sharing an externally advanceable block clock. It is
// the real two-validator topology in miniature: the proposer builds, both
// validate, both commit, and their state roots must agree after every block.
//
// Accounts are funded through the genesis Alloc, never through WithState:
// direct-state funding on one node is wiped on the OTHER node when it validates
// a block it did not propose (resetDriftUnlessSelfProposedLocked), so an
// out-of-band write can never be shared between two nodes.
type livenessPair struct {
	t         *testing.T
	proposer  *Node
	validator *Node
	clock     *time.Time
}

type livenessPairOptions struct {
	funded     []*crypto.PrivateKey
	nhbEach    *big.Int
	znhbEach   *big.Int
	adminKey   *crypto.PrivateKey // when set, becomes the genesis AdminWallet holding the ZNHB supply
	epochShort bool
	// deterministicClock installs the shared block clock as the state
	// processor's own clock on both nodes, replacing the process wall clock.
	// Every re-execution then sees the same time and the proposer's wall-clock
	// read detector stands down. Escrow and trade timestamps come from the
	// block time either way, so tests need this only to control the clock that
	// other code reads.
	deterministicClock bool
	// sharedValidatorKey gives both nodes the same validator key, so headers
	// they each commit while being seeded (see commitStateAsEmptyBlock) are
	// byte-identical and their chains keep linking.
	sharedValidatorKey bool
}

func newLivenessPair(t *testing.T, opts livenessPairOptions) *livenessPair {
	t.Helper()
	t.Setenv("NHB_ENV", "dev")

	nhb := opts.nhbEach
	if nhb == nil {
		nhb = new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(1_000_000_000_000_000_000))
	}
	znhb := opts.znhbEach
	if znhb == nil {
		znhb = big.NewInt(0)
	}
	alloc := map[string]map[string]string{}
	for _, key := range opts.funded {
		alloc[key.PubKey().Address().String()] = map[string]string{"NHB": nhb.String(), "ZNHB": znhb.String()}
	}
	spec := genesis.GenesisSpec{
		GenesisTime:  "2024-01-01T00:00:00Z",
		NativeTokens: []genesis.NativeTokenSpec{{Symbol: "NHB", Name: "NHBCoin", Decimals: 18}, {Symbol: "ZNHB", Name: "zNHBCoin", Decimals: 18}},
		Validators: []genesis.ValidatorSpec{
			{Address: livenessKey(t).PubKey().Address().String(), Power: 11440},
			{Address: livenessKey(t).PubKey().Address().String(), Power: 11336},
		},
		Alloc: alloc,
	}
	if opts.adminKey != nil {
		admin := opts.adminKey.PubKey().Address().String()
		spec.Alloc[admin] = map[string]string{"NHB": "0", "ZNHB": znhbExpectedTotalSupplyWei.String()}
		spec.AdminWallet = admin
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal genesis: %v", err)
	}
	genesisPath := filepath.Join(t.TempDir(), "genesis.json")
	if err := os.WriteFile(genesisPath, data, 0o644); err != nil {
		t.Fatalf("write genesis: %v", err)
	}

	clock := time.Unix(1_800_000_000, 0).UTC()
	pair := &livenessPair{t: t, clock: &clock}
	sharedKey := livenessKey(t)
	build := func() *Node {
		db := storage.NewMemDB()
		t.Cleanup(func() { db.Close() })
		nodeKey := livenessKey(t)
		if opts.sharedValidatorKey {
			nodeKey = sharedKey
		}
		node, err := NewNode(db, nodeKey, genesisPath, false, false)
		if err != nil {
			t.Fatalf("new node: %v", err)
		}
		if opts.epochShort {
			if err := node.ConfigureEpochLengthForTests(2); err != nil {
				t.Fatalf("configure epoch length: %v", err)
			}
		}
		node.SetTimeSource(func() time.Time { return *pair.clock })
		if opts.deterministicClock {
			node.stateMu.Lock()
			node.state.nowFunc = func() time.Time { return *pair.clock }
			node.stateMu.Unlock()
		}
		return node
	}
	pair.proposer = build()
	pair.validator = build()
	return pair
}

// advance moves the shared block clock forward.
func (p *livenessPair) advance(d time.Duration) { *p.clock = p.clock.Add(d) }

// build has the proposer assemble a block from its current mempool offer.
func (p *livenessPair) build() *types.Block {
	p.t.Helper()
	block, err := p.proposer.CreateBlock(p.proposer.GetMempool())
	if err != nil {
		p.t.Fatalf("CreateBlock: %v", err)
	}
	return block
}

// commit has the proposer commit its own block and the validator re-derive and
// commit it, then requires identical state roots and heights.
func (p *livenessPair) commit(block *types.Block) {
	p.t.Helper()
	if err := p.proposer.CommitBlock(block); err != nil {
		p.t.Fatalf("proposer commit at height %d: %v", block.Header.Height, err)
	}
	if err := p.validator.ValidateBlock(block); err != nil {
		p.t.Fatalf("validator rejected the proposer's block at height %d: %v", block.Header.Height, err)
	}
	if err := p.validator.CommitBlock(block); err != nil {
		p.t.Fatalf("validator commit at height %d: %v", block.Header.Height, err)
	}
	p.requireAgreement()
}

// mine builds and commits one block on both nodes and returns it.
func (p *livenessPair) mine() *types.Block {
	p.t.Helper()
	block := p.build()
	p.commit(block)
	return block
}

func (p *livenessPair) requireAgreement() {
	p.t.Helper()
	if hp, hv := p.proposer.GetHeight(), p.validator.GetHeight(); hp != hv {
		p.t.Fatalf("heights diverged: proposer=%d validator=%d", hp, hv)
	}
	if rp, rv := p.proposer.state.CurrentRoot(), p.validator.state.CurrentRoot(); rp != rv {
		p.t.Fatalf("state roots diverged at height %d: proposer=%s validator=%s", p.proposer.GetHeight(), rp.Hex(), rv.Hex())
	}
}

// submit admits tx on the proposer, whose mempool feeds the blocks.
func (p *livenessPair) submit(tx *types.Transaction) {
	p.t.Helper()
	if err := p.proposer.AddTransaction(tx); err != nil {
		p.t.Fatalf("admit transaction: %v", err)
	}
}

// TestLivenessPairSmoke proves the harness itself: funded genesis accounts, a
// transfer mined on the proposer, re-derived identically by the validator.
func TestLivenessPairSmoke(t *testing.T) {
	sender := livenessKey(t)
	pair := newLivenessPair(t, livenessPairOptions{funded: []*crypto.PrivateKey{sender}})
	pair.submit(livenessTransfer(t, sender, 0))
	block := pair.mine()
	if len(block.Transactions) != 1 {
		t.Fatalf("expected the transfer in the block, got %d transactions", len(block.Transactions))
	}
	if pair.proposer.GetHeight() != 1 {
		t.Fatalf("expected height 1, got %d", pair.proposer.GetHeight())
	}
}
