#!/usr/bin/env bash
set -euo pipefail

CONFIG_PATH=""

usage() {
  cat <<USAGE
Usage: $0 -c <config-file>

Validates that the supplied production configuration enables the safety
rails required for mainnet deployments. The script exits non-zero when a
violation is detected.

Only keys the node itself reads are checked, matched the way the node matches
them (config/config.go). The RPC listener must be one of:
  - TLS terminated by the node: RPCAllowInsecure false, with RPCTLSCertFile,
    RPCTLSKeyFile and RPCTLSClientCAFile set; or
  - plaintext on a loopback RPCAddress (RPCAllowInsecure true) with TLS
    terminated by a reverse proxy in front of the node. The node itself
    refuses plaintext on any other address.
USAGE
}

while getopts "c:h" opt; do
  case "$opt" in
    c)
      CONFIG_PATH="$OPTARG"
      ;;
    h)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 1
      ;;
  esac
done

if [[ -z "$CONFIG_PATH" ]]; then
  echo "error: configuration path is required" >&2
  usage >&2
  exit 1
fi

if [[ ! -f "$CONFIG_PATH" ]]; then
  echo "error: configuration file '$CONFIG_PATH' not found" >&2
  exit 1
fi

export CONFIG_PATH

python3 - <<'PY'
import ipaddress
import os
import sys
from typing import Sequence

try:
    import tomllib
except ModuleNotFoundError:
    import tomli as tomllib  # type: ignore

config_path = os.environ["CONFIG_PATH"]

with open(config_path, "rb") as fh:
    data = tomllib.load(fh)

errors: list[str] = []

# The node decodes its configuration with BurntSushi/toml (config/config.go): a
# key is matched to a struct field by exact name and then case-insensitively,
# and every other key is silently ignored. Look keys up the same way and only
# where the loader reads them, so a setting this script accepts is one the
# node actually applies.
def lookup(table, name: str):
    if not isinstance(table, dict):
        return None
    if name in table:
        return table[name]
    lowered = name.lower()
    for key, value in table.items():
        if key.lower() == lowered:
            return value
    return None

def require(path: Sequence[str]):
    cur = data
    for key in path:
        cur = lookup(cur, key)
        if cur is None:
            return None
    return cur

def ensure_string(path: Sequence[str], label: str):
    value = require(path)
    if value is None or not isinstance(value, str) or not value.strip():
        errors.append(f"{label} must be set")

def listener_host(value: str) -> str:
    raw = (value or "").strip()
    if "://" in raw:
        raw = raw.split("://", 1)[1]
    host = raw
    if raw.startswith("["):
        end = raw.find("]")
        if end != -1:
            host = raw[1:end]
    elif ":" in raw:
        host = raw.rsplit(":", 1)[0]
    return host.strip()

def is_unspecified_address(value: str) -> bool:
    raw = (value or "").strip()
    if not raw:
        return True
    if raw.startswith("unix://"):
        return False
    host = listener_host(raw)
    if not host:
        return True
    try:
        ip = ipaddress.ip_address(host)
        return ip.is_unspecified
    except ValueError:
        return host in {"*", "0.0.0.0"}

def is_loopback_address(value: str) -> bool:
    host = listener_host(value)
    if host.lower() == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False

def ensure_not_unspecified(path: Sequence[str], label: str):
    value = require(path)
    if isinstance(value, str) and is_unspecified_address(value):
        errors.append(f"{label} must not bind to an unspecified or wildcard address")

# Transport TLS between the node's internal services
if require(("network_security", "AllowInsecure")) is not False:
    errors.append("TLS must be enabled: network_security.AllowInsecure must be false")

# Network TLS assets must be populated.
ensure_string(("network_security", "ServerTLSCertFile"), "Server TLS certificate path")
ensure_string(("network_security", "ServerTLSKeyFile"), "Server TLS private key path")
ensure_string(("network_security", "ClientTLSCertFile"), "Client TLS certificate path")
ensure_string(("network_security", "ClientTLSKeyFile"), "Client TLS private key path")
ensure_string(("network_security", "ClientCAFile"), "Client CA bundle path")
ensure_string(("network_security", "ServerCAFile"), "Server CA bundle path")

# Listener binding checks
ensure_not_unspecified(("ListenAddress",), "ListenAddress")
ensure_not_unspecified(("RPCAddress",), "RPCAddress")

# RPC transport. Either the node terminates TLS itself, or it serves plaintext
# on a loopback address only, with TLS terminated by a reverse proxy in front
# of it. rpc/http.go refuses plaintext on any other address, so a config that
# asks for it anywhere else could not start; this rejects it up front.
if require(("RPCAllowInsecure",)) is True:
    rpc_address = require(("RPCAddress",))
    if not isinstance(rpc_address, str) or not is_loopback_address(rpc_address):
        errors.append("RPCAllowInsecure may only be true when RPCAddress is a loopback address (TLS must terminate in front of the node)")
    if require(("RPCAllowInsecureUnspecified",)) is True:
        errors.append("RPCAllowInsecureUnspecified must be false")
else:
    ensure_string(("RPCTLSCertFile",), "RPC TLS certificate path")
    ensure_string(("RPCTLSKeyFile",), "RPC TLS key path")
    ensure_string(("RPCTLSClientCAFile",), "RPC client CA bundle path")

# Loyalty pro-rate enforcement
if require(("global", "Loyalty", "Dynamic", "EnforceProRate")) is not True:
    errors.append("global.Loyalty.Dynamic.EnforceProRate must be true")

if require(("global", "Loyalty", "Dynamic", "EnableProRate")) is not True:
    errors.append("global.Loyalty.Dynamic.EnableProRate must be true")

# Fee routing wallets
owner_wallet = require(("global", "Fees", "OwnerWallet"))
if not isinstance(owner_wallet, str) or not owner_wallet.strip():
    errors.append("global.Fees.OwnerWallet must be set to a non-empty wallet address")

assets = require(("global", "Fees", "Assets"))
if not isinstance(assets, list) or not assets:
    errors.append("global.Fees.Assets must define at least one asset with an owner wallet")
else:
    for idx, asset in enumerate(assets):
        if not isinstance(asset, dict):
            errors.append(f"global.Fees.Assets[{idx}] must be a table")
            continue
        wallet = lookup(asset, "OwnerWallet")
        if not isinstance(wallet, str) or not wallet.strip():
            asset_name = lookup(asset, "Asset") or f"index {idx}"
            errors.append(f"global.Fees.Assets entry '{asset_name}' must set OwnerWallet")

# Staking emission caps
emission_raw = require(("global", "Staking", "MaxEmissionPerYearWei"))
if emission_raw is None:
    errors.append("global.Staking.MaxEmissionPerYearWei must be defined")
else:
    try:
        emission_val = int(str(emission_raw), 0)
        if emission_val <= 0:
            errors.append("global.Staking.MaxEmissionPerYearWei must be greater than zero")
    except ValueError:
        errors.append("global.Staking.MaxEmissionPerYearWei must be a positive integer value")

# Pause checks
pauses = require(("global", "Pauses"))
if isinstance(pauses, dict):
    unsafe = [key for key, value in pauses.items() if value is True]
    if unsafe:
        errors.append("global.Pauses disables critical modules: " + ", ".join(sorted(unsafe)))
elif pauses is None:
    errors.append("global.Pauses must be defined as a table of pause flags")
else:
    errors.append("global.Pauses must be a table of pause flags")

if errors:
    for err in errors:
        print(f"[verify_prod_config] {err}", file=sys.stderr)
    sys.exit(1)
PY

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/.." && pwd)

prod_targets=(
  "${REPO_ROOT}/config.toml"
  "${REPO_ROOT}/deploy/compose/config"
  "${REPO_ROOT}/deploy/helm/values/prod"
  "${REPO_ROOT}/deploy/helm/values/staging"
  "${REPO_ROOT}/deploy/helm/values/dev"
  "${REPO_ROOT}/deploy/helm/consensusd/values.yaml"
)

violations=0

search_pattern() {
  local regex="$1"
  local path="$2"
  if command -v rg >/dev/null 2>&1; then
    rg --no-heading --line-number "$regex" "$path"
  else
    grep -R -n -E "$regex" "$path"
  fi
}

# RPCAllowInsecure is judged above, together with the RPC listen address; the
# textual check below only catches the internal transport's AllowInsecure flag.
for target in "${prod_targets[@]}"; do
  if [[ -d "$target" ]]; then
    if search_pattern '^[[:space:]]*[^#\n]*0\.0\.0\.0' "$target"; then
      echo "error: wildcard bind detected in production artifact '$target'" >&2
      violations=1
    fi
    if search_pattern '^[[:space:]]*AllowInsecure[[:space:]]*=[[:space:]]*true' "$target"; then
      echo "error: AllowInsecure=true found in production artifact '$target'" >&2
      violations=1
    fi
  elif [[ -f "$target" ]]; then
    if search_pattern '^[[:space:]]*[^#\n]*0\.0\.0\.0' "$target"; then
      echo "error: wildcard bind detected in production artifact '$target'" >&2
      violations=1
    fi
    if search_pattern '^[[:space:]]*AllowInsecure[[:space:]]*=[[:space:]]*true' "$target"; then
      echo "error: AllowInsecure=true found in production artifact '$target'" >&2
      violations=1
    fi
  fi
done

if [[ "$violations" -ne 0 ]]; then
  exit 1
fi
