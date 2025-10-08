# Direct ZNHB Transfer Pause Runbook

Operations can now halt point-to-point ZNHB sends without affecting NHB flows by
setting the `global.pauses.transfer_znhb` flag. When the toggle is active the
state processor rejects `TxTypeTransferZNHB` transactions and emits a
`transfer.znhb.paused` event so downstream systems can track the rejection reason.【F:core/state_transition.go†L1470-L1536】【F:core/events/transfer.go†L14-L56】

## Pause ZNHB transfers

1. Pull the current pause map before making changes:
   ```bash
   go run ./examples/docs/ops/read_pauses \
     --db ./nhb-data \
     --consensus localhost:9090
   ```
   Confirm `transfer_znhb` is present and record the existing value for the
   incident log.【F:examples/docs/ops/read_pauses/main.go†L7-L42】
2. Submit a governance proposal that toggles only the ZNHB transfer flag. The
   helper CLI reads the current map, flips the requested module, and broadcasts
   `gov.v1.MsgSetPauses` to `governd`.
   ```bash
   go run ./examples/docs/ops/pause_toggle \
     --db ./nhb-data \
     --consensus localhost:9090 \
     --governance localhost:50061 \
     --authority nhb1operatorauthority0000000000000000000000 \
     --module transfer_znhb \
     --state pause
   ```
   Capture the transaction hash in the incident channel.【F:examples/docs/ops/pause_toggle/main.go†L13-L118】
3. Re-run the inspection command. The helper should now show
   `transfer_znhb=true`. Watch for the `transfer.znhb.paused` event on your
   telemetry pipeline to confirm the chain is actively rejecting direct ZNHB
   sends.【F:core/events/transfer.go†L38-L56】

## Resume ZNHB transfers

1. Repeat the inspection step to confirm the flag is still set. This prevents
   unintentionally clearing other module pauses.
2. Re-run the helper with `--state resume` to flip the flag back to `false`. The
   state processor will accept `TxTypeTransferZNHB` transactions again while NHB
   transfers remain unaffected throughout the process.【F:core/state_transition.go†L1464-L1527】
3. Notify downstream teams that the pause is lifted once the helper shows
   `transfer_znhb=false` and the pause events stop appearing.
