# Fuzzing Guide

This page lists the Go fuzz targets that exist in the repository and how to run them. The module requires Go 1.24 (`go.mod`).

## Targets

| Fuzz function | File | What it checks |
| --- | --- | --- |
| `FuzzCanonicalizeNonce` | `p2p/nonce_fuzz_test.go` | Handshake nonce canonicalisation: canonical output is lowercase, even length, unprefixed valid hex, idempotent, and a nonce is rejected as a replay in both original and canonical form (see [handshake.md](../security/handshake.md)). |
| `FuzzShareRedemptionProRata` | `tests/creator/shares_test.go` | Two fans stake on one creator; the first fan's unstake must not return more than that fan deposited. |
| `FuzzCreatorURISanitization` | `tests/fuzz/creator_uri_fuzz.go` | `PublishContent` accepts only trimmed, non-empty URIs of at most 512 bytes with an `https`, `ipfs`, `ar` or `nhb` scheme. |
| `FuzzGovernancePolicyDeltas` | `tests/fuzz/gov_policy_fuzz.go` | Randomised governance, slashing, mempool and block-limit policy deltas applied to a baseline through `govcfg` validation. |
| `FuzzLendingSupplyWithdrawAmounts` | `tests/fuzz/lending_amounts_fuzz.go` | Lending supply then withdraw: shares minted are positive, share totals are tracked and restored, and a failed supply leaves liquidity unchanged. |
| `FuzzPotsoEvidencePipeline` | `tests/fuzz/potso_evidence_fuzz.go` | POTSO evidence store, penalty engine and reward splitting: duplicate evidence is not re-accepted and a reward split never assigns more than the pool. |

These are the only `testing.F` fuzz targets in the repository.

The four files under `tests/fuzz/` are named `*_fuzz.go`, not `*_test.go`. The Go tool only discovers fuzz functions in `_test.go` files, so `go test -fuzz` does not run them as the repository is laid out.

## Running a fuzzer

Go allows `-fuzz` to match exactly one fuzz function in exactly one package (`go test` rejects `-fuzz` across multiple packages with `cannot use -fuzz flag with multiple packages`). Run one target per invocation:

```bash
go test ./p2p -run '^$' -fuzz '^FuzzCanonicalizeNonce$' -fuzztime 5m
go test ./tests/creator -run '^$' -fuzz '^FuzzShareRedemptionProRata$' -fuzztime 5m
```

`make bugcheck-fuzz` runs `go test -run ^$ -fuzz=Fuzz -fuzztime=60s ./tests/... ./p2p`, which names several packages and therefore hits the restriction above.

Seed inputs come from the `f.Add(...)` calls in each target. Go writes inputs that make a target fail to `testdata/fuzz/<FuzzName>/` next to the package (its cache of other interesting inputs lives in the Go build cache). No `testdata/fuzz` directories are checked in at present.

## Crash triage

1. **Reproduce.** A failing input is written to `testdata/fuzz/<FuzzName>/<hash>`. Re-run it with `go test ./<pkg> -run 'FuzzName/<hash>'`.
2. **Classify impact.** Decide whether the failure affects consensus safety, liveness, funds accounting or is a denial of service.
3. **File an issue** with the stack trace, the minimised input and the suspected root cause.
4. **Verify the fix.** Keep the reproducer file (or add a regression unit test) and re-run the fuzzer.

## Exit criteria

- No unreproduced crashes remain.
- Every fixed crash has a regression test or a committed reproducer.
