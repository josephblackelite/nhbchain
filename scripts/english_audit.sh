#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
TIMESTAMP="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

TEST_JSON="${TMPDIR}/go-test.json"
DOCS_LOG="${TMPDIR}/docs-verify.log"

pushd "${REPO_ROOT}" >/dev/null

echo "[english_audit] Running unit tests (go test ./... -json)"
TEST_STATUS=0
if go test ./... -json >"${TEST_JSON}"; then
  echo "[english_audit] Unit tests passed"
else
  TEST_STATUS=$?
  echo "[english_audit] Unit tests failed (exit ${TEST_STATUS}); details in ${TEST_JSON}" >&2
fi

echo "[english_audit] Running documentation verification (go run ./scripts/verify-docs-snippets --root docs)"
DOCS_STATUS=0
if go run ./scripts/verify-docs-snippets --root docs >"${DOCS_LOG}" 2>&1; then
  echo "[english_audit] Documentation verification passed"
else
  DOCS_STATUS=$?
  echo "[english_audit] Documentation verification failed (exit ${DOCS_STATUS}); details in ${DOCS_LOG}" >&2
fi

python3 - "${TIMESTAMP}" "${REPO_ROOT}" "${TMPDIR}" "${TEST_JSON}" "${TEST_STATUS}" "${DOCS_LOG}" "${DOCS_STATUS}" <<'PY'
import json
import os
import sys
import subprocess
from pathlib import Path

(
    timestamp,
    repo_root,
    tmpdir,
    test_json_path,
    test_status_raw,
    docs_log_path,
    docs_status_raw,
) = sys.argv[1:8]

test_status = int(test_status_raw)
docs_status = int(docs_status_raw)

def extract_test_data(path: str, limit: int = 3) -> dict[str, list[str]]:
    test_path = Path(path)
    if not test_path.exists():
        return {"keywords": [], "failures": [], "passes": []}

    passes: list[tuple[str, str]] = []
    failures: list[tuple[str, str]] = []
    seen_pass: set[str] = set()
    seen_fail: set[str] = set()

    with test_path.open("r", encoding="utf-8") as handle:
        for raw in handle:
            raw = raw.strip()
            if not raw:
                continue
            try:
                event = json.loads(raw)
            except json.JSONDecodeError:
                continue
            action = event.get("Action")
            name = event.get("Test")
            pkg = event.get("Package")
            if not name or "/" in name:
                continue
            identifier = f"{pkg}.{name}" if pkg else name
            if action == "fail":
                if identifier in seen_fail:
                    continue
                seen_fail.add(identifier)
                failures.append((pkg or "", name))
            elif action == "pass":
                if identifier in seen_pass:
                    continue
                seen_pass.add(identifier)
                passes.append((pkg or "", name))

    failure_keywords = [name for _, name in failures][:limit]
    pass_keywords = [name for _, name in passes][:limit]
    if failure_keywords:
        keywords = failure_keywords
    else:
        keywords = pass_keywords

    return {
        "keywords": keywords,
        "failures": failures[:limit],
        "passes": passes[:limit],
    }

def load_docs_keywords(repo: str, limit: int = 3) -> list[str]:
    candidates = [
        "func Verify(docRoot string) error {",
        "if !strings.HasPrefix(line, \"<!-- embed:\") || !strings.HasSuffix(line, \"-->\") {",
        "return fmt.Errorf(\"broken relative link %q\", target)",
        "snippets = append(snippets, snippet{lang: lang, file: embedPath})",
    ]
    keywords: list[str] = []
    source = Path(repo) / "tools" / "docs" / "snippets" / "snippets.go"
    text = source.read_text(encoding="utf-8") if source.exists() else ""
    for candidate in candidates:
        if candidate in text:
            keywords.append(candidate)
        if len(keywords) >= limit:
            break
    return keywords

def fallback_proofs(repo: str, keywords: list[str], limit: int = 3) -> list[str]:
    if not keywords:
        return []
    results: list[str] = []
    seen: set[str] = set()
    for keyword in keywords:
        if len(results) >= limit:
            break
        try:
            proc = subprocess.run(
                [
                    "rg",
                    "-n",
                    "-m",
                    "1",
                    "--no-heading",
                    "-F",
                    keyword,
                    repo,
                ],
                check=True,
                capture_output=True,
                text=True,
            )
        except subprocess.CalledProcessError:
            continue
        line = proc.stdout.strip().splitlines()
        if not line:
            continue
        first = line[0]
        parts = first.split(":", 2)
        if len(parts) < 3:
            continue
        path, lineno, snippet = parts
        snippet = snippet.strip()
        entry = f"- `{keyword}` · `{path}:{lineno}` — {snippet}"
        if entry in seen:
            continue
        seen.add(entry)
        results.append(entry)
    return results

def build_checklist(items: list[dict], output: Path) -> None:
    lines: list[str] = [f"# English audit checklist {timestamp}", ""]
    for item in items:
        symbol = "✅" if item["status"] else "❌"
        keywords = item.get("keywords", [])
        limit = max(2, min(4, item.get("limit", len(keywords) or 2)))
        payload = {"keywords": keywords, "limit": limit}
        comment = json.dumps(payload, ensure_ascii=False)
        lines.append(f"- {symbol} {item['title']} <!-- Proofs: {comment} -->")
        lines.append("")
    output.write_text("\n".join(lines) + "\n", encoding="utf-8")

def run_audit_proofs(repo: str, checklist: Path, proofs: Path) -> None:
    cmd = [
        "go",
        "run",
        "./scripts/audit-proofs",
        "-checklist",
        str(checklist),
        "-root",
        repo,
        "-out",
        str(proofs),
    ]
    subprocess.run(cmd, cwd=repo, check=True)

def parse_proofs(path: Path) -> dict[str, list[str]]:
    proofs: dict[str, list[str]] = {}
    current: str | None = None
    if not path.exists():
        return proofs
    with path.open("r", encoding="utf-8") as handle:
        for raw in handle:
            line = raw.rstrip("\n")
            if not line or line.startswith("<!--"):
                continue
            if line.startswith("## "):
                current = line[3:].strip()
                proofs[current] = []
                continue
            if current is None:
                continue
            if line.startswith("- "):
                proofs[current].append(line)
    return proofs

def append_report(repo: str, timestamp: str, items: list[dict], proofs: dict[str, list[str]]) -> None:
    doc_path = Path(repo) / "docs" / "audit" / "latest.md"
    existing = doc_path.read_text(encoding="utf-8")
    if not existing.endswith("\n"):
        existing += "\n"
    summary_lines: list[str] = [
        f"## English status report — {timestamp}",
        "",
    ]
    for item in items:
        symbol = "✅" if item["status"] else "❌"
        summary_lines.append(f"- {symbol} {item['title']} (`{item['command']}`)")
        summary_lines.append("  Proofs:")
        entries = proofs.get(item["title"], [])
        if not entries:
            summary_lines.append("    - _no proofs generated_")
        else:
            for entry in entries:
                summary_lines.append(f"    {entry}")
        notes = item.get("notes", [])
        if notes:
            summary_lines.append("  Notes:")
            for note in notes:
                summary_lines.append(f"    - {note}")
        summary_lines.append("")
    summary_text = "\n".join(summary_lines).rstrip() + "\n"
    doc_path.write_text(existing + "\n" + summary_text, encoding="utf-8")
    print("\n".join(summary_lines))

test_data = extract_test_data(test_json_path)
doc_keywords = load_docs_keywords(repo_root)

test_notes: list[str] = []
if test_data["failures"]:
    formatted = [
        f"{pkg}.{name}" if pkg else name for pkg, name in test_data["failures"]
    ]
    test_notes.append("Failing tests: " + ", ".join(formatted))
elif test_status == 0 and test_data["passes"]:
    formatted = [
        f"{pkg}.{name}" if pkg else name for pkg, name in test_data["passes"]
    ]
    test_notes.append("Sample passing tests: " + ", ".join(formatted))

items = [
    {
        "id": "unit-tests",
        "title": "Unit tests",
        "command": "go test ./...",
        "status": test_status == 0,
        "keywords": test_data["keywords"],
        "notes": test_notes,
    },
    {
        "id": "docs-verification",
        "title": "Documentation snippets",
        "command": "go run ./scripts/verify-docs-snippets --root docs",
        "status": docs_status == 0,
        "keywords": doc_keywords,
    },
]

checklist_path = Path(tmpdir) / "english_checklist.md"
proofs_path = Path(tmpdir) / "english_proofs.md"

build_checklist(items, checklist_path)
run_audit_proofs(repo_root, checklist_path, proofs_path)
proof_map = parse_proofs(proofs_path)
for item in items:
    title = item["title"]
    if not proof_map.get(title):
        fallback = fallback_proofs(repo_root, item.get("keywords", []))
        if fallback:
            proof_map[title] = fallback
append_report(repo_root, timestamp, items, proof_map)
PY

popd >/dev/null

EXIT_CODE=0
if [[ ${TEST_STATUS} -ne 0 || ${DOCS_STATUS} -ne 0 ]]; then
  EXIT_CODE=1
fi

exit ${EXIT_CODE}
