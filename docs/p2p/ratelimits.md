# Rate Limits

Rate limiting protects each node from floods while allowing legitimate bursts.
Three layers are enforced:

1. **Per-peer token bucket** — configured by `RateMsgsPerSec` and `Burst`.
   Every inbound message consumes one token. Tokens regenerate at
   `RateMsgsPerSec` (default `50 msg/s`) up to the burst capacity. Greylisted
   peers automatically receive a 75% reduction in both rate and burst.
2. **Per-IP bucket** — shares the same rate and burst settings and prevents many
   connections from the same IP from overwhelming the node. Exceeding this limit
   penalises the offending peer's reputation and terminates the connection.
3. **Global bucket** — scaled by `RateMsgsPerSec × MaxPeers` to provide a soft
   cap on aggregate throughput. When depleted, new messages are dropped and the
   peer is disconnected without a reputation penalty.

All buckets are implemented as thread-safe token buckets with floating point
counters. Buckets refill based on wall-clock time; clock drift is therefore
irrelevant.

## Configuration summary

```
[p2p]
RateMsgsPerSec = 50   # steady-state tokens per second per peer
Burst          = 200  # per-peer & per-IP burst size
MaxPeers       = 64   # global bucket = RateMsgsPerSec * MaxPeers
```

Messages exceeding the limits trigger the following outcomes:

| Layer | Behaviour |
| --- | --- |
| Per-peer | Connection terminated, reputation `-10`. |
| Per-IP | Connection terminated, reputation `-10`. |
| Global | Connection terminated without penalty. |

Operators should tune the burst value to accommodate expected gossip fan-out
without allowing a single peer to monopolise bandwidth. Greylisted peers are
throttled to 25% of the configured rate and burst until their reputation
recovers above `-GreyScore`.
