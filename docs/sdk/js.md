# JavaScript & TypeScript SDK Guide

The TypeScript code lives in `sdk/ts/` and the generated gRPC stubs in
`clients/ts/`. There is no published npm package: `sdk/ts/package.json` contains
only `{"type": "module"}` (no name, version or dependencies), and nothing in the
repository defines `@nhbchain/sdk`. Use the sources directly from a checkout.

`sdk/ts/src/wallet.ts` and `sdk/ts/src/identityGateway.ts` import
`@scure/base`, `@noble/hashes`, `@noble/secp256k1` (wallet) and Node's `crypto`
(identity gateway). The repository root `package.json` declares the three npm
packages. Its only SDK script is:

```bash
npm run test:sdk:ts   # tsx --test sdk/ts/test/wallet.test.ts
```

## `WalletClient` (`sdk/ts/src/wallet.ts`)

`WalletClient` builds and submits NHB and ZapNHB (ZNHB) transfers over JSON-RPC.
It is both the default export and a named export.

```ts
import WalletClient from './sdk/ts/src/wallet'; // adjust the relative path

const client = new WalletClient({
  baseUrl: process.env.NHB_RPC_URL!,      // for example http://localhost:8080
  authToken: process.env.NHB_RPC_TOKEN!,  // bearer token for nhb_sendTransaction
});

const { transaction, response } = await client.sendTransfer({
  privateKey: process.env.SENDER_KEY!,    // 32-byte hex (0x prefix optional) or Uint8Array
  recipient: 'nhb1recipient...',
  amount: 1000000000000000000n,
  asset: 'ZNHB',                          // 'NHB' or 'ZNHB'
});
```

Behavior:

- Constructor options: `baseUrl` (required), `authToken`, `fetchImpl` (defaults to
  `globalThis.fetch`; one must exist), `chainId` (default `0x4e4842`),
  `gasLimit` (default `25000`), `gasPrice` (default `1`). Gas values must be
  greater than zero.
- `sendTransfer` options: `recipient`, `amount`, `privateKey`, `asset`
  (`'NHB'` or `'ZNHB'`; **the default is `'ZNHB'`**), and optional `gasLimit` and
  `gasPrice`. The amount must be positive.
- The recipient must be a bech32 address with the `nhb` prefix and 20 data bytes.
  A string that does not decode as bech32, or decodes with a different prefix,
  throws `Invalid NHB address.`; a valid `nhb` bech32 string whose data is not 20
  bytes throws `Expected a 20-byte address.` (`sdk/ts/src/wallet.ts:31-46`).
- Other input errors (`sdk/ts/src/wallet.ts`): `Transfer amount must be positive.`,
  `Private key must be 32 bytes.` (a `Uint8Array` of the wrong length),
  `Expected a 32-byte hex private key.` (a hex string of the wrong length),
  `Invalid hex character in private key.`, and `Gas limit must be greater than
  zero.` / `Gas price must be greater than zero.`
- It derives the sender address from the key (keccak-256 of the public key, last
  20 bytes, bech32 with prefix `nhb`), reads the nonce with `nhb_getBalance` (no
  token), signs, and submits with `nhb_sendTransaction`.
- Transfer type constants: `TRANSFER_TYPE_NHB = 0x01`,
  `TRANSFER_TYPE_ZNHB = 0x10`.
- `sendTransfer` throws if `authToken` is not set (`Method nhb_sendTransaction
  requires an authorization token.`), and on a non-2xx HTTP status or a JSON-RPC
  `error`.
- It returns `{ transaction, response }`. `transaction` is the signed payload
  (`chainId`, `type`, `nonce`, `to` and `data` as base64, `value`, `gasLimit`,
  `gasPrice`, `r`, `s`, `v` as decimal strings). `response` is the string the
  node returned, which is `0x` plus the transaction hash.

**Known signing mismatch.** The wallet signs
`sha256` of a JSON string built by `serializeTxForHash` in `wallet.ts`. For any
transaction type above zero, including both transfer types, the node computes its
signing hash differently: `Transaction.Hash` in `core/types/transaction.go` hashes
the `NHB_TX_V3_MAINNET` binary encoding, and `Transaction.From` recovers the
sender from that hash. A signature produced over the JSON hash therefore does not
recover to the sender's address on the node. The unit tests
(`sdk/ts/test/wallet.test.ts`) run against a mock server and do not check the
signature against the node. Until this is reconciled, use the Go SDK
(`docs/sdk/go.md`) or the `nhb-cli send-nhb` / `send-znhb` commands to send
transfers.

## `IdentityGatewayClient` (`sdk/ts/src/identityGateway.ts`)

```ts
import IdentityGatewayClient from './sdk/ts/src/identityGateway';

const gateway = new IdentityGatewayClient({
  baseUrl: 'https://identity.example',
  apiKey: process.env.ID_API_KEY!,
  apiSecret: process.env.ID_API_SECRET!,
});

await gateway.registerEmail('user@example.com', 'alias-hint', { idempotencyKey: 'k1' });
await gateway.verifyEmail('user@example.com', '123456');
await gateway.bindEmail('alias-id', 'user@example.com', true);
```

It POSTs to `/identity/email/register`, `/identity/email/verify` and
`/identity/alias/bind-email`. Each request is signed exactly like the Go client:

- `X-API-Key`, `X-API-Timestamp` (Unix seconds) and `X-API-Signature`, where the
  signature is hex `HMAC-SHA256(apiSecret, "POST\n" + path + "\n" +
  hex(sha256(body)) + "\n" + timestamp)`;
- an optional `Idempotency-Key` header from `options.idempotencyKey`.

A non-2xx response throws an `Error` (`identity gateway <status>: <body>`) with
`status` and `body` properties. The `clock` option (milliseconds) replaces
`Date.now` for tests. The server's timestamp tolerance and idempotency replay are
described in [the Go SDK guide](./go.md#identity-gateway-client).

## Generated gRPC stubs (`clients/ts`)

`clients/ts/` holds code generated by `protoc-gen-ts_proto` with
`outputServices=grpc-js` (`buf.gen.yaml`): `consensus/v1`, `lending/v1`, `gov/v1`,
`swap/v1`, `network/v1`, `fees/v1`, `pos/` and `tx/`. `clients/ts/escrow/dispute.ts`
is hand-written and talks to the JSON-RPC endpoint. For a walk-through of the
lending stub see `sdk/examples/lending/ts/README.md`. The POS examples in
`sdk/pos/examples/` (`create_intent.go`, `submit_and_watch.ts`, `subscriber.ts`)
show intent creation and finality subscription.

## Examples workspace

`examples/` is a separate workspace (`examples/package.json`); it includes
`examples/lib-sdk`, a small helper package named `@nhb/examples-lib-sdk` used by
those example apps. It is example code, not part of the SDK.
