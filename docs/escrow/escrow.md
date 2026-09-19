# NHBChain Escrow

This document describes the escrow module as implemented in `native/escrow`, the state processor handlers in `core/state_transition.go`, and the RPC surface in `rpc/`. The escrow module holds funds in a per-token vault until a payee release, a payer refund, an expiry, or an arbitration decision moves them out.

Related documents: [`hardened-engine.md`](./hardened-engine.md) (transaction routing and legacy migration), [`milestones.md`](./milestones.md), [`trade.md`](./trade.md), [`gateway-api.md`](./gateway-api.md), [`nhbchain-escrow-gateway.md`](./nhbchain-escrow-gateway.md), [`mint-settlement.md`](./mint-settlement.md).

---

## 1. Module overview

* **Tokens.** Only `NHB` and `ZNHB` are accepted (`native/escrow/types.go`, `defaultTokenRegistry`). Symbols are trimmed and upper-cased; anything else fails with `unsupported escrow token`.
* **Transitions are idempotent.** Calling a transition that has already been applied returns success without changing state (`Engine.Fund`, `Release`, `Refund`, `Expire`, `Dispute`, `Resolve` in `native/escrow/engine.go`). There is no client-supplied idempotency key.
* **Writes are signed transactions.** The `escrow_create`, `escrow_fund`, `escrow_release`, `escrow_refund`, `escrow_expire`, `escrow_dispute` and `escrow_resolve` JSON-RPC methods are permanently disabled; they answer HTTP 410 with error code `-32060` (`rpc/escrow_handlers.go`, `escrowRPCDisabledMessage`). Every state change goes through a signed transaction submitted with `nhb_sendTransaction` (section 4).
* **Pause switch.** Every engine transition first checks the `escrow` module pause flag (`nativecommon.Guard(e.pauses, "escrow")`).

---

## 2. Data model

### 2.1 Escrow statuses

`native/escrow/types.go` defines six statuses. The RPC returns them as lowercase strings (`rpc/escrow_handlers.go`, `escrowStatusString`).

| Value | Constant | RPC string | Terminal |
|-------|----------|------------|----------|
| 0 | `EscrowInit` | `init` | no |
| 1 | `EscrowFunded` | `funded` | no |
| 2 | `EscrowReleased` | `released` | yes |
| 3 | `EscrowRefunded` | `refunded` | yes |
| 4 | `EscrowExpired` | `expired` | yes |
| 5 | `EscrowDisputed` | `disputed` | no |

There is no "funding", "resolved" or "cancelled" status. A resolved dispute ends as `released` or `refunded`; the decision is recorded in the `escrow.resolved` event and in the escrow's `resolutionHash`.

Allowed transitions (from `native/escrow/engine.go`):

| From | Action | To | Who may call |
|------|--------|----|--------------|
| (none) | create | `init` | the payer (sender of `TxTypeCreateEscrow`, or the signer of a delegated create) |
| `init` | fund (`Fund`) | `funded` | the payer only |
| `funded` | release (`Release`) | `released` | the payee, or the mediator if one is set |
| `funded` | refund (`Refund`) | `refunded` | the payer, and only while `now < deadline` |
| `funded` | expire (`Expire`) | `expired` | anyone, once `now >= deadline` |
| `funded` | dispute (`Dispute`) | `disputed` | the payer or the payee |
| `disputed` | release (`Release`) | `released` | the mediator only |
| `disputed` | committee decision (`ResolveWithSignatures`) | `released` or `refunded` | a quorum of the escrow's frozen arbitrators |

Notes:

* `Expire` on an escrow that is not `funded` (and not already `expired`) is a no-op once the deadline has passed; before the deadline it fails with `escrow: deadline not reached`.
* A `disputed` escrow cannot be expired or refunded by the payer.
* `Engine.Resolve` (single mediator plus `release`/`refund` outcome string) exists in the package but no transaction type or RPC calls it; on-chain resolution is `ResolveWithSignatures` only (section 5).

### 2.2 Escrow ID

```
escrowID = keccak256(payer || payee || metaHash || nonce)
```

`payer` and `payee` are the 20-byte addresses, `metaHash` is the 32-byte meta value (zero if none), and `nonce` is a mandatory positive `uint64` encoded as 8 bytes big-endian (`Engine.Create`). Creating the same definition twice returns the existing escrow; the same ID with a different definition fails with `escrow: identifier already exists with different definition`. The escrow `nonce` is unrelated to the account transaction nonce.

### 2.3 Create-time validation (`Engine.Create`)

* `nonce > 0`; `amount > 0`; `feeBps <= 10000`.
* `deadline >= now` (block time on-chain). A deadline in the past fails with `escrow: deadline before creation time`.
* `applyCreateEscrow` additionally requires a 20-byte payee, a non-empty token, `deadline > 0`, a 20-byte mediator if one is supplied, and `meta` of at most 32 bytes.
* If `realm` is set, the realm must exist and the engine freezes its current arbitrator policy into the escrow (section 5).

### 2.4 Reference types

```go
type Escrow struct {
  ID             [32]byte
  Payer          [20]byte
  Payee          [20]byte
  Mediator       [20]byte   // zero value = no mediator
  Token          string     // "NHB" or "ZNHB"
  Amount         *big.Int
  FeeBps         uint32
  Deadline       int64
  CreatedAt      int64
  Nonce          uint64
  MetaHash       [32]byte
  Status         EscrowStatus
  RealmID        string
  FrozenArb      *FrozenArb // set only when created against a realm
  ResolutionHash [32]byte   // keccak256 of the applied decision payload
  DisputeReason  string
}
```

Storage lives in the state manager (`core/state`): the escrow record, a per-escrow vault balance (`EscrowCredit`/`EscrowDebit`/`EscrowBalance`), and the frozen policy. Funds sit at the vault address returned by `EscrowVaultAddress(token)`.

---

## 3. Money flow and fees

* **Fund** (`Fund`): moves `amount` from the payer to the token vault and credits the escrow's vault balance.
* **Release** (`Release`): `fee = amount * feeBps / 10000` (integer division, rounded down). The payee receives `amount - fee`; the fee goes to the node's configured escrow fee treasury. If the fee is greater than zero and no treasury is configured, release fails with `escrow engine: fee treasury not configured`.
* **Release or refund of a `disputed` escrow** (`computeDisputePayouts`): the payout is `amount - fee - realmFee`, where `fee` uses the escrow's `feeBps` and `realmFee = amount * frozenSchedule.FeeBps / 10000` when the frozen policy carries a fee schedule. The realm fee is paid to the schedule's recipient. If the fees exceed the amount the call fails. A refund of a `funded` (not disputed) escrow returns the full amount with no fee.
* **Refund / Expire** of a `funded` escrow: the full amount returns to the payer, no fee.

The escrow fee treasury is set on the state processor (`SetEscrowFeeTreasury`) when the node starts (`core/node.go`).

---

## 4. Transactions

Write access is through these transaction types (`core/types/transaction.go`). The transaction `Type` is the numeric value shown.

| Type | Value | `tx.Data` | Authorization |
|------|-------|-----------|---------------|
| `TxTypeCreateEscrow` | `0x03` | JSON (or RLP) object: `payee` (20 bytes), `token`, `amount`, `feeBps`, `deadline`, `nonce`, optional `mediator` (20 bytes), optional `meta` (up to 32 bytes), optional `realm` | The sender becomes the payer. |
| `TxTypeReleaseEscrow` | `0x04` | the 32-byte escrow ID | Sender must be the payee or the mediator (mediator only, if disputed). |
| `TxTypeRefundEscrow` | `0x05` | the 32-byte escrow ID | Sender must be the payer, before the deadline. |
| `TxTypeLockEscrow` | `0x09` | the 32-byte escrow ID | Sender must be the payer; this is the funding step (`Engine.Fund`). |
| `TxTypeDisputeEscrow` | `0x0A` | the 32-byte escrow ID | Sender must be the payer or payee. Carries no reason. |
| `TxTypeArbitrateRelease` | `0x0B` | RLP `{escrowId string, decision bytes, signatures []string}` | Committee signatures (section 5); the transaction sender is not checked. |
| `TxTypeArbitrateRefund` | `0x0C` | same as above | same as above; the outcome comes from the signed decision, not from the transaction type. |
| `TxTypeExpireEscrow` | `0x3B` | the 32-byte escrow ID | Anyone. |
| `TxTypeDelegatedReleaseEscrow` | `0x3C` | RLP `{escrowId string, payload bytes, signature bytes}` | Signature of a participant embedded in the payload (below). |
| `TxTypeDelegatedRefundEscrow` | `0x3D` | same | same |
| `TxTypeDelegatedDisputeEscrow` | `0x3E` | same | same |
| `TxTypeEscrowCreateRealm` | `0x3F` | JSON (or RLP) realm definition (section 5) | Sender must hold `ROLE_ESCROW_REALM_ADMIN`. |
| `TxTypeEscrowUpdateRealm` | `0x40` | same | same |
| `TxTypeDelegatedCreateEscrow` | `0x41` | RLP `{payload bytes, signature bytes}` | The payer's signature embedded in the payload. |

JSON encoding notes for `TxTypeCreateEscrow` (`applyCreateEscrow`): `payee`, `mediator` and `meta` are Go `[]byte` fields, so as JSON they are base64 strings; `amount` is a JSON number. The `nhb-cli escrow create` command builds this payload for you.

Each accepted escrow transaction except the arbitration types increments the sender's account nonce (`applyArbitrate` does not, see `core/state_transition.go`), and is counted against the `escrow` module quota (`applyQuota(moduleEscrow, ...)`); arbitration transactions count against the `trade` module quota, and the two realm transactions are not quota-gated.

Escrow IDs in `tx.Data` for release/refund/lock/dispute/expire are the raw 32 bytes (`decodeEscrowID`).

### 4.1 Delegated (signed-envelope) actions

A relayer can submit a release, refund, dispute or create on a participant's behalf. The chain authorizes against the signature embedded in the payload, not against the transaction sender (`applyDelegatedEscrowAction`, `applyDelegatedCreateEscrow`). Signatures are 65-byte secp256k1 signatures over `keccak256(payload)` (no EIP-191 prefix); the recovery byte may be 0/1 or 27/28 (`RecoverSigner`).

* Release / refund / dispute payload (`escrowActionEnvelope`):

  ```json
  {"escrowId":"<64 hex>","action":"release|refund|dispute","reason":"<optional, dispute only>"}
  ```

  `escrowId` must equal the target ID and `action` must equal the transaction's action. The recovered signer is then used exactly as if it had sent the direct transaction, so the same role rules apply (payee/mediator for release, payer for refund, payer/payee for dispute).
* Create payload (`escrowCreateEnvelope`):

  ```json
  {"action":"create","payer":"<40 hex>","payee":"<40 hex>","token":"NHB","amount":"<decimal>",
   "feeBps":0,"deadline":1730000000,"nonce":1,"mediator":"<40 hex, optional>","meta":"<64 hex, optional>","realm":"<optional>"}
  ```

  `payer` must equal the recovered signer. Addresses are raw hex of the 20 address bytes, not bech32.

Replay safety comes from idempotent status transitions; there is no separate nonce registry for signed envelopes.

### 4.2 Legacy records

If an escrow ID is not found in the modern store, the state processor looks for a legacy record at `keccak256("escrow-" || id)` and migrates it once (`migrateLegacyEscrow`). See [`hardened-engine.md`](./hardened-engine.md).

---

## 5. Realms and arbitration

A realm is a named arbitrator committee. Realms are created and updated only by holders of `ROLE_ESCROW_REALM_ADMIN` (`RoleEscrowRealmAdmin`, `core/state_transition.go`).

### 5.1 Realm definition

`TxTypeEscrowCreateRealm`/`TxTypeEscrowUpdateRealm` payload (`decodeEscrowRealmPayload`, JSON or RLP):

| Field | Meaning |
|-------|---------|
| `id` | Realm identifier, required. |
| `scheme` | `1` = single, `2` = committee. |
| `threshold` | Signatures required. Must be positive and not exceed the member count. |
| `members` | Bech32 arbitrator addresses, at least one, none zero. |
| `scope` | `1` = platform, `2` = marketplace. |
| `providerProfile` | Required free text, at most 512 characters. |
| `feeBps`, `feeRecipient` | Optional dispute fee schedule (`RealmFeeSchedule`); `feeBps <= 10000`; a recipient is required when `feeBps > 0`. |
| `arbitrationFeeBps`, `feeRecipientBech32` | Metadata mirror of the fee; `arbitrationFeeBps <= 10000`; a recipient is required when it is greater than zero. |

Create fails if the realm already exists; update fails if it does not. An update replaces the arbitrator set, bumps `version` by one, and replaces the fee schedule.

Governance bounds (parameter store keys in `native/escrow/types.go`): `escrow.realm.MinThreshold` (default 1), `escrow.realm.MaxThreshold` (default 10) and `escrow.realm.AllowedSchemes` (default single and committee). The arbitrator set must have at least `MinThreshold` members and the threshold must lie within the bounds.

### 5.2 Frozen policy

When an escrow is created with `realm`, `Engine.prepareFrozenPolicy` copies the realm's members, threshold, scheme, fee schedule, metadata, realm `version` and a `policyNonce` (the realm's `nextPolicyNonce`, which then increments) into `FrozenArb`. Later realm updates do not change existing escrows.

### 5.3 Resolving a dispute

`TxTypeArbitrateRelease` / `TxTypeArbitrateRefund` call `Engine.ResolveWithSignatures`:

1. The escrow must be `disputed` (already released, refunded or expired escrows return success without change) and must have a frozen policy.
2. `decision` is a JSON envelope: `{"escrowId":"<64 hex>","outcome":"release|refund","policyNonce":<uint>,"metadata":"<optional 64 hex>"}`. `policyNonce` must be non-zero and equal the escrow's frozen `policyNonce`.
3. Each entry of `signatures` is a 65-byte signature (hex, optional `0x`) over `keccak256(decision)`. Every signer must be a frozen member; duplicates count once; the number of distinct signers must reach the frozen `threshold`.
4. `release` pays the payee and `refund` pays the payer, both after the dispute fee rules in section 3. The digest is stored as `resolutionHash`. Once the escrow is `released`, `refunded` or `expired`, any later decision (the same one or a different one) is ignored and the call returns success without changing anything (`native/escrow/engine.go`, `ResolveWithSignatures` early return). If a resolution fails part-way, the previous `resolutionHash` is restored.

An escrow created without a realm has no frozen policy and cannot be resolved this way (`escrow: missing frozen arbitrator policy`). A mediator can still release a disputed escrow with `TxTypeReleaseEscrow`.

---

## 6. Events

Escrow events are emitted by the state processor into the block's event list. Type strings (`native/escrow/events.go`):

`escrow.created`, `escrow.funded`, `escrow.released`, `escrow.refunded`, `escrow.expired`, `escrow.disputed`, `escrow.resolved`, `escrow.realm.created`, `escrow.realm.updated`, `escrow.trade.created`, `escrow.trade.partial_funded`, `escrow.trade.funded`, `escrow.trade.disputed`, `escrow.trade.resolved`, `escrow.trade.settled`, `escrow.trade.expired`, `escrow.milestone.created`, `escrow.milestone.funded`, `escrow.milestone.released`, `escrow.milestone.cancelled`, `escrow.milestone.leg_due`.

All escrow events (`escrow.created` through `escrow.resolved`) carry these attributes, all as strings: `id`, `payer`, `payee` (lowercase hex of the 20 bytes, no `0x`), `token`, `amount`, `feeBps`, `createdAt`, `nonce`; plus `mediator`, `disputeReason`, `realmId` when set; and, for realm-bound escrows, `realmVersion`, `policyNonce`, `arbScheme`, `arbThreshold`, `arbitrators`, `realmScope`, `realmProfile`, `realmFeeBps`, `realmFeeRecipient`. `escrow.resolved` adds `decision` (`release` or `refund`), `decisionMetadata` and `decisionSigners` when present. An `escrow.disputed` event is emitted again if a reason is supplied later for an already-disputed escrow that had none.

Realm events carry `realmId`, `version`, `nextNonce`, `createdAt`, `updatedAt`, `arbScheme`, `arbThreshold`, `arbitrators` and the realm metadata attributes.

Trade and milestone event attributes are described in [`trade.md`](./trade.md) and [`milestones.md`](./milestones.md).

---

## 7. JSON-RPC (read methods)

Requests use the node's JSON-RPC envelope with `params` as an array holding one object, for example `{"jsonrpc":"2.0","id":1,"method":"escrow_get","params":[{"id":"0x..."}]}`.

| Method | Params | Result |
|--------|--------|--------|
| `escrow_get` | `{"id": "<64 hex, optional 0x>"}` | Escrow object: `id`, `payer`, `payee`, `mediator?`, `token`, `amount` (decimal string), `feeBps`, `deadline`, `createdAt`, `nonce`, `status`, `meta` (`0x` + 64 hex), `disputeReason?`, and for realm-bound escrows `realm`, `realmVersion`, `policyNonce`, `arbScheme` (number), `arbThreshold`, `frozenAt`, `arbitrators` (`rpc/escrow_handlers.go`, `escrowJSON`). Addresses are bech32 (`nhb1...`). |
| `escrow_getSnapshot` | `{"id": ...}` | Same core fields plus `frozenPolicy` (scheme as `single`/`committee`, threshold, members, metadata) and `resolutionHash?` (`rpc/modules/escrow.go`). |
| `escrow_getRealm` | `{"id": "<realm id>"}` | `id`, `version`, `nextPolicyNonce`, `createdAt`, `updatedAt`, `arbitrators` (`scheme`, `threshold`, `members`), `metadata` (`scope`, `providerProfile`, `arbitrationFeeBps`, `feeRecipient?`). Unknown realm: HTTP 404, code `-32602`, message `realm not found`. |
| `escrow_listEvents` | optional `{"prefix": "escrow.", "limit": N}` | Events currently held in the node's in-memory event buffer whose type starts with the prefix (default `escrow.`), each `{sequence, type, attributes}`; `sequence` is the position within this response, not a stable cursor. |
| `escrow_milestone*` | see [`milestones.md`](./milestones.md) | |

`escrow_get` reports unknown IDs with HTTP 404 and code `-32022`.

### Error codes

| Code | Meaning (`rpc/escrow_handlers.go`, `rpc/http.go`) |
|------|------|
| `-32602` | Invalid parameters on the module-backed methods (`escrow_getRealm`, `escrow_getSnapshot`, `escrow_listEvents`). |
| `-32021` | Invalid parameters (`escrow_get` and milestone methods). |
| `-32022` | Escrow not found. |
| `-32023` | Forbidden. |
| `-32024` | Conflict (transition not valid from the current status, identifier already exists). |
| `-32025` | Internal error. |
| `-32060` | Method disabled (the write `escrow_*` methods and `p2p_createTrade`/`p2p_settle`/`p2p_dispute`/`p2p_resolve`). |
| `-32010`, `-32030` | Returned by `nhb_sendTransaction`: duplicate transaction, mempool full. |

Transaction-level failures (for example `escrow: unauthorized release caller`) surface when the block is executed, as plain Go error strings.

---

## 8. Command line

`nhb-cli escrow <command>` (`cmd/nhb-cli/escrow_cmd.go`). Every command that writes takes `--key <path to private key file>`; the signer is the actor.

| Command | Flags | Transaction |
|---------|-------|-------------|
| `create` | `--payee`, `--token` (NHB or ZNHB), `--amount` (accepts `100e18` shorthand), `--fee-bps`, `--deadline` (`+duration` such as `+72h` or `+3d`, or RFC3339), `--nonce`, `--key`; optional `--mediator`, `--meta` (0x hex, at most 32 bytes), `--realm` | `TxTypeCreateEscrow`; prints the computed escrow ID |
| `get` | `--id` | `escrow_get` (read-only) |
| `fund` | `--id`, `--key` | `TxTypeLockEscrow` |
| `release` | `--id`, `--key` | `TxTypeReleaseEscrow` |
| `refund` | `--id`, `--key` | `TxTypeRefundEscrow` |
| `expire` | `--id`, `--key` | `TxTypeExpireEscrow` |
| `dispute` | `--id`, `--key` | `TxTypeDisputeEscrow` (no reason is attached) |
| `create-realm` | `--id`, `--members` (comma-separated bech32), `--provider-profile`, `--key`; optional `--threshold` (default 1), `--scheme` (`single`/`committee`, default `single`), `--scope` (`platform`/`marketplace`, default `platform`), `--fee-bps` + `--fee-recipient`, `--arbitration-fee-bps` + `--fee-recipient-bech32` | `TxTypeEscrowCreateRealm` |
| `resolve` | none | Not available; prints an error explaining that resolution needs committee signatures |

Funding is a separate step from creation: after `create`, the payer must send `fund` (`TxTypeLockEscrow`). Sending tokens to the vault address by an ordinary transfer does not mark an escrow funded.
