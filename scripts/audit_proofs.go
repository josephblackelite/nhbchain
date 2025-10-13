package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	defaultLimit = 3
	minLimit     = 2
	maxLimit     = 4
	maxFileSize  = 5 * 1024 * 1024 // 5 MiB safety guard
)

type checklistItem struct {
	Title    string
	Keywords []string
	Limit    int
}

type keywordRef struct {
	ItemIndex int
	Keyword   string
	Norm      string
	Order     int
}

type keywordGroup struct {
	Norm string
	Refs []keywordRef
}

type match struct {
	Keyword string
	Path    string
	Line    int
	Snippet string
	Order   int
}

func main() {
	var (
		checklistPath string
		outPath       string
		rootPath      string
	)

	flag.StringVar(&checklistPath, "checklist", "", "path to the audit checklist markdown")
	flag.StringVar(&outPath, "out", "", "path to write the generated proofs markdown")
	flag.StringVar(&rootPath, "root", ".", "repository root to scan for proofs")
	flag.Parse()

	if checklistPath == "" {
		fatal("missing required -checklist flag")
	}
	if outPath == "" {
		fatal("missing required -out flag")
	}

	rootAbs, err := filepath.Abs(rootPath)
	if err != nil {
		fatalf("resolve repository root: %v", err)
	}
	checklistAbs, err := filepath.Abs(checklistPath)
	if err != nil {
		fatalf("resolve checklist path: %v", err)
	}
	outAbs, err := filepath.Abs(outPath)
	if err != nil {
		fatalf("resolve output path: %v", err)
	}

	items, err := parseChecklist(checklistAbs)
	if err != nil {
		fatalf("parse checklist: %v", err)
	}

	groups := buildKeywordGroups(items)

	matches, err := findMatches(rootAbs, checklistAbs, outAbs, items, groups)
	if err != nil {
		fatalf("scan repository: %v", err)
	}

	if err := writeOutput(outAbs, checklistAbs, items, matches); err != nil {
		fatalf("write output: %v", err)
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "audit_proofs:", msg)
	os.Exit(1)
}

func fatalf(format string, args ...any) {
	fatal(fmt.Sprintf(format, args...))
}

var bulletRegex = regexp.MustCompile(`^\s*[-*+]\s*✅\s*(.*)$`)
var commentRegex = regexp.MustCompile(`<!--.*?-->`)

func parseChecklist(path string) ([]checklistItem, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1024*1024)

	var items []checklistItem
	lineNumber := 0

	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()

		if !strings.Contains(line, "✅") {
			continue
		}

		cleaned := commentRegex.ReplaceAllString(line, "")
		cleaned = strings.TrimSpace(cleaned)

		if cleaned == "" {
			continue
		}

		matches := bulletRegex.FindStringSubmatch(cleaned)
		if len(matches) != 2 {
			continue
		}

		title := strings.TrimSpace(matches[1])
		if title == "" {
			continue
		}

		comment := extractProofComment(line)
		keywords, limit, err := parseProofConfig(comment)
		if err != nil {
			fmt.Fprintf(os.Stderr, "audit_proofs: %s:%d: %v\n", filepath.Base(path), lineNumber, err)
		}

		item := checklistItem{
			Title:    title,
			Keywords: keywords,
			Limit:    limit,
		}
		items = append(items, item)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return items, nil
}

func extractProofComment(line string) string {
	start := strings.Index(line, "<!--")
	if start == -1 {
		return ""
	}
	end := strings.Index(line[start:], "-->")
	if end == -1 {
		return ""
	}
	comment := strings.TrimSpace(line[start+4 : start+end])
	trimmed := strings.TrimSpace(comment)
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "proofs:") {
		return comment
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, `"`) {
		return comment
	}
	return ""
}

func parseProofConfig(comment string) ([]string, int, error) {
	limit := defaultLimit
	if limit < minLimit {
		limit = minLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	if comment == "" {
		return nil, limit, nil
	}

	lower := strings.ToLower(comment)
	if strings.HasPrefix(lower, "proofs:") {
		comment = strings.TrimSpace(comment[len("proofs:"):])
	}

	trimmed := strings.TrimSpace(comment)
	if trimmed == "" {
		return nil, limit, nil
	}

	// Attempt JSON parsing first
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, `"`) {
		if keywords, parsedLimit, err := parseProofJSON(trimmed, limit); err == nil {
			return keywords, parsedLimit, nil
		}
	}

	tokens := splitTokens(trimmed)
	var keywords []string

	for _, token := range tokens {
		if token == "" {
			continue
		}
		if strings.Contains(token, "=") {
			parts := strings.SplitN(token, "=", 2)
			key := strings.ToLower(strings.TrimSpace(parts[0]))
			value := strings.TrimSpace(parts[1])
			switch key {
			case "limit", "max", "max_results":
				if v, err := parseLimit(value); err == nil {
					limit = v
				} else {
					return keywords, limit, fmt.Errorf("invalid limit value %q", value)
				}
			case "keyword", "kw":
				if value != "" {
					keywords = append(keywords, value)
				}
			default:
				// ignore other keys
			}
			continue
		}
		keywords = append(keywords, token)
	}

	return keywords, clampLimit(limit), nil
}

func parseProofJSON(raw string, fallback int) ([]string, int, error) {
	var data any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, fallback, err
	}

	limit := fallback
	var keywords []string

	switch typed := data.(type) {
	case []any:
		keywords = appendKeywords(keywords, typed)
	case map[string]any:
		for key, value := range typed {
			lower := strings.ToLower(key)
			switch lower {
			case "keywords", "keys", "terms":
				switch vv := value.(type) {
				case []any:
					keywords = appendKeywords(keywords, vv)
				case string:
					keywords = append(keywords, strings.TrimSpace(vv))
				}
			case "limit", "max", "max_results":
				switch vv := value.(type) {
				case float64:
					limit = clampLimit(int(vv))
				case string:
					if v, err := parseLimit(vv); err == nil {
						limit = v
					}
				}
			}
		}
	case string:
		if strings.TrimSpace(typed) != "" {
			keywords = append(keywords, strings.TrimSpace(typed))
		}
	default:
		return nil, fallback, errors.New("unsupported proofs comment format")
	}

	return keywords, limit, nil
}

func appendKeywords(dst []string, values []any) []string {
	for _, v := range values {
		switch vv := v.(type) {
		case string:
			val := strings.TrimSpace(vv)
			if val != "" {
				dst = append(dst, val)
			}
		}
	}
	return dst
}

func splitTokens(input string) []string {
	f := func(r rune) bool {
		switch r {
		case ',', ';':
			return true
		default:
			return false
		}
	}
	pieces := strings.FieldsFunc(input, f)
	for i := range pieces {
		pieces[i] = strings.TrimSpace(pieces[i])
	}
	return pieces
}

func parseLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return clampLimit(defaultLimit), nil
	}
	value, err := strconvAtoi(raw)
	if err != nil {
		return 0, err
	}
	return clampLimit(value), nil
}

func clampLimit(limit int) int {
	if limit < minLimit {
		limit = minLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return limit
}

func buildKeywordGroups(items []checklistItem) []keywordGroup {
	groups := make(map[string]*keywordGroup)

	for idx, item := range items {
		for order, kw := range item.Keywords {
			norm := strings.ToLower(strings.TrimSpace(kw))
			if norm == "" {
				continue
			}
			group, ok := groups[norm]
			if !ok {
				group = &keywordGroup{Norm: norm}
				groups[norm] = group
			}
			group.Refs = append(group.Refs, keywordRef{ItemIndex: idx, Keyword: kw, Norm: norm, Order: order})
		}
	}

	out := make([]keywordGroup, 0, len(groups))
	for _, g := range groups {
		// Ensure deterministic order for refs as well
		sort.SliceStable(g.Refs, func(i, j int) bool {
			if g.Refs[i].ItemIndex == g.Refs[j].ItemIndex {
				return g.Refs[i].Order < g.Refs[j].Order
			}
			return g.Refs[i].ItemIndex < g.Refs[j].ItemIndex
		})
		out = append(out, *g)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Norm < out[j].Norm
	})

	return out
}

func findMatches(root, checklist, out string, items []checklistItem, groups []keywordGroup) ([][]match, error) {
	matches := make([][]match, len(items))
	seen := make([]map[string]struct{}, len(items))

	if len(groups) == 0 {
		return matches, nil
	}

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			name := d.Name()
			if skipDir(name) {
				return filepath.SkipDir
			}
			return nil
		}

		if !d.Type().IsRegular() {
			return nil
		}

		if samePath(path, checklist) || samePath(path, out) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > maxFileSize {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}

		rel = filepath.ToSlash(rel)

		// Binary detection on the first chunk
		probe := make([]byte, 1024)
		n, err := file.Read(probe)
		if err != nil && err != io.EOF {
			file.Close()
			return nil
		}
		if isBinaryBytes(probe[:n]) {
			file.Close()
			return nil
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			file.Close()
			return nil
		}

		scanner := bufio.NewScanner(file)
		buf := make([]byte, 4096)
		scanner.Buffer(buf, 1024*1024)
		lineNumber := 0

		for scanner.Scan() {
			lineNumber++
			line := scanner.Text()
			lowerLine := strings.ToLower(line)

			for _, group := range groups {
				// Check if any reference within this group still needs matches
				needsMatch := false
				for _, ref := range group.Refs {
					if len(items[ref.ItemIndex].Keywords) == 0 {
						continue
					}
					if len(matches[ref.ItemIndex]) < items[ref.ItemIndex].Limit {
						needsMatch = true
						break
					}
				}
				if !needsMatch {
					continue
				}

				if !strings.Contains(lowerLine, group.Norm) {
					continue
				}

				for _, ref := range group.Refs {
					if len(matches[ref.ItemIndex]) >= items[ref.ItemIndex].Limit {
						continue
					}
					if !strings.Contains(lowerLine, ref.Norm) {
						continue
					}

					key := fmt.Sprintf("%s:%d", rel, lineNumber)
					if seen[ref.ItemIndex] == nil {
						seen[ref.ItemIndex] = make(map[string]struct{})
					}
					if _, ok := seen[ref.ItemIndex][key]; ok {
						continue
					}

					snippet := sanitizeSnippet(line)
					matches[ref.ItemIndex] = append(matches[ref.ItemIndex], match{
						Keyword: ref.Keyword,
						Path:    rel,
						Line:    lineNumber,
						Snippet: snippet,
						Order:   ref.Order,
					})
					seen[ref.ItemIndex][key] = struct{}{}
				}
			}
		}

		if err := scanner.Err(); err != nil {
			file.Close()
			return err
		}

		file.Close()

		return nil
	})
	if walkErr != nil {
		return matches, walkErr
	}

	for idx := range matches {
		if len(matches[idx]) == 0 {
			continue
		}
		sort.SliceStable(matches[idx], func(i, j int) bool {
			if matches[idx][i].Order != matches[idx][j].Order {
				return matches[idx][i].Order < matches[idx][j].Order
			}
			if matches[idx][i].Path != matches[idx][j].Path {
				return matches[idx][i].Path < matches[idx][j].Path
			}
			if matches[idx][i].Line != matches[idx][j].Line {
				return matches[idx][i].Line < matches[idx][j].Line
			}
			return matches[idx][i].Keyword < matches[idx][j].Keyword
		})

		if len(matches[idx]) > items[idx].Limit {
			matches[idx] = matches[idx][:items[idx].Limit]
		}
	}

	return matches, nil
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aClean := filepath.Clean(a)
	bClean := filepath.Clean(b)
	return aClean == bClean
}

func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", "build", "out", "tmp", "artifacts", "coverage", "pkg", ".idea", ".vscode":
		return true
	default:
		return strings.HasPrefix(name, ".cache")
	}
}

func isBinaryBytes(buf []byte) bool {
	for _, b := range buf {
		if b == 0 {
			return true
		}
	}
	return false
}

func sanitizeSnippet(line string) string {
	snippet := strings.TrimSpace(line)
	if snippet == "" {
		snippet = "(empty line)"
	}
	snippet = strings.ReplaceAll(snippet, "`", "'")

	const maxRune = 160
	if len([]rune(snippet)) > maxRune {
		runes := []rune(snippet)
		snippet = string(runes[:maxRune]) + "…"
	}

	return snippet
}

func writeOutput(outPath, checklist string, items []checklistItem, matches [][]match) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}

	file, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	defer writer.Flush()

	header := fmt.Sprintf("<!-- Generated by audit_proofs.go on %s -->\n", time.Now().UTC().Format(time.RFC3339))
	if _, err := writer.WriteString(header); err != nil {
		return err
	}
	relChecklist := checklist
	if rel, err := filepath.Rel(filepath.Dir(outPath), checklist); err == nil {
		relChecklist = rel
	}
	summary := fmt.Sprintf("<!-- Checklist: %s -->\n\n", filepath.ToSlash(relChecklist))
	if _, err := writer.WriteString(summary); err != nil {
		return err
	}

	for idx, item := range items {
		if _, err := writer.WriteString("## " + item.Title + "\n"); err != nil {
			return err
		}

		currentMatches := matches[idx]

		switch {
		case len(item.Keywords) == 0:
			if _, err := writer.WriteString("- _no proofs keywords configured_\n\n"); err != nil {
				return err
			}
			continue
		case len(currentMatches) == 0:
			if _, err := writer.WriteString("- _no matches found for configured keywords_\n\n"); err != nil {
				return err
			}
			continue
		}

		for _, m := range currentMatches {
			line := fmt.Sprintf("- `%s` · `%s:%d` — %s\n", m.Keyword, m.Path, m.Line, m.Snippet)
			if _, err := writer.WriteString(line); err != nil {
				return err
			}
		}
		if _, err := writer.WriteString("\n"); err != nil {
			return err
		}
	}

	return nil
}

// strconvAtoi mirrors strconv.Atoi but avoids importing strconv just for this helper.
func strconvAtoi(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty string")
	}
	neg := false
	if strings.HasPrefix(s, "+") {
		s = s[1:]
	} else if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	if s == "" {
		return 0, errors.New("invalid syntax")
	}
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid syntax")
		}
		n = n*10 + int(r-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}
