# Direct Transfer Pause Runbook

Operations can now halt point-to-point NHB or ZNHB sends without affecting other
flows by setting the `global.pauses.transfer_nhb` or `global.pauses.transfer_znhb`
flags. When either toggle is active the state processor rejects the matching
transaction type and emits a dedicated `transfer.nhb.paused` or
`transfer.znhb.paused` event so downstream systems can track the rejection
reason.【F:core/state_transition.go†L1196-L1200】【F:core/state_transition.go†L1489-L1492】【F:core/events/transfer.go†L12-L78】

## Pause NHB transfers

1. Pull the current pause map before making changes:
   ```bash
   go run ./examples/docs/ops/read_pauses \
     --db ./nhb-data \
     --consensus localhost:9090
   ```
   Confirm `transfer_nhb` is present and record the existing value for the
   incident log.【F:examples/docs/ops/read_pauses/main.go†L7-L54】
2. Submit a governance proposal that toggles only the NHB transfer flag. The
   helper CLI reads the current map, flips the requested module, and broadcasts
   `gov.v1.MsgSetPauses` to `governd`.
   ```bash
   go run ./examples/docs/ops/pause_toggle \
     --db ./nhb-data \
     --consensus localhost:9090 \
     --governance localhost:50061 \
     --authority nhb1operatorauthority0000000000000000000000 \
     --module transfer_nhb \
     --state pause
   ```
   Capture the transaction hash in the incident channel.【F:examples/docs/ops/pause_toggle/main.go†L13-L135】
3. Re-run the inspection command. The helper should now show
   `transfer_nhb=true`. Watch for the `transfer.nhb.paused` event on your
   telemetry pipeline to confirm the chain is actively rejecting direct NHB
   sends.【F:core/events/transfer.go†L38-L78】

## Resume NHB transfers

1. Repeat the inspection step to confirm the flag is still set. This prevents
   unintentionally clearing other module pauses.
2. Re-run the helper with `--state resume` to flip the flag back to `false`. The
   state processor will accept `TxTypeTransfer` transactions again while ZNHB
   transfers remain unaffected throughout the process.【F:core/state_transition.go†L1196-L1204】
3. Notify downstream teams that the pause is lifted once the helper shows
   `transfer_nhb=false` and the pause events stop appearing.

## Pause ZNHB transfers

1. Pull the current pause map before making changes and confirm
   `transfer_znhb` is present.【F:examples/docs/ops/read_pauses/main.go†L7-L54】
2. Submit a governance proposal that toggles only the ZNHB transfer flag:
   ```bash
   go run ./examples/docs/ops/pause_toggle \
     --db ./nhb-data \
     --consensus localhost:9090 \
     --governance localhost:50061 \
     --authority nhb1operatorauthority0000000000000000000000 \
     --module transfer_znhb \
     --state pause
   ```
   Capture the transaction hash in the incident channel.【F:examples/docs/ops/pause_toggle/main.go†L13-L135】
3. Re-run the inspection command. The helper should now show
   `transfer_znhb=true`. Watch for the `transfer.znhb.paused` event on your
   telemetry pipeline to confirm the chain is actively rejecting direct ZNHB
   sends.【F:core/events/transfer.go†L56-L78】

## Resume ZNHB transfers

1. Repeat the inspection step to confirm the flag is still set.
2. Re-run the helper with `--state resume` to flip the flag back to `false`. The
   state processor will accept `TxTypeTransferZNHB` transactions again while NHB
   transfers remain unaffected throughout the process.【F:core/state_transition.go†L1489-L1528】
3. Notify downstream teams that the pause is lifted once the helper shows
   `transfer_znhb=false` and the pause events stop appearing.
