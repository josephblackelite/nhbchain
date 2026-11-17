package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// PL-R5T-34: step 1 of "Doing it by hand" makes the validator key in a temporary
// directory. That directory holds the key in plain text until it is installed, so
// the step must remove it on every path out: success, a key that could not be
// made, and an install that failed. This runs the step exactly as the guide
// prints it, with the key tool and sudo replaced by stand-ins, and checks that
// nothing is left in the temporary directory afterwards.
func TestDoingItByHandKeyStepRemovesItsTempDir(t *testing.T) {
	text := docSection(t, "docs/validators/snapshot-onboarding.md", "## Doing it by hand")
	lines := strings.Split(text, "\n")
	start, end := -1, -1
	for i, line := range lines {
		if strings.HasPrefix(line, "# 1. This validator's key") {
			start = i
		}
		if start >= 0 && i > start && strings.HasPrefix(line, "# 2.") {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatalf("could not find step 1 of Doing it by hand (start %d, end %d)", start, end)
	}
	var step []string
	for _, line := range lines[start:end] {
		if strings.HasPrefix(line, "#") {
			continue
		}
		step = append(step, line)
	}
	snippet := strings.Join(step, "\n")
	if !strings.Contains(snippet, "mktemp") || !strings.Contains(snippet, "generate-key") {
		t.Fatalf("step 1 no longer makes the key in a temporary directory with generate-key:\n%s", snippet)
	}

	bash := e2eBash(t)
	cases := []struct {
		name        string
		failKeyTool bool
		failInstall bool
		wantOK      bool
	}{
		{"success", false, false, true},
		{"the key tool fails", true, false, false},
		{"the install fails", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			b := filepath.ToSlash(filepath.Join(root, "b"))
			tmp := filepath.ToSlash(filepath.Join(root, "tmp"))
			if err := os.MkdirAll(filepath.Join(root, "b", "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o755); err != nil {
				t.Fatal(err)
			}
			tool := "#!/bin/sh\nif [ \"$STUB_KEY_FAIL\" = 1 ]; then echo boom >&2; exit 1; fi\necho plaintext-key > wallet.key\n"
			if err := os.WriteFile(filepath.Join(root, "b", "bin", "nhb-cli"), []byte(tool), 0o755); err != nil {
				t.Fatal(err)
			}
			script := "sudo() { if [ \"$STUB_INSTALL_FAIL\" = 1 ]; then return 1; fi; [ -f wallet.key ]; }\n" + snippet + "\n"
			cmd := exec.Command(bash, "-c", script)
			cmd.Env = append(os.Environ(), "B="+b, "TMPDIR="+tmp, "TMP="+tmp, "TEMP="+tmp)
			if tc.failKeyTool {
				cmd.Env = append(cmd.Env, "STUB_KEY_FAIL=1")
			}
			if tc.failInstall {
				cmd.Env = append(cmd.Env, "STUB_INSTALL_FAIL=1")
			}
			out, err := cmd.CombinedOutput()
			if tc.wantOK && err != nil {
				t.Fatalf("step 1 failed: %v\n%s", err, out)
			}
			if !tc.wantOK && err == nil {
				t.Fatalf("step 1 reported success although a command in it failed:\n%s", out)
			}
			left, rerr := os.ReadDir(filepath.Join(root, "tmp"))
			if rerr != nil {
				t.Fatal(rerr)
			}
			if len(left) != 0 {
				names := []string{}
				for _, e := range left {
					names = append(names, e.Name())
				}
				t.Fatalf("step 1 left %v in the temporary directory, where the key was made in plain text", names)
			}
		})
	}
}
