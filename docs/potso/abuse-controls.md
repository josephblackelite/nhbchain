# POTSO Abuse Controls

The POTSO rewards pipeline now exposes explicit guardrails that mitigate farming
attacks without penalising legitimate validators or community participants. This
note captures the rationale behind each control, how it is enforced in code, and
which regression tests cover the behaviour.

## Parameter Overview

| Parameter | Purpose | Operational Impact |
|-----------|---------|--------------------|
| `MinStakeToEarnWei` | Enforces a minimum bonded stake before engagement meters can accrue. | Prevents low-value accounts from farming rewards with negligible stake while still allowing bonded stake to count toward the staking share once eligibility is met. |
| `QuadraticTxDampenAfter` / `QuadraticTxDampenPower` | Applies a concave (square-root style) curve to transaction counts after a configurable knee point. | Reduces the marginal gain from spammy transaction bursts and smooths the leaderboard when legitimate validators scale activity. |
| `MaxUserShareBps` | Caps the percentage of the epoch budget any single winner can receive. | Enables governance to set hard upper bounds on reward concentration and ensures excess budget is redistributed, not burned. |

## Test Coverage

The following Go tests exercise the new controls and should be extended whenever
the underlying mechanics change:

- `TestMinStakeToEarnZerosEngagement` (in `native/potso/metrics_abuse_test.go`)
  asserts that engagement is zeroed for under-staked addresses while keeping the
  account in the candidate set for stake-based weight.
- `TestZeroValueParticipantDropped` confirms that addresses with zero stake and
  zero engagement are excluded from the snapshot, avoiding unstable ordering.
- `TestQuadraticTxDampening` verifies the dampening curve by checking that large
  transaction bursts collapse to the configured square-root projection.
- `TestComputeRewardsMaxUserShareRedistribution` (in `native/potso/rewards_test.go`)
  demonstrates deterministic redistribution when a leader exceeds the share cap.
- `TestComputeRewardsMaxUserShareAllClipped` ensures a zero cap retains the
  entire budget as remainder and produces no payouts.

Running `go test ./native/potso/...` will execute the full suite.

## Operational Guidance

- Tune `MinStakeToEarnWei` alongside delegation incentives. Raising the value
  too aggressively can starve new validators of the engagement component.
- Start with `QuadraticTxDampenAfter` at or slightly above the historical median
  transaction count per epoch. Adjusting the power higher than `2` produces a
  flatter response, while values close to `1` approximate linear behaviour.
- Treat `MaxUserShareBps` as a governance lever. Increasing the cap restores the
  prior distribution model, while lowering it widens the winner set without
  rewiring the staking weights.

Refer to `spec.md` for the formal math and to `dashboards.md` for monitoring
recommendations that surface when the controls activate.
