package core

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/genesis"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// TestPotsoStakeLockBlock_ProposerAndValidatorAgree drives the real
// CreateBlock/ValidateBlock/CommitBlock production code paths for a block
// containing a real, validly-signed TxTypePotsoStakeLock transaction --
// proving two independently constructed nodes derive the same state root,
// the same regression shape as the buyback/lending-refprice/governance/
// CreatePool tests. This is the direct regression test for the bug this fix
// closes: Node.PotsoStakeLock/Unbond/Withdraw used to mutate each
// validator's local trie directly, outside of consensus, and stamped
// lock/unbond/withdraw timestamps from real wall-clock time rather than the
// block timestamp -- either defect alone would have silently diverged state
// roots the moment more than one validator applied the same logical action.
func TestPotsoStakeLockBlock_ProposerAndValidatorAgree(t *testing.T) {
	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	ownerAddr := toAddress(ownerKey)
	ownerAddrStr := ownerKey.PubKey().Address().String()

	// The owner's ZNHB balance must come from genesis, not a direct
	// post-construction trie write -- a mutation applied identically to
	// both nodes still diverges pending state from committed state, and
	// gets silently wiped the first time a node validates a block it did
	// not itself propose (see governance_consensus_test.go's comment on
	// resetDriftUnlessSelfProposedLocked for the original diagnosis).
	genesisValidatorKeyA, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate genesis validator key A: %v", err)
	}
	genesisValidatorKeyB, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate genesis validator key B: %v", err)
	}
	spec := genesis.GenesisSpec{
		GenesisTime:  "2024-01-01T00:00:00Z",
		NativeTokens: []genesis.NativeTokenSpec{{Symbol: "NHB", Name: "NHBCoin", Decimals: 18}, {Symbol: "ZNHB", Name: "zNHBCoin", Decimals: 18}},
		Validators: []genesis.ValidatorSpec{
			{Address: genesisValidatorKeyA.PubKey().Address().String(), Power: 11440},
			{Address: genesisValidatorKeyB.PubKey().Address().String(), Power: 11336},
		},
		Alloc: map[string]map[string]string{
			ownerAddrStr: {"NHB": "0", "ZNHB": "1000"},
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

	build := func() *Node {
		db := storage.NewMemDB()
		t.Cleanup(func() { db.Close() })
		validatorKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate node validator key: %v", err)
		}
		node, err := NewNode(db, validatorKey, genesisPath, false, false)
		if err != nil {
			t.Fatalf("new node: %v", err)
		}
		return node
	}
	proposer := build()
	validator := build()

	lockData, err := rlp.EncodeToBytes(struct{ Amount *big.Int }{Amount: big.NewInt(600)})
	if err != nil {
		t.Fatalf("encode lock payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypePotsoStakeLock,
		Nonce:    0,
		Data:     lockData,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(ownerKey.PrivateKey); err != nil {
		t.Fatalf("sign lock tx: %v", err)
	}
	if err := proposer.AddTransaction(tx); err != nil {
		t.Fatalf("add lock tx: %v", err)
	}

	block, err := proposer.CreateBlock(append([]*types.Transaction(nil), proposer.mempool...))
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("expected the lock tx to survive into the proposed block, got %d txs", len(block.Transactions))
	}
	if err := validator.ValidateBlock(block); err != nil {
		t.Fatalf("validator rejected proposer's block: %v", err)
	}
	if err := proposer.CommitBlock(block); err != nil {
		t.Fatalf("proposer commit block: %v", err)
	}
	if err := validator.CommitBlock(block); err != nil {
		t.Fatalf("validator commit block: %v", err)
	}

	// ValidateBlock/CommitBlock above already fail loudly on any state-root
	// mismatch (see the other consensus regression tests added this
	// session) -- this final check confirms the lock itself actually
	// landed with the expected amount, identically, on both nodes.
	for _, node := range []*Node{proposer, validator} {
		info, err := node.PotsoStakeInfo(ownerAddr)
		if err != nil {
			t.Fatalf("stake info: %v", err)
		}
		if info.Bonded.String() != "600" {
			t.Fatalf("bonded = %s, want 600", info.Bonded.String())
		}
	}
}
