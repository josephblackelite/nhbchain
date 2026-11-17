# Engagement heartbeats and score

Each account carries an engagement score. It is fed by heartbeat transactions and
by counters incremented when the account sends other transactions, rolled up once
per UTC day into an exponentially weighted moving average (EMA). The score is one
input to the validator weight in [epochs](./epochs.md) and is returned as
`engagementScore` by `nhb_getBalance`. Code: `core/engagement/`,
`core/state_transition.go` (`applyHeartbeat`, `recordEngagementActivity`,
`rolloverEngagement`), `core/events/engagement.go`, `rpc/engagement_handlers.go`.

## Parameters

`engagement.Config` (`core/engagement/config.go`), default values from
`DefaultConfig()`:

| Field | Default | Meaning |
| --- | --- | --- |
| `HeartbeatWeight` | 1 | Weight per heartbeat minute. |
| `TxWeight` | 5 | Weight per counted transaction. |
| `EscrowWeight` | 10 | Weight per escrow event. |
| `GovWeight` | 20 | Weight per "gov" event (see counters below). |
| `DailyCap` | 250 | Maximum raw score credited per day. |
| `LambdaNumerator` / `LambdaDenominator` | 4 / 5 | EMA decay factor. |
| `HeartbeatInterval` | 1 minute | Minimum spacing between heartbeats. |
| `MaxMinutesPerHeartbeat` | 5 | Cap on minutes credited by one heartbeat. |

`Validate` requires a non-zero denominator, numerator not above denominator, a
positive interval and a positive minute cap. The state processor starts from
`DefaultConfig()`; no `config.toml` key sets it, and the shipped binaries do not
call `SetEngagementConfig`. Every validator must use identical values, because
the score feeds consensus state.

## Heartbeat transaction

`TxTypeHeartbeat` (`0x08`) with `data` = JSON `{"deviceId": "...", "timestamp":
N}` (`types.HeartbeatPayload`). `applyHeartbeat`:

* If `timestamp` is 0 it uses the block timestamp (not wall-clock time).
* If the account has a previous heartbeat, `timestamp` must be greater than it
  (`heartbeat replay detected`) and at least `HeartbeatInterval` after it
  (`heartbeat rate limited`); both wrap `ErrHeartbeatTooSoon`.
* Minutes credited = whole minutes since the previous heartbeat (at least 1; 1 for
  the first heartbeat), capped at `MaxMinutesPerHeartbeat`.
* Adds the minutes to the day's counter, stores the timestamp as
  `EngagementLastHeartbeat`, increments the nonce, and emits
  `engagement.heartbeat`.

## Counters incremented by other transactions

`recordEngagementActivity` adds to the sender's per-day counters after these
transaction types apply (`handleNativeTransaction` and the transfer paths in
`core/state_transition.go`):

| Counters incremented | Transaction types |
| --- | --- |
| transaction counter only | NHB and ZNHB transfers, `TxTypeRegisterIdentity`, `TxTypeLendingSupplyNHB`, `TxTypeLendingWithdrawNHB`, `TxTypeLendingDepositZNHB`, `TxTypeLendingWithdrawZNHB`, `TxTypeLendingBorrowNHB`, `TxTypeLendingRepayNHB`, `TxTypeLendingLiquidate`, `TxTypeLendingBorrowFixedTerm`, `TxTypeLendingRepayFixedTerm`, `TxTypeLendingSupplyFixedTerm` |
| transaction and escrow counters | `TxTypeCreateEscrow`, `TxTypeReleaseEscrow`, `TxTypeRefundEscrow`, `TxTypeLockEscrow`, `TxTypeDisputeEscrow`, `TxTypeArbitrateRelease`, `TxTypeArbitrateRefund`, `TxTypeSwapBurn` |
| transaction and "gov" counters | `TxTypeStake`, `TxTypeUnstake`, `TxTypeStakeClaim`, `TxTypeStakeClaimRewards` |

Other types (heartbeat, POS, loyalty, subscriptions, delegated escrow actions,
governance transactions, and so on) do not call `recordEngagementActivity`. The
transaction and escrow counters also feed the POTSO meters. No transaction type
in this code updates the "gov" counter other than the four staking types listed.

## Daily rollover and score

When an account's stored day differs from the day of the current activity, each
elapsed day is closed in order (`rolloverEngagement`):

```
raw   = min(DailyCap, minutes*HeartbeatWeight + txs*TxWeight
                      + escrowEvents*EscrowWeight + govEvents*GovWeight)
score = (score * LambdaNumerator + raw * (LambdaDenominator - LambdaNumerator))
        / LambdaDenominator            (integer division)
```

Then the day's counters reset. Days are UTC (`2006-01-02`). Days with no activity
are still closed with `raw = 0` the next time the account is touched, so the score
decays on the next activity. Each closed day emits `engagement.score_updated`.

## Events

* `engagement.heartbeat`: `address` (bech32), `device_id`, `minutes`, `timestamp`.
* `engagement.score_updated`: `address`, `day`, `raw`, `old_score`, `new_score`.

## Device registration and the RPC methods

The RPC methods below require auth ([rpc.md](../api/rpc.md#authentication)).

* `engagement_register_device`: params `[{"address": "<bech32>", "deviceId":
  "<id>"}]`. The address must be the node's own validator address
  (`device must register validator address <addr>` otherwise). Returns `{"token":
  "<32 hex chars>"}`. Registering a device id again replaces its token and resets
  its last timestamp. Registrations are held in memory in the node process.
* `engagement_submit_heartbeat`: params `[{"deviceId", "token", "timestamp"?}]`.
  The node's `engagement.Manager` checks the token (`unknown device`, `invalid
  token`), rejects a timestamp not greater than the device's last (`heartbeat
  replay`) or closer than `HeartbeatInterval` (`heartbeat rate limited`), then
  builds, signs with the validator key and adds a `TxTypeHeartbeat` transaction
  (gas limit 21000, gas price 1, or one above a pending heartbeat's price so it
  can replace it) to the mempool. Returns `{"queued": true, "timestamp": N}`;
  a full mempool returns HTTP 503, `-32030`.

`cmd/nhb` also starts a heartbeat loop for its own validator key: it registers a
device named after the host at startup and submits a heartbeat about every
minute when the on-chain last heartbeat is at least `HeartbeatInterval` plus 15
seconds old (`startValidatorHeartbeatLoop`, `core.HeartbeatSubmissionMargin`).

A validator that stops heartbeating drops out of the active set after the
readiness grace period described in [epochs](./epochs.md#who-is-eligible-and-how-weight-is-computed).
