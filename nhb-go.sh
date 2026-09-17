#!/usr/bin/env bash
set -e

echo "==========================================================="
echo "   NHBCoin Quickstart: Node Initialization & Bootstrap     "
echo "==========================================================="

# Check minimum Go version
if ! command -v go &> /dev/null; then
    echo "[!] Go is not installed. Please install Go 1.23.0+ to build NHBChain."
    exit 1
fi

GO_VERSION=$(go version | awk '{print $3}')
echo "[i] Found Go version: $GO_VERSION"

echo "\n[1/3] Compiling node and CLI binaries..."
go build -o nhb-node ./cmd/nhb/
go build -o nhb-cli ./cmd/nhb-cli/
echo "[+] Successfully built nhb-node and nhb-cli."

echo "\n[2/3] Setting production environment variables..."
# Always default to prod to prevent accidental dev-mode loopholes
export NHB_ENV="prod" 

# Check for validator passphrase, prompt if missing
if [ -z "$NHB_VALIDATOR_PASS" ]; then
    echo -n "Enter a secure Validator Keystore Passphrase (used to encrypt your node's signing key): "
    read -rs pass
    echo ""
    export NHB_VALIDATOR_PASS="$pass"
fi

# Check for JWT secret, generate a secure random one if missing
if [ -z "$NHB_RPC_JWT_SECRET" ]; then
    echo "[i] Generating a new secure JWT secret for RPC authentication..."
    export NHB_RPC_JWT_SECRET=$(openssl rand -hex 32)
    echo "[!] IMPORTANT: Your generated RPC JWT Secret is: $NHB_RPC_JWT_SECRET"
    echo "[!] Save this secretly. You need it to request API tokens."
fi

echo "\n[3/3] Launching NHBCoin Validator Node..."
echo "[i] The node is running. Press Ctrl+C to stop."
echo "==========================================================="

# Boot the node
./nhb-node
