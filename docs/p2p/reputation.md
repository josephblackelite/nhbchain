# Peer Reputation

Each peer has a score that starts at 0 (`p2p/reputation.go`, `p2p/server.go`).
Bad behavior lowers it, good behavior raises it, and it decays exponentially
toward zero. Two thresholds, both compared against the negated score, drive
enforcement:

- `GreyScore` (default 50): a peer whose score is at or below `-GreyScore` is
  **greylisted**.
- `BanScore` (default 100): a peer whose score is at or below `-BanScore` is
  **banned** for `PeerBanDuration`.

If `GreyScore` is not positive or is not below `BanScore`, the server uses 50. The
repo `config.toml` sets `GreyScore = 50`, `BanScore = 100` and
`BanDurationSeconds = 3600`; with no configured duration the server uses 15
minutes.

## Score changes in use

| Event | Delta | Where |
| --- | --- | --- |
| Any message processed (including Ping and Pong) | +1 | `recordValidMessage` |
| Protocol violation (malformed frame or payload, oversize frame, handler reports an invalid payload, a `PexAddresses` frame this node did not ask for or one over 64 KiB) | -5 | `handleProtocolViolation` |
| Per-peer or per-IP rate limit hit | -10 | `handleRateLimit` |
| A write error or write timeout | -5 | `writeLoop` |
| Disconnect with the ban flag | `-BanScore` | `applyBan` |
| Handshake violation: a chain or genesis mismatch in a handshake that the node it names actually signed | direct ban for `PeerBanDuration`; never for a configured persistent peer | `markHandshakeViolation` |
| Operator ban (`net_ban` / `BanPeer`) | `-BanScore` plus a direct ban | `BanPeer` |

`invalidBlockPenaltyDelta` (-20), `uptimeRewardDelta` (+2 per day) and the helpers
`PenalizeInvalidBlock`, `MarkUptime`, `MarkHeartbeat` exist in
`p2p/reputation.go` but nothing in the node calls them, so those events do not
change a score.

A full send queue is not a score event: a message that does not fit in a peer's
queue (by count, or by bytes: the limit is the larger of 4 MiB and four times the
largest message the server accepts) is dropped for that peer, the connection is kept, and a
warning is logged at most every 10 seconds per peer (`Broadcast`, `Enqueue`,
`p2p/peer.go`). A peer that stops reading is ended by its write timeout, which is
a write error.

A handshake whose signature does not match the node ID it names, and a replayed
handshake, are refused without holding anything against that node ID: the ID in
the packet is chosen by the sender, so a ban on it would lock out whoever really
holds it (`p2p/handshake.go`).

The reputation table holds at most 4,096 peers; when it is full the least
recently updated peer without an active ban is dropped first
(`defaultReputationMaxRecords`, `p2p/reputation.go`).

Scores decay with a half-life of 10 minutes (`DecayHalfLife` default), applied
whenever the record is read or adjusted.

## Persistent peers

For a configured persistent peer, every adjustment clamps a positive score to 0
and clears any score-based ban, so persistent peers are never banned for their
score and never accumulate a positive score. Handshake evidence does not ban a
configured persistent peer either (so it can reconnect as soon as its fault is
fixed); an operator ban (`net_ban`) still does. They are exempt from rate limits (see
[rate limits](ratelimits.md)) and from connection-manager pruning.

## Greylist

When an adjustment leaves the score at or below `-GreyScore`, the peer is
greylisted for one minute (the server sets `GreylistDuration` to 1 minute), and
every further adjustment that leaves it at or below the threshold restarts that
minute. A greylisted peer stays connected; its per-peer token bucket is reduced
to 25% of its rate and burst. The reduction is applied and removed when the
score is next adjusted.

## Ban

When an adjustment leaves the score at or below `-BanScore`, the peer is
disconnected and banned for `PeerBanDuration`. Handshakes from it are rejected
until the ban expires (`initPeer`, `registerPeer`). Handshake-violation bans and
operator bans are also written to the peerstore; a ban caused only by score is
held in memory.

## Latency and usefulness counters

The reputation record also tracks a ping-latency moving average (weight 0.2), a
count of useful messages and a count of misbehavior incidents. They do not change
the score but are exported as metrics and used to choose which peer the
connection manager prunes first (see
[networking overview](../networking/overview.md#connection-manager)).

## Telemetry

`p2p_info` shows the configured `banScore` and `greyScore`; `net_peers` and
`p2p_peers` show each peer's `score`, `state` and `bannedUntil`. See
[network RPC](../networking/net-rpc.md). The `greylisted` flag is available in the
server's `PeerInfo` structure but no JSON-RPC method returns it.
