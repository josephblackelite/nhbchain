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

The body is the JSON-RPC request. The gateway defaults `jsonrpc` to `2.0` and
`method` to `nhb_sendTransaction` when omitted, and rejects any other method.
`params[0]` is decoded into `types.Transaction` with standard Go JSON rules, so:

- `chainId`, `value`, `gasPrice`, `r`, `s`, `v`, `nonce`, `gasLimit` and `type`
  must be unquoted JSON numbers. Quoted values (including `"0x..."` strings) fail
  with `decode transaction: ...`.
- `to` and `data` are `[]byte` fields, which encode as base64 strings.
- `chainId` must be the NHB chain ID, `0x4e4842` (`5130306`).

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
      "gasPrice": 1000000000,
      "r": <signature r as a JSON integer>,
      "s": <signature s as a JSON integer>,
      "v": <signature v as a JSON integer>
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
  https://<gateway-host>/v1/transactions/send
```

`znhb-transfer.json` holds a signed payload shaped like the one above. The
backend's response is returned with its original status code and body. On
success the JSON-RPC `result` is the transaction hash as a `0x`-prefixed string
(`rpc/http.go`, `handleSendTransaction`).

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
