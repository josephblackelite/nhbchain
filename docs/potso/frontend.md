# POTSO Frontend Integration

Notes for web and mobile clients that display POTSO data. All methods are JSON-RPC 2.0 calls to the node's RPC endpoint with `params` as a one-element array holding an object.

## Reading meters and leaderboards

- `potso_userMeters` with `{"user": "nhb1...", "day": "2025-09-24"}` returns that day's meter (`day`, `uptimeSeconds`, `txCount`, `escrowEvents`, `rawScore`, `score`). Omit `day` for today (UTC).
- `potso_top` with `{"day": "...", "limit": 10}` returns `[{"user": "nhb1...", "meter": {...}}]`, already sorted by the node.
- `potso_leaderboard`, `potso_getWeight` and `potso_params` expose the reward-epoch snapshot: [leaderboard.md](leaderboard.md).
- Meters only change as transactions are applied; a day's meter does not change after that UTC day ends.
- Field definitions: [README](README.md). `uptimeSeconds` is not written by any live code path, so expect it to stay `0`.

## Heartbeats

`potso_heartbeat` is disabled. The handler always answers HTTP 503, code `-32000`, message "potso heartbeat rpc is temporarily disabled; submit engagement through the canonical transaction pipeline". Clients should not build a heartbeat flow on it, and `nhb-cli potso heartbeat` (which calls it) fails.

## Staking and rewards

- POTSO stake lock, unbond and withdraw are signed transactions of type `0x2D`, `0x2E`, `0x2F` sent with `nhb_sendTransaction`; read state with `potso_stake_info` (authenticated). See [stake.md](stake.md).
- Reward history and CSV export are unauthenticated reads ([rewards-api.md](rewards-api.md)). `potso_reward_claim` is retired: it answers HTTP 410 with code `-32060`, so a client cannot claim a reward; auto-mode rewards are credited at epoch processing without a claim ([rewards-modes.md](rewards-modes.md)).
- Evidence is reported with a signed `0x4C` transaction, not an RPC method; `potso_getEvidence` and `potso_listEvidence` read it ([evidence-and-penalties.md](evidence-and-penalties.md)).
- Amounts are decimal wei strings; addresses are `nhb1...` Bech32.

## Errors

- Invalid parameters return HTTP 400 with code `-32602` and a message such as "user is required", "invalid user" or "invalid request parameters".
- Calls that need authentication and lack a valid credential return HTTP 401 with code `-32001`.
- Retired methods (`potso_reward_claim`) return HTTP 410 with code `-32060`; an unknown method returns HTTP 404 with code `-32601`.
- Server-side failures return code `-32000`.
