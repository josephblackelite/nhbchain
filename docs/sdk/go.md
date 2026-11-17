# Go SDK Guide

The Go SDK lives in the `sdk/` directory of this repository. It is a separate Go
module, `nhbchain/sdk` (`sdk/go.mod`), that depends on the root module `nhbchain`
through `replace nhbchain => ../`. The repository's `go.work` lists both modules
(`.` and `./sdk`). Neither module path contains a host name, so the SDK is used
from inside this repository's workspace; it is not fetched with `go get`.

## Packages

| Import path | What it is |
| --- | --- |
| `nhbchain/sdk/go/client` | JSON-RPC client that builds, signs and submits NHB and ZapNHB (ZNHB) transfers, and looks up an account nonce. |
| `nhbchain/sdk/consensus` | gRPC client for `consensusd` (`ConsensusService` and `QueryService`), plus envelope helpers `NewTx`, `Sign`, `Submit`. |
| `nhbchain/sdk/network` | gRPC client for the `p2pd` `NetworkService`. |
| `nhbchain/sdk/lending` | gRPC client for the lending service, plus builders and a signer for the lending transactions. |
| `nhbchain/sdk/gov` | gRPC client for the governance service (`gov.v1` `Query` and `Msg`). |
| `nhbchain/sdk/swap` | gRPC client for `swap.v1` `SwapService`. No server for this service is registered anywhere in this repository. |
| `nhbchain/sdk/go/identity/gateway` | HTTP client for the identity gateway's email endpoints, with HMAC request signing. |

`sdk/internal/dial` holds the shared dial options. It is an `internal` package,
so code outside `sdk/` uses the option variables re-exported by each client
package (for example `consensus.WithInsecure`).

## JSON-RPC transfers: `sdk/go/client`

```go
package main

import (
	"context"
	"encoding/hex"
	"log"
	"math/big"
	"os"
	"strings"
	"time"

	"nhbchain/crypto"
	"nhbchain/sdk/go/client"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	keyBytes, err := hex.DecodeString(strings.TrimPrefix(os.Getenv("SENDER_KEY_HEX"), "0x"))
	if err != nil {
		log.Fatalf("decode key: %v", err)
	}
	key, err := crypto.PrivateKeyFromBytes(keyBytes)
	if err != nil {
		log.Fatalf("parse key: %v", err)
	}

	rpc, err := client.New(
		os.Getenv("NHB_RPC_URL"), // for example http://localhost:8080
		client.WithAuthToken(os.Getenv("NHB_RPC_TOKEN")),
	)
	if err != nil {
		log.Fatalf("new client: %v", err)
	}

	tx, hash, err := rpc.SendZNHBTransfer(ctx, key, "nhb1recipient...", big.NewInt(1000))
	if err != nil {
		log.Fatalf("send: %v", err)
	}
	log.Printf("submitted nonce=%d hash=%s", tx.Nonce, hash)
}
```

Behavior, from `sdk/go/client/tx.go`:

- `client.New(endpoint, opts...)` requires a non-empty endpoint. Options:
  `WithHTTPClient`, `WithAuthToken`, `WithChainID`, `WithGasLimit`, `WithGasPrice`.
- Defaults: chain ID `types.NHBChainID()` (`0x4e4842`), gas limit `25000`, gas
  price `1`.
- `SendNHBTransfer` (transaction type `TxTypeTransfer`, `0x01`) and
  `SendZNHBTransfer` (`TxTypeTransferZNHB`, `0x10`) take
  `(ctx, key, recipient, amount, ...TxOption)`. `TxWithGasLimit` and
  `TxWithGasPrice` override the defaults for one call.
- The recipient is decoded with `crypto.DecodeAddress`, so it must be a bech32
  address. The amount must be greater than zero.
- Each call fetches the sender's nonce with `nhb_getBalance` (no token needed),
  signs with `Transaction.Sign`, then submits with `nhb_sendTransaction`. That
  call needs the bearer token; without one the client returns
  `client: auth token required for nhb_sendTransaction`.
- Both methods return the signed `*types.Transaction` and the string the node
  returned, which is `0x` plus the transaction hash. Acceptance into the mempool
  is not confirmation; poll `nhb_getTransactionReceipt`.
- A response with any HTTP status other than 200 is returned as
  `client: rpc error status <code>: <first 1024 bytes of the body>`; a JSON-RPC
  `error` object is returned as `client: rpc error <code>: <message>`. The client
  does not retry. A node whose query pool is full answers HTTP 429 (code
  `-32020`) or 503 (code `-32021`); see
  [RPC query limits](../ops/rpc-query-limits.md).
- `AccountNonce(ctx, address)` is exported for callers that build other
  transaction types.

## gRPC clients and TLS

`consensus`, `network`, `lending`, `gov` and `swap` share the same shape:
`Dial(ctx, target, opts ...DialOption)`, `New(conn)` to wrap an existing
`*grpc.ClientConn`, `Close()`, and (except `gov`, which has `Query()` and `Msg()`)
`Raw()` for the generated client. `Dial` adds the OpenTelemetry gRPC interceptors
(`otelgrpc`).

Transport defaults (`sdk/internal/dial/options.go`): when you pass no transport
option, the client uses TLS with a minimum version of TLS 1.2 and the host's
system certificate pool. Plaintext must be requested explicitly with
`WithInsecure()`. The available options are re-exported in each package:

- `WithTransportCredentials(creds)`
- `WithTLSConfig(*tls.Config)` (clones the config and raises the minimum version
  to TLS 1.2 if lower)
- `WithTLSFromFiles(certPath, keyPath, caPath)` and
  `WithSystemCertPool(serverName)`, which both return `(DialOption, error)`.
  The cert and key must be given together; the CA path is optional.
- `WithInsecure()`
- `WithContextDialer(fn)`, `WithPerRPCCredentials(creds)`, `WithDialOptions(...)`

## Consensus client: `sdk/consensus`

`consensus.Client` methods: `SubmitTransaction`, `SubmitEnvelope`, `GetHeight`,
`GetBlockByHeight`, `GetValidatorSet`, `GetMempool`, `QueryState`, `QueryPrefix`,
`SimulateTx`, `Raw()` and `QueryClient()`.

`consensusd` listens for gRPC on `127.0.0.1:9090` by default (`--grpc` flag,
`cmd/consensusd/main.go`). Its server requires either a shared secret or a
client certificate (`buildConsensusServerSecurity`). The shared secret is read
from the `authorization` request metadata (or the header named by
`NetworkSecurity.AuthorizationHeader`), as either the bare secret or
`Bearer <secret>` (`network.NewTokenAuthenticator`); attach it with
`consensus.WithPerRPCCredentials`. Plaintext is accepted only when the config
sets `AllowInsecure`, the process runs with `--allow-insecure`, and the listener
is on a loopback address; if the config supplies TLS material the server uses TLS
instead. In `consensusd`, the same flag and config setting also gate its own
plaintext connection to `p2pd` (`buildNetworkDialOptions` in
`cmd/consensusd/main.go`).

### State queries

`QueryState(ctx, namespace, key)` and `QueryPrefix(ctx, namespace, prefix)` are
served by `core/query_router.go` and `core/node.go`. The supported queries are:

| Namespace | `QueryState` key | `QueryPrefix` prefix |
| --- | --- | --- |
| `lending` | `markets`; `positions/<address>` (bech32 or `0x` hex) | `markets` (or empty) |
| `swap` | `vouchers/<id>`; `oracles` | none |
| `gov` (or `governance`) | `proposals/<id>`; `tallies/<id>`; `params` | `params` |

`proposals/latest` is not usable today: `queryGovernanceState` in
`core/query_router.go` matches every key that starts with `proposals/` and parses
the remainder as an unsigned integer, so `latest` fails with
`gov: invalid proposal id: ...` before the node's fallback handler
(`queryStateFallback` in `core/node.go`, which does contain a `proposals/latest`
case) is reached. `params`, `tallies/<id>` and `oracles` are answered by that
fallback. Any other key returns the error `query: not supported`.
`positions/<address>` returns a JSON array of `{ "poolId": ..., "account": ... }`,
one entry per pool where the address has an account.

### Transaction envelopes

`consensus.NewTx(payload, nonce, chainID, feeAmount, feeDenom, feePayer, memo)`
wraps a protobuf message in a `TxEnvelope`, `consensus.Sign(envelope, key)`
signs `sha256(proto.Marshal(envelope))`, and `Client.SubmitEnvelope` sends it.
The server verifies that signature and then converts the envelope to a
transaction in `consensus/codec/codec.go` (`TransactionFromEnvelope`). It
accepts only these payload types: `consensus.v1.Transaction`, the POS messages
(`MsgAuthorizePayment`, `MsgCapturePayment`, `MsgVoidPayment` and the
registry messages) and the swap `MsgPayoutReceipt`. Any other payload, including
the lending `MsgSupply` produced by `lending.NewMsgSupply`, is rejected with
`envelope: unsupported module payload type`. To move lending funds, build and
sign a native transaction with the lending package (next section).

## Lending: `sdk/lending`

Read calls on `lending.Client`: `GetMarket`, `ListMarkets`, `GetPosition`.

Mutation calls (`SupplyAsset`, `WithdrawAsset`, `BorrowAsset`, `RepayAsset`,
`DepositCollateral`, `WithdrawCollateral`, `Liquidate`) do not sign anything.
Each takes a `signedTxJSON` string, an already-signed native transaction, and
returns the transaction hash the node returned. The lending service relays that
JSON to the node's `nhb_sendTransaction` unchanged (see the comment on
`signed_tx_json` in `proto/lending/v1/lending.proto`); the signer recovered from
the signature is the account acting. The other request fields are used for
validation and logging.

Build and sign the transaction with `sdk/lending/txbuilder.go`:

- `NewSupplyTx`, `NewWithdrawTx`, `NewBorrowTx`, `NewRepayTx`,
  `NewDepositCollateralTx`, `NewWithdrawCollateralTx`
  `(chainID, nonce, poolID, amount, ...TxOption)`, and
  `NewLiquidateTx(chainID, nonce, poolID, borrower, ...TxOption)`. They produce
  transaction types `TxTypeLendingSupplyNHB` (`0x13`), `TxTypeLendingWithdrawNHB`
  (`0x14`), `TxTypeLendingBorrowNHB` (`0x17`), `TxTypeLendingRepayNHB` (`0x18`),
  `TxTypeLendingDepositZNHB` (`0x15`), `TxTypeLendingWithdrawZNHB` (`0x16`) and
  `TxTypeLendingLiquidate` (`0x1D`). Amounts are positive base-10 integer
  strings. Defaults: gas limit `50000`, gas price `1`; `WithGasLimit` and
  `WithGasPrice` override them. For the six supply, withdraw, borrow, repay
  and collateral builders, a pool ID of empty or `default` produces no payload and any other
  pool ID is sent as JSON `{"poolId": "..."}` (`encodePayload` in
  `sdk/lending/txbuilder.go`). `NewLiquidateTx` has value `0`, always sends a
  payload, JSON `{"poolId": "...", "borrower": "..."}`, with an empty pool ID
  replaced by `default`, and requires a non-empty borrower. The node refuses a
  liquidation whose signer is the borrower (`lending.ErrSelfLiquidation` in
  `applyLendingLiquidate`, `core/lending_native.go`); a borrower repays with a
  repay transaction instead.
- `SignAndEncode(tx, ecdsaKey)` signs and returns the JSON string to pass as
  `signedTxJSON`. `SenderAddress(key)` returns the bech32 account string.
- `lending.NewMsgSupply`, `NewMsgBorrow`, `NewMsgRepay` and `NewMsgLiquidate`
  only validate and build protobuf messages; they do not create native
  transactions (see the envelope note above).

`sdk/examples/lending/go/main.go` is a complete example: it looks up the nonce
through `sdk/go/client`, builds and signs supply, borrow and repay transactions,
and relays each through the lending service. Its flags are `-lending-endpoint`,
`-node-endpoint`, `-key`, `-market`, `-supply`, `-borrow`, `-repay`, `-insecure`
and `-timeout`.

The lending service's mutation RPCs require authentication (an API token in the
`authorization` or `x-api-token` metadata, or an mTLS client certificate) when
either is configured (`services/lending/server/auth.go`). Its default listen
address is `:50053` (`services/lendingd/config/config.go`).

## Governance and swap clients

`gov.Client`: `GetProposal`, `ListProposals(ctx, status, pageSize, pageToken)`,
`GetTally`, `SubmitProposal`, `Vote`, `Deposit`, `SetPauses`, and `Query()` /
`Msg()` for the generated clients. Message constructors: `NewMsgSubmitProposal`,
`NewMsgVote`, `NewMsgDeposit`, `NewMsgSetPauses`.

`swap.Client`: `GetPool`, `ListPools`, `SwapExactIn`, `SwapExactOut`, and
`NewMsgSwapExactIn` / `NewMsgSwapExactOut`.

`network.Client`: `Gossip`, `GetView`, `ListPeers`, `DialPeer`, `BanPeer`,
`Raw()`. `p2pd` serves this API on `127.0.0.1:9091` by default (`--grpc`).

## Identity gateway client

`gateway.New(baseURL, apiKey, apiSecret, opts...)` (options `WithHTTPClient`,
`WithClock`) returns a client with `RegisterEmail(ctx, email, aliasHint, ...)`,
`VerifyEmail(ctx, email, code, ...)` and `BindEmail(ctx, aliasID, email, consent, ...)`,
which POST to `/identity/email/register`, `/identity/email/verify` and
`/identity/alias/bind-email`. `WithIdempotencyKey(key)` sets the
`Idempotency-Key` header.

Every request carries these headers (`sdk/go/identity/gateway/client.go`):

- `X-API-Key`: the API key.
- `X-API-Timestamp`: Unix seconds.
- `X-API-Signature`: hex `HMAC-SHA256(apiSecret, method + "\n" + path + "\n" +
  hex(sha256(body)) + "\n" + timestamp)`.

The server (`services/identity-gateway/server.go`) rejects timestamps more than
its configured skew away from its clock, five minutes by default, and replays a
cached response when an `Idempotency-Key` repeats.

## Tests

The packages ship unit tests next to the code (for example
`sdk/go/client/tx_test.go`, `sdk/consensus/client_test.go`). Run them with
`go test ./...` from the `sdk/` directory.
