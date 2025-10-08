# Wallet builder guide

Wallets integrating with NHB and ZapNHB should reuse the transaction envelopes
and signing primitives that power the core SDKs. Review the
[`transactions/signing`](../transactions/signing.md) walkthrough for the exact
ECDSA flow, then follow the copy-paste JSON-RPC examples in
[`transactions/znhb-transfer.md`](../transactions/znhb-transfer.md) to fetch the
nonce, populate a `TxTypeTransferZNHB`, and broadcast the signed payload.

Key implementation notes:

- Always read the nonce and balances via `nhb_getBalance` before crafting the
  transfer to ensure both NHB gas and ZNHB settlement funds are available.
- Respect the fee policy described in the [fees documentation](../fees/README.md),
  which currently sponsors NHB gas for eligible merchants but still requires the
  sender to hold the ZNHB principal.
- Surface the `Transfer{asset: 'ZNHB'}` receipt log in activity feeds so users
  can distinguish ZNHB settlement from standard NHB payments.

The Go and TypeScript SDKs expose helpers that take care of envelope encoding.
Most wallets can lean on those packages and only supply the signing callback that
holds the private key or hardware wallet bridge.
