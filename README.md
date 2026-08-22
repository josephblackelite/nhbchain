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
  - [Full tokenomics reference](docs/tokenomics/tokenomics.md)
- [Quick Start for Node Operators](#-quick-start-for-node-operators-step-by-step)
  - [Step 1: Get a Cloud Server](#step-1-get-a-cloud-server-aws-digitalocean-etc)
  - [Step 2: Connect to your Server](#step-2-connect-to-your-server)
  - [Step 3: Prepare the Server](#step-3-prepare-the-server)
  - [Step 4: Clone the Code](#step-4-clone-the-code)
  - [Step 5: Run the Automated Node Bootstrap](#step-5-run-the-automated-node-bootstrap)
  - [Step 6: Get Paid](#step-6-get-paid-delegate-10000-znhb-or-more)
- [Network Connection Details](#-network-connection-details)
  - [Join As A Validator In One Command](#join-as-a-validator-in-one-command)
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
- **Function:** `ZNHB` has a hard genesis supply of exactly 1,000,008,000 tokens (the 8,000 above the originally-intended 1,000,000,000 is a documented reconciliation remainder from pre-existing, since-fixed mint-path bugs — see `docs/tokenomics/tokenomics.md`), split once at genesis into an 800,008,000-ZNHB **Sale Pool** and a 200,000,000-ZNHB **Reward Pool**. Nothing is ever minted to satisfy a purchase or a reward — both pools only ever move ZNHB that already exists.
- **Automation:** Sale Pool purchases are priced entirely on-chain by the Genesis Treasury Distribution Curve — a 16,000-tranche bonding curve computed in exact rational arithmetic, immune to order-splitting. Merchant loyalty rewards are paid from business-funded paymasters, unrelated to either pool. Validator/staking rewards draw from the Reward Pool via a live Bitcoin-style halving schedule that provably converges to less than the pool's own size (`core/rewards_logic.go`). A treasury **buyback engine** (`core/tokenomics/buyback`) funds itself from a share of NHB fee revenue, repurchases ZNHB from willing sellers each epoch at a price no more favorable than an independently signed reference price, and recycles what it buys straight back into the Sale Pool — never burned, never re-minted.
- **Transparency:** A block-level invariant (`CheckZNHBSupplyInvariant`) asserts `Sale Pool + Reward Pool == treasury wallet's live ZNHB balance` every block, and two public RPCs (`znhb_getTokenomicsState`, `znhb_quoteBuy`) expose the curve's live position and pool balances to anyone. Full detail: [`docs/tokenomics/tokenomics.md`](docs/tokenomics/tokenomics.md).

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
- 🔁 **Native Subscriptions** — Recurring billing built directly into the L1, not bolted on. Where Stripe requires creating a Product and a Price before anything can be charged, an NHBCoin `Plan` is one object — set a price, an asset, and a billing interval, and it's live. A subscriber signs exactly one transaction; that signature is their entire standing mandate, and the chain itself debits them every cycle from then on — no server-side card vault, no webhook race against a payment processor, no "off-session charge" API to get wrong. NHBCoin takes a small management fee per charge (currently capped at 5%, a fraction of typical card-network take rates) — the only cost beyond the usual transfer fee. Full reference: **[`docs/subscriptions/README.md`](docs/subscriptions/README.md)**.
- 🆔 **On-Chain Identity** — Human-readable usernames, verified emails, and avatars are part of the base chain, reducing user error and enabling compliant discovery flows.
- ♻️ **EVM Compatibility** — A bundled Go-Ethereum (Geth) engine lets developers deploy Solidity smart contracts and reuse the broader Ethereum tooling ecosystem.

## Architecture Overview

The L1 is organized into modular layers that together deliver the payment network:

1. **Consensus Layer** — Implements POTSO BFT consensus, validator set management, and engagement scoring.
2. **Execution Layer** — Combines optimized Go modules for native payments/escrow with the embedded Geth EVM for smart contracts.
3. **Application Layer** — Ships identity, escrow, loyalty, and other financial primitives as first-class modules.
4. **Networking Layer** — Provides peer discovery, gossip, and fast state synchronization for geographically distributed nodes.

## Token Economics

- **NHBCoin (NHB)** — Stable, dollar-pegged medium of exchange for all payments and settlements ($1 = 1 NHB). Mint-on-deposit, burn-on-redemption, no fixed supply cap. This is pure value transfer and is **never** minted as a reward.
- **ZapNHB (ZNHB)** — A hard-capped, genesis-fixed **1,000,008,000 ZNHB** total supply, no more, ever. Secures the network, powers protocol and merchant loyalty rewards, and governs validator elections. ZNHB carries **no protocol-defined valuation ceiling or promise** — see below.
- **Dual-Purpose Staking** — Staking ZNHB serves two simultaneous functions:
  1. **Governance:** Voting power in NHBChain governance is POTSO-weighted (network participation), not raw staked balance — see the nhbportal wallet's **Governance → How governance works** tab for the full, accurate breakdown of what's votable today.
  2. **Validation:** If the stake equals or exceeds 10,000 ZNHB, the delegator becomes a **validator candidate**. The node joins the active validator set at the next epoch only after it is online, synced, and submitting validator heartbeats. You do **not** need a separate stake for governance.

### How ZNHB enters circulation

ZNHB is never minted on demand. All 1,000,008,000 tokens exist at genesis, split once into an 800,008,000-ZNHB **Sale Pool** and a 200,000,000-ZNHB **Reward Pool** — a block-level invariant enforces that the two pools always sum to exactly what the treasury wallet holds.

- **Sale Pool (live):** priced by the **Genesis Treasury Distribution Curve** — 16,000 tranches of 50,000 ZNHB each, starting at $0.05 and rising to a $1.00 terminal price for the treasury's own last unit of inventory (not a market cap or ceiling on ZNHB itself). Every purchase, whether a direct buy or a swap-voucher mint, is priced on-chain in exact rational arithmetic and is immune to order-splitting. Query the live price and pool balances with `znhb_getTokenomicsState`; get an exact purchase quote with `znhb_quoteBuy`.
- **Reward Pool (live):** funds validator/staking rewards via a Bitcoin-style halving schedule (200 ZNHB/epoch, halving every 500,000 epochs) that mathematically converges to less than the pool's own size. Every epoch's payout is clamped to the pool's live balance and debits it by the exact amount paid — the pool can never go negative regardless of any formula edge case.
- **Treasury buyback (live mechanism):** a share of NHB transaction-fee revenue (20% by default) automatically funds a per-epoch buyback that repurchases ZNHB from willing sellers at a price capped below both the curve's own spot price and an independently signed reference price, then recycles what it buys straight back into the Sale Pool. Activates automatically once a network's genesis configures a reference-price signer quorum; the signer quorum itself is permanently outside governance's reach, by design.

Full detail, including the exact pricing formula, buyback mechanics, and RPC response shapes: **[`docs/tokenomics/tokenomics.md`](docs/tokenomics/tokenomics.md)**.

## 🚀 Quick Start for Node Operators (Step-by-Step)

> **If you're setting up a validator, skip straight to ["Join As A Validator In
> One Command"](#join-as-a-validator-in-one-command) below** — it's the
> current, actually-one-command path: clone the repo, run one script, done.
> It installs its own prerequisites (including Go) automatically. The manual
> walkthrough below is for standing up a full node for other reasons and
> assumes more hands-on setup, including config files under `/etc/nhbchain/`
> this walkthrough does not generate for you.

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

> **Before you run this:** `scripts/run_nhbcoin_node.sh` execs `scripts/bringup_production_stack.sh`, which requires the following files to already exist under `/etc/nhbchain/` on the server — it fails with no guidance if any are missing:
> - `config.toml`
> - `node.env`
> - `payments-gateway.env`
> - `payoutd.env`
> - `payoutd.yaml`
> - `ops-reporting.env`
>
> Use the templates under [`deploy/env/`](./deploy/env) (`*.env.example`, `payoutd.config.example.yaml`) as your starting point for each file; see [`docs/deploy/one-shot-deploy.md`](./docs/deploy/one-shot-deploy.md) for the full required-config reference.
>
> **Only want to run a validator, not the full production stack?** You don't need any of these files — skip straight to [Join As A Validator In One Command](#join-as-a-validator-in-one-command) instead.

```bash
bash scripts/run_nhbcoin_node.sh
```

If you are launching a fresh genesis/reset deployment, use:

```bash
bash scripts/run_nhbcoin_node.sh --reset-state
```

**That brings the NHBCoin node online as a peer/full node** once the required config files and secrets have been placed on the server. After startup, check the running services with `sudo systemctl status nhb.service` and watch the node logs with `journalctl -u nhb.service -f`.

### Step 6: Get paid (delegate 10,000 ZNHB, or more)
The validator's signing key and your everyday NHBCoin wallet are **two
different things by design**. The validator key is generated on the server
itself and never leaves it; you never export or paste a private key
anywhere for this step.

1. On your Ubuntu validator server, run:

```bash
bash scripts/validator-only-bootstrap.sh --beneficiary YOUR_NHB_WALLET_ADDRESS --reset-state
```

   This generates a fresh validator key **on this machine** the first time
   it runs (reused on later runs), prints that validator's node address, and
   automatically redirects its future consensus rewards to your
   `--beneficiary` wallet address (required), signed locally before the key
   is used for anything else. Add `--email you@example.com` to also get the
   node address and instructions emailed to you.

2. Create or log into your wallet at `nhbcoin.com`.
3. Go to **Validator Hub -> Delegate**, paste the node address the script
   printed, and delegate at least `10,000 ZNHB` from that wallet.

Once the server is online, synced, and emitting validator heartbeats, the
delegated stake becomes a validator candidate and then joins the active set
at the next epoch boundary. Your wallet earns the ordinary staking yield via
the delegation itself; `--beneficiary` additionally routes the separate
consensus-participation reward to the same wallet, so nothing accumulates at
an address you can't reach.

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
- **Network ID:** `430060579445266314` *(P2P handshake identifier for the live mainnet deployment, effective from the Phase E genesis relaunch on 2026-08-06 -- genesis-hash-derived, not a chosen number. Nodes advertising a different value will not be able to peer with mainnet validators. Verify against the live node's `net_info`/`p2p_info` RPC result if this ever looks stale. The prior GTM 2026 genesis (10698789873712925303) was abandoned after a real consensus bug was found: no second validator could ever sync it -- see config/genesis.phase-e.json and core/mint.go's MintChainID comment for the full story. Account balances were snapshotted forward into the new genesis, nobody lost funds.)*
- **Transaction Signing Chain ID:** `0x4e4842` *(ASCII `NHB`; this is the value wallet and SDK transaction payloads must sign against when using `nhb_sendTransaction`.)*
- **Public RPC Endpoint:** `https://api.nhbcoin.com`
- **Currency Symbol:** `NHB`
- **Mainnet P2P Bootnode:**
  `"52.1.96.250:6001"`
  *(This node's `Bootnodes`/`PersistentPeers` config values are plain `host:port` -- the P2P dialer connects directly with `net.Dial("tcp", addr)` and does not parse an `enode://nodeid@host:port` URI scheme, so don't use that format here even though it's a common convention on other chains. Updated 2026-08-06 for the Phase E genesis relaunch — this address changes on any future genesis relaunch too; verify against the live node's `p2p_info` RPC result if this ever looks stale.)*

### Join As A Validator In One Command

For the full step-by-step walkthrough, including troubleshooting, see
[Validator Onboarding Guide](docs/validators/onboarding.md).

On a fresh Ubuntu server, clone the repo and run:

```bash
bash scripts/validator-only-bootstrap.sh --beneficiary YOUR_NHB_WALLET_ADDRESS --reset-state
```

**`--reset-state` is only safe on a brand-new server with no existing chain
data.** It wipes this node's local block history before starting, which is
correct the very first time this runs on a fresh machine but destructive if
copy-pasted again later against a node that's already synced or already
validating -- drop the flag on any re-run.

Never pass a private key to this script -- it generates one for you, on
this machine, the first time it runs. `--beneficiary` is **required**: it
points this validator's consensus reward at a wallet you actually use (see
"Getting paid" below). Without it, that reward would accumulate at the
validator's own address, whose key intentionally never leaves this server --
the script refuses to proceed without a beneficiary specifically to avoid
that.

What this does:

- generates a fresh validator key on this machine (idempotent -- reused on
  later runs instead of replaced)
- writes `/etc/nhbchain/node.env` with that key
- installs `nhb.service`
- builds the validator binaries
- points the node at the NHBCoin mainnet bootnode
- syncs block history from the network until it reaches the current head
- starts the validator and keeps validator heartbeats flowing automatically
- signs a one-time transaction locally redirecting this validator's
  consensus reward to the `--beneficiary` address you provided
- prints the validator's node address and exactly what to do next

Getting paid:

- **Staking yield**: from any nhbcoin.com wallet, go to Validator Hub ->
  Delegate, paste the node address this script printed, delegate at least
  10,000 ZNHB. That wallet earns the yield directly.
- **Consensus reward**: a separate, smaller reward for actively
  participating in consensus. Defaults to the validator's own
  (server-only) address; `--beneficiary` redirects it to your wallet
  automatically. You can set or change this later without rerunning the
  whole script:
  `nhb-cli set-reward-beneficiary <your-wallet-address> /etc/nhbchain/validator.key`

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
Validators earn staking yield on delegated stake today. A separate, additional **POTSO (Proof of Time Spent Online)**-weighted reward pays out ZNHB from the fixed 200,000,000-ZNHB Reward Pool on a halving schedule (never newly minted) — see [Token Economics](#token-economics) for the current status.
Unlike purely wealth-based systems, POTSO heavily weights your **Engagement Score**, so validators that process more transactions, handle escrow events, and maintain perfect uptime earn significantly higher yields than passive, wealthy nodes.

## Command-Line Interface

`nhb-cli` streamlines wallet management and operational tasks:

```bash
./nhb-cli generate-key              # Creates a new NHB wallet (saves to wallet.key; required before other commands)
./nhb-cli balance nhb1...            # Queries balances and staking state
./nhb-cli send-nhb <to> <amount> wallet.key
./nhb-cli send-znhb <to> <amount> wallet.key   # Same syntax as send-nhb; sends ZNHB instead of NHB
./nhb-cli deploy <contract.hex> wallet.key
./nhb-cli id set-alias --addr <addr> --alias <alias>
```

For the full identity management toolkit, refer to [`docs/identity/identity-cli.md`](./docs/identity/identity-cli.md). Always store `wallet.key` and RPC tokens securely; never commit secrets to source control—`wallet.key` is now ignored by Git to prevent accidental publication.

## APIs, SDKs, and Documentation

All protocol modules ship with reference documentation under [`docs/`](./docs):

- **Identity & Username Directory** — Concepts, RPC specs, and gateway flows (`docs/identity/identity.md`, `docs/identity/identity-api.md`, `docs/identity/identity-gateway.md`).
- **Escrow Module** — Settlement lifecycle and developer guide (`docs/escrow/escrow.md`, `docs/escrow/nhbchain-escrow-gateway.md`).
- **Loyalty & Rewards** — Network-wide loyalty engine overview (`docs/loyalty/loyalty.md`).
- **Subscriptions** — Native recurring-billing engine: the standing-mandate model, fee structure, state layout, signed transactions, and full RPC/CLI reference (`docs/subscriptions/README.md`).
- **Tokenomics** — Genesis Treasury Distribution Curve, Sale/Reward Pool mechanics, and the RPC methods for reading them (`docs/tokenomics/tokenomics.md`).
- **Pay-by-Username** — UX flows and examples (`docs/identity/pay-by-username.md`, `docs/examples/identity`).
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
- **Operations:** [Configuration guardrails](docs/ops/configuration.md), [Snapshot operations](docs/ops/snapshots.md), [Validator runbook](docs/ops/validator-runbook.md), [Validator onboarding guide](docs/validators/onboarding.md).

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
2. Email the encrypted report to `security@nhbcoin.com` or use the [security issue template](.github/ISSUE_TEMPLATE/security.yml) to create a private triage ticket.
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
