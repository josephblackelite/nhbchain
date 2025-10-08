# NHB vs. ZNHB transfers

ZapNHB (ZNHB) transfers use a dedicated transaction type alongside the existing
NHB coin payments. Both payloads share the same ECDSA signing flow: construct a
`types.Transaction`, recover the sender nonce from the latest account state, and
sign the SHA-256 hash before submitting the JSON-RPC request.

## Authenticated submission

`nhb_sendTransaction` is a privileged RPC on validator nodes. Every request is
wrapped by `requireAuth`, which rejects calls that omit the `Authorization`
header or fail bearer-token verification before `handleSendTransaction` even
parses the payload.【F:rpc/http.go†L660-L704】【F:rpc/http.go†L1180-L1198】 Wallets
MUST proxy signed transactions through trusted server infrastructure so the
token never ships to the browser. Reuse helpers such as `rpcRequest(...,
withAuth=true)` on your server routes, then forward the fully signed JSON body
with the chain ID header and `Authorization: Bearer <NHB_RPC_TOKEN>` already
attached. Both NHB (`TxTypeTransfer`) and ZNHB (`TxTypeTransferZNHB`) sends rely
on the same authenticated flow.

## 1. Fetch the current nonce and balances

Before building either transaction, query the account with `nhb_getBalance` to
retrieve the `nonce` and confirm available balances. The node returns both NHB
and ZNHB balances in wei together with the next nonce that must be echoed in the
outgoing transaction.

```jsonc
// Request
{
  "id": 1,
  "jsonrpc": "2.0",
  "method": "nhb_getBalance",
  "params": ["nhb1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh"]
}

// Response
{
  "id": 1,
  "jsonrpc": "2.0",
  "result": {
    "address": "nhb1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh",
    "balanceNHB": "0x0000000000000000000000000000000000000000000000002386f26fc10000",
    "balanceZNHB": "0x0000000000000000000000000000000000000000000000008ac7230489e800",
    "nonce": 42
  }
}
```

Wallets can reuse the same nonce lookup irrespective of the asset being moved
and should block send attempts if either NHB gas or ZNHB balance is insufficient.

## 2. NHB transfer (type `0x01`)

Standard NHB payments continue to use `TxTypeTransfer (0x01)` and execute on the
EVM path. Loyalty rewards and merchant fee routing remain scoped to these
transfers.

```json
{
  "id": 1,
  "jsonrpc": "2.0",
  "method": "nhb_sendTransaction",
  "params": [
    {
      "chainId": "0x4e4842",
      "type": 1,
      "nonce": 42,
      "to": "0x1b9b9fb69f2c6c9c1d4c1c4e7b999b20461ab29f",
      "value": "0x2386f26fc10000",
      "gasLimit": "0x61a8",
      "gasPrice": "0x3b9aca00",
      "data": "0x",
      "r": "0xc1efc6c2f0c3f3d71e2c195911edbf7a7e8bc2bd52d4b3f6b14d4b0e54738b62",
      "s": "0x27a1a8e31f42d8c3e65d021779f8921bb5ca5066a8b0f67fc6f2df548b6e2771",
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
  "id": 2,
  "jsonrpc": "2.0",
  "method": "nhb_sendTransaction",
  "params": [
    {
      "chainId": "0x4e4842",
      "type": 16,
      "nonce": 42,
      "to": "0x5c9d4cde23f68cd2209a2f5eaf0a1d34ac3e5f2a",
      "value": "0xde0b6b3a7640000",
      "gasLimit": "0x61a8",
      "gasPrice": "0x3b9aca00",
      "data": "0x",
      "r": "0x9d6bb1226fb5c07f42d41f017cbf6f6fb1dcf1c563cb5b5b6f2a7d2639a4bce1",
      "s": "0x42fdedb6f5b1f59fa3d793c9d86b8b156382fa4995df794ba53d0d2ca4f8cb22",
      "v": "0x1c"
    }
  ]
}
```

### Authenticated submission

`nhb_sendTransaction` is a privileged RPC. Every request must present
`Authorization: Bearer <NHB_RPC_TOKEN>` so only trusted infrastructure can push
transactions into the network. The HTTP layer enforces this via the
[`requireAuth`](../../rpc/http.go#L1180-L1211) guard that runs before
[`handleSendTransaction`](../../rpc/http.go#L1417-L1478), rejecting any call
that lacks the bearer token. When the header is accepted the handler forwards
the payload to `node.AddTransaction`, queuing it for consensus alongside other
pending transfers. Wallets **must not** ship the bearer token to browsers or
mobile clients—proxy the submission through a server endpoint (for example the
`rpcRequest(..., withAuth=true)` helper used elsewhere in these docs) so the
token is only attached on trusted backends.

### Fee model

The `gasLimit`/`gasPrice` fields describe the NHB gas that is burned for
execution. ZNHB transfers **do not** carry an additional MDR-style fee; instead
they follow the per-asset merchant discount rate described in the [fees
reference](../fees/README.md) where ZNHB promotions are currently fully
sponsored. Any NHB required for gas is withdrawn from the sender (or from the
configured sponsor account if [gas sponsorship](../fees/gas-sponsorship.md) is
enabled for the merchant), while the ZNHB face value routes to the recipient.

### Expected responses

Once the envelope is submitted the node returns the transaction hash:

```json
{
  "id": 2,
  "jsonrpc": "2.0",
  "result": "0xa9a6f4d59e11cce45bfb0fb89f743ad39df0cedf0e09a0e02ff80db152df2b03"
}
```

Poll `nhb_getTransactionReceipt` to confirm settlement and to surface the asset
recorded in the `Transfer` log.

```json
{
  "id": 3,
  "jsonrpc": "2.0",
  "method": "nhb_getTransactionReceipt",
  "params": ["0xa9a6f4d59e11cce45bfb0fb89f743ad39df0cedf0e09a0e02ff80db152df2b03"]
}

// Response
{
  "id": 3,
  "jsonrpc": "2.0",
  "result": {
    "transactionHash": "0xa9a6f4d59e11cce45bfb0fb89f743ad39df0cedf0e09a0e02ff80db152df2b03",
    "status": "0x1",
    "gasUsed": "0x5208",
    "logs": [
      {
        "event": "Transfer",
        "asset": "ZNHB",
        "from": "nhb1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh",
        "to": "nhb1f3v0uf2p5uvq6m7jq3q3fyhml8prwv35u2hgq9t",
        "value": "0xde0b6b3a7640000"
      }
    ]
  }
}
```

After signing, submit the payload through `nhb_sendTransaction`. Successful
settlement debits the sender, credits the recipient, and increments the sender
nonce:

* **NHB transfers** – `applyEvmTransaction` executes the envelope on the EVM,
  then reloads sender and recipient accounts to apply gas, loyalty, and fee
  bookkeeping before persisting the debited `from`/credited `to` balances back
  into the trie.【F:core/state_transition.go†L1187-L1439】
* **ZNHB transfers** – `applyTransferZNHB` performs the debit/credit entirely in
  the native state processor, subtracting from `BalanceZNHB`, adding to the
  recipient (creating the account if needed), handling fees, and recording the
  transfer event.【F:core/state_transition.go†L1463-L1532】

Gas charges are still paid in NHB, so wallets should confirm sufficient NHB
balance alongside ZNHB holdings before attempting either transfer type.

For an end-to-end example that automates signing for either token, refer to the
`send-znhb` command in `cmd/nhb-cli`, which reuses the same signing primitives
exposed in the SDK.
