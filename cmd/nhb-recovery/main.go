// Command nhb-recovery is an operator tool for correcting the persisted
// consensus validator set when it no longer matches operational reality --
// for example after a validator hot-key rotation that was never reflected
// in consensus state, leaving the running node signing with a key the
// chain does not recognize as a validator (so its own votes can never
// count toward BFT quorum, no matter how many rounds elapse), or after a
// second validator has been permanently decommissioned and its registered
// power is blocking the remaining validator(s) from ever reaching 2/3
// quorum. It must be run against a stopped node's data directory (never
// against a directory a running nhb process has open).
//
// It touches only the validator-set trie entries; it does not read,
// modify, or need access to genesis, account balances, or stake data.
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"

	"nhbchain/cmd/internal/passphrase"
	"nhbchain/config"
	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/storage"
)

const validatorPassEnv = "NHB_VALIDATOR_PASS"

func main() {
	configFile := flag.String("config", "./config.toml", "Path to the configuration file")
	validatorAddr := flag.String("validator", "", "bech32 address of the validator to remove")
	fixSelf := flag.Bool("fix-self", false, "Replace the entire validator set with this node's own running key, keeping the current total power")
	dryRun := flag.Bool("dry-run", false, "Report what would change without committing a block")
	skipBlock := flag.Bool("skip-block", false, "Patch state and the tip's declared state root, but do NOT create/commit a new block. Use this when another validator already produced a real (zero-transaction) block reflecting the same out-of-band patch -- e.g. resyncing a node from genesis that will hit that exact historical block via normal P2P sync next. Patching here first makes this node's pending state already equal that block's declared root, so it can accept the real incoming block instead of needing its own (differently-hashed) synthetic one. Mutually exclusive with --dry-run.")
	flag.Parse()

	if *skipBlock && *dryRun {
		fmt.Fprintln(os.Stderr, "Error: --skip-block and --dry-run are mutually exclusive")
		os.Exit(1)
	}

	if *fixSelf && strings.TrimSpace(*validatorAddr) != "" {
		fmt.Fprintln(os.Stderr, "Error: --fix-self and --validator are mutually exclusive")
		os.Exit(1)
	}
	if !*fixSelf && strings.TrimSpace(*validatorAddr) == "" {
		fmt.Fprintln(os.Stderr, "Error: either --fix-self or --validator <addr> is required")
		os.Exit(1)
	}

	passSource := passphrase.NewSource(validatorPassEnv)
	cfg, err := config.Load(*configFile, config.WithKeystorePassphraseSource(passSource.Get))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Opening data directory: %s\n", cfg.DataDir)
	db, err := storage.NewLevelDB(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open database (is another nhb process already using it?): %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	privKey, err := loadValidatorKey(cfg, passSource.Get)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to load validator key: %v\n", err)
		os.Exit(1)
	}

	node, err := core.NewNode(db, privKey, cfg.GenesisFile, false, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open node: %v\n", err)
		os.Exit(1)
	}

	before := node.GetValidatorSet()
	fmt.Printf("Current validator set (%d entries):\n", len(before))
	for k, power := range before {
		printAddr(k, power)
	}

	var changed bool
	if *fixSelf {
		selfAddr := node.SelfValidatorAddress()
		totalPower := big.NewInt(0)
		for _, power := range before {
			if power != nil {
				totalPower.Add(totalPower, power)
			}
		}
		if totalPower.Sign() == 0 {
			fmt.Fprintln(os.Stderr, "Error: current validator set has zero total power; refusing to guess a power value. Use --validator/--power flags in a future run if you need an explicit value.")
			os.Exit(1)
		}
		newSet := map[string]*big.Int{string(selfAddr[:]): totalPower}
		if err := node.ReplaceValidatorSet(newSet); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to replace validator set: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Replaced validator set with this node's own key (%x) at total power %s\n", selfAddr, totalPower.String())
		changed = true
	} else {
		addr, err := crypto.DecodeAddress(*validatorAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --validator address: %v\n", err)
			os.Exit(1)
		}
		var addrBytes [20]byte
		copy(addrBytes[:], addr.Bytes())

		removed, remaining, err := node.RemoveValidatorFromSet(addrBytes)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to remove validator: %v\n", err)
			os.Exit(1)
		}
		if !removed {
			fmt.Printf("Validator %s was not present in the active set; nothing to do.\n", *validatorAddr)
			return
		}
		fmt.Printf("Removed %s from the validator set. Remaining total power: %s\n", *validatorAddr, remaining.String())
		changed = true
	}

	if !changed {
		return
	}

	after := node.GetValidatorSet()
	fmt.Printf("Validator set after change (%d entries):\n", len(after))
	for k, power := range after {
		printAddr(k, power)
	}

	if *dryRun {
		fmt.Println("--dry-run set: not committing a block. Re-run without --dry-run to finalize.")
		return
	}

	// The trie mutation above is only staged. Block creation unconditionally
	// resets any pending state that doesn't already match the tip block's
	// declared StateRoot (a safety check against startup/crash drift), which
	// would otherwise silently discard this fix before it could be included
	// in a block. Patch the tip's declared root to match the corrected
	// pending state first so that check finds them already in agreement.
	newRoot := node.PendingStateRoot()
	if len(newRoot) == 0 {
		fmt.Fprintln(os.Stderr, "Error: could not compute pending state root after the fix")
		os.Exit(1)
	}
	if err := node.PatchTipStateRoot(newRoot); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to patch tip state root: %v\n", err)
		os.Exit(1)
	}

	if *skipBlock {
		fmt.Printf("--skip-block set: patched tip state root to %x at height %d, but did not create a new block.\n", newRoot, node.GetHeight())
		fmt.Println("This node's pending state now matches a real block elsewhere in the network that reflects the same patch with zero transactions. Start the real nhb service so it can sync that block normally instead of needing its own.")
		return
	}

	heightBefore := node.GetHeight()
	block, err := node.CreateBlock(nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to create recovery block: %v\n", err)
		os.Exit(1)
	}
	if err := node.CommitBlock(block); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to commit recovery block: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Committed recovery block. Height %d -> %d. New state root: %x\n", heightBefore, node.GetHeight(), block.Header.StateRoot)
	fmt.Println("Done. Stop this tool's process fully before starting the real nhb service against this data directory.")
}

func printAddr(rawKey string, power *big.Int) {
	addr, err := crypto.NewAddress(crypto.NHBPrefix, []byte(rawKey))
	if err == nil {
		fmt.Printf("  %s  power=%s\n", addr.String(), power.String())
		return
	}
	fmt.Printf("  %x  power=%s\n", []byte(rawKey), power.String())
}

func loadValidatorKey(cfg *config.Config, resolvePassphrase func() (string, error)) (*crypto.PrivateKey, error) {
	// Every validator brought up via the one-command bootstrap script
	// (scripts/deployvalidator.sh) configures ValidatorKMSEnv, not a file
	// keystore -- the raw key lives only in node.env as a hex string. This
	// tool used to hard-refuse that configuration entirely, which meant it
	// could only ever run against a hand-deployed, keystore-file validator
	// (like the original primary), not a validator that joined through the
	// documented, recommended flow. The key material here is read the exact
	// same way cmd/nhb's own loadFromKMS/keyFromEnv does -- straight from
	// the environment variable this validator's own systemd unit already
	// exposes to it, never derived, generated, or logged by this tool.
	if envName := strings.TrimSpace(cfg.ValidatorKMSEnv); envName != "" {
		value, ok := os.LookupEnv(envName)
		if !ok {
			return nil, fmt.Errorf("environment variable %q not set", envName)
		}
		return parseHexPrivateKey(value)
	}
	if cfg.ValidatorKMSURI != "" {
		return nil, fmt.Errorf("ValidatorKMSURI is not supported by this recovery tool; run it on a host with a local keystore or ValidatorKMSEnv")
	}
	if cfg.ValidatorKeystorePath == "" {
		return nil, fmt.Errorf("validator keystore path not configured")
	}
	if resolvePassphrase == nil {
		return nil, fmt.Errorf("validator keystore passphrase required; set %s", validatorPassEnv)
	}
	pass, err := resolvePassphrase()
	if err != nil {
		return nil, fmt.Errorf("failed to obtain validator keystore passphrase: %w", err)
	}
	if strings.TrimSpace(pass) == "" {
		return nil, fmt.Errorf("validator keystore passphrase cannot be empty")
	}
	key, err := crypto.LoadFromKeystore(cfg.ValidatorKeystorePath, pass)
	if err != nil {
		return nil, fmt.Errorf("unable to decrypt keystore %s: %w", cfg.ValidatorKeystorePath, err)
	}
	return key, nil
}

// parseHexPrivateKey mirrors cmd/nhb's parsePrivateKeyMaterial: the raw key
// material stored in a ValidatorKMSEnv variable is hex, optionally
// 0x-prefixed.
func parseHexPrivateKey(material string) (*crypto.PrivateKey, error) {
	trimmed := strings.TrimSpace(material)
	trimmed = strings.TrimPrefix(trimmed, "0x")
	if trimmed == "" {
		return nil, fmt.Errorf("empty private key material")
	}
	raw, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("failed to decode hex private key: %w", err)
	}
	return crypto.PrivateKeyFromBytes(raw)
}
