package modules

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// PL-R5T-93: the LendingModule write methods write the live state trie outside
// block execution. They are kept for a few tests only. This scans every non-test
// Go file under rpc/ and fails if any of them contains a call to one of the nine
// method names, so a handler can never start using them again without this test
// noticing. (The methods are defined in lending.go in this package; a definition
// is not a call.)
func TestNoRPCHandlerCallsTheLendingModuleWriteMethods(t *testing.T) {
	banned := map[string]struct{}{
		"CreatePool": {}, "SupplyNHB": {}, "WithdrawNHB": {}, "DepositZNHB": {},
		"WithdrawZNHB": {}, "BorrowNHB": {}, "BorrowNHBWithFee": {}, "RepayNHB": {}, "Liquidate": {},
	}
	root := ".." // rpc/
	var offenders []string
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if recv, isIdent := sel.X.(*ast.Ident); isIdent && recv.Name == "engine" {
				// native/lending's Engine has its own Liquidate; the module's
				// withEngine helper is how the test-only methods reach it.
				return true
			}
			if _, bad := banned[sel.Sel.Name]; bad {
				offenders = append(offenders, filepath.ToSlash(path)+": ."+sel.Sel.Name+"(")
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan rpc/: %v", err)
	}
	if scanned < 20 {
		t.Fatalf("scanned only %d files under %s: the scan is not looking at rpc/", scanned, root)
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("non-test code under rpc/ calls a test-only LendingModule write method:\n  %s", strings.Join(offenders, "\n  "))
	}
}
