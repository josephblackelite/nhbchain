# Runtime Configuration Guardrails

Operations teams configure NHB consensus nodes through `config.toml` and the
nested `[global.*]` runtime overrides. The binary performs a series of invariant
checks before it opens the state database. Invalid settings terminate the boot
sequence so you can correct the issue before a validator starts gossiping bad
state.【F:config/validate.go†L9-L21】【F:cmd/consensusd/main.go†L65-L83】

## Boot-time validation failures

`consensusd` validates the governance, slashing, mempool, and block limit knobs
before it touches the chain database:

- **Governance:** quorum must be greater than or equal to the pass threshold and
the voting period must be at least one hour.【F:config/validate.go†L9-L13】
- **Slashing:** the minimum evaluation window must be non-zero and less than or
  equal to the maximum window.【F:config/validate.go†L14-L16】
- **Mempool:** the global byte cap must be positive.【F:config/validate.go†L17-L18】
- **Blocks:** each block must allow at least one transaction.【F:config/validate.go†L19-L20】

Nodes clamp the per-validator mempool to 4,000 transactions when the `[mempool]`
section omits `MaxTransactions` or sets it to a non-positive value. Operators
who truly need an unbounded queue must opt in explicitly by setting
`AllowUnlimited = true` and `MaxTransactions = 0`; all other configurations fall
back to the default ceiling.【F:config/config.go†L107-L109】【F:config/config.go†L399-L408】

### Reproducing and fixing a validation error

1. Copy the shipping config and append invalid overrides that violate the
   invariants.

   ```bash
   cp config.toml /tmp/invalid-config.toml
   cat <<'EOCFG' >> /tmp/invalid-config.toml
   [global.governance]
   QuorumBPS = 4000
   PassThresholdBPS = 5000
   VotingPeriodSecs = 1800

   [global.slashing]
   MinWindowSecs = 120
   MaxWindowSecs = 60

   [global.mempool]
   MaxBytes = 0

   [global.blocks]
   MaxTxs = 0
   EOCFG
   ```

2. Start `consensusd` with the broken file to see the boot failure. The process
   aborts before opening the database and prints the first invariant violation.

   ```bash
   go run ./cmd/consensusd --config /tmp/invalid-config.toml --genesis ./config/genesis.json
   ```

   Example output:

   ```
   2024/04/03 12:00:00 invalid configuration err="governance: quorum_bps < pass_threshold_bps"
   exit status 1
   ```

3. Repair the overrides and retry. The commands below restore the quorum,
   voting-period, and resource limits before re-running the node.

   ```bash
   sed -i 's/QuorumBPS = 4000/QuorumBPS = 6000/' /tmp/invalid-config.toml
   sed -i 's/VotingPeriodSecs = 1800/VotingPeriodSecs = 604800/' /tmp/invalid-config.toml
   sed -i 's/MaxWindowSecs = 60/MaxWindowSecs = 600/' /tmp/invalid-config.toml
   sed -i 's/MaxBytes = 0/MaxBytes = 33554432/' /tmp/invalid-config.toml
   sed -i 's/MaxTxs = 0/MaxTxs = 5000/' /tmp/invalid-config.toml
   go run ./cmd/consensusd --config /tmp/invalid-config.toml --genesis ./config/genesis.json
   ```

   When the invariants pass, the node continues initialisation and begins wiring
   the consensus, governance, and swap modules.【F:cmd/consensusd/main.go†L83-L229】

## Pauses, quotas, and emergency levers

The runtime exposes a pair of safety nets that operators can use when a module
misbehaves or traffic exceeds contracted levels:

- **Module pauses (kill switches):** `config.Pauses` tracks per-module kill
  switches for lending, swap, escrow, trade, loyalty, and POTSO. Setting a flag
  to `true` pauses new state transitions for that module until governance
  re-enables it.【F:config/types.go†L23-L39】【F:core/node.go†L328-L347】
- **Per-address quotas:** `config.Quotas` defines the request and NHB spend
  ceilings applied to each module. The consensus node loads the configured
  values at boot and enforces them during transaction admission.【F:config/types.go†L41-L59】【F:cmd/consensusd/main.go†L220-L227】【F:core/state_transition.go†L186-L233】

Module pauses are governed via the `gov.v1.Msg/SetPauses` RPC, so include the
entire pause map when you submit a toggle to avoid unintentionally resuming a
module that should stay halted.【F:services/governd/server/server.go†L158-L170】
Per-address quota counters live in the parameter store and emit `QuotaExceeded`
block events when a client breaches either ceiling.【F:native/system/quotas/store.go†L18-L74】【F:core/state_transition.go†L203-L233】

See [Pause and quota runbook](../runbooks/pause-and-quotas.md) for operational
recipes, including copy-paste commands that dump the live pause state, submit a
pause transaction, inspect quota usage, and stage a safe cap increase.

## Change management

- Stage configuration changes in staging using the same templates as production.
- Submit configuration diffs through version control; avoid editing files directly on servers.
- Require dual approval for parameter changes impacting governance, slashing, or block production.

## Drift detection

- Export effective runtime configuration weekly using `nhbchain config dump` and store in the configuration repository.
- Compare dumps against the committed templates using `git diff` or `terraform plan` (for infrastructure-managed configs).
- Alert when critical fields (pauses, quotas, peer limits) diverge from approved values.

## Emergency procedures

1. Document the triggering event and the configuration change requested.
2. Take a snapshot or backup of the current configuration files.
3. Apply the minimal necessary change and record the command history.
4. Notify governance and security within 24 hours, and file a post-incident review.
