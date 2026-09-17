#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 3 ]]; then
  echo "usage: $0 <phase> <config> <outdir> [extra go-run args...]" >&2
  exit 2
fi

PHASE="$1"
CONFIG_ARG="$2"
OUTDIR_ARG="$3"
shift 3

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
ROOT_DIR="$(cd "${ROOT_DIR}/.." && pwd)"

if [[ "${CONFIG_ARG}" != /* ]]; then
  CONFIG_PATH="${ROOT_DIR}/${CONFIG_ARG}"
else
  CONFIG_PATH="${CONFIG_ARG}"
fi

if [[ "${OUTDIR_ARG}" != /* ]]; then
  OUT_DIR="${ROOT_DIR}/${OUTDIR_ARG}"
else
  OUT_DIR="${OUTDIR_ARG}"
fi

EXTRA_ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --hash|--compose)
      if [[ $# -lt 2 ]]; then
        echo "missing value for $1" >&2
        exit 2
      fi
      flag="$1"
      value="$2"
      shift 2
      if [[ "${value}" != /* ]]; then
        value="${ROOT_DIR}/${value}"
      fi
      EXTRA_ARGS+=("${flag}" "${value}")
      ;;
    *)
      EXTRA_ARGS+=("$1")
      shift
      ;;
  esac
done

JSON_PATH="${OUT_DIR}/report.json"
MD_PATH="${OUT_DIR}/report.md"

mkdir -p "${OUT_DIR}"

go run ./tools/audit \
  --phase "${PHASE}" \
  --config "${CONFIG_PATH}" \
  --out "${JSON_PATH}" \
  --markdown "${MD_PATH}" \
  "${EXTRA_ARGS[@]}"

echo "audit phase ${PHASE} summary written to ${JSON_PATH}" >&2
