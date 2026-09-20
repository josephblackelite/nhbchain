#!/usr/bin/env bash
# make-snapshot.sh -- make a snapshot of the chain database of a RUNNING node.
#
# Run it as the user the node runs as (the owner of the data directory), on a
# host that runs a validator or a follower. It only READS the node's data
# directory: it never stops, signals, locks or writes to the node, and the node
# keeps producing or syncing blocks while it works.
#
#   sudo -u nhb bash scripts/make-snapshot.sh --data-dir /var/lib/nhbchain/nhb-data \
#        --out-dir /var/lib/nhbchain/snapshots
#
# WHY NOT AS ROOT. The data directory belongs to the node's user, and the node
# is the network-facing part of the host. Whatever that user puts in its own
# directory is an input of this script, and what the script copies is published.
# Run as root, it would read whatever the user pointed a link at (a link named
# like a journal, aimed at a file only root can read) and publish it. The script
# therefore refuses to run as root against a directory root does not own, or that
# root owns but its group or others can write, or that has above it a directory
# another user owns or its group or others can write (that user can rename the
# directory away, at any moment of a run that lasts minutes, and put one of its own
# under the name); run it as the directory's owner and nothing it reads is more than
# that user already holds. (When the node itself runs as root, the directory is
# root's and it may be run as root: then --tool, --work-dir and --out-dir have to
# be given, and the data directory and all three have to be places nobody but root
# can change; each one is checked together with every directory above it (a
# directory that does not exist yet is made only where the nearest one that does is
# root's own). Run as anyone else, the default tool is used only when the user
# running the script, or root, owns it.)
#
# It writes, into --out-dir:
#   nhb-snapshot-<genesis prefix>-h<height>.tar.gz             the archive
#   nhb-snapshot-<genesis prefix>-h<height>.manifest.json      its manifest
#   manifest.json                                              the same manifest, at the fixed name
#                                                              a new node fetches (written last)
# Uploading them anywhere is a separate step that is not part of this script. The
# node's user writes --out-dir, so whoever copies it on as another user must copy
# regular files only and never follow a link. Old snapshots stay where they are
# unless --keep N is given.
#
# WHICH NODE BUILD THE SNAPSHOT NAMES. The manifest records the node binary the
# database was written by (its sha256) and the source commit it was built from.
# A new node runs every later block with its own build, so scripts/deployvalidator.sh
# accepts a snapshot only when its own build is that binary or that commit, and
# tells the operator to check the commit out. A manifest that names no commit is
# a dead end for every new node, so this script refuses to publish one. On a host
# laid out by scripts/deployvalidator.sh (the binary in /opt/nhbchain/bin, the
# checkout in /opt/nhbchain) both are read from there. On any other host (a
# checkout in a home directory, a binary somewhere else, or a checkout that git
# will not read for the user running this script, such as one another user owns)
# pass --node-binary, the path of the binary that is running, and
# --binary-commit, the full commit id it was built from ("git rev-parse HEAD" in
# its checkout). That commit has to contain scripts/deployvalidator.sh and
# cmd/nhb-snapshot as they are now, or a new node that checks it out finds the
# script that syncs from genesis, which does not work on this network (see
# docs/validators/snapshot-onboarding.md).
#
# HOW THE COPY IS MADE. The chain database is a LevelDB directory. It is copied
# in this order, and only these files, chosen by what the database says and never
# by what a directory listing shows:
#   1. CURRENT, and the MANIFEST it names;
#   2. the tables (*.ldb) that copied MANIFEST lists: immutable once written, so
#      they are hard-linked into a private staging directory (copied when a hard
#      link is not possible);
#   3. the journal (*.log) that MANIFEST names, last: it changes while the node
#      runs, so it is copied.
# Everything else in the directory is left where it is: a table a compaction was
# still writing, one it had finished with, an older MANIFEST, an older journal,
# and any file that only has the name of one of these (a page of text called
# 999999.log is never packed: no MANIFEST names it). Only regular files are ever
# copied and a link is never followed: a name of the database that is a link, or
# anything but a regular file, ends the run before anything is read, and the
# copies are made without following links (cp -P, ln -P) and checked afterwards.
# A pass is accepted only when the node's directory did not change underneath it:
# the same CURRENT, the same MANIFEST with the same size, the same set of
# journals before and after, every listed table copied. Passes are repeated until
# two consecutive passes agree. The staged copy is then opened read-only by
# nhb-snapshot, which prints and checks the chain id, genesis hash, height, tip
# hash and state root, re-hashes the whole state trie, reads every table in full
# and refuses the copy if it holds any file its own MANIFEST does not refer to or
# a journal that does not read as one. A copy that does not open is never packed,
# and an archive that does not unpack and open again is never published.
#
# WHAT IS IN THE SNAPSHOT. Only these files of the chain database, chosen by
# name from an allow-list and then by what the MANIFEST refers to:
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
#   --work-dir DIR         staging directory, owned by the user that runs this;
#                          keep it on the same filesystem as the data directory
#                          so tables can be hard-linked
#                          (default: <data-dir>/../.nhb-snapshot-work; as root
#                          there is no default: give one that only root can write)
#   --tool PATH            the nhb-snapshot binary (default: bin/nhb-snapshot of
#                          the install root, then PATH, when the user running the
#                          script or root owns it; as root there is no default:
#                          give one in a place only root can write)
#   --node-binary PATH     the node binary to describe in the manifest
#                          (default: /opt/nhbchain/bin/nhb; pass it on a host
#                          that was not installed by scripts/deployvalidator.sh)
#   --binary-version TEXT  version of that binary   (default: git describe)
#   --binary-commit TEXT   full source commit id of that binary (default: git
#                          rev-parse HEAD of the install root, the directory
#                          above the binary's). Required when that cannot be read.
#   --allow-unknown-binary publish although the manifest cannot name the commit.
#                          New nodes refuse such a snapshot unless they pass
#                          --allow-binary-mismatch. For tests only.
#   --keep N               after publishing, keep only the N newest snapshots
#                          (by height, per chain) in --out-dir and delete the
#                          archives and manifests of older ones. Default: keep
#                          all. The one just made, and the one manifest.json
#                          names, are never deleted, and nothing but files this
#                          script names is ever deleted.
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
ALLOW_UNKNOWN_BINARY=0
MAX_PASSES=20
WRITE_LATEST=1
KEEP=all
KEEP_WORK=0
# Why the last pass failed, for the message that ends the run when none succeeds.
PASS_ERROR=''
STAGE=''
PACK_TMP=''
VERIFY_DIR=''
LOCK_DIR=''

log() { printf '[snapshot] %s\n' "$*" >&2; }
warn() { printf '[snapshot][WARN] %s\n' "$*" >&2; }
die() { printf '[snapshot][ERROR] %s\n' "$*" >&2; exit 1; }
# pass_error says why a pass failed (it is retried) and remembers it.
pass_error() { PASS_ERROR="$*"; warn "$*"; }

usage() { sed -n '2,/^set -euo pipefail/p' "${BASH_SOURCE[0]}" | sed -e '$d' -e 's/^# \{0,1\}//' ; }

# ---------------------------------------------------------------------------
# Who is who, and what a file is. Every decision about privilege and about
# whether a name in the node's directory is a regular file goes through the
# functions of this block, so a test can put a stand-in in front of one and
# report a link, or root, without having the privilege to make either.
# ---------------------------------------------------------------------------

current_uid() { id -u; }
# owner_and_mode PATH prints "<owner uid> <octal mode>".
owner_and_mode() { stat -c '%u %a' -- "$1"; }
owner_name() { stat -c '%U' -- "$1" 2>/dev/null || stat -c '%u' -- "$1"; }

# file_kind PATH sets KIND to what PATH is, without following a link: regular,
# symlink, directory, other or missing. (It sets a variable instead of printing
# so that it needs no subshell: a pass asks about every file.)
KIND=''
file_kind() {
  local path=$1
  if [[ -L "${path}" ]]; then KIND=symlink
  elif [[ -f "${path}" ]]; then KIND=regular
  elif [[ -d "${path}" ]]; then KIND=directory
  elif [[ -e "${path}" ]]; then KIND=other
  else KIND=missing
  fi
}

kind_text() {
  case "$1" in
    symlink) echo 'a symbolic link' ;;
    directory) echo 'a directory' ;;
    missing) echo 'missing' ;;
    *) echo 'not a regular file' ;;
  esac
}

# is_database_name succeeds for the names of the files of a chain database (the
# allow-list; a file with one of these names is not necessarily one the
# database refers to).
is_database_name() { [[ "$1" =~ ^(CURRENT|MANIFEST-[0-9]{6,}|[0-9]{6,}\.(log|ldb|sst))$ ]]; }

# not_plain ends the run: name $1 in the node's directory is not a regular file.
not_plain() {
  die "refusing to run: $1 in ${DATA_DIR} is $(kind_text "$2"), not a regular file. Only the node writes files of that name, and this script never follows a link or reads anything else. Nothing was published."
}

# plain_or_gone PATH succeeds for a regular file and fails, quietly, for one that
# is not there (a compaction may have removed it). A link, a directory or any
# other kind of file ends the run.
plain_or_gone() {
  file_kind "$1"
  case "${KIND}" in
    regular) return 0 ;;
    missing) return 1 ;;
    *) not_plain "${1##*/}" "${KIND}" ;;
  esac
}

# read_state describes the node's directory as far as a copy depends on it, and
# sets:
#   STATE          one line; two reads agree when their STATE are equal
#   STATE_CURRENT  the MANIFEST that CURRENT names
#   STATE_MSIZE    its size
#   STATE_TABLE    the name of one table file (for the hard link probe)
#   STATE_UNSAFE   "name|kind" lines for every name of the database that is not a
#                  regular file (it then reads nothing else)
# It follows no link. The MANIFEST size is read BEFORE a copy is made: every table
# the MANIFEST refers to was complete before the MANIFEST said so.
read_state() {
  local path name current='' journals=''
  STATE=''; STATE_CURRENT=''; STATE_MSIZE=''; STATE_TABLE=''; STATE_UNSAFE=''
  for path in "${DATA_DIR}"/*; do
    [[ -e "${path}" || -L "${path}" ]] || continue
    name=${path##*/}
    is_database_name "${name}" || continue
    file_kind "${path}"
    case "${KIND}" in
      regular) ;;
      # Gone since the listing was made: a journal rotated, a table was compacted
      # away. That happens all the time on a running node and is not an anomaly.
      missing) continue ;;
      *) STATE_UNSAFE+="${name}|${KIND}"$'\n'; continue ;;
    esac
    case "${name}" in
      *.log) journals+=" ${name}" ;;
      *.ldb|*.sst) STATE_TABLE=${name} ;;
    esac
  done
  [[ -z "${STATE_UNSAFE}" ]] || return 0
  file_kind "${DATA_DIR}/CURRENT"
  [[ "${KIND}" == regular ]] || return 1
  IFS= read -r -N 64 current < "${DATA_DIR}/CURRENT" || true
  current=${current//$'\r'/}
  current=${current//$'\n'/}
  [[ "${current}" =~ ^MANIFEST-[0-9]{6,}$ ]] || return 1
  file_kind "${DATA_DIR}/${current}"
  [[ "${KIND}" == regular ]] || return 1
  STATE_MSIZE=$(stat -c '%s' -- "${DATA_DIR}/${current}") || return 1
  STATE_CURRENT=${current}
  STATE="CURRENT ${current}; MANIFEST size ${STATE_MSIZE}; journals${journals}"
}

# refuse_unsafe ends the run when read_state found a name of the database that
# is not a regular file. It runs in the main shell, so the whole run ends.
refuse_unsafe() {
  [[ -n "${STATE_UNSAFE}" ]] || return 0
  local name kind
  while IFS='|' read -r name kind; do
    [[ -n "${name}" ]] || continue
    not_plain "${name}" "${kind}"
  done <<< "${STATE_UNSAFE}"
}

# mkdir_private [-p] DIR makes a directory nobody but its owner can enter, with no
# moment in between at which it is more open (the umask, not a chmod afterwards).
mkdir_private() ( umask 077; mkdir "$@" )

# make_private_dir makes the directory $1, and dies when it cannot be one this
# script alone can use: it must be new (or already ours), a real directory and
# not a link, and not writable by anyone else.
make_private_dir() {
  local dir=$1
  if [[ ! -e "${dir}" && ! -L "${dir}" ]]; then
    mkdir_private "${dir}" || die "cannot create ${dir}"
  fi
  if [[ -L "${dir}" || ! -d "${dir}" ]]; then die "${dir} is not a plain directory (it is a link, or not a directory): refusing to stage there"; fi
  if [[ ! -O "${dir}" ]]; then die "${dir} belongs to another user: refusing to stage there"; fi
}

# root_only_path succeeds when nobody but root can change what the path names:
# the path belongs to root and cannot be written by its group or by others, and
# neither can any directory above it, except that a directory others can write
# but whose entries only their owners can remove (sticky, like /tmp) counts, the
# entry below it being root's. As root this script reads the data directory,
# executes the tool and writes its staging and output; all of them have to be
# places nobody else can change.
root_only_path() {
  local path=$1 child='' owner mode parent
  path=$(readlink -f -- "${path}" 2>/dev/null) || return 1
  while :; do
    read -r owner mode < <(owner_and_mode "${path}" 2>/dev/null) || return 1
    [[ "${owner}" == "0" && "${mode}" =~ ^[0-7]+$ ]] || return 1
    if [[ -z "${child}" ]]; then
      (( (8#${mode} & 8#022) == 0 )) || return 1
    else
      (( (8#${mode} & 8#022) == 0 || (8#${mode} & 8#1000) != 0 )) || return 1
    fi
    # The top is where a directory is its own parent.
    parent=$(dirname -- "${path}")
    [[ "${parent}" != "${path}" ]] || return 0
    child=${path}
    path=${parent}
  done
}

# nearest_existing prints the deepest directory above (or at) the path that exists.
nearest_existing() {
  local path=$1 parent
  while [[ ! -e "${path}" && ! -L "${path}" ]]; do
    parent=$(dirname -- "${path}")
    [[ "${parent}" != "${path}" ]] || break
    path=${parent}
  done
  echo "${path}"
}

# trusted_tool succeeds when the user running the script, or root, owns the file
# and nobody else can write it: a binary another user can replace is never
# executed by default.
trusted_tool() {
  local owner mode
  read -r owner mode < <(owner_and_mode "$1" 2>/dev/null) || return 1
  [[ "${mode}" =~ ^[0-7]+$ ]] || return 1
  [[ "${owner}" == "0" || "${owner}" == "$(current_uid)" ]] && (( (8#${mode} & 8#022) == 0 ))
}

# one_pass makes a copy of the database in ${STAGE} and succeeds only if the
# node's directory did not change while it was made. It leaves the state it
# started from in ${PASS_STATE}.
one_pass() {
  local kind name path refs got want manifest src err
  local tables=() journals=() prevs=() stale=() links=() missing=() from=()
  read_state || { pass_error "cannot read the state of ${DATA_DIR}"; return 1; }
  refuse_unsafe
  PASS_STATE=${STATE}
  manifest=${STATE_CURRENT}
  want=${STATE_MSIZE}

  make_private_dir "${STAGE}"
  # Drop what an earlier pass staged, except tables (immutable; which of them stay
  # is decided below, by the MANIFEST).
  for path in "${STAGE}"/*; do
    [[ -e "${path}" || -L "${path}" ]] || continue
    case "${path##*/}" in *.ldb|*.sst) continue ;; esac
    stale+=("${path}")
  done
  if [[ ${#stale[@]} -gt 0 ]]; then rm -f -- "${stale[@]}"; stale=(); fi

  # 1. CURRENT and the MANIFEST it names.
  if ! err=$(cp -P -t "${STAGE}" -- "${DATA_DIR}/${manifest}" "${DATA_DIR}/CURRENT" 2>&1); then
    pass_error "cannot copy ${manifest} and CURRENT: ${err}"
    return 1
  fi
  for name in "${manifest}" CURRENT; do
    file_kind "${STAGE}/${name}"
    [[ "${KIND}" == regular ]] || { pass_error "the copy of ${name} is $(kind_text "${KIND}")"; return 1; }
  done

  # 2. What that copied MANIFEST refers to: the tables it lists and the journal it
  # names. Nothing else is copied.
  if ! refs=$("${TOOL}" refs --data-dir "${STAGE}" 2>&1); then
    pass_error "the copied MANIFEST cannot be read (${refs}); retrying"
    return 1
  fi
  while read -r kind name; do
    case "${kind}" in
      manifest) [[ "${name}" == "${manifest}" ]] || { pass_error "CURRENT changed while the MANIFEST was copied; retrying"; return 1; } ;;
      journal) [[ "${name}" =~ ^[0-9]{6,}\.log$ ]] || { pass_error "the tool named a journal '${name}'"; return 1; }; journals+=("${name}") ;;
      prev-journal) [[ "${name}" =~ ^[0-9]{6,}\.log$ ]] || { pass_error "the tool named a journal '${name}'"; return 1; }; prevs+=("${name}") ;;
      table) [[ "${name}" =~ ^[0-9]{6,}\.ldb$ ]] || { pass_error "the tool named a table '${name}'"; return 1; }; tables+=("${name}") ;;
      *) pass_error "unexpected line from ${TOOL} refs: ${kind} ${name}"; return 1 ;;
    esac
  done <<< "${refs}"
  [[ ${#journals[@]} -gt 0 ]] || { pass_error "the MANIFEST names no journal"; return 1; }

  # Tables an earlier pass staged that the MANIFEST no longer lists go; those it
  # lists and that are linked to the node's table stay; the rest are made now.
  for path in "${STAGE}"/*.ldb "${STAGE}"/*.sst; do
    [[ -e "${path}" || -L "${path}" ]] || continue
    name=${path##*/}
    if [[ " ${tables[*]:-} " != *" ${name} "* ]]; then stale+=("${path}"); fi
  done
  for name in ${tables[@]+"${tables[@]}"}; do
    src="${DATA_DIR}/${name}"
    plain_or_gone "${src}" || { pass_error "table ${name}, which the MANIFEST lists, could not be copied (removed by a compaction?)"; return 1; }
    file_kind "${STAGE}/${name}"
    if [[ "${KIND}" == regular && "${STAGE}/${name}" -ef "${src}" ]]; then continue; fi
    if [[ "${KIND}" != missing ]]; then stale+=("${STAGE}/${name}"); fi
    links+=("${src}")
  done
  if [[ ${#stale[@]} -gt 0 ]]; then rm -f -- "${stale[@]}"; fi
  if [[ ${#links[@]} -gt 0 ]] && ! ln -P -t "${STAGE}" -- "${links[@]}" 2>/dev/null; then
    # A hard link is not possible (another filesystem), or a table went: copy what
    # is not staged yet.
    for src in "${links[@]}"; do
      file_kind "${STAGE}/${src##*/}"
      if [[ "${KIND}" != regular ]]; then missing+=("${src}"); fi
    done
    if [[ ${#missing[@]} -gt 0 ]] && ! cp -P -t "${STAGE}" -- "${missing[@]}" 2>/dev/null; then
      for src in "${missing[@]}"; do rm -f -- "${STAGE}/${src##*/}"; done
    fi
  fi
  for name in ${tables[@]+"${tables[@]}"}; do
    file_kind "${STAGE}/${name}"
    [[ "${KIND}" == regular ]] || { pass_error "table ${name}, which the MANIFEST lists, could not be copied (removed by a compaction?)"; return 1; }
  done

  # 3. The journal, last.
  for name in "${journals[@]}"; do from+=("${DATA_DIR}/${name}"); done
  for name in ${prevs[@]+"${prevs[@]}"}; do
    if plain_or_gone "${DATA_DIR}/${name}"; then from+=("${DATA_DIR}/${name}"); fi
  done
  plain_or_gone "${DATA_DIR}/${journals[0]}" || { pass_error "journal ${journals[0]}, which the MANIFEST names, could not be copied (rotated?)"; return 1; }
  if ! err=$(cp -P -t "${STAGE}" -- "${from[@]}" 2>&1); then
    pass_error "journal ${journals[0]}, which the MANIFEST names, could not be copied (rotated?): ${err}"
    return 1
  fi
  for src in "${from[@]}"; do
    file_kind "${STAGE}/${src##*/}"
    [[ "${KIND}" == regular ]] || { pass_error "the copy of ${src##*/} is $(kind_text "${KIND}")"; return 1; }
  done

  read_state || { pass_error "cannot read the state of ${DATA_DIR} after the copy"; return 1; }
  refuse_unsafe
  if [[ "${STATE}" != "${PASS_STATE}" ]]; then
    pass_error "the node's database changed during the pass (a flush or compaction); retrying"
    return 1
  fi
  # The copied MANIFEST must be exactly as long as it was when the pass began.
  got=$(stat -c '%s' -- "${STAGE}/${manifest}") || { pass_error "cannot read the copy of ${manifest}"; return 1; }
  if [[ "${want}" != "${got}" ]]; then
    pass_error "the copied MANIFEST has ${got} bytes, expected ${want}; retrying"
    return 1
  fi
  return 0
}

# prune_old_snapshots keeps the ${KEEP} newest snapshots of each chain in
# ${OUT_DIR} and removes the archives and manifests of the older ones. It removes
# only regular files named exactly as this script names them, never the snapshot
# just made, and never the one manifest.json names.
prune_old_snapshots() {
  [[ "${KEEP}" != "all" ]] || return 0
  local path base listing prefix height name previous='' count=0 latest=''
  file_kind "${OUT_DIR}/manifest.json"
  if [[ "${KIND}" == regular ]]; then
    latest=$("${TOOL}" manifest show --manifest "${OUT_DIR}/manifest.json" --field archive.name 2>/dev/null || true)
  fi
  listing=$(for path in "${OUT_DIR}"/nhb-snapshot-*.tar.gz; do
    file_kind "${path}"
    [[ "${KIND}" == regular ]] || continue
    base=${path##*/}
    [[ "${base}" =~ ^nhb-snapshot-([0-9a-f]{8})-h([0-9]{10})\.tar\.gz$ ]] || continue
    echo "${BASH_REMATCH[1]} ${BASH_REMATCH[2]} ${base}"
  done | LC_ALL=C sort -k1,1 -k2,2nr)
  while read -r prefix height name; do
    [[ -n "${name}" ]] || continue
    if [[ "${prefix}" != "${previous}" ]]; then previous=${prefix}; count=0; fi
    count=$((count + 1))
    if (( count <= KEEP )) || [[ "${name}" == "${archive_name}" || "${name}" == "${latest}" ]]; then continue; fi
    rm -f -- "${OUT_DIR}/${name}" "${OUT_DIR}/${name%.tar.gz}.manifest.json"
    log "removed the older snapshot ${name} and its manifest (--keep ${KEEP})"
  done <<< "${listing}"
}

cleanup() {
  local rc=$?
  if [[ -n "${PACK_TMP}" ]]; then rm -rf -- "${PACK_TMP}" 2>/dev/null || true; fi
  if [[ -n "${VERIFY_DIR}" ]]; then rm -rf -- "${VERIFY_DIR}" 2>/dev/null || true; fi
  if [[ -n "${STAGE}" && "${KEEP_WORK}" != "1" ]]; then rm -rf -- "${STAGE}" 2>/dev/null || true; fi
  if [[ -n "${LOCK_DIR}" ]]; then rm -rf -- "${LOCK_DIR}" 2>/dev/null || true; fi
  return "${rc}"
}

main() {
  local pass sig prev_sig agreed run_uid data_uid data_mode owner found candidate
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --data-dir) DATA_DIR="${2:-}"; shift 2 ;;
      --out-dir) OUT_DIR="${2:-}"; shift 2 ;;
      --work-dir) WORK_DIR="${2:-}"; shift 2 ;;
      --tool) TOOL="${2:-}"; shift 2 ;;
      --node-binary) NODE_BINARY="${2:-}"; shift 2 ;;
      --binary-version) BINARY_VERSION="${2:-}"; shift 2 ;;
      --binary-commit) BINARY_COMMIT="${2:-}"; shift 2 ;;
      --allow-unknown-binary) ALLOW_UNKNOWN_BINARY=1; shift ;;
      --keep) KEEP="${2:-}"; shift 2 ;;
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
  [[ "${KEEP}" == "all" || "${KEEP}" =~ ^[1-9][0-9]{0,5}$ ]] || die "--keep must be a whole number, at least 1 (the number of snapshots to keep); without it every snapshot is kept"
  [[ -d "${DATA_DIR}" ]] || die "data directory ${DATA_DIR} does not exist"
  [[ ! -L "${DATA_DIR}" ]] || die "data directory ${DATA_DIR} is a symbolic link: refusing to read through it (give the directory itself)"
  file_kind "${DATA_DIR}/CURRENT"
  case "${KIND}" in
    regular) ;;
    missing) die "${DATA_DIR} has no CURRENT file: it is not the data directory of a node (the chain database is directly inside it)" ;;
    *) not_plain CURRENT "${KIND}" ;;
  esac
  DATA_DIR=$(cd "${DATA_DIR}" && pwd -P)

  # Who runs this, and whose directory it is.
  run_uid=$(current_uid)
  data_uid=$(owner_and_mode "${DATA_DIR}") || die "cannot tell who owns ${DATA_DIR}"
  data_mode=${data_uid##* }
  data_uid=${data_uid%% *}
  if [[ "${run_uid}" == "0" && "${data_uid}" != "0" ]]; then
    owner=$(owner_name "${DATA_DIR}")
    die "refusing to run as root: ${DATA_DIR} belongs to ${owner}, who can put anything in it, and what this script copies is published. Run it as that user instead: sudo -u ${owner} bash ${BASH_SOURCE[0]} <the same arguments>"
  fi
  # A directory that is root's but that its group or others can write (root:nhb
  # 0775, say) is the node user's in every way that matters here: it can put a link
  # or a file of its own choosing in it, and root would read it. So is one that is
  # closed but stands in a directory someone else owns or can write: that user can
  # rename it away at any moment of a run that lasts minutes and put a directory of
  # its own under the name. The place and every directory above it have to be root's,
  # as they have to be for the tool, the work directory and the output.
  if [[ "${run_uid}" == "0" ]]; then
    [[ "${data_mode}" =~ ^[0-7]+$ ]] || die "cannot tell who can write ${DATA_DIR}"
    if (( (8#${data_mode} & 8#022) != 0 )); then
      die "refusing to run as root: ${DATA_DIR} is root's, but its group or others can write it (mode ${data_mode}), so a user other than root can put anything in it, and what this script copies is published. Make it writable by root alone (chmod go-w ${DATA_DIR}), or run this as the user that runs the node."
    fi
    root_only_path "${DATA_DIR}" || die "refusing to run as root: ${DATA_DIR} is root's, but a directory above it can be changed by someone other than root, so that user can rename ${DATA_DIR} away and put a directory of its own under the name at any moment of a run that lasts minutes, and what this script copies is published. Make every directory above it root's and writable by root alone (namei -l ${DATA_DIR} lists them), or run this as the user that runs the node."
  fi

  if [[ -z "${TOOL}" && "${run_uid}" == "0" ]]; then
    die "running as root: --tool is required (the nhb-snapshot binary, in a place only root can write). The default places are the node user's, and root would execute whatever is there."
  fi
  if [[ -z "${TOOL}" ]]; then
    for candidate in /opt/nhbchain/bin/nhb-snapshot "${REPO_ROOT}/bin/nhb-snapshot" "$(command -v nhb-snapshot 2>/dev/null || true)"; do
      [[ -n "${candidate}" && -x "${candidate}" ]] || continue
      if trusted_tool "${candidate}"; then TOOL=${candidate}; break; fi
      warn "not using ${candidate} by default: it is owned by another user, or others can write it (pass --tool to use it anyway)"
    done
  fi
  [[ -n "${TOOL}" && -x "${TOOL}" ]] || die "the nhb-snapshot binary was not found; build it with 'go build -o bin/nhb-snapshot ./cmd/nhb-snapshot' and pass --tool"
  # Resolve the tool once, here, and use the resolved path from now on: what is vetted
  # below (as root) has to be the file that is executed, not a link, in a directory
  # someone else can write, that leads to a file that is vetted.
  TOOL=$(readlink -f -- "${TOOL}") || die "cannot resolve the path of the nhb-snapshot binary ${TOOL}"
  [[ -n "${TOOL}" && -x "${TOOL}" ]] || die "the nhb-snapshot binary ${TOOL} is not an executable file"

  if [[ -z "${WORK_DIR}" ]]; then
    if [[ "${run_uid}" == "0" ]]; then
      die "running as root: --work-dir is required (an empty directory in a place only root can write). The default place is next to the node's data, which its user can change."
    fi
    WORK_DIR="$(dirname "${DATA_DIR}")/.nhb-snapshot-work"
  fi
  # Neither the staging area nor the output may live inside the node's data
  # directory: the node would see files it did not write. (Asked before either
  # is made, so a refusal leaves nothing in the node's directory.)
  case "$(readlink -m -- "${WORK_DIR}")/" in "${DATA_DIR}/"*) die "--work-dir must not be inside the data directory" ;; esac
  case "$(readlink -m -- "${OUT_DIR}")/" in "${DATA_DIR}/"*) die "--out-dir must not be inside the data directory" ;; esac
  [[ ! -L "${WORK_DIR}" ]] || die "--work-dir ${WORK_DIR} is a symbolic link: refusing to stage through it (give the directory itself)"
  if [[ "${run_uid}" == "0" ]]; then
    # Root makes no directory in a place that may be someone else's: one that is not
    # there yet is made only where the nearest directory that is there is root's
    # own (the whole path is checked below, once it exists).
    for candidate in "${OUT_DIR}" "${WORK_DIR}"; do
      [[ -d "${candidate}" ]] && continue
      found=$(nearest_existing "${candidate}")
      root_only_path "${found}" || die "running as root: ${candidate} does not exist, and ${found}, where it would be made, can be changed by someone other than root. Make it first, in a place only root can write."
    done
  fi
  mkdir -p -- "${OUT_DIR}"
  OUT_DIR=$(cd "${OUT_DIR}" && pwd -P)
  if [[ ! -e "${WORK_DIR}" ]]; then mkdir_private -p "${WORK_DIR}" || die "cannot create ${WORK_DIR}"; fi
  WORK_DIR=$(cd "${WORK_DIR}" && pwd -P)
  if [[ "${run_uid}" == "0" ]]; then
    root_only_path "${TOOL}" || die "running as root: ${TOOL} (or a directory above it) can be changed by someone other than root, and root would execute it. Use a copy in a place only root can write."
    root_only_path "${WORK_DIR}" || die "running as root: ${WORK_DIR} (or a directory above it) can be changed by someone other than root. Use a place only root can write."
    root_only_path "${OUT_DIR}" || die "running as root: ${OUT_DIR} (or a directory above it) can be changed by someone other than root, and root writes the snapshots there. Use a place only root can write."
  fi

  case "${WORK_DIR}/" in "${DATA_DIR}/"*) die "--work-dir must not be inside the data directory" ;; esac
  case "${OUT_DIR}/" in "${DATA_DIR}/"*) die "--out-dir must not be inside the data directory" ;; esac

  # The script removes what it staged in the work directory. Only ever do that in
  # a directory it made itself: an empty one, or one it marked on an earlier run.
  [[ ! -L "${WORK_DIR}" && -O "${WORK_DIR}" ]] || die "--work-dir ${WORK_DIR} belongs to another user: it has to be a directory of the user that runs this script"
  file_kind "${WORK_DIR}/.nhb-snapshot-work"
  if [[ "${KIND}" != regular ]]; then
    [[ -z "$(ls -A "${WORK_DIR}")" ]] || die "--work-dir ${WORK_DIR} is not empty and was not made by this script; give it an empty directory"
    : > "${WORK_DIR}/.nhb-snapshot-work"
  fi

  # Only one snapshot at a time may use a work directory. mkdir is atomic.
  if ! mkdir_private "${WORK_DIR}/.lock" 2>/dev/null; then
    local holder
    holder=$(cat "${WORK_DIR}/.lock/pid" 2>/dev/null || true)
    if [[ -n "${holder}" ]] && kill -0 "${holder}" 2>/dev/null; then
      die "another make-snapshot.sh (pid ${holder}) is using ${WORK_DIR}"
    fi
    warn "removing a stale lock left by pid ${holder:-unknown}"
    rm -rf -- "${WORK_DIR}/.lock"
    mkdir_private "${WORK_DIR}/.lock" || die "cannot take the lock ${WORK_DIR}/.lock"
  fi
  LOCK_DIR="${WORK_DIR}/.lock"
  echo "$$" > "${LOCK_DIR}/pid"

  STAGE="${WORK_DIR}/stage"
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  # -------------------------------------------------------------------------
  # 0. what is being described
  # -------------------------------------------------------------------------
  local main_pid running_sha
  INSTALL_ROOT=$(cd "$(dirname "${NODE_BINARY}")/.." 2>/dev/null && pwd || echo '')
  COMMIT_WHY=''
  if [[ -z "${BINARY_COMMIT}" ]]; then
    BINARY_COMMIT=unknown
    if [[ -z "${INSTALL_ROOT}" ]]; then
      COMMIT_WHY="${NODE_BINARY} is not in a directory that exists, so there is no checkout to read the commit from"
    elif ! command -v git >/dev/null 2>&1; then
      COMMIT_WHY="git is not installed"
    elif found=$(git -C "${INSTALL_ROOT}" rev-parse HEAD 2>/dev/null); then
      BINARY_COMMIT=${found}
    else
      # Typically a checkout that another user owns, read as root: git refuses it.
      COMMIT_WHY="git could not read ${INSTALL_ROOT}: $(git -C "${INSTALL_ROOT}" rev-parse HEAD 2>&1 | head -n 1 || true)"
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

  # A manifest that does not name the commit is one no new node can accept:
  # deployvalidator.sh needs it to know what to build and check out. It is never
  # published unless the operator says so, and that is decided here, before the
  # long copy.
  if [[ ! "${BINARY_COMMIT}" =~ ^[0-9a-f]{40}([0-9a-f]{24})?$ ]]; then
    if [[ -z "${COMMIT_WHY}" ]]; then
      COMMIT_WHY="--binary-commit '${BINARY_COMMIT}' is not a full commit id (40 lower-case hex digits, as git rev-parse HEAD prints it)"
    fi
    warn "the source commit of the node binary is not known: ${COMMIT_WHY}"
    if [[ "${ALLOW_UNKNOWN_BINARY}" != "1" ]]; then
      die "a snapshot that does not name the commit its node was built from is a dead end for every new node: pass --binary-commit with the full commit id (git rev-parse HEAD in the checkout the binary was built from), and --node-binary when the binary is not ${NODE_BINARY}; --allow-unknown-binary publishes it anyway"
    fi
    warn "publishing it because --allow-unknown-binary was given: a new node can use this snapshot only with --allow-binary-mismatch"
  fi

  # Space check: tables are hard-linked when the staging directory allows it.
  local avail_kb probe used_kb
  avail_kb=$(df -Pk "${WORK_DIR}" 2>/dev/null | awk 'NR==2 {print $4}' || true)
  read_state || true
  refuse_unsafe
  if [[ -n "${STATE_TABLE}" && -n "${avail_kb}" ]]; then
    probe="${WORK_DIR}/.linkprobe.$$"
    if ln -P -- "${DATA_DIR}/${STATE_TABLE}" "${probe}" 2>/dev/null; then
      rm -f -- "${probe}"
      log "tables will be hard-linked (same filesystem)"
    else
      used_kb=$(du -sk "${DATA_DIR}" 2>/dev/null | awk '{print $1}' || echo 0)
      warn "hard links are not possible from ${DATA_DIR} to ${WORK_DIR}; tables will be copied (${used_kb} KB needed, ${avail_kb} KB free)"
      if [[ "${used_kb}" =~ ^[0-9]+$ && "${avail_kb}" =~ ^[0-9]+$ && "${avail_kb}" -lt $(( used_kb + used_kb / 5 )) ]]; then
        die "not enough free space in ${WORK_DIR} for a copy of the database"
      fi
    fi
  fi

  # -------------------------------------------------------------------------
  # 1. copy until two consecutive passes agree
  # -------------------------------------------------------------------------
  rm -rf -- "${STAGE}"
  prev_sig=''
  agreed=0
  for ((pass = 1; pass <= MAX_PASSES; pass++)); do
    if one_pass; then
      sig="${PASS_STATE}"
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
  [[ "${agreed}" == "1" ]] || die "no two consecutive passes agreed in ${MAX_PASSES} passes (the last trouble: ${PASS_ERROR:-none}): the database is changing too fast, or is damaged; try again"

  # The stage must hold nothing but allow-listed chain database files, none of
  # them a link. (The copy only ever names files the MANIFEST refers to, so this
  # is a canary against a bug, not the filter; the tool checks the rest.)
  local path name
  for path in "${STAGE}"/*; do
    [[ -e "${path}" || -L "${path}" ]] || continue
    name=${path##*/}
    file_kind "${path}"
    [[ "${KIND}" != symlink ]] || die "the staged copy holds a link: ${name}"
    is_database_name "${name}" || die "unexpected file in the staged copy: ${name}"
    case "$(printf '%s' "${name}" | tr '[:upper:]' '[:lower:]')" in
      *key*|*keystore*|*secret*|*passphrase*|*token*|*.pem|*.env|*sign*|*polc*|*peerstore*) die "the staged copy holds a file that looks like key material: ${name}" ;;
    esac
  done

  # -------------------------------------------------------------------------
  # 2. open the copy and read what it says
  # -------------------------------------------------------------------------
  log "opening the copy read-only"
  local info line
  info=$("${TOOL}" info --staged --data-dir "${STAGE}") || die "the staged copy does not open, or holds a file its own MANIFEST does not refer to: refusing to publish"
  rm -f -- "${STAGE}/LOCK"
  while IFS= read -r line; do log "  ${line}"; done <<< "${info}"

  # -------------------------------------------------------------------------
  # 3. pack, then prove the archive unpacks and opens
  # -------------------------------------------------------------------------
  local manifest_tmp archive_name chain_id genesis_hash height archive_size archive_sha
  PACK_TMP=$(mktemp -d "${OUT_DIR}/.tmp-XXXXXX") || die "cannot make a temporary directory in ${OUT_DIR}"
  local pack_args=(pack --data-dir "${STAGE}" --out-dir "${PACK_TMP}"
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
  VERIFY_DIR=$(mktemp -d "${WORK_DIR}/verify.XXXXXX") || die "cannot make a temporary directory in ${WORK_DIR}"
  "${TOOL}" extract --manifest "${manifest_tmp}" --archive "${PACK_TMP}/${archive_name}" \
    --target "${VERIFY_DIR}/data" --chain-id "${chain_id}" --genesis-hash "${genesis_hash}" >/dev/null \
    || die "the archive does not unpack and open again: refusing to publish"
  rm -rf -- "${VERIFY_DIR}"

  # -------------------------------------------------------------------------
  # 4. publish: archive and its manifest first, the fixed-name manifest last
  # -------------------------------------------------------------------------
  local base
  base=${archive_name%.tar.gz}
  mv -f -- "${PACK_TMP}/${archive_name}" "${OUT_DIR}/${archive_name}"
  mv -f -- "${PACK_TMP}/${base}.manifest.json" "${OUT_DIR}/${base}.manifest.json"
  if [[ "${WRITE_LATEST}" == "1" ]]; then
    mv -f -- "${manifest_tmp}" "${OUT_DIR}/manifest.json"
  fi
  chmod 0644 -- "${OUT_DIR}/${archive_name}" "${OUT_DIR}/${base}.manifest.json" 2>/dev/null || true
  if [[ "${WRITE_LATEST}" == "1" ]]; then chmod 0644 -- "${OUT_DIR}/manifest.json" 2>/dev/null || true; fi

  log "done: ${OUT_DIR}/${archive_name}"
  log "      chain ${chain_id}, height ${height}, ${archive_size} bytes, sha256 ${archive_sha}"
  log "included files: CURRENT, MANIFEST-*, *.log, *.ldb, *.sst (the chain database, no key of any kind)"
  local skipped
  skipped=$(cd "${DATA_DIR}" && for f in * .[!.]*; do
    [[ -e "${f}" || -L "${f}" ]] || continue
    case "${f}" in
      CURRENT|MANIFEST-*|*.log|*.ldb|*.sst)
        if [[ ! -e "${STAGE}/${f}" ]]; then echo "${f} (the MANIFEST does not refer to it: not copied)"; fi ;;
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
  prune_old_snapshots
}

# main only runs when the script is executed, so that a test can source it and
# call its functions.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
