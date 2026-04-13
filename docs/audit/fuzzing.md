# Fuzzing Guide

Fuzzing validates the resilience of consensus-critical, cryptographic, and financial components under unexpected input. Follow this guide to configure fuzzers, capture crashes, and interpret results.

## Targets

| Component | Location | Harness |
| --- | --- | --- |
| Consensus state transitions | `consensus/` | `go test ./consensus/... -run TestStateTransitionFuzz -fuzz=.` |
| Crypto primitives | `crypto/` | Go fuzz targets under `*_test.go` files (e.g., `TestSignatureFuzz`). |
| Gateway order processing | `services/otc-gateway/` | `go test ./services/otc-gateway/... -run TestVoucherFuzz -fuzz=.` |
| SDK transaction builders | `sdk/` | `go test ./sdk/... -run TestTxBuilderFuzz -fuzz=.` |

## Setup

1. Ensure Go 1.20+ is installed (`go env GOVERSION`).
2. Set `GOFUZZNOCOMPRESS=1` and `GOMAXPROCS` to match available cores for deterministic reproduction.
3. Configure timeouts using `-fuzztime=5m` (adjust per target) to balance coverage and runtime.
4. Create a dedicated `artifacts/fuzz/` directory to store crash seeds and logs.

## Running fuzzers

```bash
GOFUZZNOCOMPRESS=1 go test ./consensus/... -run TestStateTransitionFuzz -fuzz=. -fuzztime=10m -fuzzminimize
```

Repeat for each target, adjusting packages and time budgets. Monitor CPU and memory usage to prevent host instability.

## Crash triage

1. **Reproduce.** Run the reported seed with `go test -run TestStateTransitionFuzz -fuzz=. -fuzztime=1x -fuzzseed=<seed>`.
2. **Classify impact.** Determine whether the failure affects consensus safety, liveness, or results in denial-of-service.
3. **File an issue.** Include stack traces, minimized corpus inputs, and suspected root cause.
4. **Verify fixes.** Add regression unit tests or invariants and re-run the fuzzer to ensure the seed no longer crashes.

## Corpus management

- Commit known-good seed corpora under `tests/fuzz/<target>/` for reproducibility.
- Periodically prune redundant seeds to keep iterations fast.
- Share crash artifacts via the audit folder with timestamps and reproduction commands.

## Exit criteria

- No unreproduced crashes remain.
- High-impact bugs have validated fixes with associated regression tests.
- Fuzzer coverage reports show steady state (no new edges found) for at least two runs of the configured duration.
