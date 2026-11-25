package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	validatorKeyLine      = regexp.MustCompile(`^\s*ValidatorKey\s*=\s*(.*)$`)
	validatorKeystoreLine = regexp.MustCompile(`^\s*ValidatorKeystorePath\s*=\s*(.*)$`)
)

// tomlStringValue reads a TOML string value (basic or literal) from the start of
// rest and ignores whatever follows it (a comment). ok is false when rest does
// not start with a string.
func tomlStringValue(rest string) (value string, ok bool) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", false
	}
	switch rest[0] {
	case '\'':
		end := strings.IndexByte(rest[1:], '\'')
		if end < 0 {
			return "", false
		}
		return rest[1 : 1+end], true
	case '"':
		for i := 1; i < len(rest); i++ {
			if rest[i] == '\\' {
				i++
				continue
			}
			if rest[i] == '"' {
				unquoted, err := strconv.Unquote(rest[:i+1])
				if err != nil {
					return "", false
				}
				return unquoted, true
			}
		}
	}
	return "", false
}

// isTableHeader reports whether the line opens a TOML table or an array of
// tables; everything after the first one is not a top-level key.
func isTableHeader(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "[")
}

// scanTopLevelKeys returns the value of the top-level ValidatorKey (empty when
// absent or empty) and whether a non-empty top-level ValidatorKeystorePath is
// set. It reads only the lines before the first table header and does not parse
// the rest of the file, so a value the whole-file decoder cannot take (a network
// id above the largest 64-bit signed integer, which config.Load quotes itself)
// cannot stop it.
func scanTopLevelKeys(text string) (legacyKey string, keystoreSet bool, err error) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if isTableHeader(line) {
			break
		}
		if m := validatorKeyLine.FindStringSubmatch(line); m != nil {
			value, ok := tomlStringValue(m[1])
			if !ok {
				return "", false, fmt.Errorf("the ValidatorKey value is not a quoted string")
			}
			legacyKey = value
			continue
		}
		if m := validatorKeystoreLine.FindStringSubmatch(line); m != nil {
			if value, ok := tomlStringValue(m[1]); ok && strings.TrimSpace(value) != "" {
				keystoreSet = true
			}
		}
	}
	return legacyKey, keystoreSet, nil
}

// replaceLegacyKeyLine turns the top-level ValidatorKey line into a
// ValidatorKeystorePath line and drops an empty top-level ValidatorKeystorePath
// line (two lines for one key would not load). Every other line, comments and
// line endings included, is returned unchanged.
func replaceLegacyKeyLine(text, keystorePath string) (string, error) {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	replaced := false
	inTables := false
	for _, line := range lines {
		body := strings.TrimRight(line, "\r")
		ending := line[len(body):]
		if !inTables && isTableHeader(body) {
			inTables = true
		}
		if !inTables {
			if validatorKeyLine.MatchString(body) {
				out = append(out, "ValidatorKeystorePath = "+strconv.Quote(keystorePath)+ending)
				replaced = true
				continue
			}
			if m := validatorKeystoreLine.FindStringSubmatch(body); m != nil {
				if value, ok := tomlStringValue(m[1]); ok && strings.TrimSpace(value) == "" {
					continue
				}
			}
		}
		out = append(out, line)
	}
	if !replaced {
		return "", fmt.Errorf("no top-level ValidatorKey line found to replace")
	}
	return strings.Join(out, "\n"), nil
}

// writeFileKeepingMode writes data to path through a temporary file in the same
// directory and a rename, so a failure never leaves a half-written config, and
// keeps the file's permission bits.
func writeFileKeepingMode(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
