# Paymaster Automatic Top-up Runbook

The automatic top-up moves one asset, `NHB` or `ZNHB`, from a configured funding account to a
sponsor (paymaster) account whose balance of that asset has fallen below a floor. It is a
transfer between existing accounts, not a mint (`maybeAutoTopUpPaymaster` in
`core/sponsorship.go`). The asset is chosen once, from the policy's `Token`, and the balance check, the
funding debit, the sponsor credit and the fee all use it. `NHB` is the asset sponsored gas is
paid in (`paymasterSponsoredAsset`), so it is the default.

## How it works

* **When it runs.** Right after a sponsored transaction is applied (see
  [Paymaster administration](../launch/paymaster-admin.md)), the state processor checks the
  sponsor's balance of the policy's token. Nothing happens unless the policy is enabled, the
  token is `NHB` or `ZNHB`, `MinBalanceWei` is positive and the balance is below it.
* **Configuration.** The `[global.Paymaster.AutoTopUp]` section of `config.toml`, read when the
  node starts (`config/global.go`, `cmd/nhb/main.go`):

  | Key | Meaning |
  | --- | --- |
  | `Enabled` | Turns the feature on. Off by default. |
  | `Token` | `NHB` or `ZNHB`. An empty value or an absent key means `NHB`; anything else fails config parsing (`invalid global.paymaster.AutoTopUp.Token: must be NHB or ZNHB`, `config/global.go`). |
  | `MinBalanceWei` | Top up when the sponsor's balance of `Token` is below this. |
  | `TopUpAmountWei` | Amount credited to the sponsor per top-up. |
  | `DailyCapWei` | Must be positive when `Enabled` is true. Limits the total debited from the funding account per sponsor per UTC day. |
  | `CooldownSeconds` | Minimum time between top-ups for one sponsor. |
  | `Governance.FundingAccount` | Account debited. |
  | `Governance.Minter`, `Governance.Approver` | Two different addresses that must both be set. |
  | `Governance.MinterRole`, `Governance.ApproverRole` | Roles the two addresses must hold in chain state. When the value in `config.toml` is empty or absent, the loader replaces it with the built-in default, `ROLE_PAYMASTER_AUTOFUND` and `ROLE_PAYMASTER_ADMIN` (`config/config.go`), so the role checks are on for a node configured from a file. |

* **Fee.** The governance parameter `paymaster.topUpFeeWei` (in `defaultAllowedGovernanceParams` in `config/config.go`, which applies only when `Governance.AllowedParams` is empty; the repository's sample `config.toml` sets its own `AllowedParams` list that does not include it, so a node run from that file rejects a proposal for it unless the key is added; default value `0`)
  adds a flat fee, in the same `Token`, that is also debited from the funding account and
  credited to the escrow fee treasury. The daily cap counts amount plus fee.
* **Restarts.** The policy is not stored in chain state. Changing it requires editing
  `config.toml` and restarting the node.

## Monitoring signals

* Metrics (`observability/metrics.go`): `nhb_paymaster_autotopups_total{outcome="success"|"failure"}`
  and `nhb_paymaster_autotopup_amount_wei_total{outcome}`, the sum of amounts topped up. The alert
  rules on both are in `observability/alerts.yaml` (see the [alert runbook](./alerts.md)).
* Event `paymaster.autotopup` with attributes `paymaster` (printed as a `znhb1...` bech32 address, `core/events/sponsorship.go`), `token`, `status` (`success` or
  `failure`), `reason` (failures only), `amountWei`, `balanceWei`, `feeWei` (when a fee applied)
  and `day`.
* Failure reasons in the code: `amount_not_configured`, `paymaster_missing`,
  `treasury_missing` (a fee is set but no escrow fee treasury is configured),
  `daily_cap_exceeded`, `cooldown_active`, `funding_account_missing`, `operator_missing`,
  `operator_role_missing`, `approver_missing` (also used when the minter and approver are the
  same address), `approver_role_missing`, `funding_insufficient`,
  `funding_account_in_use` (the funding account is the sponsor itself or the sender or recipient of
  the sponsored transfer being applied) and `treasury_account_in_use` (a fee applies and the escrow
  fee treasury is the sender or recipient of that transfer). The last two exist because the
  transfer writes its own accounts after the top-up, which would overwrite the top-up's write.

## Investigation checklist

1. Compare the amounts topped up in the last 24 hours (metric or events) with `DailyCapWei`.
2. Check that the funding account still holds enough of the configured `Token` for `TopUpAmountWei` plus any fee.
3. Check that the `Minter` and `Approver` addresses still hold the configured roles.
4. Review the sponsored traffic for the sponsor (events `tx.sponsorship.applied`) to see
   whether a merchant or device is driving the top-ups.

## Mitigation

* Set `Enabled = false` in `config.toml` and restart the node to stop top-ups.
* Lower `DailyCapWei` or raise `CooldownSeconds`, then restart.
* Refill the funding account if `funding_insufficient` appears.
* Pause or throttle the merchant or device that generates the traffic (see
  [POS pause and revoke](./pos-pause-revoke.md) and [paymaster budgets](./paymaster-budgets.md)).
