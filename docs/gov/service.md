# Governance Service (`governd`)

`governd` (`services/governd`) is a gRPC service that registers `gov.v1.Query`
and `gov.v1.Msg` (`proto/gov/v1`). The `Query` service reads governance state
through the consensus service's `QueryState` call. The `Msg` service wraps
messages in signed consensus envelopes.

**Write path status.** The `Msg` RPCs cannot change chain state today. They
build a `SignedTxEnvelope` whose payload is a `gov.v1` message
(`MsgSubmitProposal`, `MsgVote`, `MsgDeposit`, `MsgSetPauses`), and the
consensus envelope decoder (`transactionFromModulePayload` in
`consensus/codec/codec.go`) has no case for them: it only maps swap
payout-receipt and POS messages to transactions and rejects any other module
payload (`envelope: unsupported module payload type`, or `envelope: decode
module payload` when the type is not linked into the node, which is the case
for `proto/gov/v1`). `MsgSubmitProposal` also has no field for a proposal kind
or payload (`sdk/gov/tx.go`). Governance writes go through the `TxTypeGov*` transactions
described in [governance overview](../governance/overview.md), for example with
`nhb-cli gov ...`. The `Query` RPCs work.

## Running

```bash
go run ./services/governd --config services/governd/config.yaml
```

`--config` defaults to `services/governd/config.yaml`. The process listens on
`listen`, dials the consensus endpoint, and logs `governd listening on <addr>`.
`NHB_ENV`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_HEADERS` and
`OTEL_EXPORTER_OTLP_INSECURE` configure logging and telemetry
(`services/governd/main.go`).

## Configuration

YAML, loaded by `services/governd/config/config.go`. The shipped
`services/governd/config.yaml` sets the values marked (shipped).

| Key | Default in code | Notes |
| --- | --- | --- |
| `listen` | `:50061` | gRPC listen address. |
| `consensus` | `localhost:9090` | Consensus service endpoint. |
| `chain_id` | `localnet` | Placed in the envelope. |
| `signer_key` | none | 32-byte secp256k1 private key, hex. If set it wins over the two options below. |
| `signer_key_env` | none (shipped: `GOVERND_SIGNER_KEY`) | Name of an environment variable holding the hex key. Used when `signer_key` is empty. |
| `signer_key_file` | none | Path to a file holding the hex key. Used when neither of the above is set. One of the three is required. |
| `nonce_start` | `1` | Baseline nonce when nothing is persisted. |
| `nonce_store_path` | `/var/lib/nhb/governd-nonce` (shipped: `services/governd/data/nonce`) | Required, non-empty. Written after each successful envelope submission. |
| `fee.amount`, `fee.denom`, `fee.payer` | empty | Copied into each envelope. |
| `tls.cert` / `tls.cert_env` | none | Server certificate path, or the name of an environment variable whose value is that path. One is required. |
| `tls.key` / `tls.key_env` | none (shipped: `tls.key_env: GOVERND_TLS_KEY_PATH`) | Server key path, or the name of an environment variable whose value is that path. One is required. |
| `tls.client_ca` | empty | PEM bundle of client CAs. When set, clients must present a certificate signed by it (`RequireAndVerifyClientCert`). |
| `auth.api_tokens` | empty | Accepted bearer tokens for `Msg` RPCs. |
| `auth.mtls.allowed_common_names` | empty | Client certificate common names accepted for `Msg` RPCs. |
| `consensus_client.allow_insecure` | `false` (shipped: `true`) | Permit a plaintext consensus connection with no TLS and no shared secret. |
| `consensus_client.tls.cert`, `.key`, `.ca` (also `cert_env`, `key_env`) | none | Client mTLS material for the consensus connection. Cert and key must be given together. |
| `consensus_client.shared_secret.header`, `.token` | none (shipped header: `authorization`) | Static token sent as per-RPC credentials to the consensus service. |

Startup fails with `requires tls material or shared-secret authentication
unless allow_insecure=true` when the consensus client has none of TLS, a
shared secret, or `allow_insecure`.

### Nonce handling

The service keeps the next nonce in memory. Each `Msg` call reserves a nonce
before submitting the envelope; the counter is saved to `nonce_store_path`
after a successful submission. If the submission fails the reserved nonce is
not returned. On start, the nonce is the larger of `nonce_start` and the
persisted value (`RestoreNonce`).

## Authentication

Only `Msg` methods (`/nhbchain.gov.v1.Msg/*`) are authenticated
(`services/governd/server/auth.go`); `Query` methods are open to anyone who can
reach the port. A `Msg` call is accepted when it carries either

- `authorization: Bearer <token>` or `x-api-token: <token>` metadata matching
  an entry in `auth.api_tokens`, or
- a verified client certificate whose common name is in
  `auth.mtls.allowed_common_names`.

With neither list configured every `Msg` call is refused with
`PermissionDenied` (`authentication is not configured`); with credentials
configured but not matched, `Unauthenticated`.

## Query API

`gov.v1.Query` (`services/governd/server/server.go`):

| RPC | Behavior |
| --- | --- |
| `GetProposal` | `QueryState("gov", "proposals/<id>")`. `id` must be non-zero (`InvalidArgument`); a missing proposal returns `NotFound`. |
| `ListProposals` | Newest first. `page_size` defaults to `20` and is capped at `100`. `page_token` is the id to continue from; when absent the latest id comes from `QueryState("gov", "proposals/latest")`. `status_filter` filters by status. `next_page_token` is set when older ids remain. Ids that do not exist are skipped. |
| `GetTally` | `QueryState("gov", "tallies/<id>")`. The tally and status are computed live from the stored votes (see [state indexes](../governance/state-indexes.md)); a proposal with no tally returns `NotFound`. |

Query failures against the consensus service return `Internal`, and an
unavailable consensus client returns `Unavailable`.

## Message API

`gov.v1.Msg` RPCs: `SubmitProposal`, `Vote`, `Deposit`, `SetPauses`. Each
validates fields with `sdk/gov` (violations return `InvalidArgument`), signs
the envelope with the configured key, submits it, and returns the SHA-256 of
the marshaled signed envelope as `tx_hash`. See the write-path note above:
the chain rejects these envelopes.

## Generated clients

Generated Go stubs are in `proto/gov/v1`, TypeScript stubs in
`clients/ts/gov/v1`, and message constructors in `sdk/gov`.
