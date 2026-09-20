#!/bin/bash
# This script wipes existing chain state (nhb-data) and the validator
# keystore, generates a new hot validator key and starts a node from the live
# network's genesis file (config/genesis.relaunch.json, used exactly as
# shipped). It is NOT a routine "redeploy latest code" script despite the
# generic name -- running it against a server with real chain history will
# destroy that history. Guarded behind NHB_CONFIRM_RESET so it cannot run by
# accident.
if [ "${NHB_CONFIRM_RESET:-}" != "yes" ]; then
  echo "Error: this script deletes nhb-data and validator.keystore and starts a fresh node from the shipped genesis."
  echo "Set NHB_CONFIRM_RESET=yes if that is genuinely what you intend to do."
  exit 1
fi
if [ -z "${NHB_VALIDATOR_PASS:-}" ]; then
  echo "Error: NHB_VALIDATOR_PASS must be set in the environment before running this script."
  exit 1
fi

export PATH=/usr/local/go/bin:$PATH
cd /home/ubuntu/nhbchain

# Ensure we're up to date
git reset --hard
git pull origin main

# Build Both Binaries (Core and CLI)
chmod +x scripts/build.sh
./scripts/build.sh

# The node starts from the live network's genesis file, unmodified:
# config.toml's GenesisFile already points at it. Its hash is the chain id, so
# rewriting anything inside it (a validator address, for one) would start a
# different chain that no peer of the live network talks to. Check it before
# anything is deleted.
GENESIS_FILE=config/genesis.relaunch.json
GENESIS_SHA256=10932798a0058ae35b135dae1a6ee1bdf6a8bc528a55c1eeb3e9eaab534f4b3b
LIVE_CHAIN_ID=18346390202490284624
if ! echo "$GENESIS_SHA256  $GENESIS_FILE" | sha256sum -c --status; then
    echo "Error: $GENESIS_FILE is not the live network's genesis file (expected sha256 $GENESIS_SHA256)."
    exit 1
fi

# Cleanup previous state
rm -rf nhb-data
rm -f validator.keystore

# Prepare Hot Validator Architecture
export NHB_ENV="prod"

echo "Generating new Hot Validator Key..."
# --force: a wallet.key left by an earlier run is copied to wallet.key.bak-<time>
# before it is replaced (generate-key refuses to overwrite a key otherwise).
HOT_ADDRESS=$(./bin/nhb-cli generate-key --force | grep -oE 'nhb1[a-zA-Z0-9]+' | head -n 1)

if [ -z "$HOT_ADDRESS" ]; then
    echo "Failed to generate Hot Address!"
    exit 1
fi
echo "[SUCCESS] Generated Hot Validator: $HOT_ADDRESS"

# The key generated above is not a genesis validator (the genesis is used
# unmodified, see the check before the cleanup step): it is registered on chain
# like any other new validator.

# Provide the node with an encrypted keystore (converted from the raw wallet.key)
cat << 'EOF' > convert_key.go
package main

import (
	"fmt"
	"os"
	"nhbchain/crypto"
)

func main() {
	keyBytes, err := os.ReadFile("wallet.key")
	if err != nil { panic(err) }
	
	privKey, err := crypto.PrivateKeyFromBytes(keyBytes)
	if err != nil { panic(err) }
	
	pass := os.Getenv("NHB_VALIDATOR_PASS")
	if err := crypto.SaveToKeystore("validator.keystore", privKey, pass); err != nil {
		panic(err)
	}
	fmt.Println("Successfully converted wallet.key to encrypted validator.keystore")
}
EOF
go run convert_key.go

# Expose RPC and P2P for the SvelteKit Portal and External Nodes
sed -i 's/RPCAllowInsecure = false/RPCAllowInsecure = true/g' config.toml
sed -i 's/RPCAddress = "127.0.0.1:8080"/RPCAddress = "127.0.0.1:8545"/g' config.toml
sed -i 's/ListenAddress = "127.0.0.1:6001"/ListenAddress = "0.0.0.0:6001"/g' config.toml

# Remove dummy DNS seeds so the Genesis node isn't blocked waiting for them
sed -i 's/Seeds = \["nhb1seed.*\]/Seeds = \[\]/g' config.toml


# Boot the node
nohup ./bin/nhb --config config.toml > node.log 2>&1 &
sleep 5
cat node.log

# Confirm the node came up on the live chain identity.
NODE_CHAIN_ID=""
for _ in $(seq 1 30); do
    NODE_CHAIN_ID=$(curl -fsS -m 5 http://127.0.0.1:8545/ -X POST -H 'Content-Type: application/json' \
      -d '{"jsonrpc":"2.0","id":1,"method":"net_info","params":[]}' 2>/dev/null | grep -o '"chainId":[0-9]*' | head -1 | cut -d: -f2)
    [ -n "$NODE_CHAIN_ID" ] && break
    sleep 2
done
if [ "$NODE_CHAIN_ID" != "$LIVE_CHAIN_ID" ]; then
    echo "Error: the node reports chain id '${NODE_CHAIN_ID:-unknown}', expected $LIVE_CHAIN_ID. Check node.log."
    exit 1
fi
echo "[SUCCESS] Node is running on chain id $NODE_CHAIN_ID"

