package english

import (
	"path/filepath"
	"strings"
)

// Severity enumerates the impact of a finding.
type Severity string

const (
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// Theme enumerates audit categories.
type Theme string

const (
	ThemeTransport Theme = "Transport & Auth"
	ThemeSecrets   Theme = "Secrets & Logs"
	ThemeReplay    Theme = "Replay & Nonce"
	ThemeFunds     Theme = "Funds Safety"
	ThemeFees      Theme = "Fee / Free-tier Rollover"
	ThemePauses    Theme = "Pauses & Governance"
	ThemeDoS       Theme = "DoS & QoS"
	ThemeFileServe Theme = "File Serving & Path Traversal"
	ThemeDeps      Theme = "External Dependencies"
)

// Finding represents an issue detected by the audit.
type Finding struct {
	Theme       Theme
	Severity    Severity
	Path        string
	Line        int
	Snippet     string
	Title       string
	Description string
	Remediation string
}

func runChecks(files []FileData) []Finding {
	var findings []Finding
	findings = append(findings, checkTransport(files)...)
	findings = append(findings, checkSecrets(files)...)
	findings = append(findings, checkReplay(files)...)
	findings = append(findings, checkFunds(files)...)
	findings = append(findings, checkFees(files)...)
	findings = append(findings, checkPauses(files)...)
	findings = append(findings, checkDoS(files)...)
	findings = append(findings, checkFileServing(files)...)
	return findings
}

func checkTransport(files []FileData) []Finding {
	var findings []Finding
	for _, file := range files {
		if strings.HasSuffix(file.Path, ".go") {
			for idx, line := range file.Lines {
				if reAllowInsecure.MatchString(line) {
					findings = append(findings, Finding{
						Theme:       ThemeTransport,
						Severity:    SeverityError,
						Path:        file.Path,
						Line:        idx + 1,
						Snippet:     file.snippetAround(idx),
						Title:       "Insecure transport enabled",
						Description: "AllowInsecure=true exposes RPC traffic over plaintext",
						Remediation: "Require TLS by disabling AllowInsecure or guarding with loopback-only binds.",
					})
				}
				if reInsecureBind.MatchString(line) && strings.Contains(line, "http") {
					findings = append(findings, Finding{
						Theme:       ThemeTransport,
						Severity:    SeverityWarn,
						Path:        file.Path,
						Line:        idx + 1,
						Snippet:     file.snippetAround(idx),
						Title:       "Service binding to 0.0.0.0",
						Description: "Binding to 0.0.0.0 without TLS increases attack surface",
						Remediation: "Restrict bind address or enforce TLS certificates.",
					})
				}
				if reAuthorizationLog.MatchString(line) && (looksLikeLogCall(file.Lines, idx) || hasLogCallAt(file, idx+1)) {
					findings = append(findings, Finding{
						Theme:       ThemeTransport,
						Severity:    SeverityError,
						Path:        file.Path,
						Line:        idx + 1,
						Snippet:     file.snippetAround(idx),
						Title:       "Authorization header logged",
						Description: "Authorization values must never be printed in logs.",
						Remediation: "Redact Authorization header before logging or remove the log entry.",
					})
				}
				if reBearerToken.MatchString(line) {
					findings = append(findings, Finding{
						Theme:       ThemeTransport,
						Severity:    SeverityWarn,
						Path:        file.Path,
						Line:        idx + 1,
						Snippet:     file.snippetAround(idx),
						Title:       "Authorization bearer formatting",
						Description: "Authorization bearer tokens should be redacted rather than formatted into logs.",
						Remediation: "Avoid formatting bearer tokens directly; pass redacted placeholders instead.",
					})
				}
				if strings.Contains(line, "Authorization: Bearer") && !strings.Contains(line, "%s") {
					findings = append(findings, Finding{
						Theme:       ThemeTransport,
						Severity:    SeverityWarn,
						Path:        file.Path,
						Line:        idx + 1,
						Snippet:     file.snippetAround(idx),
						Title:       "Static bearer token detected",
						Description: "Static Authorization bearer tokens should rotate or rely on JWT with expiry.",
						Remediation: "Replace static tokens with short-lived JWTs or signed challenges.",
					})
				}
			}
		}
		if strings.HasSuffix(file.Path, ".toml") || strings.HasSuffix(file.Path, ".yaml") || strings.HasSuffix(file.Path, ".yml") {
			if strings.Contains(strings.ToLower(file.Content), "allowinsecure = true") {
				findings = append(findings, Finding{
					Theme:       ThemeTransport,
					Severity:    SeverityError,
					Path:        file.Path,
					Line:        1,
					Snippet:     file.line(0),
					Title:       "Production config allows insecure transport",
					Description: "Configuration permits plaintext transport",
					Remediation: "Set allowinsecure=false in production configs and document TLS setup.",
				})
			}
		}
		if strings.HasSuffix(file.Path, ".go") && reJWTDisable.MatchString(strings.ToLower(file.Content)) {
			findings = append(findings, Finding{
				Theme:       ThemeTransport,
				Severity:    SeverityWarn,
				Path:        file.Path,
				Line:        1,
				Snippet:     file.line(0),
				Title:       "JWT verification disabled",
				Description: "JWT configuration skips verification which weakens authn.",
				Remediation: "Enable verification and enforce signature/key rotation for JWT tokens.",
			})
		}
	}
	return findings
}

func checkSecrets(files []FileData) []Finding {
	var findings []Finding
	for _, file := range files {
		lower := strings.ToLower(file.Path)
		if strings.Contains(lower, "/testdata/") || strings.Contains(lower, "_test.") {
			continue
		}
		if strings.HasSuffix(file.Path, ".key") || strings.HasSuffix(file.Path, ".pem") {
			findings = append(findings, Finding{
				Theme:       ThemeSecrets,
				Severity:    SeverityError,
				Path:        file.Path,
				Line:        1,
				Snippet:     "binary/secret material",
				Title:       "Private key material committed",
				Description: "Key files are present in the repository.",
				Remediation: "Remove the key from the repo, rotate credentials, and load via secure secret manager.",
			})
			continue
		}
		if strings.HasSuffix(file.Path, ".go") {
			for idx, line := range file.Lines {
				if looksLikeLogCall(file.Lines, idx) || hasLogCallAt(file, idx+1) {
					lowerLine := strings.ToLower(line)
					if rePrivateKey.MatchString(line) || reMnemonic.MatchString(line) || strings.Contains(lowerLine, "x-api-key") {
						findings = append(findings, Finding{
							Theme:       ThemeSecrets,
							Severity:    SeverityError,
							Path:        file.Path,
							Line:        idx + 1,
							Snippet:     file.snippetAround(idx),
							Title:       "Sensitive secret logged",
							Description: "Secrets should not appear in log statements.",
							Remediation: "Remove secret from logs and use redaction helpers.",
						})
					}
				}
			}
		}
	}
	return findings
}

func checkReplay(files []FileData) []Finding {
	var findings []Finding
	for _, file := range files {
		lowerPath := strings.ToLower(file.Path)
		if !strings.Contains(lowerPath, "pos") && !strings.Contains(lowerPath, "claim") && !strings.Contains(lowerPath, "payment") {
			continue
		}
		if !reNonce.MatchString(file.Content) {
			findings = append(findings, Finding{
				Theme:       ThemeReplay,
				Severity:    SeverityError,
				Path:        file.Path,
				Line:        1,
				Snippet:     file.line(0),
				Title:       "Missing nonce/ttl validation",
				Description: "Critical transaction paths should enforce nonce or expiry checks.",
				Remediation: "Add nonce + TTL enforcement and tests for replay of POS intents and claimables.",
			})
		}
	}
	return findings
}

func checkFunds(files []FileData) []Finding {
	var findings []Finding
	for _, file := range files {
		if !strings.HasPrefix(file.Path, "core/") {
			continue
		}
		if !strings.Contains(file.Path, "state") && !strings.Contains(file.Path, "state_transition") {
			continue
		}
		for idx, line := range file.Lines {
			if reBigIntSub.MatchString(line) && !lineHasCompareGuard(file.Lines, idx) {
				findings = append(findings, Finding{
					Theme:       ThemeFunds,
					Severity:    SeverityWarn,
					Path:        file.Path,
					Line:        idx + 1,
					Snippet:     file.snippetAround(idx),
					Title:       "Potential unchecked balance subtraction",
					Description: "Balance subtractions should guard against underflow before mutating state.",
					Remediation: "Check balance >= debit before subtraction and add tests covering zero/insufficient cases.",
				})
			}
			lowerLine := strings.ToLower(line)
			if reFeeBranch.MatchString(lowerLine) && !strings.Contains(lowerLine, ">=") {
				findings = append(findings, Finding{
					Theme:       ThemeFunds,
					Severity:    SeverityWarn,
					Path:        file.Path,
					Line:        idx + 1,
					Snippet:     file.snippetAround(idx),
					Title:       "Fee deduction missing balance guard",
					Description: "Fee deduction should assert sender balance >= fee prior to mutation.",
					Remediation: "Introduce guard and fail the transaction if the account cannot pay the fee.",
				})
			}
		}
	}
	return findings
}

func checkFees(files []FileData) []Finding {
	var findings []Finding
	for _, file := range files {
		lower := strings.ToLower(file.Path)
		if !strings.Contains(lower, "fee") {
			continue
		}
		if !(strings.HasSuffix(lower, "fees.go") || strings.HasSuffix(lower, "fees_test.go") || strings.Contains(lower, "/fees/")) {
			continue
		}
		lowerContent := strings.ToLower(file.Content)
		if !strings.Contains(lowerContent, "reset") && !strings.Contains(lowerContent, "rollover") && !strings.Contains(lowerContent, "snapshot") {
			findings = append(findings, Finding{
				Theme:       ThemeFees,
				Severity:    SeverityWarn,
				Path:        file.Path,
				Line:        1,
				Snippet:     file.line(0),
				Title:       "Missing free-tier rollover logic",
				Description: "Fee counters lack explicit rollover/reset handling.",
				Remediation: "Introduce monthly window keys and reset tasks for fee counters.",
			})
		}
	}
	return findings
}

func checkPauses(files []FileData) []Finding {
	var findings []Finding
	for _, file := range files {
		lower := strings.ToLower(file.Path)
		if !(strings.Contains(lower, "transfer") || strings.Contains(lower, "staking") || strings.Contains(lower, "swap") || strings.Contains(lower, "gateway") || strings.Contains(lower, "bridge")) {
			continue
		}
		if !rePause.MatchString(strings.ToLower(file.Content)) {
			findings = append(findings, Finding{
				Theme:       ThemePauses,
				Severity:    SeverityWarn,
				Path:        file.Path,
				Line:        1,
				Snippet:     file.line(0),
				Title:       "Pause flag not referenced",
				Description: "Critical handlers should consult global pause/governance flags.",
				Remediation: "Wire pause toggles through handler and enforce gating before state mutations.",
			})
		}
	}
	return findings
}

func checkDoS(files []FileData) []Finding {
	var findings []Finding
	for _, file := range files {
		lower := strings.ToLower(file.Path)
		base := filepath.Base(lower)
		if !strings.Contains(lower, "gateway/") && !strings.Contains(lower, "services/") && !strings.Contains(lower, "mempool/") {
			continue
		}
		if !(strings.Contains(base, "server") || strings.Contains(base, "handler")) {
			continue
		}
		if !reRateLimit.MatchString(strings.ToLower(file.Content)) {
			findings = append(findings, Finding{
				Theme:       ThemeDoS,
				Severity:    SeverityWarn,
				Path:        file.Path,
				Line:        1,
				Snippet:     file.line(0),
				Title:       "Missing rate limiting",
				Description: "Handlers should enforce per-IP or per-key limits to avoid DoS.",
				Remediation: "Add middleware or queue caps to bound inbound load and document SLOs.",
			})
		}
	}
	return findings
}

func checkFileServing(files []FileData) []Finding {
	var findings []Finding
	for _, file := range files {
		if !strings.HasSuffix(file.Path, ".go") {
			continue
		}
		if reFileServer.MatchString(file.Content) {
			line := firstLineMatch(file, reFileServer)
			findings = append(findings, Finding{
				Theme:       ThemeFileServe,
				Severity:    SeverityWarn,
				Path:        file.Path,
				Line:        line,
				Snippet:     file.snippetAround(line - 1),
				Title:       "File server exposed without validation",
				Description: "Serving files directly can permit path traversal without sanitisation.",
				Remediation: "Validate requested paths, disallow .. segments, or serve vetted bundles only.",
			})
		}
		if rePathJoinUser.MatchString(file.Content) {
			line := firstLineMatch(file, rePathJoinUser)
			findings = append(findings, Finding{
				Theme:       ThemeFileServe,
				Severity:    SeverityWarn,
				Path:        file.Path,
				Line:        line,
				Snippet:     file.snippetAround(line - 1),
				Title:       "User-controlled path join",
				Description: "Joining user input into paths risks traversal and sandbox escape.",
				Remediation: "Normalise paths and enforce allow-lists before joining.",
			})
		}
	}
	return findings
}

func firstLineMatch(file FileData, re interface{ FindStringIndex(string) []int }) int {
	for idx, line := range file.Lines {
		if re.FindStringIndex(line) != nil {
			return idx + 1
		}
	}
	return 1
}

func looksLikeLogCall(lines []string, idx int) bool {
	line := strings.TrimSpace(lines[idx])
	if strings.HasPrefix(line, "log.") || strings.HasPrefix(line, "fmt.") {
		return true
	}
	if idx > 0 {
		prev := strings.TrimSpace(lines[idx-1])
		if strings.HasSuffix(prev, "log.Printf(") {
			return true
		}
	}
	return false
}

func hasLogCallAt(file FileData, line int) bool {
	for _, lc := range file.LogCalls {
		if lc.Line == line {
			return true
		}
	}
	return false
}

func lineHasCompareGuard(lines []string, idx int) bool {
	for i := idx; i >= 0 && idx-i < 4; i-- {
		if strings.Contains(lines[i], ">=") || strings.Contains(lines[i], "Cmp") {
			return true
		}
	}
	for i := idx; i < len(lines) && i-idx < 4; i++ {
		if strings.Contains(lines[i], ">=") || strings.Contains(lines[i], "Cmp") {
			return true
		}
	}
	return false
}

func checkGovuln(f []byte) []Finding {
	var findings []Finding
	matches := reGovulnFinding.FindAllSubmatch(f, -1)
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		path := string(m[1])
		msg := string(m[3])
		findings = append(findings, Finding{
			Theme:       ThemeDeps,
			Severity:    SeverityWarn,
			Path:        filepath.ToSlash(path),
			Line:        1,
			Snippet:     msg,
			Title:       "govulncheck finding",
			Description: msg,
			Remediation: "Review vulnerability, upgrade dependency, or document mitigation.",
		})
	}
	return findings
}

func mergeGovuln(findings []Finding, gov []Finding) []Finding {
	return append(findings, gov...)
}
