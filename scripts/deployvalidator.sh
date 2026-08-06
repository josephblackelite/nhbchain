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
NETWORK_ID_DEFAULT='10698789873712925303'
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
  --network-id <id>        P2P network ID. Default: 10698789873712925303
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

# Confirmed by running this exact script on a real t3.micro-class instance
# (908MB RAM, no swap -- a common free-tier/cheap default): compiling this
# dependency tree's CGo-free SQLite implementation (modernc.org/libc) got
# OOM-killed mid-build ("signal: killed"), even though disk space was
# plentiful. Add a swap file when there's little to no RAM and no existing
# swap, so the build has somewhere to page out to instead of getting killed
# -- cheap and safe on the plentiful disk space confirmed available above,
# and left in place afterward since the running validator benefits from the
# same headroom.
if [[ ! -f /swapfile ]] && [[ "$(swapon --show=SIZE --noheadings 2>/dev/null | wc -l)" -eq 0 ]]; then
  TOTAL_MEM_KB=$(awk '/MemTotal/ {print $2}' /proc/meminfo)
  if [[ -n "${TOTAL_MEM_KB}" ]] && [[ "${TOTAL_MEM_KB}" -lt 4194304 ]]; then
    echo "[INFO] low memory ($((TOTAL_MEM_KB / 1024))MB) and no swap configured -- adding a 4G swap file"
    sudo fallocate -l 4G /swapfile
    sudo chmod 600 /swapfile
    sudo mkswap /swapfile
    sudo swapon /swapfile
    grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab >/dev/null
  fi
fi

sudo useradd --system --home "${INSTALL_ROOT}" --shell /usr/sbin/nologin "${SERVICE_USER}" 2>/dev/null || true
sudo mkdir -p "${CONFIG_DIR}" "${STATE_DIR}" "${INSTALL_ROOT}/bin"
# Confirmed by running this exact script end-to-end: nhb.service (which runs
# as User=nhb, not root) crash-looped forever with "panic: Failed to load
# config: open /etc/nhbchain/config.toml: permission denied" -- mkdir here
# runs under sudo, so the directory was owned by root, and mode 700 denies
# *every* non-owner, including the nhb user, from even traversing into it
# to read config.toml/node.env/validator.key -- regardless of those
# individual files' own (correct) nhb:nhb ownership. The service silently
# never actually started; only the crash-loop's own restart backoff kept
# systemd from reporting it as immediately failed. Own the directory as the
# service user so it can read what's already correctly chowned to it.
sudo chown "${SERVICE_USER}:${SERVICE_USER}" "${CONFIG_DIR}"
sudo chmod 700 "${CONFIG_DIR}"

sudo rsync -a --delete "${REPO_ROOT}/" "${INSTALL_ROOT}/"

echo "[INFO] building NHB validator binaries"
cd "${INSTALL_ROOT}"
# Go's module cache for this dependency tree (go-ethereum, protobuf, sqlite,
# opentelemetry, etc.) needs well over a gigabyte -- confirmed by running
# this exact script on a stock Ubuntu AMI, which builds cleanly, /tmp filled
# up mid-download ("no space left on device") because /tmp there is a
# tmpfs mount capped at a few hundred MB (RAM-backed, common EC2 default),
# while the real disk had 90+ GB free and untouched. Build under
# INSTALL_ROOT instead, which is already on that real disk.
GOCACHE_DIR="${INSTALL_ROOT}/.gocache"
GOPATH_DIR="${INSTALL_ROOT}/.gopath"
sudo mkdir -p "${GOCACHE_DIR}" "${GOPATH_DIR}"
sudo env PATH=/usr/local/go/bin:/usr/bin:/bin GOCACHE="${GOCACHE_DIR}" GOPATH="${GOPATH_DIR}" HOME=/root \
  /usr/local/go/bin/go build -trimpath -ldflags="-s -w" -buildvcs=false -o "${INSTALL_ROOT}/bin/nhb" ./cmd/nhb
sudo env PATH=/usr/local/go/bin:/usr/bin:/bin GOCACHE="${GOCACHE_DIR}" GOPATH="${GOPATH_DIR}" HOME=/root \
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
# The enode string contains "@" (nodeid@host:port). Perl treats an
# unescaped "@" in the s/// replacement text as array interpolation
# (e.g. "@52" -> array @52, silently expanding to ""), which corrupted
# the peer address into "<nodeid>.1.96.250:6001" and broke P2P dialing.
# Escape it to a literal before splicing into the Perl program.
BOOTNODE_ESCAPED=$(printf '%s' "${BOOTNODE}" | sed 's/@/\\\\@/g')
sudo perl -0pi -e "s#(?m)^  Bootnodes = \\[.*\\]#  Bootnodes = [\"${BOOTNODE_ESCAPED}\"]#;" "${CONFIG_DIR}/config.toml"
sudo perl -0pi -e "s#(?m)^  PersistentPeers = \\[.*\\]#  PersistentPeers = [\"${BOOTNODE_ESCAPED}\"]#;" "${CONFIG_DIR}/config.toml"

sudo chown -R "${SERVICE_USER}:${SERVICE_USER}" "${INSTALL_ROOT}" "${STATE_DIR}"

if [[ "${RESET_STATE}" == "1" ]]; then
  echo "[INFO] resetting validator state under ${STATE_DIR}/nhb-data"
  sudo rm -rf "${STATE_DIR}/nhb-data"
fi

sudo install -m 0644 "${INSTALL_ROOT}/deploy/systemd/nhb.service" /etc/systemd/system/nhb.service
sudo systemctl daemon-reload
sudo systemctl enable nhb.service
sudo systemctl restart nhb.service

# Confirmed by running this exact script end-to-end: the service can
# silently crash-loop forever (e.g. a config it can't read) while this
# script barrels ahead and prints a false "[OK] Validator node started" at
# the end regardless -- the RPC-wait loop below previously only ran when
# --beneficiary was given, and even then it just moved on after timing out
# rather than treating that as a real failure. Always wait for the RPC to
# actually come up, and stop with clear diagnostics if it never does.
echo "[INFO] waiting for the node's RPC to come up"
NODE_HEALTHY=0
for _ in $(seq 1 30); do
  if curl -fsS -m 2 "http://${RPC_ADDR}/" >/dev/null 2>&1; then
    NODE_HEALTHY=1
    break
  fi
  sleep 2
done

if [[ "${NODE_HEALTHY}" != "1" ]]; then
  echo
  echo "=================================================================="
  echo "[ERROR] nhb.service did not come up after 60 seconds."
  echo
  echo "Check what's actually wrong with:"
  echo "  sudo systemctl status nhb.service"
  echo "  sudo journalctl -u nhb.service -n 50 --no-pager"
  echo "=================================================================="
  exit 1
fi

if [[ -n "${BENEFICIARY}" ]]; then
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
