package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// verify_prod_config.sh reads the TOML with Python's tomllib (3.11 and newer) or
// the tomli module. On a host with neither it used to die in a Python traceback
// halfway through step 4 of the one-shot deploy. It must say what is missing and
// stop before reading anything (PL-DC-16).
func TestVerifyProdConfigNamesAMissingTOMLReader(t *testing.T) {
	bash := bashPath(t)
	requireTOMLPython(t, bash)

	pyOut, code := runBash(t, bash, ".", nil, "-c", "command -v python3")
	if code != 0 {
		t.Skip("cannot locate python3")
	}
	realPython := strings.TrimSpace(pyOut)

	// A python3 that behaves like one with neither TOML module installed: the
	// import probes fail, and a script piped in finds both modules blocked.
	dir := t.TempDir()
	shim := "#!/usr/bin/env bash\n" +
		"if [ \"${1:-}\" = \"-c\" ]; then\n" +
		"  case \"${2:-}\" in\n" +
		"    *\"import tomllib\"*|*\"import tomli\"*) exit 1 ;;\n" +
		"  esac\n" +
		"fi\n" +
		"if [ \"${1:-}\" = \"-\" ]; then\n" +
		"  exec \"" + realPython + "\" -c \"import sys; sys.modules['tomllib']=None; sys.modules['tomli']=None; exec(sys.stdin.read())\"\n" +
		"fi\n" +
		"exec \"" + realPython + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "python3"), []byte(shim), 0o755); err != nil {
		t.Fatalf("write python shim: %v", err)
	}

	data, err := os.ReadFile(repoFile(t, "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	cfg := writeConfig(t, string(data))
	env := []string{"PATH=" + bashArg(dir) + string(os.PathListSeparator) + os.Getenv("PATH")}
	out, code := runBash(t, bash, repoFile(t, "."), env,
		bashArg(repoFile(t, "scripts/verify_prod_config.sh")), "-c", bashArg(cfg))
	if code == 0 {
		t.Fatalf("the check must not pass when it cannot read the TOML:\n%s", out)
	}
	if !strings.Contains(out, "tomli") || !strings.Contains(out, "3.11") {
		t.Fatalf("the message must say what to install (Python 3.11 or the tomli module), got:\n%s", out)
	}
	if strings.Contains(out, "Traceback") {
		t.Fatalf("the check died in a Python traceback instead of saying what is missing:\n%s", out)
	}
}
