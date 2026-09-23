# API Replay Protection

Two components in this repository authenticate requests with an API key and an HMAC-SHA256 signature. Both use the same implementation, `gateway/auth/auth.go`:

- the node's JSON-RPC server, for the swap methods listed in `isPublicSwapMethod` in `rpc/http.go` (`swap_submitVoucher`, `swap_voucher_get`, `swap_voucher_list`, `swap_voucher_export`, `nhb_requestSwapApproval`, `nhb_swapMint`, `nhb_swapBurn`, `nhb_getSwapStatus`, `nhb_getSwapQuote`, `nhb_checkSwapAllowance`, `nhb_getOraclePrice`, `swap_getRiskParams`, `swap_getRedemptionFeeParams`), enabled when `[RPCSwapAuth].Secrets` is non-empty;
- the escrow gateway (`services/escrow-gateway`), on its write routes and on its three read routes (see [Escrow gateway routes](#escrow-gateway-routes)).

In the node, this HMAC check is one layer. The HMAC scheme is applied to the swap methods above when `[RPCSwapAuth].Secrets` is set. `nhb_requestSwapApproval`, `nhb_getSwapQuote`, `nhb_swapMint`, `nhb_swapBurn`, `nhb_getSwapStatus` and `nhb_checkSwapAllowance` additionally require a verified TLS client certificate or a bearer JWT (`requireAuthInto` in `rpc/http.go`). `swap_voucher_get`, `swap_voucher_list` and `swap_voucher_export` require a certificate or JWT only when `[RPCSwapAuth].Secrets` is empty (`requireSwapLedgerAuth` in `rpc/swap_handlers.go`); with secrets configured, the HMAC check is their credential.

The API gateway in `cmd/gateway` does not use this scheme. It validates bearer JWTs (`gateway/middleware/auth.go`).

## Headers

Each request must carry:

| Header | Description |
| --- | --- |
| `X-Api-Key` | Identifies the credential. Must be a configured key with a non-empty secret. |
| `X-Timestamp` | Unix time in seconds, as a base-10 integer. |
| `X-Nonce` | Client-chosen string. |
| `X-Signature` | Hex-encoded HMAC-SHA256 of the payload below, keyed with the shared secret. |

## Signed payload

The payload is the five values joined with a newline (`"\n"`):

1. the `X-Timestamp` header value (whitespace-trimmed);
2. the `X-Nonce` header value (whitespace-trimmed);
3. the upper-cased HTTP method;
4. the canonical path: the URL path (`/` if empty), followed by `?` and the raw query string split on `&`, sorted as plain strings, and re-joined with `&` when a query is present (values are not decoded or re-encoded);
5. the request body bytes (empty when there is no body).

```text
payload   = join([timestamp, nonce, upper(method), canonicalPath, body], "\n")
signature = hex(HMAC_SHA256(secret, payload))
```

Source: `ComputeSignature` and `CanonicalRequestPath` in `gateway/auth/auth.go`. The signature is hex-decoded and compared with `hmac.Equal`. Header values are whitespace-trimmed before use.

## Checks, in order

`Authenticate` in `gateway/auth/auth.go` rejects a request when:

1. the body is larger than 1 MiB (`MaxBodyForSignature`);
2. `X-Api-Key` is missing, or the key is unknown or has an empty secret;
3. `X-Timestamp` is missing or not an integer, or differs from the server clock by more than the allowed skew;
4. `X-Nonce` or `X-Signature` is missing, the signature is not valid hex, or it does not match;
5. the `timestamp|nonce` pair was already used by this key inside the nonce window (`nonce already used`);
6. the timestamp is not strictly greater than the last accepted timestamp for this key while that earlier timestamp is still inside the skew window (`timestamp not increasing`). In practice a key can have at most one accepted request per second.

The signature is verified before the nonce is recorded, so unsigned or badly signed requests do not consume nonces.

### Limits

| Setting | Default | Maximum | Configuration |
| --- | --- | --- | --- |
| Timestamp skew (either direction) | 120 s | 120 s | `[RPCSwapAuth].AllowedTimestampSkewSeconds` |
| Nonce window | 10 min | 10 min | `[RPCSwapAuth].NonceTTLSeconds` |
| Nonce cache entries per API key | 4096 | 65536 | `[RPCSwapAuth].NonceCapacity` |

A value of zero or less selects the default and a larger value is clamped to the maximum (`NewAuthenticator` in `gateway/auth/auth.go`, and `swapDefault*`/`swapMax*` in `rpc/http.go`). When a configured value is clamped, `NewAuthenticator` logs a warning naming the configured and the effective value. When the cache is full the oldest entry is evicted.

For the node RPC, when secrets are configured a nonce persistence backend is mandatory: `[RPCSwapAuth.Persistence] Backend = "leveldb"` with `LevelDBPath` (relative paths are resolved under `DataDir`); an empty backend or `none` stops the node at startup (`cmd/nhb/main.go`). Persisted nonces are loaded into memory at startup. The escrow gateway constructs its authenticator without persistence, so its nonces are held in memory only. `config.Load` refuses `NetworkName = "mainnet"` when `[RPCSwapAuth].Secrets` is empty (`config/config.go`).

Optional per-key request quotas for the swap methods come from `[RPCSwapAuth].PartnerRateLimits` and `RateLimitWindowSeconds` (default window one minute); an exceeded quota returns HTTP 429.

## Failure responses

- Node JSON-RPC: HTTP 401 with the JSON-RPC error `codeUnauthorized` and the message from the failed check.
- Escrow gateway: HTTP 401 with `{"error":"<message>"}`.

## Escrow gateway routes

`Server.ServeHTTP` in `services/escrow-gateway/server.go` routes these paths:

| Route | Credential |
| --- | --- |
| `POST /escrow/create`, `/escrow/release`, `/escrow/refund`, `/escrow/dispute`, `/escrow/resolve` | API key and HMAC signature, plus the request-specific checks below. |
| `POST /p2p/offers`, `POST /p2p/accept` | API key and HMAC signature, plus a wallet co-signature (next section). |
| `GET /escrow/{id}`, `GET /p2p/offers`, `GET /p2p/trades/{id}` | API key and HMAC signature, signed over an empty body (`authenticateRead`). A failure answers HTTP 401. |

## Wallet co-signatures (escrow gateway)

The escrow gateway's `POST /p2p/offers` (signer must be the seller) and `POST /p2p/accept` (signer must be the buyer) additionally require a wallet signature in `X-Sig-Addr` (signer address) and `X-Sig` (65-byte hex signature). `verifyWalletSignature` in `services/escrow-gateway/server.go` requires the `X-Timestamp` and `X-Nonce` headers to be present and signs over them:

```text
payload = join([upper(method), canonicalPath, body, timestamp, nonce, lower(resourceID)], "|")
digest  = EIP-191 personal-message hash of keccak256(payload)
```

The address recovered from the signature must equal `X-Sig-Addr`, and, when the route names allowed signers, must be one of them. A `27`/`28` recovery byte is accepted. `POST /escrow/create` and the release, refund and dispute routes use different signatures: the wallet signs the on-chain authorization envelope itself (`verifyEscrowCreateSignature`, `verifyEscrowActionSignature`).
