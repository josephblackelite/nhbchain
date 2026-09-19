# Wallet builder guide

A wallet that sends NHB or ZapNHB (ZNHB) builds a native transaction, signs it,
and submits it with `nhb_sendTransaction`. The signing hash for that RPC is
described in the first bullet below.
[`transactions/znhb-transfer`](../transactions/znhb-transfer.md) has request
examples for both transfer types.
[`transactions/signing`](../transactions/signing.md) is about a different
thing: signing a consensus `SignedTxEnvelope` for the consensus gRPC service, not
the `nhb_sendTransaction` transaction hash. The points below are what the code
enforces.

- **Signing hash.** For every transaction type above zero the signing hash is
  the SHA-256 of the `NHB_TX_V3_MAINNET` canonical binary encoding
  (`Transaction.Hash` in `core/types/transaction.go`); the signature is
  secp256k1 with `v` = recovery id + 27, and `s` must be in the lower half of the
  curve order (`Transaction.From` rejects higher values). The Go helper
  `Transaction.Sign` produces this. Do not reuse a signing routine that hashes
  anything else; the node would recover a different sender address.
- **Chain ID.** `0x4e4842` (`types.NHBChainID()`); the RPC rejects any other value.
- **Nonce.** Read `nonce` from the `nhb_getBalance` result (no token needed). The
  RPC rejects a transaction whose nonce is lower than the account nonce with
  `nonce N has already been used; current account nonce is M`.
- **Submission.** `nhb_sendTransaction` requires the bearer token. It returns
  `0x` plus the transaction hash once the mempool accepts the transaction. Use
  `nhb_getTransactionReceipt` with that hash to follow it.
- **Transaction types.** NHB transfer is `TxTypeTransfer` (`0x01`); ZNHB transfer
  is `TxTypeTransferZNHB` (`0x10`). Both need a 20-byte recipient in `to` that is
  not the all-zero address, and a positive `value`
  (`applyEvmTransaction`, `applyTransferZNHB` in `core/state_transition.go`).
  In the JSON body, `to` and `data` are Go `[]byte` fields, so they are
  base64 strings (`rpc/http.go:2883-2885`); `chainId`, `nonce`, `value`,
  `gasLimit`, `gasPrice`, `r`, `s` and `v` accept a JSON number, a decimal string
  or a `0x` hex string (`rpc/http.go:2908-2944`).
- **What a transfer debits.** The fee is a protocol-enforced percentage of the
  amount, not `gasLimit * gasPrice` (comments in `core/state_transition.go`).
  - ZNHB transfer (`applyTransferZNHB`): the sender's ZNHB balance is debited by
    the amount plus `amount * feeBpsZNHB / 10000` (`TransferGasPolicy.ComputeFee`),
    with the fee paid in ZNHB to the configured fee collector. The fee is zero
    when the transfer gas policy marks the sender as still inside its free-spend
    tier. If the ZNHB balance cannot cover both, the transfer fails with `znhb
    transfer: insufficient balance`. This handler does not read or change the
    sender's NHB balance.
  - NHB transfer: the sender's NHB balance is debited by the amount plus
    `amount * feeBps / 10000` in NHB, unless the sender is in the free-spend tier
    or a paymaster sponsors the transfer; otherwise it fails with `insufficient
    funds for transfer+gas`.

  - Domain fee (both transfer types): when `tx.MerchantAddress` (JSON field
    `merchantAddr`, part of the signed hash) names a domain that has a configured
    fee policy, `applyTransactionFee` (`core/state_transition.go:453-600`) applies
    that policy in addition to the fee above. It is called from the NHB path
    (`:3083`) and from `applyTransferZNHB` (`:3642`). The fee, if non-zero after
    the domain's free tier, is subtracted from the domain's configured payer, which is
    the sender by default and the recipient if the policy says so, in the
    transferred asset, and is routed to the domain's owner wallet. A `fees.applied`
    event is emitted. If the domain is empty or has no configured policy, nothing
    extra is charged. If the payer cannot cover it the transaction fails with
    `fees: insufficient balance to route fee`.
  - Either transfer type can be paused by governance, in which case it fails with
    `nhb transfer: paused` (NHB) or `znhb transfer: paused` (ZNHB)
    (`ErrTransferNHBPaused`, `ErrTransferZNHBPaused`, `core/state_transition.go:107-108`).

  `fees_getTransferStatus` reports a wallet's tracked spend and whether it is
  still eligible for the free tier; see the [fees policy](../fees/policy.md).
- **Activity feeds.** Both transfers emit an event of type `transfer.native`
  with attributes `asset` (`NHB` or `ZNHB`), `from`, `to`, `amount` and, when
  known, `txHash` (`core/events/transfer.go`). Read the `asset` attribute to tell
  ZNHB settlement from an NHB payment.

The Go SDK (`sdk/go/client`) builds, signs and submits both transfer types; see
the [Go SDK guide](./go.md). The TypeScript `WalletClient` currently signs a
different hash than the node verifies; see the note in the
[JavaScript & TypeScript SDK guide](./js.md).
