# English Security Audit

The English audit is a lightweight static review that highlights common risk areas in plain English. It does **not** expose secrets and redacts sensitive headers in its report.

## Running locally

```bash
make audit:english
```

The target runs `scripts/run_english_audit.sh`, which wraps `go run ./cmd/english-audit`. The tool walks the repository (respecting build and vendor ignores), executes the configured checks, and writes the latest report to `docs/audit/english-latest.md`.

### Useful flags

You can run the binary directly for more control:

```bash
go run ./cmd/english-audit -out /tmp/english-report.md -include-tests -strict -govulncheck
```

- `-out` – change the report destination.
- `-include-tests` – include `_test.go` files in the scan.
- `-strict` – exit with a non-zero status if any high-severity (`error`) findings exist.
- `-govulncheck` – run [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) if the binary is available.

## Report format

Each report contains:

1. **Summary** – theme-by-theme status with ✅/⚠️/❌.
2. **Narrative** – human-readable context about what the theme protects.
3. **Proofs** – explicit `file:line` references with trimmed snippets for every finding.
4. **Remediation** – concrete next steps or references to upstream documentation.

The tool intentionally avoids printing secret material. Known sensitive headers or tokens are redacted before inclusion in the report snippets.

## Interpreting results

- **❌ (error)** – immediate security or fund-loss risk. Address before shipping.
- **⚠️ (warn)** – medium-risk or missing defense-in-depth control. Schedule remediation.
- **✅ (info)** – no issues detected for that theme.

Re-run the audit after applying fixes to ensure the findings disappear and to archive the updated `docs/audit/english-latest.md` for review.
