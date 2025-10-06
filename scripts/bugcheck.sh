#!/usr/bin/env bash
set -euo pipefail

# Pre-bounty bugcheck orchestrator

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
export PATH="${PATH}:$(go env GOPATH 2>/dev/null || echo "$HOME/go")/bin"

TIMESTAMP="$(date -u +"%Y%m%d-%H%M%S")"
LOG_DIR="${REPO_ROOT}/logs"
ARTIFACT_DIR="${REPO_ROOT}/artifacts"
AUDIT_DIR="${REPO_ROOT}/audit"
RUN_DIR="${ARTIFACT_DIR}/bugcheck-${TIMESTAMP}"
SUMMARY_JSON="${RUN_DIR}/summary.json"
AUDIT_MD="${AUDIT_DIR}/bugcheck-${TIMESTAMP}.md"

mkdir -p "${LOG_DIR}" "${ARTIFACT_DIR}" "${AUDIT_DIR}" "${RUN_DIR}"

results_json=()
status_overall="passed"

print_header() {
  cat <<REPORT > "${AUDIT_MD}"
# Pre-bounty Bugcheck Report

- Timestamp: ${TIMESTAMP} UTC
- Repository: $(basename "${REPO_ROOT}")

| Check | Description | Status | Log |
| --- | --- | --- | --- |
REPORT
}

append_markdown_row() {
  local check_id="$1"
  local description="$2"
  local status="$3"
  local log_file="$4"
  echo "| ${check_id} | ${description} | ${status} | ${log_file} |" >> "${AUDIT_MD}"
}

json_escape() {
  printf '%s' "$1" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'
}

record_result() {
  local check_id="$1"
  local description="$2"
  local command="$3"
  local status="$4"
  local severity="$5"
  local log_file="$6"
  local details="$7"
  local json
  json="{\"id\":${check_id},\"description\":${description},\"command\":${command},\"status\":\"${status}\",\"severity\":\"${severity}\",\"log\":${log_file},\"details\":${details}}"
  results_json+=("${json}")
}

run_check() {
  local check_id="$1"
  local description="$2"
  local severity="$3"
  shift 3
  local cmd=("$@")
  local log_file="${LOG_DIR}/${check_id}.log"
  local cmd_str
  cmd_str=$(printf '%q ' "${cmd[@]}")

  echo "[bugcheck] Running ${check_id}: ${cmd_str}" | tee "${LOG_DIR}/${check_id}.start"

  local primary_cmd="${cmd[0]}"
  if ! command -v "${primary_cmd}" >/dev/null 2>&1; then
    echo "[bugcheck] Missing command: ${primary_cmd}" | tee -a "${log_file}"
    append_markdown_row "${check_id}" "${description}" "missing" "$(basename "${log_file}")"
    record_result "$(json_escape "${check_id}")" "$(json_escape "${description}")" "$(json_escape "${cmd_str}")" "missing" "${severity}" "$(json_escape "$(basename "${log_file}")")" "$(json_escape "Command not found")"
    if [[ "${severity}" == "critical" ]]; then
      status_overall="failed"
    fi
    return 1
  fi

  local exit_code=0
  if ! ("${cmd[@]}" 2>&1 | tee "${log_file}"); then
    exit_code=$?
  fi

  if [[ ${exit_code} -ne 0 ]]; then
    append_markdown_row "${check_id}" "${description}" "failed" "$(basename "${log_file}")"
    record_result "$(json_escape "${check_id}")" "$(json_escape "${description}")" "$(json_escape "${cmd_str}")" "failed" "${severity}" "$(json_escape "$(basename "${log_file}")")" "$(json_escape "Exit code ${exit_code}")"
    if [[ "${severity}" == "critical" ]]; then
      status_overall="failed"
    fi
    return ${exit_code}
  fi

  append_markdown_row "${check_id}" "${description}" "passed" "$(basename "${log_file}")"
  record_result "$(json_escape "${check_id}")" "$(json_escape "${description}")" "$(json_escape "${cmd_str}")" "passed" "${severity}" "$(json_escape "$(basename "${log_file}")")" "$(json_escape "")"
  return 0
}

print_header

# Tooling bootstrap
run_check "bugcheck-tools" "Install or verify bugcheck toolchain" "critical" make bugcheck-tools || true

# 1. Static and security analysis
run_check "static-security" "golangci-lint, go vet, staticcheck, gosec, govulncheck" "critical" make bugcheck-static || true

# 2. Race-enabled tests and fuzzing
run_check "race-tests" "go test -race ./..." "critical" make bugcheck-race || true
run_check "fuzz-critical" "60s fuzzing on critical state transition tests" "critical" make bugcheck-fuzz || true

# 3. Determinism and BFT safety on 3-node localnet
run_check "determinism" "Determinism and BFT safety audit" "critical" make bugcheck-determinism || true

# 4. Chaos fault-injection resilience
run_check "chaos" "Chaos fault-injection audit" "critical" make bugcheck-chaos || true

# 5. Gateway end-to-end flows
run_check "gateway-e2e" "Gateway swap, lending, governance, escrow flows" "critical" make bugcheck-gateway || true

# 6. Network hardening checks
run_check "network-hardening" "Rate limits, TLS, mTLS, and auth smoke" "critical" make bugcheck-network || true

# 7. Performance benchmarks and SLA validation
run_check "performance" "Benchmark finality and proposer commit latency" "critical" make bugcheck-perf || true

# 8. Protobuf compatibility
run_check "protobuf" "buf lint and breaking checks" "critical" make bugcheck-proto || true

# 9. Documentation and examples validation
run_check "docs" "Docs/examples validation" "critical" make bugcheck-docs || true

# Emit JSON summary
{
  printf '{"timestamp":"%s","status":"%s","results":[' "${TIMESTAMP}" "${status_overall}"
  local first=1
  for entry in "${results_json[@]}"; do
    if [[ ${first} -eq 1 ]]; then
      first=0
    else
      printf ','
    fi
    printf '%s' "${entry}"
  done
  printf ']}\n'
} > "${SUMMARY_JSON}"

# Copy summary JSON to top-level artifacts directory for convenience
cp "${SUMMARY_JSON}" "${ARTIFACT_DIR}/bugcheck-${TIMESTAMP}.json"

if [[ "${status_overall}" == "passed" ]]; then
  echo "✅ GREEN LIGHT — Pre-bounty bugcheck passed."
  exit 0
else
  echo "❌ Pre-bounty bugcheck failed. See ${AUDIT_MD} for details."
  exit 1
fi
