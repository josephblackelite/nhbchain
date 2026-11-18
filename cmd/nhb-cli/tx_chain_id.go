package main

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"

	"nhbchain/rpc/txchainid"
)

// The chain id a transaction signed now must carry (ledger PL-R5T-29).
//
// Until the chain reaches its TxNetworkChainIDActivationHeight a node accepts only the legacy constant that every
// NHB network shares; from it, only the network's own id. nhb_getNetworkStats (public; net_info is operator-only)
// reports the one to use as txChainId, so the command line asks the node instead of hard-coding either. If the
// node cannot be asked, or reports none (a release older than the one that reports it), the command signs
// nothing: a transaction signed with the shared constant would replay on another network.
//
// The one exception is the operator's own explicit choice: --chain-id <n> or the NHB_CHAIN_ID variable (the flag wins).
// It is for a node that does not report txChainId, or a network the operator knows better; it is never a default and never
// a fallback, and a value that is not a positive integer is refused, not ignored.

// chainIDFlag is the value of --chain-id for this run (nil when the flag was not given).
var chainIDFlag *big.Int

// parseChainIDOverride accepts only a decimal positive integer that fits a uint64 (what a transaction hash encodes).
func parseChainIDOverride(text string) (*big.Int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("a chain id is required: a positive whole number")
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return nil, fmt.Errorf("%q is not a positive whole number (decimal digits only)", text)
		}
	}
	id, ok := new(big.Int).SetString(text, 10)
	if !ok || id.Sign() <= 0 || !id.IsUint64() {
		return nil, fmt.Errorf("%q is not a positive whole number that fits 64 bits", text)
	}
	return id, nil
}

// txChainIDReader keeps the answer for the run: a command is one process, and the id changes once, at a height.
var txChainIDReader = newTxChainIDReader()

func newTxChainIDReader() *txchainid.Reader {
	return txchainid.NewReader(func(ctx context.Context) (*big.Int, error) {
		// rpcEndpoint is read at call time: --rpc and RPC_URL are applied after this package's variables are set up.
		return txchainid.HTTPFetcher(http.DefaultClient, rpcEndpoint, "")(ctx)
	}, 0)
}

// resetTxChainID forgets the kept answer (tests switch endpoints between cases).
func resetTxChainID() { txChainIDReader = newTxChainIDReader() }

// txChainID returns the chain id a transaction signed now must carry: the operator's explicit choice (--chain-id, else
// NHB_CHAIN_ID), or else what the node reports. It returns an error when an override is not a positive integer or when
// the node does not report a usable id. Callers refuse to sign on error.
func txChainID() (*big.Int, error) {
	if chainIDFlag != nil {
		return new(big.Int).Set(chainIDFlag), nil
	}
	if v := strings.TrimSpace(os.Getenv("NHB_CHAIN_ID")); v != "" {
		id, err := parseChainIDOverride(v)
		if err != nil {
			return nil, fmt.Errorf("NHB_CHAIN_ID: %w", err)
		}
		return id, nil
	}
	return txChainIDReader.ChainID(context.Background())
}
