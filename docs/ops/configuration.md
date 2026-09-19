# Runtime Configuration Guardrails

`cmd/consensusd` loads `config.toml` with `config.Load`, fills in defaults, then
calls `config.ValidateConfig` on the `[global]` block before opening the state
database. A violation stops the process. Sources: `config/config.go`,
`config/validate.go`, `cmd/consensusd/main.go`. `cmd/nhb` (the binary the
`nhb.service` unit runs) loads the same file with `config.Load` but does not call
`ValidateConfig` or `ValidateConsensus`, so the checks below are not applied at
its startup.

## Key spelling matters

`config.Load` decodes TOML with BurntSushi/toml and then restores a default for
every `[global]` key that `meta.IsDefined` does not find. `IsDefined` compares
key names exactly, so the section and key names must use the Go spelling
(`[global.Governance]`, `QuorumBPS`, `[global.Fees]`, `OwnerWallet`,
`[global.Loyalty.Dynamic]`, `EnableProRate`, and so on, as in the repository's
`config.toml`). A key spelled differently (for example `owner_wallet` or
`minbps`) is either not decoded or is overwritten by the built-in default. Keys
that are strings (for example `MinStakeWei`, `OwnerWallet`) are defaulted when
empty rather than when absent.

## Defaults for `[global]`

From `defaultGlobalConfig` in `config/config.go`:

| Key | Default |
| --- | ------- |
| `Governance.QuorumBPS` / `PassThresholdBPS` / `VotingPeriodSecs` | `6000` / `5000` / `604800` |
| `Slashing.MinWindowSecs` / `MaxWindowSecs` | `60` / `600` |
| `Mempool.MaxBytes` / `POSReservationBPS` | `16777216` (16 MiB) / `1500` |
| `Blocks.MaxTxs` | `500` |
| `Staking.AprBps` / `PayoutPeriodDays` / `UnbondingDays` | `1250` / `30` / `7` |
| `Staking.MinStakeWei` / `MaxEmissionPerYearWei` | `10000000000000000000000` / `5000000000000000000` |
| `Staking.RewardAsset` / `CompoundDefault` | `ZNHB` / `false` |
| all `Pauses` | `false` |

If `Slashing.MaxWindowSecs` is below `MinWindowSecs` after loading, it is raised
to `MinWindowSecs`.

## Boot-time validation

`ValidateConfig` returns the first failing check, in this order (the first
column is the exact error text):

| Error | Condition |
| ----- | --------- |
| `governance: quorum_bps < pass_threshold_bps` | `QuorumBPS < PassThresholdBPS` |
| `governance: voting_period_seconds too small` | `VotingPeriodSecs < 3600` |
| `slashing: min_window > max_window or zero` | `MinWindowSecs == 0` or `> MaxWindowSecs` |
| `mempool: max_bytes <= 0` | `Mempool.MaxBytes <= 0` |
| `mempool: pos_reservation_bps > 10000` | `POSReservationBPS > 10000` |
| `blocks: max_txs <= 0` | `Blocks.MaxTxs <= 0` |
| `staking: apr_bps must be <= 10000` | `AprBps > 10000` |
| `staking: payout_period_days must be >= 1` | `PayoutPeriodDays == 0` |
| `staking: unbonding_days must be >= 1` | `UnbondingDays == 0` |
| `staking: min_stake_wei ...`, `staking: max_emission_per_year_wei ...` | non-empty value that is not a base-10 integer, or is negative |
| `staking: reward_asset must not be empty` | `RewardAsset` empty |
| `paymaster: ...` | a paymaster cap that does not parse |
| `fees: ...` | bad `TransferFreeTierSpendWei`, `TransferFreeTierWindow` not `lifetime`/`monthly`, or ZNHB fees enabled without a ZNHB route wallet |
| `loyalty.dynamic: ...`, `loyalty.dynamic.price_guard: ...` | band, cap and price-guard bounds in `ValidateConfig` |

`ValidateConsensus` separately requires the four `[consensus]` timeouts
(`ProposalTimeout`, `PrevoteTimeout`, `PrecommitTimeout`, `CommitTimeout`) to be
positive after defaults (`2s`, `2s`, `2s`, `4s`) and after the
`--consensus-timeout-*` flags and `NHB_CONSENSUS_TIMEOUT_*` variables are applied.

`cmd/consensusd` reports a failure with `log.Fatal("invalid configuration",
"err", err)`. Because the standard-library logger concatenates those operands,
the line reads `invalid configurationerr<message>` followed by exit status 1.

### Reproducing a validation error

`config.toml` in the repository root already defines `[global.Governance]` and
the other sections, so edit values in a copy rather than appending duplicate
tables (a duplicate table is a TOML error). `config.Load` also needs the
validator keystore; run from a scratch directory with `NHB_VALIDATOR_PASS` set so
it can create `validator.keystore` there.

```bash
go build -o /tmp/consensusd ./cmd/consensusd
mkdir -p /tmp/cfgtest && cp config.toml /tmp/cfgtest/config.toml && cd /tmp/cfgtest
sed -i 's/QuorumBPS = 6000/QuorumBPS = 4000/' config.toml
NHB_VALIDATOR_PASS=test-passphrase /tmp/consensusd --config ./config.toml \
  --genesis <repo>/config/genesis.json
```

The run stops at the governance check. Restore `QuorumBPS = 6000` and change
`VotingPeriodSecs` (to below `3600`), `MinWindowSecs`/`MaxWindowSecs`,
`[global.Mempool] MaxBytes` or `[global.Blocks] MaxTxs` to see the next error.

## Block and mempool limits

- `Blocks.MaxTxs`: `CreateBlock` truncates its proposal to this many
  transactions, leaving the rest in the mempool. A received block with more
  transactions is rejected by `ValidateBlock` and `commitBlock` with
  `block exceeds max transaction count: got N want <= M`
  (`core/node.go`, `rejectOversizedBlock`).
- `[mempool] MaxTransactions`: when omitted or `<= 0`, the node uses `4000`
  (`config.DefaultMempoolMaxTransactions`). An unbounded pool needs both
  `AllowUnlimited = true` and `MaxTransactions = 0`
  (`ensureMempoolDefaults`, `Node.SetMempoolLimit`). When the pool already holds
  more than the limit, the oldest entries are dropped.

## Pauses and quotas

`[global.Pauses]` has twelve flags: `Lending`, `Swap`, `Escrow`, `Trade`,
`Loyalty`, `POTSO`, `TransferNHB`, `TransferZNHB`, `Staking`, `Subscriptions`,
`SwapRedeem` and `Market` (`config/types.go`, `Node.SetModulePauses`).

The node applies the config values at start (`cmd/nhb/main.go` and
`cmd/consensusd/main.go`, `Node.SetModulePauses`), but it re-reads the pause flags from the on-chain
parameter store key `system/pauses` (`refreshModulePauses`, `core/node.go`) on
transaction validation, block creation, block validation and commit. An unset
on-chain value reads as no pauses, so after the first refresh the on-chain value,
not `[global.Pauses]`, is what the modules enforce. If `Staking` is unpaused in
the config while the on-chain flag is paused, `consensusd` logs a warning at
start and keeps enforcing the on-chain value. `cmd/nhb` logs the same warning
(`cmd/nhb/main.go`, `StakingPauseOnChain`).

`governd` exposes `gov.v1.Msg/SetPauses` (`services/governd/server/server.go`),
which wraps the message in a signed envelope and submits it to `consensusd`.
That envelope path (`consensus/codec/codec.go`, `transactionFromModulePayload`)
accepts only `consensus.v1.Transaction`, swap payout receipt and POS payloads;
any other type, including `gov.v1` messages, is rejected with
`envelope: unsupported module payload type`. No production code path that writes
`system/pauses` was found, so treat pause changes through this route as
unverified until tested against a running network.

`[global.Quotas.<Module>]` sets `MaxRequestsPerMin`, `MaxNHBPerEpoch` and
`EpochSeconds` for `Lending`, `Swap`, `Escrow`, `Trade`, `Loyalty`, `POTSO`,
`Subscriptions` and `Market`. A module whose `MaxRequestsPerMin` and
`MaxNHBPerEpoch` are both `0` is not limited. Counters are stored in chain state
(`native/system/quotas`). When a limit is hit the state processor emits a
`QuotaExceeded` event with attributes `module`, `epoch`, `reason`
(`requests`, `nhb` or `overflow`) and, when known, `address`
(`core/state_transition.go`, `emitQuotaExceeded`).

See the [Pause and quota runbook](../runbooks/pause-and-quotas.md) for the
operational steps.

## Reviewing the effective configuration

There is no command that dumps the effective configuration. The values a node
loaded are the file contents plus the defaults listed above; compare deployed
`config.toml` files against the version-controlled template with a normal text
diff.
