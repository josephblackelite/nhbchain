# Public Testnet Launch Guide

This guide walks new operators and developers through connecting to the NHBChain public testnet, validating readiness scenarios, and locating support resources ahead of launch.

## Network Overview

| Item | Value |
| --- | --- |
| Chain ID | `nhb-testnet-1` |
| Genesis Time | TBA (announced in release bulletin) |
| Epoch Duration | 1 hour |
| Token | Test ZNHB (TZNHB) |
| Faucet Drip | 25 TZNHB per request, 10 requests / day |

Core endpoints are published on <https://status.testnet.nhbchain.io>. All endpoints are fronted by load balancers with TLS required.

## Prerequisites

* Go 1.21+
* `nhbchaind` binary from the launch candidate build
* 50 GB free disk for state sync snapshots
* Publicly reachable TCP ports 26656 (p2p) and 26657 (RPC)

## Genesis & Seeds

Download the launch package from the docs portal or the signed release announcement:

```bash
curl -O https://downloads.nhbchain.io/testnet/nhb-testnet-1.tar.gz
tar -xzf nhb-testnet-1.tar.gz
cp config/genesis.json ~/.nhbchaind/config/
```

Seed nodes:

```
seed-1.testnet.nhbchain.io:26656
seed-2.testnet.nhbchain.io:26656
seed-3.testnet.nhbchain.io:26656
```

Persistent peers (optional but recommended):

```
node01.testnet.nhbchain.io:26656
node02.testnet.nhbchain.io:26656
node03.testnet.nhbchain.io:26656
```

Rotate seeds weekly using `scripts/seed-rotation.sh`. Validators should subscribe to ops alerts for rotation notices.

## Configuration Checklist

1. Copy the testnet `config.toml` template from the launch package.
2. Set `persistent_peers` to the list above and `seeds` to empty when you become a validator.
3. Enable Prometheus metrics on port 26660 and configure your scraping agent.
4. Configure the [hardened security profile](../security/release-process.md#validator-hardening).
5. Restart the node and confirm it reaches the latest block height.

## Validator Launch Steps

```bash
nhbchaind init <moniker> --chain-id nhb-testnet-1
nhbchaind config chain-id nhb-testnet-1
nhbchaind start
```

1. Wait for synchronization and verify the latest height matches the public explorer.
2. Create a validator key: `nhbchaind keys add <name>` and back up the mnemonic.
3. Confirm the current `staking.minimumValidatorStake` parameter before bonding—governance can adjust this threshold. The initial launch setting is 1,000 TZNHB; hold at least the active value before submitting your create-validator transaction.
4. Monitor uptime via the explorer validator dashboard and `nhbchaind query staking validator <valoper>`.

## Developer End-to-End Validation

1. **Get Test Funds** – Use the [faucet](./faucet.md) to request TZNHB to a fresh address.
2. **Identity Registration** – Register a username and avatar using the identity module JSON-RPC methods.
3. **Swap Execution** – Swap TZNHB for stable assets using the swap module CLI or RPC and confirm balances.
4. **Escrow & Release** – Lock funds into escrow, fulfill the condition, and validate automatic release.
5. **POTSO Workflow** – Submit a Proof-of-Transfer Service Offer request and ensure the matching engine completes the cycle.
6. **Governance Vote** – Cast a vote on the test governance proposal to confirm wallet integration.
7. **Explorer Verification** – Locate each transaction and validator status in the [explorer](./explorer.md).
8. **API Smoke Tests** – Call RPC, REST, and WebSocket endpoints to confirm they expose blocks, events, and module state.

Document any discrepancies in the launch war room channel and open GitHub issues tagged `launch-blocker` when required.

## Public Endpoints

| Service | URL |
| --- | --- |
| RPC | `https://rpc.testnet.nhbchain.io` |
| REST | `https://rest.testnet.nhbchain.io` |
| WebSocket | `wss://rpc.testnet.nhbchain.io/websocket` |
| gRPC | `https://grpc.testnet.nhbchain.io` |

Rate limiting is enforced per IP. If you require dedicated throughput, request access to the partner endpoints through support.

## Troubleshooting

* **Node stuck catching up** – Enable state sync and verify your clock is synchronized via NTP.
* **Faucet errors** – Ensure the address uses the `tnhb` prefix and that you are below the daily rate limit.
* **Explorer not showing data** – Clear cached assets and check <https://status.testnet.nhbchain.io> for indexer updates.
* **Governance tx fails** – Confirm minimum deposit and that your account has the governance module permissions enabled.

Report critical incidents to `security@nhbcoin.net` using the [disclosure policy](../security/release-process.md#vulnerability-disclosure).
