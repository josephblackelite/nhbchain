#!/bin/bash
export PATH=/usr/local/go/bin:$PATH
cd /home/ubuntu/nhbchain

# Ensure we're up to date
git reset --hard
git pull origin main

# Build Both Binaries (Core and CLI)
chmod +x scripts/build.sh
./scripts/build.sh

# Cleanup previous state
rm -rf nhb-data
rm -f validator.keystore

# Prepare Hot Validator Architecture
export NHB_VALIDATOR_PASS="nhbmaster2026"
export NHB_ENV="prod"

echo "Generating new Hot Validator Key..."
HOT_ADDRESS=$(./bin/nhb-cli generate-key | grep -oE 'nhb1[a-zA-Z0-9]+' | head -n 1)

if [ -z "$HOT_ADDRESS" ]; then
    echo "Failed to generate Hot Address!"
    exit 1
fi
echo "[SUCCESS] Generated Hot Validator: $HOT_ADDRESS"

# Inject into Genesis Block safely
cp config/genesis.mainnet.json config/genesis.json
sed -i -E "s/\"address\": \"nhb1[a-zA-Z0-9]+\"/\"address\": \"$HOT_ADDRESS\"/g" config/genesis.json

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

