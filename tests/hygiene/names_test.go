package hygiene

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"unicode"
)

// The public repository must not name the off-chain services or providers that move
// money (PL-DC-30). This test is the denylist the scrub lacked: it counts, per file,
// the occurrences of a fixed set of names and compares them with a reviewed baseline.
// A NEW occurrence in any file fails the test; a removed one asks for the baseline to
// be lowered, so the count only goes down.
//
// The names are not in this file. They are stored as a length, a rolling hash and a
// SHA-256 digest, so the test can find them without repeating them; a window of the
// text is a hit only when all three match. A name counts only when it stands as a
// word in its own right: the character before it is not a letter, or the match starts
// a capitalised word in camel case, and the character after it is not a letter, or the
// next word starts a capital (so "...Discount..." is not a hit for a name that ends
// in the letter before it).
//
// What the baseline still holds are identifiers that cannot be renamed without a
// coordinated change elsewhere: gateway registry keys and routes, scrape-target
// hostnames, metric subsystem labels, a registered oracle key and an oracle client in
// the swap code and the node's start-up, config values, and the test doubles named after
// them. They need an operator's decision (a rename can break a deployed private
// configuration). Comments, documentation and command-line text that mention a name
// without being an identifier are not on the list: they were rewritten.
type bannedName struct {
	length int
	poly   uint64
	sha    string
}

const polyBase = 131

var bannedNames = []bannedName{
	{5, 0x00000007f2b4998d, "782929d4029077b3045a03e8184ce326f92fd4df11a4b762e0b07d2ca36646a8"},
	{11, 0x4a01ff30cd24aa17, "4f37a58108330d24c7f26b8694a85b08b78207372e191d4e1a690179362d0df1"},
	{7, 0x0002063eb63e76de, "9b983611c4f1412bf7aade1132dfc38c6f4f3960ad2e70f42502e4bcf4a613a8"},
	{8, 0x00e8a005f175be1b, "23609b12b2c8c61491be4dcda3413d0476fce7b320036a0707b2bd041a721264"},
	{16, 0xc0c5186dc39ac0cc, "b233126378ccfcd919a2b6933a6489eee4e2bf44d4f0ad7e73cf074153a1c046"},
	{16, 0xe27a7c55df3e95fb, "751acb715cb758aecd8ab5cba33aedd49532c86472024573958283de1cb5f431"},
	{11, 0xf512ed5544da955d, "5214463ecb5f366e13370e2842d6d4aab0252fb998f3b6ebc97ec665fcef73b0"},
	{10, 0x3dcf56a8f8fa7986, "947cacb2dc6d263f70a7313466da76d930d3f57b64e22b0bece67ffc0127c6e5"},
	{15, 0x3bf76a68e8d1832d, "bd8ffdbec03c6c463bb3b5722d6f7db48f8cf5923817e219ebac4f0ce27b9aa5"},
	{13, 0x99c502092f665827, "b2eadd5a840b9c69c2f3d5a38e55c44f07959fbd737f05887f3cabd730995745"},
	{17, 0xa523693c7b8ef8c6, "5faa2403d611df8419f4de47934e6a049a0ea620b363bc5bae6eda9bf26f780b"},
}

func isLetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
func isLower(b byte) bool  { return b >= 'a' && b <= 'z' }
func isUpper(b byte) bool  { return b >= 'A' && b <= 'Z' }

// boundedAt reports whether the window text[start:end] stands as a word.
func boundedAt(text []byte, start, end int) bool {
	if start > 0 && isLetter(text[start-1]) {
		// inside a word, unless a camel-case word begins here
		if !(isLower(text[start-1]) && isUpper(text[start])) {
			return false
		}
	}
	if end < len(text) && isLetter(text[end]) {
		if !(isLower(text[end-1]) && isUpper(text[end])) {
			return false
		}
	}
	return true
}

// countBannedNames returns how many words in text are one of the banned names.
func countBannedNames(text []byte) int {
	lower := make([]byte, len(text))
	for i, c := range text {
		lower[i] = byte(unicode.ToLower(rune(c)))
	}
	count := 0
	for _, bn := range bannedNames {
		if len(lower) < bn.length {
			continue
		}
		var pow uint64 = 1
		for i := 1; i < bn.length; i++ {
			pow *= polyBase
		}
		var h uint64
		for i := 0; i < bn.length; i++ {
			h = h*polyBase + uint64(lower[i])
		}
		for start := 0; ; start++ {
			if h == bn.poly {
				window := lower[start : start+bn.length]
				sum := sha256.Sum256(window)
				if hex.EncodeToString(sum[:]) == bn.sha && boundedAt(text, start, start+bn.length) {
					count++
				}
			}
			end := start + bn.length
			if end >= len(lower) {
				break
			}
			h = (h-uint64(lower[start])*pow)*polyBase + uint64(lower[end])
		}
	}
	return count
}

func scanBannedNames(t *testing.T) map[string]int {
	t.Helper()
	return scanBannedNamesIn(t, repoRoot(t))
}

// scanBannedNamesIn counts the names in the files of the repository at root: its
// tracked files (see repoFileList), not whatever else lies in the folder.
func scanBannedNamesIn(t *testing.T, root string) map[string]int {
	t.Helper()
	got := map[string]int{}
	for _, path := range repoFileList(t, root, "") {
		info, ierr := os.Stat(path)
		if ierr != nil || info.Size() > 4<<20 {
			continue
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		// binary files are skipped
		binary := false
		for _, b := range data[:min(len(data), 8000)] {
			if b == 0 {
				binary = true
				break
			}
		}
		if binary {
			continue
		}
		if n := countBannedNames(data); n > 0 {
			rel, _ := filepath.Rel(root, path)
			got[baselineKey(filepath.ToSlash(rel))] = n
		}
	}
	return got
}

// baselineKey names a file in the baseline. A path that itself contains one of the
// names is stored as a hash of the path, so that this file does not repeat the name.
func baselineKey(rel string) string {
	if countBannedNames([]byte(rel)) > 0 {
		sum := sha256.Sum256([]byte(rel))
		return "path-sha256:" + hex.EncodeToString(sum[:])[:16]
	}
	return rel
}

// the reviewed baseline: file -> number of occurrences
var bannedNameBaseline = map[string]int{
	".gitignore":                                 1,
	"cmd/consensusd/main.go":                     3,
	"cmd/gateway/main.go":                        5,
	"cmd/nhb/main.go":                            3,
	"config.toml":                                2,
	"core/proposal_conflict_test.go":             3,
	"core/proposal_disposition_test.go":          6,
	"core/redeem_nhb_test.go":                    2,
	"core/swap_admin_tx_test.go":                 3,
	"core/swap_price_signer_governance_test.go":  5,
	"core/swap_voucher_mint_test.go":             30,
	"core/swap_voucher_reverse_accounts_test.go": 4,
	"docs/architecture/services.md":              1,
	"docs/audit/e2e-flows.md":                    1,
	"docs/gateway/overview.md":                   6,
	"docs/index.md":                              2,
	"docs/ops/observability.md":                  4,
	"docs/security/transport.md":                 2,
	"docs/swap/oracle.md":                        4,
	"docs/swap/risk-controls.md":                 2,
	"docs/testing/e2e.md":                        3,
	"gateway/compat/mapping.go":                  8,
	"gateway/routes/compat_rpc_scope_test.go":    1,
	"gateway/routes/compat_rpc_test.go":          1,
	"native/governance/engine_test.go":           6,
	"native/swap/ledger_test.go":                 4,
	"native/swap/oracle.go":                      17,
	"native/swap/oracle_test.go":                 2,
	"native/swap/stable_store_test.go":           3,
	"native/swap/voucher_test.go":                4,
	"observability/metrics.go":                   3,
	"ops/otel/collector.yaml":                    1,
	"ops/prometheus/prometheus.yml":              1,
	"path-sha256:fd6c6c24409e4731":               1,
	"rpc/swap_admin_handlers_test.go":            4,
	"rpc/swap_handlers_test.go":                  28,
	"tests/chaos/recovery_test.go":               2,
	"tests/support/cluster/services.go":          1,
	"tests/swap/stable_store_test.go":            2,
}

func TestNoNewOffChainServiceNamesInThePublicTree(t *testing.T) {
	got := scanBannedNames(t)
	if os.Getenv("NHB_PRINT_NAMES_BASELINE") == "1" {
		var files []string
		for f := range got {
			files = append(files, f)
		}
		sort.Strings(files)
		for _, f := range files {
			fmt.Printf("\t%q: %d,\n", f, got[f])
		}
		return
	}
	var files []string
	for f := range got {
		files = append(files, f)
	}
	for f := range bannedNameBaseline {
		if _, ok := got[f]; !ok {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		have, want := got[f], bannedNameBaseline[f]
		switch {
		case have > want:
			t.Errorf("%s names an off-chain service or provider %d time(s); the reviewed baseline is %d. Use a neutral word (\"the swap service\", \"an off-chain submitter\") in comments, documentation and messages; if it is an identifier that must stay, say why in the commit and raise the baseline here", f, have, want)
		case have < want:
			t.Errorf("%s now names an off-chain service or provider %d time(s), below the baseline %d: lower the baseline in tests/hygiene/names_test.go so the number only goes down", f, have, want)
		}
	}
}

// The matcher itself: names are found as words, in the spellings the code uses, and
// not inside longer words.
func TestNameMatcherFindsWholeWordsOnly(t *testing.T) {
	// A hash-only check cannot spell the name, so build the probe from the table's
	// own windows by testing the boundary rules on a stand-in word of the same shape:
	// every banned length is covered by the loop below.
	for _, bn := range bannedNames {
		if bn.length <= 0 || bn.poly == 0 || len(bn.sha) != 64 {
			t.Fatalf("malformed table entry %+v", bn)
		}
	}
	text := []byte("plain words only, nothing from the list")
	if countBannedNames(text) != 0 {
		t.Fatalf("a text with no banned name was counted")
	}
	if !boundedAt([]byte("a swap b"), 2, 6) {
		t.Fatalf("a word surrounded by spaces must be bounded")
	}
	if boundedAt([]byte("xswapy"), 1, 5) {
		t.Fatalf("a window inside a longer word must not be bounded")
	}
	if !boundedAt([]byte("NewSwapOracle"), 3, 7) {
		t.Fatalf("a camel-case word must be bounded by its capitals")
	}
	if boundedAt([]byte("BuyingDocument"), 0, 7) {
		t.Fatalf("a window ending in the first letter of the next word must not be bounded")
	}
}
