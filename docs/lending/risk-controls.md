# Lending Risk Controls

The lending engine enforces several guardrails to protect market health. These
mechanisms layer on top of the existing loan-to-value limits and liquidation
flows to harden the protocol against rounding exploits, runaway borrowing, and
oracle failures.

## Fixed-point precision and minimum liquidity

Interest indexes now use 27 decimal fixed-point precision (1e27) to minimise
rounding drift. Supply and borrow shares are derived from these high precision
indexes using half-up rounding. During bootstrap a pool requires a minimum of
1 NHB (1e18 wei) to be supplied; smaller deposits are rejected to prevent
share-mint rounding exploits.

## Borrow caps

Governance can configure three independent throttles via the `BorrowCaps`
structure:

- **Per-block cap**: the total NHB that can be borrowed in a single block.
- **Utilisation cap**: the maximum utilisation ratio (borrowed ÷ supplied) in
  basis points.
- **Global cap**: the absolute upper bound on outstanding borrows.

Breaching any cap causes the borrow to revert while leaving the market state
unchanged.

## Oracle freshness and deviation

Markets track the median oracle quote and the block height of the last update.
Borrows are halted when:

- the quote is older than `Oracle.MaxAgeBlocks`, or
- the median deviates from the prior update by more than
  `Oracle.MaxDeviationBps` (basis points).

Repayments remain available so borrowers can reduce exposure while the oracle
is considered unhealthy.

## Action pauses

In addition to module-wide pauses, governance can disable individual flows via
`ActionPauses`:

- supply
- borrow
- repay
- liquidate

Each switch blocks the associated action while allowing unaffected paths to
continue operating. Inspect the live pause map with
`go run ./examples/docs/ops/read_pauses` and stage emergency toggles via
`go run ./examples/docs/ops/pause_toggle --module lending --state pause` so the
incident response playbook has copy-paste commands.
