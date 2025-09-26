# Hardened Escrow Engine Routing

## Purpose

The hardened escrow engine replaces the prototype "legacy" state transition logic
with a deterministic, audited implementation that shares the same code paths used
by the RPC gateway and custody services. Routing all native escrow transactions
through the engine guarantees:

- **Single source of truth.** Every escrow lifecycle event is handled by the
  hardened engine, ensuring identical behaviour for RPC-triggered and
  consensus-triggered flows.
- **Deterministic accounting.** Balances, vault credits, and fee routing are
  executed by the engine against the canonical state manager, eliminating custom
  bookkeeping in the state processor.
- **Forward compatibility.** Native transactions can exercise new engine
  features (disputes, mediation, atomic trade settlement) without additional
  protocol changes.
- **Transparent migration.** Historical `escrow-<id>` trie entries are migrated
  lazily into the new storage layout the first time they are touched, avoiding
  disruptive network upgrades.

## Design Overview

1. **State processor wrapper.** `StateProcessor.configureTradeEngine` now wires
   both the escrow engine and the trade engine against `core/state.Manager`,
   configures the fee treasury and clock source, and registers an event emitter
   so consensus events are produced identically to RPC flows.
2. **Native transaction handlers.** The `apply*Escrow` handlers convert the
   transaction payloads into engine calls:
   - `TxTypeCreateEscrow` → `Engine.Create`
   - `TxTypeLockEscrow`   → `Engine.Fund`
   - `TxTypeReleaseEscrow`→ `Engine.Release`
   - `TxTypeRefundEscrow` → `Engine.Refund`
   - `TxTypeDisputeEscrow`→ `Engine.Dispute`
   - `TxTypeArbitrate*`   → `Engine.Resolve` (validates the `ROLE_ARBITRATOR`
     committee stored with the escrow)
   After the engine call succeeds the sender nonce is incremented using the
   freshly persisted account state.
3. **Fee treasury management.** `StateProcessor.SetEscrowFeeTreasury` allows the
   node to configure the address that receives release fees (wired during node
   start-up). The engine refuses to release funds if the treasury is unset,
   ensuring fee routing remains explicit.
4. **Legacy migration.** When an engine operation cannot find a modern escrow
   record the state processor attempts a one-off migration:
   - The legacy RLP payload stored at `keccak("escrow-" || id)` is decoded into
     an `escrow.LegacyEscrow`.
   - The data is normalised into an `escrow.Escrow` with default mediator,
     deadlines, and status mapping.
   - Funds that were implicitly "burned" in the prototype are re-materialised in
     the deterministic escrow vault accounts so future releases/refunds mirror
     hardened engine semantics.
   - The legacy key is cleared to prevent double migrations.
5. **Trade integration.** The trade engine shares the same configuration path,
   so dual-leg trades automatically react to escrow funding updates emitted from
   native transactions.

## Native Transaction Payloads

| Transaction Type         | Payload Fields |
|--------------------------|----------------|
| `TxTypeCreateEscrow`     | `payee` (`[]byte`), `token` (`"NHB"`/`"ZNHB"`), `amount` (`big.Int`), `feeBps` (`uint32`), `deadline` (`int64`), optional `mediator` (`[]byte`), optional `meta` (`[]byte <=32`). |
| `TxTypeLockEscrow`       | `data` = escrow ID (`[32]byte`). |
| `TxTypeReleaseEscrow`    | `data` = escrow ID (`[32]byte`), caller must be payee or mediator. |
| `TxTypeRefundEscrow`     | `data` = escrow ID (`[32]byte`), caller must be payer prior to deadline. |
| `TxTypeDisputeEscrow`    | `data` = escrow ID (`[32]byte`), caller must be payer or payee. |
| `TxTypeArbitrateRelease` | `data` = escrow ID (`[32]byte`), caller must satisfy the `ROLE_ARBITRATOR` committee threshold recorded on the escrow. |
| `TxTypeArbitrateRefund`  | Same as above; outcome instructs the engine to refund the payer after committee approval. |

The engine resolves arbitration transactions by loading the escrow's frozen
arbitrator policy—which captures the committee membership and signing
threshold at creation time—and verifying that the native transaction sender is
authorised. The policy is persisted alongside the escrow record when the
`Create` operation runs so later `TxTypeArbitrate*` submissions can enforce the
same governance-managed committee the RPC flows rely on. See
[`docs/rpc_escrow_module.md`](../rpc_escrow_module.md) and
[`docs/escrow/escrow.md`](./escrow.md) for additional context on how arbitrator
policies are registered and frozen during escrow creation.

### Example Create Payload

```json
{
  "payee": "\u0001...20-byte...",
  "token": "NHB",
  "amount":  "1000000000000000000",
  "feeBps": 100,
  "deadline": 1735689600,
  "mediator": "\u0000...optional...",
  "meta": "\u0012\u0034...optional..."
}
```

### Escrow ID Derivation

The engine derives the escrow identifier deterministically as:

```
escrowID = keccak256(payer || payee || metaHash)
```

Because native transactions now defer creation to the engine, clients can
pre-compute IDs using the same rule, guaranteeing they match the stored record.

## Behavioural Guarantees

- **Nonce management:** Sender nonces are incremented after the engine mutates
  state, ensuring account balances updated by the engine are preserved when the
  nonce is written back.
- **Vault accounting:** Funds locked by legacy escrows are credited into the
  deterministic module vault during migration so future releases operate on real
  balances rather than implicit debits.
- **Event parity:** All engine calls emit the same `types.Event` payloads used by
  the RPC services, allowing observers to rely on a single event schema.
- **Idempotency:** Engine methods remain idempotent; repeated fund/release calls
  are no-ops after the terminal state is reached, matching RPC behaviour.

## Examples

1. **Native single-leg escrow:**
   - Create escrow with JSON payload embedded in `TxTypeCreateEscrow`.
   - Payer submits `TxTypeLockEscrow` once funds are deposited on-chain.
   - Payee finalises with `TxTypeReleaseEscrow`, triggering fee routing to the
     configured treasury.

2. **Legacy dispute resolution:**
   - A historical `escrow-<id>` entry is encountered when a validator submits a
     `TxTypeArbitrateRelease` transaction.
   - Migration converts the record, rehydrates the vault balance, and
     `Engine.Resolve` releases the funds once the arbitrator committee
     authorises the outcome while recording the modern escrow status.

3. **Trade escrow update:**
   - When an escrow leg is funded via `TxTypeLockEscrow`, the trade engine (bound
     through `configureTradeEngine`) receives the funding event and advances the
     trade status to `TradeFunded` once both legs are complete.

By funnelling all native transactions through the hardened engine, the network
benefits from consistent validation logic, richer telemetry, and automatic
support for future escrow and trade extensions.
