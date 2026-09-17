// Command nhb-supply-audit is a read-only diagnostic tool that computes the
// true historical drift in the tracked NHB token-supply counter caused by
// applyMintTransaction never calling AdjustTokenSupply (fixed 2026-09-17 in
// core/state_transition.go -- see that fix's comment for the full root
// cause). It walks every block since genesis, sums the amount of every
// successfully-applied TxTypeMint transaction for token "NHB", and reports
// that sum alongside the currently tracked token/supply/NHB counter so the
// exact one-time reconciliation amount can be hardcoded into
// ReconcileNHBMintSupplyDriftOnce, mirroring how genesisNHBSupplyWei was
// derived for the earlier, analogous genesis-seeding gap.
//
// Nothing is ever written. Must be run against a COPY of a live data
// directory, never one a running nhb process has open (LevelDB does not
// allow concurrent readers/writers from separate processes) -- same
// constraint as cmd/nhb-diag.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"

	"nhbchain/config"
	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

// mintTransactionPayload mirrors core/mint.go's unexported type of the same
// name byte-for-byte (voucher + hex signature envelope) -- duplicated here
// rather than exported from core, since this tool only needs to read the
// voucher's own already-exported fields (Token, Amount), not verify the
// signature or apply any state change.
type mintTransactionPayload struct {
	Voucher   core.MintVoucher `json:"voucher"`
	Signature string           `json:"signature"`
}

func main() {
	configFile := flag.String("config", "./config.toml", "Path to the configuration file (only DataDir and GenesisFile are used)")
	flag.Parse()

	cfg, err := config.Load(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Opening data directory (read-only intent): %s\n", cfg.DataDir)
	db, err := storage.NewLevelDB(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open database (is another nhb process already using it? this must be a COPY, not the live directory): %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	chain, err := core.NewBlockchain(db, cfg.GenesisFile, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open blockchain: %v\n", err)
		os.Exit(1)
	}

	header := chain.CurrentHeader()
	if header == nil {
		fmt.Fprintln(os.Stderr, "Error: no committed header found")
		os.Exit(1)
	}
	tipHeight := header.Height
	fmt.Printf("Current committed height: %d\n", tipHeight)

	totalMinted := big.NewInt(0)
	var mintTxCount, blockCount, decodeWarnings int

	for height := uint64(1); height <= tipHeight; height++ {
		block, err := chain.GetBlockByHeight(height)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: load block %d: %v\n", height, err)
			os.Exit(1)
		}
		blockCount++
		for _, tx := range block.Transactions {
			if tx == nil || tx.Type != types.TxTypeMint {
				continue
			}
			var payload mintTransactionPayload
			if err := json.Unmarshal(tx.Data, &payload); err != nil {
				decodeWarnings++
				fmt.Fprintf(os.Stderr, "Warning: block %d: undecodable TxTypeMint payload (likely a rejected/pruned proposal attempt, not a settled mint): %v\n", height, err)
				continue
			}
			if payload.Voucher.NormalizedToken() != "NHB" {
				continue
			}
			amount, err := payload.Voucher.AmountBig()
			if err != nil {
				decodeWarnings++
				fmt.Fprintf(os.Stderr, "Warning: block %d: invalid voucher amount (likely a rejected/pruned proposal attempt): %v\n", height, err)
				continue
			}
			totalMinted.Add(totalMinted, amount)
			mintTxCount++
		}
	}

	fmt.Printf("\nScanned %d blocks.\n", blockCount)
	fmt.Printf("TxTypeMint(NHB) transactions found in block history: %d (decode warnings: %d -- warnings are expected for any pruned/rejected mint attempt that never settled, and are excluded from the sum)\n", mintTxCount, decodeWarnings)
	fmt.Printf("Sum of all historical NHB mint amounts (wei): %s\n", totalMinted.String())

	stateTrie, err := trie.NewTrie(db, header.StateRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open state trie at committed root: %v\n", err)
		os.Exit(1)
	}
	manager := nhbstate.NewManager(stateTrie)
	currentSupply, err := manager.TokenSupply("NHB")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: TokenSupply(NHB): %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Currently tracked token/supply/NHB counter (wei): %s\n", currentSupply.String())

	corrected := new(big.Int).Add(currentSupply, totalMinted)
	fmt.Printf("\nCorrected counter after applying the drift once (current + total historical mints): %s\n", corrected.String())
	fmt.Println("\nThis 'total historical NHB mint amount' figure is the exact constant to hardcode into ReconcileNHBMintSupplyDriftOnce, mirroring genesisNHBSupplyWei's derivation.")
}
