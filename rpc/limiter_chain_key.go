package rpc

import (
	"strings"

	"nhbchain/core/types"
)

// transactionLimiterChainKey is the chain part of the rate-limiter key of an
// nhb_sendTransaction call: the transaction's chain id, and nothing else. It
// must not carry the nonce, because the limiter uses this string as the key of
// the per-chain and the per-identity-per-chain buckets, and a nonce in it gives
// every transaction a bucket of its own (ledger PL-EXT-79).
func transactionLimiterChainKey(tx *types.Transaction) string {
	if tx == nil || tx.ChainID == nil {
		return ""
	}
	return strings.TrimSpace(tx.ChainID.String())
}
