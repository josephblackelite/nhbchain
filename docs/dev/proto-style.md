# Protobuf tooling and conventions

Protobuf sources live under `proto/`. Buf drives linting, breaking-change checks
and code generation.

## Tooling (what the repository configures)

* `proto/buf.yaml`: lint rule set `DEFAULT`; breaking-change rule set `FILE`.
  `buf.work.yaml` declares `proto` as the only workspace directory.
* `buf.gen.yaml`: plugins `go` and `go-grpc` write into `proto/` next to the
  sources (`paths=source_relative`); `ts_proto` (from
  `./node_modules/.bin/protoc-gen-ts_proto`, options `env=node`,
  `esModuleInterop=true`, `outputServices=grpc-js`, `useExactTypes=false`) writes
  into `clients/ts/`.
* `make proto` runs `go run ./tools/proto/gen.go`, which executes in order:
  `buf format -w`, `buf lint`, `buf breaking --against $BUF_BREAKING_AGAINST`
  (only when that variable is set), `buf generate`. It stops at the first failing
  step.
* `make sdk` runs `make proto`, then `go test ./...` in `sdk/`.
* `make bugcheck-proto` runs `buf lint && buf breaking --against
  ".git#branch=main"`.

## Conventions visible in the existing files

* Files live in `proto/<domain>/v1/` with package `<domain>.v1` (`consensus.v1`,
  `network.v1`, `lending.v1`, `swap.v1`, `gov.v1`). The exceptions are
  `proto/pos` (package `pos.v1`, no `v1` directory), `proto/tx` (refund query) and
  `proto/fees/v1` (package `nhb.fees.v1`).
* Enum values carry the enum name as a prefix (`PROPOSAL_STATUS_VOTING_PERIOD` in
  `gov/v1/query.proto`, `FINALITY_STATUS_FINALIZED` in `pos/realtime.proto`), as
  the Buf `DEFAULT` rules require.
* Amounts of arbitrary precision are strings holding base-10 integers
  (`lending.v1`, `pos.v1`), or a `BigInt` message (`consensus.v1`).
* Addresses in the lending and POS messages are bech32 strings; hashes and
  references (`intent_ref`, `tx_hash`, `block_hash`) are `bytes`.
* Pagination: the only paged list RPC is `gov.v1.Query/ListProposals`, using
  `page_size`, `page_token` and `next_page_token`. Other list RPCs
  (`ListMarkets`, `ListPeers`, `ListPools`) are not paged.
* Errors are returned as gRPC status codes with a message string; no rich error
  details are used.
* When a package changes incompatibly, run the breaking check against the
  reference you want by setting `BUF_BREAKING_AGAINST` (for example
  `.git#branch=main`) before `make proto`.
