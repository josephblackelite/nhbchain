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
| Valid message processed | `+1` | Applied after each well-formed message. |
| Malformed payload / protocol violation | `-20` | Also contributes to invalid-rate tracking. |
| Per-peer or per-IP rate limit hit | `-15` | Applied once when the limiter trips. |
| Slow outbound writer / full queue | `-5` | When a peer cannot drain its outbound buffer. |
| Manual ban (disconnect with ban flag) | `-BanScore` | Guarantees the ban threshold is crossed. |

Scores decay exponentially back toward zero with a 10 minute half-life. This
allows previously noisy peers to recover if they behave.

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
status, and ban configuration. Operators should monitor these endpoints to
identify chronically misbehaving peers or to confirm that positive traffic is
maintaining a healthy score.
