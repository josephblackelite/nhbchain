# NHBChain

NHBChain is a Layer 1 blockchain node written in Go. This repository is the Go module `nhbchain` (`go 1.24.0`, `toolchain go1.24.3` in `go.mod`). It contains the node, the `nhb-cli` command-line client, the native on-chain modules, Go and TypeScript SDKs, deployment files and the developer documentation.

Every statement in this file is taken from the source code, the default configuration or a genesis file in this repository. Paths in parentheses point at where a value is defined.

## Contents

- [Components](#components)
- [Chain overview](#chain-overview)
  - [Accounts and keys](#accounts-and-keys)
  - [Native assets](#native-assets)
  - [Transactions](#transactions)
  - [Consensus and block production](#consensus-and-block-production)
  - [Fees](#fees)
  - [Native modules](#native-modules)
  - [Not supported](#not-supported)
- [Token supply and issuance](#token-supply-and-issuance)
- [Network parameters](#network-parameters)
- [Staking, validators and governance](#staking-validators-and-governance)
  - [Staking, delegation and unbonding](#staking-delegation-and-unbonding)
  - [Becoming a validator](#becoming-a-validator)
  - [Active validator set and epochs](#active-validator-set-and-epochs)
  - [Rewards](#rewards)
  - [Governance](#governance)
- [Build](#build)
- [Run a node](#run-a-node)
  - [Local development chain](#local-development-chain)
  - [Validator bootstrap on a server](#validator-bootstrap-on-a-server)
  - [Full node with the production script](#full-node-with-the-production-script)
  - [Configuration reference](#configuration-reference)
- [Command-line client](#command-line-client)
- [JSON-RPC API](#json-rpc-api)
- [Security and operations](#security-and-operations)
- [Repository layout](#repository-layout)
- [Documentation](#documentation)
- [Development checks](#development-checks)
- [Contributing and license](#contributing-and-license)

## Components

Binaries under `cmd/`:

| Path | What it is |
| --- | --- |
| `cmd/nhb` | The node. One process runs the BFT consensus engine, the P2P server and the JSON-RPC server. Flags: `--config` (default `./config.toml`), `--genesis`, `--allow-autogenesis` (development only), `--allow-migrate`. This is the binary installed by `deploy/systemd/nhb.service`. |
| `cmd/nhb-cli` | Command-line client: keys, balances, transfers, staking, validator registration, governance, escrow, subscriptions, loyalty. See [Command-line client](#command-line-client). |
| `cmd/consensusd` | Consensus daemon for the split deployment. Serves the gRPC `ConsensusService` and `QueryService` (flag `--grpc`, default `127.0.0.1:9090`) and connects to `p2pd` (flag `--p2p`, default `localhost:9091`). Round timeouts can be set with `--consensus-timeout-proposal`, `--consensus-timeout-prevote`, `--consensus-timeout-precommit` and `--consensus-timeout-commit`. |
| `cmd/p2pd` | P2P daemon for the split deployment. Serves the gRPC `NetworkService` (flag `--grpc`, default `127.0.0.1:9091`). |
| `cmd/gateway` | HTTP gateway configured with a YAML file (`-config`). Proxies URL prefixes such as `/v1/lending`, `/v1/gov`, `/v1/consensus` and `/v1/transactions` to backend services. |
| `cmd/nhbctl` | One command, `migrate-keystore`, which moves a legacy `ValidatorKey` value from a config file into an encrypted keystore. |
| `cmd/nhb-diag`, `cmd/nhb-recovery`, `cmd/nhb-supply-audit`, `cmd/swap-audit`, `cmd/english-audit` | Operator and audit tools. `nhb-diag` and `nhb-supply-audit` are read-only. `nhb-recovery` edits the persisted validator set of a stopped node. |

`docs/index.md` describes how the daemons and services connect.

## Chain overview

### Accounts and keys

- Keys are secp256k1. An address is 20 bytes, the last 20 bytes of the Keccak-256 hash of the public key (`crypto/keys.go`).
- Addresses are written in Bech32 with the prefix `nhb` or `znhb`. Both prefixes carry the same 20-byte payload; genesis parsing accepts either (`core/genesis/bech32.go`).
- `nhb-cli generate-key` writes a raw 32-byte private key to `wallet.key` in the current directory (mode `0600`). CLI commands that take a key file also accept an encrypted Ethereum V3 keystore; set `NHB_KEYSTORE_PASSPHRASE` to its passphrase (`cmd/nhb-cli/main.go`).
- `wallet.key`, `*.key` and `*.keystore` are listed in `.gitignore`.

### Native assets

Both assets are registered in the genesis file with 18 decimals. Amounts in transactions, RPC results and configuration are integers in the smallest unit ("wei", 10^-18 of a token).

| | NHB | ZNHB |
| --- | --- | --- |
| Genesis name | `NHBCoin` | `ZapNHB` |
| Decimals | 18 | 18 |
| How supply changes | Created by `TxTypeMint` (0x0E) vouchers whose signer holds the role `MINTER_NHB`. Destroyed by `TxTypeRedeemNHB` (0x1B); an attestation that marks a redemption failed credits the burned amount back. No protocol constant caps it; an optional per-year mint cap is read from the governance parameter `mint.nhb.maxEmissionPerYearWei` (unset means no cap). | Never minted: a mint voucher for ZNHB is rejected with `ErrMintZNHBNotMintable` (`core/mint.go`). ZNHB enters circulation only from a treasury balance that exists at genesis. See [Token supply and issuance](#token-supply-and-issuance). |
| Transfer transaction | `TxTypeTransfer` (0x01) | `TxTypeTransferZNHB` (0x10) |
| Used for staking, governance deposits and validator stake | no | yes |

`nhb_getTotalSupply` takes an optional token symbol (default `NHB`) and returns the incrementally tracked supply counter for it as `{"symbol":"NHB","totalSupplyWei":"..."}` (`rpc/explorer_handlers.go`). The explorer snapshot method `nhb_getExplorerSnapshot` reports `znhbCirculatingSupply` as the fixed string `"1000000000"` (`explorerZNHBFixedSupply`, `rpc/explorer_query_handlers.go`), which is a constant in that handler and not the genesis total stated under [Token supply and issuance](#token-supply-and-issuance).

### Transactions

- A transaction is signed with secp256k1. Its `chainId` must equal `0x4e4842` (5,130,306, the ASCII bytes `NHB`); `nhb_sendTransaction` rejects any other value (`core/types/transaction.go`, `rpc/http.go`).
- Fields accepted by `nhb_sendTransaction`: `chainId`, `type`, `nonce`, `to`, `value`, `data`, `gasLimit`, `gasPrice`, `paymaster`, `intentRef`, `intentExpiry`, `merchantAddr`, `deviceId`, `refundOf`, signature `r`/`s`/`v` and `paymasterR`/`paymasterS`/`paymasterV`. Numbers may be decimal strings, `0x` hex strings or JSON numbers. `to`, `data`, `paymaster` and `intentRef` are Go byte slices, so JSON encodes them as base64. `gasLimit` and `gasPrice` must be greater than zero.
- For signed transaction types the mempool requires `nonce` to equal the account nonce the node currently holds for the sender. A pending transaction with the same sender and nonce is replaced only by one with a higher `gasPrice × gasLimit`; otherwise it is rejected with "fee is not higher" (`core/node.go`).
- Transaction types (`core/types/transaction.go`):

| Group | Types |
| --- | --- |
| Value and identity | 0x01 Transfer, 0x10 TransferZNHB, 0x02 RegisterIdentity (claim a username), 0x08 Heartbeat |
| Staking | 0x06 Stake, 0x07 Unstake, 0x0D StakeClaim (release a matured unbonding), 0x34 StakeClaimRewards, 0x1A SetRewardBeneficiary |
| Governance | 0x27 GovPropose, 0x28 GovVote, 0x29 GovFinalize, 0x2A GovQueue, 0x2B GovExecute |
| Escrow | 0x03 CreateEscrow, 0x04 ReleaseEscrow, 0x05 RefundEscrow, 0x09 LockEscrow, 0x0A DisputeEscrow, 0x0B ArbitrateRelease, 0x0C ArbitrateRefund, 0x3B ExpireEscrow, 0x3C/0x3D/0x3E delegated release/refund/dispute, 0x41 DelegatedCreateEscrow, 0x3F/0x40 EscrowCreateRealm/UpdateRealm |
| Mint, swap and ZNHB treasury | 0x0E Mint, 0x0F SwapPayoutReceipt, 0x19 BuyZNHB, 0x1B RedeemNHB, 0x1C AttestRedemption, 0x1E SwapVoucherMint, 0x4A SwapVoucherReverse, 0x4B SwapMarkReconciled, 0x24 BuybackAsk, 0x25 BuybackRefPrice |
| Lending | 0x13 SupplyNHB, 0x14 WithdrawNHB, 0x15 DepositZNHB, 0x16 WithdrawZNHB, 0x17 BorrowNHB, 0x18 RepayNHB, 0x1D Liquidate, 0x26 LendingRefPrice, 0x2C CreatePool, 0x38 BorrowFixedTerm, 0x39 RepayFixedTerm, 0x3A SupplyFixedTerm |
| Point of sale | 0x20 POSAuthorize, 0x21 POSCapture, 0x22 POSVoid, 0x23 POSRegistry |
| POTSO | 0x2D PotsoStakeLock, 0x2E PotsoStakeUnbond, 0x2F PotsoStakeWithdraw, 0x4C SubmitEvidence |
| Subscriptions | 0x30 CreatePlan, 0x31 UpdatePlan, 0x32 Subscribe, 0x33 Cancel |
| Market | 0x35 CreateListing, 0x36 FillListing, 0x37 CancelListing |
| Loyalty | 0x42 CreateBusiness, 0x43 SetPaymaster, 0x44 AddMerchant, 0x45 RemoveMerchant, 0x46 CreateProgram, 0x47 UpdateProgram, 0x48 PauseProgram, 0x49 ResumeProgram |

Types 0x11 (SwapMint) and 0x12 (SwapBurn) are defined but always fail with "disabled" (`core/state_transition.go`). Types 0x0E, 0x1E, 0x25, 0x26, 0x4A, 0x4B and 0x4C carry no envelope signature; their payloads carry their own signatures (`RequiresSignature` in `core/types/transaction.go`).

### Consensus and block production

The engine is in `consensus/bft`. It runs Tendermint-style rounds with locking.

- **Rounds.** Each height runs propose, prevote, precommit and commit steps. Default timeouts are proposal 2 s, prevote 2 s, precommit 2 s and commit 4 s (`consensus/bft/bft.go`, `config/config.go`). They can be changed in the `[consensus]` section of the config; a zero or missing value keeps the default. The engine waits 30 seconds after start before it begins rounds. No block interval is configured: a height ends when its precommit quorum commits, or the round times out and a new round starts, and a proposer with an empty mempool proposes an empty block.
- **Quorum.** Voting power is the value stored for each address in the active validator set. A quorum is at least `ceil(2/3 × total voting power)` (implemented as `(2 × total + 2) / 3` with integer division) for both the prevote and the precommit step. With equal power that means 1 of 1, 2 of 2, 2 of 3 and 3 of 4 validators.
- **Signatures.** Proposals and votes are signed with secp256k1. Only votes for the active proposal's block hash are counted, and each validator counts once per step.
- **Safety.** A validator that sees a quorum of prevotes for a block locks on it for the rest of that height. It prevotes a different block only if the proposal carries signed prevotes proving that a quorum moved on. The lock is written to `<DataDir>/polc_lock.json` and restored after a restart.
- **Proposer.** The proposer for a round is chosen deterministically from `sha256(last commit hash || round)`. Each validator's weight is its account `Stake` plus a boost of up to 20% of that stake (`maxEngagementBoostBps = 2000`) scaled by its engagement score against the daily cap of 250. If the total weight is zero, proposers rotate round-robin.
- **Block contents.** At most `[global.Blocks].MaxTxs` transactions per block (default 500). 15% of a block (`DefaultPOSReservationBPS = 1500`) is reserved for point-of-sale transactions by default. The pending pool holds at most `[mempool].MaxTransactions` transactions (default 4000 in `config/config.go`; the repository `config.toml` sets 5000) and `[global.Mempool].MaxBytes` bytes (default 16 MiB).
- **Quorum certificates.** A committed block carries the precommit signatures that formed its quorum. A node that syncs blocks over P2P verifies them only for blocks above `QuorumCertActivationHeight`. The default in `config/config.go` is `0`, which leaves the check off; the repository `config.toml` sets `451949`. `scripts/deployvalidator.sh` copies that `config.toml` and does not edit this key, so a validator bootstrapped with the script gets `451949`. Every validator must use the same value: the comment above the key in `config.toml` says that a value of `0` on a new validator means blocks above the live activation height are synced and committed with no quorum-certificate verification.
- **Heartbeats.** A running node signs and submits a `Heartbeat` transaction for its own validator key, spaced by the heartbeat interval (1 minute) plus a 15-second margin (`cmd/nhb/main.go`, `core/node.go`, `core/engagement/config.go`). Heartbeats feed the engagement score and validator readiness.

### Fees

- **Transfer fee.** Every NHB transfer (0x01) and ZNHB transfer (0x10) is free while the sender's recorded spend of that asset in the window is below the free limit. After that the sender pays an extra `amount × bps / 10000`. Defaults: 20 bps for NHB, 10 bps for ZNHB, free limit 1000 tokens (`1000000000000000000000` wei), window `lifetime` (`monthly` is the alternative). NHB and ZNHB spend are tracked separately. The fee is computed from the value transferred, not from `gasPrice × gasLimit`, and is credited to the fee collector. The collector is `TransferFeeCollector` when set; otherwise it is the node's treasury address, which is the genesis admin wallet (or the address in `NHB_MASTER_TREASURY`) and, when the network has neither, the validator's own address (as on an `--allow-autogenesis` development chain). Config keys: `[global.Fees]` `TransferFeeBps`, `TransferFeeBpsZNHB`, `TransferFreeTierSpendWei`, `TransferFreeTierWindow`, `TransferFeeCollector` (`core/transfer_gas_policy.go`, `config/config.go`, `core/node.go`).
- **Domain fee (MDR).** A transfer whose `merchantAddr` field equals one of the domains `pos`, `p2p` or `otc` is also charged a merchant discount rate after the payer's first `FreeTierTxPerMonth` transactions (default 100) in the UTC calendar month: default 150 bps of the amount (the repository `config.toml` sets 150 for NHB and 200 for ZNHB), configurable per asset in `[[global.Fees.Assets]]` (`native/fees/apply.go`, `core/node.go`, `core/state_transition.go`). On a network with a buyback configured (see [Token supply and issuance](#token-supply-and-issuance)), the share of NHB domain fees set by the buyback parameter `feeShareBps` (default 2000, 20%) goes to the buyback accrual account instead of the fee wallet.
- **Paymaster sponsorship.** An NHB transfer may name a `paymaster` address and carry the paymaster's signature. The sponsor pays the transfer fee if the paymaster module is enabled, the signature is valid, its NHB balance covers `gasLimit × gasPrice` and the merchant, device and global daily caps (`[global.Paymaster]`) are not exceeded (`core/sponsorship.go`).
- **Subscription management fee.** Subscription charges are settled by the chain and are not transfers; default management fee 100 bps with a 500 bps ceiling, up to 3 retries one day apart (`native/subscriptions/params.go`).

### Native modules

Implemented under `native/` and wired into the state transition in `core/state_transition.go`.

| Module | What it does |
| --- | --- |
| `native/escrow` | Escrows with arbitration realms. A realm lists arbitrators (single or committee with a signature threshold); the policy is frozen into each escrow at creation. Also delegated (signature-authorized) create/release/refund/dispute, permissionless expiry sweep after the deadline, and milestone projects. |
| `native/loyalty` | Businesses, merchants and reward programs, with base and dynamic reward rates configured from the genesis `loyaltyGlobal` section and `[global.Loyalty]`. |
| `native/subscriptions` | Plans and subscriptions. The subscriber's `Subscribe` signature is the standing authorization; charges are settled by the chain once per day-rollover. |
| `native/lending` | Pooled lending: NHB supply and borrow against ZNHB collateral, liquidation, fixed-term loans and deposits, and governance-set rate schedules. |
| `native/market` | Peer-to-peer ZNHB-for-NHB listings (create, fill, cancel). |
| `native/swap` | Swap vouchers, price sources, and the redeem (swap-out) path: `RedeemNHB` burns NHB immediately and records a request that a holder of the role `ROLE_SWAP_PAYOUT_ATTESTOR` later marks paid or failed. |
| `native/governance` | Proposals, voting, timelock and execution. See [Governance](#governance). |
| `native/potso` | Heartbeats, engagement meters, weights, reward epochs and stake locks. POTSO is the name the code uses; the code does not expand the acronym. |
| `native/pos` | Point-of-sale authorize, capture and void, plus a merchant and device registry. A finality stream is served at `/ws/pos/finality`. |
| `native/fees` | Domain fee policy (MDR and free tier counters). |
| `native/creator` | Creator content, tips and payouts. Its write RPC methods are disabled and it has no transaction types (see [Not supported](#not-supported)). |
| `native/reputation`, `native/bank`, `native/params`, `native/common` | Skill attestations, transfer bookkeeping and refund tracking, the governed parameter store, and shared pause/quota guards. |

Identity: `RegisterIdentity` claims a username. An alias is 3 to 32 characters from `[a-z0-9._-]`, has an owner, a primary address, linked addresses and an optional avatar reference (`https://` or `blob://`, at most 512 bytes) (`core/identity/alias.go`).

Roles are stored on-chain and granted in the genesis `roles` section or by a `role.allowlist` governance proposal (the Phase E genesis file grants `MINTER_NHB` to two addresses and no other role). A `role.allowlist` proposal can grant or revoke only the roles named in `[governance].AllowedRoles`. That list has no default in `config/config.go`, and while it is empty `role.allowlist` proposals are rejected as disabled. The repository `config.toml` lists `MINTER_NHB`, `ROLE_SWAP_PAYOUT_ATTESTOR`, `ROLE_ESCROW_REALM_ADMIN` and `ROLE_LOYALTY_ADMIN`, so under that file the other roles below can be set only in genesis. A grant of `MINTER_ZNHB` is always rejected (`parseRoleAllowlistPayload`, `native/governance/engine.go`). Roles the code checks:

- `MINTER_NHB`: signs mint vouchers. The mint code also names `MINTER_ZNHB`, but a ZNHB mint is rejected before any role check (`core/state_transition.go`).
- `ROLE_SWAP_PAYOUT_ATTESTOR`: marks a redemption paid or failed. `ROLE_SWAP_ADMIN`: signs the payloads of `TxTypeSwapVoucherReverse` (0x4A) and `TxTypeSwapMarkReconciled` (0x4B) (`core/state_transition.go`).
- `ROLE_ESCROW_REALM_ADMIN`: gates `TxTypeEscrowCreateRealm` and `TxTypeEscrowUpdateRealm`. `ROLE_LOYALTY_ADMIN`: the administrator override for the loyalty transaction family 0x42 to 0x49 (`core/state_transition.go`, `native/loyalty/registry.go`). `ROLE_SUBSCRIPTIONS_ADMIN`: may create and update a plan on behalf of its merchant and cancel a subscription (`native/subscriptions/registry.go`).
- `ROLE_REPUTATION_VERIFIER`: required by the reputation skill-verification path (`core/node.go`). `ROLE_ARBITRATOR`: required by `Node.P2PResolve` (`core/node.go`), which is reachable only through the disabled `p2p_resolve` method.
- Paymaster: `ROLE_PAYMASTER_ADMIN` is the caller role checked in `core/node.go`; the config defaults for auto top-up in `[global.Paymaster.AutoTopUp.Governance]` are `MinterRole = ROLE_PAYMASTER_AUTOFUND` and `ApproverRole = ROLE_PAYMASTER_ADMIN` (`config/config.go`).

### Not supported

- **Smart contracts.** `go-ethereum` is used for cryptography, tries and types. The EVM call in `applyEvmTransaction` is unreachable: `TxTypeTransfer` requires a non-empty recipient and returns from the native transfer path before an EVM is built. `nhb-cli deploy` builds a transfer with no recipient, and a node rejects it with `transfer: recipient address required`.
- **Ethereum JSON-RPC.** No `eth_*`, `net_version` or `web3_*` method exists, so wallets that speak Ethereum JSON-RPC cannot use the node.
- **RPC methods that write state are disabled** and return HTTP 410 with error code `-32060`: `stake_delegate`, `stake_undelegate`, `stake_claim`, `stake_claimRewards`; `loyalty_createBusiness`, `_setPaymaster`, `_addMerchant`, `_removeMerchant`, `_createProgram`, `_updateProgram`, `_pauseProgram`, `_resumeProgram`; `identity_setAlias`, `_setAvatar`, `_addAddress`, `_removeAddress`, `_setPrimary`, `_rename`, `_createClaimable`, `_claim`; `claimable_create`, `_claim`, `_cancel`; `escrow_create`, `_fund`, `_release`, `_refund`, `_dispute`, `_expire`, `_resolve`; `lending_supplyNHB`, `_withdrawNHB`, `_depositZNHB`, `_withdrawZNHB`, `_borrowNHB`, `_borrowNHBWithFee`, `_repayNHB`, `_liquidate`; `p2p_createTrade`, `_settle`, `_dispute`, `_resolve`; `creator_publish`, `_tip`, `_stake`, `_unstake` and `creator_payouts` with `claim: true`. The `escrow_milestone*` write methods and `reputation_verifySkill` are not in this list and are not disabled. Do the same actions by signing a transaction and sending it with `nhb_sendTransaction`. Some of these transaction types exist (staking, escrow, loyalty, lending); for identity aliases beyond `RegisterIdentity`, claimables, P2P trades and creator content, no transaction type exists.

## Token supply and issuance

**ZNHB at genesis.** `config/genesis.phase-e.json` allocates ZNHB to four accounts: 999,996,482.915335463258785943 to the admin (treasury) wallet, 10,000 to the genesis validator, 1,277.084664536741214057 and 240 to two other accounts. The total is **1,000,008,000 ZNHB**. It allocates 10,000 NHB in total across three accounts (9,994.9198, 5 and 0.0802). The same file registers ZNHB with `initialMintPaused: true`.

**Sale Pool and Reward Pool.** When a network has an admin wallet, the first block processed splits the admin wallet's live ZNHB balance once: the Reward Pool is `floor(20%)` of that balance and the Sale Pool is the remainder (`EnsureZNHBPoolsBootstrapped`, `core/state_transition.go`). Started from the Phase E genesis this gives a Sale Pool of 799,997,186.332268370607028755 ZNHB and a Reward Pool of 199,999,296.583067092651757188 ZNHB; both values were read back with `znhb_getTokenomicsState` on a node started from a copy of that genesis with only the validator address replaced. The round numbers 800,000,000 and 200,000,000 appear in the code only as the sale curve's capacity and the emission schedule's ceiling; they are not the pool balances.

Every block checks that Sale Pool + Reward Pool equals the admin wallet's `BalanceZNHB` + `LockedZNHB` + pending unbonds + governance deposit escrow. A mismatch is a hard error (`CheckZNHBSupplyInvariant`).

The Reward Pool is also the balancing entry that keeps that check true when the admin wallet's ZNHB moves through paths that are not otherwise pool-aware. It is increased by a ZNHB transfer fee credited to the admin wallet as fee collector when the sender is a different address, by a rejected governance deposit from a proposer other than the admin wallet, and by a market fill or listing cancellation that credits ZNHB to the admin wallet. It is decreased by epoch emission, by a ZNHB transfer sent from the admin wallet to another address, by the admin wallet creating a market listing, and by POTSO payouts to other addresses when `[potso.rewards].TreasuryAddress` is the admin wallet (`core/state_transition.go`, `core/market_native.go`, `core/rewards_logic.go`, `native/governance/engine.go`).

**Sale curve.** ZNHB is sold from the Sale Pool at a price set by a fixed curve (`core/tokenomics/curve`): 16,000 tranches of 50,000 ZNHB (capacity 800,000,000 ZNHB). The first tranche costs 0.05 NHB per ZNHB (`big.NewRat(5, 100)`); each following tranche is priced at a frozen ratio `r` times the previous one, with `r^16000 ≈ 20`, so the price after the last tranche (`TerminalPrice`) is 1.00. Prices use exact rational arithmetic and cost is path-independent, so splitting a purchase does not change what it costs.

- `TxTypeBuyZNHB` (0x19): payload `{znhbAmount, maxNHBAmount, quoteId?}` (RLP). The buyer's NHB goes to the admin wallet and the curve-priced ZNHB moves from the Sale Pool to the buyer. The transaction fails if the cost exceeds `maxNHBAmount` or the amount exceeds the Sale Pool balance.
- `znhb_getTokenomicsState` returns `currentTranchePrice`, `currentTrancheIndex`, `fullySoldOut`, `cumulativeSaleDistributedWei`, `salePoolBalanceWei`, `rewardPoolBalanceWei`, `buybackAccrualBalanceWei`.
- `znhb_quoteBuy` takes one parameter, a positive decimal integer amount in wei of ZNHB, and returns `znhbAmountWei`, `nhbCostWei`, `effectiveRate`.

**Reward emission.** Validator, staker and engagement rewards follow a halving schedule (`core/rewards/halving.go`): 200 ZNHB per epoch during the first era, halved each era of 500,000 epochs (integer shift at wei precision), so the total ever emitted stays below 200,000,000 ZNHB. An epoch is 100 blocks. The schedule is active only when the network has an admin wallet, and each epoch's payout is clamped to the Reward Pool balance and debits it (`core/node.go`, `core/rewards_logic.go`). The split is fixed in code at 20% validators, 50% stakers, 30% engagement. See [Rewards](#rewards).

**Buyback.** If the genesis file lists `buybackSigners` and `buybackSignerThreshold` (Phase E: three signers, threshold 2), a per-epoch buyback repurchases ZNHB from `TxTypeBuybackAsk` (0x24) sellers, funded by a share of NHB domain fees (see [Fees](#fees)). Sellers are filled pro rata at a computed maximum price. Bought ZNHB is returned to the Sale Pool by lowering the curve position; it is not burned. Values set in `core/node.go`: 2000 bps of NHB domain fees, a 500 bps discount to the curve price and a 500 bps safety margin under the reference price, which is submitted as an M-of-N signed bundle (`TxTypeBuybackRefPrice`, 0x25). The three bps values can be changed by a `policy.buybackParams` proposal; the signer set cannot be changed by any proposal.

**NHB.** See [Native assets](#native-assets). The redeem path burns NHB in the same transaction; it does not hold it in escrow.

## Network parameters

| Item | Value | Where it comes from |
| --- | --- | --- |
| Genesis file used by the repository `config.toml` | `config/genesis.phase-e.json` (genesis time `2026-08-06T08:58:54Z`) | `config.toml`, genesis file |
| Genesis block hash | `0x05f7e1d185bbd78a1f279822401d303c55fe14344f125b4844ff5d94931e1359` | header hash of the block built from that file |
| Chain ID (P2P handshake and `net_info.chainId`) | `430060579445266314` | First 8 bytes of the genesis hash read as a big-endian integer (`core/blockchain.go`). The genesis file has no `chainId` field. |
| Transaction signing chain ID | `0x4e4842` (5,130,306) | `types.NHBChainID()` |
| Mint voucher `chainId` | `430060579445266314` | constant `core.MintChainID` |
| Native symbols | `NHB`, `ZNHB` | genesis `nativeTokens` |
| P2P port | `6001`, TCP only | config defaults, `p2p/server.go` |
| RPC address | generated default `:8080`; the repository `config.toml` and the validator script use `127.0.0.1:8545`; the CLI default is `http://localhost:8080` | `config/config.go`, `config.toml`, `cmd/nhb-cli/main.go` |
| Bootnode used by the validator script | `198.51.100.10:6001` | `scripts/deployvalidator.sh` |

Notes:

- Peers must present the same chain ID and genesis hash in the P2P handshake or the connection fails (`p2p/handshake.go`). A different genesis file gives a different chain ID.
- The chain ID and genesis hash above were computed by loading `config/genesis.phase-e.json` through `core.NewBlockchain`. Only this file loads: `config/genesis.json`, `config/genesis.mainnet.json` and `config/genesis.local.json` declare a `chainId` that differs from their derived value, and loading them fails with `chainId mismatch`.
- The `[p2p].NetworkId` config key is parsed but the node does not read it; the handshake always uses the genesis-derived chain ID (`cmd/nhb/main.go`, `config/config.go`).
- Bootnodes and persistent peers are plain `host:port` strings dialed directly; an `enode://` URI is not parsed. `[p2p].Seeds` entries are `nodeid@host:port`.
- `nhbcoin.nginx.conf` is an nginx site file; one of its `server` blocks proxies to the node RPC on `localhost:8545`. It listens on port 80 only and has no TLS settings.

## Staking, validators and governance

### Staking, delegation and unbonding

All staking actions are signed transactions.

- **Stake / delegate** (`TxTypeStake`, 0x06): `value` is the ZNHB amount, `data` is an RLP list `[validator, registerValidator]`: `validator` is a 20-byte target address and `registerValidator` is an optional boolean (a one-element list `[validator]` is accepted, and `data` may be empty). An empty `validator` or your own address is a self-stake. `registerValidator: true` is accepted only on a self-stake and sets the validator flag; with `value` 0 the transaction only sets the flag and moves no funds (`stakePayload`, `applyStake`, `core/state_transition.go`). The amount moves from `BalanceZNHB` to `LockedZNHB`. An account can delegate to one validator at a time: switching requires a full unstake first. A delegation to another address adds the amount to that account's `Stake`.
- **Unstake** (`TxTypeUnstake`, 0x07): moves `value` out of `LockedZNHB` into a pending unbonding entry with a release time of block time plus the unbonding period. The default is **7 days**, governed by `staking.unbondingDays` (`unbondingPeriod` in `core/state_transition.go`). `data` is an optional RLP list `[validator, deregisterValidator]`; a non-empty `validator` must equal the validator the account is delegated to, and `deregisterValidator: true` is accepted only when that validator is the sender and clears the validator flag. With `value` 0 the transaction only clears the flag (`unstakePayload`, `applyUnstake`).
- **Claim unbonded ZNHB** (`TxTypeStakeClaim`, 0x0D): `data` is an RLP struct `{unbondingId}`. It fails until the release time has passed.
- **Claim staking rewards** (`TxTypeStakeClaimRewards`, 0x34): no payload. Payable once per payout period, default **30 days** (`staking.payoutPeriodDays`), at the APR `staking.aprBps` (default **1250** bps from `[global.Staking]`). It is paid from the balance of `[potso.rewards].TreasuryAddress` and is capped per calendar year only if `staking.maxEmissionPerYearWei` has been set by governance.
- `nhb-cli` builds 0x06 only as a self-stake (`stake <amount> <key_file>`, and `register-validator`, which also sets `registerValidator`), and 0x07 with no validator field (`un-stake`, and `deregister-validator`, which also sets `deregisterValidator`). No client in this repository builds 0x0D or 0x34, or a third-party delegation payload.
- `staking.minStakeWei` exists as a parameter but is not enforced by the staking transaction handler.

### Becoming a validator

A validator is a candidate when all three of these hold (`setAccount`, `core/state_transition.go`):

1. **Registered.** `ValidatorRegistered` is set. Send a self-stake with the register flag: `nhb-cli register-validator <amount> <key_file>` (amount `0` sets the flag and moves no funds). `nhb-cli deregister-validator <key_file>` clears it.
2. **Not delegating away.** The account is not delegating its own stake to a different validator.
3. **Stake at or above the minimum.** The account's total `Stake` is at least `staking.minimumValidatorStake`: **10,000 ZNHB** (`10000000000000000000000` wei) until a governance proposal changes it (`defaultMinimumValidatorStakeWei`, `native/governance/types.go`). Total `Stake` includes ZNHB that other accounts delegated to the address: there is no separate self-stake requirement, and delegating at least the minimum to a node address from any wallet makes it a candidate (`validatorEligibilityBasis`).

The minimum is checked only for validator candidacy. Staking yield accrues differently: an account accrues APR only on its own capital. `stakeRewardBasis` subtracts delegated-in stake from a validator's basis and credits a delegator from its own `LockedZNHB`, so a validator never accrues APR on ZNHB that belongs to its delegators.

### Active validator set and epochs

- An epoch is **100 blocks** (`epoch.DefaultConfig().Length`; the node never overrides it). Epoch rotation is off by default, so there is no cap on the number of validators.
- At each epoch boundary the active set is rebuilt from the candidates. A candidate joins only while its validator heartbeat is current: the last on-chain heartbeat must be no older than `max(15 minutes, 5 × heartbeat interval)`, and the default heartbeat interval is 1 minute, so the window is 15 minutes (`core/epochs.go`, `core/engagement/config.go`). If no candidate qualifies, the set falls back to registered, self-delegated addresses at or above the minimum stake that have sent at least one heartbeat at any time.
- An address that stops meeting the stake, registration or delegation conditions is removed from the active set as soon as its account is next written, not at the next boundary.
- After the first epoch boundary a validator's voting power is its total `Stake`. Validators listed in the genesis file start with the `power` given there (10 in the Phase E file). The proposer weight is `Stake` plus the engagement boost described under [Consensus and block production](#consensus-and-block-production).
- `nhb_getValidatorSet`, `nhb_getValidatorInfo`, `nhb_getEpochSummary` and `nhb_getEpochSnapshot` report the set and per-epoch weights. The per-epoch composite weight is `stake × 100 + engagement score × 1`.
- The engagement score is updated per UTC day: heartbeat minutes ×1, transactions ×5, escrow events ×10, governance events ×20, capped at 250 per day, then smoothed as `(previous × 4 + day) / 5` (`core/engagement/config.go`, `core/state_transition.go`).

### Rewards

Three separate streams exist; all pay ZNHB.

| Stream | When | Amount | Source of funds |
| --- | --- | --- | --- |
| Epoch rewards | Every epoch (100 blocks), only with an admin wallet | Halving schedule above, split 20% validators (equal shares among the epoch's active validators), 50% stakers (pro rata to stake, each validator's share then split between its own stake and its delegators by locked amount), 30% engagement (pro rata to engagement score) | Reward Pool |
| Staking APR | On `TxTypeStakeClaimRewards` | `staking.aprBps` per year, simple (non-compounding) interest on the account's own stake, paid per payout period | `[potso.rewards].TreasuryAddress` |
| POTSO rewards | Each `[potso.rewards].EpochLengthBlocks` blocks | `EmissionPerEpoch` shared by weight; `PayoutMode` `auto` or `claim` | `TreasuryAddress`; when that is the admin wallet the Reward Pool is debited too |

POTSO rewards are disabled unless `EpochLengthBlocks` and `EmissionPerEpoch` are both above zero. `EpochLengthBlocks` defaults to 0 (disabled); a blank `EmissionPerEpoch` is set to 1 ZNHB (`1000000000000000000` wei) by the config loader. The repository `config.toml` sets 120 blocks and 50 ZNHB.

A validator can send its epoch rewards to another wallet: `nhb-cli set-reward-beneficiary <address> <key_file>` (`""` clears it). The transaction is `TxTypeSetRewardBeneficiary` (0x1A) with `data` the RLP list `[beneficiary]`, a Bech32 address string that is empty to clear (`applySetRewardBeneficiary`). The beneficiary cannot be the validator's own address (`applySetRewardBeneficiary`). The redirect applies to every reward the account earns in `settleEpochRewards`.

### Governance

- **Submit.** `TxTypeGovPropose` whose `data` is the RLP list `[kind, payload, deposit]` (`kind` and `payload` are strings, `payload` being JSON text; `deposit` is an integer in wei taken from the proposer's ZNHB balance, not from `value`), with a deposit of at least `[governance].MinDepositWei` (default `1000e18`, 1,000 ZNHB). The deposit is locked; it is refunded to the proposer when the proposal passes. When it is rejected and the network has an admin wallet, it is moved to the admin wallet, and a rejected deposit from any proposer other than the admin wallet is also added to the Reward Pool. On a network without an admin wallet a rejected deposit stays locked (`Finalize`, `native/governance/engine.go`). Kinds: `param.update`, `param.emergency_override`, `param.update_fee_rate`, `policy.slashing`, `role.allowlist`, `treasury.directive`, `policy.swapPriceSigner`, `policy.buybackParams`, `policy.swapRiskParams`, `policy.redemptionFeeParams`, `policy.lendingRateSchedule`, `policy.lendingDepositRateSchedule`. Parameter updates are limited to the keys in `[governance].AllowedParams` (defaults in `config/config.go`), for example `staking.minimumValidatorStake`, `staking.aprBps`, `staking.payoutPeriodDays`, `staking.unbondingDays`.
- **Vote.** `TxTypeGovVote` with `data` the RLP list `[proposalId, choice]`, `choice` being `yes`, `no` or `abstain`. Voting power is the voter's `WeightBps` in the most recent processed POTSO reward-epoch snapshot, not the raw staked balance. That snapshot combines POTSO stake locks, the total stake of each eligible validator and engagement. An address with no weight in the snapshot cannot vote, and voting fails with "potso snapshot unavailable" on a node where POTSO rewards are not configured.
- **Tally.** `[governance]` defaults: voting period 604800 s (7 days), timelock 172800 s (2 days), `QuorumBps` 2000 and `PassThresholdBps` 5000. A proposal reaches quorum when the summed `WeightBps` of all ballots (yes, no and abstain) is at least `QuorumBps`, and passes when yes weight divided by yes plus no weight, in basis points, is at least `PassThresholdBps`. Abstain counts toward quorum only.
- **Finalize, queue, execute.** `TxTypeGovFinalize` (after voting ends), `TxTypeGovQueue` (passed proposals only) and `TxTypeGovExecute` (after the timelock), each with `data` the RLP list `[proposalId]`, can be sent by any account and nothing submits them automatically; `services/gov-keeper` is a daemon that does. Executing writes the change to the on-chain parameter store, roles or treasury.
- CLI: `nhb-cli gov propose|vote|finalize|queue|execute|show|list`. Reads: `gov_proposal`, `gov_list`.

## Build

Requires Go 1.24 or newer (`go.mod`: `go 1.24.0`, `toolchain go1.24.3`; CI and `scripts/deployvalidator.sh` use 1.24.3).

```bash
go build -trimpath -ldflags="-s -w" -buildvcs=false -o bin/nhb ./cmd/nhb
go build -trimpath -ldflags="-s -w" -buildvcs=false -o bin/nhb-cli ./cmd/nhb-cli
```

These are the commands the CI workflow and `scripts/deployvalidator.sh` use. `scripts/build.sh` and `scripts/build.ps1` also produce `bin/nhb` and `bin/nhb-cli` (`bin/nhb.exe` on Windows), but `GO_VERSION` defaults to 1.23.0 in both, and unless `GO_CMD` or `GOTOOLCHAIN` is set they export `GOTOOLCHAIN=go1.23.0`, a version older than the one `go.mod` requires. CI builds and tests the node on Linux, macOS and Windows.

Other `Makefile` targets: `make proto` runs `go run ./tools/proto/gen.go`, which runs `buf format -w`, `buf lint` and `buf generate` (Go stubs into `proto/`, TypeScript into `clients/ts`) and adds `buf breaking` when `BUF_BREAKING_AGAINST` is set, so it needs `buf` installed; `make sdk` depends on `make proto` and then runs `go test ./...` in `sdk/`, so it needs `buf` too; `make tidy` tidies both Go modules (`go.work` lists `.` and `./sdk`).

## Run a node

### Local development chain

A node needs a genesis file. `config/genesis.phase-e.json` names a validator whose key you do not hold, so a private chain needs its own genesis:

1. Copy `config.toml` to a working directory and set `GenesisFile = ""`. The repository `config.toml` binds P2P to `127.0.0.1:6001` and RPC to `127.0.0.1:8545`, enables the RPC JWT check and allows plaintext RPC on loopback only.
2. Start once with `--allow-autogenesis` and `NHB_VALIDATOR_PASS` set. The node creates the encrypted keystore at `ValidatorKeystorePath` (`validator.keystore`, relative to the working directory) and prints `Starting node with validator address: nhb1...`. Stop it and delete `./nhb-data`.
3. Copy `config/genesis.phase-e.json` and replace the validator address, in both `validators` and `alloc`, with the address you just saw. Leave out `chainId`; the chain ID is derived from the genesis hash.
4. Start with your genesis and the RPC JWT secret:

```bash
export NHB_ENV=dev
export NHB_VALIDATOR_PASS='the-passphrase-from-step-2'
export NHB_RPC_JWT_SECRET='a-long-random-secret'
./bin/nhb --config ./config.toml --genesis ./mygenesis.json
```

The node opens LevelDB in `DataDir` (`./nhb-data`), writes `genesis.resolved.json`, `p2p/peerstore`, `p2p/node_key.json` and `polc_lock.json` there, serves RPC, waits 30 seconds, and then commits blocks. A validator that holds all of the voting power reaches quorum by itself. Do not start a node against a `DataDir` that was created with a different genesis.

Two behaviors to know:

- `--allow-autogenesis` (or `NHB_ALLOW_AUTOGENESIS=true`) builds an empty development genesis that lists no validators (`core/blockchain.go`). With no voting power in the set the engine cannot reach a quorum (`hasTwoThirdsPowerLocked` returns false when total power is zero).
- If the file given to `--config` does not exist, the node writes a default config and a keystore. That default cannot start the RPC server: the first start panics with `subscriptions: maxRetries must be positive`, and the next start stops with `JWT authentication must be enabled unless mutual TLS is configured`. Start from the repository `config.toml`.

To call privileged RPC methods, create a token with the secret you gave the node:

```bash
NHB_RPC_JWT_SECRET='a-long-random-secret' go run generate_jwt.go   # prints an HS256 JWT
export NHB_RPC_TOKEN=<token>
export RPC_URL=http://127.0.0.1:8545
./bin/nhb-cli balance <address>
```

`generate_jwt.go` signs `iss: nhb-rpc`, `aud: ["wallets"]` with a ten-year expiry, matching `[RPCJWT]` in `config.toml`.

### Validator bootstrap on a server

For a Debian or Ubuntu style host with `sudo`, `systemd` and `apt-get`:

```bash
git clone https://github.com/josephblackelite/nhbchain.git
cd nhbchain
bash scripts/validator-only-bootstrap.sh --beneficiary YOUR_NHB_WALLET_ADDRESS --reset-state
```

`scripts/validator-only-bootstrap.sh` calls `scripts/deployvalidator.sh` with the same arguments. Options:

| Option | Default | Meaning |
| --- | --- | --- |
| `--beneficiary <nhb1...>` | none, required | Wallet that should receive this validator's epoch reward payouts. Must differ from the validator's own address. |
| `--bootnode <host:port>` | `198.51.100.10:6001` | Written into `Bootnodes` and `PersistentPeers`. |
| `--network-id <id>` | `430060579445266314` | Written into `[p2p].NetworkId`, which the node does not read. |
| `--listen-addr <addr>` | `0.0.0.0:6001` | P2P listen address. |
| `--rpc-addr <addr>` | `127.0.0.1:8545` | RPC listen address. |
| `--external-address <ip>` | auto-detected | Address peers should dial back; written to `[p2p].ExternalAddress`. |
| `--email <address>` | none | Best-effort request to an external onboarding-email endpoint (a POST of the validator address and this email address); the script continues if it fails. |
| `--onboarding-email-endpoint <url>` | an HTTPS URL set in the script | Endpoint used by `--email`. |
| `--reset-state` | off | Deletes `/var/lib/nhbchain/nhb-data` before the first start. Use it only on a new server. |
| `--help`, `-h` | | Prints the usage text and exits. |

What the script does, in order:

1. Installs `rsync`, `perl` and `curl` with `apt-get` if missing, and Go 1.24.3 into `/usr/local/go` if `/usr/local/go/bin/go` is absent. Adds a 4 GiB swap file if the host has less than 4 GiB of RAM and no swap.
2. Creates the system user `nhb`, the directories `/etc/nhbchain` (mode 700), `/var/lib/nhbchain` and `/opt/nhbchain`, copies the repository to `/opt/nhbchain` and builds `bin/nhb` and `bin/nhb-cli`.
3. Generates the validator key on the host with `nhb-cli generate-key` the first time (`/etc/nhbchain/validator.key`, mode 600) and reuses it on later runs.
4. Writes `/etc/nhbchain/node.env` (mode 600, root): `NHB_ENV=prod`, a random `NHB_RPC_JWT_SECRET` and `NHB_VALIDATOR_RAW_KEY`.
5. Copies the repository `config.toml` to `/etc/nhbchain/config.toml` and edits `ListenAddress`, `RPCAddress`, `DataDir` (`/var/lib/nhbchain/nhb-data`), `ValidatorKeystorePath` (emptied), `ValidatorKMSEnv` (`NHB_VALIDATOR_RAW_KEY`), `NetworkName` (`nhb-mainnet-validator`), `[p2p].NetworkId`, `[p2p].ExternalAddress`, `Bootnodes` and `PersistentPeers`. Every other key, including `QuorumCertActivationHeight` (451949 in the repository file), keeps the value in `config.toml`. The genesis file is the one named by `GenesisFile` in that `config.toml`, `./config/genesis.phase-e.json`, resolved from the service working directory `/opt/nhbchain`.
6. Installs and starts `nhb.service` (`ExecStart=/opt/nhbchain/bin/nhb --config /etc/nhbchain/config.toml`), then waits up to 60 seconds for `nhb_getNetworkStats` to answer on the RPC address.
7. Runs `nhb-cli set-reward-beneficiary <beneficiary> /etc/nhbchain/validator.key` and `nhb-cli register-validator 0 /etc/nhbchain/validator.key`. These are signed transactions and need `NHB_RPC_TOKEN` (see below); the script does not set it, so if they fail it prints a warning and you run them yourself.
8. Prints the validator address.

Then get the validator to at least the minimum stake (see [Becoming a validator](#becoming-a-validator)). Either delegate at least 10,000 ZNHB from any wallet to the validator address with a `TxTypeStake` transaction whose `data` is the RLP list `[validator]`, or send 10,000 ZNHB to the validator address and self-stake on the server:

```bash
sudo -u nhb env RPC_URL=http://127.0.0.1:8545 NHB_RPC_TOKEN=<token> \
  /opt/nhbchain/bin/nhb-cli register-validator 10000000000000000000000 /etc/nhbchain/validator.key
```

`NHB_RPC_TOKEN` is a JWT signed with the `NHB_RPC_JWT_SECRET` value in `/etc/nhbchain/node.env` (issuer `nhb-rpc`, audience `wallets`; `generate_jwt.go` makes one). The node joins the active set at the next epoch boundary while its heartbeat is current. `sudo systemctl status nhb.service` and `sudo journalctl -u nhb.service -f` show its state. `docs/validators/onboarding.md` walks through the same flow.

`node.env` holds the raw key because the script configures `ValidatorKMSEnv`. `ValidatorKMSURI` supports only the `env://NAME` scheme (`cmd/nhb/main.go`); there is no external KMS client in the node.

### Full node with the production script

`scripts/run_nhbcoin_node.sh` runs `scripts/bringup_production_stack.sh`. It needs `go`, `python3`, `rsync`, `systemctl` and `install`, and the files `/etc/nhbchain/config.toml` and `/etc/nhbchain/node.env` must already exist (`deploy/env/node.env.example` is the template: `NHB_ENV`, `NHB_VALIDATOR_PASS`, `NHB_RPC_JWT_SECRET`, `OTEL_EXPORTER_OTLP_*`). Options: `--install-root`, `--etc-dir`, `--state-dir`, `--systemd-dir`, `--service-user`, `--service-group`, `--reset-state` (moves `nhb-data` to a backup directory), `--skip-start`. It syncs the repository to `/opt/nhbchain`, runs `scripts/verify_prod_config.sh` on the config, runs `scripts/build.sh`, installs `nhb.service` and starts it.

`scripts/verify_prod_config.sh -c <config-file>` first checks the given file with `python3` and requires: no wildcard bind in `ListenAddress` or `RPCAddress`; `RPCAllowInsecure = false` and the RPC TLS certificate, key and client CA paths set; `network_security.AllowInsecure = false` with its six TLS file paths set; `global.Loyalty.Dynamic.EnforceProRate` and `global.Loyalty.Dynamic.enableprorate` true; `global.Fees.owner_wallet` and every `[[global.Fees.Assets]]` `owner_wallet` set; `global.Staking.MaxEmissionPerYearWei` above zero; a `global.Pauses` table with no flag set to true. The key names are matched case-sensitively as written here, and the repository `config.toml` spells two of them `EnableProRate` and `OwnerWallet`. Run against the repository `config.toml` (checked in this worktree) the script stops at that first stage with eight errors: `RPCAllowInsecure = true` (line 28), the three empty `RPCTLS*` paths, and the missing `enableprorate` and `owner_wallet` keys. Only when that stage passes does it scan, for `0.0.0.0` and `AllowInsecure = true`, `config.toml` in the directory above `scripts/` (the install root when run as `/opt/nhbchain/scripts/verify_prod_config.sh`), `deploy/compose/config`, `deploy/helm/values/prod`, `deploy/helm/values/staging`, `deploy/helm/values/dev` and `deploy/helm/consensusd/values.yaml` under that directory.

Open TCP port `6001` to peers. The RPC port does not need to be public: plaintext RPC is refused on non-loopback addresses, so expose it only with TLS configured (`RPCTLSCertFile`, `RPCTLSKeyFile`) or behind a TLS-terminating proxy.

### Configuration reference

The node reads a TOML file (`--config`). Defaults below come from `config/config.go` unless another file is named. The repository `config.toml` is a working example.

| Key | Default | Notes |
| --- | --- | --- |
| `ListenAddress` | `:6001` (generated default) | P2P listener. |
| `RPCAddress` | `:8080` (generated default) | JSON-RPC listener. |
| `DataDir` | `./nhb-data` | LevelDB and node state. |
| `GenesisFile`, `AllowAutogenesis` | empty, `false` | `--genesis` and `NHB_GENESIS` override the file. If the configured file is missing and autogenesis is off, the node writes the embedded `genesis.mainnet.json` to that path. |
| `NetworkName` | `nhb-local` | Free-form. The values `mainnet` and `nhbchain-1` force `[governance].BlockTimestampToleranceSeconds` to 5 whatever the file says (other names use the file value, and 0 becomes 5), and `mainnet` additionally requires swap RPC secrets (`config/config.go`). |
| `ValidatorKeystorePath` | `validator.keystore` in the config file's directory (generated config) | Encrypted keystore; the passphrase comes from `NHB_VALIDATOR_PASS` or a terminal prompt and must not be empty. |
| `ValidatorKMSEnv`, `ValidatorKMSURI` | empty | Read the hex private key from an environment variable (`env://NAME` for the URI form). |
| `Bootnodes`, `PersistentPeers`, `[p2p].Seeds` | empty | See [Network parameters](#network-parameters). |
| `[p2p]` limits and timers | see `config.toml` | The repository file sets `MaxPeers` 64, `MaxInbound` 60, `MaxOutbound` 30, `MinPeers` 12, `OutboundPeers` 16, `BanScore` 100, `GreyScore` 50, `HandshakeTimeoutMs` 3000, `PingIntervalSeconds` 30, `PEX` true. |
| `RPCJWT` | none | `Enable`, `Alg` (`HS256` or `RS256`), `HSSecretEnv`, `RSAPublicKeyFile`, `Issuer`, `Audience`, `MaxSkewSeconds` (30 when unset or not above zero, `rpc/http.go`; the repository `config.toml` sets 120). |
| `RPCTLSCertFile`, `RPCTLSKeyFile`, `RPCTLSClientCAFile` | empty | With a client CA the server requires client certificates. |
| `RPCAllowInsecure` | `false` | Plaintext RPC, accepted only on a loopback address. |
| `RPCMaxTxPerWindow` and per-IP, identity, chain limits | 5 per `RPCRateLimitWindow` (60 s) | The repository `config.toml` sets 120. |
| `RPCAllowlistCIDRs`, `RPCTrustedProxies`, `RPCTrustProxyHeaders`, `[RPCProxyHeaders]` | empty | Client allowlist and forwarded-header handling. |
| `QuorumCertActivationHeight` | `0` (off); the repository `config.toml` sets `451949` | See [Consensus](#consensus-and-block-production). |
| `[consensus]` | 2 s, 2 s, 2 s, 4 s | `ProposalTimeout`, `PrevoteTimeout`, `PrecommitTimeout`, `CommitTimeout`. |
| `[governance]` | see [Governance](#governance) | |
| `[global.Staking]` | `AprBps` 1250 | Read at node start to set the staking APR; the governance parameter `staking.aprBps` takes precedence. The unbonding period (7 days) and payout period (30 days) are read from the governance parameters `staking.unbondingDays` and `staking.payoutPeriodDays` when a transaction runs, and fall back to those two constants. |
| `[global.Pauses]`, `[global.Quotas.*]` | off; 6000 requests per minute in `config.toml` | Per-module pause flags and per-sender request quotas. |
| `[potso.rewards]`, `[potso.weights]`, `[potso.abuse]` | rewards disabled | See [Rewards](#rewards). |

Environment variables read by the node (`cmd/nhb`, `core`, `observability/logging`) that this README describes: `NHB_VALIDATOR_PASS`, `NHB_GENESIS`, `NHB_ALLOW_AUTOGENESIS`, `NHB_ENV`, `NHB_MASTER_TREASURY` (sets the treasury and admin wallet address; an unparsable value is ignored with a warning), `NHB_LOG_FILE` (also writes the JSON log to that file, rotated at 100 MB with 5 backups for 28 days), `NHB_ZNHB_ORACLE_PRICE_USD` (decimal ZNHB price in USD that seeds the manual swap oracle at start; `0.05` when unset or not a valid number) and the variable named by `RPCJWT.HSSecretEnv` (`NHB_RPC_JWT_SECRET` in `config.toml`). The node reads a few more variables for off-chain price and quote services that this README does not cover, so this list is not exhaustive. `NHB_ENV` is added to every log line as the environment and is also the node network mode (`core/node.go`): when unset it is `prod`. The modes `dev`, `development`, `local`, `test`, `testing` and `staging` allow the unbounded mempool option (`[mempool].AllowUnlimited`); in mode `prod` (case-insensitive) `SetGlobalConfig` rejects `EnforceProRate` true with `EnableProRate` false for the loyalty pro-rate setting. `consensusd`, `p2pd` and `gateway` also read `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_HEADERS` and `OTEL_EXPORTER_OTLP_INSECURE`.

Several values above are read from each node's local config rather than from chain state (`[global.Staking].AprBps`, the `[potso.*]` sections, `[global.Fees]`, `[governance]`). Run identical values on every validator.

## Command-line client

`nhb-cli` talks to a node over JSON-RPC.

- `RPC_URL` (or `--rpc <url>` anywhere on the command line) selects the node; the default is `http://localhost:8080`.
- Every command that submits a transaction calls `nhb_sendTransaction`, which needs `NHB_RPC_TOKEN` set to a valid JWT; the CLI refuses to send without it.
- Key arguments are a raw key file or a V3 keystore (with `NHB_KEYSTORE_PASSPHRASE`). Amounts are integers in wei.

| Command | Effect |
| --- | --- |
| `generate-key` | Writes `wallet.key` and prints the address. |
| `address <key_file>` | Prints the address of a key file. |
| `balance <address>` | `nhb_getBalance`: balances, stake, locked ZNHB, delegation, pending unbonds, username, nonce, validator registration. |
| `send-nhb`, `send-znhb [--rpc <url>] [--gas <limit>] [--gas-price <price>] <recipient> <amount> <key_file>` | Transfers (0x01, 0x10). Defaults: gas limit 21000 (NHB) and 25000 (ZNHB), gas price 1. |
| `claim-username <username> <key_file>` | `RegisterIdentity` (0x02). |
| `stake <amount> <key_file>`, `un-stake <amount> <key_file>` | Self-stake (0x06) and unstake (0x07). |
| `register-validator <amount> <key_file>`, `deregister-validator <key_file>` | Set or clear the validator flag; `register-validator` with an amount above 0 also self-stakes it. |
| `set-reward-beneficiary <address\|""> <key_file>` | 0x1A. |
| `heartbeat <key_file>` | 0x08. |
| `stake position <address>`, `stake preview <address>` | Read staking share and claimable-reward previews (need a token). |
| `gov propose --kind --payload --key [--deposit]`, `gov vote --id --key --choice`, `gov finalize\|queue\|execute --id --key`, `gov show --id`, `gov list` | Governance. `--payload` is JSON or `@file`; `--deposit` accepts `1000e18`. |
| `id resolve --alias <alias>`, `id reverse --addr <address>` | Identity reads. |
| `escrow create\|fund\|release\|refund\|expire\|dispute\|create-realm`, `escrow get` | Escrow transactions (`--key` required) and a read. |
| `subscriptions create-plan\|update-plan\|subscribe\|cancel` and `get-plan\|list-plans\|get-subscription\|list-by-payer\|list-by-merchant\|list-charges\|config` | Subscription transactions and reads. |
| `loyalty-create-business`, `loyalty-set-paymaster`, `loyalty-add-merchant`, `loyalty-remove-merchant`, `loyalty-create-program`, `loyalty-update-program`, `loyalty-pause-program`, `loyalty-resume-program` | Loyalty transactions (0x42 to 0x49). |
| `loyalty-get-business`, `loyalty-list-businesses`, `loyalty-list-programs`, `loyalty-program-stats`, `loyalty-user-daily`, `loyalty-paymaster-balance`, `loyalty-resolve-username`, `loyalty-user-qr` | Loyalty reads. |
| `potso stake lock\|unbond\|withdraw` | POTSO stake transactions (0x2D to 0x2F). `potso stake info`, `potso heartbeat`, `potso user-meters`, `potso top` and `potso reward claim\|history\|export` call the matching `potso_*` RPC methods. |
| `fees status`, `swap voucher get\|list\|export`, `pos sweep-voids`, `keystore import --out <path>` | Fee status, voucher queries, POS void sweep, and encrypting a key (from `NHB_KEYSTORE_IMPORT_PRIVATE_KEY` and `NHB_KEYSTORE_IMPORT_PASSPHRASE`) into a keystore file. |

Commands that call a disabled RPC method fail with error `-32060`: `stake claim`, `id set-alias`, `set-avatar`, `add-address`, `remove-address`, `set-primary`, `rename`, `create-claimable`, `claim`, `claimable create|claim|cancel`, and `p2p create-trade|settle|dispute|resolve`. `deploy` is rejected by the node (see [Not supported](#not-supported)). `nhb-cli` with no arguments prints only part of this list.

## JSON-RPC API

The RPC server is in `rpc/http.go`.

- **Transport.** HTTP POST of JSON-RPC 2.0 to `/` (body limit 1 MiB). `params` is an array and `id` is an integer. Errors use the JSON-RPC `error` object with HTTP status codes. `/ws/pos/finality` and `/ws/explorer` are WebSocket endpoints, and a gRPC `pos.v1.Realtime` service shares the port.
- **TLS.** The server starts only with TLS (`RPCTLSCertFile` and `RPCTLSKeyFile`), or with `RPCAllowInsecure` on a loopback address.
- **Authentication.** The server refuses to start unless a JWT verifier is enabled (`[RPCJWT]`) or a client CA file is set for mutual TLS. Methods that call `requireAuth` accept a verified client certificate or `Authorization: Bearer <JWT>` (HS256 with a secret read from an environment variable, or RS256 with a public key file; the issuer and audience must match). Methods that require it: `nhb_sendTransaction`, `tx_setSponsorshipEnabled`, `net_dial`, `net_ban`, `sync_snapshot_export`, `sync_snapshot_import`, `stake_getPosition`, `stake_previewClaim`, `engagement_register_device`, `engagement_submit_heartbeat`, `potso_stake_info`, `potso_reward_claim`, `buyback_submitRefPrice`, `lending_submitRefPrice`, `pos_sweepVoids`, `reputation_verifySkill`, `creator_payouts`, all six `escrow_milestone*` methods, and the swap methods `nhb_requestSwapApproval`, `nhb_getSwapQuote`, `nhb_swapMint`, `nhb_swapBurn`, `nhb_getSwapStatus`, `nhb_checkSwapAllowance`, `swap_limits`, `swap_provider_status`, `swap_burn_list`, `swap_voucher_reverse`, `swap_markReconciled`, `swap_setManualQuote` and `swap_listPendingRedemptions`. The other methods in the tables below do not call it. When `[RPCSwapAuth]` secrets are configured, exactly these methods also require API-key request signing (`isPublicSwapMethod`, `rpc/http.go`): `swap_submitVoucher`, `swap_voucher_get`, `swap_voucher_list`, `swap_voucher_export`, `swap_getRiskParams`, `swap_getRedemptionFeeParams`, `nhb_requestSwapApproval`, `nhb_getSwapQuote`, `nhb_swapMint`, `nhb_swapBurn`, `nhb_getSwapStatus`, `nhb_checkSwapAllowance` and `nhb_getOraclePrice`.
- **Rate limits.** Per-source limits on `nhb_sendTransaction` and other methods (`RPCMaxTx*`, `RPCRouteRateLimits`, `RPCRateLimitWindow`); over the limit the server returns code `-32020`.
- **Error codes.** `-32700` parse error, `-32600` invalid request, `-32601` method not found, `-32602` invalid params, `-32001` unauthorized, `-32000` server error, `-32010` duplicate transaction, `-32020` rate limited, `-32030` mempool full, `-32050` module paused, `-32060` method disabled. `-32040` is declared as `codeInvalidPolicyInvariants` in `rpc/http.go` and no handler in `rpc/` returns it under that name. Some method families define their own codes, and the same number means different things in different families:

  | Family | Codes |
  | --- | --- |
  | `escrow_*` (including `escrow_milestone*`) and `p2p_*` | `-32021` invalid params, `-32022` not found, `-32023` forbidden, `-32024` conflict, `-32025` internal |
  | `claimable_*` | `-32041` invalid params, `-32042` not found, `-32043` forbidden, `-32044` conflict, `-32045` internal |
  | `net_*` | `-32040` invalid params, `-32041` unknown peer, `-32042` peer banned |
  | `sync_*` | `-32060` invalid params, `-32061` unavailable |

Example:

```bash
curl -s -X POST http://127.0.0.1:8545/ -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"net_info","params":[]}'
# {"jsonrpc":"2.0","id":1,"result":{"nodeId":"0x...","peerCounts":{"total":0,"inbound":0,"outbound":0},"chainId":<uint64>,"genesisHash":"<hex>","listenAddrs":["127.0.0.1:6001"]}}
```

Method families in the dispatcher (`handle` in `rpc/http.go`):

| Family | Methods |
| --- | --- |
| Transactions | `nhb_sendTransaction`, `tx_previewSponsorship`, `tx_getSponsorshipConfig`, `tx_setSponsorshipEnabled`, `mint_with_sig` |
| Accounts and chain | `nhb_getBalance`, `nhb_getTotalSupply`, `nhb_getLatestBlocks`, `nhb_getLatestTransactions`, `nhb_getTransactionHistory`, `nhb_getAddressActivity`, `nhb_getTransaction`, `nhb_getTransactionReceipt`, `nhb_searchExplorer`, `nhb_getExplorerSnapshot`, `nhb_getNetworkStats`, `nhb_txWindowStats`, `nhb_getOwnerWalletStats`, `nhb_getLoyaltyBudgetStatus`, `nhb_getSlashingEvents`, `nhb_getOraclePrice` |
| Validators, epochs, rewards | `nhb_getValidatorSet`, `nhb_getValidatorInfo`, `nhb_getEpochSummary`, `nhb_getEpochSnapshot`, `nhb_getRewardEpoch`, `nhb_getRewardPayout`, `nhb_getRewardHistory` |
| ZNHB treasury | `znhb_getTokenomicsState`, `znhb_quoteBuy`, `buyback_getRefPriceStatus`, `buyback_submitRefPrice`, `lending_getRefPriceStatus`, `lending_submitRefPrice` |
| Fees | `fees_listTotals`, `fees_getMonthlyStatus`, `fees_getTransferStatus`, `fees_getTransferQuote` |
| Staking (reads) | `stake_getPosition`, `stake_previewClaim` |
| Governance (reads) | `gov_proposal`, `gov_list` |
| Subscriptions (reads) | `subscriptions_getPlan`, `_listPlansByMerchant`, `_getSubscription`, `_listByPayer`, `_listByMerchant`, `_listCharges`, `_getConfig` |
| Lending (reads) | `lending_getMarket`, `lend_getPools`, `lending_getUserAccount`, `lending_getFixedTermLoan`, `lending_getFixedTermDeposit`, `lending_getRateSchedule` |
| Market | `market_listOpenListings`, `market_getListing`, `market_getMyListings`, `market_getMyFills`, `market_getFlatFee` |
| Loyalty (reads) | `loyalty_getBusiness`, `_listBusinesses`, `_listPrograms`, `_programStats`, `_listAccruals`, `_userDaily`, `_paymasterBalance`, `_resolveUsername`, `_userQR` |
| Identity, claimable, escrow (reads) | `identity_resolve`, `identity_reverse`, `claimable_get`, `escrow_get`, `escrow_getRealm`, `escrow_getSnapshot`, `escrow_listEvents`, `escrow_milestoneGet` |
| Escrow milestones (writes) | `escrow_milestoneCreate`, `escrow_milestoneFund`, `escrow_milestoneRelease`, `escrow_milestoneCancel`, `escrow_milestoneSubscriptionUpdate`. Unlike the disabled escrow methods, these are live: each takes a signature over its parameters in the request, and the node writes the result to its own state (`Node.EscrowMilestone*`, `core/node.go`) rather than through a transaction in a block. |
| Network | `net_info`, `net_peers`, `net_dial`, `net_ban`, `p2p_info`, `p2p_peers`, `p2p_getTrade`, `sync_status`, `sync_snapshot_export`, `sync_snapshot_import` |
| POTSO and engagement | `potso_heartbeat`, `potso_userMeters`, `potso_top`, `potso_leaderboard`, `potso_params`, `potso_getWeight`, `potso_stake_info`, `potso_epoch_info`, `potso_epoch_payouts`, `potso_reward_claim`, `potso_rewards_history`, `potso_rewards_outflow`, `potso_export_epoch`, `potso_submitEvidence`, `potso_getEvidence`, `potso_listEvidence`, `engagement_register_device`, `engagement_submit_heartbeat` |
| POS | `pos_getAuthorization`, `pos_getAuthorizationByIntentRef`, `pos_sweepVoids` |
| Swap and mint | `swap_getRiskParams`, `swap_getRedemptionFeeParams`, `swap_submitVoucher`, `swap_voucher_get`, `swap_voucher_list`, `swap_voucher_export`, `swap_limits`, `swap_provider_status`, `swap_burn_list`, `swap_voucher_reverse`, `swap_markReconciled`, `swap_setManualQuote`, `swap_listPendingRedemptions`, `nhb_requestSwapApproval`, `nhb_getSwapQuote`, `nhb_swapMint`, `nhb_swapBurn`, `nhb_getSwapStatus`, `nhb_checkSwapAllowance` |
| Reputation, creator | `reputation_verifySkill` (writes a skill verification to the node's state; the verifier address is a request parameter and the node checks that address for `ROLE_REPUTATION_VERIFIER`, `core/node.go`), `creator_payouts` (read path) |

For `net_info`, `chainId` is the genesis-derived chain ID from [Network parameters](#network-parameters), not the transaction signing chain ID.

## Security and operations

- **RPC and keys.** See [JSON-RPC API](#json-rpc-api) for authentication and TLS. The CLI refuses a key file that contains the retired repository placeholder key and tells you to run `generate-key` (`cmd/nhb-cli/main.go`).
- **Validator key.** By default an encrypted V3 keystore whose passphrase must be non-empty (`NHB_VALIDATOR_PASS` or a prompt). `nhbctl migrate-keystore` converts a legacy `ValidatorKey` config value.
- **P2P.** Handshakes are signed with the node key and bind the chain ID, genesis hash and a nonce (replay window 10 minutes); per-peer rate limits, ban and grey scores, and peer exchange (`PEX`, default on) are configured in `[p2p]`.
- **Reporting vulnerabilities.** See `SECURITY.md`; the machine-readable contact is in `.well-known/security.txt`. Policy details are in `docs/security/disclosure.md`.
- **Operations documents:** `docs/ops/validator-runbook.md`, `docs/ops/configuration.md`, `docs/ops/snapshots.md`, `docs/security/network-hardening.md`, `docs/security/networking.md`, `docs/consensus/bft-height-sync.md`, `docs/consensus/invariants.md`, `docs/perf/baselines.md`, `docs/perf/tuning.md`.

## Repository layout

| Path | Contents |
| --- | --- |
| `cmd/` | Binaries listed in [Components](#components). |
| `consensus/` | BFT engine (`bft`), the point-of-sale block reservation constants (`proposer.go`), the consensus gRPC service, client and codec, POTSO evidence, penalty, rewards and emission code, the consensus store. |
| `core/` | Node, blockchain, state transition, epochs, rewards, staking, governance transaction handlers, genesis loader, tokenomics (`curve`, `buyback`), engagement, sync. |
| `native/` | Native modules (see [Native modules](#native-modules)). |
| `state/`, `storage/`, `mempool/` | State helpers, LevelDB and trie storage, mempool priority. |
| `p2p/`, `network/` | P2P server, handshake, peer store, and the gRPC network service used by `p2pd`. |
| `rpc/` | JSON-RPC server and handlers. |
| `crypto/` | Keys, addresses, keystore. |
| `config/` | Config loader and defaults, genesis files, embedded genesis. |
| `services/` | Auxiliary Go services: escrow gateway, governance daemon and keeper, identity gateway, lending services, webhook worker. |
| `gateway/` | Gateway routes, middleware and config. |
| `proto/`, `clients/ts/` | Protobuf definitions with generated Go stubs; generated TypeScript clients. |
| `sdk/` | Go SDK (module `nhbchain/sdk`) and TypeScript wallet and identity-gateway helpers (`sdk/ts`). |
| `observability/` | Metrics, logging and OpenTelemetry setup, dashboards and alert rules. |
| `deploy/`, `k8s/`, `ops/` | systemd unit, environment template, Docker Compose, Helm charts, ingress, Prometheus and Grafana configuration. |
| `scripts/` | Bootstrap, build, audit and verification scripts. |
| `tests/`, `bench/`, `tools/`, `examples/`, `integrations/` | Test suites, load tools, code and docs tooling, examples, reward export and webhook helpers. |
| `explorer/`, `stablequote/` | Explorer response formatters; request and response types plus an HTTP client used by the RPC swap-quote methods. |
| `audit/` | Output directory of `scripts/audit.sh` reports. |
| `.github/`, `.well-known/` | CI workflows and issue template; `security.txt`. |
| `docs/` | Developer documentation. |

## Documentation

Start with `docs/index.md` (components and deployment shapes) and `docs/README.md` (how the docs are checked).

- **Validators and deployment:** `docs/validators/onboarding.md`, `docs/deploy/one-shot-deploy.md`, `docs/deploy/local.md`, `docs/deploy/kubernetes.md`.
- **API and transactions:** `docs/api/rpc.md`, `docs/networking/net-rpc.md`, `docs/transactions/signing.md`, `docs/transactions/envelope.md`, `docs/sdk/go.md`, `docs/sdk/js.md`, `docs/cookbooks/developers.md`.
- **Modules:** `docs/tokenomics/tokenomics.md`, `docs/staking/staking.md`, `docs/governance/overview.md`, `docs/potso/README.md`, `docs/subscriptions/README.md`, `docs/escrow/escrow.md`, `docs/loyalty/loyalty.md`, `docs/identity/identity.md`, `docs/finance/lending/overview.md`, `docs/fees/policy.md`.
- **Identity examples and API:** `docs/identity/identity-cli.md`, `docs/identity/identity-api.md`, `docs/identity/pay-by-username.md`, `docs/examples/identity`, `docs/openapi/identity.yaml`.
- **Audit and security:** `docs/audit/overview.md`, `docs/audit/static-analysis.md`, `docs/audit/fuzzing.md`, `docs/audit/e2e-flows.md`, `docs/audit/docs-quality.md`, `docs/security/audit-readiness.md`, `docs/security/bug-bounty.md`, `docs/security/supply-chain.md`.

## Development checks

| Command | What it runs |
| --- | --- |
| `go test ./...` | The test suite (CI runs it on Ubuntu, macOS and Windows with Go 1.24.3). |
| `make audit:static` | `go mod tidy`, `golangci-lint run ./...` (linters in `.golangci.yml`), `govulncheck ./...`, `staticcheck ./...`, `buf lint` and `buf breaking --against ".git#branch=main"`, with output copied to `logs/`. |
| `make docs:verify` | Checks documentation code embeds and relative links (`tools/docs/verify.go`). |
| `./scripts/audit.sh` | Runs `make tidy`, `make sdk` (which first runs `make proto` and so needs `buf`) and `make docs:verify` and writes `audit/report-<timestamp>.md` and `.json` and logs under `logs/`. |
| `make bugcheck` | The wider pipeline in `scripts/bugcheck.sh` that CI runs on Linux. |

Other `make audit:*` targets (`tests`, `determinism`, `e2e`, `chaos`, `perf`, `netsec`, `ledger`, `supply`, `config`, `endpoints`) run the matching suites listed in the `Makefile`.

## Contributing and license

Open issues and pull requests on the GitHub repository. Pull requests run `.github/workflows/ci.yml`: a scan for committed PEM private keys, the build and `go test ./...` on Ubuntu, macOS and Windows, and the bugcheck pipeline.

The code is released under the Apache License, Version 2.0 (`LICENSE`, copyright 2025 NHBCoin.com). The license grants a patent license from contributors and does not grant rights to trademarks (Apache License section 6).
