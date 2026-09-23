# Rate Limits

Every inbound frame a peer sends is checked against three token buckets before it
is decoded. Buckets refill continuously from the wall clock
(`p2p/ratelimit.go`, `p2p/peer.go` `readLoop`).

## The three layers

Checked in this order for each frame, one token per frame (Ping and Pong frames
count):

1. **Per-IP bucket.** One bucket per remote host (the IP part of the connection's
   remote address), with the same rate and burst as the per-peer bucket. Buckets
   idle for 15 minutes are evicted.
2. **Per-peer bucket.** Rate `RateMsgsPerSec` tokens per second, capacity
   `Burst` (raised to the rate if `Burst` is lower; every bucket's capacity is at
   least its rate). A greylisted peer's bucket is set to 25% of both rate and burst until
   the greylist ends.
3. **Global bucket.** Rate `RateMsgsPerSec * MaxPeers`, capacity
   `MaxPeers * max(Burst, RateMsgsPerSec)`.

Peers marked persistent (addresses in `Bootnodes` / `PersistentPeers`, or node IDs
already recognised as such) skip all three checks.

The table of per-IP buckets holds at most 8,192 entries
(`requestLimiterEntries`, `p2p/server.go`).

## Configuration

```
[p2p]
RateMsgsPerSec = 50    # tokens per second, per peer and per IP
Burst          = 200   # bucket capacity, per peer and per IP
MaxPeers       = 64    # multiplies into the global bucket
```

These are the values in the repo `config.toml`. If `RateMsgsPerSec` is unset the
node falls back to the top-level `MaxMsgsPerSecond`, and then to 32
(`config/config.go`, `p2p/server.go`); `Burst` defaults to 200.

## Outcomes

| Layer | What happens |
| --- | --- |
| Per-IP or per-peer | The reputation misbehavior counter increases, the score drops by 10, and the connection is closed. The peer is banned only if its score is now at or below `-BanScore`. |
| Global | The connection is closed with no score change and no ban. |

For a persistent peer these outcomes are not applied (the frame is not counted at
all, and if a rate-limit handler is reached it logs and ignores the event).

Metrics and logs: `Peer exceeded rate limit` and `Global rate cap exceeded` log
lines, and the `nhb_p2p_peer_misbehavior` gauge; see
[networking observability](../networking/observability.md).

## Other budgets

Two further sets of limits are separate from the token buckets above; both are
described in [networking security](../networking/security.md):

- **Inbound admission** (before anything is read from a new connection): how
  often one address may open connections, how many of its handshakes may be in
  flight and how many established inbound connections it may hold, and how many
  handshakes may be in flight in all.
- **Chain-data requests** (`GetBlocks`, `GetStatus`): a budget per remote address
  and one shared by all addresses. A request over budget is dropped without a
  score change or a disconnect.
