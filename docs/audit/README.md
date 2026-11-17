# English Security Audit

The English audit is a static, text-pattern scan of the repository. It is a set of regular-expression and Go-AST heuristics (`tools/audit/english/checks.go`), not a data-flow analysis, so expect false positives and false negatives. It complements, and does not replace, the tooling described in [static-analysis.md](./static-analysis.md).

## Running locally

Run from the repository root (the scanner walks the current directory):

```bash
make audit:english
```

The target runs `scripts/run_english_audit.sh`, which runs `go run ./cmd/english-audit -out docs/audit/english-latest.md`, adding `-govulncheck` only when a `govulncheck` binary is on `PATH`. The report is written to `docs/audit/english-latest.md`; that file is not checked in.

### Flags

You can run the binary directly:

```bash
go run ./cmd/english-audit -out /tmp/english-report.md -include-tests -strict -govulncheck
```

- `-out` – report destination (default `docs/audit/english-latest.md`).
- `-include-tests` – include `_test.go` files in the scan (default off).
- `-strict` – exit with status 1 if any finding has severity `error` (default off).
- `-govulncheck` – also run `govulncheck ./...` (default off). If the binary is not installed the report gets an informational "govulncheck unavailable" finding instead.

### What is scanned

- Directories named `vendor`, `.git`, `node_modules`, `artifacts`, `build`, `bin` and `dist` are skipped.
- Files whose names start with `.` are skipped unless the name ends in `.env`.
- `_test.go` files are skipped unless `-include-tests` is set.

## Themes

Findings are grouped into nine themes. The severity a check assigns is fixed in code.

| Theme | What the checks look for | Severities used |
| --- | --- | --- |
| Transport & Auth | `AllowInsecure = true` in Go, TOML and YAML; `0.0.0.0` on lines containing `http`; `Authorization` on logging lines; bearer-token formatting; static bearer tokens; JWT disable/skip-verify patterns | error, warn |
| Secrets & Logs | committed `.key`/`.pem` files; private-key, mnemonic/seed/passphrase or `x-api-key` text on logging lines | error |
| Replay & Nonce | files whose path contains `pos`, `claim` or `payment` that never mention nonce, ttl, expiry or antireplay | error |
| Funds Safety | in `core/` state files, `big.NewInt(0).Sub` without a nearby `>=`/`Cmp`, and lines mentioning fee/znhb without `>=` | warn |
| Fee / Free-tier Rollover | fee files with no `reset`, `rollover` or `snapshot` text | warn |
| Pauses & Governance | transfer/staking/swap/gateway/bridge files that never mention `pause` | warn |
| DoS & QoS | server/handler files under `gateway/`, `services/` or `mempool/` that never mention a rate limiter | warn |
| File Serving & Path Traversal | `http.FileServer`, `http.ServeFile`, `filepath.Join(... user ...)` | warn |
| External Dependencies | `govulncheck` output lines; a missing `govulncheck` binary | warn, info |

## Report format

Each report (`# English Security Audit`) contains:

1. **Summary** – one line per theme with a status marker: ✅ (only info or no findings), ⚠️ (at least one `warn`), ❌ (at least one `error`).
2. **Narrative** – a one-line description of what the theme covers.
3. **Findings** – for each finding: title and severity, description, a **Proofs** block with the `file:line` and a three-line snippet, and a **Remediation** line. A theme with no findings prints "No issues detected."

Snippets are passed through a redactor before they are written. It replaces the value after `Authorization: Bearer `, after `x-api-key =`, and after `mnemonic` or `seed` followed by `:` or `=` (`sanitizeSnippet` in `tools/audit/english/report.go`). Other secret formats are not redacted.

## Interpreting results

- **❌ (error)** – a check the tool treats as high severity; `-strict` fails on these.
- **⚠️ (warn)** – a weaker signal or a missing-control heuristic.
- **✅ (info)** – no `warn` or `error` findings for that theme.

Because the checks are textual, review each proof in context before acting on it, and re-run the audit after a fix to confirm the finding is gone.
