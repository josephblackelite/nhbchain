# Paymaster Top-up Controls

The paymaster auto top-up pathway transfers `ZNHB` from a designated funding
wallet into sponsorship accounts when balances fall below the configured floor.
It is not a mint path.

## Governance Requirements

* Auto top-up is disabled by default.
* Operators must configure the token, minimum balance, top-up amount, cooldown,
  daily cap, funding wallet, execution operator, and approver identities before
  the pathway can run.
* The execution operator and approver must be distinct identities.
* The execution operator must hold the configured execution role.
* The approver must hold the configured approval role.

## Execution Safeguards

* Top-ups only occur when the paymaster balance is below the configured floor.
* `DailyCapWei` limits how much can be transferred to a paymaster in a UTC day.
* Cooldowns prevent back-to-back replenishment loops.
* Insufficient funding-wallet balance results in a failure event instead of
  partial execution.

## Monitoring and Auditability

* Every attempt emits a `paymaster.autotopup` event.
* Success events show the funded amount and resulting paymaster balance.
* Failure events expose the reason code for audit and alerting.
* Metrics aggregate funded volume and success/failure counts for dashboards and
  alert rules.

## Operational Response

1. Pause the top-up policy if abuse or misconfiguration is suspected.
2. Rotate operator and approver identities if credentials or role assignments
   are compromised.
3. Reconcile funding-wallet debits against `paymaster.autotopup` events and
   merchant sponsorship usage.
