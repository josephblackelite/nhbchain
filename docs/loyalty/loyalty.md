# NHBChain Loyalty Engine – Developer Guide

> Version: v0 (Phase 1–4) • ChainID: **14699254016670310680** • HRPs: **nhb**, **znhb**  
> Status: **Beta** (APIs stable; subject to additive changes)

## Table of Contents
1. [Overview](#1-overview)
2. [Concepts & Roles](#2-concepts--roles)
3. [On-Chain Model](#3-on-chain-model)
4. [Node JSON-RPC (Loyalty Admin & Read)](#4-node-json-rpc-loyalty-admin--read)
6. [CLI (`nhb-cli`) Command Reference](#6-cli--nhb-cli-loyalty)
7. [Events & Analytics](#7-events--analytics)
8. [Security & Compliance (Regulators--Auditors)](#8-security--compliance-regulators--auditors)
9. [End-to-End Flows](#9-end-to-end-flows)
10. [Errors & Troubleshooting](#10-errors--troubleshooting)
11. [Versioning & Migration](#11-versioning--migration)
12. [Appendices](#appendices)

---

## 1) Overview

The Loyalty Engine grants **ZNHB** rewards to users based on **final settlement** of payments and/or via **business-funded programs**. There is no implicit or “magic” minting in the engine: rewards are **funded from the protocol loyalty treasury and merchant paymasters**, preserving the circulating supply of the reward token and allowing auditors to reconcile balances.

**Key properties**

* **Deterministic:** reward amounts are derived from deterministic calculations performed at escrow release.
* **Idempotent:** duplicate award submissions with identical settlement identifiers are ignored after the first success.
* **Composable:** programs can be combined with base network rewards or marketing incentives without double-counting accruals.
* **Observable:** every action emits an event and can be retrieved from RPC or the Escrow Gateway for analytics tooling.

**Reward triggers**

* **Escrow-linked rewards:** On escrow `release` the engine checks the active programs for the merchant/business and allocates rewards from their paymaster wallets.
* **Base rewards:** network-wide base reward funded from the protocol loyalty treasury and credited to the **spender** on qualifying NHB commerce payments.

**Qualified spend guardrail**

Founder mainnet should treat protocol base rewards as a commerce incentive, not a generic transfer rebate.
In practice that means the reward path is intended for qualifying spend flows such as:

* POS authorization/capture settlement
* merchant invoice payment
* marketplace or escrow settlement to a merchant/payee
* OTC or approved commerce-tagged settlement flows

It should not be marketed or interpreted as rewarding arbitrary wallet-to-wallet transfers,
treasury movements, swap minting, or cash-out/redemption flows.

**Funding invariants**

* Protocol base rewards move `ZNHB` from the **loyalty treasury → spender**.
* Merchant bonus rewards move `ZNHB` from the **business paymaster → spender**.
* Founder mainnet treats `ZNHB` as a fixed-supply asset; ongoing loyalty execution does not depend on post-genesis `ZNHB` minting.
* Paymaster balances must be topped up by the business before reward execution. Insufficient paymaster balance causes the accrual to be skipped (event) without impacting settlement completion.

---

## 2) Concepts & Roles

| Actor / Concept | Description | Keys / IDs |
|-----------------|-------------|------------|
| **Business** | An owner address that manages one or more loyalty programs. Responsible for funding paymaster wallets and defining program parameters. | `businessID` (bytes32); owner wallet (Bech32 HRP `nhb`). |
| **Merchant** | Receiving address associated with a business. Merchants inherit the business’s programs. | `merchantID` (derived from address); registered via RPC or CLI. |
| **Program** | Reward configuration (basis points, caps, eligibility windows) funded via a business paymaster wallet. | `programID` (bytes32); references paymaster address. |
| **User** | An address that receives rewards. | Bech32 addresses with HRPs `nhb` or `znhb`. |
| **Paymaster** | Wallet holding ZNHB reserved for merchant-funded loyalty rewards. | Standard on-chain account managed by the business. |
| **Roles (on-chain)** | Privileged roles that govern advanced functionality. | `ROLE_ARBITRATOR`, `PAYMASTER`, `MINTER_NHB`. |

**Access patterns**

* Administrative RPC calls require transactions from the business owner or delegated admin. Ownership is validated on-chain (no off-chain ACLs).
* Program updates pause/resume reward accrual for new settlements but do not retroactively adjust previously accrued rewards.
* Multiple merchants can map to the same business, allowing consolidated program management while supporting per-merchant analytics through metadata fields.

---

## 3) On-Chain Model

### Program Structure

Programs are stored as structured data within the chain’s state. Typical fields:

* `ID` (`bytes32`): unique identifier returned upon creation.
* `Owner` (`nhb…`): business owner address; must sign admin transactions.
* `Paymaster` (`nhb…`): address holding ZNHB for merchant-funded rewards.
* `TokenSymbol` = `"ZNHB"` (fixed for loyalty payouts).
* `AccrualBps` (`uint32`): basis points applied to eligible settlement amounts (500 = 5%).
* `MinSpendWei` (`*big.Int`): minimum qualifying spend.
* `CapPerTx` (`*big.Int`): maximum reward per transaction.
* `DailyCapUser` (`*big.Int`): maximum reward per user per UTC day.
* `DailyCapProgram` (`*big.Int`): maximum total reward the program pays out across *all* users per UTC day.
* `EpochCapProgram` (`*big.Int`) / `EpochLengthSeconds` (`uint64`): maximum total reward per program epoch, and the epoch's length in seconds. `EpochLengthSeconds` is required (>0) whenever `EpochCapProgram` is set.
* `IssuanceCapUser` (`*big.Int`): maximum reward a single user can ever earn from this program, lifetime.
* `StartTime`, `EndTime` (`int64`): UNIX timestamps bounding program validity.
* `Active` (`bool`): indicates whether accrual logic executes.
* `includeP2P` (`bool`): include P2P escrow releases; default `false`.
* `metadata` (`map[string]string`): optional key/value data surfaced via analytics.

**Anti-sybil requirement**: `CreateProgram`/`UpdateProgram` reject any program where both `DailyCapProgram` and `EpochCapProgram` are unset/zero (`ErrInvalidProgram`). A per-user cap alone doesn't bound total payout -- an attacker can split spend across any number of self-controlled wallets, each staying under the per-user cap, to draw an unbounded multiple of it from the same merchant's paymaster. At least one program-wide ceiling must always be in place, regardless of how many distinct addresses participate.

### Global base reward

The chain stores a single `loyalty.GlobalConfig` record that governs the optional
network-wide base reward. Operators can toggle or tune it through governance:

* `Active` (`bool`): when `false`, base rewards are skipped entirely.
* `Treasury` (`[20]byte`): address that funds base payouts.
* `BaseBps` (`uint32`): founder default is **50 bps (0.50%)**, paying 0.5 ZNHB for every 100 NHB of qualifying spend.
* `MinSpend`, `CapPerTx`, `DailyCapUser` (`*big.Int`): caps expressed in wei (18 decimal places).
* `DailyCapCounterparty` (`*big.Int`, wei): anti-wash-trading control. Bounds how much base reward can
  accrue in one UTC day between one specific pair of addresses, regardless of which side sends --
  A→B and B→A share the same budget. Genuine commerce naturally spreads across many distinct
  counterparties (a merchant with many customers); two wallets cycling funds back and forth to farm
  rewards do not. Set to `0` to disable (matches pre-existing behavior). Configured at genesis via
  `LoyaltyGlobalSpec.dailyCapCounterparty` alongside the other caps above; there is no runtime RPC to
  change it.

With the default 50 bps rate, a settlement of `100 NHB` (`100 * 10^18` wei) accrues
`0.5 ZNHB` (`0.5 * 10^18` wei) to the **spender** so long as the treasury holds enough balance and the
per-transaction, daily, and counterparty-pair caps permit it.

Founder mainnet treats `ZNHB` as fixed-supply in practice:

* the full founder reserve is preallocated at genesis
* protocol loyalty draws from the configured treasury wallet
* merchant bonus rewards draw from business paymasters
* routine founder-mainnet loyalty does not depend on post-genesis `ZNHB` minting

### Deterministic meters

Meters are ledger entries that enforce daily caps and provide fast analytics queries:

* Key format: `loyalty/meter/<programID>/<user>/<YYYYMMDD>` → `*big.Int` (total accrued reward for that day).
* Resets automatically on UTC day rollover. Cap checks read the meter before writing.
* Meter updates are atomic with reward transfers to avoid race conditions.

### Settlement hooks

Escrow release triggers loyalty accruals via a module hook that receives:

```
struct SettlementContext {
  escrowID     [32]byte
  businessID   [32]byte
  merchant     [20]byte
  payer        [20]byte
  payee        [20]byte
  token        string
  amount       *big.Int
  txHash       [32]byte
  metadata     map[string]string
}
```

The loyalty module evaluates active programs for `businessID`, filters by `token` and thresholds, computes rewards, debits paymaster pools, and emits `loyalty.program.accrued` events per user.

---

## 4) Loyalty Admin Transactions & Node JSON-RPC (Read)

**Write operations are real, directly-signed on-chain transactions, not JSON-RPC calls.** This is a change from an earlier design: the `loyalty_createBusiness`/`setPaymaster`/`addMerchant`/`removeMerchant`/`createProgram`/`updateProgram`/`pauseProgram`/`resumeProgram` RPC methods described in older revisions of this document are **permanently disabled** (`HTTP 410`, `rpc/loyalty_handlers.go`'s `loyaltyRPCDisabledMessage`) — they used to mutate validator state directly outside the block/consensus pipeline, a guaranteed-fork bug on this chain's 2-validator, zero-quorum-slack topology, fixed 2026-09-04. Every admin action below is now a signed `Transaction` submitted through the ordinary broadcast RPC (`nhb_sendTransaction`), exactly like a transfer or any other native transaction — build it, sign it with the invoking wallet's key, submit it, and (since a transaction has no synchronous return value the way an RPC call did) look up the result afterward via the read methods in the next subsection. There is no `loyalty-gateway` relayer service; unlike escrow, every operation here has exactly one authorizing party, so there's nothing for a relayer to reconcile — the wallet that authorizes an action is the wallet that submits it.

Read operations (business/program lookups, meters) are unaffected and remain ordinary JSON-RPC calls at the node endpoint (default `http://127.0.0.1:8545`), free, via HTTP POST or WebSocket depending on node configuration.

### Authentication & Authorization

* **Authentication:** the transaction's own signature — the node recovers the signer and checks it against the required authority (business/program owner, or an address holding the `ROLE_LOYALTY_ADMIN` role as an operator-granted override) as part of applying the transaction, the same way any other module's authorization check works.
* **Nonce management:** fetch the account's current nonce before building a transaction, same as any other native transaction.
* **Gas / Fees:** native transactions on this chain are fee-free by design (`GasLimit`/`GasPrice` only affect mempool tie-breaking, never charged against the sender) — anti-spam is handled by the per-sender request-rate quota described below, not by fees.
* **Rate limiting:** `TxTypeCreateLoyaltyBusiness`/`CreateLoyaltyProgram` (the two operations that mint a new ID) are gated by `native/common`'s quota mechanism (`config.toml`'s `[global.Quotas.Loyalty]`, currently a generous 6000 requests/min per sender). The other six operations below are deliberately left unquota-gated — each is an already-verified owner/admin acting on a single resource they already control, not a new-ID-minting action a spam quota needs to bound.

### Admin (write — signed transactions, `core/types/transaction.go`)

#### `TxTypeCreateLoyaltyBusiness` (`0x42`)
* `tx.Data` (JSON): `{ "name": "<business name>" }`.
* The transaction **sender becomes the business owner** — there is no `owner` payload field to set it to anyone else; this closes a spoofing surface the old RPC's plaintext `owner`/`caller` fields had.
* **Returns:** nothing synchronous. `RegisterBusiness` emits no event; discover the assigned `businessID` afterward via `loyalty_listBusinesses` (below), keyed by the sender's own address.
* **Errors:** empty/whitespace-only `name` is rejected.

#### `TxTypeLoyaltySetPaymaster` (`0x43`)
* `tx.Data` (JSON): `{ "businessId": "0x<64 hex>", "paymaster": "nhb1..." }` — omit/empty `paymaster` to clear it.
* Rotates the paymaster address used to fund all programs under the business. **This transaction only points the business at an address — it moves no funds.** Actually funding that paymaster is a separate, ordinary NHB/ZNHB transfer to it.
* Emits `loyalty.paymaster.rotated`.
* **Checks:** sender must be the business owner or hold `ROLE_LOYALTY_ADMIN` — enforced inside `native/loyalty`'s `SetPaymaster` itself.

#### `TxTypeLoyaltyAddMerchant` (`0x44`) / `TxTypeLoyaltyRemoveMerchant` (`0x45`)
* `tx.Data` (JSON): `{ "businessId": "0x<64 hex>", "merchant": "nhb1..." }`.
* Adds or removes a merchant address from a business. Merchants inherit all active programs instantly; removing one stops future accruals but does not claw back rewards already paid.
* **Checks:** sender must be the business owner or hold `ROLE_LOYALTY_ADMIN` — unlike every other operation on this list, `native/loyalty`'s underlying `AddMerchantAddress`/`RemoveMerchantAddress` methods perform **no authorization check of their own** (a real gap found while building this), so this check is enforced entirely by the transaction-dispatch layer (`core/state_transition.go`'s `applyLoyaltyAddMerchant`/`applyLoyaltyRemoveMerchant`) before either method is ever called.

#### `TxTypeCreateLoyaltyProgram` (`0x46`)
* `tx.Data` (JSON) includes all fields described in [On-Chain Model](#3-on-chain-model) (`businessId`, `id` — client-generated 32-byte hex, `pool`, `tokenSymbol`, `rewardMode`, `accrualBps`, `fixedRewardWei`, the cap/timing fields, `active`).
* The transaction **sender becomes the program owner** — there is no `owner` payload field.
* **Checks:** the sender must already be a registered merchant of the named business (added via `TxTypeLoyaltyAddMerchant` — a business's own owner is *not* automatically its own merchant, matching the pre-existing semantics exactly) or hold `ROLE_LOYALTY_ADMIN`.
* **Validation** (unchanged, enforced inside `native/loyalty`'s `sanitizeProgram`/`CreateProgram`): token symbol must be a registered token, `accrualBps <= 100000`, time windows valid, all caps non-negative, and **at least one of `dailyCapProgram`/`epochCapProgram` must be greater than zero** — a program with only per-user caps is rejected, since per-user caps alone don't bound total payout exposure against an attacker splitting spend across many wallets.
* **Returns:** nothing synchronous; look up the program afterward via `loyalty_listPrograms`/a known `id`.

#### `TxTypeUpdateLoyaltyProgram` (`0x47`)
* `tx.Data` (JSON): same shape as create, minus `businessId` and `owner` (both are resolved from the existing on-chain record, never from the payload — `id`/`owner` can never change via this transaction, even if included).
* **Full replace, not a partial patch** — every mutable field is overwritten from the payload on every call; resend the complete desired program state, not just what's changing.
* **Checks:** sender must be the *existing* program's owner (loaded from state, never trusted from the payload) or hold `ROLE_LOYALTY_ADMIN`.

#### `TxTypePauseLoyaltyProgram` (`0x48`) / `TxTypeResumeLoyaltyProgram` (`0x49`)
* `tx.Data` (JSON): `{ "id": "0x<64 hex>" }`.
* Toggles the `Active` flag. Paused programs skip accruals but keep their meters for historical reference. Idempotent — pausing an already-paused program (or resuming an already-active one) is a no-op, not an error.
* Emits `loyalty.program.paused`/`loyalty.program.resumed`.
* **Checks:** sender must be the program owner or hold `ROLE_LOYALTY_ADMIN`.

### Read (dashboard — still ordinary JSON-RPC)

#### `loyalty_getBusiness(businessID)`
* Returns business metadata, current paymaster, and merchant list.

#### `loyalty_listBusinesses(ownerBech32)`
* Returns every `businessID` the given address owns, in deterministic order. This is the only way to discover the ID a `TxTypeCreateLoyaltyBusiness` transaction was just assigned, since transactions carry no synchronous return value the way the old RPC's response did.

#### `loyalty_listPrograms(businessID)`
* Returns an array of active and inactive programs.
* Supports optional pagination parameters: `offset`, `limit` when provided via named params.

#### `loyalty_programStats(programID, dayUTC) -> { rewardsPaid, txCount, capUsage }`
* Returns real, on-chain meter data for the given program and UTC day (`YYYY-MM-DD`). `programID` must reference an existing program (`-32602 "program not found"` otherwise).
* **`rewardsPaid`** (`string`, wei): the program's total ZNHB rewards paid out across *all* users on that UTC day. Sourced from an always-on per-program daily meter that `ApplyProgramReward` (`native/loyalty/engine_program.go`) writes on every successful accrual, regardless of whether the program has any cap configured.
* **`txCount`** (`string`, integer): the number of successful accrual events (reward payouts) for the program on that day. Written alongside `rewardsPaid` by the same code path; not affected by `loyalty.program.skipped` events (skips are not counted).
* **`capUsage`** (`string` or `null`): `rewardsPaid / DailyCapProgram`, formatted as a decimal fraction to 4 places (e.g. `"0.2500"` = 25% of the daily cap used). Returned as JSON `null` when the program has **no configured `DailyCapProgram`** (there is no denominator to divide by) -- this is deliberately distinct from `"0.0000"`, which means a *capped* program with genuinely zero usage that day. A program can have `EpochCapProgram` set without `DailyCapProgram`; `capUsage` does not reflect epoch-cap usage, since epoch windows aren't guaranteed to align with UTC day boundaries.
* **Known limitation:** `rewardsPaid`/`txCount` are only as complete as the on-chain meter history. Any UTC day that predates this method's real implementation (previously a stub returning hardcoded zeros, and before that the underlying per-program daily-total meter was only written for programs with a configured `DailyCapProgram`) will read back as `"0"`/`"0"` even if real rewards were paid that day -- indistinguishable from genuine zero activity. This cannot be backfilled retroactively from state alone; only days observed after the fix was deployed have complete data.

#### `loyalty_userDaily(userBech32, programID, dayUTC)`
* Returns user-specific meter details for compliance or customer support.

#### `loyalty_paymasterBalance(businessID)`
* Returns the ZNHB balance of the current paymaster pool and reserved amounts (pending awards).

**JSON-RPC cURL example**

```bash
curl -s http://127.0.0.1:8545 -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0",
  "id":1,
  "method":"loyalty_listPrograms",
  "params":["0x<businessID>"]
}'
```

---

## 6) CLI – `nhb-cli` (loyalty)

The `nhb-cli` binary ships with subcommands to manage loyalty constructs.

> **The 8 write subcommands below (`loyalty-create-business` through `loyalty-resume-program`) are currently non-functional.** They call the `loyalty_createBusiness`/`setPaymaster`/`addMerchant`/`removeMerchant`/`createProgram`/`updateProgram`/`pauseProgram`/`resumeProgram` RPC methods directly (`cmd/nhb-cli/main.go`'s `callLoyaltyRPC`), which are now permanently disabled (§4 above) — every one of these commands will fail with the disabled-method error until the CLI is updated to build, sign, and broadcast the real `TxTypeCreateLoyaltyBusiness`.. `TxTypeResumeLoyaltyProgram` transactions instead (`cmd/nhb-cli/main.go` already has the primitives this needs — `sendTransaction(tx *types.Transaction)` and the file's existing private-key/keystore loading used by other signed-transaction commands — this is a mechanical rewire, not new infrastructure). The read subcommands (`loyalty-get-business`, `loyalty-list-programs`, `loyalty-program-stats`, `loyalty-user-daily`, `loyalty-paymaster-balance`, `loyalty-resolve-username`, `loyalty-user-qr`) are unaffected and work as documented.

```bash
# Create business -- NOT CURRENTLY WORKING, see note above
nhb-cli loyalty-create-business nhb1... "Zenith Hotels"

# Set paymaster -- NOT CURRENTLY WORKING, see note above
nhb-cli loyalty-set-paymaster nhb1... 0x... nhb1...

# Add merchant -- NOT CURRENTLY WORKING, see note above
nhb-cli loyalty-add-merchant nhb1... 0x... nhb1...

# Create program -- NOT CURRENTLY WORKING, see note above
nhb-cli loyalty-create-program nhb1... 0x... '{"...program spec JSON..."}'

# Update program (full replace, not partial -- see §4) -- NOT CURRENTLY WORKING, see note above
nhb-cli loyalty-update-program nhb1... '{"...program spec JSON..."}'

# Pause / Resume -- NOT CURRENTLY WORKING, see note above
nhb-cli loyalty-pause-program nhb1... 0x...
nhb-cli loyalty-resume-program nhb1... 0x...

# Stats -- read-only, works
nhb-cli loyalty-program-stats 0x... 2025-09-22

# User meter lookup -- read-only, works
nhb-cli loyalty-user-daily nhb1... 0x... 2025-09-22
```

**CLI configuration tips**

* Use `--rpc http://127.0.0.1:8545` to override default RPC URL.
* Combine with `jq` to parse JSON output for automation pipelines.

---

## 7) Events & Analytics

Events are emitted both on-chain and via the Escrow Gateway for downstream ingestion.

| Event | Description | Payload fields |
|-------|-------------|----------------|
| `loyalty.program.accrued` | Program-funded reward successfully applied to a user. | `{ program, user, merchant, token, amount, bps, escrowId, txHash }` |
| `loyalty.program.skipped` | Program-funded reward skipped due to validation failure or insufficient funds. | `{ program, user, reason, ctx }` |
| `loyalty.base.accrued` | Base (protocol treasury) reward successfully applied to a spender. | `{ user, token, amount, txHash }` |
| `loyalty.base.skipped` | Base reward skipped due to validation failure or insufficient funds. | `{ user, reason, ctx }` |
| `loyalty.program.paused` / `loyalty.program.resumed` | Program state toggled. | `{ program, actor, timestamp }` |
| `loyalty.paymaster.rotated` | Paymaster changed for a business. | `{ business, old, new, actor }` |

**Analytics guidance**

* Subscribe to events via node WebSocket or replicate using the indexer service.
* Correlate `escrowId` with settlement records to compute blended take rates.
* Use meters in combination with events to reconcile totals (events provide context, meters provide authoritative counts).

---

## 8) Security & Compliance (Regulators / Auditors)

### Authentication

* **Node access:** requires wallet signatures. Private keys must be stored in secure keystores (HSM, KMS, or encrypted files). Avoid exporting raw keys.
* **Gateway access:** API Key + HMAC. Rotate API keys quarterly or upon personnel changes. Use TLS 1.2+ and enforce IP allow-lists for production.
* **Wallet signatures for privileged REST endpoints:** Use EIP-191 style signing, ensuring the `timestamp` and request body are included in the signed payload to prevent replay.

### Authorization

* Business owners may delegate admin privileges via on-chain role assignments (future additive feature). Until then, use multisig or KMS-managed keys to enforce dual control.
* Arbitrator and paymaster roles are set by governance. Ensure separation of duties: arbitrators should not control paymaster funds.

### Determinism & Accounting Controls

* All reward computations use fixed-point math via `big.Int`. Avoid floating-point operations in client code.
* Programs cannot overdraft paymaster pools. When funds are insufficient, the accrual is skipped and flagged. Businesses should monitor balances via `loyalty_paymasterBalance`.
* Daily and per-transaction caps are enforced at the time of accrual; updates to caps affect only future accruals.

### Audit & Retention

* Escrow Gateway maintains an append-only audit log with request hash, actor, RPC response, and blockchain transaction hash.
* Retain logs for a minimum of **7 years** or as required by jurisdictional regulations.
* Provide auditors with read-only API keys or offline exports from RPC and gateway logs. Use hashed identifiers when sharing user-level data.

### Privacy & Data Handling

* Do not persist raw PII outside secure, access-controlled systems.

### Compliance Checklist

* ✅ API key rotation policy defined and documented.
* ✅ Dual control for paymaster funding (multisig or approval workflow).
* ✅ Monitoring alerts for low paymaster balance and high skip rates.
* ✅ Periodic reconciliation between on-chain events, meters, and accounting ledgers.
* ✅ Incident response playbook for compromised keys or suspicious award activity.

---

## 9) End-to-End Flows

### Business Program Setup & Settlement

1. **Create business** using RPC or CLI. Record `businessID`.
2. **Assign paymaster** wallet funded with ZNHB.
3. **Add merchants** that process transactions for the business.
4. **Create program** defining accrual rate and caps.
5. **Fund paymaster** periodically (`wallet send` or bridging). Ensure buffer covers expected rewards.
6. **Escrow release** occurs → loyalty engine evaluates and transfers rewards.
7. **Event handling:** `loyalty.program.accrued` event notifies downstream systems.
8. **Reporting:** Use `loyalty_programStats` and `loyalty_userDaily` to reconcile payouts.

### Automation example (CLI)

```bash
# Query program stats for daily reconciliation
nhb-cli loyalty-program-stats 0x... $(date -u +%Y-%m-%d)
```

---

## 10) Errors & Troubleshooting

### JSON-RPC error codes

Loyalty RPC handlers return standard numeric JSON-RPC error codes paired with a plain-English message, not string error codes:

| Code | Description | Recommended action |
|------|-------------|--------------------|
| `-32602` (invalid params) | Malformed or invalid request parameters, e.g. `"invalid caller address"`, `"invalid businessId"`, `"business not found"`. | Check the message text and correct the request. |
| `-32001` (unauthorized) | Caller lacks required role or ownership, e.g. `"caller not authorized"`. | Sign the transaction with the business owner wallet or a `ROLE_LOYALTY_ADMIN` holder. |
| `-32000` (server error) | Internal failure loading state (business, account, meters, programs). | Retry; if persistent, contact node operator. |

### Troubleshooting checklist

* **High skip rate:** monitor `loyalty.program.skipped` / `loyalty.base.skipped` events; inspect the `reason` field for patterns (caps, balance, inactive program).
* **Program not applying to merchant:** verify merchant is registered to the business and program `StartTime/EndTime` encompasses settlement timestamp.
* **Module paused:** run `go run ./examples/docs/ops/read_pauses` to confirm the loyalty flag is `false`; resume with `go run ./examples/docs/ops/pause_toggle --module loyalty --state resume` when cleared by governance.
* **Cap rejections:** inspect the program meters via `loyalty_programStats` to see `capUsage` and compare against configured per-transaction / daily caps before retrying. `capUsage` is `null` for programs without a configured `DailyCapProgram` -- see [Section 4](#4-node-json-rpc-loyalty-admin--read) for the full field contract.

---

## 11) Versioning & Migration

* This documentation covers **Phase 1–4** features. Future phases will add additive fields/endpoints while preserving backward compatibility.
* All changes will be tracked in `docs/CHANGELOG.md` (forthcoming). Subscribe to release notes to keep client integrations up to date.
* Migration best practices:
  * Test new program configurations on **devnet (ChainID 187001)** before mainnet.
  * Use feature flags in client applications to gradually roll out new program logic.
  * Maintain compatibility tests that validate RPC and REST schemas using golden fixtures.

---

## Appendices

### Appendix A – HMAC Example (Pseudo)

```
data = method + "|" + path + "|" + body + "|" + timestamp
sig  = hex( HMAC_SHA256(secret_for_api_key, data) )
```

* Ensure `body` is the exact raw JSON string sent over the wire.
* When `body` is empty (e.g., GET requests), use an empty string between separators.

### Appendix B – Wallet Signature (EIP-191 style)

```
message = keccak256(method|path|body|timestamp|id)
sig     = wallet.sign(message)
headers = {
  "X-Sig-Addr": "nhb1...",
  "X-Sig": sig,
  "X-Timestamp": timestamp
}
```

* Use `id` = `Idempotency-Key` to bind the signature to a unique request instance.
* Verify signatures server-side using `recoverAddress` to ensure caller authorization.

---

### Appendix C – Sample Program Spec

```json
{
  "tokenSymbol": "ZNHB",
  "accrualBps": 500,
  "minSpendWei": "100000000000000000",
  "capPerTx": "5000000000000000000",
  "dailyCapUser": "10000000000000000000",
  "dailyCapProgram": "500000000000000000000",
  "startTime": 1730400000,
  "endTime": 1762032000,
  "includeP2P": false,
  "metadata": {
    "tier": "gold",
    "region": "NA"
  }
}
```

---
