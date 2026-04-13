#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
AUDIT_DIR="${REPO_ROOT}/audit"
DOCS_DIR="${REPO_ROOT}/docs/audit"
HISTORY_DIR="${DOCS_DIR}/history"
INDEX_FILE="${DOCS_DIR}/index.md"
LATEST_FILE="${DOCS_DIR}/latest.md"
META_FILE="${DOCS_DIR}/_meta.json"
MARKER="<!-- BUGCHECK_HISTORY -->"

mkdir -p "${DOCS_DIR}" "${HISTORY_DIR}"

shopt -s nullglob
reports=("${AUDIT_DIR}"/report-*.md "${AUDIT_DIR}"/bugcheck-*.md)
shopt -u nullglob

filtered_reports=()
for report in "${reports[@]}"; do
  if [[ -f "${report}" ]]; then
    filtered_reports+=("${report}")
  fi
done

if (( ${#filtered_reports[@]} == 0 )); then
  echo "publish_bugcheck: no bugcheck markdown reports found under ${AUDIT_DIR}" >&2
  exit 1
fi

latest_report=$(ls -1t "${filtered_reports[@]}" | head -n1)
if [[ -z "${latest_report}" ]]; then
  echo "publish_bugcheck: failed to determine latest report" >&2
  exit 1
fi

latest_name="$(basename "${latest_report}")"
latest_slug="${latest_name%.md}"
if [[ -z "${latest_slug}" ]]; then
  echo "publish_bugcheck: unable to derive slug from ${latest_name}" >&2
  exit 1
fi

mapfile -t time_parts < <(python3 - "$latest_report" <<'PY'
import datetime
import os
import sys
path = sys.argv[1]
try:
    mtime = os.path.getmtime(path)
    dt = datetime.datetime.utcfromtimestamp(mtime)
except Exception:
    dt = datetime.datetime.utcnow()
print(dt.strftime('%Y-%m-%d %H:%M:%SZ'))
print(dt.strftime('%Y-%m-%d %H:%M UTC'))
PY
)

latest_timestamp="${time_parts[0]}"
latest_display="${time_parts[1]}"

history_markdown="${HISTORY_DIR}/${latest_slug}.md"
cp "${latest_report}" "${history_markdown}"
cp "${latest_report}" "${LATEST_FILE}"

declare -a json_candidates=(
  "${AUDIT_DIR}/${latest_slug}.json"
  "${REPO_ROOT}/artifacts/${latest_slug}.json"
)
for candidate in "${json_candidates[@]}"; do
  if [[ -f "${candidate}" ]]; then
    cp "${candidate}" "${HISTORY_DIR}/${latest_slug}.json"
    break
  fi
done

if [[ ! -f "${INDEX_FILE}" ]]; then
  cat <<'TEMPLATE' > "${INDEX_FILE}"
# Bugcheck history

The [latest bugcheck report](./latest.md) is automatically published after every pipeline run.

## Reading the report

Bugcheck gates must remain **PASS** to ship:

- **Static security** – staticcheck, go vet, golangci-lint, gosec, govulncheck.
- **Race tests** – `go test -race` across the full module graph.
- **Fuzzing** – coverage-guided fuzzing on critical state transitions.
- **Determinism** – multi-node determinism, state sync, and BFT safety checks.
- **Chaos** – container, network, and process fault injection resilience.
- **Performance** – proposer throughput and finality latency SLO verification.
- **Protobuf** – Buf lint and breaking-change contracts for all APIs.
- **Docs** – documentation linting, snippet verification, and example execution.

## Past runs

| Timestamp (UTC) | Report |
| --- | --- |
<!-- BUGCHECK_HISTORY -->
TEMPLATE
fi

if ! grep -q "${MARKER}" "${INDEX_FILE}"; then
  echo "${MARKER}" >> "${INDEX_FILE}" || true
fi

python3 - "$INDEX_FILE" "$MARKER" "$latest_timestamp" "$latest_slug" <<'PY'
import sys
from pathlib import Path

index_path = Path(sys.argv[1])
marker = sys.argv[2]
timestamp = sys.argv[3]
slug = sys.argv[4]
link = f"| {timestamp} | [{slug}](./history/{slug}.md) |"

lines = index_path.read_text().splitlines()
try:
    marker_idx = lines.index(marker)
except ValueError:
    lines.append(marker)
    marker_idx = len(lines) - 1

if link not in lines:
    lines.insert(marker_idx + 1, link)

index_path.write_text("\n".join(lines) + "\n")
PY

python3 - "$META_FILE" "$latest_slug" "$latest_display" <<'PY'
import json
import sys
from pathlib import Path

meta_path = Path(sys.argv[1])
slug = sys.argv[2]
pretty = sys.argv[3]

if meta_path.exists():
    data = json.loads(meta_path.read_text())
else:
    data = {}

# Ensure friendly titles for existing audit docs.
def ensure_title(key, title):
    if key not in data:
        data[key] = title
    elif isinstance(data[key], str):
        if not data[key]:
            data[key] = title
    elif isinstance(data[key], dict):
        data[key].setdefault("title", title)

ensure_title("overview", "Audit program overview")
ensure_title("docs-quality", "Documentation quality checks")
ensure_title("e2e-flows", "End-to-end flows")
ensure_title("fuzzing", "Fuzzing coverage")
ensure_title("recon", "Ledger reconciliation")
ensure_title("static-analysis", "Static analysis")
ensure_title("latest", "Latest bugcheck report")
ensure_title("index", "Bugcheck history")

history = data.get("history")
if not isinstance(history, dict):
    history = {"title": "Historical reports", "type": "folder", "items": {}}
    data["history"] = history

items = history.setdefault("items", {})
items[slug] = {"title": pretty}

meta_path.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")
PY

echo "Published ${latest_name} to docs/audit." >&2
