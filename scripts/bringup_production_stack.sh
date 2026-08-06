#!/usr/bin/env bash
set -euo pipefail

INSTALL_ROOT="${INSTALL_ROOT:-/opt/nhbchain}"
ETC_DIR="${ETC_DIR:-/etc/nhbchain}"
STATE_DIR="${STATE_DIR:-/var/lib/nhbchain}"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
SERVICE_USER="${SERVICE_USER:-nhb}"
SERVICE_GROUP="${SERVICE_GROUP:-nhb}"
ENABLE_OTC="${ENABLE_OTC:-auto}"
RESET_STATE=0
SKIP_START=0

usage() {
  cat <<USAGE
Usage: scripts/run_nhbcoin_node.sh [options]

One-time production deployment script for NHBChain. It:
  1. syncs the repo into the runtime install root
  2. installs example config templates
  3. validates required production config
  4. builds the node and founder-grade backend services
  5. installs systemd units
  6. optionally clears existing chain/service state
  7. enables and starts the runtime stack

Options:
  --install-root <path>   Runtime install root. Default: /opt/nhbchain
  --etc-dir <path>        Server config directory. Default: /etc/nhbchain
  --state-dir <path>      Service state directory. Default: /var/lib/nhbchain
  --systemd-dir <path>    systemd unit directory. Default: /etc/systemd/system
  --service-user <name>   Runtime service user. Default: nhb
  --service-group <name>  Runtime service group. Default: nhb
  --enable-otc            Require and start otc-gateway
  --disable-otc           Do not start otc-gateway
  --reset-state           Purge node and service state before start
  --skip-start            Install and build only; do not start services
  -h, --help              Show this help

Required server-side config files:
  /etc/nhbchain/config.toml
  /etc/nhbchain/node.env
  /etc/nhbchain/payments-gateway.env
  /etc/nhbchain/payoutd.env
  /etc/nhbchain/payoutd.yaml
  /etc/nhbchain/policies.yaml
  /etc/nhbchain/ops-reporting.env

Optional:
  /etc/nhbchain/otc-gateway.env

Examples:
  bash scripts/run_nhbcoin_node.sh --reset-state
  ENABLE_OTC=1 bash scripts/run_nhbcoin_node.sh
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
    --enable-otc)
      ENABLE_OTC="1"
      shift
      ;;
    --disable-otc)
      ENABLE_OTC="0"
      shift
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

resolve_otc_mode() {
  case "${ENABLE_OTC}" in
    1|true|TRUE|yes|YES)
      echo "1"
      ;;
    0|false|FALSE|no|NO)
      echo "0"
      ;;
    auto|AUTO|"")
      if [[ -f "${ETC_DIR}/otc-gateway.env" ]]; then
        echo "1"
      else
        echo "0"
      fi
      ;;
    *)
      echo "invalid ENABLE_OTC value: ${ENABLE_OTC}" >&2
      exit 1
      ;;
  esac
}

need_cmd go
need_cmd python3
need_cmd rsync
need_cmd systemctl
need_cmd install

OTC_ENABLED=$(resolve_otc_mode)

step "1/10" "Preparing runtime directories and service account"
if ! getent group "${SERVICE_GROUP}" >/dev/null 2>&1; then
  run_root groupadd --system "${SERVICE_GROUP}"
fi
if ! id -u "${SERVICE_USER}" >/dev/null 2>&1; then
  run_root useradd --system --gid "${SERVICE_GROUP}" --home "${INSTALL_ROOT}" --shell /usr/sbin/nologin "${SERVICE_USER}"
fi
run_root install -d -m 0755 "${INSTALL_ROOT}" "${INSTALL_ROOT}/bin" "${ETC_DIR}" "${ETC_DIR}/examples" "${STATE_DIR}" "${STATE_DIR}/payoutd"

step "2/10" "Syncing repo into runtime install root"
run_root rsync -a --delete \
  --exclude ".git/" \
  --exclude "nhb-data/" \
  --exclude "nhb-data-local/" \
  --exclude "node.log" \
  --exclude "*.db" \
  --exclude ".svelte-kit/" \
  --exclude "build/" \
  "${REPO_ROOT}/" "${INSTALL_ROOT}/"

step "3/10" "Installing example server-side config templates"
install_example "${ENV_SRC}/node.env.example" "${ETC_DIR}/examples/node.env.example"
install_example "${ENV_SRC}/payments-gateway.env.example" "${ETC_DIR}/examples/payments-gateway.env.example"
install_example "${ENV_SRC}/payoutd.env.example" "${ETC_DIR}/examples/payoutd.env.example"
install_example "${ENV_SRC}/payoutd.config.example.yaml" "${ETC_DIR}/examples/payoutd.config.example.yaml"
install_example "${ENV_SRC}/ops-reporting.env.example" "${ETC_DIR}/examples/ops-reporting.env.example"
install_example "${ENV_SRC}/otc-gateway.env.example" "${ETC_DIR}/examples/otc-gateway.env.example"
install_example "${REPO_ROOT}/services/payoutd/policies.yaml" "${ETC_DIR}/examples/policies.yaml.example"
if [[ ! -f "${ETC_DIR}/policies.yaml" ]]; then
  run_root install -m 0640 "${REPO_ROOT}/services/payoutd/policies.yaml" "${ETC_DIR}/policies.yaml"
fi

step "4/10" "Validating required production configuration"
require_file "${ETC_DIR}/config.toml"
require_file "${ETC_DIR}/node.env"
require_file "${ETC_DIR}/payments-gateway.env"
require_file "${ETC_DIR}/payoutd.env"
require_file "${ETC_DIR}/payoutd.yaml"
require_file "${ETC_DIR}/policies.yaml"
require_file "${ETC_DIR}/ops-reporting.env"
if [[ "${OTC_ENABLED}" == "1" ]]; then
  require_file "${ETC_DIR}/otc-gateway.env"
fi
"${INSTALL_ROOT}/scripts/verify_prod_config.sh" -c "${ETC_DIR}/config.toml"

step "5/10" "Stopping old ad hoc processes and installed services"
for svc in nhb.service payments-gateway.service payoutd.service ops-reporting.service otc-gateway.service; do
  run_root systemctl stop "$svc" >/dev/null 2>&1 || true
done
run_root pkill -f "/bin/nhb --config" >/dev/null 2>&1 || true
run_root pkill -f "/bin/payments-gateway" >/dev/null 2>&1 || true
run_root pkill -f "/bin/payoutd" >/dev/null 2>&1 || true
run_root pkill -f "/bin/ops-reporting" >/dev/null 2>&1 || true
run_root pkill -f "/bin/otc-gateway" >/dev/null 2>&1 || true

step "6/10" "Building binaries"
pushd "${INSTALL_ROOT}" >/dev/null
bash scripts/build.sh
GOFLAGS="${GOFLAGS:--buildvcs=false}" go build -trimpath -ldflags="-s -w" -buildvcs=false -o bin/payoutd ./cmd/payoutd
GOFLAGS="${GOFLAGS:--buildvcs=false}" go build -trimpath -ldflags="-s -w" -buildvcs=false -o bin/payments-gateway ./services/payments-gateway
GOFLAGS="${GOFLAGS:--buildvcs=false}" go build -trimpath -ldflags="-s -w" -buildvcs=false -o bin/ops-reporting ./services/ops-reporting
GOFLAGS="${GOFLAGS:--buildvcs=false}" go build -trimpath -ldflags="-s -w" -buildvcs=false -o bin/otc-gateway ./services/otc-gateway
popd >/dev/null

step "7/10" "Installing systemd units"
render_unit "${SYSTEMD_SRC}/nhb.service" "${SYSTEMD_DIR}/nhb.service"
render_unit "${SYSTEMD_SRC}/payments-gateway.service" "${SYSTEMD_DIR}/payments-gateway.service"
render_unit "${SYSTEMD_SRC}/payoutd.service" "${SYSTEMD_DIR}/payoutd.service"
render_unit "${SYSTEMD_SRC}/ops-reporting.service" "${SYSTEMD_DIR}/ops-reporting.service"
render_unit "${SYSTEMD_SRC}/otc-gateway.service" "${SYSTEMD_DIR}/otc-gateway.service"
run_root systemctl daemon-reload

step "8/10" "Fixing ownership and runtime permissions"
run_root chown -R "${SERVICE_USER}:${SERVICE_GROUP}" "${INSTALL_ROOT}" "${STATE_DIR}"
run_root chmod 0755 "${INSTALL_ROOT}/scripts/bringup_production_stack.sh" "${INSTALL_ROOT}/scripts/verify_prod_config.sh"
run_root chmod 0755 "${INSTALL_ROOT}/bin/nhb" "${INSTALL_ROOT}/bin/nhb-cli" "${INSTALL_ROOT}/bin/payoutd" "${INSTALL_ROOT}/bin/payments-gateway" "${INSTALL_ROOT}/bin/ops-reporting" "${INSTALL_ROOT}/bin/otc-gateway"

if [[ "${RESET_STATE}" -eq 1 ]]; then
  step "9/10" "Resetting node and service state for fresh genesis"
  timestamp=$(date -u +%Y%m%dT%H%M%SZ)
  backup_dir="${STATE_DIR}/backup-${timestamp}"
  run_root install -d -m 0750 "${backup_dir}"
  if [[ -d "${INSTALL_ROOT}/nhb-data" ]]; then
    run_root mv "${INSTALL_ROOT}/nhb-data" "${backup_dir}/nhb-data"
  fi
  if [[ -d "${INSTALL_ROOT}/nhb-data-local" ]]; then
    run_root mv "${INSTALL_ROOT}/nhb-data-local" "${backup_dir}/nhb-data-local"
  fi
  if [[ -f "${STATE_DIR}/payments-gateway.db" ]]; then
    run_root mv "${STATE_DIR}/payments-gateway.db" "${backup_dir}/payments-gateway.db"
  fi
  if [[ -f "${STATE_DIR}/escrow-gateway.db" ]]; then
    run_root mv "${STATE_DIR}/escrow-gateway.db" "${backup_dir}/escrow-gateway.db"
  fi
  if [[ -d "${STATE_DIR}/payoutd" ]]; then
    run_root mv "${STATE_DIR}/payoutd" "${backup_dir}/payoutd"
  fi
  run_root install -d -m 0755 "${STATE_DIR}" "${STATE_DIR}/payoutd"
else
  step "9/10" "Keeping existing state (no reset requested)"
fi

step "10/10" "Enabling and starting services"
services=(nhb.service payments-gateway.service payoutd.service ops-reporting.service)
if [[ "${OTC_ENABLED}" == "1" ]]; then
  services+=(otc-gateway.service)
fi
run_root systemctl enable "${services[@]}" >/dev/null
if [[ "${SKIP_START}" -eq 0 ]]; then
  run_root systemctl restart nhb.service
  sleep 3
  run_root systemctl restart payments-gateway.service
  run_root systemctl restart payoutd.service
  run_root systemctl restart ops-reporting.service
  if [[ "${OTC_ENABLED}" == "1" ]]; then
    run_root systemctl restart otc-gateway.service
  fi
fi

echo
echo "Deployment complete."
echo
echo "Install root: ${INSTALL_ROOT}"
echo "Config dir:   ${ETC_DIR}"
echo "State dir:    ${STATE_DIR}"
echo "OTC enabled:  ${OTC_ENABLED}"
echo
echo "Validate stack with:"
echo "  sudo systemctl status ${services[*]}"
echo "  sudo ss -ltnp | egrep ':8545|:8084|:7082|:8091|:8086|:80|:443'"
echo
echo "If this was a fresh genesis reset, run your post-start validation next:"
echo "  1. local RPC check"
echo "  2. inbound swap-mint check"
echo "  3. payout check"
echo "  4. OTC invoice flow check"
