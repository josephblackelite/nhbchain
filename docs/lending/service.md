# Lending Service (`lendingd`)

The `lendingd` daemon exposes the `lending.v1` gRPC API described in
`proto/lending/v1/lending.proto`. The service currently starts an empty gRPC
server that reserves the canonical port (`50053`) and will be extended in future
iterations to proxy requests into the on-chain lending module.

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
`UNIMPLEMENTED` for all RPCs.
