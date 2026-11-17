# NHBChain documentation index

This page summarises the chain as implemented in this repository and links to the
reference documents. Every statement here is taken from the code; file names
point at where to check it.

## Chain basics

* **Transaction chain id:** `0x4e4842` (5130306, ASCII "NHB"); `nhb_sendTransaction`
  rejects any other `chainId` and it is part of the signed hash
  (`core/types/transaction.go`, `rpc/http.go`).
* **Network chain id:** the first 8 bytes of the genesis block hash
  (`core/blockchain.go`), reported by `net_info` and used in the P2P handshake. The
  live network's genesis is `config/genesis.relaunch.json`, whose chain id is
  18346390202490284624. It is embedded in the binary (`config/embed.go`): a node whose
  configured genesis file does not exist writes those bytes there unless autogenesis
  is enabled (`resolveGenesisPath`, `cmd/nhb/main.go`).
* **Assets:** two native assets, NHB and ZNHB, each held in the account as
  `BalanceNHB` / `BalanceZNHB` in wei (18 decimals). Addresses are 20 bytes
  encoded as bech32 with the prefix `nhb` (`znhb` is also a defined prefix,
  `crypto/keys.go`).
* **Consensus:** BFT engine in `consensus/bft`; phase timeouts come from the
  `[consensus]` section (defaults: proposal 2 s, prevote 2 s, precommit 2 s, commit
  4 s, `config/config.go`). A block commits when precommits carry strictly more than
  two thirds of the validator set's voting power, `floor(2T/3) + 1` for a total
  power `T` (`types.QuorumThreshold`, `core/types/quorum.go`, used by the engine, by
  `QuorumCert.Verify` and by `core/sync`). The engine has no fixed block time: the
  pace is set by the local `[consensus] MinBlockInterval` (default 1 s when the key
  is absent, `0s` turns the wait off, at most half the commit timeout; the shipped
  `config.toml` sets `2s`). Everything counted in blocks (epoch length, per-epoch
  emission, and so on) therefore takes a time that depends on that setting; see
  [block cadence](../consensus/block-cadence.md).
* **Storage:** LevelDB (`storage/db.go`); account and module state is kept in a
  trie (`storage/trie`) accessed through `core/state`.
* **Limits:** at most `[global.Blocks] MaxTxs` transactions per block (default
  500) and `[mempool] MaxTransactions` pending transactions (default 4000).

## Binaries

| Binary | Purpose |
| --- | --- |
| `cmd/nhb` | All-in-one node with the JSON-RPC server. |
| `cmd/consensusd`, `cmd/p2pd` | Split deployment: consensus over gRPC, and the P2P server. |
| `cmd/gateway` | HTTP gateway/proxy. |
| `cmd/nhb-cli` | Client CLI: keys, balances, transfers, staking, governance, escrow, identity, POTSO, loyalty and more. |
| `cmd/nhbctl` | Keystore migration (`migrate-keystore`). |
| `cmd/nhb-diag`, `cmd/nhb-supply-audit` | Read-only diagnostics over a node data directory. |
| `cmd/nhb-recovery` | Operator tool for correcting the persisted validator set. |
| `cmd/nhb-snapshot` | Packs, verifies and unpacks snapshots of a node's chain database and reports how far a node is from the network tip (`info`, `refs`, `pack`, `verify`, `extract`, `manifest`, `check-config`, `wait-synced`, `version`). |
| `cmd/swap-audit`, `cmd/english-audit` | Audit report generators (`english-audit` writes `docs/audit/english-latest.md` by default). |
| `services/governd`, `services/lending` / `services/lendingd` | gRPC services for governance and lending. |

Details and ports are in [architecture/services.md](../architecture/services.md).

## Transaction types

All state changes are signed transactions (`types.Transaction`); `type` is the
byte below. Encoding and signing:
[rpc.md](../api/rpc.md#transaction-encoding), [signing](../transactions/signing.md).
Constants are defined in `core/types/transaction.go`.

| Value | Name |
| --- | --- |
| `0x01` | `TxTypeTransfer` |
| `0x02` | `TxTypeRegisterIdentity` |
| `0x03` | `TxTypeCreateEscrow` |
| `0x04` | `TxTypeReleaseEscrow` |
| `0x05` | `TxTypeRefundEscrow` |
| `0x06` | `TxTypeStake` |
| `0x07` | `TxTypeUnstake` |
| `0x08` | `TxTypeHeartbeat` |
| `0x09` | `TxTypeLockEscrow` |
| `0x0A` | `TxTypeDisputeEscrow` |
| `0x0B` | `TxTypeArbitrateRelease` |
| `0x0C` | `TxTypeArbitrateRefund` |
| `0x0D` | `TxTypeStakeClaim` |
| `0x0E` | `TxTypeMint` |
| `0x0F` | `TxTypeSwapPayoutReceipt` |
| `0x10` | `TxTypeTransferZNHB` |
| `0x11` | `TxTypeSwapMint` |
| `0x12` | `TxTypeSwapBurn` |
| `0x13` | `TxTypeLendingSupplyNHB` |
| `0x14` | `TxTypeLendingWithdrawNHB` |
| `0x15` | `TxTypeLendingDepositZNHB` |
| `0x16` | `TxTypeLendingWithdrawZNHB` |
| `0x17` | `TxTypeLendingBorrowNHB` |
| `0x18` | `TxTypeLendingRepayNHB` |
| `0x19` | `TxTypeBuyZNHB` |
| `0x1A` | `TxTypeSetRewardBeneficiary` |
| `0x1B` | `TxTypeRedeemNHB` |
| `0x1C` | `TxTypeAttestRedemption` |
| `0x1D` | `TxTypeLendingLiquidate` |
| `0x1E` | `TxTypeSwapVoucherMint` |
| `0x20` | `TxTypePOSAuthorize` |
| `0x21` | `TxTypePOSCapture` |
| `0x22` | `TxTypePOSVoid` |
| `0x23` | `TxTypePOSRegistry` |
| `0x24` | `TxTypeBuybackAsk` |
| `0x25` | `TxTypeBuybackRefPrice` |
| `0x26` | `TxTypeLendingRefPrice` |
| `0x27` | `TxTypeGovPropose` |
| `0x28` | `TxTypeGovVote` |
| `0x29` | `TxTypeGovFinalize` |
| `0x2A` | `TxTypeGovQueue` |
| `0x2B` | `TxTypeGovExecute` |
| `0x2C` | `TxTypeLendingCreatePool` |
| `0x2D` | `TxTypePotsoStakeLock` |
| `0x2E` | `TxTypePotsoStakeUnbond` |
| `0x2F` | `TxTypePotsoStakeWithdraw` |
| `0x30` | `TxTypeSubscriptionCreatePlan` |
| `0x31` | `TxTypeSubscriptionUpdatePlan` |
| `0x32` | `TxTypeSubscriptionSubscribe` |
| `0x33` | `TxTypeSubscriptionCancel` |
| `0x34` | `TxTypeStakeClaimRewards` |
| `0x35` | `TxTypeMarketCreateListing` |
| `0x36` | `TxTypeMarketFillListing` |
| `0x37` | `TxTypeMarketCancelListing` |
| `0x38` | `TxTypeLendingBorrowFixedTerm` |
| `0x39` | `TxTypeLendingRepayFixedTerm` |
| `0x3A` | `TxTypeLendingSupplyFixedTerm` |
| `0x3B` | `TxTypeExpireEscrow` |
| `0x3C` | `TxTypeDelegatedReleaseEscrow` |
| `0x3D` | `TxTypeDelegatedRefundEscrow` |
| `0x3E` | `TxTypeDelegatedDisputeEscrow` |
| `0x3F` | `TxTypeEscrowCreateRealm` |
| `0x40` | `TxTypeEscrowUpdateRealm` |
| `0x41` | `TxTypeDelegatedCreateEscrow` |
| `0x42` | `TxTypeCreateLoyaltyBusiness` |
| `0x43` | `TxTypeLoyaltySetPaymaster` |
| `0x44` | `TxTypeLoyaltyAddMerchant` |
| `0x45` | `TxTypeLoyaltyRemoveMerchant` |
| `0x46` | `TxTypeCreateLoyaltyProgram` |
| `0x47` | `TxTypeUpdateLoyaltyProgram` |
| `0x48` | `TxTypePauseLoyaltyProgram` |
| `0x49` | `TxTypeResumeLoyaltyProgram` |
| `0x4A` | `TxTypeSwapVoucherReverse` |
| `0x4B` | `TxTypeSwapMarkReconciled` |
| `0x4C` | `TxTypeSubmitEvidence` |

`0x1F` is unassigned. The types `TxTypeMint`, `TxTypeSwapVoucherMint`,
`TxTypeBuybackRefPrice`, `TxTypeLendingRefPrice`, `TxTypeSwapVoucherReverse` and
`TxTypeSwapMarkReconciled` do not require an originator signature:
`RequiresSignature` returns false for them because they originate from module
attestations that carry their own signatures. Every other type, including
`TxTypeSubmitEvidence` (`0x4C`), is a signed native transaction whose sender is
recovered from the signature; an evidence transaction additionally needs the
reporter to have at least the governed minimum validator stake of its own ZNHB
locked (`LockedZNHB`, not stake delegated to it by others;
`requireEvidenceReporterBond`, `core/potso_evidence_tx.go`).

## JSON-RPC surface

One endpoint, method names prefixed by module. The dispatch table is the `switch`
in `handle` (`rpc/http.go`); the prefixes are `nhb_`, `tx_`, `mint_with_sig`,
`swap_`, `znhb_`, `stake_`, `identity_`, `claimable_`, `escrow_`, `gov_`
(read-only), `loyalty_`, `creator_`, `lending_` / `lend_`, `market_`,
`subscriptions_`, `fees_`, `potso_`, `pos_`, `engagement_`, `buyback_`,
`reputation_`, `p2p_`, `net_` and `sync_`. Transport, authentication, rate limits
and error codes: [api/rpc.md](../api/rpc.md). Governance and swap admin:
[api.md](../api.md).

### RPC and transaction safeguards

Config keys read by the RPC server (`config/config.go`):

* `RPCTrustedProxies` and `RPCTrustProxyHeaders`: a forwarded client address
  (`X-Forwarded-For`, `X-Real-IP`) is honoured only when the peer that sent it is
  listed in `RPCTrustedProxies`; the toggle alone trusts nobody. `RPCAllowlistCIDRs`
  limits client addresses. `RPCProxyHeaders` (`XForwardedFor`, `XRealIP`) sets
  whether such a header is accepted (`single`) or refused (`ignore`, the default).
* `RPCMaxTxPerWindow`, `RPCMaxTxPerIP`, `RPCMaxTxPerIdentity`, `RPCMaxTxPerChain`,
  `RPCMaxTxPerIdentityChain` per `RPCRateLimitWindow` seconds (defaults 5 and 60);
  over the limit gives HTTP 429 / `-32020` and increments
  `nhb_rpc_limiter_hits_total`.
* `RPCReadHeaderTimeout`, `RPCReadTimeout`, `RPCWriteTimeout`, `RPCIdleTimeout`
  (10, 15, 15 and 120 seconds in the config file the node writes when none exists;
  a value of 0, as in the repository `config.toml`, sets no timeout),
  `RPCTLSCertFile`, `RPCTLSKeyFile`,
  `RPCTLSClientCAFile`. Without TLS certificates the server refuses to start unless
  `RPCAllowInsecure` is true, and even then plaintext is accepted only on a
  loopback address (`RPCAllowInsecureUnspecified` lets an unspecified bind such as
  `0.0.0.0` count as loopback).
* `RPCJWT` configures the JWT verification behind every privileged method;
  `nhb-cli rpc-token` mints a token for it.
* `RPCQueryMaxConcurrent`, `RPCQueryMaxPerClient`, `RPCQueryQueueDepth`,
  `RPCQueryQueueWaitMS`, `RPCQueryTimeoutSeconds` size the admission pool for the
  public reads that scan or lock state (refusals answer HTTP 429 / `-32020`,
  timeouts HTTP 503 / `-32021`), and `RPCDisableExplorerLoop` stops the background
  explorer snapshot rebuild; see [RPC query limits](../ops/rpc-query-limits.md).
* `RPCWebSocketOrigins`, `RPCWebSocketMaxConnections` (default 256) and
  `RPCWebSocketMaxPerIP` (default 8) bound the WebSocket streams.

## Governance

Governance changes network parameters and roles through proposals.

1. **Propose** (`TxTypeGovPropose`): `param.update` and the other supported kinds;
   `param.update` payloads may only set keys on the `[governance] AllowedParams`
   list. A ZNHB deposit (at least `MinDepositWei`, default `1000e18`) is debited
   from the proposer and locked in escrow.
2. **Vote** (`TxTypeGovVote`): starts immediately at submission and lasts
   `VotingPeriodSeconds` (default 604800). Voting power is the voter's POTSO
   weight from the last processed reward epoch.
3. **Finalize, queue, execute** (`0x29`, `0x2A`, `0x2B`): quorum `QuorumBps`
   (default 2000) and pass threshold `PassThresholdBps` (default 5000) decide the
   outcome; a passed proposal must be queued and waits `TimelockSeconds` (default
   172800) after `voting_end` before it can execute. Events: `gov.proposed`,
   `gov.vote`, `gov.finalized`, `gov.queued`, `gov.executed`.

Details, statuses and payload encodings are in [api.md](../api.md).

## Validators, staking and epochs

* Staking is delegating ZNHB to a validator address (`TxTypeStake`), unbonding
  (`TxTypeUnstake`, then `TxTypeStakeClaim` after the unbonding period) and
  claiming rewards (`TxTypeStakeClaimRewards`). Defaults in `config/config.go`:
  APR 1250 bps, payout period 30 days, unbonding 7 days; all are governed
  parameters (`staking.aprBps`, `staking.payoutPeriodDays`,
  `staking.unbondingDays`).
* A node address is a validator candidate when it is registered
  (`nhb-cli register-validator`, a `TxTypeStake` with the register flag; amount 0
  only flips the flag), is not delegating its own stake to a different validator,
  and its total stake, including ZNHB delegated to it by other wallets, is at
  least `staking.minimumValidatorStake` (governed; default 10,000 ZNHB, a
  `param.update` value may be written bare or as a quoted decimal string). There is
  no separate self-stake requirement. Reward accrual is different: a validator
  does not accrue rewards on stake delegated to it by others.
* A new validator does not sync from genesis: block sync from genesis is not
  supported on the live network. It starts from a verified snapshot of a running
  node's chain database (`cmd/nhb-snapshot`, `scripts/make-snapshot.sh`,
  `scripts/deployvalidator.sh`); see
  [onboarding from a snapshot](../validators/snapshot-onboarding.md) for the
  procedure and the reason.
* Validators must also keep sending heartbeats to remain in the active set. See
  [epochs](./epochs.md) for the weight formula, selection and rotation, and
  [engagement](./engagement-program.md) for heartbeats and the score.

## Identity and username directory

Human-readable aliases, avatars, email discovery and claimables.

* [Identity concepts and state model](../identity/identity.md)
* [JSON-RPC API reference](../identity/identity-api.md)
* [Gateway REST API](../identity/identity-gateway.md)
* [Pay-by-username and email flows](../identity/pay-by-username.md)
* [Avatar specification](../identity/avatars.md)
* [CLI usage (`nhb-cli id`)](../identity/identity-cli.md)
* [Security, privacy and compliance brief](../identity/identity-security-compliance.md)
* [OpenAPI schema](../openapi/identity.yaml), [HTTP examples](../examples/identity)

## Reference documents

Core modules:

* [Escrow and P2P developer guide](../escrow/escrow.md),
  [escrow gateway](../escrow/nhbchain-escrow-gateway.md)
* [Loyalty](../loyalty/loyalty.md), [Staking and delegation](../staking/staking.md)
* [POTSO](../potso/README.md), [Swap](../swap/README.md)

Core reference (this section):

* API: [rpc.md](../api/rpc.md), [governance and swap admin](../api.md),
  [events](../api/events.md), [fees query](../api/fees-query.md),
  [lending gRPC](../api/lending-grpc.md), [POS gateway](../api/gateway-pos.md),
  [POS realtime](../api/pos-realtime.md)
* Transactions: [envelope](../transactions/envelope.md),
  [signing](../transactions/signing.md), [NHB and ZNHB transfers](../transactions/znhb-transfer.md)
* Architecture: [binaries and services](../architecture/services.md)
* Overview: [epochs](./epochs.md), [engagement](./engagement-program.md),
  [P2P](./p2p.md)
* Specs: [NHB Pay intents](../specs/nhb-pay.md),
  [intent fields](../specs/pos-intent-fields.md), [POS lifecycle](../specs/pos-lifecycle.md),
  [POS QoS](../specs/pos-qos.md), [refunds](../specs/refunds.md)
* Development and testing: [protobuf tooling](../dev/proto-style.md),
  [e2e and chaos tests](../testing/e2e.md), [performance measurement](../perf/baselines.md),
  [tuning](../perf/tuning.md)
* Platform overview and service directory: [index](../index.md),
  [services](../services/index.md)

## Building and testing

* `go build ./cmd/nhb` builds the node (this is what CI builds); `go test ./...`
  runs the Go tests.
* `make proto` regenerates protobuf code; `make sdk` runs the SDK tests;
  `make bugcheck` runs the full pre-release check pipeline
  (`scripts/bugcheck.sh`, which exits non-zero when a check fails);
  `make bugcheck-fuzz` runs every fuzz target one at a time, 60 s each by default
  (`scripts/fuzz_all.sh`, `FUZZTIME` overrides); `make docs:verify` checks
  documentation links and embedded snippets (`tools/docs`).
