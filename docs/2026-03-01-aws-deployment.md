# NHBChain AWS Production Deployment Guide

This guide details the definitive deployment architecture and steps required to securely host an NHBChain mainnet node on AWS, enabling external P2P connections and securely exposing RPC endpoints.

## 1. AWS Infrastructure Requirements

**EC2 Instance Specification:**
- **Instance Type:** `c6a.2xlarge` or `c7g.2xlarge` (Compute optimized, 8 vCPUs, 16GB RAM)
- **EBS Volume:** 2TB `gp3` (3000 IOPS, 250 MB/s throughput)
- **OS:** Ubuntu 24.04 LTS or Amazon Linux 2023

## 2. Security Groups (Firewall Configuration)

Validators must ensure their security group rules allow necessary P2P communication while strictly limiting RPC access.

| Protocol | Port Range | Source | Purpose |
|----------|------------|--------|---------|
| TCP      | `26656`    | `0.0.0.0/0` | P2P Network (External node syncing & POTSO rewards) |
| TCP      | `22`       | Custom Admin IPs | SSH Access |
| TCP      | `9091`     | Custom Admin IPs | Gateway P2P Metrics/Debugging (Optional) |

**Important:** Do **NOT** expose `9090` (Consensus RPC) or `8545` (Web3 Gateway EVM RPC) directly to `0.0.0.0/0` unless you explicitly intend to serve the public through the proper HTTP rate limiters and front-end load balancers configured in the Gateway. 

## 3. Node Configuration Setup

Before starting the node, modify your configuration files:

1. **P2P Configuration (`config/p2p.toml`)**:
   - `ListenAddress`: Must be set to `0.0.0.0:26656`
   - `ExternalAddress`: Set to your EC2 instance's Elastic IP `tcp://<Elastic-IP>:26656`
   - `PEX` (Peer Exchange) must be enabled (`true`) to participate in peer gossiping.

2. **Consensus Configuration (`config/consensus.toml`)**:
   - Set `ChainID` to `nhbchain-1`
   - Ensure `AllowlistCIDRs` limits administrative RPC capabilities to only your expected UI backend or VPC subnets.

3. **Global Configuration (`config/config.go`)**:
   - Network Name must be strictly `"mainnet"`.

## 4. Docker Compose Deployment

The recommended approach to run NHBChain is using the provided Docker Compose stack:

```bash
# 1. Clone the repository
git clone https://github.com/josephblackelite/nhbchain.git
cd nhbchain

# 2. Create the .env configuration file
cp deploy/compose/.env.example deploy/compose/.env
nano deploy/compose/.env # Make sure to populate NHB_VALIDATOR_PASS and Identity secrets!

# 3. Pull required images or build from source
sudo docker-compose -f deploy/compose/docker-compose.yml build

# 4. Start the cluster
sudo docker-compose -f deploy/compose/docker-compose.yml up -d
```

## 5. Monitoring & Maintenance

- Monitor the logs for the critical `PUBLIC NODE SETUP REQUIRED` banner. Ensure you see active peer connections over `26656`.
- Utilize standard Docker log rotation strategies. Lumberjack integration is configured by providing the `NHB_LOG_FILE` environment variable within your docker configuration or `.env`.
- Back up `/var/lib/nhb` (mounted on the EBS volume) carefully, as it contains your node identity and consensus state.
