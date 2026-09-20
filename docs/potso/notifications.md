# POTSO Reward Events

The state processor appends these events while applying a block (`core/events/potso.go`, appended with `AppendEvent`). All attribute values are strings.

| Event | When | Attributes |
| --- | --- | --- |
| `potso.reward.ready` | Claim mode, at epoch processing, for each winner with a positive amount. | `epoch`, `address`, `amount`, `mode` (`claim`) |
| `potso.reward.paid` | Auto mode at epoch processing, for each winner with a positive amount when the epoch's total paid is positive. The event type also has a `claim` mode variant, emitted only by `Node.PotsoRewardClaim`, which no RPC or CLI reaches any more (see below). | `epoch`, `address`, `amount`, `mode` (`auto`, or `claim` from `Node.PotsoRewardClaim`) |
| `potso.reward.epoch` | Once per processed epoch. | `epoch`, `totalPaid`, `winners`, plus `emission`, `budget`, `remainder` |

`address` is a Bech32 `nhb1...` address; `amount`, `totalPaid`, `emission`, `budget` and `remainder` are decimal wei.

Notes:

- The `potso.reward.paid` event with `mode = claim` is appended by `Node.PotsoRewardClaim`, which mutates the node's live state directly rather than through a block transaction. Its only RPC, `potso_reward_claim`, answers HTTP 410, so no running node emits it (see [rewards-modes.md](rewards-modes.md)).
- Events are kept in the node's event log (`core/event_log.go`), which holds the last 20,000 events of ordinary kinds in memory (fee, penalty and escrow events are kept separately, up to 100,000). It is not chain state and older reward events drop out of it. The durable record of settled payouts is the state read by `potso_rewards_history` and `potso_export_epoch`.
- The event names are `potso.reward.*`. The node does not deliver webhooks or any other push notification for them; consumers read events from the node, or poll `potso_rewards_history` and `potso_export_epoch` ([rewards-api.md](rewards-api.md)).
- Other POTSO events: `potso.stake.locked` / `unbonded` / `withdrawn` ([stake.md](stake.md)), `potso.evidence.accepted` and `potso.penalty.applied` ([evidence-and-penalties.md](evidence-and-penalties.md)), `potso.heartbeat` (emitted only by `Node.PotsoHeartbeat`, which nothing calls, see [README](README.md)), and `potso.alert.invariant_violation` (type defined in `core/events/potso_alert.go`; nothing in the node emits it).
