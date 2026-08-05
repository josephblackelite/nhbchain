#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/.." && pwd)

INSTALL_ROOT=/opt/nhbchain
CONFIG_DIR=/etc/nhbchain
STATE_DIR=/var/lib/nhbchain
SERVICE_USER=nhb
VALIDATOR_KEY_FILE="${CONFIG_DIR}/validator.key"
ONBOARDING_EMAIL_ENDPOINT_DEFAULT='https://api.nhbcoin.com/v1/validators/onboarding-email'

BOOTNODE_DEFAULT='enode://bc1717ec2932efac3b37b9891f20f55cff491d48b790346ac02977cd646d4454@52.1.96.250:6001'
NETWORK_ID_DEFAULT='187001'
LISTEN_ADDR_DEFAULT='0.0.0.0:6001'
RPC_ADDR_DEFAULT='127.0.0.1:8545'

BOOTNODE="${BOOTNODE_DEFAULT}"
NETWORK_ID="${NETWORK_ID_DEFAULT}"
LISTEN_ADDR="${LISTEN_ADDR_DEFAULT}"
RPC_ADDR="${RPC_ADDR_DEFAULT}"
RESET_STATE=0
BENEFICIARY=''
OPERATOR_EMAIL=''
ONBOARDING_EMAIL_ENDPOINT="${ONBOARDING_EMAIL_ENDPOINT_DEFAULT}"

usage() {
  cat <<'EOF'
Usage:
  bash scripts/deployvalidator.sh [options]

Options:
  --beneficiary <nhb1...>  Wallet to receive this validator's future reward
                           payouts (see "Getting paid" below). Strongly
                           recommended -- without it, rewards accumulate at
                           this validator's own address, whose key never
                           leaves this server.
  --email <address>        Email to receive setup instructions (optional;
                           best-effort, does not fail the script if it can't
                           be sent).
  --bootnode <enode>       Bootnode enode to join. Default: NHBCoin mainnet bootnode.
  --network-id <id>        P2P network ID. Default: 187001
  --listen-addr <addr>     P2P listen address. Default: 0.0.0.0:6001
  --rpc-addr <addr>        Local RPC listen address. Default: 127.0.0.1:8545
  --reset-state            Remove existing local chain state before first start.
  --help                   Show this help message.

This bootstrap installs a validator-only NHBCoin node. It generates a fresh
validator key ON THIS MACHINE (never pass a key in -- one is created for
you the first time this runs, and reused on later runs), builds the node
from the checked-out repo, and starts the validator. The node itself will
auto-submit validator heartbeats after startup so it can become quorum-ready
by the next epoch.

Getting paid:
  Your validator's stake and its ordinary staking yield are handled by
  delegating to it from an nhbcoin.com wallet (Validator Hub -> paste this
  validator's node address -> delegate >= 10,000 ZNHB). That part happens in
  your own wallet, not on this server.

  Separately, this validator also earns a smaller epoch reward for actively
  participating in consensus, credited by default to this validator's own
  address -- which you cannot conveniently spend from, since its key must
  stay on this server. Passing --beneficiary redirects that reward to your
  own wallet automatically, signed locally with the key this script just
  generated, before it's ever used for anything else.
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "[ERROR] required command not found: $1" >&2
    exit 1
  }
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --beneficiary)
      BENEFICIARY="${2:-}"
      shift 2
      ;;
    --email)
      OPERATOR_EMAIL="${2:-}"
      shift 2
      ;;
    --onboarding-email-endpoint)
      ONBOARDING_EMAIL_ENDPOINT="${2:-}"
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

require_cmd sudo
require_cmd systemctl

# "Open a fresh EC2 Ubuntu server, git pull, run this script" is the whole
# promise -- a stock Ubuntu AMI has neither Go, rsync, nor perl installed, so
# failing here with "command not found" instead of just installing them
# would break that promise on literally the first run. Install what's
# missing instead of demanding the operator do it by hand first.
GO_VERSION="1.24.3"
GO_TARBALL="go${GO_VERSION}.linux-amd64.tar.gz"

APT_MISSING=()
command -v rsync >/dev/null 2>&1 || APT_MISSING+=(rsync)
command -v perl >/dev/null 2>&1 || APT_MISSING+=(perl)
command -v curl >/dev/null 2>&1 || APT_MISSING+=(curl)
if [[ ${#APT_MISSING[@]} -gt 0 ]]; then
  echo "[INFO] installing missing packages: ${APT_MISSING[*]}"
  sudo apt-get update -y
  sudo apt-get install -y "${APT_MISSING[@]}"
fi

if [[ ! -x /usr/local/go/bin/go ]]; then
  echo "[INFO] Go not found at /usr/local/go/bin/go -- installing Go ${GO_VERSION}"
  TMP_GO_DIR=$(mktemp -d)
  curl -fsSL "https://go.dev/dl/${GO_TARBALL}" -o "${TMP_GO_DIR}/${GO_TARBALL}"
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf "${TMP_GO_DIR}/${GO_TARBALL}"
  rm -rf "${TMP_GO_DIR}"
fi

require_cmd rsync
require_cmd perl
require_cmd /usr/local/go/bin/go

sudo useradd --system --home "${INSTALL_ROOT}" --shell /usr/sbin/nologin "${SERVICE_USER}" 2>/dev/null || true
sudo mkdir -p "${CONFIG_DIR}" "${STATE_DIR}" "${INSTALL_ROOT}/bin"
sudo chmod 700 "${CONFIG_DIR}"

sudo rsync -a --delete "${REPO_ROOT}/" "${INSTALL_ROOT}/"

echo "[INFO] building NHB validator binaries"
cd "${INSTALL_ROOT}"
sudo env PATH=/usr/local/go/bin:/usr/bin:/bin GOCACHE=/tmp/nhb-gocache GOPATH=/tmp/nhb-gopath HOME=/root \
  /usr/local/go/bin/go build -trimpath -ldflags="-s -w" -buildvcs=false -o "${INSTALL_ROOT}/bin/nhb" ./cmd/nhb
sudo env PATH=/usr/local/go/bin:/usr/bin:/bin GOCACHE=/tmp/nhb-gocache GOPATH=/tmp/nhb-gopath HOME=/root \
  /usr/local/go/bin/go build -trimpath -ldflags="-s -w" -buildvcs=false -o "${INSTALL_ROOT}/bin/nhb-cli" ./cmd/nhb-cli

# Generate the validator key ON THIS MACHINE the first time this script
# runs. Never pass a key in via a flag or environment variable set from
# outside this box -- that puts it in shell history and makes it visible to
# any other user via `ps aux` while this script runs. Re-running this
# script reuses the existing key rather than silently generating a new one
# and orphaning the old identity.
if [[ ! -f "${VALIDATOR_KEY_FILE}" ]]; then
  echo "[INFO] generating a fresh validator key on this machine"
  TMP_KEY_DIR=$(mktemp -d)
  trap 'rm -rf "${TMP_KEY_DIR}"' EXIT
  (cd "${TMP_KEY_DIR}" && "${INSTALL_ROOT}/bin/nhb-cli" generate-key >"${TMP_KEY_DIR}/generate-key.out")
  sudo install -m 0600 -o "${SERVICE_USER}" -g "${SERVICE_USER}" "${TMP_KEY_DIR}/wallet.key" "${VALIDATOR_KEY_FILE}"
  VALIDATOR_ADDRESS=$(grep -o 'nhb1[a-z0-9]*' "${TMP_KEY_DIR}/generate-key.out" | head -1)
  rm -rf "${TMP_KEY_DIR}"
  trap - EXIT
else
  echo "[INFO] reusing existing validator key at ${VALIDATOR_KEY_FILE}"
  VALIDATOR_ADDRESS=""
fi
sudo chown "${SERVICE_USER}:${SERVICE_USER}" "${VALIDATOR_KEY_FILE}"
sudo chmod 600 "${VALIDATOR_KEY_FILE}"

if [[ -z "${VALIDATOR_ADDRESS}" ]]; then
  # Deriving the address from an existing key file requires reading it, so
  # only the CLI (running as the service user) does this -- never dump the
  # raw key itself to stdout/logs.
  VALIDATOR_ADDRESS=$(sudo -u "${SERVICE_USER}" "${INSTALL_ROOT}/bin/nhb-cli" address "${VALIDATOR_KEY_FILE}" 2>/dev/null || true)
fi

VALIDATOR_KEY_HEX=$(sudo od -An -tx1 "${VALIDATOR_KEY_FILE}" | tr -d ' \n')

JWT_SECRET=$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
sudo tee "${CONFIG_DIR}/node.env" >/dev/null <<EOF
NHB_ENV=prod
NHB_RPC_JWT_SECRET=${JWT_SECRET}
NHB_VALIDATOR_RAW_KEY=${VALIDATOR_KEY_HEX}
EOF
sudo chmod 600 "${CONFIG_DIR}/node.env"
sudo chown root:root "${CONFIG_DIR}/node.env"
unset VALIDATOR_KEY_HEX

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

sudo chown -R "${SERVICE_USER}:${SERVICE_USER}" "${INSTALL_ROOT}" "${STATE_DIR}"

if [[ "${RESET_STATE}" == "1" ]]; then
  echo "[INFO] resetting validator state under ${STATE_DIR}/nhb-data"
  sudo rm -rf "${STATE_DIR}/nhb-data"
fi

sudo install -m 0644 "${INSTALL_ROOT}/deploy/systemd/nhb.service" /etc/systemd/system/nhb.service
sudo systemctl daemon-reload
sudo systemctl enable nhb.service
sudo systemctl restart nhb.service

if [[ -n "${BENEFICIARY}" ]]; then
  echo "[INFO] waiting for the node's RPC to come up so the reward beneficiary can be set"
  for _ in $(seq 1 30); do
    if curl -fsS -m 2 "http://${RPC_ADDR}/" >/dev/null 2>&1; then
      break
    fi
    sleep 2
  done
  echo "[INFO] setting reward beneficiary to ${BENEFICIARY}"
  if ! sudo -u "${SERVICE_USER}" env RPC_URL="http://${RPC_ADDR}" "${INSTALL_ROOT}/bin/nhb-cli" \
      set-reward-beneficiary "${BENEFICIARY}" "${VALIDATOR_KEY_FILE}"; then
    echo "[WARN] could not set the reward beneficiary automatically -- retry later with:"
    echo "  sudo -u ${SERVICE_USER} ${INSTALL_ROOT}/bin/nhb-cli set-reward-beneficiary ${BENEFICIARY} ${VALIDATOR_KEY_FILE}"
  fi
fi

if [[ -n "${OPERATOR_EMAIL}" ]]; then
  echo "[INFO] requesting onboarding instructions be emailed to ${OPERATOR_EMAIL}"
  curl -fsS -m 5 -X POST "${ONBOARDING_EMAIL_ENDPOINT}" \
    -H 'Content-Type: application/json' \
    -d "{\"nodeAddress\":\"${VALIDATOR_ADDRESS}\",\"email\":\"${OPERATOR_EMAIL}\"}" \
    >/dev/null 2>&1 \
    && echo "[OK] onboarding email requested" \
    || echo "[WARN] could not reach the onboarding email endpoint -- follow the instructions printed below instead"
fi

echo
echo "=================================================================="
echo "[OK] Validator node started."
echo
if [[ -n "${VALIDATOR_ADDRESS}" ]]; then
  echo "Your validator's node address is:"
  echo "  ${VALIDATOR_ADDRESS}"
  echo
fi
echo "To get paid, from a wallet you actually use:"
echo "  1. Go to https://nhbcoin.com and open (or create) your wallet."
echo "  2. Go to Validator Hub -> Delegate."
echo "  3. Paste the node address above, delegate at least 10,000 ZNHB."
if [[ -z "${BENEFICIARY}" ]]; then
  echo
  echo "You did not pass --beneficiary, so this validator's epoch consensus"
  echo "reward (separate from the staking yield above) will accumulate at its"
  echo "own address, which this server's key controls. To redirect it to a"
  echo "wallet you can actually spend from, run:"
  echo "  sudo -u ${SERVICE_USER} ${INSTALL_ROOT}/bin/nhb-cli set-reward-beneficiary <your-wallet-address> ${VALIDATOR_KEY_FILE}"
fi
echo
echo "Check status with:"
echo "  sudo systemctl status nhb.service"
echo "  sudo journalctl -u nhb.service -f"
echo
echo "This node will auto-submit validator heartbeats after startup and can enter"
echo "the active validator set at the next epoch once it remains online and synced."
echo "=================================================================="
