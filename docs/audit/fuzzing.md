# Fuzzing Guide

This page lists the Go fuzz targets that exist in the repository and how to run them. The module requires Go 1.24 (`go.mod`).

## Targets

| Fuzz function | File | What it checks |
| --- | --- | --- |
| `FuzzCanonicalizeNonce` | `p2p/nonce_fuzz_test.go` | Handshake nonce canonicalisation: canonical output is lowercase, even length, unprefixed valid hex, idempotent, and a nonce is rejected as a replay in both original and canonical form (see [handshake.md](../security/handshake.md)). |
| `FuzzShareRedemptionProRata` | `tests/creator/shares_test.go` | Two fans stake on one creator; the first fan's unstake must not return more than that fan deposited. |
| `FuzzCreatorURISanitization` | `tests/fuzz/creator_uri_fuzz_test.go` | `PublishContent` accepts only trimmed, non-empty URIs of at most 512 bytes with an `https`, `ipfs`, `ar` or `nhb` scheme. |
| `FuzzGovernancePolicyDeltas` | `tests/fuzz/gov_policy_fuzz_test.go` | Randomised governance, slashing, mempool and block-limit policy deltas applied to a baseline through `govcfg` validation. |
| `FuzzLendingSupplyWithdrawAmounts` | `tests/fuzz/lending_amounts_fuzz_test.go` | Lending supply then withdraw: shares minted are positive, share totals are tracked and restored, up to the half-up rounding of the share conversion; the module balance equals the market's supplied total; the user's shares are cleared; and a failed supply leaves liquidity unchanged. |
| `FuzzPotsoEvidencePipeline` | `tests/fuzz/potso_evidence_fuzz_test.go` | POTSO evidence store, penalty engine and reward splitting: duplicate evidence is not re-accepted; a reward split never assigns more than the pool plus the rounding dust carried in; over all epochs the assigned amounts plus the rounding bucket equal the pools exactly; and no weight or amount goes negative. |

These are the only `testing.F` fuzz targets in the repository. The files under `tests/fuzz/` are `_test.go` files, so the Go tool discovers them; `scripts/fuzz_all.sh` finds targets by searching `_test.go` files for `func Fuzz...`, and `tests/scripts/fuzz_audit_targets_test.go` checks that it does.

## Running a fuzzer

Go allows `-fuzz` to match exactly one fuzz function in exactly one package (`go test` rejects `-fuzz` across multiple packages with `cannot use -fuzz flag with multiple packages`). To run one target, name its package and function:

```bash
go test ./p2p -run '^$' -fuzz '^FuzzCanonicalizeNonce$' -fuzztime 5m
go test ./tests/creator -run '^$' -fuzz '^FuzzShareRedemptionProRata$' -fuzztime 5m
```

To run every target, `make bugcheck-fuzz` runs `scripts/fuzz_all.sh`, which lists the targets and runs them one at a time, each for `FUZZTIME` (default `60s`), and exits non-zero if any of them fails. `FUZZ_LIST_ONLY=1 bash scripts/fuzz_all.sh` prints the `<package> <function>` pairs and runs nothing.

Seed inputs come from the `f.Add(...)` calls in each target. Go writes inputs that make a target fail to `testdata/fuzz/<FuzzName>/` next to the package (its cache of other interesting inputs lives in the Go build cache). No `testdata/fuzz` directories are checked in at present.

## Crash triage

1. **Reproduce.** A failing input is written to `testdata/fuzz/<FuzzName>/<hash>`. Re-run it with `go test ./<pkg> -run 'FuzzName/<hash>'`.
2. **Classify impact.** Decide whether the failure affects consensus safety, liveness, funds accounting or is a denial of service.
3. **File an issue** with the stack trace, the minimised input and the suspected root cause.
4. **Verify the fix.** Keep the reproducer file (or add a regression unit test) and re-run the fuzzer.

## Exit criteria

- No unreproduced crashes remain.
- Every fixed crash has a regression test or a committed reproducer.
