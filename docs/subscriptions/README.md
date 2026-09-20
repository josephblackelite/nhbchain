# Subscriptions - Native Recurring Billing

Subscriptions is a native chain module for recurring charges. A merchant
creates a `Plan` (name, price, asset, interval), a payer signs one transaction
to subscribe, and the chain debits the payer once per interval during block
processing. The payer's signature on the subscribe transaction is the standing
authorization: no further signature is required for later charges, and every
validator computes each charge identically. This document is the transaction
and RPC reference for the chain module (`native/subscriptions`,
`core/subscriptions_tx.go`, `core/subscriptions_settlement.go`,
`rpc/subscriptions_handlers.go`).

## Lifecycle

1. **Create a plan.** The merchant signs `TxTypeSubscriptionCreatePlan`. The
   pricing terms (`PriceWei`, `Asset`, `IntervalSeconds`,
   `TrialPeriodSeconds`) are never modified afterwards; only `Name` and
   `Active` can change (see [Plan updates](#plan-updates)).
2. **Subscribe.** A payer signs `TxTypeSubscriptionSubscribe` naming a
   `PlanID`. The transaction moves no funds and does not check the payer's
   balance. It copies the plan's price, asset and interval onto a new
   `Subscription` record, sets `NextChargeAt = block time + TrialPeriodSeconds`,
   and schedules the subscription in the due index. It fails if the plan does
   not exist, is not `Active`, or the node has no subscriptions engine
   configured.
3. **Charge.** At settlement the chain debits the payer's live balance of the
   plan asset by `PriceWei`, credits the merchant `PriceWei` minus the
   management fee, and credits the fee to the configured treasury. If the
   payer's balance is below `PriceWei` the attempt fails (see
   [Retry and dunning](#retry-and-dunning)).
4. **Cancel.** The payer, the plan's merchant, or a holder of
   `ROLE_SUBSCRIPTIONS_ADMIN` signs `TxTypeSubscriptionCancel`. A subscription
   that is already `cancelled` or `suspended` cannot be cancelled again
   (`subscriptions: subscription already cancelled`). A cancelled subscription
   is skipped at settlement and never charges again.

## Bounded standing mandate

The subscribe signature authorizes the chain to debit exactly the snapshotted
`PriceWei`, once per `IntervalSeconds`, from the payer's spendable balance,
until the subscription is cancelled or suspended. Nothing is locked or
escrowed at subscribe time. Each charge reads the payer's balance at that
moment and either succeeds in full or records a failed attempt. The only code
path that debits a payer is `settleSubscriptionCharges`.

## Plan updates

`TxTypeSubscriptionUpdatePlan` changes only `Name` (must be non-empty after
trimming) and `Active`. It can be sent by the plan's merchant or a holder of
`ROLE_SUBSCRIPTIONS_ADMIN`. `Active = false` stops new subscriptions; existing
subscriptions continue to charge. Existing subscriptions hold their own copy of
price, asset and interval, so no plan update can reprice them.

Plan validation (`native/subscriptions/registry.go` `sanitizePlan`): `Name`
non-empty, `PriceWei > 0`, `Asset` exactly `NHB` or `ZNHB`, `IntervalSeconds >
0`.

## State model

Keys are written through the state manager's KV store, which Keccak-256-hashes
every key before it reaches the trie. Values are RLP-encoded. IDs in keys are
8-byte big-endian integers; addresses are raw 20-byte values.

| Key | Value |
| --- | --- |
| `subscriptions/plan/<planId>` | `Plan` |
| `subscriptions/merchantplans/<merchant>` | list of plan IDs |
| `subscriptions/sub/<subscriptionId>` | `Subscription` |
| `subscriptions/payersubs/<payer>` | list of subscription IDs |
| `subscriptions/merchantsubs/<merchant>` | list of subscription IDs |
| `subscriptions/charges/<subscriptionId>` | list of `Charge` (full attempt history) |
| `subscriptions/due/<day>` | list of subscription IDs to attempt on that UTC day (day = unix seconds / 86400) |
| `subscriptions/watermark` | last UTC day number closed out |
| `subscriptions/seq/plan`, `subscriptions/seq/sub` | ID counters |

`PlanID` and `SubscriptionID` are `uint64` sequence numbers assigned by the
chain.

```go
type Plan struct {
    ID                 PlanID
    Merchant           [20]byte
    Name               string
    PriceWei           *big.Int
    Asset              Asset // "NHB" or "ZNHB"
    IntervalSeconds    uint64
    TrialPeriodSeconds uint64
    Active             bool
    CreatedAt          uint64
}

type Subscription struct {
    ID               SubscriptionID
    PlanID           PlanID
    Payer            [20]byte
    Merchant         [20]byte
    PriceWei         *big.Int // copied from Plan at subscribe time
    Asset            Asset
    IntervalSeconds  uint64
    Status           SubscriptionStatus // 0 active, 1 past_due, 2 cancelled, 3 suspended
    StartAt          uint64
    NextChargeAt     uint64
    CycleCount       uint64
    FailedAttempts   uint32
    LastChargeAt     uint64
    LastChargeStatus ChargeStatus // 0 paid, 1 failed
    CreatedAt        uint64
    CancelledAt      uint64
}
```

### Due-date index and settlement timing

`ProcessBlockLifecycle` (`core/epochs.go`) calls `settleSubscriptionCharges`
on every block, independent of epoch boundaries. It processes the due bucket
of every UTC day from the day after the stored watermark (day 0 when no
watermark exists yet) up to and including the current day, then advances the
watermark to the previous day. Charging is therefore **day-granular**: a subscription is
attempted on the first block that processes the UTC day containing its
`NextChargeAt`, at that block's timestamp, not at the exact second in
`NextChargeAt`. With no trial period `NextChargeAt` is set to the subscribe block's timestamp
and the subscription is added to that day's due bucket, so the first attempt
happens at the end of the subscribe transaction's own block:
`ProcessBlockLifecycle` runs after the block's transactions and re-scans
today's bucket on every block. When a charge succeeds, the next
`NextChargeAt` is the charging block's time plus `IntervalSeconds`, so the
schedule drifts with settlement time.

Settlement does nothing when the node has no subscriptions engine configured.

## Fee model

Each successful charge splits `PriceWei`:

* **Management fee** = `PriceWei * ManagementFeeBps / 10000` (integer division),
  credited to the configured treasury address.
* **Merchant proceeds** = `PriceWei` minus that fee.

This is applied directly to account balances in the settlement hook; it does
not go through the transfer-fee path (`native/fees`) that applies to
`TxTypeTransfer`/`TxTypeTransferZNHB`.

Configuration (`config.toml` `[subscriptions]`, validated by
`subscriptions.Config.Validate`): `ManagementFeeBps` (shipped `100`, 1%),
`ManagementFeeCapBps` (shipped `500`, 5%), `Treasury`, `MaxRetries` (shipped
`3`), `RetryIntervalSeconds` (shipped `86400`). Validation requires
`ManagementFeeCapBps <= 10000`, `ManagementFeeBps <= ManagementFeeCapBps`, a
non-zero treasury whenever `ManagementFeeBps > 0`, `MaxRetries > 0` and
`RetryIntervalSeconds > 0`. `cmd/nhb/main.go` panics at startup if the treasury
is empty with a non-zero fee. The built-in defaults in
`native/subscriptions/params.go` are the same four numbers. Because this
configuration is read from each node's own `config.toml`, every validator must
run the same values.

## Retry and dunning

A failed charge (payer balance below `PriceWei` at charge time) never fails the
block. Instead:

* The subscription becomes `past_due`, `FailedAttempts` increments, and the
  next attempt is scheduled `RetryIntervalSeconds` after the failed attempt,
  independent of the plan interval. The failure reason is
  `insufficient_balance`.
* When `FailedAttempts + 1 >= MaxRetries`, the subscription becomes
  `suspended` instead (reason `insufficient_balance_max_retries_exceeded`). It
  is not scheduled again and never charges again. With `MaxRetries = 3`, the
  third consecutive failure suspends it. `suspended` is distinct from
  `cancelled`.
* A successful charge resets `FailedAttempts` to `0` and the status to
  `active`.
* Every attempt, successful or failed, is appended to the subscription's
  charge history (`subscriptions_listCharges`). `attemptNumber` is the running
  count of all attempts on that subscription (previous charges plus one), not
  a count of consecutive failures.

## Events

| Event | Attributes |
| --- | --- |
| `subscriptions.plan.created` | `planId`, `merchant`, `priceWei`, `asset`, `intervalSeconds` |
| `subscriptions.plan.updated` | `planId`, `name`, `active` |
| `subscriptions.subscription.created` | `subscriptionId`, `planId`, `payer`, `merchant`, `priceWei`, `asset`, `nextChargeAt` |
| `subscriptions.subscription.cancelled` | `subscriptionId`, `payer`, `merchant` |
| `subscriptions.subscription.suspended` | `subscriptionId`, `payer`, `merchant`, `failedAttempts` |
| `subscriptions.charge.succeeded` | `subscriptionId`, `payer`, `merchant`, `asset`, `amountWei`, `feeWei`, `attemptNumber`, `nextChargeAt` |
| `subscriptions.charge.failed` | `subscriptionId`, `payer`, `merchant`, `attemptNumber`, `failureReason`, `newStatus`, `nextChargeAt` |

A failed charge that suspends the subscription emits both
`subscriptions.subscription.suspended` and `subscriptions.charge.failed`
(`newStatus` = `suspended`).

## Signed transactions

Every mutating action is a signed transaction submitted with
`nhb_sendTransaction`; there is no bearer-token write RPC for this module. The
acting address is the transaction's recovered signer. Payloads are
RLP-encoded structs in `tx.Data` (positional fields, no field names on the
wire), and each transaction increments the sender's nonce. Each is counted
against the `subscriptions` module quota and is refused while the
`subscriptions` module is paused.

| TxType | Byte | Signer | Payload fields, in order |
| --- | --- | --- | --- |
| `TxTypeSubscriptionCreatePlan` | `0x30` | the plan's merchant | `Name` string, `PriceWei` big int, `Asset` string, `IntervalSeconds` uint64, `TrialPeriodSeconds` uint64 |
| `TxTypeSubscriptionUpdatePlan` | `0x31` | the plan's merchant, or `ROLE_SUBSCRIPTIONS_ADMIN` | `PlanID` uint64, `Name` string, `Active` bool |
| `TxTypeSubscriptionSubscribe` | `0x32` | the payer | `PlanID` uint64 |
| `TxTypeSubscriptionCancel` | `0x33` | the payer, the plan's merchant, or `ROLE_SUBSCRIPTIONS_ADMIN` | `SubscriptionID` uint64 |

## JSON-RPC endpoints

The methods below are read-only, are dispatched without an authentication
check (`rpc/http.go`), and take one parameter object. IDs are decimal strings;
addresses are bech32.

### `subscriptions_getPlan`

**Params:** `{"planId": "1"}`. Returns HTTP 404 `plan not found` for an unknown
ID.

**Result:** `{planId, merchant, name, priceWei, asset, intervalSeconds, trialPeriodSeconds, active, createdAt}`

### `subscriptions_listPlansByMerchant`

**Params:** `{"merchant": "nhb1..."}`

**Result:** array of the plan result shape above.

### `subscriptions_getSubscription`

**Params:** `{"subscriptionId": "1"}`. Returns HTTP 404 `subscription not
found` for an unknown ID.

**Result:** `{subscriptionId, planId, payer, merchant, priceWei, asset, intervalSeconds, status, startAt, nextChargeAt, cycleCount, failedAttempts, lastChargeAt, lastChargeStatus, createdAt, cancelledAt}`
where `status` is `active`, `past_due`, `cancelled` or `suspended`,
`lastChargeStatus` is `paid` or `failed`, and `cancelledAt` is omitted when `0`.

### `subscriptions_listByPayer`

**Params:** `{"payer": "nhb1..."}`

**Result:** array of the subscription result shape above.

### `subscriptions_listByMerchant`

**Params:** `{"merchant": "nhb1..."}`

**Result:** array of the subscription result shape above.

### `subscriptions_listCharges`

**Params:** `{"subscriptionId": "1"}`

**Result:** array, in chronological order, of `{subscriptionId, planId, payer, merchant, asset, amountWei, feeWei, status, attemptNumber, chargedAt, failureReason}`;
`status` is `paid` or `failed`, and `failureReason` is omitted when empty. A
failed attempt has `amountWei` and `feeWei` of `"0"`.

### `subscriptions_getConfig`

**Params:** none

**Result:** `{managementFeeBps, managementFeeCapBps, treasury, maxRetries, retryIntervalSeconds, configured}`.
`configured` is `false` when the node has no subscriptions engine configured;
`treasury` is omitted when unset.

## CLI

`nhb-cli subscriptions` (`cmd/nhb-cli/subscriptions.go`):

```bash
# Merchant: create a plan. --price is in wei (scientific notation accepted).
# Defaults: --asset NHB, --interval-seconds 2592000 (30 days), --trial-seconds 0, --key wallet.key
nhb-cli subscriptions create-plan --name "Pro Monthly" --price 10e18 --asset NHB --interval-seconds 2592000 --key merchant.key

# Merchant: rename a plan or stop it accepting new subscribers (--name is required)
nhb-cli subscriptions update-plan --plan-id 1 --name "Pro Monthly (legacy)" --active=false --key merchant.key

# Payer: subscribe
nhb-cli subscriptions subscribe --plan-id 1 --key payer.key

# Payer, merchant, or admin: cancel
nhb-cli subscriptions cancel --subscription-id 1 --key payer.key

# Read-only queries
nhb-cli subscriptions get-plan --plan-id 1
nhb-cli subscriptions list-plans --merchant nhb1...
nhb-cli subscriptions get-subscription --subscription-id 1
nhb-cli subscriptions list-by-payer --payer nhb1...
nhb-cli subscriptions list-by-merchant --merchant nhb1...
nhb-cli subscriptions list-charges --subscription-id 1
nhb-cli subscriptions config
```
