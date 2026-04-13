# Governance API Surface

> Include function-level documentation for developer integrations and technical specs; docs must be generated into /docs/governance/* for auditors, investors, regulators, and consumers.

| Endpoint | Method | Description |
| --- | --- | --- |
| `gov_propose` | `POST` | Submit a governance proposal (parameter, slashing policy, role allow-list, or treasury directive) and lock the ZNHB deposit. |

### `gov_propose`

Submits a proposal for any supported `kind`. The payload schema depends on the target:

| Kind | Description | Payload Schema |
| --- | --- | --- |
| `param.update` | Standard parameter updates. | JSON object mapping allow-listed parameter keys to new values. |
| `param.emergency_override` | Parameter update that is flagged as an emergency override. | Same schema as `param.update`; execution emits a dedicated audit log entry. |
| `policy.slashing` | Updates the slashing policy envelope. | `{"enabled": bool, "maxPenaltyBps": int, "windowSeconds": int, "maxSlashWei": string, "evidenceTtlSeconds": int, "notes"?: string}` |
| `role.allowlist` | Grants or revokes governance-managed roles. | `{"grant"?: [{"role": string, "address": bech32}], "revoke"?: [...], "memo"?: string}` |
| `treasury.directive` | Executes a treasury-funded transfer to one or more recipients. | `{"source": bech32, "transfers": [{"to": bech32, "amountWei": string, "memo"?: string, "kind"?: string}], "memo"?: string}` |

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

Additional examples:

*Slashing policy update*

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "gov_propose",
  "params": {
    "proposer": "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqp8d7z2",
    "kind": "policy.slashing",
    "payload": {
      "enabled": true,
      "maxPenaltyBps": 400,
      "windowSeconds": 600,
      "maxSlashWei": "2500",
      "evidenceTtlSeconds": 1200
    },
    "deposit": "500000000000000000000"
  }
}
```

*Role allow-list adjustment*

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "gov_propose",
  "params": {
    "proposer": "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqp8d7z2",
    "kind": "role.allowlist",
    "payload": {
      "grant": [{"role": "compliance", "address": "nhb1examplegrantrole00000000000000000000"}],
      "revoke": [{"role": "compliance", "address": "nhb1examplerevokerole000000000000000000"}],
      "memo": "Rotate compliance operators"
    }
  }
}
```

*Treasury directive*

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "gov_propose",
  "params": {
    "proposer": "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqp8d7z2",
    "kind": "treasury.directive",
    "payload": {
      "source": "nhb1treasuryallowlisted000000000000000000",
      "transfers": [
        {"to": "nhb1recipient000000000000000000000000000", "amountWei": "250000000000000000000", "memo": "Q3 grants"}
      ]
    }
  }
}
```

Invalid payloads return `codeInvalidParams` along with a descriptive error. Examples include unknown parameter keys, out-of-range numeric values, unapproved roles, or treasury sources that are not allow-listed.

### Tally Schema

Finalized proposals expose a `tally` object summarising the recorded voting
power. The ratios are expressed in basis points (1/10,000). `yes_ratio_bps`
excludes abstain votes from the numerator and denominator per the governance
rules.

```json
{
  "turnout_bps": 4200,
  "quorum_bps": 3000,
  "yes_power_bps": 2100,
  "no_power_bps": 1500,
  "abstain_power_bps": 600,
  "yes_ratio_bps": 5833,
  "pass_threshold_bps": 5000,
  "total_ballots": 12
}
```
