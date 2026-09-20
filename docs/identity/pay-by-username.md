# Pay by Username

Paying a username is a wallet-side flow: the wallet resolves the alias to an address with `identity_resolve` and then sends an ordinary transfer to the returned address. There is no on-chain "transfer to alias" transaction type; the transaction `to` field is always an address.

## Flow

1. **Resolve.** `identity_resolve("frankrocks")` returns `alias`, `aliasId`, `primary`, `addresses`, `avatarRef` (if set), `createdAt`, `updatedAt` (see [`identity-api.md`](./identity-api.md)). An unknown alias returns HTTP 404 with code `-32602`.
2. **Send.** Send an NHB transfer (`TxTypeTransfer`) or ZNHB transfer (`TxTypeTransferZNHB`) to `primary`.
3. **Confirm before sending.** The resolve result contains what a wallet needs to show a confirmation: the alias, the primary address, `createdAt`, `updatedAt` and `avatarRef`.

A wallet that caches a resolve result cannot learn about later changes from events today: alias-change events are only emitted by code paths behind disabled RPC methods (see [`identity.md`](./identity.md)), so re-resolve before each payment.

## Claiming a name

A user gets a name with `TxTypeRegisterIdentity` (`nhb-cli claim-username <username> <key_file>`). The name is 3 to 32 characters of `a-z 0-9 . _ -`, lower-cased, and an account can hold one username.

## Loyalty and username lookup

The loyalty RPC methods `loyalty_resolveUsername` and `loyalty_userQR` resolve a username through the node's username index (`Node.ResolveUsername`) and return a bech32 address, or, for `loyalty_userQR`, `{"address": ..., "payload": "nhb:<address>"}` (see [`../loyalty/loyalty.md`](../loyalty/loyalty.md)).

## Pay to an alias that has no address yet

That case uses claimables, which cannot be created on a running node at present; see [`pay-by-email.md`](./pay-by-email.md).
