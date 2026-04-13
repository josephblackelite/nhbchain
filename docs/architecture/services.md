# Service Topology

The node is decomposed into a small collection of gRPC microservices so that
consensus, networking, and application surfaces can evolve independently. The
current layout and suggested default ports are:

| Service     | Proto Package       | Default Port | Description |
|-------------|---------------------|--------------|-------------|
| Consensus   | `nhb.consensus.v1`  | `50051`      | Handles block production, validator set queries, and mempool management. |
| Network     | `nhb.network.v1`    | `50052`      | Provides gossip fan-out, peer management, and connectivity telemetry. |
| Lending     | `nhb.lending.v1`    | `50053`      | Exposes on-chain money market state and execution endpoints. |
| Swap        | `nhb.swap.v1`       | `50054`      | Routes AMM swaps and returns pool state snapshots. |
| Governance  | `nhb.gov.v1`        | `50055`      | Surfaces proposal lifecycle management and voting APIs. |

Each service owns its protobuf definitions under `proto/<domain>/v1` and is
versioned independently following semantic versioning. Clients should negotiate
with the service registry or configuration management layer to discover the
actual bind addresses used in a deployment.

## Local Development Layout

Running `make proto` regenerates all Go and TypeScript protobuf stubs. Running
`make sdk` compiles the typed Go clients under `sdk/` to ensure the generated
artifacts remain buildable.

For smoke testing clients there is a set of basic examples under
`examples/clients`:

- `examples/clients/go/basic_consensus_client/main.go` demonstrates dialing the
  consensus service with the Go SDK.
- `examples/clients/ts/basic_consensus_client.ts` illustrates a Connect/TS
  client hitting the same API surface.

The services operate independently and can be deployed on separate hosts. The
network service must be reachable by consensus to support gossip. Application
services (lending, swap, governance) consume consensus state and can be scaled
horizontally behind load balancers.
