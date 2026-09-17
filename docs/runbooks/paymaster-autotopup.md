# Paymaster Automatic Top-up Runbook

Automatic paymaster top-ups transfer `ZNHB` from a configured funding wallet
into sponsored paymaster balances when those balances dip below the configured
floor. The flow is treasury-funded, not inflationary.

## Monitoring Signals

* `nhb_paymaster_autotopups_total{outcome="success"|"failure"}` tracks
  execution outcomes.
* `nhb_paymaster_autotopup_amount_wei_total` tracks aggregate funded volume.
* The `paymaster.autotopup` event stream should be indexed for the following
  failure reasons:
  * `daily_cap_exceeded`
  * `cooldown_active`
  * `funding_insufficient`
  * `operator_missing`
  * `operator_role_missing`
  * `approver_missing`
  * `approver_role_missing`

## Investigation Checklist

1. Confirm whether the spike is organic sponsored demand or a runaway
   configuration.
2. Compare the funded 24h total against `DailyCapWei`.
3. Verify the configured funding wallet still holds enough `ZNHB`.
4. Confirm the execution operator and approver identities still hold the
   expected governance roles.
5. Review recent merchant/device sponsorship activity for abuse or loops.

## Mitigation Options

* Raise `DailyCapWei` only after treasury review.
* Pause top-ups by setting `enabled: false` when abuse is suspected.
* Refill the funding wallet from treasury if balances are legitimately low.
* Throttle or disable specific merchants if a single counterparty is driving the
  spike.

## Post-incident Actions

* Record total funded amount, root cause, and the policy change made.
* Confirm counters and event flow return to normal within one monitoring window.
* Review whether cooldown, cap, and role assignments still match expected
  production traffic.
