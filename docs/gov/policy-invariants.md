# Governance Policy Invariants

The runtime enforces a set of invariants before accepting governance policy
updates. These bounds guarantee that proposals cannot install values that would
halt core services or render future upgrades impossible.

## Quorum and Approval Thresholds

* **Quorum** must always be greater than or equal to the approval threshold.
  Proposals that drop quorum below the approval threshold are rejected during
  preflight, preventing a configuration where no outcome can ever pass.
* **Approval threshold** is clamped to non-trivial values (`>= 5,000` basis
  points). The client and chain both reject payloads that fall outside this
  range.

## Voting Periods

The minimum voting period is defined in software (default `3,600` seconds). Any
attempt to shorten the period below this floor is rejected. The same checks are
run by the client preflight helpers and the chain when applying a proposal.

## Slashing Windows

Slashing policy proposals must keep the evaluation window bounded within the
runtime limits. The minimum window must be greater than zero and may not exceed
its configured maximum. Evidence TTL values must also remain within their
published limits to avoid unbounded liability.

## Treasury Directives

Treasury directives require at least one transfer and the source vault must be
present on the allow list before execution. This prevents empty directives and
ensures proposals cannot route funds from unauthorised vaults.

## Dual Enforcement

All invariants are checked twice:

1. **Client preflight:** payloads are simulated against the current runtime
   configuration before the transaction is broadcast.
2. **Chain execution:** the same simulation runs when the proposal is queued or
   executed. Any violation aborts the proposal and the change is not applied.

This dual enforcement ensures misconfigured payloads are rejected before they
can brick the network, even if a client bypasses the service-side checks.
