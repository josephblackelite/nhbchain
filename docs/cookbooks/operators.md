# Operator Cookbooks

These guides target operators responsible for provisioning and maintaining the
new service-oriented topology.

## Run a validator

1. Provision infrastructure that satisfies the recommended CPU, RAM, and SSD
   throughput targets.
2. Deploy the consensus service container and point it at your validator key
   material.
3. Launch a colocated `p2pd` instance so the validator can participate in the
   gossip mesh.
4. Expose only the gateway and telemetry ports through the load balancer.

```
docker run --rm \
  -v $PWD/validator.keys:/keys \
  -e CONSENSUS_DB_DSN=postgres://validator@db/nhb \
  -e CONSENSUS_PRIV_KEY=/keys/validator.key \
  -e CONSENSUS_P2P_ADDR=/ip4/0.0.0.0/tcp/26656 \
  ghcr.io/nhbchain/consensus:latest
```

## Run `p2pd`

1. Configure persistent peers and seed nodes in `p2pd.toml`.
2. Mount the libp2p key so the daemon preserves its node identity.
3. Co-locate `p2pd` with every consensus service replica and expose `26656`.

```
docker run --rm \
  -v $PWD/p2pd.toml:/etc/nhb/p2pd.toml \
  -v $PWD/p2p.keys:/var/lib/p2pd \
  -e P2PD_CONFIG=/etc/nhb/p2pd.toml \
  --network host \
  ghcr.io/nhbchain/p2pd:latest
```

## Upgrade services

1. Drain traffic from the gateway by removing the instance from the load
   balancer.
2. Deploy the new container tag for each service, starting with stateless
   gateways, then stateful consensus nodes, and finally domain services.
3. Run smoke tests against the gateway and lending service.
4. Rotate traffic back and monitor the dashboards for regressions.

```
kubectl rollout restart deployment/nhb-gateway
kubectl rollout status deployment/nhb-gateway
kubectl rollout restart statefulset/nhb-consensus
kubectl rollout restart deployment/nhb-lending
```
