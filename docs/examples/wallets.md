# Submitting a signed transfer

Two ways to submit a signed NHB or ZNHB transfer: straight to a node's `nhb_sendTransaction`, or through the gateway's `/v1/transactions/send` route, which validates the transaction type and forwards to the node. Both need a bearer token, so submit from a server you control and never ship the token to a browser or mobile client.

For signing itself see [`docs/transactions/signing.md`](../transactions/signing.md) and [`docs/transactions/znhb-transfer.md`](../transactions/znhb-transfer.md). The values below come from `core/types/transaction.go` and `rpc/http.go`.

## Transaction fields

| Item | Value |
| --- | --- |
| NHB transfer | `type` `1` (`TxTypeTransfer`, `0x01`) |
| ZNHB transfer | `type` `16` (`TxTypeTransferZNHB`, `0x10`) |
| Chain ID | `0x4e4842` = `5130306`. Any other value is rejected with `transaction chainId does not match NHBCoin network`. |
| `to` | Base64 of the 20-byte address (Go `[]byte` JSON encoding). A `0x`-hex string is rejected: `invalid transaction format`. |
| `data` | Base64 bytes; omit it for a plain transfer. |
| `gasLimit`, `gasPrice` | Both must be greater than zero. `nhb-cli` defaults: gas limit `21000` (NHB) and `25000` (ZNHB), gas price `1`. |
| `nonce` | Must be at least the account's current `nonce` (`nhb_getBalance`); otherwise `nonce N has already been used; current account nonce is M`. |
| `r`, `s`, `v` | The sender's secp256k1 signature. |

The easiest way to produce a correct, signed transaction is `nhb-cli`, which builds and signs it for you and submits it with `nhb_sendTransaction`:

```bash
nhb-cli --rpc "$NHB_RPC_URL" send-znhb <recipient> <amount> <key_file>
nhb-cli --rpc "$NHB_RPC_URL" send-nhb  <recipient> <amount> <key_file>
```

Optional flags before the positional arguments: `--gas <limit>` and `--gas-price <price>` (`cmd/nhb-cli/send.go`). `<amount>` is a positive base-10 integer. `NHB_RPC_TOKEN` must be set in the environment because `nhb_sendTransaction` is privileged.

## Directly to the node

`nhb_sendTransaction` requires `Authorization: Bearer <JWT>` (`requireAuthInto` in `rpc/http.go`). On this path the numeric fields (`chainId`, `nonce`, `value`, `gasLimit`, `gasPrice`, `r`, `s`, `v`) may be JSON numbers, decimal strings or `0x`-hex strings (`parseNumericString`).

Replace the `<...>` placeholders; the signature values must come from signing this exact transaction.

```bash
curl -s -X POST "$NHB_RPC_URL" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $NHB_RPC_TOKEN" \
  -d '{
        "jsonrpc": "2.0",
        "id": 99,
        "method": "nhb_sendTransaction",
        "params": [{
          "chainId": "0x4e4842",
          "type": 16,
          "nonce": 7,
          "to": "<base64 of the 20-byte recipient address>",
          "value": "0x2386f26fc10000",
          "gasLimit": "0x61a8",
          "gasPrice": "0x3b9aca00",
          "r": "0x<r>",
          "s": "0x<s>",
          "v": "0x<v>"
        }]
      }'
```

Success returns the transaction hash as a `0x`-prefixed string in `result`. Errors: `401` for a missing or invalid token, `400` for invalid parameters, `429` `transaction rate limit exceeded`, `503` `mempool full`.

## Through the gateway

`POST <gateway>/v1/transactions/send` (`gateway/routes/transactions.go`). The route is registered with `RequireAuth: true` in `cmd/gateway/main.go`, and the handler forwards `Authorization` and `X-Chain-Id` to the node and appends the caller's address to `X-Forwarded-For`.

What it enforces before forwarding:

- Body non-empty. Only the first 1 MiB is read (`io.LimitReader` with `transactionsRequestLimit`); there is no size-based rejection, so a larger body is truncated and normally fails to parse as JSON with `400 decode request: ...`.
- `method` defaults to `nhb_sendTransaction` and `jsonrpc` to `2.0` when omitted; any other method is `400 unsupported method`.
- `params[0]` must decode as a `types.Transaction` with standard Go JSON rules, and its `type` must be `0x01` or `0x10`, otherwise `400 unsupported transaction type 0x..`.

Because the gateway decodes with standard rules, the hex-string form accepted by the node is not accepted here: `chainId`, `nonce`, `value`, `gasLimit`, `gasPrice`, `r`, `s`, `v` must be JSON numbers, and `to` / `data` must be base64. Chain ID `5130306` is the number form of `0x4e4842`.

```bash
curl -s -X POST "$GATEWAY_URL/v1/transactions/send" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $NHB_RPC_TOKEN" \
  -d '{
        "params": [{
          "chainId": 5130306,
          "type": 16,
          "nonce": 7,
          "to": "<base64 of the 20-byte recipient address>",
          "value": 10000000000000000,
          "gasLimit": 25000,
          "gasPrice": 1000000000,
          "r": <r as a decimal number>,
          "s": <s as a decimal number>,
          "v": <v as a decimal number>
        }]
      }'
```

The gateway checks the bearer token against its own auth configuration (`gateway/middleware/auth.go`: HMAC secret, issuer, audience and, when required, scopes), independent of the node's `RPCJWT` settings. Reusing `$NHB_RPC_TOKEN` in the example above works only if the gateway's auth settings accept that token; the code does not make the two configurations the same. If gateway auth is disabled (`Enabled` false) the middleware passes requests through.

The response body and status are the node's, copied through unchanged.

## Postman

Create a `POST` request to the URL above, add the `Content-Type` and `Authorization` headers, set the body to raw JSON and paste one of the payloads. There is no Postman collection for this route in `examples/postman`.
