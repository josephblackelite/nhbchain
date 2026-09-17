package english

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// Options control the English audit execution.
type Options struct {
	OutPath      string
	Strict       bool
	IncludeTests bool
	Govulncheck  bool
	Stdout       io.Writer
	Stderr       io.Writer
}

// Run executes the audit and writes the report to OutPath.
func Run(opts Options) error {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	files, err := scanRepository(opts.IncludeTests)
	if err != nil {
		return fmt.Errorf("scan repository: %w", err)
	}

	findings := runChecks(files)
	if opts.Govulncheck {
		govFindings := runGovulncheck()
		findings = mergeGovuln(findings, govFindings)
	}

	report := buildReport(findings)
	if err := os.MkdirAll(filepathDir(opts.OutPath), 0o755); err != nil {
		return fmt.Errorf("ensure output dir: %w", err)
	}
	if err := os.WriteFile(opts.OutPath, report, 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}

	fmt.Fprintf(opts.Stdout, "english audit written to %s\n", opts.OutPath)

	if opts.Strict {
		for _, f := range findings {
			if f.Severity == SeverityError {
				return errors.New("strict mode: high severity findings present")
			}
		}
	}

	return nil
}

func buildReport(findings []Finding) []byte {
	var buf bytes.Buffer
	buf.WriteString("# English Security Audit\n\n")
	themes := map[Theme][]Finding{}
	for _, finding := range findings {
		themes[finding.Theme] = append(themes[finding.Theme], finding)
	}

	themeOrder := []Theme{ThemeTransport, ThemeSecrets, ThemeReplay, ThemeFunds, ThemeFees, ThemePauses, ThemeDoS, ThemeFileServe, ThemeDeps}

	buf.WriteString("## Summary\n\n")
	for _, theme := range themeOrder {
		status := themeStatus(themes[theme])
		buf.WriteString(fmt.Sprintf("- %s %s\n", statusEmoji(status), theme))
	}
	buf.WriteString("\n")

	for _, theme := range themeOrder {
		buf.WriteString(fmt.Sprintf("## %s\n\n", theme))
		buf.WriteString(themeNarrative(theme))
		buf.WriteString("\n\n")
		if len(themes[theme]) == 0 {
			buf.WriteString("No issues detected.\n\n")
			continue
		}
		sortFindings(themes[theme])
		for _, f := range themes[theme] {
			buf.WriteString(fmt.Sprintf("### %s (%s)\n\n", f.Title, strings.ToUpper(string(f.Severity))))
			buf.WriteString(f.Description + "\n\n")
			buf.WriteString("**Proofs:**\n\n")
			buf.WriteString(fmt.Sprintf("- `%s:%d`\n\n````\n%s\n````\n\n", f.Path, f.Line, sanitizeSnippet(strings.TrimSpace(f.Snippet))))
			buf.WriteString("**Remediation:**\n\n")
			buf.WriteString("- " + f.Remediation + "\n\n")
		}
	}
	return buf.Bytes()
}

func themeStatus(findings []Finding) Severity {
	status := SeverityInfo
	for _, f := range findings {
		if f.Severity == SeverityError {
			return SeverityError
		}
		if f.Severity == SeverityWarn {
			status = SeverityWarn
		}
	}
	return status
}

func statusEmoji(sev Severity) string {
	switch sev {
	case SeverityError:
		return "❌"
	case SeverityWarn:
		return "⚠️"
	default:
		return "✅"
	}
}

func themeNarrative(theme Theme) string {
	switch theme {
	case ThemeTransport:
		return "Assess TLS coverage, RPC authentication, and leakage of credentials in logs."
	case ThemeSecrets:
		return "Ensure secrets are stored outside the repo and redacted from telemetry."
	case ThemeReplay:
		return "Transactions must enforce nonce/expiry semantics to prevent replay."
	case ThemeFunds:
		return "Fund movements should guard against underflow and apply debits before credits."
	case ThemeFees:
		return "Monthly fee windows require rollover/reset to prevent unbounded accumulation."
	case ThemePauses:
		return "Governance pause switches should gate transfers, staking, and swaps across RPC layers."
	case ThemeDoS:
		return "Gateways and mempool must enforce rate limits and queue caps to mitigate DoS."
	case ThemeFileServe:
		return "File handlers need strict path validation to avoid traversal."
	case ThemeDeps:
		return "Track upstream CVEs; upgrade or mitigate when govulncheck reports issues."
	default:
		return ""
	}
}

func sortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Severity != fs[j].Severity {
			return severityRank(fs[i].Severity) > severityRank(fs[j].Severity)
		}
		if fs[i].Path != fs[j].Path {
			return fs[i].Path < fs[j].Path
		}
		return fs[i].Line < fs[j].Line
	})
}

func severityRank(s Severity) int {
	switch s {
	case SeverityError:
		return 3
	case SeverityWarn:
		return 2
	default:
		return 1
	}
}

func filepathDir(path string) string {
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		return path[:idx]
	}
	return "."
}

func runGovulncheck() []Finding {
	cmd := execCommand("govulncheck", "./...")
	output, err := cmd.CombinedOutput()
	if err != nil {
		if isNotFound(err) {
			return []Finding{{
				Theme:       ThemeDeps,
				Severity:    SeverityInfo,
				Path:        "govulncheck",
				Line:        0,
				Snippet:     "govulncheck binary not available",
				Title:       "govulncheck unavailable",
				Description: "govulncheck not installed; skipped dependency vulnerability scan",
				Remediation: "Install golang.org/x/vuln/cmd/govulncheck to enable dependency scanning.",
			}}
		}
		// command returned exit code with findings, continue parsing output anyway
	}
	if len(output) == 0 {
		return nil
	}
	return checkGovuln(output)
}

var execCommand = func(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.Error
	if errors.As(err, &exitErr) && exitErr.Err == exec.ErrNotFound {
		return true
	}
	return false
}

var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)([^\s]+)`),
	regexp.MustCompile(`(?i)(x-api-key\s*=\s*)([^'"\s]+)`),
	regexp.MustCompile(`(?i)(mnemonic|seed)(\s*[:=]\s*)([^'"\s]+)`),
}

func sanitizeSnippet(snippet string) string {
	redacted := snippet
	for _, re := range sensitivePatterns {
		redacted = re.ReplaceAllString(redacted, "$1<redacted>")
	}
	return redacted
}
