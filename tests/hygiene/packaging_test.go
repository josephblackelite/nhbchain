// Package hygiene holds checks that the repository's metadata and packaging
// files agree with each other and with the code (PL-DC-42): they read files only
// and touch no node code.
package hygiene

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

var goDirective = regexp.MustCompile(`(?m)^go (\d+)\.(\d+)(?:\.(\d+))?\s*$`)

// goVersion parses "1.24.0" style versions into comparable numbers.
type goVersion [3]int

func parseGoVersion(t *testing.T, s string) goVersion {
	t.Helper()
	parts := strings.Split(strings.TrimSpace(s), ".")
	var v goVersion
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			t.Fatalf("not a Go version: %q", s)
		}
		v[i] = n
	}
	return v
}

func (a goVersion) less(b goVersion) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func moduleGoVersion(t *testing.T, rel string) goVersion {
	t.Helper()
	m := goDirective.FindStringSubmatch(readRepoFile(t, rel))
	if m == nil {
		t.Fatalf("%s has no go directive", rel)
	}
	v := m[1] + "." + m[2]
	if m[3] != "" {
		v += "." + m[3]
	}
	return parseGoVersion(t, v)
}

func TestPackageJSONLicenseMatchesLICENSE(t *testing.T) {
	var pkg struct {
		License string `json:"license"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, "package.json")), &pkg); err != nil {
		t.Fatalf("parse package.json: %v", err)
	}
	if !strings.Contains(readRepoFile(t, "LICENSE"), "Apache License") {
		t.Fatalf("LICENSE is not the Apache License any more; update this test and package.json together")
	}
	if pkg.License != "Apache-2.0" {
		t.Fatalf("package.json says license %q but LICENSE is Apache 2.0", pkg.License)
	}
}

func TestSDKModuleGoVersionIsNotBelowTheRootModule(t *testing.T) {
	root, sdk := moduleGoVersion(t, "go.mod"), moduleGoVersion(t, "sdk/go.mod")
	if sdk.less(root) {
		t.Fatalf("sdk/go.mod says go %v but go.mod requires go %v", sdk, root)
	}
}

// Every place that tells a person which Go to install or that pins a build image
// must not name a Go older than the module requires.
func TestBuildFilesDoNotNameAGoOlderThanTheModule(t *testing.T) {
	need := moduleGoVersion(t, "go.mod")
	check := func(file string, re *regexp.Regexp) {
		t.Helper()
		matches := re.FindAllStringSubmatch(readRepoFile(t, file), -1)
		for _, m := range matches {
			if got := parseGoVersion(t, m[1]); got.less(need) {
				t.Errorf("%s names Go %s but go.mod requires %v", file, m[1], need)
			}
		}
	}
	check("nhb-go.sh", regexp.MustCompile(`Go (\d+\.\d+(?:\.\d+)?)\+`))
	check("README.md", regexp.MustCompile(`go\.dev/dl/go(\d+\.\d+(?:\.\d+)?)\.`))
	check("deploy/compose/Dockerfile", regexp.MustCompile(`(?m)^ARG GO_VERSION=(\d+\.\d+(?:\.\d+)?)`))
	for _, df := range []string{"Dockerfile.consensusd", "Dockerfile.p2pd"} {
		check("examples/compose/mininet/"+df, regexp.MustCompile(`(?m)^FROM golang:(\d+\.\d+(?:\.\d+)?)`))
	}
}

// The simulated cluster used by the e2e, chaos and perf gates must say in its own
// package documentation that it is a test double (PL-DC-27).
func TestClusterStubSaysItIsATestDouble(t *testing.T) {
	src := readRepoFile(t, "tests/support/cluster/services.go")
	if !strings.Contains(src, "TEST DOUBLE") {
		t.Fatalf("tests/support/cluster must state that it is a test double and imports no node code")
	}
	if strings.Contains(src, "\"nhbchain/core") || strings.Contains(src, "\"nhbchain/consensus") {
		t.Fatalf("the stub now imports node code: update its documentation and docs/audit/e2e-flows.md together")
	}
}

// The p2p server listens on TCP only (net.Listen("tcp") in p2p/server.go), so no
// document may tell an operator that UDP 6001 is required (PL-JN-06). The test
// reads the code first, so it cannot go stale: if the server starts listening on UDP,
// it fails and the documents are to be corrected the other way.
func TestDocsDoNotRequireUDPForP2P(t *testing.T) {
	var p2pSource strings.Builder
	for _, rel := range []string{"p2p/server.go", "p2p/seeds/seeds.go"} {
		if data, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel))); err == nil {
			p2pSource.Write(data)
		}
	}
	src := p2pSource.String()
	if !strings.Contains(src, `net.Listen("tcp"`) {
		t.Fatalf("p2p/server.go no longer listens with net.Listen(\"tcp\", ...): review this test")
	}
	if strings.Contains(src, "ListenUDP") || strings.Contains(src, "ListenPacket") {
		t.Fatalf("the p2p code now listens on UDP: the documents must say so; update this test")
	}
	claim := regexp.MustCompile(`(?i)(tcp\s*(and|/|&)\s*udp|udp\s*(and|/|&)\s*tcp|both protocols are required)`)
	for _, rel := range []string{"README.md", "docs/validators/onboarding.md", "docs/validators/snapshot-onboarding.md"} {
		for i, line := range strings.Split(readRepoFile(t, rel), "\n") {
			if strings.Contains(line, "6001") && claim.MatchString(line) {
				t.Errorf("%s:%d tells operators 6001 needs UDP: %s", rel, i+1, strings.TrimSpace(line))
			}
		}
	}
	// the sentence under the 6001 bullet in the onboarding guide
	if strings.Contains(readRepoFile(t, "docs/validators/onboarding.md"), "UDP is the") {
		t.Errorf("docs/validators/onboarding.md still has the 'UDP is the one people forget' sentence")
	}
}
