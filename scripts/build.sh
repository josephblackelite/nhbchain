#!/usr/bin/env bash
set -euo pipefail
GO_VERSION="${GO_VERSION:-1.23.0}"

if [[ -z "${GO_CMD:-}" ]]; then
  if command -v "go${GO_VERSION}" >/dev/null 2>&1; then
    GO_CMD="go${GO_VERSION}"
  else
    GO_CMD="go"
    if [[ -z "${GOTOOLCHAIN:-}" ]]; then
      export GOTOOLCHAIN="go${GO_VERSION}"
    fi
  fi
fi

export GOFLAGS="${GOFLAGS:--buildvcs=false}"

echo "[*] using ${GO_CMD} (target Go ${GO_VERSION})"
"${GO_CMD}" version
echo "[*] go mod tidy"
"${GO_CMD}" mod tidy
echo "[*] building..."
"${GO_CMD}" build -trimpath -ldflags="-s -w" -buildvcs=false -o bin/nhb ./cmd/nhb
echo "[✓] done: bin/nhb"
