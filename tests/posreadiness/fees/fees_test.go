//go:build posreadiness

package posreadiness

import (
	"math/big"
	"testing"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/fees"
	"nhbchain/tests/posreadiness/harness"
)

func TestSponsorshipCapsAndRouting(t *testing.T) {
	chain := newMiniChain(t)
	node := chain.Node()

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer key: %v", err)
	}
	merchantKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate merchant key: %v", err)
	}
	routeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate route key: %v", err)
	}

	startBalance := big.NewInt(5_000_000)
	if err := seedAccount(node, payerKey, new(big.Int).Set(startBalance)); err != nil {
		t.Fatalf("seed payer: %v", err)
	}
	if err := seedAccount(node, merchantKey, big.NewInt(0)); err != nil {
		t.Fatalf("seed merchant: %v", err)
	}
	if err := seedAccount(node, routeKey, big.NewInt(0)); err != nil {
		t.Fatalf("seed route wallet: %v", err)
	}

	if _, err := chain.FinalizeTxs(); err != nil {
		t.Fatalf("commit seed block: %v", err)
	}

	var routeWallet [20]byte
	copy(routeWallet[:], routeKey.PubKey().Address().Bytes())
	policy := fees.Policy{
		Version: 1,
		Domains: map[string]fees.DomainPolicy{
			fees.DomainPOS: {
				FreeTierAllowance: 2,
				MDRBps:            150,
				RouteWallet:       routeWallet,
			},
		},
	}
	node.SetFeePolicy(policy)

	value := big.NewInt(1_000)
	merchantAddr := merchantKey.PubKey().Address().Bytes()

	grossTotal := big.NewInt(0)
	expectedFee := big.NewInt(0)
	expectedNet := big.NewInt(0)

	for i := uint64(0); i < 3; i++ {
		tx := buildPOSTransfer(t, payerKey, merchantAddr, i, value)
		prev := len(node.Events())
		if _, err := chain.FinalizeTxs(tx); err != nil {
			t.Fatalf("finalize tx %d: %v", i, err)
		}
		var feeEvt *types.Event
		newEvents := node.Events()[prev:]
		for idx := range newEvents {
			if newEvents[idx].Type == "fees.applied" {
				copyEvt := newEvents[idx]
				feeEvt = &copyEvt
			}
		}
		if feeEvt == nil {
			t.Fatalf("expected fee event for tx %d", i)
		}
		feeWei := feeEvt.Attributes["feeWei"]
		if i < 2 {
			if feeWei != "0" {
				t.Fatalf("expected free-tier fee 0 for tx %d, got %s", i, feeWei)
			}
		} else {
			if feeWei == "0" {
				t.Fatalf("expected fee after free-tier exhaustion")
			}
		}
		grossTotal.Add(grossTotal, value)
		if i >= 2 {
			feeAmount := new(big.Int)
			if _, ok := feeAmount.SetString(feeWei, 10); !ok {
				t.Fatalf("parse fee: %s", feeWei)
			}
			expectedFee.Add(expectedFee, feeAmount)
			net := new(big.Int).Sub(value, feeAmount)
			expectedNet.Add(expectedNet, net)
		} else {
			expectedNet.Add(expectedNet, value)
		}
	}

	merchantBalance := accountBalance(t, node, merchantKey.PubKey().Address().Bytes())
	expectedMerchant := new(big.Int).Set(expectedNet)
	if merchantBalance.Cmp(expectedMerchant) != 0 {
		t.Fatalf("merchant balance mismatch: got %s want %s", merchantBalance.String(), expectedMerchant.String())
	}

	routeBalance := accountBalance(t, node, routeKey.PubKey().Address().Bytes())
	if routeBalance.Cmp(expectedFee) != 0 {
		t.Fatalf("route wallet balance mismatch: got %s want %s", routeBalance.String(), expectedFee.String())
	}

	totals, err := node.FeesTotals("pos")
	if err != nil {
		t.Fatalf("fees totals query: %v", err)
	}
	if len(totals) != 1 {
		t.Fatalf("expected single totals record, got %d", len(totals))
	}
	record := totals[0]
	if record.Gross == nil || record.Gross.Cmp(grossTotal) != 0 {
		t.Fatalf("gross mismatch: got %v want %v", record.Gross, grossTotal)
	}
	if record.Fee == nil || record.Fee.Cmp(expectedFee) != 0 {
		t.Fatalf("fee mismatch: got %v want %v", record.Fee, expectedFee)
	}
	if record.Net == nil || record.Net.Cmp(expectedNet) != 0 {
		t.Fatalf("net mismatch: got %v want %v", record.Net, expectedNet)
	}
	if record.Domain != fees.DomainPOS {
		t.Fatalf("unexpected domain: %s", record.Domain)
	}
	if record.Wallet != routeWallet {
		t.Fatalf("unexpected wallet: got %x want %x", record.Wallet, routeWallet)
	}
}

func buildPOSTransfer(t *testing.T, key *crypto.PrivateKey, to []byte, nonce uint64, value *big.Int) *types.Transaction {
	if len(to) == 0 {
		t.Fatalf("recipient required")
	}
	tx := &types.Transaction{
		ChainID:         types.NHBChainID(),
		Type:            types.TxTypeTransfer,
		Nonce:           nonce,
		To:              append([]byte(nil), to...),
		Value:           new(big.Int).Set(value),
		GasLimit:        21000,
		GasPrice:        big.NewInt(1),
		MerchantAddress: fees.DomainPOS,
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	return tx
}

func accountBalance(t *testing.T, node *core.Node, addr []byte) *big.Int {
	var balance *big.Int
	err := node.WithState(func(m *nhbstate.Manager) error {
		account, err := m.GetAccount(addr)
		if err != nil {
			return err
		}
		balance = new(big.Int).Set(account.BalanceNHB)
		return nil
	})
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	return balance
}

func newMiniChain(t *testing.T) *harness.MiniChain {
	t.Helper()
	chain, err := harness.NewMiniChain()
	if err != nil {
		t.Fatalf("new mini chain: %v", err)
	}
	t.Cleanup(func() {
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
