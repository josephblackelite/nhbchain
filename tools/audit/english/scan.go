package english

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var ignoredDirs = map[string]struct{}{
	"vendor":       {},
	".git":         {},
	"node_modules": {},
	"artifacts":    {},
	"build":        {},
	"bin":          {},
	"dist":         {},
}

// FileData captures the parsed representation of a file for analysis.
type FileData struct {
	Path     string
	Content  string
	Lines    []string
	FileMode fs.FileMode
	AST      *ast.File
	FileSet  *token.FileSet
	LogCalls []LogCall
}

// LogCall captures context around logging statements for downstream checks.
type LogCall struct {
	Line int
	Expr string
}

func scanRepository(includeTests bool) ([]FileData, error) {
	var files []FileData
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if _, ok := ignoredDirs[d.Name()]; ok {
				return filepath.SkipDir
			}
			return nil
		}

		if strings.HasPrefix(d.Name(), ".") && !strings.HasSuffix(d.Name(), ".env") {
			return nil
		}

		if !includeTests && strings.HasSuffix(path, "_test.go") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		lines := strings.Split(content, "\n")

		fd := FileData{
			Path:     filepath.ToSlash(path),
			Content:  content,
			Lines:    lines,
			FileMode: d.Type(),
		}

		if strings.HasSuffix(path, ".go") {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, content, parser.ParseComments)
			if err == nil {
				fd.AST = file
				fd.FileSet = fset
				fd.LogCalls = extractLogCalls(file, fset)
			}
		}

		files = append(files, fd)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

func (f FileData) line(idx int) string {
	if idx < 0 || idx >= len(f.Lines) {
		return ""
	}
	return f.Lines[idx]
}

func (f FileData) snippetAround(idx int) string {
	start := idx - 1
	if start < 0 {
		start = 0
	}
	end := idx + 2
	if end > len(f.Lines) {
		end = len(f.Lines)
	}
	return strings.Join(f.Lines[start:end], "\n")
}

func extractLogCalls(file *ast.File, fset *token.FileSet) []LogCall {
	var calls []LogCall
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var ident string
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if x, ok := fun.X.(*ast.Ident); ok {
				ident = x.Name + "." + fun.Sel.Name
			}
		case *ast.Ident:
			ident = fun.Name
		}
		if ident == "" {
			return true
		}
		if strings.HasPrefix(ident, "log.") || strings.HasPrefix(ident, "fmt.Printf") || strings.HasPrefix(ident, "fmt.Fprintf") {
			pos := fset.Position(call.Pos())
			calls = append(calls, LogCall{Line: pos.Line, Expr: ident})
		}
		return true
	})
	return calls
}
