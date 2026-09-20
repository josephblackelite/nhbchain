# RPC cookbook

Working JSON-RPC calls against a node, plus the state of the helper scripts under `examples/cookbook`. Every response shape below is taken from the handler in `rpc/http.go`.

## 1. Set up

The node serves JSON-RPC over HTTP `POST /` (`Serve` in `rpc/http.go`). The address it listens on is `RPCAddress` in `config.toml` (`127.0.0.1:8545` in the checked-in file). Read-only methods such as the ones below need no `Authorization` header.

```bash
export NHB_RPC_URL=http://127.0.0.1:8545
export NHB_ADDRESS=<a bech32 address that starts with nhb1>
```

The cookbook scripts default `NHB_RPC_URL` to `https://rpc.testnet.nhbcoin.net` when it is unset (`examples/cookbook/go/main.go`, `examples/cookbook/js/index.mjs`).

Addresses are Bech32 with the `nhb` prefix (`crypto.NHBPrefix` in `crypto/keys.go`).

## 2. Account snapshot: `nhb_getBalance`

```bash
curl -sS "$NHB_RPC_URL" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"nhb_getBalance","params":["'"$NHB_ADDRESS"'"]}'
```

The `result` is a `BalanceResponse` (`rpc/http.go`). Amounts are integer base units, sent as unquoted JSON numbers (the fields are `*big.Int`). Parse them with a big-integer-safe parser: `JSON.parse` in JavaScript rounds anything above 2^53.

| Field | Notes |
| --- | --- |
| `address` | Echo of the request parameter. |
| `balanceNHB`, `balanceZNHB` | Account balances. |
| `stake` | Total staked ZNHB on the account. |
| `lockedZNHB` | Locked ZNHB; `null` if the account record has no value (`balanceResponseFromAccount` only sets it when non-nil). |
| `delegatedValidator` | Bech32 address; omitted when the account does not delegate. |
| `pendingUnbonds` | Omitted when empty; entries are `{id, validator, amount, releaseTime}`. |
| `unbondingCompletesAt` | Omitted unless there are pending unbonds; the latest `releaseTime`. |
| `pendingStakingRewards` | Always present. |
| `username`, `nonce`, `engagementScore` | As named. |
| `validatorRegistered`, `validatorRegisteredAt` | `validatorRegisteredAt` is omitted when zero. |

Errors (HTTP 400, JSON-RPC code `-32602`):

- no parameter: `address parameter required`
- parameter is not a string: `invalid address parameter`
- not a valid address: `failed to decode address`

Use `nonce` from this response when building a transaction. `nhb-cli balance <address>` prints only some of them (`getBalance` in `cmd/nhb-cli/main.go`): username, NHB and ZNHB balances, stake, locked ZNHB, delegated validator (when set), validator-registered flag and registration time, pending unbonds, and nonce. It does not print `engagementScore` or `pendingStakingRewards`.

## 3. Recent activity

```bash
curl -sS "$NHB_RPC_URL" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"nhb_getLatestBlocks","params":[5]}'

curl -sS "$NHB_RPC_URL" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"nhb_getLatestTransactions","params":[10]}'

curl -sS "$NHB_RPC_URL" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"nhb_getEpochSummary","params":[]}'
```

- `nhb_getLatestBlocks(count)`: newest first; `count` defaults to 10 and is capped at 20 (`handleGetLatestBlocks`). Each element is a `types.Block` serialised with Go's default field names: `Header` (`height`, `timestamp`, `prevHash`, `stateRoot`, `txRoot`, `executionGraphRoot`, `validator`), `Transactions`, and `quorumCert` when present. Byte fields are base64 strings.
- `nhb_getLatestTransactions(count)`: walks blocks from the tip; `count` defaults to 20 and is capped at 50 (`handleGetLatestTransactions`). Each element is a raw `types.Transaction`: `chainId`, `type` (a number, see `core/types/transaction.go`), `nonce`, `to` (base64 of the 20-byte address), `value`, `data`, `gasLimit`, `gasPrice`, `r`, `s`, `v`, plus optional fields. There is no `hash`, `from` or `timestamp` field in this response. Use `nhb_getTransaction` / `nhb_getTransactionReceipt` with a hash for that.
- `nhb_getEpochSummary(epoch?)`: with no parameter, the latest epoch; the parameter may be a number or `{"epoch": n}`. Result fields: `epoch`, `height`, `finalizedAt`, `totalWeight` (string), `activeValidators` (`0x`-hex addresses), `eligibleValidatorCount`. Unknown epoch: HTTP 404, `epoch summary not found`.

## 4. The helper scripts

Run from the repository root:

```bash
NHB_ADDRESS=$NHB_ADDRESS NHB_RPC_URL=$NHB_RPC_URL go run ./examples/cookbook/go
NHB_ADDRESS=$NHB_ADDRESS NHB_RPC_URL=$NHB_RPC_URL node ./examples/cookbook/js/index.mjs
```

Both require `NHB_ADDRESS`, call `nhb_getBalance` and `nhb_getLatestTransactions` (count 10), and, if `NHB_API_KEY` and `NHB_API_SECRET` are set, also make a signed `GET /trades?buyer=...&status=SETTLED&limit=5` request against `NHB_API_BASE` (default `https://api.nhbcoin.net/escrow/v1`).

Known problems in the current scripts (source not changed here):

- The Go helper declares the balance fields as strings, but the node sends JSON numbers, so `json.Unmarshal` fails on the first call and the program exits with `rpc balance query failed`.
- Both helpers print `from -> to (value)` per transaction. The node's transaction JSON has no `from` field and `to` is base64, so the JavaScript output shows `undefined -> <base64> (<number>)`.
- The REST leg does not match any server in this repository. The helpers send `X-API-Key`, `X-Timestamp` (RFC 3339) and `X-Signature` (base64 HMAC over `METHOD\npath?query\nbody\ntimestamp`). The gateway authenticator in `gateway/auth/auth.go` expects `X-Api-Key`, `X-Timestamp` (Unix seconds), `X-Nonce` and `X-Signature` (hex HMAC over `timestamp\nnonce\nMETHOD\npath?query\nbody`), and `services/escrow-gateway/server.go` has no `/trades` route.

## 5. Postman collection

`examples/postman/NHB.postman_collection.json` contains four JSON-RPC requests (`nhb_getBalance`, `nhb_getLatestBlocks` with `[5]`, `nhb_getLatestTransactions` with `[10]`, `nhb_getEpochSummary`) and two REST requests (`GET {{api_base}}/trades` and `GET {{api_base}}/metrics/sla`). The variables are `rpc_base`, `api_base`, `wallet_address`, `api_key`, `api_secret`. The JSON-RPC requests work. The REST requests have the same mismatch as the helper scripts above, and `/metrics/sla` is not a route in `services/escrow-gateway/server.go`.

## 6. Troubleshooting

| Symptom | Cause (from the code) |
| --- | --- |
| HTTP 401 with code `-32001` | The method is privileged and the request has no valid `Authorization: Bearer <JWT>` (`requireAuth`). Errors: `missing Authorization header`, `Authorization header must use Bearer scheme`, `invalid JWT`, `JWT authentication not configured`. |
| HTTP 429 with code `-32020` | `RPC rate limit exceeded` (or `transaction rate limit exceeded` for `nhb_sendTransaction`). |
| HTTP 404, `unknown method <name>` (code `-32601`) | The method is not in the dispatcher. |
| HTTP 410, code `-32060` | The method is retired. See the [examples index](README.md). |
| HTTP 400 `failed to decode address` | The address is not valid Bech32 for this chain. |
