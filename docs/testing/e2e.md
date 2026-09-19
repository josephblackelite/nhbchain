# End-to-end and chaos tests

## Running locally

```bash
go test ./tests/e2e ./tests/chaos
```

`TestEndToEndFinancialFlows` and the chaos tests build an in-process cluster from
`tests/support/cluster`: HTTP stubs named `consensusd`, `p2pd`, `lendingd`,
`swapd`, `governd` and a `gateway`. These are test doubles that keep in-memory
state; they are not the real binaries (there is no `swapd` binary in this
repository).

## What each test covers

`tests/e2e`:

* `TestEndToEndFinancialFlows` (`flows_e2e_test.go`): through the stub gateway,
  supply, replay of the same `request_id` (idempotent), borrow, repay, position
  query, swap mint and redeem, governance proposal create, vote and apply, and a
  consensus snapshot check.
* `TestLendingRPCEndpoints` (`lending_rpc_test.go`): starts a real `core.Node` on
  an in-memory database with the real `rpc` server and exercises the lending
  JSON-RPC methods with a JWT.
* `TestPotsoTask3Determinism` (`potso_task3_test.go`): for seeds 42, 1337 and 9001,
  runs the same generated POTSO scenario through two pipeline nodes with
  different delivery orders (including duplicate deliveries), requires identical
  snapshots, and compares them to golden files (`UPDATE_POTSO_GOLDEN=1` rewrites
  the golden files).
* `TestAuditSmokePlan` (`audit_smoke_test.go`): checks that `ops/audit/e2e.yaml`
  parses and defines at least one check. `make audit:e2e` runs it and then the
  audit phase runner.

`tests/chaos` (`recovery_test.go`):

* `TestLendingFlowRecoversAfterServiceRestart`: kills the stub `lendingd` during a
  borrow, confirms the borrow fails, restarts it, retries with the same
  `request_id`, confirms the result is unchanged on a second retry, and fails if
  the sequence takes longer than 30 seconds.
* `TestSwapMintRedeemWithChaos`: mints, kills the stub `swapd` during a redeem,
  restarts it, retries the redeem, and confirms a duplicate redeem with the same
  `request_id` does not change the balance.

## Related make targets

`make bugcheck-gateway` runs `go test -run TestEndToEndFinancialFlows ./tests/e2e`.
`make audit:chaos`, `make audit:e2e` and `make audit:determinism` run the audit
phases defined in `Makefile` with the plans in `ops/audit/*.yaml`.

## CI

`.github/workflows/ci.yml` runs `go test ./...` on Ubuntu, macOS and Windows with
Go 1.24.3 (`go.mod` requires 1.24.0), builds `./cmd/nhb`, and then runs
`scripts/bugcheck.sh` (the `bugcheck` pipeline) on Linux. A minimal job for the
suites in this page:

```yaml
jobs:
  e2e:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.24.3"
      - run: go test ./tests/e2e ./tests/chaos
```
