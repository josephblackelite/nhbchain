# NHBChain Loyalty Engine – Developer Guide

> Version: v0 (Phase 1–4) • ChainID: **187001** • HRPs: **nhb**, **znhb**  
> Status: **Beta** (APIs stable; subject to additive changes)

## Table of Contents
1. [Overview](#1-overview)
2. [Concepts & Roles](#2-concepts--roles)
3. [On-Chain Model](#3-on-chain-model)
4. [Node JSON-RPC (Loyalty Admin & Read)](#4-node-json-rpc-loyalty-admin--read)
5. [Escrow Gateway (External Awards API)](#5-escrow-gateway--external-awards-api-off-network-purchases)
6. [CLI (`nhb-cli`) Command Reference](#6-cli--nhb-cli-loyalty)
7. [Events & Analytics](#7-events--analytics)
8. [Security & Compliance (Regulators--Auditors)](#8-security--compliance-regulators--auditors)
9. [End-to-End Flows](#9-end-to-end-flows)
10. [Errors & Troubleshooting](#10-errors--troubleshooting)
11. [Versioning & Migration](#11-versioning--migration)
12. [Appendices](#appendices)

---

## 1) Overview

The Loyalty Engine grants **ZNHB** rewards to users based on **final settlement** of payments (escrow **release**) and/or via **business-funded programs**. There is no implicit or “magic” minting in the engine: rewards are **funded from program pools**, preserving the circulating supply of the reward token and allowing auditors to reconcile balances.

**Key properties**

* **Deterministic:** reward amounts are derived from deterministic calculations performed at escrow release or explicit award submissions.
* **Idempotent:** duplicate award submissions with identical `referenceId` or settlement identifiers are ignored after the first success.
* **Composable:** programs can be combined with base network rewards or marketing incentives without double-counting accruals.
* **Observable:** every action emits an event and can be retrieved from RPC or the Escrow Gateway for analytics tooling.

**Reward triggers**

* **Escrow-linked rewards:** On escrow `release` the engine checks the active programs for the merchant/business and allocates rewards from their paymaster pools.
* **External awards:** Businesses can directly allocate rewards off-network via the Escrow Gateway, enabling marketing programs for non-escrow activity.
* **Base rewards:** (Optional) network-wide base reward applied during settlement. Toggleable to either accrue at `release` or during funding depending on governance policy.

**Funding invariants**

* Program rewards move `ZNHB` from **Program.Pool → User** (standard token transfer).  
* Mint authority (if any) is governed separately (outside the engine) and is never called by the engine.  
* Paymaster balances must be topped up by the business before reward execution. Insufficient pool balance causes the accrual to be skipped (event + webhook) without impacting escrow completion.

---

## 2) Concepts & Roles

| Actor / Concept | Description | Keys / IDs |
|-----------------|-------------|------------|
| **Business** | An owner address that manages one or more loyalty programs. Responsible for funding paymaster pools and defining program parameters. | `businessID` (bytes32); owner wallet (Bech32 HRP `nhb`). |
| **Merchant** | Receiving address associated with a business. Merchants inherit the business’s programs. | `merchantID` (derived from address); registered via RPC or CLI. |
| **Program** | Reward configuration (basis points, caps, eligibility windows) funded via a paymaster pool. | `programID` (bytes32); references paymaster address. |
| **User** | An address (optionally mapped from username/email via Escrow Gateway) that receives rewards. | Bech32 addresses with HRPs `nhb` or `znhb`. |
| **Paymaster** | Escrow-controlled wallet holding ZNHB reserved for program rewards. | Standard on-chain account managed by the business. |
| **Roles (on-chain)** | Privileged roles that govern advanced functionality. | `ROLE_ARBITRATOR`, `PAYMASTER`, `MINTER_NHB`, `MINTER_ZNHB`. |

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
* `Paymaster` (`nhb…`): pool address holding ZNHB for rewards.
* `TokenSymbol` = `"ZNHB"` (fixed for loyalty payouts).
* `AccrualBps` (`uint32`): basis points applied to eligible settlement amounts (500 = 5%).
* `MinSpendWei` (`*big.Int`): minimum qualifying spend.
* `CapPerTx` (`*big.Int`): maximum reward per transaction.
* `DailyCapUser` (`*big.Int`): maximum reward per user per UTC day.
* `StartTime`, `EndTime` (`int64`): UNIX timestamps bounding program validity.
* `Active` (`bool`): indicates whether accrual logic executes.
* `includeP2P` (`bool`): include P2P escrow releases; default `false`.
* `metadata` (`map[string]string`): optional key/value data surfaced via analytics.

### Global base reward

The chain stores a single `loyalty.GlobalConfig` record that governs the optional
network-wide base reward. Operators can toggle or tune it through governance:

* `Active` (`bool`): when `false`, base rewards are skipped entirely.
* `Treasury` (`[20]byte`): address that funds base payouts.
* `BaseBps` (`uint32`): network default is **5,000 bps (50%)**, minting 0.5 ZNHB for every 1 NHB of qualifying spend.
* `MinSpend`, `CapPerTx`, `DailyCapUser` (`*big.Int`): caps expressed in wei (18 decimal places).

With the default 5,000 bps rate, a settlement of `100 NHB` (`100 * 10^18` wei) accrues
`50 ZNHB` (`50 * 10^18` wei) so long as the treasury holds enough balance and the
per-transaction and daily caps permit it.

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

The loyalty module evaluates active programs for `businessID`, filters by `token` and thresholds, computes rewards, debits paymaster pools, and emits `loyalty.accrued` events per user.

---

## 4) Node JSON-RPC (Loyalty Admin & Read)

All loyalty RPC calls use standard JSON-RPC 2.0 at the node endpoint (default `http://127.0.0.1:8545`). Write operations are executed as on-chain transactions; clients must sign payloads with the invoking wallet. Read operations are free and can use HTTP POST or WebSocket depending on node configuration.

### Authentication & Authorization

* **Authentication:** Provided via wallet signatures inherent in transaction submission. The node verifies that the transaction sender matches the required role (business owner/admin) before state transition.
* **Nonce management:** Clients should fetch the latest account nonce before submitting consecutive admin calls to avoid transaction replay errors.
* **Gas / Fees:** Loyalty transactions follow the same fee schedule as other module calls. Ensure the caller wallet has sufficient NHB for gas.

### Admin (write)

#### `loyalty.createBusiness(ownerBech32, name) -> businessID`
* **Parameters**
  * `ownerBech32` (`string`): address that will own the business.
  * `name` (`string`): optional display name.
* **Requirements:** Transaction sender must equal `ownerBech32`.
* **Returns:** `businessID` (hex-encoded bytes32).
* **Errors:** `INVALID_BECH32`, `BUSINESS_EXISTS`, `UNAUTHORIZED_CALLER`.

#### `loyalty.setPaymaster(businessID, paymasterBech32)`
* Rotates the paymaster pool used for all programs under the business.
* Emits `loyalty.paymaster.rotated` with `{businessID, old, new}`.
* **Checks:** caller is business owner/admin; paymaster must be a valid address.

#### `loyalty.addMerchant(businessID, merchantBech32)` / `loyalty.removeMerchant(...)`
* Adds or removes merchant mappings. Merchants inherit all active programs instantly.
* Removing a merchant stops future accruals but does not claw back existing rewards.

#### `loyalty.createProgram(businessID, ProgramSpecJSON) -> programID`
* `ProgramSpecJSON` includes all fields described in [On-Chain Model](#3-on-chain-model).
* **Validation:** ensures token symbol is `ZNHB`, time windows are valid, caps are non-negative, and paymaster balance >= configured reserve threshold.
* Returns `programID` (hex string).

#### `loyalty.updateProgram(programID, ProgramSpecJSON)`
* Partial updates permitted. Omitted fields remain unchanged.
* Sensitive fields (token symbol, owner) immutable; attempting to change them returns `FIELD_IMMUTABLE`.

#### `loyalty.pauseProgram(programID)` / `loyalty.resumeProgram(programID)`
* Toggles `Active` flag. Paused programs skip accruals but maintain meters for historical reference.
* Emits `loyalty.program.paused` or `loyalty.program.resumed` events.

### Read (dashboard)

#### `loyalty.getBusiness(businessID)`
* Returns business metadata, current paymaster, and merchant list.

#### `loyalty.listPrograms(businessID)`
* Returns an array of active and inactive programs.
* Supports optional pagination parameters: `offset`, `limit` when provided via named params.

#### `loyalty.programStats(programID, dayUTC)`
* Provides aggregated metrics for the specified UTC day (format `YYYY-MM-DD`).
* Fields: `rewardsPaid`, `txCount`, `capUsage`, `skips` (count of skipped accruals).

#### `loyalty.userDaily(userBech32, programID, dayUTC)`
* Returns user-specific meter details for compliance or customer support.

#### `loyalty.paymasterBalance(businessID)`
* Returns the ZNHB balance of the current paymaster pool and reserved amounts (pending awards).

**JSON-RPC cURL example**

```bash
curl -s http://127.0.0.1:8545 -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0",
  "id":1,
  "method":"loyalty.listPrograms",
  "params":["0x<businessID>"]
}'
```

**WebSocket subscription (optional)**

Nodes exposing WebSocket transport support `subscribe` to loyalty events. See RPC docs for `eth_subscribe` usage with filter topic `loyalty.*`.

---

## 5) Escrow Gateway – External Awards API (Off-Network Purchases)

The Escrow Gateway exposes REST APIs for businesses that operate outside the on-chain settlement loop but still want to issue loyalty rewards. Requests require API keys and HMAC signatures. Idempotency is enforced via headers to guarantee at-most-once award issuance.

**Base URL:** service deployment of `services/escrow-gateway` (e.g., `https://api.devnet.nhbcoin.net`).

### Authentication headers

| Header | Description |
|--------|-------------|
| `X-Api-Key` | Public identifier for the client. |
| `X-Timestamp` | RFC3339 timestamp. Skew must be within ±300s of server time. |
| `X-Signature` | Hex-encoded HMAC-SHA256 signature computed as `HMAC(secret, method|path|body|timestamp)`. |
| `Idempotency-Key` | Unique key per request (UUID or hash). Required for POSTs. |
| `X-Sig-Addr` / `X-Sig` | Optional wallet signature headers for endpoints requiring additional authorization. |

Server responses include `X-Request-ID` for tracing and `Replay-After` for rate limiting when `429` is returned.

### Endpoints

#### `POST /external/users/lookup`

* **Request body**
  ```json
  { "externalId": "user@example.com" }
  ```
* **Response**
  ```json
  { "address": "nhb1qxy..." }
  ```
* Returns `address: null` if no mapping exists. Include hashed external IDs in analytics logs to avoid raw PII retention.

#### `POST /external/users/register`

* **Request body**
  ```json
  {
    "externalId": "user@example.com",
    "bech32": "nhb1qxy..."
  }
  ```
* Registers or updates the mapping. The gateway salts and hashes the identifier before persistence.
* Response: `{ "address": "nhb1qxy...", "status": "created|updated" }`.

#### `POST /external/awards`

* **Request body**
  ```json
  {
    "referenceId": "REF123",
    "user": "nhb1qxy...",
    "programId": "0xabc123...",
    "amount": "1000000000000000000",
    "metadata": {
      "campaign": "spring-2025",
      "note": "manual adjustment"
    }
  }
  ```
* **Behavior**
  * Validates `referenceId` uniqueness per API key.
  * Debits the program paymaster and queues on-chain transfer via internal worker.
  * Returns 202 Accepted when asynchronous submission is scheduled; clients should poll status.
* **Response**
  ```json
  {
    "referenceId": "REF123",
    "status": "queued",
    "queuedAt": "2025-03-21T12:30:45Z"
  }
  ```

#### `GET /external/awards/{referenceId}`

* Returns
  ```json
  {
    "referenceId": "REF123",
    "status": "queued|submitted|settled|skipped|reversed",
    "txHash": "0x...",             // when submitted
    "skipReason": "POOL_INSUFFICIENT_FUNDS",
    "amount": "1000000000000000000",
    "programId": "0xabc123...",
    "updatedAt": "2025-03-21T12:35:10Z"
  }
  ```

#### `POST /external/awards/{referenceId}/reverse`

* Initiates reversal when business policy allows clawbacks. Requires wallet signature headers.
* Response contains `status: "reversed"` or `status: "pending"` if asynchronous.

### Webhooks

* **Events**: `award.settled`, `award.skipped`.
* Payload example:
  ```json
  {
    "event": "award.settled",
    "referenceId": "REF123",
    "programId": "0xabc123...",
    "user": "nhb1qxy...",
    "amount": "1000000000000000000",
    "txHash": "0x...",
    "settledAt": "2025-03-21T12:37:21Z"
  }
  ```
* Webhook signatures reuse the HMAC header scheme (`X-Api-Key`, `X-Timestamp`, `X-Signature`). Verify before processing.

### Rate limits

* Default: `60` award submissions per minute per API key; `600` lookup/register requests per minute.
* Bursting beyond rate limits returns `429` with `Retry-After` header. Clients should implement exponential backoff.

---

## 6) CLI – `nhb-cli` (loyalty)

The `nhb-cli` binary ships with subcommands to manage loyalty constructs. Commands implicitly use local keystore accounts unless `--from` is specified. Use `--chain-id 187001` when targeting devnet.

```bash
# Create business
nhb-cli loyalty create-business --owner nhb1... --name "Zenith Hotels"

# Set paymaster
nhb-cli loyalty set-paymaster --business 0x... --paymaster nhb1...

# Add merchant
nhb-cli loyalty add-merchant --business 0x... --merchant nhb1...

# Create program
nhb-cli loyalty create-program --business 0x... --spec ./program.json

# Update program (partial)
nhb-cli loyalty update-program --program 0x... --spec ./program-update.json

# Pause / Resume
nhb-cli loyalty pause --program 0x...
nhb-cli loyalty resume --program 0x...

# Stats
nhb-cli loyalty stats --program 0x... --day 2025-09-22

# User meter lookup
nhb-cli loyalty user-daily --program 0x... --user nhb1... --day 2025-09-22
```

**CLI configuration tips**

* Use `--node http://127.0.0.1:8545` to override default RPC URL.
* Set `NHBCHAIN_KEYRING_PASS` env var for unattended scripts.
* Combine with `jq` to parse JSON output for automation pipelines.

---

## 7) Events & Analytics

Events are emitted both on-chain and via the Escrow Gateway for downstream ingestion.

| Event | Description | Payload fields |
|-------|-------------|----------------|
| `loyalty.accrued` | Reward successfully applied to a user. | `{ program, user, merchant, token, amount, bps, escrowId, txHash }` |
| `loyalty.skipped` | Reward skipped due to validation failure or insufficient funds. | `{ program, user, reason, ctx }` |
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
* Programs cannot overdraft paymaster pools. When funds are insufficient, the accrual is skipped and flagged. Businesses should monitor balances via `loyalty.paymasterBalance`.
* Daily and per-transaction caps are enforced at the time of accrual; updates to caps affect only future accruals.

### Audit & Retention

* Escrow Gateway maintains an append-only audit log with request hash, actor, RPC response, and blockchain transaction hash.
* Retain logs for a minimum of **7 years** or as required by jurisdictional regulations.
* Provide auditors with read-only API keys or offline exports from RPC and gateway logs. Use hashed identifiers when sharing user-level data.

### Privacy & Data Handling

* External ID mappings use salted hashes; salts are rotated periodically. Store salts in a secure secret manager.
* Do not persist raw PII outside secure, access-controlled systems. Ensure webhook consumers follow the same policy.

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
7. **Event handling:** `loyalty.accrued` event and optional webhook notify downstream systems.
8. **Reporting:** Use `loyalty.programStats` and `loyalty.userDaily` to reconcile payouts.

### External Award (Off-Network) via Escrow Gateway

1. **Register user** mapping (`POST /external/users/register`).
2. **Submit award** with unique `referenceId` via `POST /external/awards`.
3. **Poll status** using `GET /external/awards/{referenceId}` until `settled` or `skipped`.
4. **Handle webhook** if configured, confirming final state.
5. **Reversals** (if necessary) using `POST /external/awards/{referenceId}/reverse` with wallet signature.

### Automation example (cURL + CLI)

```bash
# 1. Lookup address for external user
curl -s "$GATEWAY/external/users/lookup" \
  -H "Content-Type: application/json" \
  -H "X-Api-Key: $API_KEY" \
  -H "X-Timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -H "Idempotency-Key: $(uuidgen)" \
  -H "X-Signature: $(./scripts/sign-hmac.sh lookup /external/users/lookup '{"externalId":"user@example.com"}')" \
  -d '{"externalId":"user@example.com"}'

# 2. Submit award once escrow release confirmed
curl -s "$GATEWAY/external/awards" \
  -H "Content-Type: application/json" \
  -H "X-Api-Key: $API_KEY" \
  -H "X-Timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -H "Idempotency-Key: REF123" \
  -H "X-Signature: $(./scripts/sign-hmac.sh post /external/awards '{"referenceId":"REF123","user":"nhb1...","programId":"0x...","amount":"1000000000000000000"}')" \
  -d '{"referenceId":"REF123","user":"nhb1...","programId":"0x...","amount":"1000000000000000000"}'

# 3. Query program stats for daily reconciliation
nhb-cli loyalty stats --program 0x... --day $(date -u +%Y-%m-%d)
```

---

## 10) Errors & Troubleshooting

### JSON-RPC error codes

| Code | Description | Recommended action |
|------|-------------|--------------------|
| `INVALID_BECH32` | Address fails Bech32 validation. | Verify HRP (`nhb`/`znhb`) and checksum. |
| `UNKNOWN_PROGRAM` | Program ID not found or belongs to another business. | List programs and confirm ID. |
| `POOL_INSUFFICIENT_FUNDS` | Paymaster balance below required amount. | Top up ZNHB; use `loyalty.paymasterBalance`. |
| `CAP_EXCEEDED_TX` | Reward exceeds per-transaction cap. | Adjust program caps or split transaction. |
| `CAP_EXCEEDED_DAILY` | User reached daily maximum. | Inform customer; resets at UTC midnight. |
| `UNAUTHORIZED_CALLER` | Caller lacks required role or ownership. | Sign transaction with authorized wallet. |

### REST error model

* Standard JSON object:
  ```json
  {
    "error": {
      "code": "POOL_INSUFFICIENT_FUNDS",
      "message": "Paymaster balance below required minimum",
      "details": {"required": "1000000000000000000", "available": "0"}
    },
    "idempotencyKey": "REF123"
  }
  ```
* HTTP status mapping: `400` (validation), `401/403` (auth), `409` (duplicate idempotency key), `422` (business rule), `429` (rate limit), `500` (unexpected).

### Troubleshooting checklist

* **Signature mismatch:** ensure canonical JSON encoding (no whitespace changes) when computing HMAC; confirm server timestamp tolerance.
* **Delayed settlement:** check worker queue health; use gateway status endpoint for updates.
* **High skip rate:** monitor `loyalty.skipped` events; inspect `reason` field for patterns (caps, balance, inactive program).
* **Program not applying to merchant:** verify merchant is registered to the business and program `StartTime/EndTime` encompasses settlement timestamp.
* **Module paused:** run `go run ./examples/docs/ops/read_pauses` to confirm the loyalty flag is `false`; resume with `go run ./examples/docs/ops/pause_toggle --module loyalty --state resume` when cleared by governance.
* **Cap rejections:** inspect the program meters via `loyalty.programStats` to see `capUsage` and compare against configured per-transaction / daily caps before retrying.

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
