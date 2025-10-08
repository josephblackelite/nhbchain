# NHB vs. ZNHB transfers

ZapNHB (ZNHB) transfers use a dedicated transaction type alongside the existing
NHB coin payments. Both payloads share the same ECDSA signing flow: construct a
`types.Transaction`, recover the sender nonce from the latest account state, and
sign the SHA-256 hash before submitting the JSON-RPC request.

## 1. Fetch the current nonce

Before building either transaction, query the account with `nhb_getBalance` to
retrieve the `nonce` and confirm available balances.

```json
{
  "id": 1,
  "method": "nhb_getBalance",
  "params": ["nhb1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh"]
}
```

The response includes both NHB and ZNHB balances together with the latest
`nonce` that must be echoed in the outgoing transaction.

## 2. NHB transfer (type `0x01`)

Standard NHB payments continue to use `TxTypeTransfer (0x01)` and execute on the
EVM path. Loyalty rewards and merchant fee routing remain scoped to these
transfers.

```json
{
  "id": 1,
  "method": "nhb_sendTransaction",
  "params": [
    {
      "chainId": "0x4e4842",
      "type": 1,
      "nonce": 7,
      "to": "0x1b9b9fb69f2c6c9c1d4c1c4e7b999b20461ab29f",
      "value": "0x2386f26fc10000",
      "gasLimit": 25000,
      "gasPrice": "0x3b9aca00",
      "r": "0x…",
      "s": "0x…",
      "v": "0x1b"
    }
  ]
}
```

## 3. ZNHB transfer (type `0x10`)

ZapNHB uses the new `TxTypeTransferZNHB (0x10)` constant. Only the asset changes
— the fee path still burns NHB gas and no loyalty accrual is triggered. The
`value` field is denominated in ZNHB wei and the recipient must be supplied.

```json
{
  "id": 1,
  "method": "nhb_sendTransaction",
  "params": [
    {
      "chainId": "0x4e4842",
      "type": 16,
      "nonce": 12,
      "to": "0x5c9d4cde23f68cd2209a2f5eaf0a1d34ac3e5f2a",
      "value": "0xde0b6b3a7640000",
      "gasLimit": 25000,
      "gasPrice": "0x3b9aca00",
      "r": "0x…",
      "s": "0x…",
      "v": "0x1c"
    }
  ]
}
```

After signing, submit the payload through `nhb_sendTransaction`. Successful
settlement debits the sender's `balanceZNHB`, credits the recipient (creating the
account metadata if necessary), and increments the sender nonce. Gas charges are
still paid in NHB, so wallets should confirm sufficient NHB balance alongside
ZNHB holdings.

For an end-to-end example that automates signing for either token, refer to the
`send-znhb` command in `cmd/nhb-cli`, which reuses the same signing primitives
exposed in the SDK.
