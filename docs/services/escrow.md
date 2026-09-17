# Escrow Service API

The Escrow service exposes read-only helpers for governance tooling and arbitration dashboards. It complements transactional `escrow_*` endpoints by exposing realm metadata, deterministic escrow snapshots (including frozen arbitrator policies), and the raw event feed consumed by downstream indexers.

## Methods

### `escrow_getRealm`

Returns the latest arbitration realm definition.

**Parameters**

- `id` – realm identifier (`string`).

**Result**

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
    "members": ["nhb1…", "nhb1…"]
  }
}
```

### `escrow_getSnapshot`

Fetches the canonical escrow record and any frozen arbitrator policy captured at creation time.

**Parameters**

- `id` – escrow identifier (`0x`-prefixed hex string).

**Result**

```json
{
  "id": "0x…",
  "payer": "nhb1…",
  "payee": "nhb1…",
  "token": "NHB",
  "amount": "1000000000000000000",
  "feeBps": 50,
  "deadline": 1720204800,
  "createdAt": 1719952000,
  "status": "funded",
  "meta": "0x…",
  "realm": "core",
  "frozenPolicy": {
    "realmId": "core",
    "realmVersion": 3,
    "policyNonce": 17,
    "scheme": "committee",
    "threshold": 2,
    "members": ["nhb1…", "nhb1…"],
    "frozenAt": 1719952000
  }
}
```

If a dispute has been resolved the `resolutionHash` field contains the recorded decision payload hash.

### `escrow_listEvents`

Streams recent `escrow.*` events emitted by the node. Front-ends can use these payloads to display signer fingerprints (`decisionSigners`), frozen policy metadata, and realm lifecycle information without custom parsing.

**Parameters (optional)**

- `prefix` – filter by event type prefix (defaults to `escrow.`).
- `limit` – maximum number of events to return.

**Result**

```json
[
  {
    "sequence": 1,
    "type": "escrow.realm.updated",
    "attributes": {
      "realmId": "core",
      "version": "3",
      "arbScheme": "1",
      "arbThreshold": "2",
      "arbitrators": "0x…"
    }
  }
]
```

## CLI Helper

A lightweight CLI (`cmd/nhb/escrowcmd`) is available for interacting with the new endpoints:

```bash
# Fetch realm metadata
nhb-escrow realm get --id core

# Inspect an escrow snapshot (including frozen arbitrator policy)
nhb-escrow snapshot --id 0x…

# Tail recent escrow events for dashboards or indexers
nhb-escrow events --limit 10

# Open a new escrow and submit arbitration outcomes via RPC
nhb-escrow open --payer nhb1... --payee nhb1... --token NHB --amount 1000000000000000000 --fee-bps 50 --deadline 1720204800 --realm core
nhb-escrow resolve --id 0x... --caller nhb1... --outcome release
```

The CLI honours `--auth`/`NHB_RPC_TOKEN` and `--rpc`/`NHB_RPC_URL` for secure deployments.

## Event Stream Integration

The existing gateway indexer now receives the enriched `escrow.*` events returned by `escrow_listEvents`. Because every attribute is persisted in SQLite and propagated to webhooks, front-ends can display signer fingerprints (`decisionSigners`), frozen arbitrator policies (`realmVersion`, `policyNonce`, `arbThreshold`, `arbitrators`), and other metadata without additional state reads.
