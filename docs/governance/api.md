# Governance API Surface

> Include function-level documentation for developer integrations and technical specs; docs must be generated into /docs/governance/* for auditors, investors, regulators, and consumers.

| Endpoint | Method | Description |
| --- | --- | --- |
| `gov_propose` | `POST` | Submit a parameter change proposal and lock the ZNHB deposit. |

### `gov_propose`

Submits a `param.update` proposal. The payload must only contain allow-listed parameter keys. The ZNHB deposit is debited from the proposer account and locked in escrow until the proposal is resolved.

**Request**

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "gov_propose",
  "params": {
    "proposer": "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqp8d7z2",
    "kind": "param.update",
    "payload": {
      "fees.baseFee": "420000000000"
    },
    "deposit": "300000000000000000000"
  }
}
```

**Response**

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "proposalId": 42,
    "votingStart": 1700000000,
    "votingEnd": 1700003600,
    "timelockEnd": 1700004400,
    "status": "voting_period"
  }
}
```

On success, a `gov.proposed` event is emitted for downstream consumers.
