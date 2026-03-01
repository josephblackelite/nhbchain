# NHBChain — Comprehensive Deployment Guide
**Date:** 2026-03-01

This guide covers deploying NHBChain locally for testing and robustness validation, and globally on AWS for production mainnet.

---

## 1. Localhost Deployment (Testing & Robustness)

Run the chain on `localhost` first to ensure all modules are functioning and configurations are correct before touching AWS.

### 1.1 Prerequisites
- Go 1.23+
- Git, curl, jq

### 1.2 Build from Source
```bash
git clone https://github.com/josephblackelite/nhbchain.git
cd nhbchain
go mod tidy
go build -o nhb-node ./cmd/nhb/
go build -o nhb-cli ./cmd/nhb-cli/
```

### 1.3 Local Network Initialization
The local testnet uses `config/genesis.local.json` with `autoPopulateLocal: true`, which auto-generates your validator and provides test balances.

1. **Environment Variables**:
   ```bash
   export NHB_ENV="dev"
   export NHB_VALIDATOR_PASS="test1234"
   export NHB_RPC_JWT_SECRET="local-secret"
   ```
2. **Start the Node**:
   ```bash
   ./nhb-node --config config/config.toml
   ```
3. **Verify Status**:
   ```bash
   curl http://127.0.0.1:8080/nhb_getNetworkInfo
   ```

**Goal:** Ensure the chain produces blocks locally and endpoints return successfully. Once robust, proceed to AWS.

---

## 2. AWS Mainnet Deployment (Step-by-Step)

The production network requires multiple nodes. The "Master Validator" (Genesis node) starts the chain, Seed Nodes connect it globally, and public RPC endpoints serve external traffic.

### 2.1 Instance Sizing & Security Groups
- **Recommended OS:** Ubuntu 22.04 LTS
- **Instance Type:** c5.2xlarge (or equivalent minimum 4 vCPU, 8 GB RAM)
- **Storage:** Minimum 250 GB gp3 SSD

**AWS Security Group Inbound Rules:**
- **TCP 6001**: Open to `0.0.0.0/0` for the P2P validator network.
- **TCP 8080**: Restrict to `localhost` or your internal Load Balancer ONLY.
- **TCP 443** (If running reverse proxy): Open to `0.0.0.0/0`.
- **TCP 22**: SSH restricted to your IP.

### 2.2 Installing the Node on AWS (Master Validator)
SSH into your first AWS instance.

1. **Install Go & Dependencies**:
   ```bash
   sudo apt update && sudo apt install -y git build-essential tmux curl jq
   wget https://go.dev/dl/go1.23.0.linux-amd64.tar.gz
   sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.23.0.linux-amd64.tar.gz
   echo 'export PATH="/usr/local/go/bin:$PATH"' >> ~/.bashrc
   source ~/.bashrc
   ```
2. **Clone and Build**:
   ```bash
   git clone https://github.com/josephblackelite/nhbchain.git /opt/nhbchain
   cd /opt/nhbchain
   go build -o nhb-node ./cmd/nhb/
   go build -o nhb-cli ./cmd/nhb-cli/
   ```

### 2.3 Cryptographic Key Generation
Generate real cryptographic keys for Mainnet operations using the CLI:
```bash
./nhb-cli generate-key > owner_wallet.key
./nhb-cli generate-key > treasury_wallet.key
./nhb-cli generate-key > master_validator.key
```
*Store these keys in AWS Secrets Manager.*

### 2.4 Create `genesis.mainnet.json`
Populate the genesis file (`/etc/nhb/genesis.json`) using the addresses (the `nhb1...` strings) generated in the previous step.
```json
{
  "chainId": 187001,
  "nativeTokens": [
    { "symbol": "NHB", "decimals": 18, "mintAuthority": "<OWNER_WALLET_ADDRESS>" },
    { "symbol": "ZNHB", "decimals": 18, "mintAuthority": "<TREASURY_WALLET_ADDRESS>" }
  ],
  "validators": [
    { "address": "<MASTER_VALIDATOR_ADDRESS>", "power": 10 }
  ]
}
```

### 2.5 Node Configuration
Create `/etc/nhb/config.toml`:
```toml
ListenAddress = "0.0.0.0:6001"
RPCAddress = "127.0.0.1:8080"
DataDir = "/var/lib/nhb"
GenesisFile = "/etc/nhb/genesis.json"
AllowAutogenesis = false
NetworkName = "nhb-mainnet"

[mempool]
MaxTransactions = 5000

[p2p]
NetworkId = 187001
MaxPeers = 64
Bootnodes = []  # Empty for Master Node
Seeds = []
```

Create `/etc/nhb/nhb.env` to inject secure variables:
```bash
export NHB_ENV="prod"
export NHB_VALIDATOR_PASS="<From-Secrets-Manager>"
export NHB_RPC_JWT_SECRET="<JWT-Secret>"
export NHB_NETWORK_SHARED_SECRET="<P2P-Secret>"
```

### 2.6 Start the Master Node
Use `tmux` or Linux `systemd` to keep the node running.
```bash
source /etc/nhb/nhb.env
/opt/nhbchain/nhb-node --config /etc/nhb/config.toml
```

---

## 3. Connecting Seed Nodes & Other Validators
A blockchain requires multiple peers. Deploy 2 additional EC2 instances in different regions (e.g., EU, Asia) following the steps above, but update their configuration to connect to the Master.

**On Seed Node 1 (`seed1.nhbcoin.com`):**
In `config.toml`, setup the master node as a Bootnode:
```toml
Bootnodes = [
  "nhb1...<master_validator_address>@<MASTER_NODE_IP>:6001"
]
```

---

## 4. Public Gateway & Load Balancing
You **must** place production nodes behind a reverse proxy (like NGINX on an Application Load Balancer).
1. Configure a domain (e.g., `rpc.nhbcoin.com`) pointing to the ALB.
2. The ALB handles TLS Termination (Port 443 with an AWS Certificate Manager cert).
3. The ALB routes traffic securely back to the VPC instance's port 8080.
4. Implement strict request limits in AWS WAF to mitigate DDoS attacks.
