#!/usr/bin/env bash
set -euo pipefail

CONFIG_PATH=""

usage() {
  cat <<USAGE
Usage: $0 -c <config-file>

Validates that the supplied production configuration enables the safety
rails required for mainnet deployments. The script exits non-zero when a
violation is detected.
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
import os
import sys
from typing import Iterable, Sequence

try:
    import tomllib
except ModuleNotFoundError:
    import tomli as tomllib  # type: ignore

config_path = os.environ["CONFIG_PATH"]

with open(config_path, "rb") as fh:
    data = tomllib.load(fh)

errors: list[str] = []

def require(path: Sequence[str]):
    cur = data
    for key in path:
        if isinstance(cur, dict) and key in cur:
            cur = cur[key]
        else:
            return None
    return cur

def first_defined(paths: Iterable[Sequence[str]]):
    for path in paths:
        value = require(path)
        if value is not None:
            return value
    return None

def ensure_string(paths: Iterable[Sequence[str]], label: str):
    value = first_defined(paths)
    if value is None or not isinstance(value, str) or not value.strip():
        errors.append(f"{label} must be set")

# TLS requirements
allow_insecure = first_defined((("network_security", "AllowInsecure"),))
if allow_insecure is not False:
    errors.append("TLS must be enabled: network_security.AllowInsecure must be false")

rpc_allow_insecure = first_defined((
    ("RPCAllowInsecure",),
    ("global", "RPCAllowInsecure"),
    ("network_security", "RPCAllowInsecure"),
    ("global", "staking", "RPCAllowInsecure"),
))
if rpc_allow_insecure is not False:
    errors.append("TLS must be enabled: RPCAllowInsecure must be false")

# Network TLS assets must be populated.
ensure_string((("network_security", "ServerTLSCertFile"),), "Server TLS certificate path")
ensure_string((("network_security", "ServerTLSKeyFile"),), "Server TLS private key path")
ensure_string((("network_security", "ClientTLSCertFile"),), "Client TLS certificate path")
ensure_string((("network_security", "ClientTLSKeyFile"),), "Client TLS private key path")
ensure_string((("network_security", "ClientCAFile"),), "Client CA bundle path")
ensure_string((("network_security", "ServerCAFile"),), "Server CA bundle path")

# RPC TLS configuration may be nested or flat.
ensure_string((
    ("global", "RPC", "TLSCertFile"),
    ("RPCTLSCertFile",),
    ("global", "staking", "RPCTLSCertFile"),
    ("network_security", "RPCTLSCertFile"),
), "RPC TLS certificate path")
ensure_string((
    ("global", "RPC", "TLSKeyFile"),
    ("RPCTLSKeyFile",),
    ("global", "staking", "RPCTLSKeyFile"),
    ("network_security", "RPCTLSKeyFile"),
), "RPC TLS key path")
ensure_string((
    ("global", "RPC", "TLSClientCAFile"),
    ("RPCTLSClientCAFile",),
    ("global", "staking", "RPCTLSClientCAFile"),
    ("network_security", "RPCTLSClientCAFile"),
), "RPC client CA bundle path")

# Loyalty pro-rate enforcement
enforce_prorate = first_defined((("global", "loyalty", "Dynamic", "EnforceProRate"),))
if enforce_prorate is not True:
    errors.append("global.loyalty.Dynamic.EnforceProRate must be true")

enable_prorate = first_defined((("global", "loyalty", "Dynamic", "enableprorate"),))
if enable_prorate is not True:
    errors.append("global.loyalty.Dynamic.enableprorate must be true")

# Fee routing wallets
owner_wallet = first_defined((("global", "fees", "owner_wallet"),))
if not isinstance(owner_wallet, str) or not owner_wallet.strip():
    errors.append("global.fees.owner_wallet must be set to a non-empty wallet address")

assets = first_defined((("global", "fees", "assets"),))
if not isinstance(assets, list) or not assets:
    errors.append("global.fees.assets must define at least one asset with an owner wallet")
else:
    for idx, asset in enumerate(assets):
        if not isinstance(asset, dict):
            errors.append(f"global.fees.assets[{idx}] must be a table")
            continue
        wallet = asset.get("owner_wallet")
        if not isinstance(wallet, str) or not wallet.strip():
            asset_name = asset.get("asset", f"index {idx}")
            errors.append(f"global.fees.assets entry '{asset_name}' must set owner_wallet")

# Staking emission caps
emission_raw = first_defined((("global", "staking", "MaxEmissionPerYearWei"),))
if emission_raw is None:
    errors.append("global.staking.MaxEmissionPerYearWei must be defined")
else:
    try:
        emission_val = int(str(emission_raw), 0)
        if emission_val <= 0:
            errors.append("global.staking.MaxEmissionPerYearWei must be greater than zero")
    except ValueError:
        errors.append("global.staking.MaxEmissionPerYearWei must be a positive integer value")

# Pause checks
pauses = first_defined((("global", "pauses"),))
if isinstance(pauses, dict):
    unsafe = [key for key, value in pauses.items() if value is True]
    if unsafe:
        errors.append("global.pauses disables critical modules: " + ", ".join(sorted(unsafe)))
elif pauses is None:
    errors.append("global.pauses must be defined as a table of pause flags")
else:
    errors.append("global.pauses must be a table of pause flags")

if errors:
    for err in errors:
        print(f"[verify_prod_config] {err}", file=sys.stderr)
    sys.exit(1)
PY
