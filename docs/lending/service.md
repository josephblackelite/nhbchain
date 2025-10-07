# Lending Service (`lendingd`)

> **Preview notice:** `lendingd` is shipped as a **disabled preview** while the
> supply/withdraw/borrow/repay/liquidate flows are still being integrated with
> the on-chain lending engine. The binary starts and reserves the canonical
> gRPC port (`50053`) but every RPC currently returns `UNIMPLEMENTED`. The
> service remains opt-in until the API is fully wired up.

The `lendingd` daemon exposes the `lending.v1` gRPC API described in
`proto/lending/v1/lending.proto`. Future iterations will proxy requests into the
core lending engine once the remaining RPC handlers are implemented.

## Configuration

The service loads YAML configuration via the `-config` flag. A minimal example
is provided in `services/lending/config.yaml`:

```yaml
listen: ":50053"
```

## Running locally

```bash
go run ./services/lendingd -config services/lending/config.yaml
```

The server responds on the configured address but, at this stage, returns
`UNIMPLEMENTED` for all RPCs. Deployments SHOULD keep the service disabled (the
default in Helm charts) until the handlers are available.
