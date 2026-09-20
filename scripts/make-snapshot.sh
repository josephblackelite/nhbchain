#!/usr/bin/env bash
# make-snapshot.sh -- make a snapshot of the chain database of a RUNNING node.
#
# Run it as root or as the user the node runs as, on a host that runs a
# validator or a follower. It only READS the node's data directory: it never
# stops, signals, locks or writes to the node, and the node keeps producing or
# syncing blocks while it works.
#
#   bash scripts/make-snapshot.sh --data-dir /var/lib/nhbchain/nhb-data \
#        --out-dir /var/lib/nhbchain/snapshots
#
# It writes, into --out-dir:
#   nhb-snapshot-<genesis prefix>-h<height>.tar.gz             the archive
#   nhb-snapshot-<genesis prefix>-h<height>.manifest.json      its manifest
#   manifest.json                                              the same manifest, at the fixed name
#                                                              a new node fetches (written last)
# Uploading them anywhere is a separate step that is not part of this script.
#
# HOW THE COPY IS MADE. The chain database is a LevelDB directory. Its table
# files (*.ldb) are immutable once written, so they are hard-linked into a
# private staging directory (copied when a hard link is not possible); the
# MANIFEST and the write-ahead journal (*.log) change while the node runs, so
# they are copied, and last. A pass is accepted only when the node's
# directory did not change underneath it: the same CURRENT, the same MANIFEST
# with the same size, the same set of journals before and after, and every
# table copied. Passes are repeated until two consecutive passes agree. The
# staged copy is then opened read-only by nhb-snapshot (which prints and checks
# the chain id, genesis hash, height, tip hash and state root, and re-hashes the
# whole state trie). A copy that does not open is never packed, and an archive
# that does not unpack and open again is never published.
#
# WHAT IS IN THE SNAPSHOT. Only these files of the chain database, chosen by
# name from an allow-list, never by exclusion:
#   CURRENT, MANIFEST-*, *.log, *.ldb, *.sst
# They hold blocks, the state trie and the indexes: data that every node on the
# network already holds and that is public on chain. They hold no key.
#
# WHAT IS NEVER IN IT (never read, never copied, never packed):
#   p2p/node_key.json      the node's network identity (a private key)
#   p2p/peerstore/         the addresses of the peers this node has met
#   bft_sign_state.json    what this validator has voted (double-sign guard)
#   polc_lock.json         this validator's consensus lock
#   LOCK, LOG, LOG.old     LevelDB bookkeeping
#   genesis.resolved.json  regenerated from the genesis file at every start
#   any other file, and any directory
# The consensus key does not live in the data directory at all (it is read from
# the environment or a keystore outside it). The archive is checked again for
# names that look like keys before it is written.
#
# Options:
#   --data-dir DIR         data directory of the running node        (required)
#   --out-dir DIR          where the archive and manifests are written (required)
#   --work-dir DIR         staging directory; keep it on the same filesystem as
#                          the data directory so tables can be hard-linked
#                          (default: <data-dir>/../.nhb-snapshot-work)
#   --tool PATH            the nhb-snapshot binary (default: bin/nhb-snapshot of
#                          the install root, then PATH)
#   --node-binary PATH     the node binary to describe in the manifest
#                          (default: /opt/nhbchain/bin/nhb)
#   --binary-version TEXT  version of that binary   (default: git describe)
#   --binary-commit TEXT   source commit of that binary (default: git rev-parse
#                          of the install root)
#   --max-passes N         give up after N passes (default 20)
#   --no-latest            do not write manifest.json
#   --keep-work            keep the staging directory
#   --help
set -euo pipefail
export LC_ALL=C

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/.." && pwd)

DATA_DIR="${NHB_DATA_DIR:-}"
OUT_DIR=''
WORK_DIR=''
TOOL=''
NODE_BINARY='/opt/nhbchain/bin/nhb'
BINARY_VERSION=''
BINARY_COMMIT=''
MAX_PASSES=20
WRITE_LATEST=1
KEEP_WORK=0

log() { printf '[snapshot] %s\n' "$*" >&2; }
warn() { printf '[snapshot][WARN] %s\n' "$*" >&2; }
die() { printf '[snapshot][ERROR] %s\n' "$*" >&2; exit 1; }

usage() { sed -n '2,/^set -euo pipefail/p' "${BASH_SOURCE[0]}" | sed -e '$d' -e 's/^# \{0,1\}//' ; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --data-dir) DATA_DIR="${2:-}"; shift 2 ;;
    --out-dir) OUT_DIR="${2:-}"; shift 2 ;;
    --work-dir) WORK_DIR="${2:-}"; shift 2 ;;
    --tool) TOOL="${2:-}"; shift 2 ;;
    --node-binary) NODE_BINARY="${2:-}"; shift 2 ;;
    --binary-version) BINARY_VERSION="${2:-}"; shift 2 ;;
    --binary-commit) BINARY_COMMIT="${2:-}"; shift 2 ;;
    --max-passes) MAX_PASSES="${2:-}"; shift 2 ;;
    --no-latest) WRITE_LATEST=0; shift ;;
    --keep-work) KEEP_WORK=1; shift ;;
    --help|-h) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

[[ -n "${DATA_DIR}" ]] || die "--data-dir is required"
[[ -n "${OUT_DIR}" ]] || die "--out-dir is required"
[[ "${MAX_PASSES}" =~ ^[0-9]+$ && "${MAX_PASSES}" -ge 2 ]] || die "--max-passes must be a number, at least 2"
[[ -d "${DATA_DIR}" ]] || die "data directory ${DATA_DIR} does not exist"
[[ -f "${DATA_DIR}/CURRENT" ]] || die "${DATA_DIR} has no CURRENT file: it is not the data directory of a node (the chain database is directly inside it)"
DATA_DIR=$(cd "${DATA_DIR}" && pwd)

if [[ -z "${TOOL}" ]]; then
  for candidate in /opt/nhbchain/bin/nhb-snapshot "${REPO_ROOT}/bin/nhb-snapshot"; do
    if [[ -x "${candidate}" ]]; then TOOL="${candidate}"; break; fi
  done
  [[ -n "${TOOL}" ]] || TOOL=$(command -v nhb-snapshot 2>/dev/null || true)
fi
[[ -n "${TOOL}" && -x "${TOOL}" ]] || die "the nhb-snapshot binary was not found; build it with 'go build -o bin/nhb-snapshot ./cmd/nhb-snapshot' and pass --tool"

mkdir -p "${OUT_DIR}"
OUT_DIR=$(cd "${OUT_DIR}" && pwd)
if [[ -z "${WORK_DIR}" ]]; then
  WORK_DIR="$(dirname "${DATA_DIR}")/.nhb-snapshot-work"
fi
mkdir -p "${WORK_DIR}"
WORK_DIR=$(cd "${WORK_DIR}" && pwd)

# Neither the staging area nor the output may live inside the node's data
# directory: the node would see files it did not write.
case "${WORK_DIR}/" in "${DATA_DIR}/"*) die "--work-dir must not be inside the data directory" ;; esac
case "${OUT_DIR}/" in "${DATA_DIR}/"*) die "--out-dir must not be inside the data directory" ;; esac

# The script removes what it staged in the work directory. Only ever do that in
# a directory it made itself: an empty one, or one it marked on an earlier run.
if [[ ! -f "${WORK_DIR}/.nhb-snapshot-work" ]]; then
  [[ -z "$(ls -A "${WORK_DIR}")" ]] || die "--work-dir ${WORK_DIR} is not empty and was not made by this script; give it an empty directory"
  : > "${WORK_DIR}/.nhb-snapshot-work"
fi

# Only one snapshot at a time may use a work directory. mkdir is atomic.
LOCK_DIR="${WORK_DIR}/.lock"
if ! mkdir "${LOCK_DIR}" 2>/dev/null; then
  holder=$(cat "${LOCK_DIR}/pid" 2>/dev/null || true)
  if [[ -n "${holder}" ]] && kill -0 "${holder}" 2>/dev/null; then
    die "another make-snapshot.sh (pid ${holder}) is using ${WORK_DIR}"
  fi
  warn "removing a stale lock left by pid ${holder:-unknown}"
  rm -rf "${LOCK_DIR}"
  mkdir "${LOCK_DIR}" || die "cannot take the lock ${LOCK_DIR}"
fi
echo "$$" > "${LOCK_DIR}/pid"

STAGE="${WORK_DIR}/stage"
PACK_TMP="${OUT_DIR}/.tmp-$$"
VERIFY_DIR="${WORK_DIR}/verify"
cleanup() {
  local rc=$?
  rm -rf "${PACK_TMP}" "${VERIFY_DIR}" 2>/dev/null || true
  if [[ "${KEEP_WORK}" != "1" ]]; then rm -rf "${STAGE}" 2>/dev/null || true; fi
  rm -rf "${LOCK_DIR}" 2>/dev/null || true
  return "${rc}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

file_size() { wc -c < "$1" | tr -d ' \r\n'; }

# list_state prints the state of the node's directory that decides whether a
# copy is consistent, in a fixed order. The MANIFEST size is read BEFORE the
# tables are listed: every table the MANIFEST refers to was complete before the
# MANIFEST said so, so a table list taken after the size cannot lack one.
list_state() {
  local current manifest path name
  current=$(cat "${DATA_DIR}/CURRENT" 2>/dev/null) || return 1
  current=${current//$'\r'/}
  current=${current//$'\n'/}
  [[ "${current}" =~ ^MANIFEST-[0-9]{6,}$ ]] || return 1
  manifest="${DATA_DIR}/${current}"
  [[ -f "${manifest}" ]] || return 1
  echo "CURRENT ${current}"
  echo "MANIFEST ${current} $(file_size "${manifest}")"
  for path in "${DATA_DIR}"/*; do
    [[ -f "${path}" ]] || continue
    name=${path##*/}
    if [[ "${name}" =~ ^[0-9]{6,}\.(ldb|sst)$ ]]; then
      echo "TABLE ${name}"
    elif [[ "${name}" =~ ^[0-9]{6,}\.log$ ]]; then
      echo "JOURNAL ${name}"
    fi
  done | LC_ALL=C sort
}

# signature is what two passes must agree on: everything in list_state.
signature() { list_state; }

copy_table() {
  local name=$1 src="${DATA_DIR}/$1" dst="${STAGE}/$1"
  # A table never changes once it is written and its name is never reused, so
  # a hard link to it, or a staged copy of the same size, is the same file.
  # (A copy taken while the table was still being written is shorter, and is
  # copied again.)
  if [[ -f "${dst}" ]]; then
    if [[ "${dst}" -ef "${src}" ]]; then return 0; fi
    if [[ "$(file_size "${dst}")" == "$(file_size "${src}")" ]]; then return 0; fi
  fi
  rm -f "${dst}"
  if ln "${src}" "${dst}" 2>/dev/null; then return 0; fi
  cp "${src}" "${dst}"
}

# one_pass makes a copy of the database in ${STAGE} and succeeds only if the
# node's directory did not change while it was made. It leaves the state it
# started from in ${PASS_STATE}.
one_pass() {
  local before after kind name path manifest_name want_size got_size
  before=$(list_state) || { warn "cannot read the state of ${DATA_DIR}"; return 1; }
  PASS_STATE="${before}"

  mkdir -p "${STAGE}"
  # Drop what a previous pass copied, except tables (immutable, reusable) that
  # are still in the node's directory.
  for path in "${STAGE}"/*; do
    [[ -e "${path}" ]] || continue
    name=${path##*/}
    if [[ "${name}" =~ ^[0-9]{6,}\.(ldb|sst)$ ]] && [[ -f "${DATA_DIR}/${name}" ]]; then continue; fi
    rm -f "${path}"
  done

  # 1. tables (immutable): hard link, or copy.
  while read -r kind name _; do
    [[ "${kind}" == "TABLE" ]] || continue
    copy_table "${name}" || { warn "table ${name} could not be copied (removed by a compaction?)"; return 1; }
  done <<< "${before}"

  # 2. the MANIFEST and CURRENT, then 3. the journals, last.
  manifest_name=$(sed -n 's/^CURRENT //p' <<< "${before}")
  cp "${DATA_DIR}/${manifest_name}" "${STAGE}/${manifest_name}" || { warn "cannot copy ${manifest_name}"; return 1; }
  cp "${DATA_DIR}/CURRENT" "${STAGE}/CURRENT" || { warn "cannot copy CURRENT"; return 1; }
  while read -r kind name _; do
    [[ "${kind}" == "JOURNAL" ]] || continue
    cp "${DATA_DIR}/${name}" "${STAGE}/${name}" || { warn "journal ${name} could not be copied (rotated?)"; return 1; }
  done <<< "${before}"

  after=$(list_state) || { warn "cannot read the state of ${DATA_DIR} after the copy"; return 1; }
  # Tables are left out of the comparison: a table that appears meanwhile is
  # one a compaction is writing, which the MANIFEST copied here cannot refer to
  # (the MANIFEST would have grown), and one that vanishes was already copied.
  if [[ "$(sed '/^TABLE /d' <<< "${before}")" != "$(sed '/^TABLE /d' <<< "${after}")" ]]; then
    warn "the node's database changed during the pass (a flush or compaction); retrying"
    return 1
  fi
  # The copied MANIFEST must be exactly as long as it was when the pass began.
  want_size=$(sed -n 's/^MANIFEST [^ ]* //p' <<< "${before}")
  got_size=$(file_size "${STAGE}/${manifest_name}")
  if [[ "${want_size}" != "${got_size}" ]]; then
    warn "the copied MANIFEST has ${got_size} bytes, expected ${want_size}; retrying"
    return 1
  fi
  return 0
}

# ---------------------------------------------------------------------------
# 0. what is being described
# ---------------------------------------------------------------------------
INSTALL_ROOT=$(cd "$(dirname "${NODE_BINARY}")/.." 2>/dev/null && pwd || echo '')
if [[ -z "${BINARY_COMMIT}" ]]; then
  BINARY_COMMIT=unknown
  if [[ -n "${INSTALL_ROOT}" ]] && command -v git >/dev/null 2>&1 && git -C "${INSTALL_ROOT}" rev-parse --git-dir >/dev/null 2>&1; then
    BINARY_COMMIT=$(git -C "${INSTALL_ROOT}" rev-parse HEAD 2>/dev/null || echo unknown)
  fi
fi
if [[ -z "${BINARY_VERSION}" ]]; then
  BINARY_VERSION=unknown
  if [[ -n "${INSTALL_ROOT}" ]] && command -v git >/dev/null 2>&1 && git -C "${INSTALL_ROOT}" rev-parse --git-dir >/dev/null 2>&1; then
    BINARY_VERSION=$(git -C "${INSTALL_ROOT}" describe --tags --always 2>/dev/null || echo unknown)
  fi
fi
BINARY_SHA256=''
if [[ -f "${NODE_BINARY}" ]]; then
  BINARY_SHA256=$(sha256sum "${NODE_BINARY}" | cut -d' ' -f1)
else
  warn "node binary ${NODE_BINARY} not found; the manifest will not carry its sha256"
fi
# The binary that is running, when it can be told (Linux): it is the one the
# database was written by, whatever is on disk now.
if command -v systemctl >/dev/null 2>&1; then
  main_pid=$(systemctl show -p MainPID --value nhb.service 2>/dev/null || true)
  if [[ "${main_pid}" =~ ^[0-9]+$ && "${main_pid}" -gt 0 && -r "/proc/${main_pid}/exe" ]]; then
    running_sha=$(sha256sum "/proc/${main_pid}/exe" 2>/dev/null | cut -d' ' -f1 || true)
    if [[ -n "${running_sha}" && "${running_sha}" != "${BINARY_SHA256}" ]]; then
      warn "the running nhb.service binary differs from ${NODE_BINARY}; recording the running one"
      BINARY_SHA256="${running_sha}"
    fi
  fi
fi
log "data directory:  ${DATA_DIR}"
log "output:          ${OUT_DIR}"
log "staging:         ${STAGE}"
log "tool:            ${TOOL}"
log "binary:          version=${BINARY_VERSION} commit=${BINARY_COMMIT} sha256=${BINARY_SHA256:-unknown}"

# Space check: tables are hard-linked when the staging directory allows it.
avail_kb=$(df -Pk "${WORK_DIR}" 2>/dev/null | awk 'NR==2 {print $4}' || true)
first_table=$(ls "${DATA_DIR}" 2>/dev/null | grep -E '^[0-9]{6,}\.(ldb|sst)$' | head -1 || true)
if [[ -n "${first_table}" && -n "${avail_kb}" ]]; then
  probe="${WORK_DIR}/.linkprobe.$$"
  if ln "${DATA_DIR}/${first_table}" "${probe}" 2>/dev/null; then
    rm -f "${probe}"
    log "tables will be hard-linked (same filesystem)"
  else
    used_kb=$(du -sk "${DATA_DIR}" 2>/dev/null | awk '{print $1}' || echo 0)
    warn "hard links are not possible from ${DATA_DIR} to ${WORK_DIR}; tables will be copied (${used_kb} KB needed, ${avail_kb} KB free)"
    if [[ "${used_kb}" =~ ^[0-9]+$ && "${avail_kb}" =~ ^[0-9]+$ && "${avail_kb}" -lt $(( used_kb + used_kb / 5 )) ]]; then
      die "not enough free space in ${WORK_DIR} for a copy of the database"
    fi
  fi
fi

# ---------------------------------------------------------------------------
# 1. copy until two consecutive passes agree
# ---------------------------------------------------------------------------
rm -rf "${STAGE}"
prev_sig=''
agreed=0
for ((pass = 1; pass <= MAX_PASSES; pass++)); do
  if one_pass; then
    sig=$(sed -e '/^TABLE /d' <<< "${PASS_STATE}")
    if [[ -n "${prev_sig}" && "${sig}" == "${prev_sig}" ]]; then
      log "pass ${pass}: the copy agrees with the previous pass"
      agreed=1
      break
    fi
    log "pass ${pass}: consistent copy made; confirming with another pass"
    prev_sig="${sig}"
  else
    prev_sig=''
    sleep 0.3 2>/dev/null || sleep 1
  fi
done
[[ "${agreed}" == "1" ]] || die "no two consecutive passes agreed in ${MAX_PASSES} passes: the database is changing too fast; try again"

# The stage must hold nothing but allow-listed chain database files. (The copy
# only ever names those, so this is a canary against a bug, not the filter.)
for path in "${STAGE}"/*; do
  name=${path##*/}
  [[ "${name}" =~ ^(CURRENT|MANIFEST-[0-9]{6,}|[0-9]{6,}\.(log|ldb|sst))$ ]] || die "unexpected file in the staged copy: ${name}"
  case "$(printf '%s' "${name}" | tr '[:upper:]' '[:lower:]')" in
    *key*|*keystore*|*secret*|*passphrase*|*token*|*.pem|*.env|*sign*|*polc*|*peerstore*) die "the staged copy holds a file that looks like key material: ${name}" ;;
  esac
done

# ---------------------------------------------------------------------------
# 2. open the copy and read what it says
# ---------------------------------------------------------------------------
log "opening the copy read-only"
info=$("${TOOL}" info --data-dir "${STAGE}") || die "the staged copy does not open: refusing to publish"
rm -f "${STAGE}/LOCK"
while IFS= read -r line; do log "  ${line}"; done <<< "${info}"

# ---------------------------------------------------------------------------
# 3. pack, then prove the archive unpacks and opens
# ---------------------------------------------------------------------------
rm -rf "${PACK_TMP}"
mkdir -p "${PACK_TMP}"
pack_args=(pack --data-dir "${STAGE}" --out-dir "${PACK_TMP}"
  --binary-version "${BINARY_VERSION}" --binary-commit "${BINARY_COMMIT}" --latest)
if [[ -n "${BINARY_SHA256}" ]]; then pack_args+=(--binary-sha256 "${BINARY_SHA256}"); fi
"${TOOL}" "${pack_args[@]}" > "${PACK_TMP}/pack.out" || die "packing failed"

manifest_tmp="${PACK_TMP}/manifest.json"
archive_name=$("${TOOL}" manifest show --manifest "${manifest_tmp}" --field archive.name)
chain_id=$("${TOOL}" manifest show --manifest "${manifest_tmp}" --field chainId)
genesis_hash=$("${TOOL}" manifest show --manifest "${manifest_tmp}" --field genesisHash)
height=$("${TOOL}" manifest show --manifest "${manifest_tmp}" --field height)
archive_size=$("${TOOL}" manifest show --manifest "${manifest_tmp}" --field archive.size)
archive_sha=$("${TOOL}" manifest show --manifest "${manifest_tmp}" --field archive.sha256)
[[ -f "${PACK_TMP}/${archive_name}" ]] || die "the packer did not write ${archive_name}"

log "checking that the archive unpacks and opens"
rm -rf "${VERIFY_DIR}"
mkdir -p "${VERIFY_DIR}"
"${TOOL}" extract --manifest "${manifest_tmp}" --archive "${PACK_TMP}/${archive_name}" \
  --target "${VERIFY_DIR}/data" --chain-id "${chain_id}" --genesis-hash "${genesis_hash}" >/dev/null \
  || die "the archive does not unpack and open again: refusing to publish"
rm -rf "${VERIFY_DIR}"

# ---------------------------------------------------------------------------
# 4. publish: archive and its manifest first, the fixed-name manifest last
# ---------------------------------------------------------------------------
base=${archive_name%.tar.gz}
mv -f "${PACK_TMP}/${archive_name}" "${OUT_DIR}/${archive_name}"
mv -f "${PACK_TMP}/${base}.manifest.json" "${OUT_DIR}/${base}.manifest.json"
if [[ "${WRITE_LATEST}" == "1" ]]; then
  mv -f "${manifest_tmp}" "${OUT_DIR}/manifest.json"
fi
chmod 0644 "${OUT_DIR}/${archive_name}" "${OUT_DIR}/${base}.manifest.json" 2>/dev/null || true
if [[ "${WRITE_LATEST}" == "1" ]]; then chmod 0644 "${OUT_DIR}/manifest.json" 2>/dev/null || true; fi

log "done: ${OUT_DIR}/${archive_name}"
log "      chain ${chain_id}, height ${height}, ${archive_size} bytes, sha256 ${archive_sha}"
log "included files: CURRENT, MANIFEST-*, *.log, *.ldb, *.sst (the chain database, no key of any kind)"
skipped=$(cd "${DATA_DIR}" && for f in * .[!.]*; do
  [[ -e "${f}" ]] || continue
  case "${f}" in
    CURRENT|MANIFEST-*|*.log|*.ldb|*.sst) : ;;
    LOCK|LOG|LOG.old) echo "${f} (LevelDB bookkeeping)" ;;
    p2p) echo "${f}/ (network identity and peer list)" ;;
    bft_sign_state.json|polc_lock.json) echo "${f} (consensus vote and lock state of this node)" ;;
    genesis.resolved.json) echo "${f} (regenerated from the genesis file at start)" ;;
    *) echo "${f} (not part of the chain database)" ;;
  esac
done)
if [[ -n "${skipped}" ]]; then
  log "not included (never read):"
  while IFS= read -r line; do log "  ${line}"; done <<< "${skipped}"
fi
