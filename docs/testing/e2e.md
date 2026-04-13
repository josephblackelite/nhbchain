# End-to-End & Chaos Testing

The end-to-end suite exercises the supply → borrow → repay, mint → redeem, and governance proposal flows across a synthetic service mesh. Chaos tests simulate process crashes and validate that retries are idempotent and recovery occurs in under 30 seconds.

## Running locally

```bash
go test ./tests/e2e ./tests/chaos
```

Both packages build an in-process cluster consisting of stubbed versions of `consensusd`, `p2pd`, `lendingd`, `swapd`, `governd`, and the HTTP gateway. Each service exposes a minimal API that mimics the real production interfaces so downstream clients can exercise the expected flows.

## What the tests cover

- **Lending flow:** supply collateral, borrow against it, repay, and verify health metrics through the gateway.
- **Swap flow:** mint and redeem tokens while respecting balance limits and idempotent request IDs.
- **Governance flow:** submit a proposal, cast votes, apply the change, and confirm the result via consensus state.
- **Chaos:** terminate `lendingd` or `swapd` mid-transaction, restart, and ensure the original request ID can be safely retried with consistent results.

## CI integration

Add the following job to your GitHub Actions workflow to run the suite on each pull request:

```yaml
jobs:
  e2e:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.21"
      - run: go test ./tests/e2e ./tests/chaos
```

This command returns non-zero on any regression, allowing CI to block merges until the full flows and chaos recovery scenarios succeed.
