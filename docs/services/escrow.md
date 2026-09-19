# Escrow Service API

The node's JSON-RPC exposes three read-only escrow helpers (`rpc/modules/escrow.go`,
registered in `rpc/http.go`) alongside `escrow_get`:

- `escrow_getRealm` returns an arbitration realm definition.
- `escrow_getSnapshot` returns an escrow record with its frozen arbitrator policy.
- `escrow_listEvents` returns `escrow.*` events from the node's event buffer.

These three methods do not call the JWT check; they are subject to the RPC
server's per-source rate limit (`RPCMaxTxPer*` in `config.toml`).

The mutating methods `escrow_create`, `escrow_fund`, `escrow_release`,
`escrow_refund`, `escrow_dispute`, `escrow_expire` and `escrow_resolve` are
disabled: each returns HTTP `410` with JSON-RPC code `-32060` and a message
beginning `this method is disabled` (`rpc/escrow_handlers.go`,
`escrowRPCDisabledMessage`). Escrow state changes go through signed transactions.

All three read methods take a single parameter object in `params`.

## Methods

### `escrow_getRealm`

Params: `[{"id": "<realm id>"}]`. `id` is required.

Result:

```json
{
  "id": "core",
  "version": 3,
  "nextPolicyNonce": 42,
  "createdAt": 1716403200,
  "updatedAt": 1719081600,
  "arbitrators": {
    "scheme": "committee",
    "threshold": 2,
    "members": ["nhb1...", "nhb1..."]
  },
  "metadata": {
    "scope": "platform",
    "providerProfile": "...",
    "arbitrationFeeBps": 0,
    "feeRecipient": "nhb1..."
  }
}
```

`scheme` is `single`, `committee` or `unspecified`. `metadata` is present only
when the realm has metadata; `scope` is `platform`, `marketplace` or
`unspecified`; `feeRecipient` is omitted when empty. An unknown realm returns
HTTP `404` with message `realm not found`.

### `escrow_getSnapshot`

Params: `[{"id": "0x<64 hex characters>"}]` (32 bytes; a missing `0x` prefix is
accepted).

Result:

```json
{
  "id": "0x...",
  "payer": "nhb1...",
  "payee": "nhb1...",
  "mediator": "nhb1...",
  "token": "NHB",
  "amount": "1000000000000000000",
  "feeBps": 50,
  "deadline": 1720204800,
  "createdAt": 1719952000,
  "nonce": 1,
  "status": "funded",
  "meta": "0x...",
  "disputeReason": "...",
  "realm": "core",
  "frozenPolicy": {
    "realmId": "core",
    "realmVersion": 3,
    "policyNonce": 17,
    "scheme": "committee",
    "threshold": 2,
    "members": ["nhb1...", "nhb1..."],
    "frozenAt": 1719952000,
    "metadata": { "scope": "platform", "providerProfile": "...", "arbitrationFeeBps": 0 }
  },
  "resolutionHash": "0x..."
}
```

`mediator`, `disputeReason`, `realm`, `frozenPolicy` and `resolutionHash` are
omitted when unset. `status` is one of `init`, `funded`, `released`, `refunded`,
`expired`, `disputed`, `unknown`. `meta` is the 32-byte metadata hash. An unknown
escrow returns HTTP `404` with message `escrow not found`.

### `escrow_listEvents`

Params: none, or `[{"prefix": "<event type prefix>", "limit": <int>}]`. Both keys
are optional. `prefix` defaults to `escrow.` and is matched case-insensitively.
`limit` keeps the first N matching events (a negative value becomes 0).

The events come from the node's in-memory event buffer (`Node.Events()`), in
buffer order. `sequence` is the 1-based position in the returned list, not a
persistent identifier.

Result:

```json
[
  {
    "sequence": 1,
    "type": "escrow.realm.updated",
    "attributes": {
      "realmId": "core",
      "version": "3",
      "nextNonce": "42",
      "createdAt": "1716403200",
      "updatedAt": "1719081600",
      "arbScheme": "2",
      "arbThreshold": "2",
      "arbitrators": "0x..."
    }
  }
]
```

Event types are defined in `native/escrow/events.go` (for example
`escrow.created`, `escrow.funded`, `escrow.released`, `escrow.refunded`,
`escrow.expired`, `escrow.disputed`, `escrow.resolved`, `escrow.realm.created`,
`escrow.realm.updated`, `escrow.trade.*`, `escrow.milestone.*`). In realm events
`arbScheme` is numeric: `1` single, `2` committee. Escrow events carry the
frozen-policy attributes `realmVersion`, `policyNonce`, `arbScheme` and
`arbThreshold`, and `decisionSigners` when a decision is recorded.

## CLI helper

`cmd/nhb/escrowcmd` is a small Go program (its usage text calls it `nhb-escrow`;
build it with `go build -o nhb-escrow ./cmd/nhb/escrowcmd`). The global flags
must come before the command:

```bash
nhb-escrow [--rpc URL] [--auth TOKEN] <command> [options]

nhb-escrow realm get --id core
nhb-escrow snapshot --id 0x...
nhb-escrow events --prefix escrow.realm. --limit 10
```

`--rpc` defaults to `NHB_RPC_URL`, else `http://127.0.0.1:8545`; `--auth` defaults
to `NHB_RPC_TOKEN`.

The CLI also has `open` (flags `--payer`, `--payee`, `--token` (default `NHB`),
`--amount`, `--fee-bps`, `--deadline`, `--mediator`, `--meta`, `--realm`) and
`resolve` (`--id`, `--caller`, `--outcome release|refund`). They call
`escrow_create` and `escrow_resolve`, which the node currently rejects with the
`410` described above.

## Gateway indexing

`services/escrow-gateway` stores idempotency keys, audit entries and P2P
offers/trades in SQLite (`services/escrow-gateway/storage.go`) and reads escrow
state from the node with `escrow_get` and `escrow_getRealm`. See
[Escrow gateway](../escrow/nhbchain-escrow-gateway.md) for its behaviour.
