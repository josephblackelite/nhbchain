# POTSO Telemetry Overview

The POTSO (Proof of Time Spent Online) telemetry service records validator and participant availability without attaching any rewards. It focuses on deterministic daily meters that power dashboards and future incentive programs.

This document explains the data model, storage layout, and RPC interface so any engineering or analytics team can integrate with the meters quickly.

## Daily meters

Every participant accumulates the following UTC-scoped counters:

| Field | Description |
| --- | --- |
| `uptimeSeconds` | Total credited uptime derived from signed heartbeats. Each accepted heartbeat adds the delta between the previous timestamp and the new timestamp (minimum 60 seconds). |
| `txCount` | Number of validated transactions initiated by the participant during the day. The counter is updated by the state transition processor whenever a transaction succeeds. |
| `escrowEvents` | Count of escrow interactions (fund, release, refund, dispute, resolve). |
| `rawScore` | Weighted sum of the raw counters: `floor(uptimeSeconds/60) + txCount*5 + escrowEvents*10`. |
| `score` | Currently equal to `rawScore`, reserved for future smoothing or caps. |

Meters are stored by day (`YYYY-MM-DD` in UTC) and by address. Each day also tracks a participant index so the leaderboard can be assembled without scanning the entire state.

## Heartbeat flow

1. A participant signs the tuple `(user, lastBlock, lastBlockHash, timestamp)` using their NHB private key. The timestamp must be within ±120 seconds of the node clock, and heartbeats must be spaced at least 60 seconds apart.
2. The CLI fetches the latest block, computes its hash, signs the payload, and calls `potso_heartbeat`.
3. The node verifies the signature, checks the referenced block hash, ensures the interval is satisfied, and stores the updated heartbeat record and meter.
4. An event `potso.heartbeat` is emitted for downstream consumers.

The first accepted heartbeat for a day contributes 60 seconds of uptime, establishing a baseline for subsequent deltas.

## Storage keys

Key prefixes introduced in the trie:

- `potso/heartbeat/<addr>` – stores the last accepted timestamp, block height, and block hash.
- `potso/meter/<day>:<addr>` – RLP-encoded `Meter` struct for the day.
- `potso/day-index/<day>` – list of addresses with activity on the day (deduplicated) used by the leaderboard.

These records are deterministic and do not alter consensus semantics beyond adding additional state commitments.

## RPC surface

- `potso_heartbeat` – accepts the signed payload and returns the updated meter and credited uptime delta.
- `potso_userMeters` – returns the meter for a given user and optional day (defaults to current UTC day).
- `potso_top` – returns the highest scoring participants for a day, sorted by score, raw score, uptime, and address.

Refer to the companion documents for payload shapes and integration guidance.
