# Governance JSON-RPC API

The governance RPC surface exposes proposal lifecycle operations for devnet environments. Each request is a JSON-RPC 2.0 call posted to the node's RPC endpoint.

## gov_propose
Submit a new proposal with an optional deposit.

**Request**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "gov_propose",
  "params": [
    {
      "kind": "param.update",
      "payload": "{\"fees.baseFee\":\"1000\"}",
      "from": "nhb1exampleaddress000000000000000000000000",
      "deposit": "1000000000000000000000"
    }
  ]
}
```

**Response**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "proposalId": 7
  }
}
```

## gov_vote
Record or update a vote for the proposal.

**Request**
```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "gov_vote",
  "params": [
    {
      "id": 7,
      "from": "nhb1exampleaddress000000000000000000000000",
      "choice": "yes"
    }
  ]
}
```

**Response**
```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "ok": true
  }
}
```

## gov_proposal
Fetch proposal details by identifier.

**Request**
```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "gov_proposal",
  "params": [
    { "id": 7 }
  ]
}
```

**Response**
```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "id": 7,
    "target": "param.update",
    "status": 2,
    "deposit": "1000000000000000000000",
    "voting_start": "2024-01-01T00:00:00Z",
    "voting_end": "2024-01-08T00:00:00Z",
    "timelock_end": "2024-01-10T00:00:00Z",
    "queued": false,
    "proposed_change": "{\"fees.baseFee\":\"1000\"}"
  }
}
```

## gov_list
List proposals in descending order with optional cursor and limit.

**Request**
```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "gov_list",
  "params": [
    {
      "cursor": 7,
      "limit": 2
    }
  ]
}
```

**Response**
```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "proposals": [
      { "id": 7, "status": 2, "queued": false },
      { "id": 6, "status": 3, "queued": true }
    ],
    "nextCursor": 5
  }
}
```

## gov_finalize
Close voting, tally results, and update status.

**Request**
```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "method": "gov_finalize",
  "params": [
    { "id": 7 }
  ]
}
```

**Response**
```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "result": {
    "proposal": {
      "id": 7,
      "status": 3,
      "queued": false
    },
    "tally": {
      "turnout_bps": 4200,
      "yes_power_bps": 3000,
      "no_power_bps": 1000,
      "abstain_power_bps": 200
    }
  }
}
```

## gov_queue
Mark a passed proposal as queued for execution.

**Request**
```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "method": "gov_queue",
  "params": [
    { "id": 7 }
  ]
}
```

**Response**
```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "result": {
    "ok": true,
    "proposal": {
      "id": 7,
      "queued": true
    }
  }
}
```

## gov_execute
Execute a queued proposal once the timelock has elapsed.

**Request**
```json
{
  "jsonrpc": "2.0",
  "id": 7,
  "method": "gov_execute",
  "params": [
    { "id": 7 }
  ]
}
```

**Response**
```json
{
  "jsonrpc": "2.0",
  "id": 7,
  "result": {
    "ok": true,
    "proposal": {
      "id": 7,
      "status": 6,
      "queued": true
    }
  }
}
```
