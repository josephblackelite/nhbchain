# Direct Transfer Pause Runbook

The module pause map has two flags that stop point-to-point transfers without touching
other modules: `transfer_nhb` (config field `TransferNHB`) and `transfer_znhb`
(`TransferZNHB`). This runbook describes what the node does when they are set and how to
observe them. How the pause map is stored and read is described in
[Pause and quota operations](./pause-and-quotas.md).

## What a set flag does

* `transfer_nhb`: the state processor rejects `TxTypeTransfer` with the error
  `nhb transfer: paused` and appends a `transfer.nhb.paused` event
  (`core/state_transition.go`, `ErrTransferNHBPaused`).
* `transfer_znhb`: the state processor rejects `TxTypeTransferZNHB` with the error
  `znhb transfer: paused` and appends a `transfer.znhb.paused` event
  (`ErrTransferZNHBPaused`).
* Only the matching transaction type is affected. The two flags are independent.
* The blocked-transfer events are kept when the transaction is rejected for this reason;
  events of other rejected transactions are discarded, except those of a refused sponsorship and
  the `stake.paused` event of a paused staking module (`executeTransaction`,
  `core/state_transition.go`).

Event attributes (`core/events/transfer.go`): `asset`, `from`, `to` (bech32 addresses),
`reason` (the value `paused by governance`) and `txHash`, each present only when known.

## Inspect the current state

The helper `examples/docs/ops/read_pauses` opens the node's data directory, takes the state
root from the latest block (fetched over the consensus gRPC endpoint), and prints the
`system/pauses` entry:

```bash
go run ./examples/docs/ops/read_pauses --db ./nhb-data --consensus localhost:9090
```

Both flags shown are its defaults. It prints `no pause overrides set (all modules
active)` when the entry does not exist. Otherwise it lists `lending`, `swap`, `escrow`,
`trade`, `loyalty`, `potso`, `transfer_nhb`, `transfer_znhb` and `staking`. It does not
print the `Subscriptions`, `SwapRedeem` or `Market` flags that `config.Pauses` also has.

Watch for the `transfer.nhb.paused` and `transfer.znhb.paused` events in your event
pipeline to confirm the chain is rejecting transfers.

## Changing the flags

The node enforces the value stored under `system/pauses` in chain state. Read the
"Changing pauses" section of [Pause and quota operations](./pause-and-quotas.md) before
relying on any helper to set it: the helper `examples/docs/ops/pause_toggle` builds a
`gov.v1.MsgSetPauses` message whose payload carries only `lending`, `swap`, `escrow`,
`trade`, `loyalty` and `potso`, and the node does not accept that message type.
