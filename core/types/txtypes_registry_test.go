package types

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// declaredTxTypeConstants parses this package's non-test sources and returns
// every constant declared with the TxType type, by name, with its literal value
// when it has one. A const group that omits the type on later specs (iota
// style) inherits the type of the previous spec, as the compiler does.
func declaredTxTypeConstants(t *testing.T) map[string]uint64 {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}
	out := map[string]uint64{}
	{
		for _, file := range files {
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				inheritsTxType := false
				for _, spec := range gen.Specs {
					vs := spec.(*ast.ValueSpec)
					switch {
					case vs.Type != nil:
						ident, isIdent := vs.Type.(*ast.Ident)
						inheritsTxType = isIdent && ident.Name == "TxType"
					case len(vs.Values) > 0:
						inheritsTxType = false // an untyped constant starts a new run
					}
					if !inheritsTxType {
						continue
					}
					for i, name := range vs.Names {
						if name.Name == "_" {
							continue
						}
						value := ^uint64(0)
						if i < len(vs.Values) {
							if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.INT {
								if parsed, err := strconv.ParseUint(lit.Value, 0, 8); err == nil {
									value = parsed
								}
							}
						}
						out[name.Name] = value
					}
				}
			}
		}
	}
	return out
}

// TestAllTxTypesRegistryIsComplete is the compile-time-ish guard that a new
// transaction type cannot be added without being registered: it parses this
// package, finds every TxType constant, and requires each to be in the registry
// under the name it is declared with (minus the "TxType" prefix) and, where the
// value is a literal, with that value.
func TestAllTxTypesRegistryIsComplete(t *testing.T) {
	declared := declaredTxTypeConstants(t)
	if len(declared) < 70 {
		t.Fatalf("parsed only %d TxType constants; the parser is not seeing the declarations", len(declared))
	}
	registered := map[string]TxType{}
	for _, entry := range txTypeRegistry {
		registered[entry.Name] = entry.Type
	}
	for name, value := range declared {
		short := strings.TrimPrefix(name, "TxType")
		typ, ok := registered[short]
		if !ok {
			t.Errorf("constant %s is declared but missing from txTypeRegistry (add {%s, %q})", name, name, short)
			continue
		}
		if value != ^uint64(0) && uint64(typ) != value {
			t.Errorf("registry maps %s to 0x%02X but the constant is 0x%02X", short, byte(typ), value)
		}
	}
	for short := range registered {
		if _, ok := declared["TxType"+short]; !ok {
			t.Errorf("registry entry %q has no matching TxType%s constant", short, short)
		}
	}
	if len(declared) != len(txTypeRegistry) {
		t.Errorf("%d constants are declared but the registry has %d entries", len(declared), len(txTypeRegistry))
	}
}

func TestAllTxTypesIsSortedUniqueAndACopy(t *testing.T) {
	all := AllTxTypes()
	if len(all) != len(txTypeRegistry) {
		t.Fatalf("AllTxTypes returned %d types, registry has %d", len(all), len(txTypeRegistry))
	}
	if !sort.SliceIsSorted(all, func(i, j int) bool { return all[i] < all[j] }) {
		t.Fatalf("AllTxTypes must be in ascending byte order")
	}
	seen := map[TxType]bool{}
	for _, typ := range all {
		if seen[typ] {
			t.Fatalf("duplicate type 0x%02X", byte(typ))
		}
		seen[typ] = true
	}
	all[0] = 0xFF
	if AllTxTypes()[0] == 0xFF {
		t.Fatalf("AllTxTypes must return a fresh copy")
	}
	// The design's inventory: 0x01-0x4C with a gap at 0x1F.
	if len(all) != 75 || seen[0x1F] || !seen[0x01] || !seen[0x4C] {
		t.Fatalf("unexpected inventory: %d types", len(all))
	}
}

func TestTxTypeName(t *testing.T) {
	cases := map[TxType]string{
		TxTypeTransfer:       "Transfer",
		TxTypeCreateEscrow:   "CreateEscrow",
		TxTypeSubmitEvidence: "SubmitEvidence",
		0x00:                 "Unknown(0x00)",
		0x1F:                 "Unknown(0x1F)",
		0xFF:                 "Unknown(0xFF)",
	}
	for typ, want := range cases {
		if got := TxTypeName(typ); got != want {
			t.Errorf("TxTypeName(0x%02X) = %q, want %q", byte(typ), got, want)
		}
	}
}
