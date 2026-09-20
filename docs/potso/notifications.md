# POTSO Reward Events

The state processor appends these events while applying a block (`core/events/potso.go`, appended with `AppendEvent`). All attribute values are strings.

| Event | When | Attributes |
| --- | --- | --- |
| `potso.reward.ready` | Claim mode, at epoch processing, for each winner with a positive amount. | `epoch`, `address`, `amount`, `mode` (`claim`) |
| `potso.reward.paid` | Auto mode at epoch processing, for each winner with a positive amount when the epoch's total paid is positive; claim mode after a successful `potso_reward_claim`. | `epoch`, `address`, `amount`, `mode` (`auto` or `claim`) |
| `potso.reward.epoch` | Once per processed epoch. | `epoch`, `totalPaid`, `winners`, plus `emission`, `budget`, `remainder` |

`address` is a Bech32 `nhb1...` address; `amount`, `totalPaid`, `emission`, `budget` and `remainder` are decimal wei.

Notes:

- The `potso.reward.paid` event for a claim is appended by `Node.PotsoRewardClaim`, which mutates the node's live state directly rather than through a block transaction (see [rewards-modes.md](rewards-modes.md)).
- The event names are `potso.reward.*`. The node does not deliver webhooks or any other push notification for them; consumers read events from the node, or poll `potso_rewards_history` and `potso_export_epoch` ([rewards-api.md](rewards-api.md)).
- Other POTSO events: `potso.stake.locked` / `unbonded` / `withdrawn` ([stake.md](stake.md)), `potso.evidence.accepted` and `potso.penalty.applied` ([evidence-and-penalties.md](evidence-and-penalties.md)), `potso.heartbeat` (emitted only by `Node.PotsoHeartbeat`, which nothing calls, see [README](README.md)), and `potso.alert.invariant_violation` (type defined in `core/events/potso_alert.go`; nothing in the node emits it).
