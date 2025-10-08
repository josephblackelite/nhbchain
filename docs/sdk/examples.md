# SDK transfer examples

The SDKs expose thin helpers for composing NHB and ZapNHB (ZNHB) transfers. The
examples below show the minimal setup for both token types using Go and
TypeScript.

## Go

### NHB transfer

```go
recipient, _ := crypto.DecodeAddress("nhb1recipient...")
account := fetchAccount("nhb1senderaddress...") // reuse the nhb-cli helper

tx := &types.Transaction{
    ChainID:  types.NHBChainID(),
    Type:     types.TxTypeTransfer,
    Nonce:    account.Nonce,
    To:       recipient.Bytes(),
    Value:    big.NewInt(100_000_000_000_000_000),
    GasLimit: 25_000,
    GasPrice: big.NewInt(1),
}

if err := tx.Sign(loadPrivateKey()); err != nil {
    log.Fatalf("sign transaction: %v", err)
}

payload := map[string]any{
    "jsonrpc": "2.0",
    "id":      1,
    "method":  "nhb_sendTransaction",
    "params":  []any{tx},
}
if err := postWithAuth(context.Background(), payload); err != nil {
    log.Fatalf("submit transaction: %v", err)
}
```

The snippet mirrors the helpers shipped with `cmd/nhb-cli`: `fetchAccount`,
`loadPrivateKey`, and `postWithAuth` wrap the JSON-RPC calls and bearer-token
submission.

### ZNHB transfer

```go
key := loadPrivateKey()
client, err := wallet.New(
    "https://rpc.nhb.dev",
    wallet.WithAuthToken(os.Getenv("NHB_RPC_TOKEN")),
)
if err != nil {
    log.Fatalf("new wallet client: %v", err)
}

tx, result, err := client.SendZNHBTransfer(
    context.Background(),
    key,
    "nhb1recipient...",
    big.NewInt(100_000_000_000_000_000),
)
if err != nil {
    log.Fatalf("send znhb transfer: %v", err)
}
log.Printf("Queued ZNHB transfer nonce=%d result=%s", tx.Nonce, result)
```

The helper fetches the latest nonce via `nhb_getBalance`, signs the
`TxTypeTransferZNHB` payload, and forwards it through `nhb_sendTransaction` with
the provided bearer token.

## TypeScript

### NHB transfer

```ts
import { rpcClient } from '@nhb/examples-lib-sdk';
import { keccak_256 } from '@noble/hashes/sha3';
import { getPublicKey, signSync } from '@noble/secp256k1';

const rpc = rpcClient({ baseUrl: process.env.NHB_RPC_URL!, apiKey: process.env.NHB_RPC_TOKEN! });

async function sendNHB(privateKey: Uint8Array, recipient: string, amount: bigint) {
  const sender = deriveBech32(privateKey);
  const account = await rpc.request('nhb_getBalance', [sender]);
  const tx = buildTransaction({
    type: 0x01,
    nonce: BigInt(account.nonce),
    to: decodeRecipient(recipient),
    value: amount,
  });
  const digest = sha256(encodeForHash(tx));
  const [sig, recid] = signSync(digest, privateKey, { der: false, recovered: true });
  attachSignature(tx, sig, recid);
  await rpc.request('nhb_sendTransaction', [tx]);
}
```

### ZNHB transfer

```ts
import WalletClient from 'nhbchain/sdk/ts/src/wallet';

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

The `WalletClient` class performs the nonce lookup, signs the payload with the
provided private key, and submits the envelope with the configured RPC token.
Use `asset: 'NHB'` to reuse the helper for standard NHB transfers.
