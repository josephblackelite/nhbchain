package core

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-DC-39: the creator engine's stake path used to burn the fan's deposit and mint
// the redeemed assets on unstake. It now moves the principal through a module-owned
// vault (native/creator, TestStakeThenUnstakeConservesSupply and its neighbours). The
// engine is still dormant on this line: no transaction type reaches it, and the
// Node methods that do reach it mutate the state trie directly, outside block
// execution, which is not safe for a replicated chain. These tests keep it dormant. They
// fail the day something starts to call the engine, which is the day the checklist in
// docs/creator/economics.md must have been completed.

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatalf("not the repository root: %s", dir)
	}
	return dir
}

func TestNoTransactionTypeReachesTheCreatorEngine(t *testing.T) {
	for _, typ := range types.AllTxTypes() {
		if strings.Contains(strings.ToLower(types.TxTypeName(typ)), "creator") {
			t.Errorf("transaction type %s (0x%02X) looks like a creator transaction: the creator engine is dormant until its re-enable checklist (docs/creator/economics.md) is done", types.TxTypeName(typ), byte(typ))
		}
	}
}

func TestNothingOutsideTheNodeCallsTheCreatorWriteMethods(t *testing.T) {
	calls := regexp.MustCompile(`\.Creator(Publish|Tip|Stake|Unstake|ClaimPayouts)\(`)
	root := repoRoot(t)
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "outbox-W2", "sdk", "clients":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if calls.Match(data) {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 0 {
		t.Fatalf("these files call the Node's creator write methods, which mutate the state trie outside block execution: %v", offenders)
	}
}

// The node hands the creator engine the stake vault the engine moves principal through,
// derived the way the node derives its other module accounts and distinct from the
// accounts that hold amounts owed to creators.
func TestNodeHandsTheCreatorEngineItsStakeVault(t *testing.T) {
	node := newTestNode(t)
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	engine := node.newCreatorEngine(nhbstate.NewManager(node.state.Trie))
	want := deriveModuleAddress("module/creator/stake", crypto.NHBPrefix)
	got := engine.StakeVault()
	if string(got[:]) != string(want.Bytes()) {
		t.Fatalf("the engine's stake vault is %x, want %x", got, want.Bytes())
	}
	var zero [20]byte
	if got == zero {
		t.Fatalf("the engine has no stake vault")
	}
	for _, other := range []string{"module/creator/payout", "module/creator/rewards"} {
		if o := deriveModuleAddress(other, crypto.NHBPrefix); string(o.Bytes()) == string(got[:]) {
			t.Fatalf("the stake vault is the same account as %s: principal and amounts owed to creators would be commingled", other)
		}
	}
}
