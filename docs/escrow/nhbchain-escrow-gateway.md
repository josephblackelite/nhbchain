# Escrow Gateway: configuration, signing and relay model

This document describes how `services/escrow-gateway` is configured and how it turns REST calls into chain transactions. The endpoint list is in [`gateway-api.md`](./gateway-api.md); the on-chain rules are in [`escrow.md`](./escrow.md).

## What the gateway does

* Accepts REST requests authenticated with an API key + HMAC (every route, reads included) and, for escrow create, release, refund and dispute, a participant wallet signature.
* Relays the action to the node as a signed transaction using its own **relayer key**: `TxTypeDelegatedCreateEscrow`, `TxTypeDelegatedReleaseEscrow`, `TxTypeDelegatedRefundEscrow`, `TxTypeDelegatedDisputeEscrow`, or `TxTypeArbitrateRelease` / `TxTypeArbitrateRefund` for resolve (`services/escrow-gateway/node_client.go`). The transaction is submitted with `nhb_sendTransaction`.
* Reads the node with `escrow_get`, `escrow_getRealm`, `p2p_getTrade` and `nhb_getBalance`.
* Stores idempotency records, an audit log, P2P offers, webhook subscriptions and delivery attempts, and a trade table that no running code path fills, in SQLite.

The node's `escrow_create`, `escrow_release`, `escrow_refund`, `escrow_dispute` and `escrow_resolve` JSON-RPC methods are disabled, and the gateway does not call them.

The relayer only pays for and sequences the transaction. The chain authorizes the action from the participant signature embedded in the transaction data (`Engine.CreateWithSignature`, `ReleaseWithSignature`, `RefundWithSignature`, `DisputeWithSignature`, `ResolveWithSignatures` in `native/escrow/engine.go`), never from the relayer.

## Configuration

All values come from environment variables (`LoadConfigFromEnv`, `services/escrow-gateway/config.go`).

| Variable | Required | Default | Meaning |
|----------|----------|---------|---------|
| `ESCROW_GATEWAY_NODE_URL` | yes | | Node JSON-RPC URL. |
| `ESCROW_GATEWAY_API_KEYS` | yes | | JSON array `[{"key":"...","secret":"...","merchant":{...}}]`. At least one entry; `key` and `secret` are required. |
| `ESCROW_GATEWAY_RELAYER_KMS_ENV` | yes | | Name of another environment variable that holds the relayer's raw hex secp256k1 private key (`0x` prefix optional). Startup fails if it is empty or the named variable is unset or invalid. |
| `ESCROW_GATEWAY_LISTEN` | no | `:8081` | HTTP listen address. |
| `ESCROW_GATEWAY_NODE_TOKEN` | no | none | Bearer token sent as `Authorization: Bearer ...` on every node RPC call. The node requires authentication for `nhb_sendTransaction` (`rpc/http.go`), so every relayed write fails without a valid token. |
| `ESCROW_GATEWAY_DB_PATH` | no | `escrow-gateway.db` | SQLite file. |
| `ESCROW_GATEWAY_TIMESTAMP_SKEW` | no | `2m` | Allowed request clock skew (Go duration). The authenticator caps it at 2 minutes and logs a warning at startup when a larger value is lowered. |
| `ESCROW_GATEWAY_NONCE_TTL` | no | twice the skew | How long nonces are remembered; positive Go duration, never below the skew; capped at 10 minutes by the authenticator (warning logged when lowered). |
| `ESCROW_GATEWAY_NONCE_CAP` | no | `1024` | Nonce cache size per key; positive integer; capped at 65536 (warning logged when lowered). |
| `ESCROW_GATEWAY_RELAYER_MIN_BALANCE_WEI` | no | `1000000000000000000` | Low-balance warning threshold (non-negative integer). |
| `ESCROW_GATEWAY_RELAYER_BALANCE_CHECK_INTERVAL` | no | `10m` | Interval of the balance check. |
| `ESCROW_GATEWAY_QUEUE_CAP` | no | `1024` | Webhook queue capacity. |
| `ESCROW_GATEWAY_QUEUE_HISTORY` | no | `256` | Webhook history size. |
| `ESCROW_GATEWAY_QUEUE_TTL` | no | `15m` | Webhook queue entry lifetime. |
| `NHB_ENV`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_INSECURE` | no | | Logging environment label and OpenTelemetry export (`main.go`); insecure defaults to true. |

Merchant settings (optional `merchant` object on an API key entry, `sanitizeMerchantConfig`):

```json
{"key":"acme","secret":"...","merchant":{"identity":"acme","realm":{"default":"acme","scope":"platform","type":"private","enforceIdentityMatch":true}}}
```

`identity` defaults to the key (max 128 characters); `realm.default` is at most 64 characters; `scope` is `platform` or `marketplace`; `type` is `public` or `private`; `enforceIdentityMatch` forces type `private` and defaults the realm to the identity. On `POST /escrow/create`, the merchant's default realm is applied when the request has none, and a request without a realm is rejected (`realm selection required`) when any realm setting is configured.

Realm checks on create (`enforceRealmConstraints`): if `scope` is set, the realm's `scope` from `escrow_getRealm` must equal it. The node's realm result has no `type` field (`EscrowRealmMetadataResult` in `rpc/modules/escrow.go` carries `scope`, `providerProfile`, `arbitrationFeeBps`, `feeRecipient`), so there is nothing to compare a configured `type` against. Instead, when `type` is `private` or `enforceIdentityMatch` is set, the realm name must equal the merchant identity (case-insensitive), else the request fails with `realm must match merchant identity <identity>`. A merchant of type `public` accepts any realm that passes the scope check.

## Relayer

* At startup the gateway derives the relayer address from the key, reads its account nonce once with `nhb_getBalance`, and logs the address (`main.go`, `InitRelayer`).
* Each relayed transaction uses `types.NHBChainID()`, gas limit `30000`, gas price `1`, and the relayer's next nonce; the nonce is advanced only after the node accepts the submission. Submissions are serialized under a mutex.
* A goroutine (`startRelayerBalanceMonitor`) checks the relayer's NHB balance at startup and every check interval and logs a warning (`escrow gateway relayer balance is low`) when the balance is at or below the threshold. It is a log line only.
* If the relayer is not initialized, mutating endpoints fail with `ErrRelayerNotConfigured`.

## Signing envelopes

All signatures are 65-byte secp256k1 signatures over `keccak256` of the exact JSON bytes, hex encoded, sent in `X-Sig` (create, release, refund, dispute) or in the body (resolve). There is no EIP-191 prefix. The gateway verifies each signature before relaying (403 on mismatch) and the chain verifies it again.

* **Create** (signer must equal `payer`; `X-Sig-Addr` must equal the request's `payer`):

  ```json
  {"action":"create","payer":"<40 hex>","payee":"<40 hex>","token":"NHB","amount":"<decimal>","feeBps":0,"deadline":1730000000,"nonce":1,"mediator":"<40 hex, omitted if unset>","meta":"<as supplied, omitted if empty>","realm":"<omitted if empty>"}
  ```

  `payer`, `payee` and `mediator` are lower-case hex of the 20 address bytes (the gateway converts from bech32). `meta` is the string exactly as sent in the request; the delegated-create path on the chain (`CreateWithSignature`) requires it to decode to exactly 32 bytes (a 64-character hex string, optional `0x`). The JSON key order is the order shown; sign the same bytes the gateway will build.
* **Release / refund / dispute:**

  ```json
  {"escrowId":"<escrowId exactly as sent in the request>","action":"release","reason":"<dispute only, omitted if empty>"}
  ```

  `action` is `release`, `refund` or `dispute`. The chain requires `escrowId` to decode to the escrow's 32-byte ID (hex, optional `0x`).
* **Resolve** (no wallet headers): the body carries the decision object and the arbitrators' signatures over `keccak256(decision bytes)`:

  ```json
  {"escrowId":"0x...","decision":{"escrowId":"<64 hex>","outcome":"release","policyNonce":1,"metadata":"<64 hex, optional>"},"signatures":["0x...","0x..."]}
  ```

  The gateway relays the decision bytes without re-encoding them. The transaction type is chosen from the decision's `outcome` (`release` selects `TxTypeArbitrateRelease`, `refund` selects `TxTypeArbitrateRefund`); the chain takes the outcome from the signed decision either way. The escrow must have been created with a `realm` (which freezes the arbitrator policy) and the number of distinct valid arbitrator signatures must reach the frozen threshold. `policyNonce` comes from the escrow (`escrow_get` returns it for realm-bound escrows).

Replay safety of these signatures comes from the chain's idempotent status transitions; envelopes contain no timestamp or nonce (except the create envelope's escrow `nonce`, which is part of the escrow's identity).

## Pay intents

`POST /escrow/create` returns a `payIntent` (`payintent.go`): the vault address for the token (`keccak256("module/escrow/vault/" + TOKEN)`, last 20 bytes, bech32; identical to the chain's `EscrowVaultAddress`), the amount, and a memo `ESCROW:<ID upper-cased>`. The memo is informational: the chain funds an escrow only when the payer sends `TxTypeLockEscrow`. See [`escrow.md`](./escrow.md).

## Not implemented or retired

* Only `escrow.created` webhooks exist: the worker is started by `main.go`, but the gateway has no feed of chain events, so no other event is produced. See [`gateway-api.md`](./gateway-api.md) section 4.
* `POST /p2p/accept` always fails: trade creation through the gateway is retired (`ErrP2PTradeRetired`).
* The gateway exposes no endpoint to read its audit log, to register webhooks, or to settle, dispute or resolve P2P trades.
