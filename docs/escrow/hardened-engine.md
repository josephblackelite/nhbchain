# Escrow Engine Routing

## Purpose

All native escrow transactions are applied by the escrow engine in `native/escrow/engine.go`. The state processor (`core/state_transition.go`) decodes each transaction into an engine call, so one implementation serves every path. There is no separate "legacy" transition logic; only a one-time migration of old records remains (see Legacy migration below).

For the status machine, fees and transaction table see [`escrow.md`](./escrow.md).

## How a transaction reaches the engine

1. **State processor wiring.** `StateProcessor.configureTradeEngine` (`core/state_transition.go`) binds the escrow engine and the trade engine to the state manager (`core/state`), sets the fee treasury and the clock (block time), and installs an emitter that appends engine events to the block's event list.
2. **Handlers.** Each escrow transaction type maps to one engine call:

   | Transaction type | Engine call |
   |------------------|-------------|
   | `TxTypeCreateEscrow` | `Engine.Create` (sender is the payer) |
   | `TxTypeLockEscrow` | `Engine.Fund` |
   | `TxTypeReleaseEscrow` | `Engine.Release` |
   | `TxTypeRefundEscrow` | `Engine.Refund` |
   | `TxTypeDisputeEscrow` | `Engine.Dispute` (empty reason) |
   | `TxTypeExpireEscrow` | `Engine.Expire` (block time) |
   | `TxTypeArbitrateRelease`, `TxTypeArbitrateRefund` | `Engine.ResolveWithSignatures` |
   | `TxTypeDelegatedCreateEscrow` | `Engine.CreateWithSignature` |
   | `TxTypeDelegatedReleaseEscrow`, `...RefundEscrow`, `...DisputeEscrow` | `Engine.ReleaseWithSignature`, `RefundWithSignature`, `DisputeWithSignature` |
   | `TxTypeEscrowCreateRealm`, `TxTypeEscrowUpdateRealm` | `Engine.CreateRealm`, `Engine.UpdateRealm` (after a `ROLE_ESCROW_REALM_ADMIN` check) |

   After the engine call succeeds the sender's account nonce is incremented on the freshly persisted account (`updateSenderNonce`), so balance changes made by the engine are preserved. The arbitration handler (`applyArbitrate`) also advances the sender nonce after `ResolveWithSignatures` succeeds, although the sender is not authorized by the transaction.
3. **Fee treasury.** `StateProcessor.SetEscrowFeeTreasury` sets the address that receives escrow fees; the node wires it when it starts (`core/node.go`). The engine returns `escrow engine: fee treasury not configured` if a release or refund has a non-zero fee and no treasury is set.
4. **Trade engine.** The trade engine is constructed and configured in the same place, but no escrow transaction handler notifies it: `applyLockEscrow` calls only `Engine.Fund`. See [`trade.md`](./trade.md).

## Transaction payloads

See [`escrow.md`](./escrow.md) section 4 for the full table. In short:

* `TxTypeCreateEscrow` (`0x03`): JSON or RLP object `payee`, `token`, `amount`, `feeBps`, `deadline`, `nonce`, optional `mediator`, `meta`, `realm`. `payee`, `mediator` and `meta` are byte fields (base64 in JSON). `nonce` is required and must be greater than zero.
* `TxTypeLockEscrow` (`0x09`), `TxTypeReleaseEscrow` (`0x04`), `TxTypeRefundEscrow` (`0x05`), `TxTypeDisputeEscrow` (`0x0A`), `TxTypeExpireEscrow` (`0x3B`): `tx.Data` is the raw 32-byte escrow ID.
* `TxTypeArbitrateRelease` (`0x0B`) and `TxTypeArbitrateRefund` (`0x0C`): RLP `{escrowId, decision, signatures}`. The outcome is taken from the signed `decision` JSON, not from which of the two types is used. Authorization is a quorum of the escrow's frozen arbitrators, not the transaction sender.

### Example create payload

```json
{
  "payee": "<base64 of the 20 payee address bytes>",
  "token": "NHB",
  "amount": 1000000000000000000,
  "feeBps": 100,
  "deadline": 1735689600,
  "nonce": 1,
  "mediator": "<optional, base64 of 20 bytes>",
  "meta": "<optional, base64 of up to 32 bytes>",
  "realm": "<optional realm id>"
}
```

`nhb-cli escrow create` builds and signs this payload; use it instead of assembling JSON by hand.

### Escrow ID derivation

```
escrowID = keccak256(payer || payee || metaHash || nonce)
```

`nonce` is the positive, caller-supplied `uint64` encoded as 8 bytes big-endian; `metaHash` is the 32-byte meta value (zero-filled if none). `Create` rejects `nonce == 0`. Clients can compute the ID before sending the transaction.

## Behavioural notes

* **Nonce handling.** Handlers, including the arbitration handler, increment the sender nonce after the engine mutates state.
* **Idempotency.** Repeating fund, release, refund, expire or dispute on an escrow that already reached the target state succeeds without change. `ResolveWithSignatures` returns success without change for any escrow that is already `released`, `refunded` or `expired`, whatever the decision submitted (`native/escrow/engine.go`).
* **Determinism.** The engine's clock is the block timestamp (`sp.blockTimestamp()`, set through `SetNowFunc` in `configureTradeEngine`), and the expiry handler passes the block timestamp explicitly.
* **Events.** Every engine call appends the events listed in [`escrow.md`](./escrow.md) section 6.

## Legacy migration

When a handler looks up an escrow ID and the modern record does not exist, `ensureEscrowReady` calls `migrateLegacyEscrow` (`core/state_transition.go`):

* The legacy record is read from the trie key `keccak256("escrow-" || id)` and decoded as an `escrow.LegacyEscrow` (`buyer`, `seller`, `amount`, `status`).
* It is converted by `convertLegacyEscrow`: payer = legacy seller, payee = legacy buyer (or the seller if no buyer), token `NHB`, no mediator, `feeBps` 0, nonce 1, `createdAt` = block time, `deadline` = block time + 30 days (`convertLegacyEscrow` uses `sp.blockTimestamp()`). Legacy status maps as: released to `released`, refunded to `refunded`, disputed to `disputed`, open or in-progress (and anything else) to `funded`.
* For `funded` and `disputed` results the amount is credited to the escrow's vault balance and to the vault account, so later releases and refunds operate on real balances.
* The legacy key is cleared so the migration runs once.

If neither record exists (or the legacy trie read fails or returns empty data) the handler fails with `escrow <id> not found`, where `<id>` is the 64-character lowercase hex of the ID with no `0x` prefix (`fmt.Errorf("escrow %x not found", id)` in `migrateLegacyEscrow`, `core/state_transition.go`).

## Examples

1. **Single escrow, direct transactions.** Payer sends `TxTypeCreateEscrow`, then `TxTypeLockEscrow` to fund it; the payee (or mediator) sends `TxTypeReleaseEscrow`, which pays the payee and routes the fee to the treasury.
2. **Dispute with a realm.** An escrow created with `realm` is funded, then the payer or payee sends `TxTypeDisputeEscrow`. A quorum of the frozen arbitrators signs a decision envelope and anyone submits it in a `TxTypeArbitrateRelease` or `TxTypeArbitrateRefund` transaction; `ResolveWithSignatures` releases to the payee or refunds the payer.
3. **Relayed action.** A participant signs the release, refund, dispute or create envelope off-chain and a relayer submits it in the matching `TxTypeDelegated*Escrow` transaction; the chain authorizes against the embedded signature.
