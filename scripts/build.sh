#!/usr/bin/env bash
set -euo pipefail
echo "[*] go mod tidy"
go mod tidy
echo "[*] building..."
go build -trimpath -ldflags="-s -w" -buildvcs=false -o bin/nhb ./cmd/nhb
echo "[✓] done: bin/nhb"
