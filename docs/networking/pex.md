# Peer Exchange (PEX)

Peer exchange lets a node share the peer addresses it knows about. It reuses the
authenticated TCP connection and adds two message types, `PexRequest` (`0x0D`)
and `PexAddresses` (`0x0E`). The implementation is `p2p/pex.go`; the payload
types are in `p2p/messages.go`. PEX is on unless `[p2p] PEX = false`; when it is
off, the node ignores both message types.

**What the node does today.** It answers `PexRequest` messages. It accepts a
`PexAddresses` message only as the reply to a `PexRequest` that it sent, and its
own code never sends a `PexRequest` (`NewPexRequestMessage` is only called from
tests). So in practice a `PexAddresses` frame from a peer is unsolicited: the
peer connection sees `unsolicited pex addresses`, which is a protocol violation
(score -5, connection closed; see [reputation](../p2p/reputation.md)). With
`PEX = false` both message types are ignored instead. Addresses enter the address
book from handshakes and from the seed list.

## Message payloads

Both are carried in the `Message` envelope described in
[overview.md](overview.md#wire-format).

### `PexRequest`

| Field | Type | Meaning |
| ----- | ---- | ------- |
| `limit` | int | Maximum addresses wanted. Values above 32, or zero or negative, become 32. |
| `token` | string | Echoed in the reply. An empty token is replaced by a random 128-bit hex string. A token longer than 128 bytes ends the request with an error (a protocol violation). |

### `PexAddresses`

| Field | Type | Meaning |
| ----- | ---- | ------- |
| `token` | string | Token copied from the request. |
| `addresses[].addr` | string | Dialable `host:port`, at most 255 characters. |
| `addresses[].nodeID` | string | Normalized `0x` node ID, at most 128 characters (note the JSON key is `nodeID`). |
| `addresses[].lastSeen` | time | When the sender last recorded the address. |

A `PexAddresses` payload larger than 64 KiB is refused.

## Address book

Each node keeps an in-memory book keyed by node ID (one address per ID), holding
at most 1,024 entries (`pexBookMax`). Entries come from:

- the seed list, at start-up (`newPexManager`); seeds are never evicted;
- every handshake that ended in a registered peer (`recordPeer`, using the peer's
  reported `listenAddrs`); such an entry is **verified**;
- `PexAddresses` replies (`handleAddresses`); such an entry stays **unverified**
  until this node has itself reached the peer.

Only verified entries (and seeds) are handed out to other peers, and what another
peer says never overrides a verified entry or a seed. Entries older than 60
minutes (`pexAddressTTL`) are dropped whenever the book is pruned. When the book
is full, unverified entries are evicted first, then the least recently touched
verified ones.

## Answering a request

`handleRequest` builds the response from the book:

1. A peer may ask for addresses three times back to back and then once every
   5 seconds (`pexRequestBurst`, `pexRequestRate`); a request over that is not
   answered.
2. Skip unverified entries, the requester, the local node, banned peers and
   entries older than the TTL.
3. Shuffle the remaining entries and truncate to `limit` (at most 32).
4. Send `PexAddresses` with the request's token.

## Receiving addresses

`handleAddresses` drops the whole message unless its token matches a request this
node sent to that peer that is still open (a request stays open for 5 minutes, and
at most 4 are open per peer). Of the reply, at most 32 entries are used, and an
entry is ignored when its node ID or `host:port` is empty, invalid or too long, it
is the local node or the sending peer, the node is banned, or its `lastSeen` is
older than 60 minutes (a `lastSeen` in the future is taken as now). Accepted new
entries go into the book as unverified candidates. Nothing learned this way is
written to the peerstore: at most 4 candidates are dialed at a time, each once, and
a peer reaches the peerstore only after this node has completed a handshake with it
and registered it. A candidate that cannot be reached is forgotten.
