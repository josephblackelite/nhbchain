package core

import (
	"errors"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-R5T-58: mint vouchers are bound to the retired Phase E chain id, not to the chain
// that is running. Two chain rules, both off by default: accept the running id too
// (MintRunningChainIDActivationHeight) and retire the Phase E id
// (MintPhaseEChainIDRetireHeight).

const liveChainIDForMintTests uint64 = 18346390202490284624

func TestMintVoucherChainIDAccepted_Table(t *testing.T) {
	const running = liveChainIDForMintTests
	cases := []struct {
		name         string
		from, retire uint64
		runningChain uint64
		chainID      uint64
		height       uint64
		wantAccepted bool
	}{
		{"off: the Phase E id is accepted", 0, 0, running, MintChainID, 50, true},
		{"off: the running id is refused", 0, 0, running, running, 50, false},
		{"off: any other id is refused", 0, 0, running, 12345, 50, false},
		{"running id below the height is refused", 10, 0, running, running, 9, false},
		{"running id at the height is accepted", 10, 0, running, running, 10, true},
		{"running id above the height is accepted", 10, 0, running, running, 11, true},
		{"Phase E id still accepted after the running id switches on", 10, 0, running, MintChainID, 11, true},
		{"another id is refused after the running id switches on", 10, 0, running, 12345, 11, false},
		{"Phase E id below the retirement is accepted", 10, 20, running, MintChainID, 19, true},
		{"Phase E id at the retirement is refused", 10, 20, running, MintChainID, 20, false},
		{"Phase E id above the retirement is refused", 10, 20, running, MintChainID, 21, false},
		{"running id at the retirement is accepted", 10, 20, running, running, 20, true},
		{"retirement alone switches the running id on at its height", 0, 20, running, running, 20, true},
		{"retirement alone leaves the running id refused below its height", 0, 20, running, running, 19, false},
		{"an unset running chain id is never accepted (running id zero)", 10, 0, 0, 0, 11, false},
		{"an unset running chain id leaves the Phase E id alone", 10, 0, 0, MintChainID, 11, true},
		{"an unset running chain id after the retirement refuses everything", 10, 20, 0, MintChainID, 21, false},
		{"a chain whose running id is the Phase E id keeps accepting it after the retirement", 10, 20, MintChainID, MintChainID, 21, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := newStakingStateProcessor(t)
			sp.SetSwapVoucherChainID(tc.runningChain)
			sp.SetMintRunningChainIDActivationHeight(tc.from)
			sp.SetMintPhaseEChainIDRetireHeight(tc.retire)
			if got := sp.mintVoucherChainIDAccepted(tc.chainID, tc.height); got != tc.wantAccepted {
				t.Fatalf("mintVoucherChainIDAccepted(%d, %d) = %v, want %v", tc.chainID, tc.height, got, tc.wantAccepted)
			}
		})
	}
}

func TestMintVoucherChainIDAccepted_NilProcessorKeepsTheOldRule(t *testing.T) {
	var sp *StateProcessor
	if !sp.mintVoucherChainIDAccepted(MintChainID, 1) {
		t.Fatalf("a nil processor must accept the Phase E id")
	}
	if sp.mintVoucherChainIDAccepted(liveChainIDForMintTests, 1) {
		t.Fatalf("a nil processor must refuse any other id")
	}
}

type mintChainFixture struct {
	node      *Node
	minter    *crypto.PrivateKey
	recipient *crypto.PrivateKey
	seq       int
}

func newMintChainFixture(t *testing.T) *mintChainFixture {
	t.Helper()
	node := newTestNode(t)
	minter, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	assignRole(t, node, "MINTER_NHB", toAddress(minter))
	recipient, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	return &mintChainFixture{node: node, minter: minter, recipient: recipient}
}

func (f *mintChainFixture) voucher(chainID uint64) (MintVoucher, []byte) {
	f.seq++
	v := MintVoucher{
		InvoiceID: "inv-chain-" + string(rune('a'+f.seq)),
		Recipient: f.recipient.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "10",
		ChainID:   chainID,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	return v, nil
}

func (f *mintChainFixture) signed(t *testing.T, chainID uint64) (*MintVoucher, []byte) {
	v, _ := f.voucher(chainID)
	return &v, signVoucher(t, f.minter, v)
}

func (f *mintChainFixture) supply(t *testing.T) *big.Int {
	t.Helper()
	f.node.stateMu.Lock()
	defer f.node.stateMu.Unlock()
	got, err := nhbstate.NewManager(f.node.state.Trie).TokenSupply("NHB")
	if err != nil {
		t.Fatalf("supply: %v", err)
	}
	return got
}

// A mint tx built by hand and put straight into a block, bypassing admission, so the
// block executor's own check is what is tested.
func (f *mintChainFixture) mintTx(t *testing.T, chainID uint64) *types.Transaction {
	t.Helper()
	v, sig := f.signed(t, chainID)
	payload, err := encodeMintTransaction(v, sig)
	if err != nil {
		t.Fatalf("encode mint: %v", err)
	}
	return &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeMint, Data: payload, GasLimit: 0, GasPrice: big.NewInt(0), Value: big.NewInt(0)}
}

func TestNodeMintRunningChainID_OffRefusesTheRunningIDAtAdmission(t *testing.T) {
	f := newMintChainFixture(t)
	v, sig := f.signed(t, f.node.ChainID())
	if _, err := f.node.MintWithSignature(v, sig); !errors.Is(err, ErrMintInvalidChainID) {
		t.Fatalf("with the setting off a voucher naming the running chain must be refused, got %v", err)
	}
	v2, sig2 := f.signed(t, MintChainID)
	if _, err := f.node.MintWithSignature(v2, sig2); err != nil {
		t.Fatalf("the Phase E id must still be accepted with the settings off: %v", err)
	}
}

func TestNodeMintRunningChainID_AcceptedFromTheHeightAtAdmissionAndInBlocks(t *testing.T) {
	f := newMintChainFixture(t)
	if f.node.ChainID() == 0 || f.node.ChainID() == MintChainID {
		t.Skipf("the test node's chain id (%d) cannot tell the two ids apart", f.node.ChainID())
	}
	f.node.SetMintRunningChainIDActivationHeight(3)

	// Next block is 1: still off.
	v, sig := f.signed(t, f.node.ChainID())
	if _, err := f.node.MintWithSignature(v, sig); !errors.Is(err, ErrMintInvalidChainID) {
		t.Fatalf("below the height the running id must be refused at admission, got %v", err)
	}
	commitEmptyBlock(t, f.node) // 1
	// A block built below the height leaves a running-id mint out.
	b2 := commitBlockWith(t, f.node, f.mintTx(t, f.node.ChainID()))
	if b2.Header.Height != 2 || len(b2.Transactions) != 0 {
		t.Fatalf("block %d below the height took %d running-id mints, want none", b2.Header.Height, len(b2.Transactions))
	}

	// Next block is 3: on.
	before := f.supply(t)
	v3, sig3 := f.signed(t, f.node.ChainID())
	if _, err := f.node.MintWithSignature(v3, sig3); err != nil {
		t.Fatalf("at the height the running id must be accepted at admission: %v", err)
	}
	b3 := commitBlockWith(t, f.node, append([]*types.Transaction(nil), f.node.mempool...)...)
	if b3.Header.Height != 3 || len(b3.Transactions) != 1 {
		t.Fatalf("block %d took %d transactions, want the one running-id mint", b3.Header.Height, len(b3.Transactions))
	}
	if got, want := f.supply(t), new(big.Int).Add(before, big.NewInt(10)); got.Cmp(want) != 0 {
		t.Fatalf("supply after the mint: %s, want %s", got, want)
	}

	// The Phase E id is still valid.
	b4 := commitBlockWith(t, f.node, f.mintTx(t, MintChainID))
	if len(b4.Transactions) != 1 {
		t.Fatalf("block %d: a Phase E mint was left out after the running id switched on", b4.Header.Height)
	}
}

func TestNodeMintRunningChainID_TheRetirementRefusesThePhaseEIDFromItsHeight(t *testing.T) {
	f := newMintChainFixture(t)
	if f.node.ChainID() == 0 || f.node.ChainID() == MintChainID {
		t.Skipf("the test node's chain id (%d) cannot tell the two ids apart", f.node.ChainID())
	}
	f.node.SetMintPhaseEChainIDRetireHeight(3)

	commitEmptyBlock(t, f.node) // 1
	b2 := commitBlockWith(t, f.node, f.mintTx(t, MintChainID))
	if len(b2.Transactions) != 1 {
		t.Fatalf("block 2 is below the retirement; the Phase E mint should be in it")
	}
	// Next block is 3: the Phase E id is refused at admission and in the block.
	v, sig := f.signed(t, MintChainID)
	if _, err := f.node.MintWithSignature(v, sig); !errors.Is(err, ErrMintInvalidChainID) {
		t.Fatalf("at the retirement the Phase E id must be refused at admission, got %v", err)
	}
	b3 := commitBlockWith(t, f.node, f.mintTx(t, MintChainID))
	if b3.Header.Height != 3 || len(b3.Transactions) != 0 {
		t.Fatalf("block %d took %d Phase E mints, want none", b3.Header.Height, len(b3.Transactions))
	}
	// The running id is accepted from the same height.
	b4 := commitBlockWith(t, f.node, f.mintTx(t, f.node.ChainID()))
	if len(b4.Transactions) != 1 {
		t.Fatalf("block %d left out a running-id mint after the retirement", b4.Header.Height)
	}
}

// The Phase E sentinel of the two supply repairs is a name of its own: with the voucher
// rules switched on at height 1, the repairs still do nothing on the relaunch chain.
func TestMintRunningChainID_TheSupplyRepairsStayNoOpsOnTheRelaunchChain(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetSwapVoucherChainID(liveChainIDForMintTests)
	sp.SetMintRunningChainIDActivationHeight(1)
	sp.SetMintPhaseEChainIDRetireHeight(1)
	sp.BeginBlock(5, time.Unix(1_700_000_000, 0).UTC())
	defer sp.EndBlock()
	if err := sp.SeedGenesisNHBSupplyOnce(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := sp.ReconcileNHBMintSupplyDriftOnce(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got, err := nhbstate.NewManager(sp.Trie).TokenSupply("NHB")
	if err != nil {
		t.Fatalf("supply: %v", err)
	}
	if got.Sign() != 0 {
		t.Fatalf("the Phase E repairs changed the counter on the relaunch chain: %s", got)
	}
	if phaseEChainID != MintChainID {
		t.Fatalf("phaseEChainID %d and MintChainID %d must be the same value until the voucher rule moves", phaseEChainID, MintChainID)
	}
}

func TestMintRunningChainID_TheSettingsSurviveCopy(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetMintRunningChainIDActivationHeight(11)
	sp.SetMintPhaseEChainIDRetireHeight(22)
	copied, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copied.mintRunningChainIDFromHeight != 11 || copied.mintPhaseERetireFromHeight != 22 {
		t.Fatalf("Copy lost a setting: %d %d", copied.mintRunningChainIDFromHeight, copied.mintPhaseERetireFromHeight)
	}
}

func TestNodeMintRunningChainID_SettersReachTheProcessorAndSurviveARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetMintRunningChainIDActivationHeight(5)
	node.SetMintPhaseEChainIDRetireHeight(9)
	node.stateMu.Lock()
	a, b := node.state.mintRunningChainIDFromHeight, node.state.mintPhaseERetireFromHeight
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	ra, rb := node.state.mintRunningChainIDFromHeight, node.state.mintPhaseERetireFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if a != 5 || b != 9 {
		t.Fatalf("setters left the processor at %d and %d, want 5 and 9", a, b)
	}
	if ra != 5 || rb != 9 {
		t.Fatalf("a rebuilt processor lost the settings: %d and %d", ra, rb)
	}
}
