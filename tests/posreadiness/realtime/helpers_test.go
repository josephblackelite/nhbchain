//go:build posreadiness

package posreadiness

import (
	"math/big"
	"testing"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/tests/posreadiness/harness"
)

func newMiniChain(t *testing.T) *harness.MiniChain {
	t.Helper()
	chain, err := harness.NewMiniChain()
	if err != nil {
		t.Fatalf("new mini chain: %v", err)
	}
	t.Cleanup(func() {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("recovering minichain shutdown panic: %v", r)
			}
		}()
		if err := chain.Close(); err != nil {
			t.Fatalf("close minichain: %v", err)
		}
	})
	return chain
}

func seedAccount(node *core.Node, key *crypto.PrivateKey, balance *big.Int) error {
	return node.WithState(func(m *nhbstate.Manager) error {
		account := &types.Account{BalanceNHB: new(big.Int).Set(balance), BalanceZNHB: big.NewInt(0)}
		return m.PutAccount(key.PubKey().Address().Bytes(), account)
	})
}
