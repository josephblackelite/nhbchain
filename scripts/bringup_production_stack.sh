#!/usr/bin/env bash
set -euo pipefail

INSTALL_ROOT="${INSTALL_ROOT:-/opt/nhbchain}"
ETC_DIR="${ETC_DIR:-/etc/nhbchain}"
STATE_DIR="${STATE_DIR:-/var/lib/nhbchain}"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
SERVICE_USER="${SERVICE_USER:-nhb}"
SERVICE_GROUP="${SERVICE_GROUP:-nhb}"
RESET_STATE=0
SKIP_START=0

usage() {
  cat <<USAGE
Usage: scripts/run_nhbcoin_node.sh [options]

One-time production deployment script for the NHBChain validator/full node. It:
  1. syncs the repo into the runtime install root
  2. installs example config templates
  3. validates required production config
  4. builds the node binary
  5. installs the systemd unit
  6. optionally clears existing chain state
  7. enables and starts the node

Options:
  --install-root <path>   Runtime install root. Default: /opt/nhbchain
  --etc-dir <path>        Server config directory. Default: /etc/nhbchain
  --state-dir <path>      Service state directory. Default: /var/lib/nhbchain
  --systemd-dir <path>    systemd unit directory. Default: /etc/systemd/system
  --service-user <name>   Runtime service user. Default: nhb
  --service-group <name>  Runtime service group. Default: nhb
  --reset-state           Purge node state before start
  --skip-start            Install and build only; do not start the node
  -h, --help              Show this help

Required server-side config files:
  /etc/nhbchain/config.toml
  /etc/nhbchain/node.env

Examples:
  bash scripts/run_nhbcoin_node.sh --reset-state
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-root)
      INSTALL_ROOT="$2"
      shift 2
      ;;
    --etc-dir)
      ETC_DIR="$2"
      shift 2
      ;;
    --state-dir)
      STATE_DIR="$2"
      shift 2
      ;;
    --systemd-dir)
      SYSTEMD_DIR="$2"
      shift 2
      ;;
    --service-user)
      SERVICE_USER="$2"
      shift 2
      ;;
    --service-group)
      SERVICE_GROUP="$2"
      shift 2
      ;;
    --reset-state)
      RESET_STATE=1
      shift
      ;;
    --skip-start)
      SKIP_START=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/.." && pwd)
SYSTEMD_SRC="${REPO_ROOT}/deploy/systemd"
ENV_SRC="${REPO_ROOT}/deploy/env"

run_root() {
  if [[ "${EUID}" -eq 0 ]]; then
    "$@"
  else
    sudo "$@"
  fi
}

need_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "required command not found: $cmd" >&2
    exit 1
  fi
}

step() {
  echo
  echo "[$1] $2"
}

require_file() {
  local path="$1"
  if [[ ! -f "$path" ]]; then
    echo "missing required file: $path" >&2
    exit 1
  fi
}

install_example() {
  local src="$1"
  local dest="$2"
  if [[ ! -f "$dest" ]]; then
    run_root install -m 0640 "$src" "$dest"
  fi
}

render_unit() {
  local src="$1"
  local dest="$2"
  local temp
  temp=$(mktemp)
  sed \
    -e "s|/opt/nhbchain|${INSTALL_ROOT}|g" \
    -e "s|/etc/nhbchain|${ETC_DIR}|g" \
    -e "s|User=nhb|User=${SERVICE_USER}|g" \
    -e "s|Group=nhb|Group=${SERVICE_GROUP}|g" \
    "$src" >"$temp"
  run_root install -m 0644 "$temp" "$dest"
  rm -f "$temp"
}

need_cmd go
need_cmd python3
need_cmd rsync
need_cmd systemctl
need_cmd install

step "1/7" "Preparing runtime directories and service account"
if ! getent group "${SERVICE_GROUP}" >/dev/null 2>&1; then
  run_root groupadd --system "${SERVICE_GROUP}"
fi
if ! id -u "${SERVICE_USER}" >/dev/null 2>&1; then
  run_root useradd --system --gid "${SERVICE_GROUP}" --home "${INSTALL_ROOT}" --shell /usr/sbin/nologin "${SERVICE_USER}"
fi
run_root install -d -m 0755 "${INSTALL_ROOT}" "${INSTALL_ROOT}/bin" "${ETC_DIR}" "${ETC_DIR}/examples" "${STATE_DIR}"

step "2/7" "Syncing repo into runtime install root"
run_root rsync -a --delete \
  --exclude ".git/" \
  --exclude "nhb-data/" \
  --exclude "nhb-data-local/" \
  --exclude "node.log" \
  --exclude "*.db" \
  --exclude ".svelte-kit/" \
  --exclude "build/" \
  "${REPO_ROOT}/" "${INSTALL_ROOT}/"

step "3/7" "Installing example server-side config templates"
install_example "${ENV_SRC}/node.env.example" "${ETC_DIR}/examples/node.env.example"

step "4/7" "Validating required production configuration"
require_file "${ETC_DIR}/config.toml"
require_file "${ETC_DIR}/node.env"
"${INSTALL_ROOT}/scripts/verify_prod_config.sh" -c "${ETC_DIR}/config.toml"

step "5/7" "Stopping old ad hoc processes and building the node binary"
run_root systemctl stop nhb.service >/dev/null 2>&1 || true
run_root pkill -f "/bin/nhb --config" >/dev/null 2>&1 || true
pushd "${INSTALL_ROOT}" >/dev/null
bash scripts/build.sh
popd >/dev/null

step "6/7" "Installing the systemd unit"
render_unit "${SYSTEMD_SRC}/nhb.service" "${SYSTEMD_DIR}/nhb.service"
run_root systemctl daemon-reload
run_root chown -R "${SERVICE_USER}:${SERVICE_GROUP}" "${INSTALL_ROOT}" "${STATE_DIR}"
run_root chmod 0755 "${INSTALL_ROOT}/scripts/bringup_production_stack.sh" "${INSTALL_ROOT}/scripts/verify_prod_config.sh"
run_root chmod 0755 "${INSTALL_ROOT}/bin/nhb" "${INSTALL_ROOT}/bin/nhb-cli"

if [[ "${RESET_STATE}" -eq 1 ]]; then
  echo "Resetting node state for fresh genesis"
  timestamp=$(date -u +%Y%m%dT%H%M%SZ)
  backup_dir="${STATE_DIR}/backup-${timestamp}"
  run_root install -d -m 0750 "${backup_dir}"
  if [[ -d "${INSTALL_ROOT}/nhb-data" ]]; then
    run_root mv "${INSTALL_ROOT}/nhb-data" "${backup_dir}/nhb-data"
  fi
  if [[ -d "${INSTALL_ROOT}/nhb-data-local" ]]; then
    run_root mv "${INSTALL_ROOT}/nhb-data-local" "${backup_dir}/nhb-data-local"
  fi
else
  echo "Keeping existing state (no reset requested)"
fi

step "7/7" "Enabling and starting the node"
run_root systemctl enable nhb.service >/dev/null
if [[ "${SKIP_START}" -eq 0 ]]; then
  run_root systemctl restart nhb.service
fi

echo
echo "Deployment complete."
echo
echo "Install root: ${INSTALL_ROOT}"
echo "Config dir:   ${ETC_DIR}"
echo "State dir:    ${STATE_DIR}"
echo
echo "Validate with:"
echo "  sudo systemctl status nhb.service"
echo "  sudo ss -ltnp | egrep ':8545'"
