# Peer Reputation

Every connection accumulates a reputation score that influences throttling and
ban decisions. The score starts at `0` and moves negative for bad behaviour and
positive for healthy traffic. Two configurable thresholds control enforcement:

* `GreyScore` (default: `50`) — peers at or below `-GreyScore` are **greylisted**.
* `BanScore` (default: `100`) — peers at or below `-BanScore` are **banned** for
  `PeerBanDuration`.

Persistent peers configured in `config.toml` are never banned, but they still
receive greylist throttling and reputation telemetry.

## Scoring events

| Event | Delta | Notes |
| --- | --- | --- |
| Heartbeat window satisfied | `+1` | Applied after each well-formed protocol heartbeat. |
| Uptime credit | `+2 per day` | Applied via scheduled maintenance jobs or CLI tooling. |
| Malformed payload / protocol violation | `-5` | Also contributes to invalid-rate tracking. |
| Per-peer or per-IP rate limit hit | `-10` | Applied once when the limiter trips. |
| Invalid/forked block | `-20` | Heavy penalty; typically leads to a ban. |
| Manual ban (disconnect with ban flag) | `-BanScore` | Guarantees the ban threshold is crossed. |

Scores decay exponentially back toward zero with the configured half-life (10
minutes by default). This allows previously noisy peers to recover if they
behave.

## Greylist behaviour

Greylisted peers remain connected but their per-peer token bucket is throttled
by 75% (`greylistRateMultiplier = 0.25`). The greylist period is two minutes,
refreshed on every additional infraction while the score remains below the
threshold. Administrators will see log lines noting the reduced throughput.

## Ban behaviour

When the ban threshold is crossed the peer is disconnected and recorded in the
ban list for `PeerBanDuration`. Subsequent handshake attempts before expiry are
rejected. Ban expirations automatically clear reputation history so the peer can
rejoin cleanly.

## Telemetry

The RPC endpoints `p2p_info` and `p2p_peers` expose current scores, greylist
status, ban configuration, and the recorded `firstSeen`/`lastSeen` timestamps.
Operators should monitor these endpoints to identify chronically misbehaving
peers or to confirm that positive traffic is maintaining a healthy score.
