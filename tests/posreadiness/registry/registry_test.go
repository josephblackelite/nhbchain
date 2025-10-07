//go:build posreadiness

package posreadiness

import (
	"math/big"
	"strings"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/pos"
	"nhbchain/tests/posreadiness/harness"
)

func TestPauseMerchantBlocksSponsored(t *testing.T) {
	chain := newMiniChain(t)
	node := chain.Node()

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer key: %v", err)
	}
	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	if err := seedAccount(node, payerKey, big.NewInt(1_000_000)); err != nil {
		t.Fatalf("seed payer: %v", err)
	}
	if err := seedAccount(node, paymasterKey, big.NewInt(1_000_000)); err != nil {
		t.Fatalf("seed paymaster: %v", err)
	}

	if _, err := chain.FinalizeTxs(); err != nil {
		t.Fatalf("commit funding block: %v", err)
	}

	merchant := "merchant-pause-1"
	device := "device-pause-1"
	if err := node.WithState(func(m *nhbstate.Manager) error {
		if err := m.POSPutMerchant(&pos.Merchant{Address: merchant, Paused: true}); err != nil {
			return err
		}
		return m.POSPutDevice(&pos.Device{DeviceID: device, Merchant: merchant})
	}); err != nil {
		t.Fatalf("seed registry state: %v", err)
	}
	if _, err := chain.FinalizeTxs(); err != nil {
		t.Fatalf("commit registry block: %v", err)
	}

	to := recipientKey.PubKey().Address().Bytes()
	baseTx := func() *types.Transaction {
		return &types.Transaction{
			ChainID:         types.NHBChainID(),
			Type:            types.TxTypeTransfer,
			Nonce:           0,
			To:              append([]byte(nil), to...),
			Value:           big.NewInt(1_000),
			GasLimit:        21000,
			GasPrice:        big.NewInt(1),
			MerchantAddress: merchant,
			DeviceID:        device,
		}
	}

	sponsored := baseTx()
	sponsored.Paymaster = append([]byte(nil), paymasterKey.PubKey().Address().Bytes()...)
	signPaymaster(t, sponsored, paymasterKey)
	signTransaction(t, sponsored, payerKey)

	assessment, err := node.EvaluateSponsorship(sponsored)
	if err != nil {
		t.Fatalf("evaluate sponsorship: %v", err)
	}
	if assessment == nil || assessment.Status != core.SponsorshipStatusThrottled {
		t.Fatalf("expected throttled sponsorship, got %+v", assessment)
	}
	if !strings.Contains(assessment.Reason, "merchant") {
		t.Fatalf("expected merchant pause reason, got %q", assessment.Reason)
	}

	raw := baseTx()
	signTransaction(t, raw, payerKey)

	block, err := chain.FinalizeTxs(raw)
	if err != nil {
		t.Fatalf("finalize raw tx: %v", err)
	}
	if block == nil || len(block.Transactions) != 1 {
		t.Fatalf("expected single raw transaction in block, got %#v", block)
	}
}

func TestRevokeDeviceBlocksSponsored(t *testing.T) {
	chain := newMiniChain(t)
	node := chain.Node()

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer key: %v", err)
	}
	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	if err := seedAccount(node, payerKey, big.NewInt(1_000_000)); err != nil {
		t.Fatalf("seed payer: %v", err)
	}
	if err := seedAccount(node, paymasterKey, big.NewInt(1_000_000)); err != nil {
		t.Fatalf("seed paymaster: %v", err)
	}

	if _, err := chain.FinalizeTxs(); err != nil {
		t.Fatalf("commit funding block: %v", err)
	}

	merchant := "merchant-revoke-1"
	device := "device-revoke-1"
	if err := node.WithState(func(m *nhbstate.Manager) error {
		if err := m.POSPutMerchant(&pos.Merchant{Address: merchant}); err != nil {
			return err
		}
		return m.POSPutDevice(&pos.Device{DeviceID: device, Merchant: merchant, Revoked: true})
	}); err != nil {
		t.Fatalf("seed registry state: %v", err)
	}
	if _, err := chain.FinalizeTxs(); err != nil {
		t.Fatalf("commit registry block: %v", err)
	}

	to := recipientKey.PubKey().Address().Bytes()
	baseTx := func() *types.Transaction {
		return &types.Transaction{
			ChainID:         types.NHBChainID(),
			Type:            types.TxTypeTransfer,
			Nonce:           0,
			To:              append([]byte(nil), to...),
			Value:           big.NewInt(500),
			GasLimit:        21000,
			GasPrice:        big.NewInt(1),
			MerchantAddress: merchant,
			DeviceID:        device,
		}
	}

	sponsored := baseTx()
	sponsored.Paymaster = append([]byte(nil), paymasterKey.PubKey().Address().Bytes()...)
	signPaymaster(t, sponsored, paymasterKey)
	signTransaction(t, sponsored, payerKey)

	assessment, err := node.EvaluateSponsorship(sponsored)
	if err != nil {
		t.Fatalf("evaluate sponsorship: %v", err)
	}
	if assessment == nil || assessment.Status != core.SponsorshipStatusThrottled {
		t.Fatalf("expected throttled sponsorship, got %+v", assessment)
	}
	if !strings.Contains(assessment.Reason, "device") {
		t.Fatalf("expected device revocation reason, got %q", assessment.Reason)
	}

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

func signTransaction(t *testing.T, tx *types.Transaction, key *crypto.PrivateKey) {
	t.Helper()
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
}

func signPaymaster(t *testing.T, tx *types.Transaction, key *crypto.PrivateKey) {
	t.Helper()
	hash, err := tx.Hash()
	if err != nil {
		t.Fatalf("hash transaction: %v", err)
	}
	sig, err := ethcrypto.Sign(hash, key.PrivateKey)
	if err != nil {
		t.Fatalf("sign paymaster: %v", err)
	}
	tx.PaymasterR = new(big.Int).SetBytes(sig[:32])
	tx.PaymasterS = new(big.Int).SetBytes(sig[32:64])
	tx.PaymasterV = new(big.Int).SetUint64(uint64(sig[64]) + 27)
}
