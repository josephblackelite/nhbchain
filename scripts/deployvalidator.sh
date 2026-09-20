#!/usr/bin/env bash
# deployvalidator.sh -- bring up a new node of the live network from a snapshot.
#
# Block sync from genesis does NOT work on this network (see
# docs/validators/snapshot-onboarding.md), so this script never starts a node
# from an empty data directory. It installs the node, puts a verified snapshot
# of the chain database in its data directory, starts the node as a NON-voting
# follower (a fresh key that is not registered anywhere never counts toward
# consensus), waits until the follower is at the network tip, and only then
# submits the registration steps and prints what to do next.
#
# Every step stops the script with an error when it fails; nothing is skipped
# quietly. It is safe to run again: what is already in place is left alone, and
# a node that is already running is not restarted unless something changed. A
# node whose data directory already holds this network's chain is never given a
# snapshot, so a run that installs none does not fetch a manifest at all: what
# the snapshot host publishes, or whether it answers, cannot change that run.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/.." && pwd)

INSTALL_ROOT="${NHB_INSTALL_ROOT:-/opt/nhbchain}"
CONFIG_DIR="${NHB_CONFIG_DIR:-/etc/nhbchain}"
STATE_DIR="${NHB_STATE_DIR:-/var/lib/nhbchain}"
SERVICE_USER=nhb
VALIDATOR_KEY_FILE="${CONFIG_DIR}/validator.key"
DATA_DIR="${STATE_DIR}/nhb-data"
DOWNLOAD_DIR="${STATE_DIR}/.snapshot-download"

# The network this release is pinned to. The chain id is the first 8 bytes of
# the genesis block hash, and the genesis hash follows from the genesis file, so
# the three values below agree with each other and with the live network.
# tests/config/shipped_genesis_test.go checks every one of them.
NETWORK_ID_DEFAULT='18346390202490284624'
GENESIS_HASH_DEFAULT='0xfe9b78af9223ea50f456f63c41084dd99bac4aaa3a790a10fcc26d1dc63210a2'
# The live network's genesis file. Its hash is the chain id, so the copy this
# script runs from must match byte for byte.
GENESIS_FILE_REL='config/genesis.relaunch.json'
GENESIS_SHA256='10932798a0058ae35b135dae1a6ee1bdf6a8bc528a55c1eeb3e9eaab534f4b3b'
LISTEN_ADDR_DEFAULT='0.0.0.0:6001'
RPC_ADDR_DEFAULT='127.0.0.1:8545'

BOOTNODE="${NHB_BOOTNODE:-}"
SNAPSHOT_URL="${NHB_SNAPSHOT_URL:-}"
TIP_RPC="${NHB_TIP_RPC_URL:-}"
NETWORK_ID="${NETWORK_ID_DEFAULT}"
LISTEN_ADDR="${LISTEN_ADDR_DEFAULT}"
RPC_ADDR="${RPC_ADDR_DEFAULT}"
RESET_STATE=0
BENEFICIARY=''
EXTERNAL_ADDRESS=''
EXTERNAL_ADDRESS_HOSTPORT=''
ALLOW_INSECURE_HTTP=0
ALLOW_BINARY_MISMATCH=0
ALLOW_EXISTING_KEY=0
MAX_SNAPSHOT_AGE=''
TIP_HASH=''
STATE_ROOT=''
MAX_SNAPSHOT_GIB=16
# The height of the snapshot this run installed; empty when it installed none.
SNAPSHOT_HEIGHT=''
MAX_LAG_BLOCKS=3
SYNC_INTERVAL="${NHB_SYNC_INTERVAL:-5s}"
SYNC_TIMEOUT_SECS=7200
CLI_RETRY_DELAY="${CLI_RETRY_DELAY:-5}"

usage() {
  cat <<'EOF'
Usage:
  bash scripts/deployvalidator.sh --beneficiary <nhb1...> --snapshot-url <url> --bootnode <host:port> [options]

Required:
  --beneficiary <nhb1...>  Wallet to receive this validator's epoch reward
                           payouts. Without it they would accumulate at this
                           validator's own address, whose key never leaves this
                           server. MUST differ from this validator's own node
                           address (printed at the end): the chain rejects a
                           beneficiary that matches the validator's own address.
  --snapshot-url <url>     Where the snapshot is published: a directory URL that
                           holds manifest.json and the archive it names.
                           https only (see --allow-insecure-http). There is no
                           default: which snapshots to trust is your decision.
                           May be given as NHB_SNAPSHOT_URL instead.
  --bootnode <host:port>   A peer of the network to sync new blocks from. Plain
                           host:port, not an enode:// URI. There is no default.
                           May be given as NHB_BOOTNODE instead.

Options:
  --tip-rpc <url>          RPC URL of a node you trust. Used to decide when this
                           node has caught up: its newest blocks are compared
                           with that node's, and a node whose blocks differ from
                           that node's is refused. Without it the age of the
                           newest block is used. May be given as NHB_TIP_RPC_URL.
  --max-lag-blocks <n>     How far from the network tip (either side) still
                           counts as caught up (default 3, at most 15; needs
                           --tip-rpc).
  --sync-timeout <secs>    Give up waiting for the catch-up after this long
                           (default 7200).
  --max-snapshot-age <d>   Refuse a snapshot created longer ago than this, for
                           example 48h. A follower can catch up only so many
                           blocks; see the documentation for the measured limit.
  --tip-hash <hex>         The tip hash the snapshot must have (32 bytes of hex),
                           as read from nodes you trust. A snapshot with another
                           tip is refused before it is downloaded.
  --state-root <hex>       The state root the snapshot must have, likewise.
  --max-snapshot-gib <n>   Refuse a snapshot whose archive, or whose unpacked
                           database, is larger than this many GiB (default 16),
                           or that the disk has no room for.
  --listen-addr <addr>     P2P listen address. Default: 0.0.0.0:6001
  --rpc-addr <addr>        Local RPC listen address. Default: 127.0.0.1:8545
  --external-address <ip>  This node's own publicly-dialable IP (the address
                           peers should use to reconnect to it). Default:
                           auto-detected from this machine's public IP.
  --reset-state            Replace the existing data directory with a snapshot.
                           The snapshot is downloaded, verified and unpacked
                           first, next to the old directory, which is not
                           touched until then; only after that is the node
                           stopped and the old directory moved aside (it is
                           never deleted). The node's p2p identity and its
                           consensus vote/lock state are carried over, so a
                           validator never forgets what it voted; it works on
                           a node that is already a validator. Use it only
                           when the data directory is broken.
  --allow-insecure-http    Accept an http:// or file:// snapshot URL.
  --allow-existing-key     Run with a validator key that is already in
                           /etc/nhbchain/validator.key although this script did
                           not create it. Only if you are sure no other node in
                           the world runs that key: two nodes with one key
                           double-sign and are slashed.
  --allow-binary-mismatch  Continue although the node built here is not the
                           binary the snapshot was taken with (see below).
  --help                   Show this help message.

What it does, in order (each step stops the script if it fails):
  1. installs Go and the build tools, builds nhb, nhb-cli and nhb-snapshot;
  2. when it will install a snapshot (an empty data directory, or
     --reset-state; a node that already holds the chain needs none, and then
     nothing is fetched): fetches the snapshot manifest and checks it is for
     the pinned network (chain id 18346390202490284624, the genesis of
     config/genesis.relaunch.json) and for the same node binary as the one
     built here;
  3. creates this validator's key ON THIS MACHINE (never pass a key in) and
     refuses to go on if that key could already be running elsewhere;
  4. downloads the snapshot (within the size limits above), verifies its sha256
     and structure, checks the unpacked database opens and matches the
     manifest, and puts it in the data directory (never over data that is
     already there, except through --reset-state);
  5. writes the node's config from the shipped config.toml (only paths, ports
     and peers change) and checks the values consensus depends on;
  6. starts nhb.service as a follower and waits until it is at the network tip:
     connected to a peer, and (after a snapshot) past the snapshot's height;
  7. only then registers the validator and prints the next steps.

Getting paid:
  Your validator's stake is delegated to it from a wallet (delegate at least
  the minimum to the node address printed at the end) or staked by the node's
  own key. Separately it earns an epoch reward for taking part in consensus,
  credited to the address passed as --beneficiary.
EOF
}

log() { echo "[INFO] $*"; }
warn() { echo "[WARN] $*" >&2; }
die() { echo "[ERROR] $*" >&2; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

as_root() { sudo "$@"; }
as_service() { sudo -u "${SERVICE_USER}" "$@"; }

parse_args() {
  local network_id_arg=''
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --beneficiary) BENEFICIARY="${2:-}"; shift 2 ;;
      --snapshot-url) SNAPSHOT_URL="${2:-}"; shift 2 ;;
      --bootnode) BOOTNODE="${2:-}"; shift 2 ;;
      --tip-rpc) TIP_RPC="${2:-}"; shift 2 ;;
      --max-lag-blocks) MAX_LAG_BLOCKS="${2:-}"; shift 2 ;;
      --sync-timeout) SYNC_TIMEOUT_SECS="${2:-}"; shift 2 ;;
      --max-snapshot-age) MAX_SNAPSHOT_AGE="${2:-}"; shift 2 ;;
      --tip-hash) TIP_HASH="${2:-}"; shift 2 ;;
      --state-root) STATE_ROOT="${2:-}"; shift 2 ;;
      --max-snapshot-gib) MAX_SNAPSHOT_GIB="${2:-}"; shift 2 ;;
      --network-id) network_id_arg="${2:-}"; shift 2 ;;
      --listen-addr) LISTEN_ADDR="${2:-}"; shift 2 ;;
      --rpc-addr) RPC_ADDR="${2:-}"; shift 2 ;;
      --external-address) EXTERNAL_ADDRESS="${2:-}"; shift 2 ;;
      --reset-state) RESET_STATE=1; shift ;;
      --allow-insecure-http) ALLOW_INSECURE_HTTP=1; shift ;;
      --allow-existing-key) ALLOW_EXISTING_KEY=1; shift ;;
      --allow-binary-mismatch) ALLOW_BINARY_MISMATCH=1; shift ;;
      --help|-h) usage; exit 0 ;;
      *) echo "[ERROR] unknown argument: $1" >&2; usage >&2; exit 1 ;;
    esac
  done
  if [[ -n "${network_id_arg}" && "${network_id_arg}" != "${NETWORK_ID_DEFAULT}" ]]; then
    die "--network-id ${network_id_arg} is not the network this release is pinned to (${NETWORK_ID_DEFAULT}); check out the release that matches the network you want to join"
  fi
}

# validate_inputs stops before anything is installed when an argument is
# missing or wrong.
validate_inputs() {
  if [[ -z "${BENEFICIARY}" ]]; then
    echo "[ERROR] --beneficiary <nhb1...> is required." >&2
    echo >&2
    echo "This validator will earn epoch rewards. Without a beneficiary wallet," >&2
    echo "those rewards accumulate at this validator's own server-only address," >&2
    echo "which you cannot conveniently spend from. Pass --beneficiary with a" >&2
    echo "wallet you actually use." >&2
    echo >&2
    usage >&2
    exit 1
  fi
  [[ "${BENEFICIARY}" =~ ^nhb1[a-z0-9]{20,80}$ ]] || die "--beneficiary '${BENEFICIARY}' is not an nhb1... address"

  [[ -n "${SNAPSHOT_URL}" ]] || die "--snapshot-url (or NHB_SNAPSHOT_URL) is required: this script starts from a snapshot and has no default location for one"
  case "${SNAPSHOT_URL}" in
    https://*) CURL_PROTO='=https' ;;
    http://*|file://*)
      [[ "${ALLOW_INSECURE_HTTP}" == "1" ]] || die "the snapshot URL is not https; pass --allow-insecure-http only if you know the path to it is trusted"
      CURL_PROTO='=https,http,file'
      ;;
    *) die "the snapshot URL must start with https://" ;;
  esac
  [[ "${SNAPSHOT_URL}" =~ ^[A-Za-z0-9:/._~%@+=,-]+$ ]] || die "the snapshot URL holds characters that are not allowed"

  [[ -n "${BOOTNODE}" ]] || die "--bootnode (or NHB_BOOTNODE) is required: a peer of the network to sync new blocks from"
  case "${BOOTNODE}" in
    enode://*) die "the bootnode must be plain host:port, not an enode:// URI (the node dials it directly)" ;;
  esac
  [[ "${BOOTNODE}" =~ ^[A-Za-z0-9._-]+:[0-9]{1,5}$ ]] || die "the bootnode '${BOOTNODE}' is not host:port"

  if [[ -n "${TIP_RPC}" ]]; then
    [[ "${TIP_RPC}" =~ ^https?://[A-Za-z0-9._:/-]+$ ]] || die "--tip-rpc '${TIP_RPC}' is not an http(s) URL"
  fi
  [[ "${LISTEN_ADDR}" =~ ^[A-Za-z0-9._:-]+$ ]] || die "--listen-addr '${LISTEN_ADDR}' is not host:port"
  [[ "${RPC_ADDR}" =~ ^[A-Za-z0-9._:-]+:[0-9]{1,5}$ ]] || die "--rpc-addr '${RPC_ADDR}' is not host:port"
  [[ "${MAX_LAG_BLOCKS}" =~ ^[0-9]+$ ]] || die "--max-lag-blocks must be a number"
  if [[ -n "${TIP_RPC}" ]] && (( 10#${MAX_LAG_BLOCKS} > 15 )); then
    die "--max-lag-blocks ${MAX_LAG_BLOCKS} is more than the 15 blocks that can be compared with --tip-rpc's"
  fi
  [[ "${SYNC_TIMEOUT_SECS}" =~ ^[0-9]+$ ]] || die "--sync-timeout must be a number of seconds"
  if [[ -n "${MAX_SNAPSHOT_AGE}" ]]; then
    [[ "${MAX_SNAPSHOT_AGE}" =~ ^[0-9]+(h|m|s)$ ]] || die "--max-snapshot-age must look like 48h"
  fi
  [[ "${MAX_SNAPSHOT_GIB}" =~ ^[1-9][0-9]{0,3}$ ]] || die "--max-snapshot-gib must be a whole number of GiB, at least 1"
  if [[ -n "${TIP_HASH}" ]]; then
    [[ "${TIP_HASH}" =~ ^(0x)?[0-9a-fA-F]{64}$ ]] || die "--tip-hash must be 32 bytes of hex (64 digits, 0x optional)"
    TIP_HASH="0x$(printf '%s' "${TIP_HASH#0x}" | tr 'A-F' 'a-f')"
  fi
  if [[ -n "${STATE_ROOT}" ]]; then
    [[ "${STATE_ROOT}" =~ ^(0x)?[0-9a-fA-F]{64}$ ]] || die "--state-root must be 32 bytes of hex (64 digits, 0x optional)"
    STATE_ROOT="0x$(printf '%s' "${STATE_ROOT#0x}" | tr 'A-F' 'a-f')"
  fi
  if [[ -n "${NHB_MASTER_TREASURY:-}" ]]; then
    die "NHB_MASTER_TREASURY is set in this environment; a node that overrides the treasury computes different state from the network. Unset it."
  fi
}

# ---------------------------------------------------------------------------
# Snapshot helpers
# ---------------------------------------------------------------------------

# fetch_file <url> <destination> <max bytes>
# curl stops at <max bytes> whatever the server sends, and what it wrote is
# removed when the download fails.
fetch_file() {
  local url=$1 dest=$2 max=$3
  as_service curl --fail --silent --show-error --location \
    --proto "${CURL_PROTO}" --proto-redir "${CURL_PROTO}" \
    --retry 3 --retry-delay 3 --connect-timeout 30 --max-time 7200 \
    --max-filesize "${max}" --output "${dest}" "${url}" \
    || { as_service rm -f "${dest}"; die "could not download ${url} (at most ${max} bytes are accepted)"; }
}

tool() { "${INSTALL_ROOT}/bin/nhb-snapshot" "$@"; }

# manifest_field <manifest> <field>
manifest_field() { as_service "${INSTALL_ROOT}/bin/nhb-snapshot" manifest show --manifest "$1" --field "$2"; }

# check_binary_identity <manifest sha256> <manifest commit> <local sha256> <local commit>
# The follower runs every later block through its own build of the node, so it
# must be the code that made the snapshot: the same binary, or the same commit.
check_binary_identity() {
  local m_sha=$1 m_commit=$2 l_sha=$3 l_commit=$4
  if [[ -n "${m_sha}" && "${m_sha}" == "${l_sha}" ]]; then
    log "the node built here is byte for byte the binary the snapshot was taken with"
    return 0
  fi
  if [[ -n "${m_commit}" && "${m_commit}" != "unknown" && "${m_commit}" == "${l_commit}" ]]; then
    log "the node was built from commit ${l_commit}, the commit the snapshot was taken with"
    return 0
  fi
  echo "[ERROR] the node built here is not the one the snapshot was taken with." >&2
  echo "        snapshot: commit ${m_commit:-unknown}, binary sha256 ${m_sha:-unknown}" >&2
  echo "        here:     commit ${l_commit:-unknown}, binary sha256 ${l_sha:-unknown}" >&2
  echo "        A node that executes blocks with different consensus code forks off the network." >&2
  echo "        Check out the commit above (git checkout ${m_commit:-<commit>}) and run this script again." >&2
  if [[ "${ALLOW_BINARY_MISMATCH}" == "1" ]]; then
    warn "continuing because --allow-binary-mismatch was given: only do this if the two builds differ in nothing that consensus executes"
    return 0
  fi
  return 1
}

# fetch_manifest downloads the manifest and stops unless it is for the pinned
# network. It sets MANIFEST_FILE.
fetch_manifest() {
  as_root install -d -m 0700 -o "${SERVICE_USER}" -g "${SERVICE_USER}" "${DOWNLOAD_DIR}"
  MANIFEST_FILE="${DOWNLOAD_DIR}/manifest.json"
  as_service rm -f "${MANIFEST_FILE}"
  fetch_file "${SNAPSHOT_URL%/}/manifest.json" "${MANIFEST_FILE}" 4194304
  local chain genesis
  chain=$(manifest_field "${MANIFEST_FILE}" chainId) || die "the manifest at ${SNAPSHOT_URL%/}/manifest.json is not a valid snapshot manifest"
  genesis=$(manifest_field "${MANIFEST_FILE}" genesisHash) || die "the manifest is not valid"
  if [[ "${chain}" != "${NETWORK_ID_DEFAULT}" || "${genesis}" != "${GENESIS_HASH_DEFAULT}" ]]; then
    die "the snapshot is for chain ${chain} (genesis ${genesis}), not the pinned network ${NETWORK_ID_DEFAULT} (genesis ${GENESIS_HASH_DEFAULT})"
  fi
  # What the operator pinned to nodes they trust is checked here, before the
  # archive is downloaded; verify and extract check it again.
  local tip root
  if [[ -n "${TIP_HASH}" ]]; then
    tip=$(manifest_field "${MANIFEST_FILE}" tipHash) || die "the manifest is not valid"
    [[ "${tip}" == "${TIP_HASH}" ]] || die "the snapshot's tip hash is ${tip}, not the ${TIP_HASH} pinned with --tip-hash"
  fi
  if [[ -n "${STATE_ROOT}" ]]; then
    root=$(manifest_field "${MANIFEST_FILE}" stateRoot) || die "the manifest is not valid"
    [[ "${root}" == "${STATE_ROOT}" ]] || die "the snapshot's state root is ${root}, not the ${STATE_ROOT} pinned with --state-root"
  fi
  log "snapshot manifest: chain ${chain}, height $(manifest_field "${MANIFEST_FILE}" height), created $(manifest_field "${MANIFEST_FILE}" createdAt)"
}

# free_kib prints how much room is left, in KiB, on the disk the node's data
# lives on.
free_kib() { df -Pk "${STATE_DIR}" 2>/dev/null | awk 'NR==2 {print $4}'; }

# check_snapshot_size <archive bytes> <unpacked bytes> stops the script, before
# anything is downloaded, when the snapshot is larger than this run accepts or
# than the disk has room for. The manifest's sizes are nobody's signed word, so
# the bound is the operator's (--max-snapshot-gib), never the manifest's own.
check_snapshot_size() {
  local size=$1 unpacked=$2 limit free need
  [[ "${size}" =~ ^[0-9]+$ && "${unpacked}" =~ ^[0-9]+$ ]] || die "the manifest's archive sizes are not numbers"
  limit=$((MAX_SNAPSHOT_GIB * 1073741824))
  if (( size > limit )); then
    die "the snapshot archive is ${size} bytes, more than the ${MAX_SNAPSHOT_GIB} GiB this run accepts (--max-snapshot-gib); nothing was downloaded"
  fi
  if (( unpacked > limit )); then
    die "the snapshot unpacks to ${unpacked} bytes, more than the ${MAX_SNAPSHOT_GIB} GiB this run accepts (--max-snapshot-gib); nothing was downloaded"
  fi
  free=$(free_kib || true)
  [[ "${free}" =~ ^[0-9]+$ ]] || die "could not tell how much disk space is free in ${STATE_DIR}"
  # The archive and the copy unpacked from it exist side by side, and the node
  # needs room to grow afterwards.
  need=$(( (size + unpacked) / 1024 + 1048576 ))
  if (( free < need )); then
    die "not enough free disk space in ${STATE_DIR}: ${need} KiB are needed (the ${size} byte archive, the ${unpacked} bytes it unpacks to, and 1 GiB for the node) and ${free} KiB are free; nothing was downloaded"
  fi
}

# install_snapshot <target> <refuse own key> downloads the archive the manifest
# names, verifies it and unpacks it into <target>, a directory that does not
# exist yet (or is empty). Nothing else on the host is touched. With <refuse own
# key> set to 1, a snapshot in which this host's own key is already a validator
# is refused: a new node must never start with a key that is validating.
install_snapshot() {
  local target=$1 refuse_own_key=$2 name size unpacked limit
  [[ -n "${MANIFEST_FILE:-}" ]] || die "internal error: a snapshot is to be installed but no manifest was fetched"
  name=$(manifest_field "${MANIFEST_FILE}" archive.name) || die "the manifest is not valid"
  size=$(manifest_field "${MANIFEST_FILE}" archive.size) || die "the manifest is not valid"
  unpacked=$(manifest_field "${MANIFEST_FILE}" archive.uncompressedSize) || die "the manifest is not valid"
  [[ "${name}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*\.tar\.gz$ ]] || die "the manifest names an archive '${name}' that is not a plain file name"
  local archive="${DOWNLOAD_DIR}/${name}"
  as_service rm -f "${archive}"
  check_snapshot_size "${size}" "${unpacked}"
  limit=$((MAX_SNAPSHOT_GIB * 1073741824))
  log "downloading ${name} (${size} bytes)"
  fetch_file "${SNAPSHOT_URL%/}/${name}" "${archive}" "${size}"
  local age=() pins=() own=()
  if [[ -n "${MAX_SNAPSHOT_AGE}" ]]; then age=(--max-age "${MAX_SNAPSHOT_AGE}"); fi
  if [[ -n "${TIP_HASH}" ]]; then pins+=(--tip-hash "${TIP_HASH}"); fi
  if [[ -n "${STATE_ROOT}" ]]; then pins+=(--state-root "${STATE_ROOT}"); fi
  if [[ "${refuse_own_key}" == "1" ]]; then own=(--reject-validator "${VALIDATOR_ADDRESS}"); fi
  log "verifying the archive and unpacking it into ${target}"
  as_service "${INSTALL_ROOT}/bin/nhb-snapshot" verify --manifest "${MANIFEST_FILE}" --archive "${archive}" \
    --chain-id "${NETWORK_ID_DEFAULT}" --genesis-hash "${GENESIS_HASH_DEFAULT}" --max-bytes "${limit}" \
    ${pins[@]+"${pins[@]}"} ${age[@]+"${age[@]}"} \
    || { as_service rm -f "${archive}"; die "the snapshot did not verify; nothing was installed"; }
  as_service "${INSTALL_ROOT}/bin/nhb-snapshot" extract --manifest "${MANIFEST_FILE}" --archive "${archive}" \
    --target "${target}" --chain-id "${NETWORK_ID_DEFAULT}" --genesis-hash "${GENESIS_HASH_DEFAULT}" --max-bytes "${limit}" \
    ${own[@]+"${own[@]}"} ${pins[@]+"${pins[@]}"} ${age[@]+"${age[@]}"} \
    || { as_service rm -f "${archive}"; die "the snapshot could not be installed; nothing was changed (${DATA_DIR} and nhb.service are as they were)"; }
  as_service rm -f "${archive}"
  as_root chmod 0700 "${target}"
  SNAPSHOT_HEIGHT=$(manifest_field "${MANIFEST_FILE}" height) || die "the manifest is not valid"
}

# ---------------------------------------------------------------------------
# Refusals
# ---------------------------------------------------------------------------

# refuse_second_node stops the script when another nhb process, other than the
# one nhb.service manages, is running on this host: two nodes started from one
# key double-sign.
refuse_second_node() {
  local main_pid pid args other=()
  main_pid=$(systemctl show -p MainPID --value nhb.service 2>/dev/null || echo 0)
  while read -r pid args; do
    [[ -n "${pid}" ]] || continue
    case "$(basename "${args%% *}")" in
      nhb|consensusd) ;;
      *) continue ;;
    esac
    [[ "${pid}" == "${main_pid}" ]] && continue
    other+=("${pid}: ${args}")
  done < <(ps -eo pid=,args= 2>/dev/null || true)
  if [[ ${#other[@]} -gt 0 ]]; then
    echo "[ERROR] another node process is running on this host, outside nhb.service:" >&2
    printf '        %s\n' "${other[@]}" >&2
    echo "        Two nodes must never run with one validator key. Stop it first." >&2
    return 1
  fi
  return 0
}

# refuse_foreign_key stops the script when the key file was not made by this
# script and nothing on this host shows it belongs to this node.
refuse_foreign_key() {
  local marker="${CONFIG_DIR}/.validator.key.created-here"
  # As root: /etc/nhbchain belongs to the service user and is closed to the
  # user who runs this script, who would otherwise see no key there.
  if ! as_root test -f "${VALIDATOR_KEY_FILE}"; then return 0; fi
  if as_root test -f "${marker}"; then return 0; fi
  if data_dir_has_data; then return 0; fi   # a node of this host already used it
  if [[ "${ALLOW_EXISTING_KEY}" == "1" ]]; then
    warn "using the validator key that was already at ${VALIDATOR_KEY_FILE} because --allow-existing-key was given"
    return 0
  fi
  echo "[ERROR] ${VALIDATOR_KEY_FILE} exists but this script did not create it and this host has no node data." >&2
  echo "        A key that was made or used elsewhere must not start a second node: two nodes with one key" >&2
  echo "        double-sign and are slashed. Remove the file to have a fresh key made here, or pass" >&2
  echo "        --allow-existing-key if you are sure that key runs nowhere else." >&2
  return 1
}

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

# render_config <template> <output>: the template with only this node's own
# values changed. It stops when the template lacks one of the lines it changes.
render_config() {
  local template=$1 out=$2
  NHB_R_LISTEN="${LISTEN_ADDR}" NHB_R_RPC="${RPC_ADDR}" NHB_R_DATA="${DATA_DIR}" \
  NHB_R_GENESIS="${INSTALL_ROOT}/${GENESIS_FILE_REL}" NHB_R_NETID="${NETWORK_ID}" \
  NHB_R_EXTERNAL="${EXTERNAL_ADDRESS_HOSTPORT}" NHB_R_BOOT="${BOOTNODE}" \
  perl -0777 -e '
    use strict; use warnings;
    my $c = <STDIN>;
    sub top { my ($k, $v) = @_; $c =~ s/^\Q$k\E = .*$/$k = $v/m or die "the config template has no line for $k\n"; }
    sub p2p { my ($k, $v) = @_; $c =~ s/^  \Q$k\E = .*$/  $k = $v/m or die "the config template has no [p2p] line for $k\n"; }
    top("ListenAddress", qq{"$ENV{NHB_R_LISTEN}"});
    top("RPCAddress", qq{"$ENV{NHB_R_RPC}"});
    top("DataDir", qq{"$ENV{NHB_R_DATA}"});
    top("GenesisFile", qq{"$ENV{NHB_R_GENESIS}"});
    top("ValidatorKeystorePath", q{""});
    top("ValidatorKMSEnv", q{"NHB_VALIDATOR_RAW_KEY"});
    top("NetworkName", q{"nhb-mainnet-validator"});
    p2p("NetworkId", $ENV{NHB_R_NETID});
    p2p("Bootnodes", qq{["$ENV{NHB_R_BOOT}"]});
    p2p("PersistentPeers", qq{["$ENV{NHB_R_BOOT}"]});
    p2p("ExternalAddress", qq{"$ENV{NHB_R_EXTERNAL}"}) if length $ENV{NHB_R_EXTERNAL};
    print $c;
  ' < "${template}" > "${out}" || return 1
}

# check_config <config>: the values consensus depends on are the network's.
check_config() {
  "${INSTALL_ROOT}/bin/nhb-snapshot" check-config --config "$1" --genesis "${INSTALL_ROOT}/${GENESIS_FILE_REL}"
}

install_config() {
  local tmp
  tmp=$(mktemp)
  render_config "${INSTALL_ROOT}/config.toml" "${tmp}" || { rm -f "${tmp}"; die "could not render the node config from ${INSTALL_ROOT}/config.toml"; }
  if ! check_config "${tmp}"; then rm -f "${tmp}"; die "the node config does not carry the values consensus depends on"; fi
  if [[ -f "${CONFIG_DIR}/config.toml" ]] && cmp -s "${tmp}" "${CONFIG_DIR}/config.toml"; then
    CONFIG_CHANGED=0
  else
    as_root install -m 0600 -o "${SERVICE_USER}" -g "${SERVICE_USER}" "${tmp}" "${CONFIG_DIR}/config.toml"
    CONFIG_CHANGED=1
  fi
  rm -f "${tmp}"
}

# write_env keeps the RPC secret of an earlier run, so running the script again
# does not change what the node reads.
write_env() {
  local secret key_hex tmp
  secret=$(as_root grep '^NHB_RPC_JWT_SECRET=' "${CONFIG_DIR}/node.env" 2>/dev/null | head -1 | cut -d= -f2- || true)
  if [[ -z "${secret}" ]]; then
    secret=$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
  fi
  JWT_SECRET="${secret}"
  key_hex=$(as_root od -An -tx1 "${VALIDATOR_KEY_FILE}" | tr -d ' \n')
  tmp=$(mktemp)
  chmod 600 "${tmp}"
  {
    echo "NHB_ENV=prod"
    echo "NHB_RPC_JWT_SECRET=${JWT_SECRET}"
    echo "NHB_VALIDATOR_RAW_KEY=${key_hex}"
  } > "${tmp}"
  unset key_hex
  if as_root test -f "${CONFIG_DIR}/node.env" && as_root cmp -s "${tmp}" "${CONFIG_DIR}/node.env"; then
    ENV_CHANGED=0
  else
    as_root install -m 0600 -o root -g root "${tmp}" "${CONFIG_DIR}/node.env"
    ENV_CHANGED=1
  fi
  rm -f "${tmp}"
}

# ---------------------------------------------------------------------------
# Data directory
# ---------------------------------------------------------------------------

service_active() { systemctl is-active --quiet nhb.service 2>/dev/null; }

# data_dir_has_data asks as root: the data directory belongs to the service user
# and is closed to the user who runs this script, which would otherwise see a
# directory full of chain data as an empty one.
data_dir_has_data() { as_root test -d "${DATA_DIR}" && [[ -n "$(as_root ls -A "${DATA_DIR}" 2>/dev/null)" ]]; }

# snapshot_needed succeeds when this run will install a snapshot: the data
# directory holds nothing, or --reset-state asked for a fresh one. A node that
# already holds this network's chain needs none, so nothing the snapshot host
# publishes, or whether it answers at all, can matter to that run.
snapshot_needed() { [[ "${RESET_STATE}" == "1" ]] || ! data_dir_has_data; }

# restore_identity <from> <to> copies the files that make the node the node it
# was, its p2p identity and what it has voted, from the data directory <from>
# into <to>.
restore_identity() {
  local from=$1 to=$2 f
  for f in p2p/node_key.json bft_sign_state.json polc_lock.json; do
    if as_root test -f "${from}/${f}"; then
      as_root mkdir -p "$(dirname "${to}/${f}")" || return 1
      as_root cp -p "${from}/${f}" "${to}/${f}" || return 1
      log "kept ${f} from the previous data directory"
    fi
  done
  as_root chown -R "${SERVICE_USER}:${SERVICE_USER}" "${to}" || return 1
}

# replace_data_dir puts a fresh snapshot in place of this node's own data
# directory. The snapshot is downloaded, verified and unpacked next to the old
# directory first, while the node runs on the old one, so a snapshot that does
# not check out changes nothing. Only then is the node stopped, what it has
# voted copied over (it can no longer change), the old directory moved aside
# (it is never deleted) and the new one moved into its place.
#
# It is the node's own data directory, so the node's own key may well be a
# validator in the snapshot (a registered validator is one): the check that
# refuses a new node's key is not made here.
replace_data_dir() {
  local stamp staged aside stopped=0 why='' rc=0
  stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
  staged="${DATA_DIR}.new-${stamp}"
  aside="${DATA_DIR}.replaced-${stamp}"
  install_snapshot "${staged}" 0
  # The node is stopped whatever state it is in. One whose data directory is
  # broken is often not "active" but restarting every few seconds
  # (Restart=on-failure), and a restart in the middle of the swap would start it
  # on whatever the data directory holds at that moment. (Exit status 5 is a unit
  # systemd does not know: there is no service to stop.)
  log "stopping nhb.service"
  as_root systemctl stop nhb.service || rc=$?
  if [[ "${rc}" == "0" ]]; then
    stopped=1
  elif [[ "${rc}" != "5" ]]; then
    die "could not stop nhb.service: ${DATA_DIR} is as it was, and the unpacked snapshot is at ${staged}"
  fi
  if ! restore_identity "${DATA_DIR}" "${staged}"; then
    why="could not copy the node's identity and vote state into the new data directory"
  elif ! as_root mv "${DATA_DIR}" "${aside}"; then
    why="could not move ${DATA_DIR} aside"
  elif ! as_root mv "${staged}" "${DATA_DIR}"; then
    if as_root mv "${aside}" "${DATA_DIR}"; then
      why="could not move the new data directory into place (the old one was put back)"
    else
      echo "[ERROR] could not move the new data directory into place, and could not put the old one back." >&2
      echo "        The old data directory is at ${aside} and the new one at ${staged}." >&2
      echo "        Put the old one back with: sudo mv ${aside} ${DATA_DIR}" >&2
      exit 1
    fi
  fi
  if [[ -n "${why}" ]]; then
    echo "[ERROR] ${why}." >&2
    echo "        ${DATA_DIR} is as it was, and the unpacked snapshot is at ${staged}." >&2
    if [[ "${stopped}" == "1" ]]; then echo "        nhb.service is stopped; start it again with: sudo systemctl start nhb.service" >&2; fi
    exit 1
  fi
  log "the old data directory is at ${aside}; delete it yourself when you no longer need it"
}

# prepare_data_dir leaves DATA_DIR holding this network's chain database, and
# never overwrites one that is there except when --reset-state says to.
prepare_data_dir() {
  if [[ "${RESET_STATE}" == "1" ]]; then
    if data_dir_has_data; then
      replace_data_dir
      return 0
    fi
    log "--reset-state: there is no data to move aside"
  fi
  if data_dir_has_data; then
    if service_active; then
      log "the data directory already holds data and nhb.service is running; not installing a snapshot"
      return 0
    fi
    local info
    info=$(as_service "${INSTALL_ROOT}/bin/nhb-snapshot" info --data-dir "${DATA_DIR}" --format json --header-window 1 --no-state-check) \
      || die "${DATA_DIR} holds data that does not open as a chain database; move it aside or run with --reset-state"
    if ! grep -q "\"chainId\": \"${NETWORK_ID_DEFAULT}\"" <<< "${info}" || ! grep -q "\"genesisHash\": \"${GENESIS_HASH_DEFAULT}\"" <<< "${info}"; then
      die "${DATA_DIR} holds another chain than the pinned network ${NETWORK_ID_DEFAULT}; move it aside or run with --reset-state"
    fi
    log "the data directory already holds this network's chain; not installing a snapshot"
    return 0
  fi
  # A new node: its key must not already be validating.
  install_snapshot "${DATA_DIR}" 1
}

# ---------------------------------------------------------------------------
# Installation
# ---------------------------------------------------------------------------

install_prerequisites() {
  # "Open a fresh EC2 Ubuntu server, git pull, run this script" is the whole
  # promise -- a stock Ubuntu AMI has neither Go, rsync, nor perl installed, so
  # failing here with "command not found" instead of just installing them
  # would break that promise on literally the first run.
  local go_version="1.24.3" go_tarball apt_missing=()
  go_tarball="go${go_version}.linux-amd64.tar.gz"
  command -v rsync >/dev/null 2>&1 || apt_missing+=(rsync)
  command -v perl >/dev/null 2>&1 || apt_missing+=(perl)
  command -v curl >/dev/null 2>&1 || apt_missing+=(curl)
  if [[ ${#apt_missing[@]} -gt 0 ]]; then
    log "installing missing packages: ${apt_missing[*]}"
    as_root apt-get update -y
    as_root apt-get install -y "${apt_missing[@]}"
  fi

  if [[ ! -x /usr/local/go/bin/go ]]; then
    log "Go not found at /usr/local/go/bin/go -- installing Go ${go_version}"
    local tmp_go
    tmp_go=$(mktemp -d)
    curl -fsSL "https://go.dev/dl/${go_tarball}" -o "${tmp_go}/${go_tarball}"
    as_root rm -rf /usr/local/go
    as_root tar -C /usr/local -xzf "${tmp_go}/${go_tarball}"
    rm -rf "${tmp_go}"
  fi

  require_cmd rsync
  require_cmd perl
  require_cmd /usr/local/go/bin/go

  # Compiling this dependency tree needs real memory: a 908MB host with no
  # swap gets the compiler killed. Add a swap file when there is little RAM
  # and no swap, and leave it in place for the running node.
  if [[ ! -f /swapfile ]] && [[ "$(swapon --show=SIZE --noheadings 2>/dev/null | wc -l)" -eq 0 ]]; then
    local total_mem_kb
    total_mem_kb=$(awk '/MemTotal/ {print $2}' /proc/meminfo)
    if [[ -n "${total_mem_kb}" ]] && [[ "${total_mem_kb}" -lt 4194304 ]]; then
      log "low memory ($((total_mem_kb / 1024))MB) and no swap configured -- adding a 4G swap file"
      as_root fallocate -l 4G /swapfile
      as_root chmod 600 /swapfile
      as_root mkswap /swapfile
      as_root swapon /swapfile
      grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' | as_root tee -a /etc/fstab >/dev/null
    fi
  fi
}

install_tree_and_build() {
  as_root useradd --system --home "${INSTALL_ROOT}" --shell /usr/sbin/nologin "${SERVICE_USER}" 2>/dev/null || true
  as_root mkdir -p "${CONFIG_DIR}" "${STATE_DIR}" "${INSTALL_ROOT}/bin"
  # The service runs as ${SERVICE_USER}: the config directory must be its own,
  # or it cannot even traverse into it to read config.toml.
  as_root chown "${SERVICE_USER}:${SERVICE_USER}" "${CONFIG_DIR}"
  as_root chmod 700 "${CONFIG_DIR}"

  as_root rsync -a --delete --exclude '/.gocache' --exclude '/.gopath' --exclude '/.gotmp' "${REPO_ROOT}/" "${INSTALL_ROOT}/"

  # A node started from a modified or different genesis file writes it into its
  # database and can never peer with the live network, so stop before anything
  # starts if the file differs from the one the live network was started from.
  if ! echo "${GENESIS_SHA256}  ${INSTALL_ROOT}/${GENESIS_FILE_REL}" | sha256sum -c --status; then
    die "${INSTALL_ROOT}/${GENESIS_FILE_REL} is not the live network's genesis file (expected sha256 ${GENESIS_SHA256}). Restore it from the repository."
  fi

  log "building nhb, nhb-cli and nhb-snapshot"
  # Go's module cache for this dependency tree needs well over a gigabyte, and
  # /tmp is a small RAM-backed tmpfs on many hosts: build under INSTALL_ROOT,
  # which is on the real disk.
  local gocache="${INSTALL_ROOT}/.gocache" gopath="${INSTALL_ROOT}/.gopath" gotmp="${INSTALL_ROOT}/.gotmp"
  as_root mkdir -p "${gocache}" "${gopath}" "${gotmp}"
  local pkg
  for pkg in nhb nhb-cli nhb-snapshot; do
    ( cd "${INSTALL_ROOT}" && as_root env PATH=/usr/local/go/bin:/usr/bin:/bin GOCACHE="${gocache}" GOPATH="${gopath}" GOTMPDIR="${gotmp}" TMPDIR="${gotmp}" HOME=/root \
      /usr/local/go/bin/go build -trimpath -ldflags="-s -w" -buildvcs=false -o "${INSTALL_ROOT}/bin/${pkg}" "./cmd/${pkg}" ) \
      || die "building ${pkg} failed"
  done
  as_root chown -R "${SERVICE_USER}:${SERVICE_USER}" "${INSTALL_ROOT}"
  as_root chown "${SERVICE_USER}:${SERVICE_USER}" "${STATE_DIR}"
}

# ensure_key makes the validator key on this machine the first time and reuses
# it afterwards. Never pass a key in: it would end up in shell history.
ensure_key() {
  if ! as_root test -f "${VALIDATOR_KEY_FILE}"; then
    log "generating a fresh validator key on this machine"
    local tmp_key_dir
    tmp_key_dir=$(mktemp -d)
    ( cd "${tmp_key_dir}" && "${INSTALL_ROOT}/bin/nhb-cli" generate-key > "${tmp_key_dir}/generate-key.out" )
    as_root install -m 0600 -o "${SERVICE_USER}" -g "${SERVICE_USER}" "${tmp_key_dir}/wallet.key" "${VALIDATOR_KEY_FILE}"
    as_root touch "${CONFIG_DIR}/.validator.key.created-here"
    rm -rf "${tmp_key_dir}"
  else
    log "reusing existing validator key at ${VALIDATOR_KEY_FILE}"
  fi
  as_root chown "${SERVICE_USER}:${SERVICE_USER}" "${VALIDATOR_KEY_FILE}"
  as_root chmod 600 "${VALIDATOR_KEY_FILE}"
  # Deriving the address from the key file needs to read it: only the CLI, as
  # the service user, does that. The raw key is never printed.
  VALIDATOR_ADDRESS=$(as_service "${INSTALL_ROOT}/bin/nhb-cli" address "${VALIDATOR_KEY_FILE}" 2>/dev/null | grep -o 'nhb1[a-z0-9]*' | head -1 || true)
  [[ -n "${VALIDATOR_ADDRESS}" ]] || die "could not determine the validator address from ${VALIDATOR_KEY_FILE}"
}

detect_external_address() {
  if [[ -z "${EXTERNAL_ADDRESS}" ]]; then
    log "--external-address not provided -- attempting to auto-detect this server's public IP"
    # Try the EC2 instance metadata service (IMDSv2) first, then a public echo
    # service. Best-effort: peers that only ever reach this node inbound will
    # not get a reconnectable address for it if neither answers.
    local token
    token=$(curl -fsS -m 2 -X PUT "http://169.254.169.254/latest/api/token" \
      -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' 2>/dev/null || true)
    if [[ -n "${token}" ]]; then
      EXTERNAL_ADDRESS=$(curl -fsS -m 2 -H "X-aws-ec2-metadata-token: ${token}" \
        "http://169.254.169.254/latest/meta-data/public-ipv4" 2>/dev/null || true)
    fi
    if [[ -z "${EXTERNAL_ADDRESS}" ]]; then
      EXTERNAL_ADDRESS=$(curl -fsS -m 3 https://ifconfig.me 2>/dev/null || true)
    fi
    if [[ -n "${EXTERNAL_ADDRESS}" ]]; then
      log "auto-detected public IP: ${EXTERNAL_ADDRESS}"
    else
      warn "could not auto-detect a public IP -- peers that only ever connect to this node inbound will not be able to reconnect to it later. Re-run with --external-address <ip> to fix this."
    fi
  fi
  if [[ -n "${EXTERNAL_ADDRESS}" ]]; then
    [[ "${EXTERNAL_ADDRESS}" =~ ^[A-Za-z0-9._:-]+$ ]] || die "the external address '${EXTERNAL_ADDRESS}' is not an IP or host:port"
    # ExternalAddress is host:port, like ListenAddress.
    case "${EXTERNAL_ADDRESS}" in
      *:*) EXTERNAL_ADDRESS_HOSTPORT="${EXTERNAL_ADDRESS}" ;;
      *) EXTERNAL_ADDRESS_HOSTPORT="${EXTERNAL_ADDRESS}:${LISTEN_ADDR##*:}" ;;
    esac
  fi
}

install_service() {
  local unit="${INSTALL_ROOT}/deploy/systemd/nhb.service"
  UNIT_CHANGED=1
  if [[ -f /etc/systemd/system/nhb.service ]] && cmp -s "${unit}" /etc/systemd/system/nhb.service; then UNIT_CHANGED=0; fi
  if [[ "${UNIT_CHANGED}" == "1" ]]; then
    as_root install -m 0644 "${unit}" /etc/systemd/system/nhb.service
    as_root systemctl daemon-reload
  fi
  as_root systemctl enable nhb.service
  local fingerprint_file="${STATE_DIR}/.deploy-fingerprint" now old
  now=$( { sha256sum "${INSTALL_ROOT}/bin/nhb"; as_root cat "${CONFIG_DIR}/config.toml"; as_root cat "${CONFIG_DIR}/node.env"; cat "${unit}"; } | sha256sum | cut -d' ' -f1)
  old=$(as_root cat "${fingerprint_file}" 2>/dev/null || true)
  if service_active && [[ "${old}" == "${now}" ]]; then
    log "nhb.service is running with the current binary and configuration; leaving it as it is"
  else
    log "starting nhb.service"
    as_root systemctl restart nhb.service
    printf '%s\n' "${now}" | as_root tee "${fingerprint_file}" >/dev/null
  fi
}

# wait_until_synced waits for the node's RPC, checks it reports the pinned
# network, and waits until it is at the network tip: connected to a peer and, when
# this run installed a snapshot, past that snapshot's height, which a snapshot that
# is not part of the network's chain can never be. With --tip-rpc the node's newest
# blocks must also be the ones that node has.
wait_until_synced() {
  local args=(wait-synced --rpc "http://${RPC_ADDR}/" --chain-id "${NETWORK_ID_DEFAULT}" --genesis-hash "${GENESIS_HASH_DEFAULT}"
    --interval "${SYNC_INTERVAL}" --timeout "${SYNC_TIMEOUT_SECS}s" --stall-timeout 15m --max-lag-blocks "${MAX_LAG_BLOCKS}")
  if [[ -n "${TIP_RPC}" ]]; then args+=(--tip-rpc "${TIP_RPC}"); fi
  if [[ -n "${SNAPSHOT_HEIGHT}" ]]; then args+=(--min-height "${SNAPSHOT_HEIGHT}"); fi
  log "waiting for the node to reach the network tip (this follows its progress; a few minutes to a few hours depending on the snapshot's age)"
  local rc=0
  "${INSTALL_ROOT}/bin/nhb-snapshot" "${args[@]}" || rc=$?
  if [[ "${rc}" != "0" ]]; then
    echo
    echo "=================================================================="
    case "${rc}" in
      3) echo "[ERROR] the node did not reach the network tip within ${SYNC_TIMEOUT_SECS} seconds." ;;
      4) echo "[ERROR] the node has stopped making progress." ;;
      5) echo "[ERROR] the node is not on the pinned network (chain id ${NETWORK_ID_DEFAULT})." ;;
      6) echo "[ERROR] the node's newest blocks are not the ones the --tip-rpc node has: the snapshot it started from is not part of the network's chain." ;;
      *) echo "[ERROR] the node could not be checked (exit ${rc})." ;;
    esac
    echo
    echo "Check what is actually wrong with:"
    echo "  sudo systemctl status nhb.service"
    echo "  sudo journalctl -u nhb.service -n 80 --no-pager"
    echo "A node that is at the tip has a peer and has applied blocks past its snapshot:"
    echo "check the --bootnode address, and that the snapshot is one of the network's."
    echo "A snapshot that is too old for the node to catch up is the usual cause of a"
    echo "stall; fetch a newer one and run this script again with --reset-state."
    echo "=================================================================="
    exit 1
  fi
}

# --- begin validator CLI helpers (exercised by tests/scripts) ---
# nhb-cli submits the transactions below through the node's privileged RPC,
# which needs a bearer token (NHB_RPC_TOKEN). Mint a short-lived one from the
# JWT secret this run wrote to node.env. The secret reaches nhb-cli on stdin,
# not on a command line, and neither it nor the token is ever printed.
mint_rpc_token() {
  printf '%s' "${JWT_SECRET}" | sudo -u "${SERVICE_USER}" \
    "${INSTALL_ROOT}/bin/nhb-cli" rpc-token --secret-stdin --ttl 10m
}

run_cli() {
  sudo -u "${SERVICE_USER}" env RPC_URL="http://${RPC_ADDR}" NHB_RPC_TOKEN="${RPC_TOKEN}" \
    "${INSTALL_ROOT}/bin/nhb-cli" "$@"
}

# How an operator runs a signing command later (the messages below print
# these): the command needs a fresh token exactly like the steps in this script.
TOKEN_RECIPE="TOKEN=\$(sudo sh -c '. ${CONFIG_DIR}/node.env && printf %s \"\$NHB_RPC_JWT_SECRET\"' | sudo -u ${SERVICE_USER} ${INSTALL_ROOT}/bin/nhb-cli rpc-token --secret-stdin)"
cli_recipe() {
  echo "sudo -u ${SERVICE_USER} env RPC_URL=http://${RPC_ADDR} NHB_RPC_TOKEN=\"\$TOKEN\" ${INSTALL_ROOT}/bin/nhb-cli $*"
}

# The node may still be finishing its own startup, so try a few times.
run_cli_step() {
  local attempt
  for attempt in 1 2 3; do
    if run_cli "$@"; then
      return 0
    fi
    if [[ "${attempt}" -lt 3 ]]; then
      sleep "${CLI_RETRY_DELAY:-5}"
    fi
  done
  return 1
}

# Submits the reward-beneficiary and validator-registration transactions.
# Returns 1 -- after saying which steps failed and how to run them again -- if
# any did not go through, so the caller never reports success for a step that
# did not happen.
submit_validator_steps() {
  local failed=() step

  if [[ -n "${BENEFICIARY}" ]]; then
    echo "[INFO] setting reward beneficiary to ${BENEFICIARY}"
    run_cli_step set-reward-beneficiary "${BENEFICIARY}" "${VALIDATOR_KEY_FILE}" \
      || failed+=("set-reward-beneficiary ${BENEFICIARY} ${VALIDATOR_KEY_FILE}")
  fi

  # Validator eligibility is gated on an explicit on-chain opt-in
  # (ValidatorRegistered) plus the account's total stake -- its own stake AND ZNHB
  # delegated to it by any wallet, added together -- meeting
  # staking.minimumValidatorStake, and the address not delegating its own stake to
  # a different validator (core/state_transition.go's setAccount and
  # validatorEligibilityBasis). This "pure registration" call (zero value,
  # RegisterValidator=true) costs nothing and needs no pre-funding -- it just
  # flips the flag now, so the only step left for the operator is getting stake
  # onto this validator's address, by delegation or self-stake (printed below).
  echo "[INFO] registering this validator's on-chain eligibility flag"
  run_cli_step register-validator 0 "${VALIDATOR_KEY_FILE}" \
    || failed+=("register-validator 0 ${VALIDATOR_KEY_FILE}")

  if [[ "${#failed[@]}" -gt 0 ]]; then
    echo
    echo "=================================================================="
    echo "[ERROR] The node is running, but these steps did not complete:"
    for step in "${failed[@]}"; do
      echo "  nhb-cli ${step}"
    done
    echo
    echo "Once the error above is fixed, run them again with:"
    echo "  ${TOKEN_RECIPE}"
    for step in "${failed[@]}"; do
      echo "  $(cli_recipe "${step}")"
    done
    echo "=================================================================="
    return 1
  fi
}
# --- end validator CLI helpers ---

print_next_steps() {
  echo
  echo "=================================================================="
  echo "[OK] Validator node is running and at the network tip."
  echo
  echo "It follows the network as a NON-voting node: its key is not registered with"
  echo "stake, so its votes are ignored, until the steps below have taken effect."
  echo
  echo "Your validator's node address is:"
  echo "  ${VALIDATOR_ADDRESS}"
  echo
  if [[ -n "${EXTERNAL_ADDRESS_HOSTPORT}" ]]; then
    echo "This node advertises itself to peers as: ${EXTERNAL_ADDRESS_HOSTPORT}"
  else
    warn "no external address is set -- peers that only connect to this node inbound will not be able to reconnect. Re-run with --external-address <ip> to fix this."
  fi
  echo
  echo "To make this node a validator candidate, its address needs at least"
  echo "10,000 ZNHB of total stake (staking.minimumValidatorStake, adjustable by"
  echo "governance). Delegated stake and the validator's own stake add together,"
  echo "so either route works:"
  echo "  A. Delegate: from any wallet, delegate at least 10,000 ZNHB to this"
  echo "     validator's node address (printed above)."
  echo "  B. Self-stake: send at least 10,000 ZNHB to this validator's node"
  echo "     address, and once it has arrived, stake it from this server:"
  echo "     ${TOKEN_RECIPE}"
  echo "     $(cli_recipe register-validator 10000000000000000000000 "${VALIDATOR_KEY_FILE}")"
  echo "  (the registration transaction was submitted above and takes effect once a"
  echo "   block includes it; this node's own address must not itself be delegating"
  echo "   its stake to a different validator.)"
  echo
  echo "It joins the active validator set at the next epoch boundary once it has stake,"
  echo "is registered and keeps its heartbeat current. Until then do not expect rewards."
  echo
  echo "Check status with:"
  echo "  sudo systemctl status nhb.service"
  echo "  sudo journalctl -u nhb.service -f"
  echo
  echo "Never copy this server's key (${VALIDATOR_KEY_FILE}) or the node's data directory to"
  echo "another machine to run a second node: a key that runs twice double-signs, and a"
  echo "copy of the node's p2p identity makes two nodes indistinguishable to their peers."
  echo "=================================================================="
}

# take_lock allows one run of this script at a time (mkdir is atomic).
LOCK_DIR="${TMPDIR:-/tmp}/nhb-deploy.lock.d"
take_lock() {
  if ! mkdir "${LOCK_DIR}" 2>/dev/null; then
    local holder
    holder=$(cat "${LOCK_DIR}/pid" 2>/dev/null || true)
    if [[ -n "${holder}" ]] && kill -0 "${holder}" 2>/dev/null; then
      die "another run of this script (pid ${holder}) is in progress"
    fi
    rm -rf "${LOCK_DIR}"
    mkdir "${LOCK_DIR}" || die "cannot take the lock ${LOCK_DIR}"
  fi
  echo "$$" > "${LOCK_DIR}/pid"
  trap 'rm -rf "${LOCK_DIR}"' EXIT
}

main() {
  parse_args "$@"
  validate_inputs

  require_cmd sudo
  require_cmd systemctl
  require_cmd sha256sum

  take_lock
  refuse_second_node || exit 1
  install_prerequisites
  require_cmd curl
  refuse_foreign_key || exit 1
  install_tree_and_build

  # Only a run that installs a snapshot has any use for a manifest, or for the
  # question whether the node built here is the one that made the snapshot. A
  # node that already holds this network's chain (the ordinary re-run) is not
  # given one, so it never depends on the snapshot host: not on what it now
  # publishes, and not on its answering at all.
  if snapshot_needed; then
    local local_sha local_commit
    local_sha=$(sha256sum "${INSTALL_ROOT}/bin/nhb" | cut -d' ' -f1)
    # The commit of the checkout this script runs from. A checkout with local
    # changes to tracked files is not that commit: only an identical binary matches.
    local_commit=$(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null || echo unknown)
    if [[ "${local_commit}" != "unknown" && -n "$(git -C "${REPO_ROOT}" status --porcelain --untracked-files=no 2>/dev/null)" ]]; then
      local_commit="${local_commit}-dirty"
    fi

    fetch_manifest
    check_binary_identity "$(manifest_field "${MANIFEST_FILE}" producer.binarySha256)" "$(manifest_field "${MANIFEST_FILE}" producer.binaryCommit)" \
      "${local_sha}" "${local_commit}" || exit 1
  else
    log "the data directory already holds data: no snapshot is needed, so none is fetched"
  fi

  ensure_key
  detect_external_address
  as_root install -d -m 0700 -o "${SERVICE_USER}" -g "${SERVICE_USER}" "${CONFIG_DIR}"
  prepare_data_dir
  install_config
  write_env
  install_service

  wait_until_synced

  if ! RPC_TOKEN=$(mint_rpc_token); then
    die "could not create an RPC token for the local node"
  fi
  submit_validator_steps || exit 1
  print_next_steps
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
