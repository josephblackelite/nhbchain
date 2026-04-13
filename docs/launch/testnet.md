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

Core endpoints are published on <https://status.testnet.nhbcoin.net>. All endpoints are fronted by load balancers with TLS required.

## Prerequisites

* Go 1.21+
* `nhbchaind` binary from the launch candidate build
* 50 GB free disk for state sync snapshots
* Publicly reachable TCP ports 26656 (p2p) and 26657 (RPC)

## Genesis & Seeds

Download the launch package from the docs portal or the signed release announcement:

```bash
curl -O https://downloads.nhbcoin.net/testnet/nhb-testnet-1.tar.gz
tar -xzf nhb-testnet-1.tar.gz
cp config/genesis.json ~/.nhbchaind/config/
```

Seed nodes:

```
seed-1.testnet.nhbcoin.net:26656
seed-2.testnet.nhbcoin.net:26656
seed-3.testnet.nhbcoin.net:26656
```

Persistent peers (optional but recommended):

```
node01.testnet.nhbcoin.net:26656
node02.testnet.nhbcoin.net:26656
node03.testnet.nhbcoin.net:26656
```

Rotate seeds weekly using `scripts/seed-rotation.sh`. Validators should subscribe to ops alerts for rotation notices.

## Wiring the Testnet

Follow this sequence any time you connect fresh infrastructure to the public testnet:

1. **Provision network perimeter** – Open TCP/26656 (P2P) and TCP/26657 (RPC) to trusted source ranges only. Outbound traffic should be unrestricted so the node can discover peers and fetch snapshots. Keep SSH/RDP on a separate management network.
2. **Install launch artifacts** – Extract the launch package to `/var/lib/nhbchaind` (or your preferred data directory) and double-check the `genesis.json` hash against the value posted on the status page. Reject any archive that fails signature validation.
3. **Harden TLS & auth gateways** – Terminate TLS in front of RPC/REST/gRPC endpoints and enforce HMAC authentication or mutual TLS as documented in [`docs/networking/net-rpc.md`](../networking/net-rpc.md). Disable anonymous RPC entirely.
4. **Wire observability** – Enable Prometheus on `:26660`, ship logs to your SIEM, and configure alerting for: missed blocks, consensus health, RPC auth failures, and handshake violations. Every new validator should register the provided dashboards before going live.
5. **Validate handshake path** – Run `nhbchaind status` to ensure the node advances past genesis, then execute `curl https://rpc.testnet.nhbcoin.net/healthz` from the host to confirm outbound internet routing and TLS trust. The P2P log should show successful NET-2A challenges.
6. **Exercise fail safes** – Trigger a manual `net_ban` via authenticated RPC against a known test peer and verify that your automation lifts the ban after expiry. This proves that operator controls are functioning before you accept production traffic.

Document completion of each step in your runbook; audits expect proof that the node was introduced following least-privilege and zero-trust principles.

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

Run `go test ./tests/e2e/...` after every deployment. The E2E harness signs transactions with disposable keys, verifies consensus, and asserts RPC auth enforcement so you know the internal test coverage is live before exposing public services.

Document any discrepancies in the launch war room channel and open GitHub issues tagged `launch-blocker` when required.

## Public Endpoints

| Service | URL |
| --- | --- |
| RPC | `https://rpc.testnet.nhbcoin.net` |
| REST | `https://rest.testnet.nhbcoin.net` |
| WebSocket | `wss://rpc.testnet.nhbcoin.net/websocket` |
| gRPC | `https://grpc.testnet.nhbcoin.net` |

Rate limiting is enforced per IP. If you require dedicated throughput, request access to the partner endpoints through support.

## Troubleshooting

* **Node stuck catching up** – Enable state sync and verify your clock is synchronized via NTP.
* **Faucet errors** – Ensure the address uses the `tnhb` prefix and that you are below the daily rate limit.
* **Explorer not showing data** – Clear cached assets and check <https://status.testnet.nhbcoin.net> for indexer updates.
* **Governance tx fails** – Confirm minimum deposit and that your account has the governance module permissions enabled.

Report critical incidents to `security@nehborly.net` using the [disclosure policy](../security/release-process.md#vulnerability-disclosure).
