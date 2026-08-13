# Subscriptions — Native Recurring Billing

Subscriptions is a native chain module for recurring billing: a merchant
defines a `Plan` (a price, an asset, a billing interval), a payer signs
one transaction to subscribe, and the chain itself charges that payer on
schedule from then on — no server-side card vault, no webhook race with a
payment processor, no separate "off-session charge" API. The signature on
the original subscribe transaction *is* the standing authorization; every
later charge is a deterministic block-level state transition every
validator computes identically, the same way staking rewards and the
treasury buyback settle.

Where Stripe requires creating a **Product** and a **Price** before
anything can be charged against them, a Subscriptions `Plan` folds both
into one object — a merchant sets a name, a price, an asset, and a
billing interval, and it's live. This document is the RPC/transaction
reference for the chain module itself; the merchant-facing dashboard and
full REST API built on top of it live in the `nhbportal` repository.

## Lifecycle Overview

1. **Create a Plan** — a merchant signs a transaction defining a
   recurring price. `PriceWei`/`Asset`/`IntervalSeconds`/`TrialPeriodSeconds`
   are fixed forever once the plan exists (see [Plan immutability](#plan-immutability)
   below); only `Name` and `Active` can change later.
2. **Subscribe** — a payer signs a transaction naming a `PlanID`. This
   transaction moves no funds and requires no funds up front — it only
   records a standing authorization, snapshotting the plan's price/asset/
   interval onto the new `Subscription` record so a later change to the
   Plan (or a merchant creating a differently-priced Plan) can never
   retroactively reprice an existing subscriber.
3. **Charge** — the chain itself debits the payer and credits the
   merchant (minus the platform management fee) once per billing cycle,
   entirely without further payer involvement. A failed charge (insufficient
   balance) marks the subscription past-due and retries on a fixed
   schedule; after too many consecutive failures the subscription is
   suspended.
4. **Cancel** — the payer, the plan's merchant, or an operator holding
   `ROLE_SUBSCRIPTIONS_ADMIN` can cancel at any time. Cancellation is
   final — a cancelled subscription never charges again.

## The Mandate: How a Charge Happens With No Fresh Signature

This is the one genuinely new piece of infrastructure this module adds to
the chain, so it's worth stating precisely. Every other module that moves
a user's funds without a fresh per-transfer signature falls into one of a
few existing patterns:

- **Staking/validator rewards** (`core/rewards_logic.go`) — the system
  computes a schedule-driven payout and debits a genesis-configured admin
  wallet, once per epoch, identically on every validator.
- **Treasury buyback** (`core/buyback_settlement.go`) — a seller signs an
  ask *once*, which immediately escrows their ZNHB into a module-owned
  vault; settlement later pulls only from that already-escrowed vault,
  never from the seller's own spendable balance again.

Subscriptions introduces a third pattern: a **bounded standing mandate**.
The payer's signature on `TxTypeSubscriptionSubscribe` authorizes the
chain to debit *exactly* `PriceWei` — a fixed, disclosed-up-front amount,
never open-ended and never chosen by the system — from their own live
spendable balance, once per `IntervalSeconds`, until they cancel. No funds
are locked or escrowed at subscribe time; each cycle's charge reads the
payer's real-time balance and either succeeds or marks the cycle past-due.
This is deliberately the closest on-chain analogue to a real-world ACH
direct-debit mandate or a card network's stored-payment-method
authorization: one signature, a fixed amount, revocable at any time.

## Plan Immutability

A `Plan`'s pricing terms (`PriceWei`, `Asset`, `IntervalSeconds`,
`TrialPeriodSeconds`) can never change after creation — a merchant who
wants different terms creates a new Plan. This mirrors Stripe's own real
behavior (Prices are immutable; Products are not) and exists for the same
reason: every `Subscription` snapshots its price/asset/interval at
subscribe time, so mutating a live Plan's price would either do nothing
(subscribers already snapshotted the old price) or, worse, invite a design
where it *did* retroactively reprice active subscribers — silently
raising what someone already agreed to pay. `Name` and `Active` remain
mutable so a merchant can rename a plan or stop it from accepting new
subscribers without disturbing anyone already on it.

## State Model

All keys are Keccak-256 hashed before reaching the underlying trie, exactly
like every other native module's storage.

| Key | Value |
| --- | --- |
| `subscriptions/plan/<planId>` | `Plan` |
| `subscriptions/merchantplans/<merchant>` | `[]uint64` plan ID index |
| `subscriptions/sub/<subscriptionId>` | `Subscription` |
| `subscriptions/payersubs/<payer>` | `[]uint64` subscription ID index |
| `subscriptions/merchantsubs/<merchant>` | `[]uint64` subscription ID index |
| `subscriptions/charges/<subscriptionId>` | `[]Charge` full audit history |
| `subscriptions/due/<day>` | `[]uint64` subscription IDs due for a charge attempt on that UTC day |
| `subscriptions/watermark` | last UTC day number fully closed out |
| `subscriptions/seq/plan`, `subscriptions/seq/sub` | monotonic ID counters |

`PlanID` and `SubscriptionID` are plain `uint64` sequence numbers assigned
by the chain at creation time (mirroring `native/governance`'s
`GovernanceNextProposalID` counter) — callers never invent or race an ID
the way an off-chain system might.

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
    PriceWei         *big.Int // snapshotted from Plan at subscribe time
    Asset            Asset
    IntervalSeconds  uint64
    Status           SubscriptionStatus // active | past_due | cancelled | suspended
    StartAt          uint64
    NextChargeAt     uint64
    CycleCount       uint64
    FailedAttempts   uint32
    LastChargeAt     uint64
    LastChargeStatus ChargeStatus
    CreatedAt        uint64
    CancelledAt      uint64
}
```

### Due-Date Index

A due-index bucketed by UTC calendar day (`subscriptions/due/<day>`)
mirrors the treasury buyback engine's epoch-bucketed ask list
(`core/state/buyback.go`) — the settlement hook only ever reads and
clears the single day bucket it's currently processing, never scans every
outstanding subscription on the chain. Subscription billing cadence
(commonly monthly) has no natural relationship to validator-epoch length,
so unlike buyback (which settles once per epoch), this bucket is checked
unconditionally at the top of every block's lifecycle processing,
internally gated by a persisted watermark so it's a cheap no-op on every
block that isn't a day rollover. The current day's bucket is deliberately
*never* marked permanently closed — a Subscribe transaction or a same-day
retry can still add a new entry to it at any point before the day ends —
only days that have fully elapsed are closed for good.

## Fee Model

Every successful charge splits `PriceWei` two ways:

- **Merchant proceeds** — `PriceWei` minus the management fee, credited
  directly to the merchant's own balance.
- **Platform management fee** — `ManagementFeeBps` of `PriceWei` (bounded
  by an immutable `ManagementFeeCapBps` ceiling), credited to the
  deployment's configured treasury address.

This management fee is **separate from, and charged alongside, the
ordinary transfer fee** (`native/fees`) — a subscription charge is not a
`TxTypeTransfer`/`TxTypeTransferZNHB` and never goes through that fee path
at all. It exists specifically so NHBCoin can offer recurring billing as a
managed service the way Stripe does, at a fraction of a typical payment
processor's take rate rather than matching it. See
[`config.toml`](../../config.toml)'s `[subscriptions]` section for the
live default (1%, capped at 5%) and `native/subscriptions/params.go` for
the underlying constants.

## Retry and Dunning

A failed charge (payer balance below `PriceWei` at charge time) never
reverts or errors the block it's discovered in — a subscriber running low
on funds must never be able to stall block production. Instead:

- The subscription moves to `past_due`, `FailedAttempts` increments, and
  a retry is scheduled `RetryIntervalSeconds` later (independent of the
  plan's own billing interval — a real dunning cadence, not "wait a full
  month and try again").
- After `MaxRetries` consecutive failures, the subscription moves to
  `suspended` — terminal, permanently dropped from the due-index — and
  will never charge again. `suspended` is deliberately distinct from
  `cancelled` so downstream consumers (the portal's dunning-email
  scheduler, merchant webhooks) can tell "the payer chose to stop" from
  "the payer's funding ran out."
- Every attempt, successful or not, is recorded in the subscription's full
  `Charge` history (`subscriptions_listCharges`) — this is the single
  source of truth the portal's reminder/payment-failure email scheduler
  and merchant webhook delivery both read from, not a separate off-chain
  ledger.

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

## Signed Transactions

Every mutating action is a real signed transaction submitted via the
generic `nhb_sendTransaction` RPC method, the same as every other native
transaction type on this chain — there is no bespoke bearer-token write
RPC method for this module (that class of bug was fixed for governance,
lending pool creation, and POTSO staking earlier in this codebase's
history; a new module doesn't get to reintroduce it). The caller is
always the transaction's own cryptographically recovered signer
(`tx.From()`), never a client-supplied payload field.

| TxType | Byte | Signer | Payload |
| --- | --- | --- | --- |
| `TxTypeSubscriptionCreatePlan` | `0x30` | the new plan's merchant | `{Name, PriceWei, Asset, IntervalSeconds, TrialPeriodSeconds}` |
| `TxTypeSubscriptionUpdatePlan` | `0x31` | the plan's merchant (or `ROLE_SUBSCRIPTIONS_ADMIN`) | `{PlanID, Name, Active}` |
| `TxTypeSubscriptionSubscribe` | `0x32` | the new subscription's payer | `{PlanID}` |
| `TxTypeSubscriptionCancel` | `0x33` | the payer, the plan's merchant, or `ROLE_SUBSCRIPTIONS_ADMIN` | `{SubscriptionID}` |

## JSON-RPC Endpoints

Every mutating action above goes through `nhb_sendTransaction`. Everything
below is a read-only public query — no authentication required, the same
class as `potso_stake_info` or `gov_list`.

### `subscriptions_getPlan`

**Params:** `{"planId": "1"}`

**Result:** `{planId, merchant, name, priceWei, asset, intervalSeconds, trialPeriodSeconds, active, createdAt}`

### `subscriptions_listPlansByMerchant`

**Params:** `{"merchant": "nhb1..."}`

**Result:** array of the plan result shape above.

### `subscriptions_getSubscription`

**Params:** `{"subscriptionId": "1"}`

**Result:** `{subscriptionId, planId, payer, merchant, priceWei, asset, intervalSeconds, status, startAt, nextChargeAt, cycleCount, failedAttempts, lastChargeAt, lastChargeStatus, createdAt, cancelledAt}`

### `subscriptions_listByPayer`

**Params:** `{"payer": "nhb1..."}`

**Result:** array of the subscription result shape above.

### `subscriptions_listByMerchant`

**Params:** `{"merchant": "nhb1..."}`

**Result:** array of the subscription result shape above.

### `subscriptions_listCharges`

**Params:** `{"subscriptionId": "1"}`

**Result:** array of `{subscriptionId, planId, payer, merchant, asset, amountWei, feeWei, status, attemptNumber, chargedAt, failureReason}`, in chronological order.

### `subscriptions_getConfig`

**Params:** none

**Result:** `{managementFeeBps, managementFeeCapBps, treasury, maxRetries, retryIntervalSeconds, configured}` — the live deployment configuration, so a dashboard can display the real fee rate rather than a hardcoded guess. `configured` is `false` on a network that hasn't set up the subscriptions engine at all.

## CLI Support

`nhb-cli` bundles the full lifecycle under `subscriptions`:

```bash
# Merchant: create a plan (price in wei, scientific notation accepted)
nhb-cli subscriptions create-plan --name "Pro Monthly" --price 10e18 --asset NHB --interval-seconds 2592000 --key merchant.key

# Merchant: rename a plan or stop it accepting new subscribers
nhb-cli subscriptions update-plan --plan-id 1 --name "Pro Monthly (legacy)" --active=false --key merchant.key

# Payer: subscribe -- this single signature is the entire standing mandate
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

## Designing a Subscriptions Integration

- **Merchant dashboard and public REST API** — nhbportal's Products &
  Pricing / Subscriptions pages, and the full `/api/v1/subscriptions/*`
  REST API that wraps every RPC method above (plus reminder/dunning
  emails and webhook delivery) for developers who never want to touch
  JSON-RPC directly, live in the `nhbportal` repository.
- [POTSO Staking Locks](../potso/stake.md) – the earlier module this
  session's signed-transaction discipline (no bespoke bearer-auth write
  RPC, replay protection via the standard account nonce) was established
  against; read alongside this document for the shared conventions.
