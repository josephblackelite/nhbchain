# Peer Exchange (PEX)

The NET-2D release introduces address gossip so that nodes can discover peers
beyond the configured seeds. Peer Exchange (PEX) reuses the authenticated TCP
transport and adds two lightweight message types: `PEX_REQUEST` and
`PEX_ADDRESSES`.

## Message Schemas

### `PEX_REQUEST`

| Field | Type | Description |
| ----- | ---- | ----------- |
| `limit` | `int` | Upper bound on the number of addresses requested. Values are capped at `32`. |
| `token` | `string` | Echo-suppression token supplied by the requester and reflected in the response. Empty values are replaced with a random 128-bit hex string. |

### `PEX_ADDRESSES`

| Field | Type | Description |
| ----- | ---- | ----------- |
| `token` | `string` | Echo token copied from the request. Peers drop frames that reuse a previously seen token. |
| `addresses[].addr` | `string` | Dialable `host:port` endpoint observed for the peer. |
| `addresses[].nodeID` | `string` | 0x-prefixed, normalized NodeID associated with the address. |
| `addresses[].lastSeen` | `time.Time` | Wall-clock timestamp (UTC) when the sender last confirmed the address. |

## Selection, Deduplication & TTL

When answering a `PEX_REQUEST` the responder walks its address book and applies
the following filters in order:

1. **Identity Deduplication** – the address book is keyed by `nodeID`, so a
   peer can appear at most once in a response even if it was observed at multiple
   endpoints. New observations replace the stored address.
2. **Sanity Filtering** – the responder never returns its own identity, the
   requester, or currently banned peers. Invalid `host:port` values are skipped
   during ingestion.
3. **TTL Window** – entries older than 60 minutes are expired. The check is a
   simple `now - lastSeen > 60m` comparison performed whenever the address book
   is pruned. Fresh observations reset the timestamp.
4. **Limit Enforcement** – after filtering, the responder shuffles the working
   set and truncates it to the caller's requested `limit` (defaulting to `32`).

All addresses learned through PEX are persisted to the peerstore with
`lastSeen` timestamps so the dialer can queue them even if the node restarts.

## Echo Suppression

Echo suppression prevents infinite gossip loops when peers request addresses
from each other in quick succession. Each request carries a caller-supplied
`token` that is mirrored back in the `PEX_ADDRESSES` response. The responder
records the token and ignores any subsequent `PEX_ADDRESSES` frames from that
peer that reuse the same token.

The following diagrams illustrate a typical interaction:

```
A ---- PEX_REQUEST(token=t1) ----> B
B ---- PEX_ADDRESSES(token=t1, {C}) --> A

# Later, B receives an address gossip from C with token=t2 and forwards it.
B ---- PEX_REQUEST(token=t2) ----> C
C ---- PEX_ADDRESSES(token=t2, {D}) --> B
B ---- PEX_ADDRESSES(token=t2, {D}) --> A  (allowed, new token)

# Echo suppression example when a response loops back.
A ---- PEX_REQUEST(token=t3) ----> B
B ---- PEX_ADDRESSES(token=t3, {C}) --> A
A ---- PEX_ADDRESSES(token=t3, {C}) --> B  (dropped: token already seen)
```

Tokens expire alongside address entries (60 minutes). Old tokens are pruned so
genuine updates are accepted after the window elapses.
