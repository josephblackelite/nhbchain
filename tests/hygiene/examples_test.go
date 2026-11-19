package hygiene

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var relativeImport = regexp.MustCompile(`(?m)(?:from|import)\s+['"](\.{1,2}/[^'"]+)['"]`)

// Every relative import in a TypeScript example must resolve to a file in the
// repository. The wallet-lite sample imported a module that was one directory
// too short (PL-DC-41).
func TestExampleRelativeImportsResolve(t *testing.T) {
	root := repoRoot(t)
	exts := []string{"", ".ts", ".tsx", ".js", ".mjs", ".json", "/index.ts", "/index.tsx", "/index.js"}
	var missing []string
	for _, path := range repoFileList(t, root, "examples") {
		if !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx") {
			continue
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("read %s: %v", path, rerr)
		}
		for _, m := range relativeImport.FindAllStringSubmatch(string(data), -1) {
			target := filepath.Join(filepath.Dir(path), filepath.FromSlash(m[1]))
			found := false
			candidates := []string{target}
			// ESM TypeScript imports name the compiled .js file.
			if strings.HasSuffix(target, ".js") {
				candidates = append(candidates, strings.TrimSuffix(target, ".js"))
			}
			for _, cand := range candidates {
				for _, ext := range exts {
					if st, serr := os.Stat(cand + filepath.FromSlash(ext)); serr == nil && !st.IsDir() {
						found = true
					}
				}
			}
			if !found {
				rel, _ := filepath.Rel(root, path)
				missing = append(missing, filepath.ToSlash(rel)+" imports "+m[1])
			}
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("unresolved import: %s", m)
	}
}

var quotedRPCName = regexp.MustCompile(`['"]((?:loyalty|creator)_[A-Za-z0-9_]+)['"]`)

// The RPC method names the web samples allow or call must exist in the node's
// dispatch (a case in rpc/ or a table entry), otherwise the sample returns
// "unknown method" (PL-DC-41: three creator-rewards methods).
func TestExampleRPCMethodNamesExistInTheNode(t *testing.T) {
	root := repoRoot(t)
	var node strings.Builder
	entries, err := os.ReadDir(filepath.Join(root, "rpc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(root, "rpc", e.Name()))
		node.Write(b)
	}
	haystack := node.String()
	files := []string{
		"examples/merchant-loyalty-console/app/api/rpc/route.ts",
		"examples/merchant-loyalty-console/app/page.tsx",
		"examples/creator-studio/pages/api/rpc.ts",
	}
	for _, rel := range files {
		data, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if rerr != nil {
			t.Fatalf("read %s: %v", rel, rerr)
		}
		for _, m := range quotedRPCName.FindAllStringSubmatch(string(data), -1) {
			if !strings.Contains(haystack, `"`+m[1]+`"`) {
				t.Errorf("%s names RPC method %s, which the node does not implement", rel, m[1])
			}
		}
	}
}

// A browser-visible environment variable (NEXT_PUBLIC_*) must never be a source
// of an RPC token in an example.
func TestExamplesDoNotReadATokenFromABrowserVisibleVariable(t *testing.T) {
	root := repoRoot(t)
	re := regexp.MustCompile(`NEXT_PUBLIC_[A-Z0-9_]*TOKEN`)
	for _, path := range repoFileList(t, root, "examples") {
		if !(strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx") || strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".example")) {
			continue
		}
		data, _ := os.ReadFile(path)
		if loc := re.Find(data); loc != nil {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s reads %s: a browser-visible variable would ship the token to every visitor", filepath.ToSlash(rel), loc)
		}
	}
}
