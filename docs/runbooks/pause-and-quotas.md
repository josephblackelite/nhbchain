# Pause and Quota Operations Runbook

This runbook covers the module pause map and the per-sender request quotas the state
processor enforces.

## How pauses work

* **Where the state lives.** The pause map is the parameter-store entry `system/pauses`, a
  JSON encoding of `config.Pauses` (`native/params/store.go`, `config/types.go`). The flags
  are `Lending`, `Swap`, `Escrow`, `Trade`, `Loyalty`, `POTSO`, `TransferNHB`,
  `TransferZNHB`, `Staking`, `Subscriptions`, `SwapRedeem` and `Market`. When the entry does
  not exist every flag is false.
* **What is enforced.** The node loads the entry from state when it is constructed and again
  every time it creates, validates or commits a block (`refreshModulePauses` in
  `core/node.go`). The comment on `StakingPauseOnChain` in the same file calls this value the
  only source `nativecommon.Guard` enforces.
* **Config file.** `[global.Pauses]` in `config.toml` is applied at process start
  (`SetModulePauses` in `cmd/nhb/main.go` and `cmd/consensusd/main.go`), but the in-memory
  map is replaced by the state value at the next reload, so the config file does not keep a
  module paused. At startup both binaries log a warning when the config says staking is
  unpaused while the state says paused.
* **Per module.** The direct transfer flags are described in
  [Direct transfer pauses](./pauses.md), staking in [Staking operations](./staking-ops.md)
  and loyalty in [Loyalty operations](./loyalty-ops.md).

## Inspect the current pause state

```bash
go run ./examples/docs/ops/read_pauses --db ./nhb-data --consensus localhost:9090
```

`--db` is the node data directory and `--consensus` is the consensus gRPC endpoint (both
flags are shown with their defaults). The helper reads the latest block over gRPC, opens the
state at that block's state root and prints the `system/pauses` entry. It prints
`no pause overrides set (all modules active)` when the entry is absent, and otherwise the
nine flags `lending`, `swap`, `escrow`, `trade`, `loyalty`, `potso`, `transfer_nhb`,
`transfer_znhb` and `staking` (not `Subscriptions`, `SwapRedeem` or `Market`).

## Changing pauses

Read this section before planning a pause. What the code in this repository provides:

* `params.Store.SetPauses` (`native/params/store.go`) writes `system/pauses`. Its only
  callers here are tests.
* A governance `param.update` proposal cannot write it. `system/pauses` is not in the default
  allow-list (`defaultAllowedGovernanceParams` in `config/config.go`) and the governance
  engine rejects any key that has no validation rule (`validateParamPayload` in
  `native/governance/engine.go`).
* `examples/docs/ops/pause_toggle` reads the current map, flips one module and sends a
  `gov.v1.MsgSetPauses` message to `governd` (`--governance`, default `localhost:50061`;
  `--authority` is required; `--module` is one of `lending`, `swap`, `escrow`, `trade`,
  `loyalty`, `potso`, `transfer_nhb`, `transfer_znhb`, `staking`; `--state` is `pause` or
  `resume`). Two limits apply. The `gov.v1.Pauses` message defined in `proto/gov/v1/tx.proto`
  has only `lending`, `swap`, `escrow`, `trade`, `loyalty` and `potso`, and the helper copies
  only those six into it, so the transfer and staking flags are never sent. And `governd`
  wraps the message in a signed consensus envelope that the node cannot decode:
  `TransactionFromEnvelope` (`consensus/codec/codec.go`) accepts `consensus.v1.Transaction`,
  the swap payout receipt and the POS messages, and returns `envelope: unsupported module
  payload type` for anything else.

So no transaction path in this repository changes the pause map of a running chain. Treat any
procedure that assumes one as unverified until that changes.

## Inspect quota usage for an address

Quota counters are stored under the state key `quotas/<module>/<epoch>/<address hex>` and
hold a request count and an NHB amount (`native/system/quotas`). The helper computes the
epoch from the latest block timestamp and `--epoch-seconds`:

```bash
go run ./examples/docs/ops/quota_dump \
  --db ./nhb-data --consensus localhost:9090 \
  --module swap --address <bech32 address> --epoch-seconds 60
```

`--module` and `--address` are required. `--epoch` overrides the computed epoch and
`--epoch-seconds` defaults to `60`. The output shows `requests used` and `nhb used`. When
no counters exist it prints `no counters recorded for this address in the selected epoch`.
The `--epoch-seconds` value must equal the module's configured `EpochSeconds` (or `60` when
that is `0`) for the epoch number to match the node's.

## How quotas work

* **Configuration.** One table per module under `[global.Quotas]` in `config.toml`:
  `Lending`, `Swap`, `Escrow`, `Trade`, `Loyalty`, `POTSO`, `Subscriptions` and `Market`.
  Each has `MaxRequestsPerMin`, `MaxNHBPerEpoch` and `EpochSeconds` (`config.Quota`). They
  are read at process start (`SetModuleQuotas` in `cmd/nhb/main.go` and
  `cmd/consensusd/main.go`).
* **What is counted.** The limit is checked per transaction sender, for the transaction
  types of that module, one request per transaction (`applyQuota` in
  `core/state_transition.go`). A module with `MaxRequestsPerMin = 0` and
  `MaxNHBPerEpoch = 0` is not limited.
* **The window.** The counter resets per epoch, where the epoch is
  `block timestamp / EpochSeconds` (`EpochSeconds = 0` means `60`). `MaxRequestsPerMin` is
  compared with the count inside one epoch, so it only means "per minute" when
  `EpochSeconds` is `60`.
* **`MaxNHBPerEpoch` is inert.** Every `applyQuota` call passes an NHB amount of `0`, so this
  field never limits anything.
* **On a breach.** The transaction fails with `quota: <module>: quota requests exceeded` and
  the node appends a `QuotaExceeded` event with `module`, `epoch`, `reason` (`requests`,
  `nhb` or `overflow`) and `address`.

## Raise a module cap

1. Edit the existing `[global.Quotas.<Module>]` table in the node's `config.toml`. Do not
   append a second table with the same name: the file would then define the table twice.
   For example, to allow 30 swap requests per 60-second epoch:

   ```toml
   [global.Quotas.Swap]
     MaxRequestsPerMin = 30
     MaxNHBPerEpoch = 0
     EpochSeconds = 60
   ```

2. Restart the node (`nhb` or `consensusd`) so it re-reads the file. `consensusd` runs
   `config.ValidateConfig` on the `[global]` section at startup and refuses to start on a
   problem; `nhb` logs each problem as a warning and starts (`cmd/nhb/config_check.go`). The
   quota tables are not among the checks in either case. `consensusd` prints
   `--- Consensus node initialised and running ---` once it is up.
3. Use the `quota_dump` helper above to confirm counters follow the new epoch length.
