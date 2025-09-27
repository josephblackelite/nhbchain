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
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [Legal Notice & License](#legal-notice--license)

---

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

- **NHBCoin (NHB)** — Stable, dollar-pegged medium of exchange for all payments and settlements.
- **ZapNHB** — Earned through usage and staking; secures the network, unlocks loyalty rewards, and governs validator elections.
- **Staking** — Validators must bond ZapNHB, maintain uptime, and submit heartbeat transactions to maximize their Engagement Score under POTSO.

## Quick Start for Node Operators

### Prerequisites

- **Operating System** — Ubuntu 22.04 LTS (recommended) or any modern Linux distribution.
- **Go Toolchain** — Version 1.22.6.
- **Git** — For cloning the repository.

Update your server and install dependencies:

```bash
sudo apt update
sudo apt install git build-essential tmux -y
wget https://go.dev/dl/go1.22.6.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.22.6.linux-amd64.tar.gz
echo 'export PATH="/usr/local/go/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```

> The helper scripts in [`scripts/`](./scripts) default to Go 1.22.6 and set `GOFLAGS=-buildvcs=false`. Override `GO_VERSION`, `GO_CMD`, or `GOFLAGS` if you manage the toolchain manually.

### Clone and Build

```bash
git clone https://github.com/josephblackelite/nhbchain.git
cd nhbchain
go mod tidy
go build -o nhb-node ./cmd/nhb/
go build -o nhb-cli ./cmd/nhb-cli/
```

This produces two executables:

- `nhb-node` — the full node / validator client.
- `nhb-cli` — the command-line utility for wallet, staking, and maintenance operations.

### Initial Configuration

On first launch the node creates `config.toml` alongside an encrypted validator keystore. To pre-configure or inspect settings, edit `config.toml` and point `GenesisFile` at the vetted genesis JSON supplied by network operations (autogenesis is only for isolated dev workflows):

```toml
ListenAddress = "0.0.0.0:6001"
RPCAddress    = "0.0.0.0:8080"
DataDir       = "./nhb-data"
GenesisFile   = "./config/genesis.json" # required: must match the network's published hash
AllowAutogenesis = false                 # dev override; never enable on shared networks
ValidatorKeystorePath = ""
NetworkName = "nhb-local"
Bootnodes = [
  # e.g. "34.67.8.77:6001"
]
PersistentPeers = [
  # validators you expect to stay connected to long-term
]
```

Set required secrets as environment variables before bootstrapping:

```bash
export NHB_VALIDATOR_PASS="choose-a-strong-passphrase"
export NHB_RPC_TOKEN="choose-a-strong-shared-secret"
```

If migrating from legacy plaintext keys, convert them with:

```bash
go run ./cmd/nhbctl migrate-keystore --config ./config.toml
```

### Starting a Node

Run the node inside a persistent `tmux` session to maintain uptime:

```bash
tmux new -s nhb-node
./nhb-node
```

Detach with `Ctrl+B`, `D` and reattach via `tmux attach -t nhb-node`. Logs will confirm chain synchronization and peer connectivity.

## Joining the Network as a Peer or Validator

1. **Discover Peers** — Populate `Bootnodes`/`PersistentPeers` with known validator endpoints or connect to NHBCoin’s bootstrap nodes published via governance notices.
2. **Sync the Chain** — Allow `nhb-node` to download and verify state. Monitor progress via RPC (`nhb_getLatestBlocks`).
3. **Generate Wallet Keys** — Use `./nhb-cli generate-key` to create a wallet; secure `wallet.key` offline.
4. **Acquire ZapNHB** — Request testnet allocations or participate in mainnet offerings to stake.
5. **Stake to Validate** — Bond at least the active `staking.minimumValidatorStake` governance parameter (defaults to 1,000 ZapNHB when unset). Confirm the live threshold before staking:
   ```bash
   ./nhb-cli gov list --limit 50 | jq -r '
     [.proposals[]
      | select(.target=="param.update")
      | {id, change: (try (.proposed_change | fromjson) catch empty)}
      | select(.change."staking.minimumValidatorStake" != null)]
     | sort_by(.id)
     | last
     | .change."staking.minimumValidatorStake"'
   ```
   Once you know the minimum, stake an amount that meets or exceeds it:
   ```bash
   ./nhb-cli stake <amount> wallet.key
   ```
6. **Maintain Engagement** — Submit periodic heartbeat transactions to maximize POTSO weight:
   ```bash
   ./nhb-cli heartbeat wallet.key
   ```
7. **Unstake When Needed** — Withdraw bonded ZapNHB while respecting unbonding schedules:
   ```bash
   ./nhb-cli un-stake 1000 wallet.key
   ```

Non-validating peers may omit staking but should still configure RPC authentication to protect privileged endpoints. Read-only integrations are limited to allow-listed methods (`nhb_getBalance`, `nhb_getLatestBlocks`, `nhb_getLatestTransactions`) unless presenting the bearer token.

## Command-Line Interface

`nhb-cli` streamlines wallet management and operational tasks:

```bash
./nhb-cli generate-key              # Creates a new NHB wallet (saves to wallet.key)
./nhb-cli balance nhb1...            # Queries balances and staking state
./nhb-cli send <to> <amount> wallet.key
./nhb-cli deploy <contract.hex> wallet.key
./nhb-cli id register <alias> wallet.key
```

For the full identity management toolkit, refer to [`docs/identity-cli.md`](./docs/identity-cli.md). Always store `wallet.key` and RPC tokens securely; never commit secrets to source control.

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
- **Key Management** — Validator keys default to encrypted Ethereum-compatible keystores. Integrate with external KMS via `ValidatorKMSURI` and `ValidatorKMSEnv`.
- **Observability** — Monitor validator uptime, engagement scores, and staking state using CLI commands or forthcoming telemetry dashboards. Forward RPC/WAF logs to your SIEM so abuse attempts can be correlated with P2P events.
- **Compliance Alignment** — Native identity modules provide audit trails, verified contact points, and consent-driven discovery suitable for regulatory review.
- **Audits & Bug Bounty** — We run an ongoing [bug bounty program](docs/security/bug-bounty.md) and maintain an [audit readiness guide](docs/security/audit-readiness.md) with frozen commits, reproducible builds, and fixtures for third-party assessors.

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
