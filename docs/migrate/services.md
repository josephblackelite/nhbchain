# Migration Guide: Service-Oriented Topology

This guide walks through migrating from the legacy JSON-RPC node to the new
service-oriented topology.

## 1. Prepare infrastructure

- Allocate separate compute for the gateway, consensus, lending, price oracle,
  and state services. Each can be scaled independently after cutover.
- Deploy a managed Postgres cluster and Redis instance to back the stateful
  services.

## 2. Bootstrap consensus + `p2pd`

- Promote a validator key and provision the consensus service container.
- Launch `p2pd` next to every consensus node using the new `p2pd.toml` schema.
- Allow gossip ports (`26656`) between validators and seeds.

## 3. Stand up gateways

- Deploy at least two gateway replicas behind your edge load balancer.
- Configure JWT signing keys and consensus endpoints via environment variables.
- Update DNS to point wallets and partner applications at the gateway tier.

## 4. Bring up domain services

- Configure the lending service with oracle endpoints and market metadata.
- Provision the price oracle service with publisher API keys and threshold
  policies.
- Connect both services to consensus using mTLS identities.

## 5. Drain the legacy node

- Freeze the legacy JSON-RPC node by rejecting external RPC requests.
- Replay the last 1,000 blocks into the new consensus service to validate state
  parity.
- Switch traffic from the legacy node to the gateway using a weighted load
  balancer change.

## 6. Validate + monitor

- Run the developer cookbooks (first transaction, position query, price oracle
  publish) against production endpoints.
- Monitor consensus, gateway, and oracle dashboards for error spikes.
- Decommission the legacy node after 24 hours of stable metrics.

For detailed service configuration, refer to the [service directory](../services/index.md).
