# NHB Chain SDK

This directory holds the Go SDK (module `nhbchain/sdk`, see `go.mod`), a small
TypeScript SDK (`ts/`) and SDK examples. The module is part of the repository's
`go.work` workspace and resolves the root `nhbchain` module through
`replace nhbchain => ../`; it is used from inside this repository, not fetched
from a module proxy.

Guides: [Go SDK](../docs/sdk/go.md), [JavaScript and TypeScript
SDK](../docs/sdk/js.md), [transfer examples](../docs/sdk/examples.md), [wallet
builder guide](../docs/sdk/wallets.md).

## Layout

| Path | Contents |
| --- | --- |
| `go/client` | JSON-RPC client: `SendNHBTransfer`, `SendZNHBTransfer`, `AccountNonce`. Defaults: gas limit `25000`, gas price `1`, chain ID `0x4e4842`. `nhb_sendTransaction` needs a bearer token (`WithAuthToken`). |
| `consensus` | gRPC client for `consensusd` (submit, height, blocks, validator set, mempool, `QueryState`, `QueryPrefix`, `SimulateTx`) and the `NewTx` / `Sign` / `Submit` envelope helpers. |
| `network` | gRPC client for the `p2pd` network service (`Gossip`, `GetView`, `ListPeers`, `DialPeer`, `BanPeer`). |
| `lending` | gRPC client for the lending service, plus transaction builders (`NewSupplyTx`, ...) and `SignAndEncode`. |
| `gov` | gRPC client for the governance service. |
| `swap` | gRPC client for `swap.v1` `SwapService`. This repository registers no server for that service. |
| `go/identity/gateway` | HTTP client for the identity gateway email endpoints (HMAC-signed requests). |
| `internal/dial` | Shared gRPC dial options, re-exported by each client package. |
| `ts` | `src/wallet.ts` (`WalletClient`) and `src/identityGateway.ts` (`IdentityGatewayClient`), with tests in `test/`. |
| `examples/lending` | Go and TypeScript lending examples (`go/main.go`, `ts/README.md`). |
| `pos/examples` | POS reference examples: `create_intent.go` and `submit_and_watch.ts` call the POS `Tx` gRPC service, which the node no longer registers (they fail with `Unimplemented`; see the note in `create_intent.go` and `Server.Serve` in `rpc/http.go`); `subscriber.ts` uses the finality subscription (`Realtime` gRPC service and the `/ws/pos/finality` WebSocket), which is still served. |

## gRPC transport defaults

Every gRPC client (`consensus`, `network`, `lending`, `gov`, `swap`) is created
with `Dial(ctx, target, opts...)`. With no transport option it uses TLS with a
minimum of TLS 1.2 and the host operating system's certificate pool
(`internal/dial.Resolve`). To connect to a plaintext endpoint, such as a local
development server, pass `WithInsecure()` from the package you are using (for
example `network.WithInsecure()`).

You can instead pass `WithTLSConfig`, `WithTLSFromFiles(cert, key, ca)`,
`WithSystemCertPool(serverName)` or `WithTransportCredentials`.
`WithTLSFromFiles` and `WithSystemCertPool` return an error along with the
option. `WithPerRPCCredentials`, `WithContextDialer` and `WithDialOptions` attach
credentials, a custom dialer or raw gRPC dial options.

## JSON-RPC transfer helpers

`Client.SendNHBTransfer` and `Client.SendZNHBTransfer` in `go/client` fetch the
sender's nonce with `nhb_getBalance`, apply the client's default gas limit and
gas price (or per-call `TxWithGasLimit` / `TxWithGasPrice`), sign the
transaction, and submit it with `nhb_sendTransaction`. They emit
`TxTypeTransfer` (`0x01`) and `TxTypeTransferZNHB` (`0x10`) respectively and
return the signed transaction and the node's response string (`0x` plus the
transaction hash).

## Tests

Run `go test ./...` from this directory for the Go packages. The TypeScript
wallet test is run from the repository root with `npm run test:sdk:ts`.
