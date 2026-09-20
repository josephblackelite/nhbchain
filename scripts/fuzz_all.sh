#!/usr/bin/env bash
# Runs every fuzz target in the repository, one at a time, for FUZZTIME each
# (default 60s), and fails if any of them does.
#
# `go test -fuzz` accepts one package and one fuzz function per run, and finds
# them only in _test.go files, so the targets are listed from those files and
# each one gets a run of its own. Set FUZZ_LIST_ONLY=1 to print the
# "<package> <function>" pairs and run nothing.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

FUZZTIME="${FUZZTIME:-60s}"

targets=()
while IFS= read -r line; do
  targets+=("${line}")
done < <(
  grep -r --include='*_test.go' -o -E '^func Fuzz[A-Za-z0-9_]*' \
    --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=artifacts . |
    sed -E 's|^\./||; s|/[^/]*_test\.go:func | |' |
    sort -u
)

if [[ ${#targets[@]} -eq 0 ]]; then
  echo "[fuzz] no fuzz targets found" >&2
  exit 1
fi

if [[ "${FUZZ_LIST_ONLY:-}" == "1" ]]; then
  printf '%s\n' "${targets[@]}"
  exit 0
fi

failed=0
for target in "${targets[@]}"; do
  pkg="./${target%% *}"
  name="${target##* }"
  echo "[fuzz] ${pkg} ${name} for ${FUZZTIME}"
  if go test -run '^$' -fuzz "^${name}\$" -fuzztime "${FUZZTIME}" "${pkg}"; then
    :
  else
    code=$?
    echo "[fuzz] FAILED (exit ${code}): ${pkg} ${name}" >&2
    failed=1
  fi
done

exit "${failed}"
