package config_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"nhbchain/core"
	"nhbchain/storage"
)

// The published way to join the network is: clone the repo, run scripts/validator-only-bootstrap.sh. What a stranger's node does depends on
// defaults that live in different files, and each of them has been wrong once without any test noticing:
//   - the genesis the shipped config.toml starts from (it named the retired Phase E genesis while the live network runs the 2026-09-09
//     relaunch genesis, so every node built from the documented path was refused at the handshake with "chain ID mismatch"),
//   - the network id the docs and the script publish,
//   - the bootnode the script dials (a documentation-only placeholder address was committed in its place on a side branch, so every join would have
//     dialed nothing; the repo's own rules name the validator bootnode as the one public address that belongs in the onboarding guide).
// These tests keep the genesis file, config.toml, the bootstrap script, the README and the onboarding guide telling one story.

func repoRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return abs
}

func readRepo(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return data
}

func firstGroup(t *testing.T, pattern string, data []byte, what string) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindSubmatch(data)
	if m == nil {
		t.Fatalf("%s: pattern %q not found", what, pattern)
	}
	return string(m[1])
}

func configNetworkID(t *testing.T) string {
	return firstGroup(t, `(?m)^[ \t]*NetworkId[ \t]*=[ \t]*(\d+)[ \t]*$`, readRepo(t, "config.toml"), "config.toml NetworkId")
}

// The genesis config.toml starts from is the one the bootstrap script pins by hash, and the chain id it produces is the one everything publishes.
func TestShippedConfigStartsFromTheGenesisTheScriptPinsAndThatProducesThePublishedChainID(t *testing.T) {
	config := readRepo(t, "config.toml")
	genesisRel := firstGroup(t, `(?m)^GenesisFile\s*=\s*"\./([^"]+)"`, config, "config.toml GenesisFile")
	script := readRepo(t, "scripts/deployvalidator.sh")
	if want := firstGroup(t, `(?m)^GENESIS_FILE_REL='([^']+)'`, script, "script GENESIS_FILE_REL"); want != genesisRel {
		t.Fatalf("config.toml starts from %s but the bootstrap script installs and pins %s", genesisRel, want)
	}

	// the pinned hash is of the bytes a clone checks out (the file is marked -text in .gitattributes, so no line-ending conversion)
	genesisBytes := readRepo(t, genesisRel)
	sum := sha256.Sum256(genesisBytes)
	if pinned := firstGroup(t, `(?m)^GENESIS_SHA256='([0-9a-f]{64})'`, script, "script GENESIS_SHA256"); pinned != hex.EncodeToString(sum[:]) {
		t.Fatalf("the bootstrap script pins sha256 %s but %s hashes to %s: every install would be stopped, or a changed genesis let through", pinned, genesisRel, hex.EncodeToString(sum[:]))
	}

	// the chain id the node derives from that genesis is the one config.toml, the script, the README and the guide all publish
	networkID, err := strconv.ParseUint(configNetworkID(t), 10, 64)
	if err != nil {
		t.Fatalf("config.toml NetworkId is not a uint64: %v", err)
	}
	bc, err := core.NewBlockchain(storage.NewMemDB(), filepath.Join(repoRoot(t), filepath.FromSlash(genesisRel)), false)
	if err != nil {
		t.Fatalf("the genesis the shipped config starts from (%s) does not load: %v", genesisRel, err)
	}
	if got := bc.ChainID(); got != networkID {
		t.Fatalf("%s derives chain id %d but config.toml says NetworkId %d; a node built from this repo would be refused by every peer on the live network", genesisRel, got, networkID)
	}
}

func TestBootstrapDefaultBootnodeIsARealAddressAndDocumented(t *testing.T) {
	script := readRepo(t, "scripts/deployvalidator.sh")
	bootnode := firstGroup(t, `(?m)^BOOTNODE_DEFAULT='([^']+)'`, script, "deployvalidator.sh BOOTNODE_DEFAULT")

	host, port, err := net.SplitHostPort(bootnode)
	if err != nil {
		t.Fatalf("BOOTNODE_DEFAULT %q must be plain host:port: %v", bootnode, err)
	}
	if port != "6001" {
		t.Fatalf("BOOTNODE_DEFAULT %q: the P2P port is 6001", bootnode)
	}
	if ip := net.ParseIP(host); ip != nil {
		for _, cidr := range []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8"} {
			if _, block, _ := net.ParseCIDR(cidr); block.Contains(ip) {
				t.Fatalf("BOOTNODE_DEFAULT %q is in %s, which is not reachable from the public internet: a node built from this repo would dial nothing", bootnode, cidr)
			}
		}
	}

	// the README's network details and the onboarding guide quote the same bootnode the script dials
	if got := firstGroup(t, "Mainnet P2P Bootnode:\\*\\*\\s+`\"([^\"]+)\"`", readRepo(t, "README.md"), "README bootnode"); got != bootnode {
		t.Fatalf("README names bootnode %q but the script dials %q", got, bootnode)
	}
	if got := firstGroup(t, "\\| `--bootnode` \\| `([^`]+)` \\|", readRepo(t, "docs/validators/onboarding.md"), "onboarding --bootnode row"); got != bootnode {
		t.Fatalf("onboarding guide names bootnode %q but the script dials %q", got, bootnode)
	}
}

func TestPublishedNetworkIDMatchesTheShippedConfig(t *testing.T) {
	want := configNetworkID(t)

	if got := firstGroup(t, `(?m)^NETWORK_ID_DEFAULT='(\d+)'`, readRepo(t, "scripts/deployvalidator.sh"), "script NETWORK_ID_DEFAULT"); got != want {
		t.Fatalf("the bootstrap script defaults to network id %s but config.toml says %s", got, want)
	}
	if got := firstGroup(t, "\\*\\*Network ID:\\*\\* `(\\d+)`", readRepo(t, "README.md"), "README Network ID"); got != want {
		t.Fatalf("README publishes network id %s but config.toml says %s", got, want)
	}
	if got := firstGroup(t, "\\| `--network-id` \\| `(\\d+)` \\|", readRepo(t, "docs/validators/onboarding.md"), "onboarding --network-id row"); got != want {
		t.Fatalf("onboarding guide publishes network id %s but config.toml says %s", got, want)
	}
}
