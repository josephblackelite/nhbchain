package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// bashPath returns the bash the script tests run under. On Windows they are
// skipped unless NHB_TEST_BASH names a POSIX bash (Git Bash, for one), since
// the bash first on PATH there may be a different, incompatible one.
func bashPath(t *testing.T) string {
	t.Helper()
	if path := strings.TrimSpace(os.Getenv("NHB_TEST_BASH")); path != "" {
		return path
	}
	if runtime.GOOS == "windows" {
		t.Skip("script tests need a POSIX bash; set NHB_TEST_BASH to run them on Windows")
	}
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	return path
}

// repoFile returns the absolute path of a file in the repository.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("resolve %s: %v", rel, err)
	}
	return abs
}

// bashArg renders a host path the way bash expects to receive it: Git Bash on
// Windows takes forward slashes, and everywhere else nothing changes.
func bashArg(path string) string {
	return filepath.ToSlash(path)
}

// runBash runs a bash script file and returns its combined output and exit code.
func runBash(t *testing.T, bash string, dir string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bash, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run %s: %v\n%s", bash, err, out)
	}
	return string(out), exitErr.ExitCode()
}
