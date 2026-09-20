# Peer Exchange (PEX)

Peer exchange lets a node share the peer addresses it knows about. It reuses the
authenticated TCP connection and adds two message types, `PexRequest` (`0x0D`)
and `PexAddresses` (`0x0E`). The implementation is `p2p/pex.go`; the payload
types are in `p2p/messages.go`. PEX is on unless `[p2p] PEX = false`; when it is
off, the node ignores both message types.

**What the node does today.** It answers `PexRequest` messages and it ingests
`PexAddresses` messages it receives. Its own code never sends a `PexRequest`
(`NewPexRequestMessage` is only called from tests), so a node only learns
addresses through PEX if a peer sends it unsolicited `PexAddresses` frames.
Addresses also enter the address book from handshakes and from the seed list.

## Message payloads

Both are carried in the `Message` envelope described in
[overview.md](overview.md#wire-format).

### `PexRequest`

| Field | Type | Meaning |
| ----- | ---- | ------- |
| `limit` | int | Maximum addresses wanted. Values above 32, or zero or negative, become 32. |
| `token` | string | Echo-suppression token. An empty token is replaced by a random 128-bit hex string. |

### `PexAddresses`

| Field | Type | Meaning |
| ----- | ---- | ------- |
| `token` | string | Token copied from the request. |
| `addresses[].addr` | string | Dialable `host:port`. |
| `addresses[].nodeID` | string | Normalized `0x` node ID (note the JSON key is `nodeID`). |
| `addresses[].lastSeen` | time | When the sender last recorded the address. |

## Address book

Each node keeps an in-memory book keyed by node ID (one address per ID; a newer
observation replaces the address). Entries come from:

- the seed list, at start-up (`newPexManager`);
- every successful handshake (`recordPeer`, using the peer's reported
  `listenAddrs`);
- `PexAddresses` messages (`handleAddresses`).

Entries older than 60 minutes (`pexAddressTTL`) are dropped whenever the book is
pruned. Addresses learned from `PexAddresses` are also written to the peerstore
with their `lastSeen`.

## Answering a request

`handleRequest` builds the response from the book:

1. Skip the requester, the local node, banned peers and entries older than the
   TTL.
2. Shuffle the remaining entries and truncate to `limit` (at most 32).
3. Record the token as seen and remember it as the token last sent to that peer.
4. Send `PexAddresses` with that token.

## Receiving addresses

`handleAddresses` drops the whole message if its token equals the token this node
last sent to that peer (a reflection), or if the token was already seen within the
last 60 minutes. Otherwise it records the token and, for each address, ignores it
when: the node ID or `host:port` is empty or invalid, it is the local node or the
sending peer, the node is banned, or its `lastSeen` is older than 60 minutes.
Accepted entries update the book and the peerstore.

Tokens are pruned after the same 60 minutes.
