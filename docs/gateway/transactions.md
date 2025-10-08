# Transaction submission via the gateway

The gateway exposes a scoped `/v1/transactions/send` endpoint that proxies
`nhb_sendTransaction` to the consensus node. Requests are authenticated with the
same bearer token as the legacy JSON-RPC surface and retain the node-level rate
limits and duplicate detection so wallets do not lose existing safeguards.

- **Method:** `POST`
- **Path:** `/v1/transactions/send`
- **Auth:** `Authorization: Bearer <NHB_RPC_TOKEN>`
- **Rate limit:** Shares the standard transaction limiter applied by the node.

The gateway validates the transaction payload before forwarding it. Both the
classic NHB transfer (`TxTypeTransfer`) and the new ZNHB transfer
(`TxTypeTransferZNHB`) are accepted; other transaction types are rejected with a
`400` response.

## Request format

Send the same JSON-RPC payload that validator nodes accept. The gateway will
normalise the payload (defaulting `jsonrpc` to `2.0` and `method` to
`nhb_sendTransaction` when omitted) before relaying the call.

```jsonc
{
  "jsonrpc": "2.0",
  "id": 1,
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

## Example: ZNHB transfer with `curl`

```bash
curl -s \
  -X POST \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $NHB_RPC_TOKEN" \
  -d @znhb-transfer.json \
  https://app.nhbcoin.com/v1/transactions/send
```

Where `znhb-transfer.json` contains the JSON-RPC payload shown above. The
response mirrors the node output, for example:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": "Transaction received by node."
}
```

## Example: ZNHB transfer via Postman

1. Create a new `POST` request to `https://app.nhbcoin.com/v1/transactions/send`.
2. Under **Headers** add `Authorization: Bearer {{NHB_RPC_TOKEN}}` and
   `Content-Type: application/json`.
3. Paste the JSON-RPC payload into the **Body** tab (`raw`, `JSON`).
4. Send the request. A successful submission returns the same JSON payload as
   the underlying node, while authentication failures return `401`.

## Error responses

| Condition                                | HTTP | Body                                              |
| ---------------------------------------- | ---- | ------------------------------------------------- |
| Missing or malformed payload             | 400  | `{ "error": "request body is empty" }`          |
| Unsupported transaction type             | 400  | `{ "error": "unsupported transaction type 0x.." }` |
| Upstream node rejected or throttled call | 4xx  | JSON-RPC error forwarded from the node            |
