# Lending Service (`lendingd`)

`lendingd` exposes the `lending.v1` gRPC API described in
`proto/lending/v1/lending.proto` and proxies requests into the on-chain lending
engine through the node JSON-RPC surface.

## Configuration

The service loads YAML configuration via the `-config` flag. A minimal example
is provided in `services/lending/config.yaml`:

```yaml
listen: ":50053"
node_rpc_url: "https://127.0.0.1:8081"
shared_secret_header: "X-NHB-Shared-Secret"
shared_secret_value: "replace-me"
rate_limit_per_min: 120
```

## Running locally

```bash
go run ./services/lendingd -config services/lending/config.yaml
```

The daemon now serves:

- market reads via `GetMarket` and `ListMarkets`
- position reads via `GetPosition`
- transaction flows via `SupplyAsset`, `WithdrawAsset`, `BorrowAsset`, and `RepayAsset`

`lendingd` requires normal service authentication through API tokens and/or
mTLS, and its upstream node RPC credentials should be configured with the same
operational discipline as the other financial rail services.
