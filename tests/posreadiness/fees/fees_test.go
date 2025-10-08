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
	routeNHBKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate NHB route key: %v", err)
	}
	routeZNHBKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate ZNHB route key: %v", err)
	}

	startBalance := big.NewInt(5_000_000)
	if err := seedAccount(node, payerKey, new(big.Int).Set(startBalance)); err != nil {
		t.Fatalf("seed payer: %v", err)
	}
	if err := seedAccount(node, merchantKey, big.NewInt(0)); err != nil {
		t.Fatalf("seed merchant: %v", err)
	}
	if err := seedAccount(node, routeNHBKey, big.NewInt(0)); err != nil {
		t.Fatalf("seed NHB route wallet: %v", err)
	}
	if err := seedAccount(node, routeZNHBKey, big.NewInt(0)); err != nil {
		t.Fatalf("seed ZNHB route wallet: %v", err)
	}

	if _, err := chain.FinalizeTxs(); err != nil {
		t.Fatalf("commit seed block: %v", err)
	}

	var routeWalletNHB, routeWalletZNHB [20]byte
	copy(routeWalletNHB[:], routeNHBKey.PubKey().Address().Bytes())
	copy(routeWalletZNHB[:], routeZNHBKey.PubKey().Address().Bytes())
	policy := fees.Policy{
		Version: 1,
		Domains: map[string]fees.DomainPolicy{
			fees.DomainPOS: {
				FreeTierTxPerMonth: 2,
				MDRBasisPoints:     150,
				OwnerWallet:        routeWalletNHB,
				Assets: map[string]fees.AssetPolicy{
					fees.AssetNHB:  {MDRBasisPoints: 150, OwnerWallet: routeWalletNHB},
					fees.AssetZNHB: {MDRBasisPoints: 200, OwnerWallet: routeWalletZNHB},
				},
			},
		},
	}
	node.SetFeePolicy(policy)

	valueNHB := big.NewInt(1_000)
	valueZNHB := big.NewInt(2_000)
	merchantAddr := merchantKey.PubKey().Address().Bytes()

	grossNHB := big.NewInt(0)
	feeNHB := big.NewInt(0)
	netNHB := big.NewInt(0)

	for i := uint64(0); i < 3; i++ {
		tx := buildPOSTransfer(t, payerKey, merchantAddr, i, valueNHB)
		prev := len(node.Events())
		if _, err := chain.FinalizeTxs(tx); err != nil {
			t.Fatalf("finalize NHB tx %d: %v", i, err)
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
			t.Fatalf("expected fee event for NHB tx %d", i)
		}
		if asset := feeEvt.Attributes["asset"]; asset != "NHB" {
			t.Fatalf("unexpected asset tag for NHB tx %d: %s", i, asset)
		}
		feeWei := feeEvt.Attributes["feeWei"]
		grossNHB.Add(grossNHB, valueNHB)
		if i < 2 {
			if feeWei != "0" {
				t.Fatalf("expected free-tier fee 0 for NHB tx %d, got %s", i, feeWei)
			}
			netNHB.Add(netNHB, valueNHB)
			continue
		}
		feeAmount := new(big.Int)
		if _, ok := feeAmount.SetString(feeWei, 10); !ok {
			t.Fatalf("parse fee: %s", feeWei)
		}
		feeNHB.Add(feeNHB, feeAmount)
		netNHB.Add(netNHB, new(big.Int).Sub(valueNHB, feeAmount))
	}

	if err := node.WithState(func(m *nhbstate.Manager) error {
		acct, err := m.GetAccount(payerKey.PubKey().Address().Bytes())
		if err != nil {
			return err
		}
		acct.BalanceZNHB = big.NewInt(0).Mul(valueZNHB, big.NewInt(10))
		return m.PutAccount(payerKey.PubKey().Address().Bytes(), acct)
	}); err != nil {
		t.Fatalf("top up payer ZNHB: %v", err)
	}

	grossZNHB := big.NewInt(0)
	feeZNHB := big.NewInt(0)
	netZNHB := big.NewInt(0)

	for j := uint64(0); j < 2; j++ {
		nonce := j + 3
		tx := buildPOSTransferZNHB(t, payerKey, merchantAddr, nonce, valueZNHB)
		prev := len(node.Events())
		if _, err := chain.FinalizeTxs(tx); err != nil {
			t.Fatalf("finalize ZNHB tx %d: %v", j, err)
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
			t.Fatalf("expected fee event for ZNHB tx %d", j)
		}
		if asset := feeEvt.Attributes["asset"]; asset != "ZNHB" {
			t.Fatalf("unexpected asset tag for ZNHB tx %d: %s", j, asset)
		}
		feeWei := feeEvt.Attributes["feeWei"]
		if feeWei == "0" {
			t.Fatalf("expected ZNHB fee to apply after free-tier exhaustion")
		}
		grossZNHB.Add(grossZNHB, valueZNHB)
		feeAmount := new(big.Int)
		if _, ok := feeAmount.SetString(feeWei, 10); !ok {
			t.Fatalf("parse ZNHB fee: %s", feeWei)
		}
		feeZNHB.Add(feeZNHB, feeAmount)
		netZNHB.Add(netZNHB, new(big.Int).Sub(valueZNHB, feeAmount))
	}

	merchantNHB := accountBalance(t, node, merchantKey.PubKey().Address().Bytes(), fees.AssetNHB)
	if merchantNHB.Cmp(netNHB) != 0 {
		t.Fatalf("merchant NHB balance mismatch: got %s want %s", merchantNHB, netNHB)
	}
	merchantZNHB := accountBalance(t, node, merchantKey.PubKey().Address().Bytes(), fees.AssetZNHB)
	if merchantZNHB.Cmp(netZNHB) != 0 {
		t.Fatalf("merchant ZNHB balance mismatch: got %s want %s", merchantZNHB, netZNHB)
	}

	routeNHB := accountBalance(t, node, routeNHBKey.PubKey().Address().Bytes(), fees.AssetNHB)
	if routeNHB.Cmp(feeNHB) != 0 {
		t.Fatalf("route NHB balance mismatch: got %s want %s", routeNHB, feeNHB)
	}
	routeZNHB := accountBalance(t, node, routeZNHBKey.PubKey().Address().Bytes(), fees.AssetZNHB)
	if routeZNHB.Cmp(feeZNHB) != 0 {
		t.Fatalf("route ZNHB balance mismatch: got %s want %s", routeZNHB, feeZNHB)
	}

	totals, err := node.FeesTotals("pos")
	if err != nil {
		t.Fatalf("fees totals query: %v", err)
	}
	if len(totals) != 2 {
		t.Fatalf("expected two totals records, got %d", len(totals))
	}
	records := make(map[string]fees.Totals, len(totals))
	for i := range totals {
		records[totals[i].Asset] = totals[i]
	}
	nhbRecord, ok := records[fees.AssetNHB]
	if !ok {
		t.Fatalf("missing NHB totals record")
	}
	if nhbRecord.Gross == nil || nhbRecord.Gross.Cmp(grossNHB) != 0 {
		t.Fatalf("NHB gross mismatch: got %v want %v", nhbRecord.Gross, grossNHB)
	}
	if nhbRecord.Fee == nil || nhbRecord.Fee.Cmp(feeNHB) != 0 {
		t.Fatalf("NHB fee mismatch: got %v want %v", nhbRecord.Fee, feeNHB)
	}
	if nhbRecord.Net == nil || nhbRecord.Net.Cmp(netNHB) != 0 {
		t.Fatalf("NHB net mismatch: got %v want %v", nhbRecord.Net, netNHB)
	}
	if nhbRecord.Domain != fees.DomainPOS {
		t.Fatalf("unexpected NHB domain: %s", nhbRecord.Domain)
	}
	if nhbRecord.Wallet != routeWalletNHB {
		t.Fatalf("unexpected NHB wallet: got %x want %x", nhbRecord.Wallet, routeWalletNHB)
	}

	znhbRecord, ok := records[fees.AssetZNHB]
	if !ok {
		t.Fatalf("missing ZNHB totals record")
	}
	if znhbRecord.Gross == nil || znhbRecord.Gross.Cmp(grossZNHB) != 0 {
		t.Fatalf("ZNHB gross mismatch: got %v want %v", znhbRecord.Gross, grossZNHB)
	}
	if znhbRecord.Fee == nil || znhbRecord.Fee.Cmp(feeZNHB) != 0 {
		t.Fatalf("ZNHB fee mismatch: got %v want %v", znhbRecord.Fee, feeZNHB)
	}
	if znhbRecord.Net == nil || znhbRecord.Net.Cmp(netZNHB) != 0 {
		t.Fatalf("ZNHB net mismatch: got %v want %v", znhbRecord.Net, netZNHB)
	}
	if znhbRecord.Domain != fees.DomainPOS {
		t.Fatalf("unexpected ZNHB domain: %s", znhbRecord.Domain)
	}
	if znhbRecord.Wallet != routeWalletZNHB {
		t.Fatalf("unexpected ZNHB wallet: got %x want %x", znhbRecord.Wallet, routeWalletZNHB)
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

func buildPOSTransferZNHB(t *testing.T, key *crypto.PrivateKey, to []byte, nonce uint64, value *big.Int) *types.Transaction {
	if len(to) == 0 {
		t.Fatalf("recipient required")
	}
	tx := &types.Transaction{
		ChainID:         types.NHBChainID(),
		Type:            types.TxTypeTransferZNHB,
		Nonce:           nonce,
		To:              append([]byte(nil), to...),
		Value:           new(big.Int).Set(value),
		GasLimit:        21000,
		GasPrice:        big.NewInt(1),
		MerchantAddress: fees.DomainPOS,
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign znhb tx: %v", err)
	}
	return tx
}

func accountBalance(t *testing.T, node *core.Node, addr []byte, asset string) *big.Int {
	var balance *big.Int
	err := node.WithState(func(m *nhbstate.Manager) error {
		account, err := m.GetAccount(addr)
		if err != nil {
			return err
		}
		switch asset {
		case fees.AssetZNHB:
			if account.BalanceZNHB != nil {
				balance = new(big.Int).Set(account.BalanceZNHB)
			} else {
				balance = big.NewInt(0)
			}
		default:
			if account.BalanceNHB != nil {
				balance = new(big.Int).Set(account.BalanceNHB)
			} else {
				balance = big.NewInt(0)
			}
		}
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
