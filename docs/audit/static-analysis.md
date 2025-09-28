# Static Analysis Guide

This playbook explains how to run and interpret static-analysis tooling across nhbchain repositories. Use it to produce repeatable reports and to prioritize actionable findings.

## Toolchain

| Tool | Location | Purpose |
| --- | --- | --- |
| `golangci-lint` | `make lint` | Go code linting, vetting, and static checks across services. |
| `buf lint` | `proto/` | Protocol buffer linting and breaking change detection. |
| `npm run lint` | `gateway/`, `services/` front-end packages | Ensures TypeScript/JavaScript adherence to project style and catches unsafe patterns. |
| `semgrep` | Repository root | Searches for vulnerability patterns beyond language-specific linters. |

## Running the pipeline

1. **Bootstrap dependencies.** Run `make deps` to install Go linters and ensure Node packages are present where required.
2. **Execute language linters.**
   - For Go services, run `make lint` from the repository root.
   - For protocol buffers, run `buf lint` within the `proto/` directory.
   - For TypeScript packages, run `npm run lint` inside each package (`gateway/`, `services/web/`, etc.).
3. **Semgrep sweep.** Execute `semgrep ci --config=auto` from the root to aggregate rule packs. Capture the SARIF output for archival.
4. **Record suppressions.** Any rule suppressions must be justified in the findings tracker with a link to the code and the rationale.

## Triage workflow

- **Categorize findings** as blocker, high, medium, low, or informational based on impact and exploitability.
- **Validate reachability.** Confirm whether flagged code is reachable in production deployments; false positives should be documented.
- **Check remediation status.** Track each issue in the audit board with an owner, fix version, and verification notes.
- **Re-run linters** after fixes to confirm the findings no longer appear and to catch regressions.

## Reporting

- Export linter logs (`make lint`, `buf lint`, package-specific linting) and attach them to the audit artifact bundle.
- Summarize outstanding issues in the static-analysis section of the final audit report, including remediation ETAs and compensating controls.
- Highlight systemic themes (for example, missing input validation or unchecked errors) that require broader engineering action.

## Exit criteria

- No blocker or high findings remain untriaged without an approved mitigation plan.
- All suppressions are reviewed and documented.
- Final report contains links to raw logs and tracking tickets.
