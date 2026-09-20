package scripts_test

// The fuzz and audit targets in the Makefile must be able to run: `go test -fuzz`
// takes one package and one fuzz function, and finds them only in _test.go
// files; the audit phases hash files that have to exist.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var fuzzFunc = regexp.MustCompile(`(?m)^func (Fuzz[A-Za-z0-9_]*)\(`)

// repoFuzzTargets walks the repository and returns "<dir> <function>" for every
// fuzz function in a _test.go file, and the non-test files that declare one.
func repoFuzzTargets(t *testing.T) (targets, misplaced []string) {
	t.Helper()
	root := repoFile(t, ".")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "artifacts", "nm":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, m := range fuzzFunc.FindAllStringSubmatch(string(data), -1) {
			if strings.HasSuffix(path, "_test.go") {
				targets = append(targets, filepath.ToSlash(filepath.Dir(rel))+" "+m[1])
			} else {
				misplaced = append(misplaced, rel+" "+m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the repository: %v", err)
	}
	sort.Strings(targets)
	return targets, misplaced
}

func TestFuzzTargetsLiveInTestFiles(t *testing.T) {
	targets, misplaced := repoFuzzTargets(t)
	if len(misplaced) != 0 {
		t.Fatalf("fuzz functions in files that are not _test.go, where `go test -fuzz` cannot find them: %v", misplaced)
	}
	found := map[string]bool{}
	for _, target := range targets {
		found[target] = true
	}
	for _, want := range []string{
		"tests/fuzz FuzzCreatorURISanitization",
		"tests/fuzz FuzzGovernancePolicyDeltas",
		"tests/fuzz FuzzLendingSupplyWithdrawAmounts",
		"tests/fuzz FuzzPotsoEvidencePipeline",
	} {
		if !found[want] {
			t.Fatalf("%s is not a fuzz test in a _test.go file", want)
		}
	}
}

func TestFuzzScriptRunsEachTargetOnItsOwn(t *testing.T) {
	bash := bashPath(t)
	targets, _ := repoFuzzTargets(t)
	out, code := runBash(t, bash, repoFile(t, "."), []string{"FUZZ_LIST_ONLY=1"}, bashArg(repoFile(t, "scripts/fuzz_all.sh")))
	if code != 0 {
		t.Fatalf("fuzz_all.sh (list only) exited %d:\n%s", code, out)
	}
	var listed []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			listed = append(listed, line)
		}
	}
	sort.Strings(listed)
	if strings.Join(listed, "\n") != strings.Join(targets, "\n") {
		t.Fatalf("the script lists\n%s\nbut the repository has\n%s", strings.Join(listed, "\n"), strings.Join(targets, "\n"))
	}

	// The Makefile target goes through it, not through one go test over several
	// packages with a pattern that matches several functions.
	makefile, err := os.ReadFile(repoFile(t, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	var recipe []string
	in := false
	for _, line := range strings.Split(string(makefile), "\n") {
		if strings.HasPrefix(line, "bugcheck-fuzz:") {
			in = true
			continue
		}
		if in {
			if !strings.HasPrefix(line, "\t") {
				break
			}
			recipe = append(recipe, line)
		}
	}
	joined := strings.Join(recipe, "\n")
	if !strings.Contains(joined, "scripts/fuzz_all.sh") || strings.Contains(joined, "-fuzz=Fuzz") {
		t.Fatalf("the bugcheck-fuzz recipe is %q; it must run scripts/fuzz_all.sh", joined)
	}
}

func TestAuditTargetsNameFilesThatExist(t *testing.T) {
	makefile, err := os.ReadFile(repoFile(t, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	hash := regexp.MustCompile(`--hash ([^\s']+)`)
	for _, m := range hash.FindAllStringSubmatch(string(makefile), -1) {
		path := m[1]
		if strings.HasPrefix(path, "artifacts/") {
			continue // written by an earlier step of the same target
		}
		if _, err := os.Stat(repoFile(t, path)); err != nil {
			t.Errorf("a Makefile audit target hashes %s, which is not in the repository", path)
		}
	}

	// run_phase.sh is not executable in the repository, so a target has to run it
	// through bash.
	for _, line := range strings.Split(string(makefile), "\n") {
		if strings.Contains(line, "'./scripts/audit/run_phase.sh") {
			t.Errorf("a target runs run_phase.sh directly, but the file is not executable: %s", strings.TrimSpace(line))
		}
	}

	pathLine := regexp.MustCompile(`(?m)^\s+path: (\S+)`)
	for _, phase := range []string{"config", "docs"} {
		plan, err := os.ReadFile(repoFile(t, "ops/audit/"+phase+".yaml"))
		if err != nil {
			t.Fatalf("read the %s plan: %v", phase, err)
		}
		for _, m := range pathLine.FindAllStringSubmatch(string(plan), -1) {
			if _, err := os.Stat(repoFile(t, m[1])); err != nil {
				t.Errorf("ops/audit/%s.yaml lists %s, which is not in the repository", phase, m[1])
			}
		}
	}
}
