#!/usr/bin/env bash
# Fails when a commit in the history of HEAD changes no file.
#
# A commit whose message describes work but whose diff is empty cannot be used as
# evidence that the work was done. Merge commits are not looked at. Commits listed
# by full id in scripts/known-empty-commits.txt (or the file named by
# KNOWN_EMPTY_COMMITS) are skipped. Usage, from inside a repository:
#
#   scripts/check_empty_commits.sh [<rev-list range>]      (default: HEAD, the whole history)
set -euo pipefail

range="${1:-HEAD}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
known_file="${KNOWN_EMPTY_COMMITS:-${script_dir}/known-empty-commits.txt}"

declare -A known=()
if [[ -f "${known_file}" ]]; then
  while IFS= read -r line || [[ -n "${line}" ]]; do
    line="${line%%#*}"
    line="${line//[[:space:]]/}"
    if [[ ${#line} -eq 40 ]]; then known["${line}"]=1; fi
  done < "${known_file}"
fi

bad=0
while IFS= read -r sha; do
  [[ -n "${sha}" ]] || continue
  if [[ -n "${known[${sha}]:-}" ]]; then continue; fi
  changed=$(git diff-tree --no-commit-id --name-only -r --root "${sha}")
  if [[ -z "${changed}" ]]; then
    echo "empty commit ${sha:0:12}: $(git log -1 --format=%s "${sha}")" >&2
    bad=1
  fi
done < <(git rev-list --no-merges "${range}")

if [[ "${bad}" != "0" ]]; then
  echo "check_empty_commits: the commits above change no file; their messages describe work the history does not show." >&2
  exit 1
fi
echo "check_empty_commits: no empty commits in ${range}"
