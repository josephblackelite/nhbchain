# NHBChain Platform Overview

The NHBChain platform is now delivered as a **service-oriented topology**. Each
service exposes a narrowly scoped API, deploys independently, and communicates
through authenticated gRPC streams. The diagram below illustrates the default
production topology.

```
┌─────────────────┐        ┌──────────────────┐        ┌───────────────────┐
│  Client Wallets │  gRPC  │  Gateway Service │  gRPC  │ Consensus Service │
└─────────────────┘  REST  └──────────────────┘  gRPC  └───────────────────┘
          ▲                     ▲      ▲                    ▲
          │                     │      │                    │
          │                     │      │            ┌─────────────────┐
          │                     │      │            │  State Service  │
          │                     │      │            └─────────────────┘
          │                     │      │                    ▲
          │                     │      │                    │
          │                     │      │            ┌─────────────────┐
          │                     │      └────────────│  Lending Svc    │
          │                     │                   └─────────────────┘
          │                     │            ┌─────────────────────────┐
          └───────────┬─────────┴────────────│  Price Oracle Service   │
                      │                      └─────────────────────────┘
                      │
              ┌──────────────┐
              │  P2P Daemon  │
              └──────────────┘
```

The gateway terminates HTTP requests and forwards signed transactions to the
consensus service. Domain services such as lending or price oracle subscribe to
state updates and expose module-specific gRPC APIs. Operators run the P2P daemon
(`p2pd`) to gossip network events and seed validators.

## Glossary

- **Gateway Service** – Authenticates REST clients, translates requests into
  signed envelopes, and proxies consensus queries.
- **Consensus Service** – Authoritative ledger component that finalises blocks,
  exposes state queries, and accepts signed envelopes from gateways and
  services.
- **State Service** – Materialises module state into queryable projections for
  analytics and dashboards.
- **Domain Services** – Independent services (lending, swap, price oracle,
  identity, etc.) that encapsulate module-specific logic.
- **P2P Daemon (`p2pd`)** – Maintains gossip mesh connectivity and supplies
  validator peers with fresh network metadata.
- **Operator Control Plane** – Automation scripts, Helm charts, and Terraform
  bundles that provision, upgrade, and monitor services as isolated workloads.

Use the [cookbooks](./cookbooks/developers.md) for task-driven guides and the
[service references](./services/index.md) for API-by-API details.
