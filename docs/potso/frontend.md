# POTSO Frontend Integration Guide

This guide targets web and mobile engineers building dashboards or wallets that surface POTSO telemetry. It walks through authentication, signing, RPC calls, and rendering recommendations.

## Prerequisites

- Access to an NHB private key for the participant whose uptime should be reported.
- Network connectivity to a node exposing the JSON-RPC interface with the POTSO methods wired.
- Ability to fetch the latest block header in order to include the canonical `lastBlock` and `lastBlockHash` in the heartbeat payload.

## Heartbeat submission flow

1. **Fetch latest block**
   - Call `nhb_getLatestBlocks` with `params: [1]` to retrieve the tip.
   - Extract `header.height` and compute the header hash using the same JSON serialization as the node (SHA-256 over the JSON-encoded header).

2. **Construct payload**
   - Collect the fields `user` (bech32 address), `lastBlock`, `lastBlockHash` (hex string), and `timestamp` (UTC UNIX seconds).
   - The timestamp must be within ±120 seconds of the node's clock. Consider calling `nhb_getLatestBlocks` immediately before signing to minimise drift.

3. **Sign payload**
   - Canonical message: `potso_heartbeat|<lowercase user>|<lastBlock>|<lowercase hex hash>|<timestamp>`.
   - Hash the message with SHA-256 and sign using the participant's secp256k1 key (same flow as signing NHB transactions). Encode the 65-byte signature as a hex string.

4. **Submit RPC**
   - Call `potso_heartbeat` with body:

```json
{
  "method": "potso_heartbeat",
  "params": [
    {
      "user": "nhb1...",
      "lastBlock": 1024,
      "lastBlockHash": "0x...",
      "timestamp": 1732473600,
      "signature": "0x..."
    }
  ]
}
```

   - Successful responses include the credited `uptimeDelta` and the updated `meter` struct. When the heartbeat is throttled (interval not satisfied) `accepted` will be `false` and the delta will be `0`.

## Displaying meters

- Use `potso_userMeters` to fetch the current day or a specific day by passing `{"user":"nhb1...", "day":"2025-09-24"}`.
- Render uptime in hours or minutes, and surface transaction + escrow counts alongside the derived score so users understand the breakdown.
- When building leaderboards, call `potso_top` with a limit (default 10). Sort order is already normalised server-side.
- Cache responses by day to avoid redundant calls; meters only change within the current UTC day.

## Error handling

- HTTP 400 responses with `codeInvalidParams` indicate malformed payloads (bad signature, hash mismatch, stale timestamp). Expose actionable messages to the user and prompt for a retry.
- When the node clock diverges from the client, resynchronise by refetching the block tip and recomputing the timestamp.
- Duplicate submissions within 60 seconds return success with `accepted=false`. Handle idempotence on the client to prevent confusing error toasts.

## Security recommendations

- Keep signing keys in secure enclaves. The CLI example (`nhb-cli potso heartbeat`) demonstrates fetching the block tip, signing, and submitting in one command for auditing and automation.
- Validate the `meter` returned by the server against expected monotonic increases. Any decreases indicate a configuration or clock issue.
- Log the emitted `potso.heartbeat` events (subscribe via node logs) to cross-check that heartbeats arrive in the expected cadence.
