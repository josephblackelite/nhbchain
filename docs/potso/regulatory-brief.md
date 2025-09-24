# POTSO Rewards – Regulatory Brief

POTSO distributes existing treasury funds to validators based on a blend of
stake and verifiable engagement. No new tokens are minted, and the process is
fully deterministic.

## Key Points for Regulators & Investors

- **Non-inflationary:** Rewards are funded entirely from a pre-existing treasury
  address. The emission per epoch is capped by configuration and bounded by the
  treasury balance. If the treasury is empty, no payouts occur.

- **Transparent weight formula:** Participants are ranked using an auditable
  combination of bonded stake and engagement counters (transactions, escrow
  touchpoints, uptime). The weighting logic and parameters are public and can be
  re-run by any observer.

- **Operational incentives:** The decay-based EMA encourages validators to stay
  online and actively support network services. Short interruptions reduce the
  engagement component, providing a clear incentive to maintain high uptime.

- **Deterministic outcomes:** Every epoch stores the ranked winners, total stake
  and engagement aggregates, and the budget spent. These records ensure payouts
  are reproducible and resist manipulation.

- **Configurability under governance:** All weighting and eligibility thresholds
  are controlled by on-chain governance via configuration changes. Adjustments
  apply to future epochs only, providing predictability for participants.

## Reporting Considerations

- The `potso_leaderboard` RPC provides a machine-readable audit trail of each
  epoch’s winners, including basis-point shares. Regulators can use this data to
  monitor aggregate payouts or evaluate fairness across participants.

- The `potso_params` RPC confirms the exact configuration in force, enabling
  compliance teams to verify that policy changes (e.g., new minimum stake
  requirements) have been applied.

- Because payouts are deterministic and sourced from an existing treasury,
  distribution events do not constitute new security offerings or token
  issuance. They represent operational rewards for validators performing network
  maintenance tasks.

