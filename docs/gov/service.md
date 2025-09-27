# Governance Service (`governd`)

The governance service exposes a gRPC surface for submitting proposals and
ballots while mirroring on-chain governance state through a set of read models.
It wraps the consensus transaction envelope helpers so that downstream tooling
can interact with governance without embedding consensus-specific logic.

## Configuration

`governd` loads a YAML configuration file. The example below matches the default
`services/governd/config.yaml` shipped with the repository.

```yaml
listen: ":50061"              # gRPC listen address
consensus: "localhost:9090"   # consensus service endpoint
chain_id: "localnet"          # consensus chain identifier
signer_key: "<hex private key>" # 32 byte hex encoded secp256k1 key material
nonce_start: 1                 # next account nonce to use when building envelopes
fee:                           # optional transaction fee metadata
  amount: ""
  denom: ""
  payer: ""
```

* **`signer_key`** is required and must be the lowercase hexadecimal encoding of
  the 32 byte secp256k1 private key used to sign governance transactions.
* **`nonce_start`** should be set to the next available account nonce for the
  configured signer. The service increments this value for each successfully
  constructed envelope. When rotating signers or restarting the process make
  sure the nonce is resynchronised with on-chain state to avoid replay
  protection errors.

## Running the service

```bash
$ go run services/governd/main.go --config services/governd/config.yaml
2024/05/28 09:15:24 governd listening on :50061
```

The service establishes a single consensus client connection and registers both
`gov.v1.Query` and `gov.v1.Msg` gRPC services.

## Query API

The read API mirrors the structures returned by the on-chain governance module
while using pagination primitives friendly to explorer-style consumers.

| RPC | Description |
| --- | ----------- |
| `GetProposal` | Returns a single proposal by identifier. |
| `ListProposals` | Streams proposals in reverse identifier order with optional status filtering. |
| `GetTally` | Computes the latest tally for a proposal using the consensus state votes. |

### Pagination semantics

`ListProposals` uses a cursor-based token. The response `next_page_token` can be
fed back into subsequent requests to continue iterating older proposal
identifiers. When the token is absent all proposals have been consumed.

## Transaction API

The `gov.v1.Msg` surface converts module messages into signed consensus
transactions before forwarding them to the validator. Responses contain the
transaction hash so callers can correlate with block explorers or observability
pipelines.

| RPC | Description |
| --- | ----------- |
| `SubmitProposal` | Broadcasts a `MsgSubmitProposal` locking the provided deposit. |
| `Vote` | Broadcasts a `MsgVote` selecting `yes`, `no`, or `abstain`. |
| `Deposit` | Broadcasts a `MsgDeposit` to top-up proposal escrow. |

All transaction helpers validate basic fields using the Go SDK prior to
constructing the consensus envelope. Validation errors are surfaced as
`INVALID_ARGUMENT` gRPC codes.

### Nonce management

`governd` tracks the next nonce in memory. If the consensus submission fails the
nonce is still considered consumed to avoid double-use. Operators should bump
`nonce_start` manually when resynchronising with state or when rotating the
signing account.

## Error handling

| Scenario | gRPC status |
| -------- | ------------ |
| Unknown proposal or tally | `NOT_FOUND` |
| Invalid identifiers or malformed payloads | `INVALID_ARGUMENT` |
| Consensus connectivity issues | `UNAVAILABLE` or `INTERNAL` depending on the failure point |

## Generated client stubs

The repository includes generated Go and TypeScript stubs under
`proto/gov/v1` and `clients/ts/gov/v1`. Use the Go SDK helpers in `sdk/gov` to
simplify message construction and submission from custom tooling.
