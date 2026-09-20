# NHB and ZNHB transfers

NHB (`TxTypeTransfer`, `0x01`) and ZapNHB (`TxTypeTransferZNHB`, `0x10`) are both
sent with `nhb_sendTransaction`. Construct a `types.Transaction`, read the
sender's nonce from `nhb_getBalance`, sign it, and submit the JSON-RPC request.
The signing hash is the SHA-256 of the `NHB_TX_V3_MAINNET` binary encoding
(`Transaction.Hash`, `core/types/transaction.go`); the [wallet builder
guide](../sdk/wallets.md) lists every rule the code enforces. This page shows the
request shapes.

For a copy/paste JSON-RPC payload that showcases the ZNHB transfer format, see
the [Sending ZNHB via `nhb_sendTransaction`](../api/rpc.md#sending-znhb-via-nhb_sendtransaction)
example in the RPC reference.

## Authenticated submission

`nhb_sendTransaction` is a privileged RPC. The dispatcher calls
`requireAuthInto` before `handleSendTransaction` (`rpc/http.go`), which accepts a
verified TLS client certificate when the server requires one, and otherwise an
`Authorization: Bearer <token>` header carrying a JWT that the server's verifier
accepts. A request that fails this check is answered with HTTP 401 before the
payload is parsed. Wallets must not ship the bearer token to browsers or mobile
clients; proxy the signed transaction through a trusted server endpoint that
attaches the header. Both transfer types use the same authenticated flow. When
the handler accepts the transaction it returns `0x` plus the transaction hash.

The CLI does this for you: `nhb-cli send-nhb` and `nhb-cli send-znhb` take
`[--rpc <url>] [--gas <limit>] [--gas-price <price>] <recipient> <amount> <key_file>`,
read the token from `NHB_RPC_TOKEN` and the endpoint from `--rpc` or `RPC_URL`
(default `http://localhost:8080`). `--gas` defaults to `21000` for NHB and
`25000` for ZNHB; `--gas-price` defaults to `1`. `amount` is a positive integer in
wei (`cmd/nhb-cli/send.go`).

Query the account with `nhb_getBalance` (no token needed) to read the `nonce`
and the balances. The result includes `balanceNHB`, `balanceZNHB` and `nonce`,
among other fields (`BalanceResponse` in `rpc/http.go`).

`nhb_getBalance` returns `nonce`, `balanceNHB` and `balanceZNHB`
([rpc.md](../api/rpc.md#nhb_getbalance)). Use `nonce` as the transaction `nonce`;
a lower value is rejected.

// Response (abridged)
{
  "id": 1,
  "jsonrpc": "2.0",
  "result": {
    "address": "nhb1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh",
    "balanceNHB": 10000000000000000,
    "balanceZNHB": 39062500000000000,
    "nonce": 42
  }
}
```

The nonce lookup is the same for both assets. The node rejects a transaction
whose nonce is lower than the account nonce with `nonce N has already been used;
current account nonce is M`.

Which balance a wallet must check before sending depends on the asset. An NHB
transfer debits NHB and a ZNHB transfer debits ZNHB only (see "Fee model"
below), so a wallet should check the balance of the asset being sent against the
amount plus the fee. A ZNHB send does not need any NHB.

## Request body encoding

The request body is the transaction object. `to` and `data` are byte fields and
must be base64 strings (`rpc/http.go`, `txDTO`); a `0x` hex string is not
accepted there. `chainId`, `nonce`, `value`, `gasLimit`, `gasPrice`, `r`, `s` and
`v` accept a JSON number, a decimal string or a `0x` hex string. The `r`, `s`
and `v` values below are placeholders, not a valid signature.

`TxTypeTransfer` takes a native fast path inside `applyEvmTransaction`: balances
are updated by the state processor rather than by running EVM code. The receipt's
gas used is the transaction's `gasLimit`.

`applyEvmTransaction` handles `TxTypeTransfer`. The recipient must be a 20-byte,
non-zero address and `value` must be positive.

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
      "to": "G5uftp8sbJwdTBxOe5mbIEYasp8=",
      "value": "0x2386f26fc10000",
      "gasLimit": "0x61a8",
      "gasPrice": "0x1",
      "data": "",
      "r": "<signature r>",
      "s": "<signature s>",
      "v": "<signature v, 27 or 28>"
    }
  ]
}
```

## Fees

`applyTransferZNHB` handles `TxTypeTransferZNHB`. The recipient must be a
20-byte, non-zero address and `value` (in ZNHB base units) must be positive.

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
      "to": "XJ1M3iP2jNIgmi9erwodNKw+Xyo=",
      "value": "0xde0b6b3a7640000",
      "gasLimit": "0x61a8",
      "gasPrice": "0x1",
      "data": "",
      "r": "<signature r>",
      "s": "<signature s>",
      "v": "<signature v, 27 or 28>"
    }
  ]
}
```

`gasLimit` must be greater than zero and `gasPrice` must be greater than zero, or
the RPC rejects the request (`rpc/http.go`); the amounts above are examples.

### Fee model

The transfer fee is not `gasLimit * gasPrice`. It is a protocol-enforced
percentage of the amount, computed by `TransferGasPolicy.ComputeFee` and charged
in the asset being sent (`core/state_transition.go`):

- **ZNHB transfer** (`applyTransferZNHB`): the sender's ZNHB balance is debited by
  the amount plus the fee, the recipient is credited the amount, and the fee goes
  to the configured fee collector. The handler does not read or change the
  sender's NHB balance. If the ZNHB balance cannot cover both, the transfer fails
  with `znhb transfer: insufficient balance`.
- **NHB transfer**: the sender's NHB balance is debited by the amount plus the
  fee unless the sender is in the free-spend tier or a paymaster sponsors the
  transfer; otherwise it fails with `insufficient funds for transfer+gas`.
- Both types can additionally be charged a domain fee when `merchantAddr` names a
  domain with a configured fee policy (`applyTransactionFee`); see the [wallet
  builder guide](../sdk/wallets.md) and the [fees reference](../fees/policy.md).

Both transfer types can be paused by governance; the transaction then fails with
`nhb transfer: paused` or `znhb transfer: paused`.

### Expected responses

Once the transaction is accepted the node returns the transaction hash as a
string:

```json
{
  "id": 2,
  "jsonrpc": "2.0",
  "result": "0xa9a6f4d59e11cce45bfb0fb89f743ad39df0cedf0e09a0e02ff80db152df2b03"
}
```

Poll `nhb_getTransactionReceipt` with that hash. The result is `null` until the
transaction is found in a block. Otherwise the receipt has `transactionHash`,
`blockHash`, `blockNumber`, `status` (always `"0x1"` for a transaction that is in
a block), `gasUsed` and `logs` (`buildReceiptResult`, `rpc/http.go`). Each log is
a string map built from the transaction's events; a transfer produces a
`Transfer` log with `asset` (`NHB` or `ZNHB`), `value` and the event's other
attributes, and a domain fee adds a `FeeApplied` log.

```json
{
  "id": 3,
  "jsonrpc": "2.0",
  "method": "nhb_getTransactionReceipt",
  "params": ["0xa9a6f4d59e11cce45bfb0fb89f743ad39df0cedf0e09a0e02ff80db152df2b03"]
}
```

Successful settlement debits the sender, credits the recipient and increments the
sender nonce. `applyTransferZNHB` does this entirely in the native state
processor (subtracting from `BalanceZNHB`, adding to the recipient, creating the
account if needed) and records a `transfer.native` event
(`core/events/transfer.go`). For an end-to-end example that signs for either
asset, see the `send-nhb` and `send-znhb` commands in `cmd/nhb-cli`.
