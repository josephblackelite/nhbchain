# Pause and Quota Operations Runbook

This runbook covers the operator-facing kill switches (`system/pauses`) and per
address quotas enforced by the runtime. Use these procedures when you need to
freeze a module, confirm that the kill switch landed, inspect live quota usage,
or raise caps after coordinating with governance.

## Inspect current pause state

1. Query the live parameter store snapshot through the helper script. It loads
the latest block root, decodes `system/pauses`, and prints the module map.【F:examples/docs/ops/read_pauses/main.go†L7-L42】

   ```bash
   go run ./examples/docs/ops/read_pauses \
     --db ./nhb-data \
     --consensus localhost:9090
   ```

2. Record the output in the incident channel so responders know which modules
   are paused. When the store does not contain an override, all modules are
   active.

## Pause or resume a module

1. Confirm the desired state using the previous step. `SetPauses` overwrites the
   entire map, so starting from an up-to-date snapshot prevents accidental
   unpauses.【F:services/governd/server/server.go†L158-L170】
2. Submit the governance transaction through the helper CLI. It reads the
   current map, toggles the selected module, and broadcasts
   `gov.v1.MsgSetPauses` to `governd`. Capture the transaction hash in the
   incident log.【F:examples/docs/ops/pause_toggle/main.go†L13-L97】

   ```bash
   go run ./examples/docs/ops/pause_toggle \
     --db ./nhb-data \
     --consensus localhost:9090 \
     --governance localhost:50061 \
     --authority nhb1operatorauthority0000000000000000000000 \
     --module swap \
     --state pause
   ```

3. Re-run the inspection command to verify the pause state propagated. To resume
   a module replace `--state pause` with `--state resume`.

> **ZNHB redemption tip:** Disabling cash-outs cleanly requires pausing both the on-chain
> swap module (`global.pauses.swap`) and the `swapd` stable engine (`stable.paused` in
> the YAML). Pause the service first to drain in-flight requests, then toggle the
> on-chain flag. When restoring service, unpause `swapd` only after the governance
> update clears the module pause so submitted redemptions will execute successfully.

### Pause playbooks (mints vs. redemptions)

* **Pause minting:** run the helper above with `--module swap --state pause` and
  capture the transaction hash for the incident log.
* **Pause redemptions:** flip `stable.paused=true` in the active swapd overlay
  while leaving the mint flag untouched. For example:

  ```bash
  yq -i '.stable.paused = true' deploy/environments/prod/swapd.yaml
  kubectl rollout restart deployment swapd -n treasury
  ```

  The restart ensures the new flag propagates. Reverting to `stable.paused=false`
  re-opens redemptions once the on-chain pause clears.
* **Observe toggles:** combine the consensus snapshot with the swapd status
  endpoint to confirm the desired state landed:

  ```bash
  go run ./examples/docs/ops/swap_pause_inspect \
    --db ./nhb-data \
    --consensus localhost:9090 \
    --swapd https://swapd.internal.nhb
  ```

  The helper prints `global.pauses.swap` and whether `/v1/stable/status` is
  returning `501 stable engine not enabled` (paused) or live counters (active).

## Inspect quota usage for an address

1. Determine the module, target address, and quota epoch window. The helper
   script normalises the module name, derives the current epoch from the block
   timestamp, and loads the counters stored under `quotas/<module>/<epoch>/<addr>`.【F:examples/docs/ops/quota_dump/main.go†L7-L63】【F:native/system/quotas/store.go†L18-L74】

   ```bash
   go run ./examples/docs/ops/quota_dump \
     --db ./nhb-data \
     --consensus localhost:9090 \
     --module swap \
     --address nhb1customeraddress000000000000000000000 \
     --epoch-seconds 3600
   ```

2. Share the `requests used` and `nhb used` figures with the requester. If the
   script reports no counters, the address has not consumed quota in the current
   epoch.

## Raise module caps safely

1. Stage a config overlay that increases the target module’s quota. The example
   below raises the swap request ceiling to 30/min while keeping the NHB cap
   disabled. Keep the overlay under version control alongside the change ticket.【F:config/types.go†L41-L59】【F:cmd/consensusd/main.go†L220-L227】

   ```bash
   cp config.toml /tmp/quotas-override.toml
   cat <<'EOCFG' >> /tmp/quotas-override.toml
   [global.quotas.swap]
   MaxRequestsPerMin = 30
   MaxNHBPerEpoch = 0
   EpochSeconds = 60
   EOCFG
   ```

2. Validate the override locally before deploying. `consensusd` will abort if
   any invariants are violated. Use `Ctrl+C` once you see the “initialised and
   running” banner.【F:cmd/consensusd/main.go†L220-L235】

   ```bash
   go run ./cmd/consensusd --config /tmp/quotas-override.toml --genesis ./config/genesis.json
   ```

3. Roll the change through staging, post the diff and test logs in the change
   channel, and coordinate the production restart. After restart, re-run the
   quota inspection command to confirm the new cap is in effect.
