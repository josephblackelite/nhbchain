package rpc

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
)

// The cap itself: with a per-chain cap of 3, transactions of ONE chain with
// four different nonces, sent from four different addresses, are refused at the
// fourth. Keyed by chain and nonce (the old key) the fourth would have been
// allowed, because every nonce had a bucket of its own.
func TestPerChainCapCountsTransactionsOfDifferentNonces(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{
		MaxTxPerIP:            100,
		MaxTxPerIdentity:      100,
		MaxTxPerChain:         3,
		MaxTxPerIdentityChain: 100,
	})
	now := time.Now()
	chain := big.NewInt(77)
	sources := []string{"203.0.113.1", "203.0.113.2", "203.0.113.3", "203.0.113.4"}
	for i, source := range sources {
		tx := &types.Transaction{ChainID: chain, Nonce: uint64(i + 1)}
		allowed := server.allowSource(source, "", transactionLimiterChainKey(tx), rateLimitTestMethod, now)
		if i < 3 && !allowed {
			t.Fatalf("transaction %d of the chain was refused before the cap of 3", i+1)
		}
		if i == 3 && allowed {
			t.Fatalf("the fourth transaction of one chain was allowed: the per-chain cap does not count different nonces together")
		}
	}
}

// Ledger PL-EXT-79: the per-chain cap of nhb_sendTransaction never bound because
// its key contained the nonce. Two transactions of one chain with different
// nonces must share one key, and the limiter must then count them together.
func TestTransactionLimiterChainKeyIgnoresTheNonce(t *testing.T) {
	a := &types.Transaction{ChainID: big.NewInt(18346390202490284624 % 1000003), Nonce: 7}
	b := &types.Transaction{ChainID: a.ChainID, Nonce: 8}
	if transactionLimiterChainKey(a) != transactionLimiterChainKey(b) {
		t.Fatalf("keys differ for one chain and two nonces: %q and %q", transactionLimiterChainKey(a), transactionLimiterChainKey(b))
	}
	other := &types.Transaction{ChainID: big.NewInt(5), Nonce: 7}
	if transactionLimiterChainKey(a) == transactionLimiterChainKey(other) {
		t.Fatalf("two chains share the key %q", transactionLimiterChainKey(a))
	}
	if transactionLimiterChainKey(nil) != "" || transactionLimiterChainKey(&types.Transaction{}) != "" {
		t.Fatalf("a missing transaction or chain id must give an empty key")
	}
}
