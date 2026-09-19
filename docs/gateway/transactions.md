# Transaction submission via the gateway

The gateway exposes `POST /v1/transactions/send`, which validates a JSON-RPC
`nhb_sendTransaction` request and forwards it to the backend configured as
`consensusd` (`gateway/routes/transactions.go`). The backend must speak the
node's JSON-RPC over HTTP (the `RPCAddress` listener of `cmd/nhb`); the gateway
POSTs to the configured endpoint URL itself, not to a sub-path.

- **Method and path:** `POST /v1/transactions/send`
- **Auth:** `Authorization: Bearer <token>`. The route requires a valid gateway
  JWT (see [Gateway Overview](./overview.md#authentication)) unless
  `auth.enabled` is `false`. The header is then forwarded unchanged to the
  backend, where `nhb_sendTransaction` is also authenticated (JWT, or a verified
  client certificate when the node runs mTLS).
- **Rate limits:** the gateway's `consensus` limiter applies first. The node
  then applies its own per-source transaction quotas
  ([Validator Operations Runbook](../ops/validator-runbook.md)).
- **Body limit:** 1 MiB.

Only `TxTypeTransfer` (type `1`, NHB transfer) and `TxTypeTransferZNHB` (type
`16`, ZNHB transfer) are accepted; any other type is rejected by the gateway with
`400`. Values come from `core/types/transaction.go`.

## Request format

Send a JSON-RPC payload for `nhb_sendTransaction`. The gateway defaults `jsonrpc`
to `2.0` and `method` to `nhb_sendTransaction` when omitted, and rejects any
other method. Before relaying, it decodes `params[0]` with the standard Go JSON
decoding of `types.Transaction` (`gateway/routes/transactions.go`). That means
the numeric fields (`chainId`, `nonce`, `value`, `gasLimit`, `gasPrice`, `r`,
`s`, `v`) must be JSON numbers, and `to` and `data` must be base64 strings. The
`0x`-prefixed hex strings that the node itself also accepts are rejected by the
gateway with a `decode transaction` error.

```jsonc
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "nhb_sendTransaction",
  "params": [
    {
      "chainId": 5130306,
      "type": 16,
      "nonce": 42,
      "to": "XJ1M3iP2jNIgmi9erwodNKw+Xyo=",
      "value": 1000000000000000000,
      "gasLimit": 25000,
      "gasPrice": 1,
      "data": "",
      "r": <signature r as a decimal number>,
      "s": <signature s as a decimal number>,
      "v": <signature v as a decimal number, 27 or 28>
    }
  ]
}
```

The gateway forwards `Authorization`, `X-Chain-Id` and an appended
`X-Forwarded-For` header to the backend. The gateway always sets that header
when it can read the caller's address, and a node with the default
`RPCProxyHeaders.XForwardedFor = "ignore"` rejects any request carrying it with
HTTP `403` (`rpc/http.go`, `resolveClientIP`). For this route to work against
a node, set `XForwardedFor = "single"` and put the gateway's address in
`RPCTrustedProxies` in the node's `config.toml` (see
[Gateway and RPC Security Settings](../ops/security.md)). If the caller itself
sent an `X-Forwarded-For` header, the forwarded value holds two addresses and
`single` mode rejects it.

## Example

```bash
curl -s \
  -X POST \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d @znhb-transfer.json \
  https://gateway.example/v1/transactions/send
```

Where `znhb-transfer.json` contains the JSON-RPC payload shown above (with the
signature filled in). The gateway copies the node's status, headers and body, so
a successful response is the node's JSON-RPC result, which is `0x` plus the
transaction hash:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": "0x<transaction hash>"
}
```

## Example: ZNHB transfer via Postman

1. Create a new `POST` request to `https://gateway.example/v1/transactions/send`.
2. Under **Headers** add `Authorization: Bearer {{NHB_RPC_TOKEN}}` and
   `Content-Type: application/json`.
3. Paste the JSON-RPC payload into the **Body** tab (`raw`, `JSON`).
4. Send the request. A successful submission returns the same JSON payload as
   the underlying node, while authentication failures return `401`.

## Error responses

Errors produced by the gateway itself are JSON of the form `{"error": "<message>"}`.

| Condition | HTTP | Message |
| --------- | ---- | ------- |
| Empty body | 400 | `request body is empty` |
| Body is not valid JSON | 400 | `decode request: ...` |
| `method` is not `nhb_sendTransaction` | 400 | `unsupported method "<name>"` |
| `params` is empty | 400 | `transaction parameter required` |
| `params[0]` does not decode | 400 | `decode transaction: ...` |
| Transaction type is not 1 or 16 | 400 | `unsupported transaction type 0x<type>` |
| Backend unreachable or timed out (10 s) | 500 | `forward request: ...` |
| Missing or invalid gateway token | 401 | `missing bearer token` / `invalid token` |
| Token lacks a required scope | 403 | `insufficient scope` |
| Gateway rate limit | 429 | `Too Many Requests` |

Rejections from the node (validation, nonce, `-32020` rate limit) are forwarded
as the node's JSON-RPC error with the node's HTTP status.
