#!/usr/bin/env bash
set -euo pipefail

ARGS=("-out" "docs/audit/english-latest.md")

if command -v govulncheck >/dev/null 2>&1; then
  ARGS+=("-govulncheck")
fi

go run ./cmd/english-audit "${ARGS[@]}"

echo "Report written to docs/audit/english-latest.md"
