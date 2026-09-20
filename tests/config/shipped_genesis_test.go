package config_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"nhbchain/config"
	"nhbchain/core"
	"nhbchain/core/genesis"
	"nhbchain/storage"
)

// The live network was started from config/genesis.relaunch.json. A node
// reports the first 8 bytes of the genesis block hash as its chain id.
const (
	liveChainID           = uint64(18346390202490284624)
	liveGenesisHash       = "fe9b78af9223ea50f456f63c41084dd99bac4aaa3a790a10fcc26d1dc63210a2"
	liveGenesisFile       = "config/genesis.relaunch.json"
	liveGenesisFileSHA256 = "10932798a0058ae35b135dae1a6ee1bdf6a8bc528a55c1eeb3e9eaab534f4b3b"
	liveGenesisFileSize   = 2013
)

func repoPath(rel string) string {
	return filepath.Join("..", "..", filepath.FromSlash(rel))
}

func readRepoFile(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(repoPath(rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return data
}

// bootChain starts a blockchain on the database the way a node does for a
// first start and returns its identity.
func bootChain(t *testing.T, db storage.Database, genesisPath string) (uint64, string) {
	t.Helper()
	bc, err := core.NewBlockchain(db, genesisPath, false)
	if err != nil {
		t.Fatalf("load genesis %s: %v", genesisPath, err)
	}
	return bc.ChainID(), hex.EncodeToString(bc.GenesisHash())
}

func expectLiveIdentity(t *testing.T, id uint64, hash string) {
	t.Helper()
	if id != liveChainID {
		t.Fatalf("chain id: got %d want %d", id, liveChainID)
	}
	if hash != liveGenesisHash {
		t.Fatalf("genesis hash: got %s want %s", hash, liveGenesisHash)
	}
}

func TestLiveGenesisFileIsByteIdentical(t *testing.T) {
	data := readRepoFile(t, liveGenesisFile)
	if len(data) != liveGenesisFileSize {
		t.Fatalf("%s is %d bytes, want %d", liveGenesisFile, len(data), liveGenesisFileSize)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != liveGenesisFileSHA256 {
		t.Fatalf("%s sha256 %s, want %s", liveGenesisFile, got, liveGenesisFileSHA256)
	}
	if !bytes.Equal(config.MainnetGenesis, data) {
		t.Fatalf("the embedded default genesis is not byte-identical to %s", liveGenesisFile)
	}
}

// The live genesis, loaded through the code path a node uses, must give the
// live chain id and genesis hash.
func TestLiveGenesisLoadsAsTheLiveChain(t *testing.T) {
	path := repoPath(liveGenesisFile)

	t.Run("memory database", func(t *testing.T) {
		db := storage.NewMemDB()
		defer db.Close()
		id, hash := bootChain(t, db, path)
		expectLiveIdentity(t, id, hash)
	})

	t.Run("leveldb, restarted", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "db")
		db, err := storage.NewLevelDB(dir)
		if err != nil {
			t.Fatalf("open leveldb: %v", err)
		}
		id, hash := bootChain(t, db, path)
		expectLiveIdentity(t, id, hash)
		db.Close()

		// A restart finds the stored genesis and derives the same identity.
		db, err = storage.NewLevelDB(dir)
		if err != nil {
			t.Fatalf("reopen leveldb: %v", err)
		}
		defer db.Close()
		id, hash = bootChain(t, db, path)
		expectLiveIdentity(t, id, hash)
	})

	// cmd/nhb parses the genesis file and hands core.NewNode a re-encoded
	// copy (genesis.resolved.json in the data directory); the copy must
	// describe the same chain.
	t.Run("as the node re-encodes it", func(t *testing.T) {
		spec, err := genesis.LoadGenesisSpec(path)
		if err != nil {
			t.Fatalf("load spec: %v", err)
		}
		data, err := json.MarshalIndent(spec, "", "  ")
		if err != nil {
			t.Fatalf("encode spec: %v", err)
		}
		resolved := filepath.Join(t.TempDir(), "genesis.resolved.json")
		if err := os.WriteFile(resolved, data, 0o644); err != nil {
			t.Fatalf("write resolved spec: %v", err)
		}
		db := storage.NewMemDB()
		defer db.Close()
		id, hash := bootChain(t, db, resolved)
		expectLiveIdentity(t, id, hash)
	})
}

// A node with no genesis file writes the embedded default; that default must
// be the live chain.
func TestEmbeddedDefaultGenesisLoadsAsTheLiveChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "genesis.json")
	if err := os.WriteFile(path, config.MainnetGenesis, 0o644); err != nil {
		t.Fatalf("write embedded genesis: %v", err)
	}
	db := storage.NewMemDB()
	defer db.Close()
	id, hash := bootChain(t, db, path)
	expectLiveIdentity(t, id, hash)
}

// shippedGenesisFiles is every genesis file the repository ships and the
// chain id it must produce. A new genesis file has to be added here on
// purpose, and an unloadable one fails this test.
var shippedGenesisFiles = map[string]uint64{
	"config/genesis.relaunch.json": liveChainID,
	// The genesis of the Phase E network that the relaunch replaced.
	// core.MintChainID is its chain id.
	"config/genesis.phase-e.json": core.MintChainID,
	// A local development genesis with no counterpart on any live network.
	"config/genesis.local.json": 18310364065167596269,
}

func TestEveryShippedGenesisFileLoadsWithAConsistentChainID(t *testing.T) {
	// A developer's own genesis file is git-ignored (config/local-genesis.json)
	// and is not shipped.
	ignored := map[string]bool{}
	for _, line := range strings.Split(string(readRepoFile(t, ".gitignore")), "\n") {
		if entry := strings.TrimSpace(line); strings.Contains(strings.ToLower(entry), "genesis") {
			ignored[entry] = true
		}
	}

	var found []string
	err := filepath.WalkDir(repoPath("."), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(entry.Name())
		if strings.Contains(name, "genesis") && strings.HasSuffix(name, ".json") {
			rel, relErr := filepath.Rel(repoPath("."), path)
			if relErr != nil {
				return relErr
			}
			if !ignored[filepath.ToSlash(rel)] {
				found = append(found, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
	sort.Strings(found)

	var want []string
	for rel := range shippedGenesisFiles {
		want = append(want, rel)
	}
	sort.Strings(want)
	if strings.Join(found, "\n") != strings.Join(want, "\n") {
		t.Fatalf("genesis files in the repository do not match the list this test covers:\n found: %v\n want:  %v", found, want)
	}

	for rel, wantID := range shippedGenesisFiles {
		t.Run(rel, func(t *testing.T) {
			path := repoPath(rel)
			db := storage.NewMemDB()
			defer db.Close()
			gotID, _ := bootChain(t, db, path)
			if gotID != wantID {
				t.Fatalf("chain id: got %d want %d", gotID, wantID)
			}
			// A chain id declared in the file must be the one its content
			// produces (loading already refuses a mismatch; say it here too).
			spec, err := genesis.LoadGenesisSpec(path)
			if err != nil {
				t.Fatalf("load spec: %v", err)
			}
			if declared, ok := spec.ChainIDValue(); ok && declared != gotID {
				t.Fatalf("declared chain id %d, derived %d", declared, gotID)
			}
		})
	}
}

var (
	networkIDPattern   = regexp.MustCompile(`(?m)^\s*NetworkId\s*=\s*"?(\d+)"?\s*$`)
	genesisFilePattern = regexp.MustCompile(`(?m)^\s*GenesisFile\s*=\s*"([^"]*)"\s*$`)
)

func onlyMatch(t *testing.T, pattern *regexp.Regexp, rel string, data []byte) string {
	t.Helper()
	matches := pattern.FindAllSubmatch(data, -1)
	if len(matches) != 1 {
		t.Fatalf("%s: expected exactly one match of %s, got %d", rel, pattern, len(matches))
	}
	return string(matches[0][1])
}

// Every shipped file that names the live network must agree with the genesis
// the node is started from.
func TestShippedConfigsNameTheLiveChain(t *testing.T) {
	want := strconv.FormatUint(liveChainID, 10)

	for _, rel := range []string{
		"config.toml",
		"config/prod.toml",
		"deploy/helm/values/prod/consensusd.yaml",
		"deploy/helm/values/prod/p2pd.yaml",
	} {
		data := readRepoFile(t, rel)
		if got := onlyMatch(t, networkIDPattern, rel, data); got != want {
			t.Errorf("%s: NetworkId %s, want %s", rel, got, want)
		}
		path := onlyMatch(t, genesisFilePattern, rel, data)
		if filepath.Base(path) != filepath.Base(liveGenesisFile) {
			t.Errorf("%s: GenesisFile %q is not the live genesis file %s", rel, path, filepath.Base(liveGenesisFile))
		}
	}

	env := readRepoFile(t, "examples/.env.example")
	if got := onlyMatch(t, regexp.MustCompile(`(?m)^NHB_CHAIN_ID=(\d+)\s*$`), "examples/.env.example", env); got != want {
		t.Errorf("examples/.env.example: NHB_CHAIN_ID %s, want %s", got, want)
	}

	scripts := map[string]*regexp.Regexp{
		"scripts/deployvalidator.sh": regexp.MustCompile(`(?m)^NETWORK_ID_DEFAULT='(\d+)'\s*$`),
		"deploy_node.sh":             regexp.MustCompile(`(?m)^LIVE_CHAIN_ID=(\d+)\s*$`),
		"update_env.go":              regexp.MustCompile(`"NHB_CHAIN_ID=(\d+)"`),
	}
	for rel, pattern := range scripts {
		if got := onlyMatch(t, pattern, rel, readRepoFile(t, rel)); got != want {
			t.Errorf("%s: chain id %s, want %s", rel, got, want)
		}
	}
	// The scripts refuse to start a node from any other genesis file.
	hashes := map[string]*regexp.Regexp{
		"scripts/deployvalidator.sh": regexp.MustCompile(`(?m)^GENESIS_SHA256='([0-9a-f]+)'\s*$`),
		"deploy_node.sh":             regexp.MustCompile(`(?m)^GENESIS_SHA256=([0-9a-f]+)\s*$`),
	}
	for rel, pattern := range hashes {
		if got := onlyMatch(t, pattern, rel, readRepoFile(t, rel)); got != liveGenesisFileSHA256 {
			t.Errorf("%s: genesis sha256 %s, want %s", rel, got, liveGenesisFileSHA256)
		}
	}
	// The validator bootstrap installs a snapshot only if it is for this
	// genesis block, so it pins the block hash as well as the file.
	pinned := regexp.MustCompile(`(?m)^GENESIS_HASH_DEFAULT='0x([0-9a-f]{64})'\s*$`)
	if got := onlyMatch(t, pinned, "scripts/deployvalidator.sh", readRepoFile(t, "scripts/deployvalidator.sh")); got != liveGenesisHash {
		t.Errorf("scripts/deployvalidator.sh: genesis hash %s, want %s", got, liveGenesisHash)
	}
}

// config.toml is what a new validator's config is made from. The values in it
// that change what a block does must be the network's: the treasuries the genesis
// file names, and the quorum-certificate height the live validators run with. A
// node started from a snapshot with any other value computes different state at
// the first block that touches it (the first reward epoch, for the POTSO
// treasury).
func TestShippedConfigConsensusValuesMatchTheGenesis(t *testing.T) {
	var spec struct {
		AdminWallet   string `json:"adminWallet"`
		LoyaltyGlobal struct {
			Treasury string `json:"treasury"`
		} `json:"loyaltyGlobal"`
	}
	if err := json.Unmarshal(readRepoFile(t, liveGenesisFile), &spec); err != nil {
		t.Fatalf("parse %s: %v", liveGenesisFile, err)
	}
	admin, znhb := spec.AdminWallet, spec.LoyaltyGlobal.Treasury
	if !strings.HasPrefix(admin, "nhb1") || !strings.HasPrefix(znhb, "znhb1") {
		t.Fatalf("genesis treasuries %q and %q", admin, znhb)
	}
	cfg := readRepoFile(t, "config.toml")

	all := func(pattern string) []string {
		var out []string
		for _, m := range regexp.MustCompile(pattern).FindAllSubmatch(cfg, -1) {
			out = append(out, string(m[1]))
		}
		return out
	}
	expectAll := func(what string, got []string, want string, atLeast int) {
		if len(got) < atLeast {
			t.Errorf("config.toml: expected at least %d %s, found %d", atLeast, what, len(got))
		}
		for _, g := range got {
			if g != want {
				t.Errorf("config.toml: %s is %s, the genesis says %s", what, g, want)
			}
		}
	}
	expectAll("[potso.rewards] TreasuryAddress", all(`(?m)^\s*TreasuryAddress\s*=\s*"([^"]*)"`), znhb, 1)
	expectAll("[subscriptions] Treasury", all(`(?m)^\s*Treasury\s*=\s*"([^"]*)"`), admin, 1)
	expectAll("an NHB OwnerWallet", all(`(?m)^\s*OwnerWallet\s*=\s*"(nhb1[^"]*)"`), admin, 2)
	expectAll("a ZNHB OwnerWallet", all(`(?m)^\s*OwnerWallet\s*=\s*"(znhb1[^"]*)"`), znhb, 1)
	if got := all(`(?m)^QuorumCertActivationHeight\s*=\s*(\d+)\s*$`); len(got) != 1 || got[0] != "0" {
		t.Errorf("config.toml: QuorumCertActivationHeight is %v, the live validators run with 0", got)
	}
	// No other treasury of an earlier network is left behind.
	if bytes.Contains(cfg, []byte("10lephh6ffd79cc7lk6edc6rkxe9ha8xe")) {
		t.Errorf("config.toml still names the treasury of an earlier network")
	}
}

// A config generated on first run names the live network too.
func TestGeneratedDefaultConfigNamesTheLiveChain(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "config.toml"), config.WithKeystorePassphrase("a-test-passphrase"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.P2P.NetworkID != liveChainID {
		t.Fatalf("default NetworkId: got %d want %d", cfg.P2P.NetworkID, liveChainID)
	}
}
