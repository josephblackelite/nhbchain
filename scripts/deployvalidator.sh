#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/.." && pwd)

INSTALL_ROOT=/opt/nhbchain
CONFIG_DIR=/etc/nhbchain
STATE_DIR=/var/lib/nhbchain
SERVICE_USER=nhb

BOOTNODE_DEFAULT='enode://9606e2dd587cef5c8c46c6d41d03faf365edcb2f394921099e2b812261010841@52.1.96.250:6001'
NETWORK_ID_DEFAULT='14699254016670310680'
LISTEN_ADDR_DEFAULT='0.0.0.0:6001'
RPC_ADDR_DEFAULT='127.0.0.1:8545'

VALIDATOR_KEY=''
BOOTNODE="${BOOTNODE_DEFAULT}"
NETWORK_ID="${NETWORK_ID_DEFAULT}"
LISTEN_ADDR="${LISTEN_ADDR_DEFAULT}"
RPC_ADDR="${RPC_ADDR_DEFAULT}"
RESET_STATE=0

usage() {
  cat <<'EOF'
Usage:
  bash scripts/deployvalidator.sh --validator-key <hex-private-key> [options]

Options:
  --validator-key <hex>   Raw NHB validator private key from your wallet.
  --bootnode <enode>      Bootnode enode to join. Default: NHBCoin mainnet bootnode.
  --network-id <id>       P2P network ID. Default: 14699254016670310680
  --listen-addr <addr>    P2P listen address. Default: 0.0.0.0:6001
  --rpc-addr <addr>       Local RPC listen address. Default: 127.0.0.1:8545
  --reset-state           Remove existing local chain state before first start.
  --help                  Show this help message.

This bootstrap installs a validator-only NHBCoin node. It writes the validator
key into /etc/nhbchain/node.env (mode 600), installs nhb.service, builds the
node from the checked-out repo, and starts the validator. The node itself will
auto-submit validator heartbeats after startup so it can become quorum-ready by
the next epoch.
EOF
}

rand_hex() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  else
    head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'
  fi
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "[ERROR] required command not found: $1" >&2
    exit 1
  }
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --validator-key)
      VALIDATOR_KEY="${2:-}"
      shift 2
      ;;
    --bootnode)
      BOOTNODE="${2:-}"
      shift 2
      ;;
    --network-id)
      NETWORK_ID="${2:-}"
      shift 2
      ;;
    --listen-addr)
      LISTEN_ADDR="${2:-}"
      shift 2
      ;;
    --rpc-addr)
      RPC_ADDR="${2:-}"
      shift 2
      ;;
    --reset-state)
      RESET_STATE=1
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "[ERROR] unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

if [[ -z "${VALIDATOR_KEY}" ]]; then
  echo "[ERROR] --validator-key is required" >&2
  usage
  exit 1
fi

require_cmd sudo
require_cmd rsync
require_cmd perl
require_cmd systemctl
require_cmd /usr/local/go/bin/go

sudo useradd --system --home "${INSTALL_ROOT}" --shell /usr/sbin/nologin "${SERVICE_USER}" 2>/dev/null || true
sudo mkdir -p "${CONFIG_DIR}" "${STATE_DIR}" "${INSTALL_ROOT}/bin"
sudo chmod 700 "${CONFIG_DIR}"

JWT_SECRET=$(rand_hex)
sudo tee "${CONFIG_DIR}/node.env" >/dev/null <<EOF
NHB_ENV=prod
NHB_RPC_JWT_SECRET=${JWT_SECRET}
NHB_VALIDATOR_RAW_KEY=${VALIDATOR_KEY}
EOF
sudo chmod 600 "${CONFIG_DIR}/node.env"
sudo chown root:root "${CONFIG_DIR}/node.env"

sudo cp "${REPO_ROOT}/config.toml" "${CONFIG_DIR}/config.toml"
sudo perl -0pi -e "s#(?m)^ListenAddress = \".*\"#ListenAddress = \"${LISTEN_ADDR}\"#;" "${CONFIG_DIR}/config.toml"
sudo perl -0pi -e "s#(?m)^RPCAddress = \".*\"#RPCAddress = \"${RPC_ADDR}\"#;" "${CONFIG_DIR}/config.toml"
sudo perl -0pi -e "s#(?m)^DataDir = \".*\"#DataDir = \"${STATE_DIR}/nhb-data\"#;" "${CONFIG_DIR}/config.toml"
sudo perl -0pi -e "s#(?m)^ValidatorKeystorePath = \".*\"#ValidatorKeystorePath = \"\"#;" "${CONFIG_DIR}/config.toml"
sudo perl -0pi -e "s#(?m)^ValidatorKMSEnv = \".*\"#ValidatorKMSEnv = \"NHB_VALIDATOR_RAW_KEY\"#;" "${CONFIG_DIR}/config.toml"
sudo perl -0pi -e "s#(?m)^NetworkName = \".*\"#NetworkName = \"nhb-mainnet-validator\"#;" "${CONFIG_DIR}/config.toml"
sudo perl -0pi -e "s#(?m)^  NetworkId = .*#  NetworkId = ${NETWORK_ID}#;" "${CONFIG_DIR}/config.toml"
sudo perl -0pi -e "s#(?m)^  Bootnodes = \\[.*\\]#  Bootnodes = [\"${BOOTNODE}\"]#;" "${CONFIG_DIR}/config.toml"
sudo perl -0pi -e "s#(?m)^  PersistentPeers = \\[.*\\]#  PersistentPeers = [\"${BOOTNODE}\"]#;" "${CONFIG_DIR}/config.toml"

sudo rsync -a --delete "${REPO_ROOT}/" "${INSTALL_ROOT}/"
sudo chown -R "${SERVICE_USER}:${SERVICE_USER}" "${INSTALL_ROOT}" "${STATE_DIR}"

if [[ "${RESET_STATE}" == "1" ]]; then
  echo "[INFO] resetting validator state under ${STATE_DIR}/nhb-data"
  sudo rm -rf "${STATE_DIR}/nhb-data"
fi

echo "[INFO] building NHB validator binaries"
cd "${INSTALL_ROOT}"
sudo env PATH=/usr/local/go/bin:/usr/bin:/bin GOCACHE=/tmp/nhb-gocache GOPATH=/tmp/nhb-gopath HOME=/root \
  /usr/local/go/bin/go build -trimpath -ldflags="-s -w" -buildvcs=false -o "${INSTALL_ROOT}/bin/nhb" ./cmd/nhb
sudo env PATH=/usr/local/go/bin:/usr/bin:/bin GOCACHE=/tmp/nhb-gocache GOPATH=/tmp/nhb-gopath HOME=/root \
  /usr/local/go/bin/go build -trimpath -ldflags="-s -w" -buildvcs=false -o "${INSTALL_ROOT}/bin/nhb-cli" ./cmd/nhb-cli

sudo install -m 0644 "${INSTALL_ROOT}/deploy/systemd/nhb.service" /etc/systemd/system/nhb.service
sudo systemctl daemon-reload
sudo systemctl enable nhb.service
sudo systemctl restart nhb.service

echo
echo "[OK] Validator node started."
echo "Check status with:"
echo "  sudo systemctl status nhb.service"
echo "  sudo journalctl -u nhb.service -f"
echo
echo "This node will auto-submit validator heartbeats after startup and can enter"
echo "the active validator set at the next epoch once it remains online and synced."
