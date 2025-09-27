# Service Directory

Each NHBChain workload is deployed as an independently scalable service. Use the
following references to configure, monitor, and integrate with each component.

## Gateway Service

- **Endpoint:** HTTPS / REST + gRPC streaming.
- **Responsibilities:** Request authentication, REST to gRPC translation, rate
  limiting, and transaction memo enrichment.
- **Key configuration:** `GATEWAY_BIND`, `GATEWAY_RATE_LIMIT`,
  `CONSENSUS_GRPC_ENDPOINT`.
- **Operational notes:** Deploy at least two replicas behind your public load
  balancer. Gateways should be stateless and read the validator set from the
  consensus service on startup.

## Consensus Service

- **Endpoint:** gRPC on `9090` by default.
- **Responsibilities:** Validates signed envelopes, executes transactions,
  materialises blocks, and exposes deterministic state queries.
- **Key configuration:** `CONSENSUS_DB_DSN`, `CONSENSUS_P2P_ADDR`,
  `P2P_SEEDS`.
- **Operational notes:** Validators run the consensus service co-located with a
  `p2pd` instance. Horizontally scale read-only replicas for query workloads.

## State Service

- **Endpoint:** gRPC and GraphQL for denormalised projections.
- **Responsibilities:** Subscribes to block events, projects module state into
  Postgres, and serves analytical queries.
- **Key configuration:** `STATE_EVENT_STREAM`, `STATE_GRAPHQL_PORT`,
  `STATE_RETENTION_DAYS`.
- **Operational notes:** Scale according to dashboard load. Downstream caches
  should treat the state service as the source of truth for complex reporting.

## Lending Service

- **Endpoint:** gRPC on `9444` (configurable).
- **Responsibilities:** Enforces lending business logic, risk limits, and emits
  health factor telemetry per account.
- **Key configuration:** `LENDING_CONSENSUS_ENDPOINT`,
  `LENDING_PRICE_ORACLE_ENDPOINT`, `LENDING_MARKET_CONFIG`.
- **Operational notes:** Co-locate near the consensus service to minimise
  envelope latency. Configure circuit breakers for price oracle unavailability.

## Price Oracle Service

- **Endpoint:** gRPC streaming on `9555`.
- **Responsibilities:** Aggregates publisher feeds, normalises price updates,
  and signs attestations for downstream services.
- **Key configuration:** `ORACLE_PUBLISHERS`, `ORACLE_MIN_SIGNERS`,
  `ORACLE_CONSENSUS_ENDPOINT`.
- **Operational notes:** Run at least three replicas across availability zones.
  Publishers should use mTLS identities issued by the operator.

## P2P Daemon (`p2pd`)

- **Endpoint:** libp2p gossip ports (default `26656`).
- **Responsibilities:** Maintains the gossip mesh, exchanges seed lists, and
  propagates consensus metadata to validators and observers.
- **Operational notes:** Operators deploy `p2pd` alongside every consensus node
  and at edge locations serving RPC read replicas.

Refer to the [migration guide](../migrate/services.md) when upgrading from the
legacy JSON-RPC topology.

---

Additional module references:

- [Escrow Service API](./escrow.md)
