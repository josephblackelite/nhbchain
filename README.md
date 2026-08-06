# NHBCoin Layer 1 Node

Welcome to the official Go implementation of the NHBCoin Layer 1 (L1) blockchain. This repository hosts the production codebase used to run validator and full nodes that power NHBCoin—a purpose-built payment rail engineered for instant settlement, mainstream usability, and institutional-grade compliance.

```
███╗   ██╗██╗  ██╗██████╗   ██████╗ ██████╗ ██╗███╗   ██╗
████╗  ██║██║  ██║██╔══██╗ ██╔════╝██╔═══██╗██║████╗  ██║
██╔██╗ ██║███████║██████╔╝ ██║     ██║   ██║██║██╔██╗ ██║
██║╚██╗██║██╔══██║██╔══██╗ ██║     ██╚═══██╗██║██║╚██╗██║
██║ ╚████║██║  ██║██████╔╝ ╚██████╗ ╚██████╔╝██║██║ ╚████║
╚═╝  ╚═══╝╚═╝  ╚═╝╚═════╝   ╚═════╝  ╚═════╝ ╚═╝╚═╝  ╚═══╝

```

NHBCoin abstracts away the traditional complexities of crypto networks. Native account abstraction, on-chain identity, fee sponsorship, and loyalty incentives are all protocol features—not afterthoughts—so that the experience of paying with NHB is as intuitive as the best consumer FinTech products.

---

## Contents

- [Why NHBCoin Matters](#why-nhbcoin-matters)
- [Protocol Pillars](#protocol-pillars)
- [Architecture Overview](#architecture-overview)
- [Token Economics](#token-economics)
- [Quick Start for Node Operators](#quick-start-for-node-operators)
  - [Prerequisites](#prerequisites)
  - [Clone and Build](#clone-and-build)
  - [Initial Configuration](#initial-configuration)
  - [Starting a Node](#starting-a-node)
- [Joining the Network as a Peer or Validator](#joining-the-network-as-a-peer-or-validator)
- [Command-Line Interface](#command-line-interface)
- [APIs, SDKs, and Documentation](#apis-sdks-and-documentation)
- [Security, Compliance, and Operations](#security-compliance-and-operations)
  - [Audit & Operations Reference Library](#audit--operations-reference-library)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [Legal Notice & License](#legal-notice--license)

---

## 🌌 Introduction

NHBCoin is a high-performance Layer 1 blockchain engineered specifically for Enterprise-Grade stablecoin operations and algorithmic logistics. It separates the highly volatile nature of network consensus from the stable requirements of real-world commerce by utilizing a dual-token architecture.

## 🤖 The Autonomous Economic Engine

To guarantee mathematical sustainability and zero human intervention, NHBCoin is governed by algorithmic, system-controlled treasuries. These entities are hardcoded into the genesis block to automatically manage the network's monetary policy without centralized ownership:

### 1. The Protocol Fee Collector (System Wallet `nhb1...`)
- **Function:** Every transaction on the network (transfers, atomic swaps, and escrow contracts) incurs a micro-fractional Merchant Discount Rate (MDR) fee.
- **Automation:** The core `native/fees` module autonomously sweeps these fractional pennies and routes them directly to a static, system-controlled Protocol Fee Collector.
- **Transparency:** This eliminates hidden gas inflation. The network organically funds its own underlying operations purely through real-world utility and transaction volume, meaning the stablecoin supply remains mathematically solvent.

### 2. The Fixed-Supply ZNHB Treasury (System Wallet `znhb1...`)
- **Function:** `ZNHB` is the loyalty, utility, and network-incentive asset. The founder economic model uses a fixed genesis supply that is preallocated to treasury-controlled wallets.
- **Automation:** Protocol base rewards, merchant loyalty campaigns, and validator/POTSO rewards are paid from funded treasuries and paymasters, not from ongoing inflationary minting.
- **Transparency:** This keeps the circulating and treasury balances auditable on-chain and preserves the scarcity story for `ZNHB` holders.

## Why NHBCoin Matters

NHBCoin is designed to be the next-generation money movement network—faster, safer, and more aligned with real-world commerce than generalized smart-contract chains.

- **Developers** gain a full-stack payment substrate with built-in account abstraction, identity, and escrow primitives that are programmable via familiar Go and Solidity tooling.
- **Regulators and auditors** benefit from deterministic on-chain identity records, policy-aware RPC authentication, and transparent consensus metrics for validating operational integrity.
- **Investors and businesses** access a zero-fee settlement rail coupled with a network-wide loyalty economy that compounds adoption and utility.
- **End users** experience instant payments, human-readable usernames, and sponsored fees so that sending NHB feels like using modern digital wallets.

## Protocol Pillars

NHBCoin L1 differentiates itself through protocol-native capabilities that directly support retail and enterprise payment flows:

- ⚡ **Proof of Time Spent Online (POTSO)** — A Byzantine fault tolerant consensus that weights block production by both staked ZapNHB and an on-chain Engagement Score, rewarding validators that actively maintain network health.
- 🧾 **Native Account Abstraction (NAA)** — Every account is a contract account; Paymasters can sponsor gas, enabling truly fee-less experiences for retail users.
- 🏦 **Dual-Token Model** — NHBCoin (stable settlement currency) and ZapNHB (staking & loyalty asset) are managed directly by the protocol for predictable monetary policy.
- 🤝 **Embedded P2P Escrow** — Trust-minimized escrow flows enable marketplaces without bespoke contract engineering.
- 🆔 **On-Chain Identity** — Human-readable usernames, verified emails, and avatars are part of the base chain, reducing user error and enabling compliant discovery flows.
- ♻️ **EVM Compatibility** — A bundled Go-Ethereum (Geth) engine lets developers deploy Solidity smart contracts and reuse the broader Ethereum tooling ecosystem.

## Architecture Overview

The L1 is organized into modular layers that together deliver the payment network:

1. **Consensus Layer** — Implements POTSO BFT consensus, validator set management, and engagement scoring.
2. **Execution Layer** — Combines optimized Go modules for native payments/escrow with the embedded Geth EVM for smart contracts.
3. **Application Layer** — Ships identity, escrow, loyalty, and other financial primitives as first-class modules.
4. **Networking Layer** — Provides peer discovery, gossip, and fast state synchronization for geographically distributed nodes.

## Token Economics

- **NHBCoin (NHB)** — Stable, dollar-pegged medium of exchange for all payments and settlements ($1 = 1 NHB). This is pure value transfer and is **never** minted as a reward.
- **ZapNHB (ZNHB)** — The fixed-cap governance and utility asset. It secures the network, powers protocol and merchant loyalty rewards, and governs validator elections.
- **Dual-Purpose Staking** — Staking ZNHB serves two simultaneous functions:
  1. **Governance:** Every 1 ZNHB staked equals 1 governing vote for network parameters and protocol upgrades.
  2. **Validation:** If the stake equals or exceeds 10,000 ZNHB, the delegator becomes a **validator candidate**. The node joins the active validator set at the next epoch only after it is online, synced, and submitting validator heartbeats. You do **not** need a separate stake for governance.

## 🚀 Quick Start for Node Operators (Step-by-Step)

We have intentionally designed this process so that **anyone**, regardless of Linux experience, can spin up a node in under 5 minutes. 

### Step 1: Get a Cloud Server (AWS, DigitalOcean, etc.)
You need a server that runs 24/7. Rent a basic VPS (Virtual Private Server).
- **Recommended OS:** Ubuntu 22.04 LTS
- **Recommended Size:** 2 vCPUs, 4GB RAM (e.g., AWS `t3.medium` or a $10 DigitalOcean droplet).
- **Crucial Security Step:** In your server provider's firewall settings (AWS Security Groups), you must open the following ports to the public (`0.0.0.0/0`):
  - `22` (TCP) - For your SSH access
  - `6001` (TCP/UDP) - For the blockchain P2P network
  - `8545` (TCP) - (Optional) If you want to accept RPC requests like MetaMask

### Step 2: Connect to your Server
Open your computer's terminal (or Command Prompt) and connect via SSH using the IP address your provider gave you:
```bash
ssh ubuntu@YOUR_SERVER_IP
```

### Step 3: Prepare the Server
Once you are logged in, copy and paste this exact block of text and press Enter. This updates the server and installs the necessary Go programming language:
```bash
sudo apt update && sudo apt install git build-essential -y
wget https://go.dev/dl/go1.23.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.23.0.linux-amd64.tar.gz
echo 'export PATH="/usr/local/go/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```

### Step 4: Clone the Code
Now, download the NHBCoin blockchain engine to your server:
```bash
git clone https://github.com/josephblackelite/nhbchain.git
cd nhbchain
```

### Step 5: Run the Automated Node Bootstrap
The repository now includes a single node bootstrap script intended to be the public operator entrypoint. It installs the runtime stack, builds the binaries, installs the service units, and brings the node online once your server-side config is ready. Run:
```bash
chmod +x scripts/run_nhbcoin_node.sh
bash scripts/run_nhbcoin_node.sh
```

If you are launching a fresh genesis/reset deployment, use:

```bash
bash scripts/run_nhbcoin_node.sh --reset-state
```

**That brings the NHBCoin node online as a peer/full node** once the required config files and secrets have been placed on the server. After startup, check the running services with `sudo systemctl status nhb.service` and watch the node logs with `journalctl -u nhb.service -f`.

### Step 6: Delegate your 10,000 ZNHB (Stake)
The validator bootstrap now uses the private key from your NHBCoin wallet
directly, so the server and the validator wallet address are the same account.

1. Create or log into your wallet at `nhbcoin.com`.
2. Make sure that wallet has at least `10,000 ZNHB` staked or ready to stake.
3. In the wallet app, go to **Settings** and reveal your private key.
4. On your Ubuntu validator server, run:

```bash
chmod +x scripts/validator-only-bootstrap.sh
bash scripts/validator-only-bootstrap.sh --validator-key YOUR_WALLET_PRIVATE_KEY_HEX --reset-state
```

Once the server is online, synced, and emitting validator heartbeats, the
staked wallet becomes a validator candidate and then joins the active set at
the next epoch boundary.

---

### Windows Desktop Node (Local Development)
The entire NHBCoin blockchain engine is written in cross-platform Go. If you are developing locally on a Windows PC, we strongly recommend using **WSL (Windows Subsystem for Linux)**.

1. Open PowerShell and run: `wsl --install`
2. Restart your computer and open the **Ubuntu** app from your Start Menu.
3. You now have a native Linux terminal inside Windows. Follow Step 3 through Step 5 from the guide above exactly as if you were on a cloud server!

---

## 🔗 Network Connection Details

If you are setting up a frontend application, a Web3 wallet (like MetaMask), or configuring your Node manually, here are the official Mainnet parameters:

- **Network Name:** NHBCoin Mainnet
- **Network ID:** `14699254016670310680` *(This dynamic network identifier is mathematically enforced by the genesis state to prevent cross-network replay and handshake confusion.)*
- **Transaction Signing Chain ID:** `0x4e4842` *(ASCII `NHB`; this is the value wallet and SDK transaction payloads must sign against when using `nhb_sendTransaction`.)*
- **Public RPC Endpoint:** `https://api.nhbcoin.com`
- **Currency Symbol:** `NHB`
- **Mainnet P2P Bootnode (enode):** 
  `"enode://9606e2dd587cef5c8c46c6d41d03faf365edcb2f394921099e2b812261010841@52.1.96.250:6001"`

### Join As A Validator In One Command

On a fresh Ubuntu server, clone the repo and run:

```bash
chmod +x scripts/validator-only-bootstrap.sh
bash scripts/validator-only-bootstrap.sh --validator-key YOUR_WALLET_PRIVATE_KEY_HEX --reset-state
```

What this does:

- writes `/etc/nhbchain/node.env` with your validator key
- installs `nhb.service`
- builds the validator binaries
- points the node at the NHBCoin mainnet bootnode
- syncs block history from the network until it reaches the current head
- starts the validator and keeps validator heartbeats flowing automatically

Operational model:

- staking `>= 10,000 ZNHB` makes the wallet a **validator candidate**
- the server becomes **active next epoch**, not instantly
- readiness requires the node to be online, synced, and heartbeat-ready
- offline validators are removed from quorum automatically at epoch rollover instead of freezing the network

Compatibility note:

- `scripts/deployvalidator.sh` remains available as a backwards-compatible alias
  to the same validator-only bootstrap flow.

---

### Do I need 10,000 ZNHB to use the network?
**No. Ordinary users, businesses, and traders do NOT need 10,000 ZNHB.**
Anyone can connect a wallet to the network, send funds, vote in governance, or use smart contracts with absolutely **zero** minimum balances. The 10,000 ZNHB requirement applies *strictly to Server Operators (Validators)*.

### What is the benefit of running a Validator Server?
Validators earn rewards through the **POTSO (Proof of True Staking and Operation)** consensus mechanism. 
Every epoch (approx. 120 blocks), the network distributes fees and newly minted ZapNHB (ZNHB) to active validators. 
Unlike purely wealth-based systems, POTSO heavily weights your **Engagement Score**. Validators that process more transactions, handle escrow events, and maintain perfect uptime earn significantly higher yields than passive, wealthy nodes.

## Command-Line Interface

`nhb-cli` streamlines wallet management and operational tasks:

```bash
./nhb-cli generate-key              # Creates a new NHB wallet (saves to wallet.key; required before other commands)
./nhb-cli balance nhb1...            # Queries balances and staking state
./nhb-cli send <to> <amount> wallet.key
./nhb-cli deploy <contract.hex> wallet.key
./nhb-cli id register <alias> wallet.key
```

For the full identity management toolkit, refer to [`docs/identity-cli.md`](./docs/identity-cli.md). Always store `wallet.key` and RPC tokens securely; never commit secrets to source control—`wallet.key` is now ignored by Git to prevent accidental publication.

## APIs, SDKs, and Documentation

All protocol modules ship with reference documentation under [`docs/`](./docs):

- **Identity & Username Directory** — Concepts, RPC specs, and gateway flows (`docs/identity.md`, `docs/identity-api.md`, `docs/identity-gateway.md`).
- **Escrow Module** — Settlement lifecycle and developer guide (`docs/escrow.md`, `docs/escrow/nhbchain-escrow-gateway.md`).
- **Loyalty & Rewards** — Network-wide loyalty engine overview (`docs/loyalty.md`).
- **Pay-by-Username** — UX flows and examples (`docs/pay-by-username.md`, `docs/examples/identity`).
- **OpenAPI Specification** — Machine-readable schema for REST integrations (`docs/openapi/identity.yaml`).

> Additional SDKs and tooling (TypeScript, Rust) are in development. Subscribe to governance releases for updates.

## Security, Compliance, and Operations

- **Authentication** — RPC bearer tokens protect privileged calls; rotate secrets regularly and enforce mutual TLS or HMAC as described in the [Network Hardening Playbook](docs/security/network-hardening.md).
- **Key Management** — Validator keys default to encrypted Ethereum-compatible keystores protected by a non-empty passphrase (`NHB_VALIDATOR_PASS` or interactive prompt). Integrate with external KMS via `ValidatorKMSURI` and `ValidatorKMSEnv`. Wallet operators **must** generate fresh CLI keys locally (`./nhb-cli generate-key`)—any environment that previously relied on the repository placeholder must rotate by deleting the old file, minting a new key, and migrating funds/allowances to the new address immediately.
- **Observability** — Monitor validator uptime, engagement scores, and staking state using CLI commands or forthcoming telemetry dashboards. Forward RPC/WAF logs to your SIEM so abuse attempts can be correlated with P2P events.
- **Compliance Alignment** — Native identity modules provide audit trails, verified contact points, and consent-driven discovery suitable for regulatory review.
- **Audits & Bug Bounty** — We run an ongoing [bug bounty program](docs/security/bug-bounty.md) and maintain an [audit readiness guide](docs/security/audit-readiness.md) with frozen commits, reproducible builds, and fixtures for third-party assessors.

### Audit & Operations Reference Library

- **Audit phases:** [Overview](docs/audit/overview.md), [Static analysis](docs/audit/static-analysis.md), [Fuzzing](docs/audit/fuzzing.md), [End-to-end flows](docs/audit/e2e-flows.md), [Documentation quality](docs/audit/docs-quality.md), and [Reconnaissance](docs/audit/recon.md).
- **Consensus:** [BFT height sync](docs/consensus/bft-height-sync.md), [Consensus invariants](docs/consensus/invariants.md).
- **Performance:** [Baselines](docs/perf/baselines.md), [Tuning guide](docs/perf/tuning.md).
- **Security:** [Network security](docs/security/networking.md), [Supply chain security](docs/security/supply-chain.md).
- **Operations:** [Configuration guardrails](docs/ops/configuration.md), [Snapshot operations](docs/ops/snapshots.md), [Validator runbook](docs/ops/validator-runbook.md).

### Operational Audit Harness

Run the bundled audit harness before scheduled compliance reviews or release sign-offs. It executes the repository's critical `make` targets, captures logs, and writes timestamped reports under `audit/`.

```bash
./scripts/audit.sh
```

The script prepares `logs/` and `artifacts/` directories, then emits Markdown and JSON summaries (for example, `audit/report-<timestamp>.md` and `audit/report-<timestamp>.json`) that can be attached to change-management tickets.

### Static Analysis & Security Checks

For day-to-day development, run the static analysis bundle before opening a pull request:

```bash
make audit:static
```

The target enables `set -o pipefail` so any failing tool stops the sequence and bubbles a non-zero exit code back to the orchestrator. Each tool's console output is tee'd into `logs/` for later review:

| Tool | Log file | How to interpret |
| --- | --- | --- |
| `go mod tidy` | `logs/go-mod-tidy.log` | Confirms module metadata is canonical. Non-empty output typically means dependencies were added/removed and should be committed. |
| `golangci-lint run ./...` | `logs/golangci-lint.log` | Surfaces lint violations from `govet`, `errcheck`, `staticcheck`, `ineffassign`, `gosec`, `revive`, `misspell`, `unparam`, `gocyclo` (excluding `_test.go`), and `prealloc`. Address findings in source or add justified suppressions. |
| `govulncheck ./...` | `logs/govulncheck.log` | Flags known vulnerabilities in Go dependencies. Either upgrade the dependency or document why the finding is a false positive. |
| `staticcheck ./...` | `logs/staticcheck.log` | Provides additional static diagnostics beyond `golangci-lint`. Review reported code smells or correctness issues. |
| `buf lint` | `logs/buf-lint.log` | Ensures protobuf APIs conform to style and best practices. Resolve comments or naming issues before merging. |
| `buf breaking --against ".git#branch=main"` | `logs/buf-breaking.log` | Detects backward-incompatible protobuf changes relative to `main`. Update the API consciously or coordinate a versioned release if a breaking change is intentional. |

Archive the `logs/` directory with release artifacts so compliance reviewers can validate that the checks passed for a given build.

### Reporting Vulnerabilities

1. Encrypt your findings with the [repository PGP key](docs/security/repository-pgp-key.asc) (fingerprint `8F2D 3C71 9A0B 4D52 8EFA 9C1B 6D74 C5A2 1D3F 8B9E`).
2. Email the encrypted report to `security@nehborly.net` or use the [security issue template](.github/ISSUE_TEMPLATE/security.yml) to create a private triage ticket.
3. For time-sensitive issues, escalate via Signal at `+13234559568` after sending your report.

Full policy details, SLAs, and embargo expectations live in [`docs/security/disclosure.md`](docs/security/disclosure.md). A machine-readable summary is published at [`.well-known/security.txt`](.well-known/security.txt) for automated scanners.

## Roadmap

- **Security Hardening** — Exhaustive internal testing plus third-party audits.
- **Frontend & Wallet** — Launch of nhbcoin.com consumer and merchant experiences with embedded Paymaster support.
- **Testnet Expansion** — Onboarding community validators and ecosystem partners.
- **Mainnet Launch** — Final production release with full loyalty activation.

## Contributing

We welcome community collaboration:

1. Open an issue to report bugs or propose enhancements.
2. Fork the repository and submit pull requests.
3. Join forthcoming community channels to participate in technical governance and product feedback.

## Legal Notice & License

© 2025 NHBCoin.com. All rights reserved. NHBCoin, NHBCoin L1, ZapNHB, and Proof of Time Spent Online (POTSO) are proprietary innovations owned exclusively by NHBCoin. No portion of the POTSO consensus design, related trademarks, or brand assets may be copied, replicated, or distributed without written authorization from NHBCoin.

This codebase is released under the MIT License:

```
MIT License

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

By running or contributing to this project you acknowledge the above ownership terms and agree to respect NHBCoin’s intellectual property and brand guidelines.
