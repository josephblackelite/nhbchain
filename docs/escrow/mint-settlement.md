# Mint Voucher RPC (`mint_with_sig`)

## Overview

`mint_with_sig` submits a signed mint voucher for **NHB**. The node validates the voucher, wraps it in a `TxTypeMint` (`0x0E`) transaction and adds it to the mempool. When the transaction is executed in a block, the recipient is credited and a `mint.settled` event is emitted.

The voucher is created and signed off-chain by an address that holds the on-chain `MINTER_NHB` role. How an off-chain submitter decides that a voucher should exist is outside this node's source. This document covers only the wire format and the on-chain rules.

**ZNHB cannot be minted.** A voucher with token `ZNHB` is always rejected with `ErrMintZNHBNotMintable`, whatever roles exist (`core/state_transition.go`, `applyMintTransaction`; `core/node.go`, `MintWithSignature`).

Code: `core/mint.go`, `core/node.go` (`MintWithSignature`), `core/state_transition.go` (`applyMintTransaction`), `rpc/mint_handlers.go`, `core/events/mint.go`.

## Voucher schema

| Field | Type | Description |
|-------|------|-------------|
| `invoiceId` | string | Unique identifier. Trimmed; must not be empty. Reuse is rejected once a mint with this ID has executed. |
| `recipient` | string | An NHB bech32 address, or an identity alias. The address is tried first; if it does not decode, the value is resolved through the identity registry (`IdentityResolve`, primary address). |
| `token` | string | Trimmed and upper-cased. Only `NHB` is accepted. |
| `amount` | string | Base-unit integer, strictly positive. |
| `chainId` | number | Must equal the constant `core.MintChainID` (`430060579445266314`, `core/mint.go`), checked in `Node.MintWithSignature` and again in `applyMintTransaction`. It is a fixed number in the source, not read from the running chain: the shipped configuration's network id is `18346390202490284624` (`config.toml`, `NetworkId`), so a voucher that carries the running chain's id is refused with `ErrMintInvalidChainID`. |
| `expiry` | number | Unix seconds. Must be later than the current time (node time when submitting, block time when executing). |

The mint transaction itself uses chain ID `types.NHBChainID()` (`0x4e4842`), gas limit 0 and gas price 0, and has no sender signature; the voucher signature is the authorization (`RequiresSignature(TxTypeMint)` is false).

## Canonical signing payload

* Canonical JSON (`MintVoucher.CanonicalJSON`) is a JSON object with the keys in this order: `invoiceId`, `recipient`, `token`, `amount`, `chainId`, `expiry`. Strings are trimmed, `token` upper-cased, and `amount` re-rendered as a normalized decimal integer.
* The digest is `keccak256(canonicalJSON)`.
* The signature is a 65-byte secp256k1 signature over that digest (`ethcrypto.Sign`), passed as hex with an optional `0x`. The signer is recovered with `SigToPub`, which expects a recovery byte of 0 or 1.
* The recovered address must hold `MINTER_NHB`. `MINTER_ZNHB` exists in the code but is unreachable because ZNHB mints are refused before the role check.

Go helpers: `core.MintVoucher.CanonicalJSON`, `Digest`, `core.MintVoucherHash`.

## Execution rules

`applyMintTransaction` checks, in this order: decode payload; positive amount; `chainId`; expiry against block time; canonical JSON; 65-byte signature; token is not ZNHB and is NHB; non-empty `invoiceId` and `recipient`; the token's `MintPaused` metadata flag (`ErrMintPaused`); signer holds `MINTER_NHB` (`ErrMintInvalidSigner`); invoice not already used (`ErrMintInvoiceUsed`); recipient resolvable (`ErrMintRecipientUnresolved`); yearly emission cap.

* **Replay protection.** A successful mint stores a flag under the invoice key (`MintInvoiceKey`) in state. The mempool also rejects a second pending mint transaction with the same invoice ID.
* **Emission cap.** The governance parameter `mint.nhb.maxEmissionPerYearWei` sets the maximum NHB minted per calendar year (UTC, by block time). The value may be stored bare or as a quoted decimal string (`nativecommon.ParamDecimal`, `mintMaxEmissionPerYear` in `core/state_transition.go`). Unset, empty, or `0` means no cap. Exceeding it fails with `ErrMintEmissionCapExceeded`. The year-to-date total is tracked in state.
* **Supply.** The recipient's NHB balance is credited, the tracked total NHB supply is increased by the amount, and a supply-change event is recorded.

## Event

A successful mint appends a `mint.settled` event with these attributes (`core/events/mint.go`):

```json
{
  "type": "mint.settled",
  "attributes": {
    "invoiceId": "inv-123",
    "recipient": "nhb1...",
    "token": "NHB",
    "amount": "2500000000000000000",
    "txHash": "0x...",
    "voucherHash": "0x..."
  }
}
```

`txHash` is `types.Transaction.Hash()` of the mint transaction. `voucherHash` is `keccak256(canonicalJSON || signature)`, hex with `0x` (`MintVoucherHash`).

## RPC

Method `mint_with_sig`, two positional params: the voucher (a JSON object, or a string containing the voucher JSON) and the signature hex string. The RPC layer does not require a bearer token for this method; authorization is the voucher signature and the `MINTER_NHB` role.

```json
{
  "jsonrpc": "2.0",
  "id": 7,
  "method": "mint_with_sig",
  "params": [
    {
      "invoiceId": "inv-123",
      "recipient": "nhb1alice...",
      "token": "NHB",
      "amount": "2500000000000000000",
      "chainId": 430060579445266314,
      "expiry": 1733070300
    },
    "0x...signature..."
  ]
}
```

Result: `{"txHash": "0x...", "voucherHash": "0x..."}`. The call returns as soon as the transaction is queued; it does not wait for a block.

Errors (`rpc/mint_handlers.go`):

| Condition | HTTP | Code |
|-----------|------|------|
| Wrong parameter count, bad JSON, signature missing or not valid hex | 400 | `-32602` |
| Signer lacks `MINTER_NHB` (`ErrMintInvalidSigner`) | 401 | `-32001` |
| Invoice already settled or pending (`ErrMintInvoiceUsed`) | 409 | `-32010` |
| Expired (`ErrMintExpired`), wrong chain ID (`ErrMintInvalidChainID`), errors wrapping `ErrMintInvalidPayload` (ZNHB is one of these), emission cap exceeded (`ErrMintEmissionCapExceeded`) | 400 | `-32602` |
| Mempool full | 503 | `-32030` |
| Anything else | 500 | `-32000` (message `mint failed`) |

`Node.MintWithSignature` (`core/node.go`) returns several validation failures as plain errors that do not wrap any of the sentinel errors the handler matches, so they fall into the last row and return HTTP 500 rather than 400. These are: an amount that is empty, not a base-10 integer or not positive; an empty `invoiceId`, `recipient` or `token` (`MintVoucher.CanonicalJSON`, `core/mint.go`); a token other than `NHB` or `ZNHB` (`unsupported token`); a signature whose decoded length is not 65 bytes (`invalid signature length`); and a signature from which no public key can be recovered (`recover signer: ...`). Only a signature that is empty or not valid hex is rejected with 400 by the RPC handler itself.
