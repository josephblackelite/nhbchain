# SDK transfer examples

Sending NHB and ZapNHB (ZNHB) transfers with the code in `sdk/`. Both assets use
the same flow: read the sender's nonce with `nhb_getBalance`, sign a transaction
(`TxTypeTransfer`, `0x01`, for NHB; `TxTypeTransferZNHB`, `0x10`, for ZNHB), and
submit it with `nhb_sendTransaction`, which needs a bearer token
(`NHB_RPC_TOKEN`). Amounts are positive integers in base units and recipients are
bech32 addresses.

## Go

`sdk/go/client` (see the [Go SDK guide](./go.md)) does the nonce lookup, signing
and submission. Its default gas limit is `25000` and gas price `1`.

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
		os.Getenv("NHB_RPC_URL"),
		client.WithAuthToken(os.Getenv("NHB_RPC_TOKEN")),
	)
	if err != nil {
		log.Fatalf("new client: %v", err)
	}

	recipient := "nhb1recipient..."
	amount := big.NewInt(100_000_000_000_000_000)

	// NHB transfer.
	tx, hash, err := rpc.SendNHBTransfer(ctx, key, recipient, amount)
	if err != nil {
		log.Fatalf("send nhb transfer: %v", err)
	}
	log.Printf("NHB transfer nonce=%d hash=%s", tx.Nonce, hash)

	// ZNHB transfer, with a per-call gas limit override.
	tx, hash, err = rpc.SendZNHBTransfer(ctx, key, recipient, amount, client.TxWithGasLimit(30_000))
	if err != nil {
		log.Fatalf("send znhb transfer: %v", err)
	}
	log.Printf("ZNHB transfer nonce=%d hash=%s", tx.Nonce, hash)
}
```

The second return value is the string `nhb_sendTransaction` returned: `0x` plus
the transaction hash. Use it with `nhb_getTransactionReceipt` to confirm
inclusion.

To do the same from the command line, see [`send-nhb` and `send-znhb`](../cli/send.md).

## TypeScript

`sdk/ts/src/wallet.ts` exposes `WalletClient` with a single `sendTransfer`
method; the `asset` option selects `'NHB'` or `'ZNHB'` and defaults to `'ZNHB'`.

```ts
import WalletClient from './sdk/ts/src/wallet'; // adjust the relative path

const client = new WalletClient({
  baseUrl: process.env.NHB_RPC_URL!,
  authToken: process.env.NHB_RPC_TOKEN!,
});

const { transaction, response } = await client.sendTransfer({
  privateKey: process.env.SENDER_KEY!,
  recipient: 'nhb1recipient...',
  amount: 1000000000000000000n,
  asset: 'ZNHB',
});

console.log('Submitted ZNHB transfer', transaction.nonce, response);
```

Read the "Known signing mismatch" note in the [JavaScript & TypeScript SDK
guide](./js.md#walletclient-sdktssrcwalletts) before relying on this client: its
signing hash differs from the one the node verifies.
