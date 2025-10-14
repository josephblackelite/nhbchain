#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CHECKLIST_FILE="${REPO_ROOT}/docs/audit/latest.md"
GO_PACKAGE="${REPO_ROOT}/scripts/audit-proofs"

if [[ ! -f "${CHECKLIST_FILE}" ]]; then
  echo "publish_audit: checklist file not found at ${CHECKLIST_FILE}" >&2
  exit 1
fi

if [[ ! -d "${GO_PACKAGE}" ]]; then
  echo "publish_audit: generator package missing at ${GO_PACKAGE}" >&2
  exit 1
fi

PROOFS_TMP="$(mktemp)"
trap 'rm -f "${PROOFS_TMP}"' EXIT

( cd "${REPO_ROOT}" && go run "${GO_PACKAGE}" -checklist "${CHECKLIST_FILE}" -root "${REPO_ROOT}" -out "${PROOFS_TMP}" )

python3 - "${CHECKLIST_FILE}" "${PROOFS_TMP}" <<'PY'
import re
import sys
from pathlib import Path

checklist_path = Path(sys.argv[1])
proofs_path = Path(sys.argv[2])

comment_re = re.compile(r'<!--.*?-->', re.DOTALL)
bullet_re = re.compile(r'^\s*[-*+]\s*✅\s*(.*)$')


def parse_proofs(path: Path):
    sections = {}
    current = None
    entries = []
    for raw_line in path.read_text().splitlines():
        line = raw_line.strip('\n')
        if line.startswith('## '):
            if current is not None:
                sections[current] = entries
            current = line[3:].strip()
            entries = []
            continue
        if current is None:
            continue
        stripped = line.strip()
        if stripped.startswith('- '):
            entry = stripped[2:].strip()
            if entry:
                entries.append(entry)
    if current is not None and current not in sections:
        sections[current] = entries
    return sections


def extract_title(line):
    without_comments = comment_re.sub('', line)
    match = bullet_re.match(without_comments.strip())
    if not match:
        return None
    title = match.group(1).strip()
    return title or None


def leading_spaces(line):
    return len(line) - len(line.lstrip(' '))


proofs = parse_proofs(proofs_path)
lines = checklist_path.read_text().splitlines()
result = []
idx = 0

while idx < len(lines):
    line = lines[idx]
    title = extract_title(line)
    if title is None:
        result.append(line)
        idx += 1
        continue

    indent_prefix = line[:len(line) - len(line.lstrip(' '))]
    indent_len = len(indent_prefix)
    result.append(line)

    j = idx + 1
    while j < len(lines):
        current_line = lines[j]
        stripped = current_line.strip()
        current_indent = leading_spaces(current_line)

        if current_indent >= indent_len + 2 and stripped.startswith('Proofs:'):
            j += 1
            while j < len(lines):
                follow_line = lines[j]
                follow_trim = follow_line.strip()
                follow_indent = leading_spaces(follow_line)
                if follow_indent >= indent_len + 4 and (follow_trim.startswith('- ') or follow_trim.startswith('* ') or follow_trim.startswith('+ ')):
                    j += 1
                    continue
                if follow_trim == '':
                    j += 1
                    continue
                break
            continue

        if stripped == '':
            break
        if current_indent <= indent_len and (stripped.startswith('- ') or stripped.startswith('* ') or stripped.startswith('+ ') or re.match(r'\d+[\.)]\s', stripped)):
            break

        result.append(current_line)
        j += 1

    entries = proofs.get(title, [])
    if not entries:
        entries = ['_no matches found_']

    result.append(f"{indent_prefix}  Proofs:")
    for entry in entries:
        result.append(f"{indent_prefix}    - {entry}")

    idx = j

output = "\n".join(result) + "\n"
checklist_path.write_text(output)
PY

echo "publish_audit: refreshed proofs in ${CHECKLIST_FILE}" >&2
