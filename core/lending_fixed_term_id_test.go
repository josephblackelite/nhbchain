package core

import (
	"bytes"
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

const lendingIDTestActivation = 100

// lendingIDScoped is the ID a fixed-term record is stored under from the
// activation height, derived here without the code under test.
func lendingIDScoped(kind string, sender, txHash []byte) [32]byte {
	var id [32]byte
	copy(id[:], ethcrypto.Keccak256([]byte("nhb/lending/fixed-term/"+kind), sender, txHash))
	return id
}

// lendingTwoBorrowers seeds a pool with NHB liquidity and two borrowers who each
// hold ample ZNHB collateral in the pool, all through the transaction pipeline, and
// returns the processor with both borrowers ready to originate a loan with nonce 1.
func lendingTwoBorrowers(t *testing.T, gate uint64) (sp *StateProcessor, a, b *crypto.PrivateKey) {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	tr, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err = NewStateProcessor(tr)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	sp.SetLendingRiskParameters(lending.RiskParameters{MaxLTV: 7_500, LiquidationThreshold: 8_000})
	sp.SetLendingAccrualConfig(0, 0, lending.DefaultInterestModel)
	if gate != 0 {
		sp.SetLendingFixedTermIDActivationHeight(gate)
	}

	supplier, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	a, err = crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err = crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	collateral := mustBigInt(t, "50000000000000000000000")
	if err := sp.setAccount(supplier.PubKey().Address().Bytes(), &types.Account{BalanceNHB: mustBigInt(t, "10000000000000000000000"), BalanceZNHB: big.NewInt(0)}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []*crypto.PrivateKey{a, b} {
		if err := sp.setAccount(k.PubKey().Address().Bytes(), &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: new(big.Int).Set(collateral)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit seeded state: %v", err)
	}
	setup := []*types.Transaction{
		mustSignLendingTx(t, supplier, types.TxTypeLendingSupplyNHB, 0, mustBigInt(t, "3000000000000000000000"), lendingNativePayload{PoolID: "default"}),
		mustSignLendingTx(t, a, types.TxTypeLendingDepositZNHB, 0, collateral, lendingNativePayload{PoolID: "default"}),
		mustSignLendingTx(t, b, types.TxTypeLendingDepositZNHB, 0, collateral, lendingNativePayload{PoolID: "default"}),
	}
	for i, tx := range setup {
		height := uint64(i + 1)
		sp.BeginBlock(height, time.Unix(int64(height), 0).UTC())
		if err := sp.ApplyTransaction(tx); err != nil {
			sp.EndBlock()
			t.Fatalf("setup tx %d: %v", i, err)
		}
		sp.EndBlock()
		if _, err := sp.Commit(height); err != nil {
			t.Fatalf("commit setup tx %d: %v", i, err)
		}
	}
	return sp, a, b
}

func borrowAt(t *testing.T, sp *StateProcessor, key *crypto.PrivateKey, tx *types.Transaction, height uint64) {
	t.Helper()
	sp.BeginBlock(height, time.Unix(int64(height), 0).UTC())
	defer sp.EndBlock()
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("borrow by %x at height %d: %v", key.PubKey().Address().Bytes()[:4], height, err)
	}
}

// Two accounts sign field-identical fixed-term borrows (same nonce, amount,
// tenure and gas fields), so the transactions share one hash. Without the gate the
// second origination overwrites the first loan's record while both principals were
// paid out and both pool totals stand: the first borrower's obligation is erased
// and they keep the cash. From the activation height each loan keeps its own record.
func TestFixedTermLoanIDsCollideWithoutTheGateAndStayApartWithIt(t *testing.T) {
	principal := mustBigInt(t, "100000000000000000000")
	cases := []struct {
		name     string
		gate     uint64
		height   uint64
		separate bool
	}{
		{"DefaultOffKeepsTheTransactionHashAsTheID", 0, 10_000_000, false},
		{"BelowActivationKeepsTheTransactionHashAsTheID", lendingIDTestActivation, lendingIDTestActivation - 1, false},
		{"AtActivationScopesTheIDToTheBorrower", lendingIDTestActivation, lendingIDTestActivation, true},
		{"AboveActivationScopesTheIDToTheBorrower", lendingIDTestActivation, lendingIDTestActivation + 40, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp, a, b := lendingTwoBorrowers(t, tc.gate)
			txA := mustSignLendingFixedTermBorrowTx(t, a, 1, principal, 30)
			txB := mustSignLendingFixedTermBorrowTx(t, b, 1, principal, 30)
			hashA, err := txA.Hash()
			if err != nil {
				t.Fatal(err)
			}
			hashB, err := txB.Hash()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(hashA, hashB) {
				t.Fatalf("the two borrows were expected to share one hash, got %x and %x", hashA, hashB)
			}
			borrowAt(t, sp, a, txA, tc.height)
			borrowAt(t, sp, b, txB, tc.height)

			manager := nhbstate.NewManager(sp.Trie)
			addrA, addrB := a.PubKey().Address().Bytes(), b.PubKey().Address().Bytes()
			var legacyID [32]byte
			copy(legacyID[:], hashA)
			idA, idB := lendingIDScoped("loan", addrA, hashA), lendingIDScoped("loan", addrB, hashA)

			_, legacyFound, err := manager.LendingGetFixedTermLoan(legacyID)
			if err != nil {
				t.Fatal(err)
			}
			loanA, foundA, err := manager.LendingGetFixedTermLoan(idA)
			if err != nil {
				t.Fatal(err)
			}
			loanB, foundB, err := manager.LendingGetFixedTermLoan(idB)
			if err != nil {
				t.Fatal(err)
			}

			var poolA, poolB [20]byte
			copy(poolA[:], addrA)
			copy(poolB[:], addrB)
			activeA, okA, err := manager.LendingGetActiveFixedTermLoanID("default", poolA)
			if err != nil || !okA {
				t.Fatalf("borrower A has no active loan index: ok=%v err=%v", okA, err)
			}
			activeB, okB, err := manager.LendingGetActiveFixedTermLoanID("default", poolB)
			if err != nil || !okB {
				t.Fatalf("borrower B has no active loan index: ok=%v err=%v", okB, err)
			}

			if !tc.separate {
				if !legacyFound || foundA || foundB {
					t.Fatalf("expected one record under the transaction hash only: legacy=%v scopedA=%v scopedB=%v", legacyFound, foundA, foundB)
				}
				if activeA != legacyID || activeB != legacyID {
					t.Fatalf("both borrowers should point at the shared record, got %x and %x", activeA[:4], activeB[:4])
				}
				shared, _, _ := manager.LendingGetFixedTermLoan(legacyID)
				if !bytes.Equal(shared.Borrower.Bytes(), addrB) {
					t.Fatalf("the second borrower's record was expected to have overwritten the first")
				}
				return
			}
			if legacyFound {
				t.Fatalf("no record may exist under the shared transaction hash from the activation height")
			}
			if !foundA || !foundB {
				t.Fatalf("each borrower must own a record: A=%v B=%v", foundA, foundB)
			}
			if !bytes.Equal(loanA.Borrower.Bytes(), addrA) || !bytes.Equal(loanB.Borrower.Bytes(), addrB) {
				t.Fatalf("records are held by the wrong borrowers")
			}
			if loanA.LoanID != idA || loanB.LoanID != idB {
				t.Fatalf("a record carries an ID other than its key")
			}
			if activeA != idA || activeB != idB {
				t.Fatalf("active loan indexes point at %x and %x, want %x and %x", activeA[:4], activeB[:4], idA[:4], idB[:4])
			}
			market, marketFound, err := nhbstate.NewManager(sp.Trie).LendingGetMarket("default")
			if err != nil || !marketFound || market == nil {
				t.Fatalf("load market: found=%v err=%v", marketFound, err)
			}
			want := new(big.Int).Mul(principal, big.NewInt(2))
			if market.TotalNHBBorrowed.Cmp(want) != 0 {
				t.Fatalf("pool total borrowed %s, want the sum of both loans %s", market.TotalNHBBorrowed, want)
			}
		})
	}
}

// From the activation height an origination onto an ID that already has a record is
// refused for both kinds; below it (and by default) the ID is the plain hash and
// nothing is checked, as in every earlier release.
func TestLendingFixedTermRecordIDRefusesAnExistingRecordOnlyFromTheGate(t *testing.T) {
	sender := bytes.Repeat([]byte{0xAB}, 20)
	hash := bytes.Repeat([]byte{0xCD}, 32)

	for _, kind := range []string{"loan", "deposit"} {
		t.Run(kind, func(t *testing.T) {
			sp := newStakingStateProcessor(t)
			sp.BeginBlock(lendingIDTestActivation, time.Unix(1_900_000_000, 0).UTC())
			defer sp.EndBlock()

			// Gate off: the plain hash, no check.
			id, err := sp.lendingFixedTermRecordID(kind, sender, hash)
			if err != nil || !bytes.Equal(id[:], hash) {
				t.Fatalf("gate off: got %x, %v; want the transaction hash and no error", id, err)
			}

			sp.SetLendingFixedTermIDActivationHeight(lendingIDTestActivation + 1)
			id, err = sp.lendingFixedTermRecordID(kind, sender, hash)
			if err != nil || !bytes.Equal(id[:], hash) {
				t.Fatalf("below the height: got %x, %v; want the transaction hash and no error", id, err)
			}

			sp.SetLendingFixedTermIDActivationHeight(lendingIDTestActivation)
			id, err = sp.lendingFixedTermRecordID(kind, sender, hash)
			if err != nil {
				t.Fatalf("at the height with no record: %v", err)
			}
			if want := lendingIDScoped(kind, sender, hash); id != want {
				t.Fatalf("at the height: got %x, want %x", id, want)
			}
			if other := lendingIDScoped(kind, bytes.Repeat([]byte{0xAC}, 20), hash); other == id {
				t.Fatalf("two senders produced the same ID")
			}
			if loanID := lendingIDScoped("loan", sender, hash); kind == "deposit" && loanID == id {
				t.Fatalf("a loan and a deposit produced the same ID")
			}

			manager := nhbstate.NewManager(sp.Trie)
			switch kind {
			case "loan":
				err = manager.LendingPutFixedTermLoan(&lending.FixedTermLoan{LoanID: id, PoolID: "default", PrincipalWei: big.NewInt(1), TotalInterestWei: big.NewInt(0), RepaidWei: big.NewInt(0), Status: lending.FixedTermLoanStatusActive})
			default:
				err = manager.LendingPutFixedTermDeposit(&lending.FixedTermDeposit{DepositID: id, PoolID: "default", PrincipalWei: big.NewInt(1), TotalInterestOwedWei: big.NewInt(0), PaidInterestWei: big.NewInt(0), Status: lending.FixedTermDepositStatusActive})
			}
			if err != nil {
				t.Fatalf("seed the existing record: %v", err)
			}
			if _, err := sp.lendingFixedTermRecordID(kind, sender, hash); err == nil {
				t.Fatalf("an origination onto an existing %s record was not refused", kind)
			}

			// Gate off again: the same record is not looked at.
			sp.SetLendingFixedTermIDActivationHeight(0)
			if _, err := sp.lendingFixedTermRecordID(kind, sender, hash); err != nil {
				t.Fatalf("gate off must not check for a record: %v", err)
			}
		})
	}
}

// The gate must survive Copy and a rebuild of the state processor from the node.
func TestLendingFixedTermIDGateReachesBlockState(t *testing.T) {
	node := newTestNode(t)
	node.stateMu.RLock()
	unset := node.state.lendingFixedTermIDFromHeight
	node.stateMu.RUnlock()
	if unset != 0 {
		t.Fatalf("a new node has activation height %d, want 0 (off)", unset)
	}

	node.SetLendingFixedTermIDActivationHeight(lendingIDTestActivation)

	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	if got := node.state.lendingFixedTermIDFromHeight; got != lendingIDTestActivation {
		t.Fatalf("node state has activation height %d, want %d", got, lendingIDTestActivation)
	}
	copied, err := node.state.Copy()
	if err != nil {
		t.Fatalf("copy state: %v", err)
	}
	if got := copied.lendingFixedTermIDFromHeight; got != lendingIDTestActivation {
		t.Fatalf("a copy of the state has activation height %d, want %d", got, lendingIDTestActivation)
	}
	if err := node.rebuildStateProcessorLocked(node.state.Trie.Root()); err != nil {
		t.Fatalf("rebuild state processor: %v", err)
	}
	if got := node.state.lendingFixedTermIDFromHeight; got != lendingIDTestActivation {
		t.Fatalf("a rebuilt state processor has activation height %d, want %d", got, lendingIDTestActivation)
	}
}
