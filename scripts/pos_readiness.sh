#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG_DIR="$ROOT_DIR/logs/pos"
mkdir -p "$LOG_DIR"

cd "$ROOT_DIR"

echo "[pos] building test binaries"
make pos:build-tests | tee "$LOG_DIR/build.log"

declare -a SUITES=(
  "run-intent"
  "run-paymaster"
  "run-registry"
  "run-realtime"
  "run-security"
  "run-fees"
)

for suite in "${SUITES[@]}"; do
  target="pos:${suite}"
  echo "[pos] running $target"
  if ! make "$target"; then
    echo "[pos] $target failed" >&2
    exit 1
  fi
done

echo "[pos] running pos:bench-qos"
make pos:bench-qos

echo "[pos] readiness profile completed"
