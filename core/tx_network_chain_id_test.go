package core

import (
	"bytes"
	"errors"
	"math/big"
	"os"
	"regexp"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-R5T-29: a transaction signature binds the constant 0x4e4842 that every NHB network
// shares, not the network. From TxNetworkChainIDActivationHeight a transaction must carry
// the network's own chain id and the shared constant is refused.

const (
	networkAForTxTests uint64 = 18346390202490284624
	networkBForTxTests uint64 = 10698789873712925303
)

func bigU(v uint64) *big.Int { return new(big.Int).SetUint64(v) }

func TestTxChainIDAccepted_Table(t *testing.T) {
	legacy := types.NHBChainID()
	cases := []struct {
		name    string
		from    uint64
		network uint64
		chainID *big.Int
		height  uint64
		want    bool
	}{
		{"off: the legacy constant is accepted", 0, networkAForTxTests, legacy, 50, true},
		{"off: the network id is refused", 0, networkAForTxTests, bigU(networkAForTxTests), 50, false},
		{"off: another id is refused", 0, networkAForTxTests, bigU(7), 50, false},
		{"off: nil is refused", 0, networkAForTxTests, nil, 50, false},
		{"below the height the legacy constant is accepted", 10, networkAForTxTests, legacy, 9, true},
		{"below the height the network id is refused", 10, networkAForTxTests, bigU(networkAForTxTests), 9, false},
		{"at the height the legacy constant is refused", 10, networkAForTxTests, legacy, 10, false},
		{"at the height the network id is accepted", 10, networkAForTxTests, bigU(networkAForTxTests), 10, true},
		{"above the height the network id is accepted", 10, networkAForTxTests, bigU(networkAForTxTests), 99, true},
		{"above the height another network's id is refused", 10, networkAForTxTests, bigU(networkBForTxTests), 99, false},
		{"above the height nil is refused", 10, networkAForTxTests, nil, 99, false},
		{"above the height an id that only wraps to the network id modulo 2^64 is refused", 10, networkAForTxTests, new(big.Int).Add(bigU(networkAForTxTests), new(big.Int).Lsh(big.NewInt(1), 64)), 99, false},
		{"an unset network id accepts nothing from the height on", 10, 0, legacy, 10, false},
		{"an unset network id accepts zero from the height on too", 10, 0, big.NewInt(0), 10, false},
		{"an unset network id leaves the legacy constant below the height", 10, 0, legacy, 9, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := newStakingStateProcessor(t)
			sp.SetSwapVoucherChainID(tc.network)
			sp.SetTxNetworkChainIDActivationHeight(tc.from)
			if got := sp.txChainIDAccepted(tc.chainID, tc.height); got != tc.want {
				t.Fatalf("txChainIDAccepted(%v, %d) = %v, want %v", tc.chainID, tc.height, got, tc.want)
			}
		})
	}
}

func TestTxChainIDAccepted_NilProcessorKeepsTheLegacyRule(t *testing.T) {
	var sp *StateProcessor
	if !sp.txChainIDAccepted(types.NHBChainID(), 1) || sp.txChainIDAccepted(bigU(networkAForTxTests), 1) {
		t.Fatalf("a nil processor must keep the legacy rule")
	}
}

func TestTxChainIDAt_NamesTheIDAnAcceptedTransactionMustCarry(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetSwapVoucherChainID(networkAForTxTests)
	if sp.txChainIDAt(100).Cmp(types.NHBChainID()) != 0 {
		t.Fatalf("with the setting off a transaction must carry the legacy constant")
	}
	sp.SetTxNetworkChainIDActivationHeight(10)
	if sp.txChainIDAt(9).Cmp(types.NHBChainID()) != 0 {
		t.Fatalf("below the height a transaction must carry the legacy constant")
	}
	if sp.txChainIDAt(10).Cmp(bigU(networkAForTxTests)) != 0 {
		t.Fatalf("at the height a transaction must carry the network id")
	}
	// Whatever it names, the executor accepts.
	for _, h := range []uint64{1, 9, 10, 11} {
		if !sp.txChainIDAccepted(sp.txChainIDAt(h), h) {
			t.Fatalf("txChainIDAt(%d) names an id the executor refuses", h)
		}
	}
	sp.SetSwapVoucherChainID(0)
	if sp.txChainIDAt(10).Cmp(types.NHBChainID()) != 0 {
		t.Fatalf("with no network id set the helper falls back to the legacy constant")
	}
}

// ---- through the real executor ------------------------------------------------------

type txChainFixture struct {
	sp   *StateProcessor
	key  *crypto.PrivateKey
	addr [20]byte
}

func newTxChainFixture(t *testing.T, network, activation uint64) *txChainFixture {
	t.Helper()
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(5_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}
	sp.SetSwapVoucherChainID(network)
	sp.SetTxNetworkChainIDActivationHeight(activation)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var addr [20]byte
	copy(addr[:], key.PubKey().Address().Bytes())
	writeAccount(t, sp, addr, &types.Account{BalanceZNHB: big.NewInt(30_000), Stake: big.NewInt(8_000)})
	return &txChainFixture{sp: sp, key: key, addr: addr}
}

// A zero-value register: a transaction that exercises the full executor path and needs
// nothing but a key.
func (f *txChainFixture) register(t *testing.T, chainID *big.Int, nonce uint64) *types.Transaction {
	t.Helper()
	tx := &types.Transaction{
		ChainID:  chainID,
		Type:     types.TxTypeStake,
		Nonce:    nonce,
		To:       make([]byte, 20),
		Value:    big.NewInt(0),
		Data:     mustEncodeStakePayload(t, stakePayload{RegisterValidator: true}),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(f.key.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tx
}

func (f *txChainFixture) apply(height uint64, tx *types.Transaction) error {
	f.sp.BeginBlock(height, time.Unix(int64(1_700_000_000+height), 0).UTC())
	defer f.sp.EndBlock()
	return f.sp.ApplyTransaction(tx)
}

func TestTxNetworkChainID_OffAcceptsOnlyTheLegacyConstant(t *testing.T) {
	f := newTxChainFixture(t, networkAForTxTests, 0)
	if err := f.apply(50, f.register(t, bigU(networkAForTxTests), 0)); !errors.Is(err, ErrInvalidChainID) {
		t.Fatalf("with the setting off the network id must be refused, got %v", err)
	}
	if err := f.apply(50, f.register(t, types.NHBChainID(), 0)); err != nil {
		t.Fatalf("with the setting off the legacy constant must be accepted: %v", err)
	}
}

func TestTxNetworkChainID_TheBoundaryIsTheConfiguredHeight(t *testing.T) {
	f := newTxChainFixture(t, networkAForTxTests, 100)
	if err := f.apply(99, f.register(t, types.NHBChainID(), 0)); err != nil {
		t.Fatalf("legacy constant at 99: %v", err)
	}
	if err := f.apply(99, f.register(t, bigU(networkAForTxTests), 0)); !errors.Is(err, ErrInvalidChainID) {
		t.Fatalf("network id at 99 must be refused, got %v", err)
	}
	if err := f.apply(100, f.register(t, types.NHBChainID(), 0)); !errors.Is(err, ErrInvalidChainID) {
		t.Fatalf("legacy constant at 100 must be refused, got %v", err)
	}
	if err := f.apply(100, f.register(t, bigU(networkAForTxTests), 0)); err != nil {
		t.Fatalf("network id at 100: %v", err)
	}
}

// The attack: a transaction signed for network A, replayed on network B. The id is
// inside the signed hash, so rewriting it for B changes the recovered signer, and
// leaving it as A is refused by B.
func TestTxNetworkChainID_ATransactionSignedForOneNetworkIsUselessOnAnother(t *testing.T) {
	a := newTxChainFixture(t, networkAForTxTests, 10)
	b := newTxChainFixture(t, networkBForTxTests, 10)
	// The same key and nonce exist on both networks.
	writeAccount(t, b.sp, a.addr, &types.Account{BalanceZNHB: big.NewInt(30_000), Stake: big.NewInt(8_000)})
	b.key, b.addr = a.key, a.addr

	signedForA := a.register(t, bigU(networkAForTxTests), 0)
	if err := a.apply(10, signedForA); err != nil {
		t.Fatalf("network A: %v", err)
	}
	// Verbatim on B: refused.
	if err := b.apply(10, signedForA); !errors.Is(err, ErrInvalidChainID) {
		t.Fatalf("a verbatim replay on network B must be refused, got %v", err)
	}
	// Retargeted to B's id: the signature no longer recovers the same sender.
	retargeted := &types.Transaction{
		ChainID: bigU(networkBForTxTests), Type: signedForA.Type, Nonce: signedForA.Nonce, To: signedForA.To,
		Value: signedForA.Value, Data: signedForA.Data, GasLimit: signedForA.GasLimit, GasPrice: signedForA.GasPrice,
		R: signedForA.R, S: signedForA.S, V: signedForA.V,
	}
	from, err := retargeted.From()
	if err == nil && bytes.Equal(from, a.addr[:]) {
		t.Fatalf("rewriting the chain id left the signer unchanged: the id is not inside the signed hash")
	}
}

func TestTxNetworkChainID_AnUnsetNetworkIDRefusesEverythingFromTheHeight(t *testing.T) {
	f := newTxChainFixture(t, 0, 10)
	if err := f.apply(10, f.register(t, types.NHBChainID(), 0)); !errors.Is(err, ErrInvalidChainID) {
		t.Fatalf("legacy constant with no network id set must be refused from the height, got %v", err)
	}
}

func TestTxNetworkChainID_TheSettingSurvivesCopy(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetTxNetworkChainIDActivationHeight(11)
	copied, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copied.txNetworkChainIDFromHeight != 11 {
		t.Fatalf("Copy lost the setting: %d", copied.txNetworkChainIDFromHeight)
	}
}

// ---- through a real node ------------------------------------------------------------

func TestNodeTxNetworkChainID_SetterReachesTheProcessorAndSurvivesARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetTxNetworkChainIDActivationHeight(5)
	node.stateMu.Lock()
	got := node.state.txNetworkChainIDFromHeight
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuilt := node.state.txNetworkChainIDFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got != 5 || rebuilt != 5 {
		t.Fatalf("setter left %d, rebuild left %d, want 5 and 5", got, rebuilt)
	}
}

func nodeTxChainFixture(t *testing.T, node *Node) *txChainFixture {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var addr [20]byte
	copy(addr[:], key.PubKey().Address().Bytes())
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	writeAccount(t, node.state, addr, &types.Account{BalanceZNHB: big.NewInt(30_000), Stake: big.NewInt(8_000)})
	return &txChainFixture{sp: node.state, key: key, addr: addr}
}

func TestNodeTxNetworkChainID_AdmissionAndBlocksFollowTheHeight(t *testing.T) {
	node := newTestNode(t)
	network := node.ChainID()
	if network == 0 {
		t.Skip("the test node has no chain id")
	}
	f := nodeTxChainFixture(t, node)
	node.SetTxNetworkChainIDActivationHeight(4)
	legacy := types.NHBChainID()
	// A zero-value register does not advance the nonce while ValidatorFlagNonceActivationHeight
	// is off, so every transaction below is signed at nonce 0.

	// Blocks 1..3: the legacy constant works at admission and in blocks; the network id does not.
	if err := node.AddTransaction(f.register(t, bigU(network), 0)); err == nil {
		t.Fatalf("below the height the network id must be refused at admission")
	}
	commitEmptyBlock(t, node) // 1
	b2 := commitBlockWith(t, node, f.register(t, legacy, 0))
	if len(b2.Transactions) != 1 {
		t.Fatalf("block 2 should take a legacy-id transaction")
	}
	commitEmptyBlock(t, node) // 3

	// Next block is 4: the legacy constant is refused at admission and left out of the block.
	if err := node.AddTransaction(f.register(t, legacy, 0)); err == nil {
		t.Fatalf("at the height the legacy constant must be refused at admission")
	}
	b4 := commitBlockWith(t, node, f.register(t, legacy, 0))
	if b4.Header.Height != 4 || len(b4.Transactions) != 0 {
		t.Fatalf("block %d took %d legacy-id transactions, want none", b4.Header.Height, len(b4.Transactions))
	}
	// The network id is accepted at admission and in the next block.
	if err := node.AddTransaction(f.register(t, bigU(network), 0)); err != nil {
		t.Fatalf("at the height the network id must be accepted at admission: %v", err)
	}
	b5 := commitBlockWith(t, node, f.register(t, bigU(network), 0))
	if len(b5.Transactions) != 1 {
		t.Fatalf("block %d should take a network-id transaction", b5.Header.Height)
	}
}

func TestNodeTxNetworkChainID_TheNodeNamesTheIDAnAcceptedTransactionMustCarry(t *testing.T) {
	node := newTestNode(t)
	network := node.ChainID()
	if network == 0 {
		t.Skip("the test node has no chain id")
	}
	node.SetTxNetworkChainIDActivationHeight(3)
	if node.TxChainIDForNextBlock().Cmp(types.NHBChainID()) != 0 || node.txChainIDForNextBlock().Cmp(types.NHBChainID()) != 0 {
		t.Fatalf("next block 1: want the legacy constant")
	}
	if !node.TxChainIDAcceptedForNextBlock(types.NHBChainID()) || node.TxChainIDAcceptedForNextBlock(bigU(network)) {
		t.Fatalf("next block 1: legacy accepted, network id refused")
	}
	commitEmptyBlock(t, node) // 1
	commitEmptyBlock(t, node) // 2
	if node.TxChainIDForNextBlock().Cmp(bigU(network)) != 0 {
		t.Fatalf("next block 3: want the network id, got %v", node.TxChainIDForNextBlock())
	}
	if node.TxChainIDAcceptedForNextBlock(types.NHBChainID()) || !node.TxChainIDAcceptedForNextBlock(bigU(network)) {
		t.Fatalf("next block 3: network id accepted, legacy refused")
	}
}

// Every transaction the node builds itself takes its id from the helper; a literal
// types.NHBChainID() in core/node.go would keep signing with the shared constant.
func TestNodeBuiltTransactionsNeverUseTheLiteralLegacyChainID(t *testing.T) {
	src, err := os.ReadFile("node.go")
	if err != nil {
		t.Fatalf("read node.go: %v", err)
	}
	if loc := regexp.MustCompile(`types\.NHBChainID\(\)`).FindIndex(src); loc != nil {
		line := 1 + bytes.Count(src[:loc[0]], []byte("\n"))
		t.Fatalf("core/node.go:%d builds a transaction with the literal legacy chain id; use n.txChainIDForNextBlock()", line)
	}
}

// The transactions the node builds itself carry the id the node will accept: a mint voucher
// submitted through MintWithSignature is admitted (the node simulates it with the same chain
// id rule) both before the height and from it. Were the node to sign with the shared
// constant from the height on, admission would refuse its own transaction.
func TestNodeTxNetworkChainID_ANodeBuiltTransactionCarriesTheIDTheNodeAccepts(t *testing.T) {
	f := newMintChainFixture(t)
	if f.node.ChainID() == 0 {
		t.Skip("the test node has no chain id")
	}
	f.node.SetTxNetworkChainIDActivationHeight(3)

	before, sigBefore := f.signed(t, MintChainID)
	if _, err := f.node.MintWithSignature(before, sigBefore); err != nil {
		t.Fatalf("before the height the node's own mint transaction was refused: %v", err)
	}
	commitEmptyBlock(t, f.node) // 1
	commitEmptyBlock(t, f.node) // 2: the next block is 3, the height itself
	after, sigAfter := f.signed(t, MintChainID)
	if _, err := f.node.MintWithSignature(after, sigAfter); err != nil {
		t.Fatalf("from the height the node's own mint transaction was refused: %v", err)
	}
	// And what it queued carries the network id.
	f.node.stateMu.Lock()
	queued := append([]*types.Transaction(nil), f.node.mempool...)
	f.node.stateMu.Unlock()
	if len(queued) == 0 {
		t.Fatalf("nothing queued")
	}
	if got := queued[len(queued)-1].ChainID; got == nil || got.Cmp(bigU(f.node.ChainID())) != 0 {
		t.Fatalf("the queued transaction carries chain id %v, want the network's %d", got, f.node.ChainID())
	}
}

func TestNodeTxChainID_ANilNodeKeepsTheLegacyRule(t *testing.T) {
	var n *Node
	if n.TxChainIDForNextBlock().Cmp(types.NHBChainID()) != 0 {
		t.Fatalf("a nil node must name the legacy constant")
	}
	if !n.TxChainIDAcceptedForNextBlock(types.NHBChainID()) || n.TxChainIDAcceptedForNextBlock(bigU(networkAForTxTests)) {
		t.Fatalf("a nil node must keep the legacy rule")
	}
}
