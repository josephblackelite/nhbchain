package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type snippet struct {
	lang string
	file string
}

var linkRE = regexp.MustCompile(`!?\[[^\]]*\]\(([^)]+)\)`)

func main() {
	snippets := make([]snippet, 0)
	if err := filepath.WalkDir("docs", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		fileSnippets, err := processDoc(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		snippets = append(snippets, fileSnippets...)
		if err := checkLinks(path); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "docs verification failed: %v\n", err)
		os.Exit(1)
	}

	if err := buildSnippets(snippets); err != nil {
		fmt.Fprintf(os.Stderr, "docs verification failed: %v\n", err)
		os.Exit(1)
	}
}

func processDoc(path string) ([]snippet, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	snippets := make([]snippet, 0)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "<!-- embed:") || !strings.HasSuffix(line, "-->") {
			continue
		}
		embedPath := strings.TrimSuffix(strings.TrimPrefix(line, "<!-- embed:"), " -->")
		langLine, err := readLine(scanner)
		if err != nil {
			return nil, fmt.Errorf("embed %s missing code fence: %w", embedPath, err)
		}
		if !strings.HasPrefix(langLine, "```") {
			return nil, fmt.Errorf("embed %s expected code fence, got %q", embedPath, langLine)
		}
		lang := strings.TrimPrefix(langLine, "```")
		blockLines := make([]string, 0)
		closed := false
		for scanner.Scan() {
			text := scanner.Text()
			if text == "```" {
				closed = true
				break
			}
			blockLines = append(blockLines, text)
		}
		if scanner.Err() != nil {
			return nil, scanner.Err()
		}
		if !closed {
			return nil, fmt.Errorf("embed %s missing closing fence", embedPath)
		}
		expected, err := os.ReadFile(embedPath)
		if err != nil {
			return nil, fmt.Errorf("embed %s read: %w", embedPath, err)
		}
		normalizedDoc := normalizeBlock(blockLines)
		normalizedFile := normalizeBlock(strings.Split(strings.TrimRight(string(expected), "\n"), "\n"))
		if !bytes.Equal([]byte(strings.Join(normalizedDoc, "\n")), []byte(strings.Join(normalizedFile, "\n"))) {
			return nil, fmt.Errorf("embed %s out of sync", embedPath)
		}
		snippets = append(snippets, snippet{lang: lang, file: embedPath})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return snippets, nil
}

func normalizeBlock(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strings.TrimRight(line, "\r")
	}
	return out
}

func readLine(scanner *bufio.Scanner) (string, error) {
	if scanner.Scan() {
		return scanner.Text(), nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", errors.New("unexpected EOF")
}

func buildSnippets(snippets []snippet) error {
	goFiles := make([]string, 0)
	hasTS := false
	for _, snip := range snippets {
		switch strings.TrimSpace(snip.lang) {
		case "go":
			goFiles = append(goFiles, snip.file)
		case "ts", "typescript":
			hasTS = true
		}
	}
	for _, file := range goFiles {
		cmd := exec.Command("go", "build", "-o", os.DevNull, file)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("go build %s: %w", file, err)
		}
	}
	if hasTS {
		cmd := exec.Command("npx", "tsc", "--noEmit", "--project", "examples/docs/tsconfig.json")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("ts compile snippets: %w", err)
		}
	}
	return nil
}
func checkLinks(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	matches := linkRE.FindAllSubmatch(data, -1)
	for _, match := range matches {
		target := string(match[1])
		if target == "" {
			continue
		}
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
			strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") ||
			strings.HasPrefix(target, "tel:") {
			continue
		}
		if strings.HasPrefix(target, "data:") {
			continue
		}
		clean := target
		if idx := strings.IndexByte(clean, '#'); idx >= 0 {
			clean = clean[:idx]
		}
		if clean == "" {
			continue
		}
		if strings.HasPrefix(clean, "/") {
			// Treat as site-absolute and skip.
			continue
		}
		resolved := filepath.Clean(filepath.Join(dir, clean))
		if _, err := os.Stat(resolved); err != nil {
			return fmt.Errorf("broken relative link %q", target)
		}
	}
	return nil
}
