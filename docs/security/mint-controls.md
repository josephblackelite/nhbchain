# Paymaster Top-up Controls

The paymaster auto top-up moves `ZNHB` from a configured funding account to a paymaster account when the paymaster's balance is below a floor. It transfers existing balance; it does not mint. The logic is `maybeAutoTopUpPaymaster` in `core/sponsorship.go`, and it runs while a sponsored transaction is processed (`core/state_transition.go`), after the sponsor's gas cost for that transaction has been deducted.

## Configuration

The policy is read from the `[global.Paymaster.AutoTopUp]` table (and its `Governance` sub-table) in the node configuration and parsed by `Global.PaymasterAutoTopUpConfig` in `config/global.go`.

| Key | Default | Meaning |
| --- | --- | --- |
| `Enabled` | `false` | The pathway does nothing unless this is true. |
| `Token` | `"ZNHB"` | Only `ZNHB` is accepted; any other value is a configuration error. |
| `MinBalanceWei` | `"0"` | Floor. A top-up is considered only when the paymaster's ZNHB balance is below it; a floor of zero disables it. |
| `TopUpAmountWei` | `"0"` | Amount credited to the paymaster. |
| `DailyCapWei` | `"0"` | Cap on the funding account's total daily outflow for this paymaster. Must be positive when `Enabled` is true, or configuration parsing fails. |
| `CooldownSeconds` | `0` | Minimum time between top-ups for one paymaster; zero means no cooldown. |
| `Governance.FundingAccount` | empty | Account debited. |
| `Governance.Minter` | empty | The execution operator. Field name in code: `Minter`. |
| `Governance.Approver` | empty | The approver. |
| `Governance.MinterRole` | `ROLE_PAYMASTER_AUTOFUND` | Role the operator must hold. |
| `Governance.ApproverRole` | `ROLE_PAYMASTER_ADMIN` | Role the approver must hold. |

Defaults are in `config/config.go` (`Paymaster.AutoTopUp`).

## Execution rules

A top-up runs only if all of the following hold; each check that fails after the balance test emits a failure event with the listed `reason` and changes nothing:

1. The policy is enabled, the token is `ZNHB`, `MinBalanceWei` is positive, and the paymaster balance is below it. (Otherwise there is no event.)
2. `TopUpAmountWei` is positive (`amount_not_configured`) and the paymaster address is non-zero (`paymaster_missing`).
3. If the governed top-up fee is above zero, the escrow fee destination account is set on the node (`treasury_missing`). The fee is `governance.ParamKeyPaymasterTopUpFeeWei`, default `0`.
4. The day's outflow plus the amount plus the fee does not exceed `DailyCapWei` (`daily_cap_exceeded`). The day is the UTC date of the block timestamp.
5. The cooldown has elapsed (`cooldown_active`).
6. `FundingAccount` is set (`funding_account_missing`).
7. `Minter` is set (`operator_missing`), and if `MinterRole` is non-empty it holds that role (`operator_role_missing`).
8. `Approver` is set and differs from `Minter` (both failures report `approver_missing`), and if `ApproverRole` is non-empty it holds that role (`approver_role_missing`).
9. The funding account holds at least the amount plus the fee (`funding_insufficient`).

On success the funding account is debited by amount plus fee, the paymaster is credited the amount, and any fee is credited to that account. If a later step of the same sponsored transaction fails (saving the account, recording paymaster usage), the top-up is reverted with `Rollback`.

The role checks are skipped when the corresponding role name is empty.

## Monitoring

- Event type `paymaster.autotopup` (`core/events/sponsorship.go`), emitted for each success and for each failure listed above. Attributes present when known: `paymaster`, `token`, `status` (`success` or `failure`), `reason`, `amountWei`, `balanceWei`, `feeWei` (only when a fee was taken) and `day`.
- Prometheus counters `nhb_paymaster_autotopups_total{outcome}` and `nhb_paymaster_autotopup_amount_wei_total{outcome}` (`observability/metrics.go`). The amount counter's help text says "minted"; the amount is transferred from the funding account.

## Turning it off

The only switch is `Enabled = false` in the node configuration, which takes effect when the node restarts with the new configuration. There is no runtime pause for this pathway in the code.
