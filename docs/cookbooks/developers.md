# Developer Cookbooks

Task-oriented examples that use the code in this repository. Blocks marked with
an `embed` comment in the Markdown source are copied from a file under
`examples/` and are checked by `go run ./tools/docs/verify.go` (which
`make audit:docs` runs): the block must match the file, each embedded Go file
must build, and embedded TypeScript is compiled with
`npx tsc --noEmit --project examples/docs/tsconfig.json`. The first example
below is not embedded and is not compiled by that check.

## First transaction

Send a transfer with the Go SDK's JSON-RPC client.

1. Have a node RPC endpoint (`nhb-cli` defaults to `http://localhost:8080`), a
   bearer token for it (`NHB_RPC_TOKEN`), and a funded account's private key.
2. Build the client with `client.New`, passing the endpoint and
   `client.WithAuthToken`.
3. Call `SendNHBTransfer` (or `SendZNHBTransfer`). The client reads the nonce with
   `nhb_getBalance`, signs, and submits with `nhb_sendTransaction`.
4. Keep the returned hash (`0x` plus the transaction hash) and poll
   `nhb_getTransactionReceipt` for it.

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

	tx, hash, err := rpc.SendNHBTransfer(ctx, key, os.Getenv("RECIPIENT"), big.NewInt(1000))
	if err != nil {
		log.Fatalf("send: %v", err)
	}
	log.Printf("submitted nonce=%d hash=%s", tx.Nonce, hash)
}
```

`RECIPIENT` must be a bech32 address. See the [Go SDK guide](../sdk/go.md) for
the client's defaults and options, and [`send-nhb`](../cli/send.md) for the CLI
equivalent.

## Query positions

Read a lending account's positions from the consensus query service.

1. Call `QueryState` on the `lending` namespace with the key
   `positions/<address>` (`core/query_router.go`). The address may be bech32 or
   `0x` hex.
2. The value is a JSON array with one `{ "poolId": ..., "account": ... }` entry
   per pool where the address has an account. An address with no accounts yields
   `[]`.
3. Decode it and print it.

Set `LENDING_ADDRESS` to the address to query; `CONSENSUSD_GRPC_ADDR` selects the
`consensusd` gRPC endpoint (the snippet falls back to `localhost:9090`;
`consensusd` itself listens on `127.0.0.1:9090` unless started with a different
`--grpc`). Note that `consensusd` requires a shared secret or a client
certificate on every gRPC call and serves plaintext only on a loopback listener
when explicitly allowed (`buildConsensusServerSecurity` in
`cmd/consensusd/main.go`), so a real deployment needs the TLS and
`WithPerRPCCredentials` options described in the [Go SDK guide](../sdk/go.md)
in addition to what this snippet passes.

<!-- embed:examples/queries/lending_positions.go -->
```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"nhbchain/sdk/consensus"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	endpoint := os.Getenv("CONSENSUSD_GRPC_ADDR")
	if endpoint == "" {
		endpoint = "localhost:9090"
	}
	addr := os.Getenv("LENDING_ADDRESS")
	if addr == "" {
		log.Fatal("set LENDING_ADDRESS (bech32 or 0x hex) to query positions")
	}

	client, err := consensus.Dial(ctx, endpoint, consensus.WithInsecure())
	if err != nil {
		log.Fatalf("dial consensus service: %v", err)
	}
	defer client.Close()

	value, _, err := client.QueryState(ctx, "lending", fmt.Sprintf("positions/%s", addr))
	if err != nil {
		log.Fatalf("query positions: %v", err)
	}
	if len(value) == 0 {
		fmt.Println("no active positions for address")
		return
	}
	var decoded []map[string]any
	if err := json.Unmarshal(value, &decoded); err != nil {
		log.Fatalf("decode response: %v", err)
	}
	pretty, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		log.Fatalf("format response: %v", err)
	}
	fmt.Printf("Positions for %s:\n%s\n", addr, string(pretty))
}
```

The same data is available from the lending service's `GetPosition` call. The
TypeScript snippet below calls it with the stubs in `clients/ts`. It reads
`LENDING_GRPC_ADDR` (fallback `localhost:9444`; the lending service's own default
listen address is `:50053`) and `LENDING_ACCOUNT`. Read calls do not need
authentication (`services/lending/server/auth.go` authenticates only the
mutation RPCs).

<!-- embed:examples/docs/ts/query-positions.ts -->
```ts
import path from 'node:path';
import process from 'node:process';
import { credentials, ClientUnaryCall, ServiceError, Metadata } from '@grpc/grpc-js';
import { loadPackageDefinition } from '@grpc/grpc-js';
import { loadSync } from '@grpc/proto-loader';

type LendingServiceClient = {
  GetPosition(
    request: { account: string },
    metadata: Metadata,
    callback: (err: ServiceError | null, response: { position?: unknown }) => void
  ): ClientUnaryCall;
};

type LendingServiceCtor = new (address: string, creds: ReturnType<typeof credentials.createInsecure>) => LendingServiceClient;

const protoRoot = path.resolve(__dirname, '../../../proto/lending/v1/lending.proto');
const definition = loadSync(protoRoot, {
  keepCase: true,
  longs: String,
  enums: String,
  defaults: true,
  oneofs: true
});

const pkg = loadPackageDefinition(definition) as unknown as {
  lending: {
    v1: {
      LendingService: LendingServiceCtor;
    };
  };
};

const endpoint = process.env.LENDING_GRPC_ADDR ?? 'localhost:9444';
const client = new pkg.lending.v1.LendingService(endpoint, credentials.createInsecure());

client.GetPosition(
  { account: process.env.LENDING_ACCOUNT ?? 'nhb1exampleaddress' },
  new Metadata(),
  (err, resp) => {
    if (err) {
      console.error('position query failed', err);
      return;
    }
    console.log('active positions', JSON.stringify(resp.position ?? {}, null, 2));
  }
);
```

## Send a lending transaction through the lending service

The lending service's mutation RPCs (`SupplyAsset`, `WithdrawAsset`,
`BorrowAsset`, `RepayAsset`, `DepositCollateral`, `WithdrawCollateral`,
`Liquidate`) relay a transaction you have already signed; they never sign for
you, and a request without `signed_tx_json` is rejected.

1. Look up the account nonce with `AccountNonce` from `sdk/go/client`.
2. Build the transaction with `lending.NewSupplyTx` (or the matching builder) and
   sign it with `lending.SignAndEncode`.
3. Pass the resulting JSON as the `signedTxJSON` argument to the matching
   `lending.Client` method, which returns the mempool-accepted transaction hash.
4. Poll `GetPosition` after the transaction confirms.

`sdk/examples/lending/go/main.go` implements these steps for supply, borrow and
repay. See the [Go SDK guide](../sdk/go.md#lending-sdklending) for the builders,
their defaults and the service's authentication.
