# Paymaster Top-up Controls

The paymaster auto top-up moves one asset, `NHB` by default or `ZNHB` when the policy names it, from a configured funding account to a paymaster account when the paymaster's balance of that asset is below a floor. It transfers existing balance; it does not mint. The logic is `maybeAutoTopUpPaymaster` in `core/sponsorship.go`, and it runs while a sponsored transaction is processed (`core/state_transition.go`): on the native transfer path after the sponsor's gas cost for that transaction has been deducted, and on the EVM path after the transaction has executed.

The asset is chosen once, from the policy: the balance check, the funding debit, the paymaster credit and the fee credit all use it (`topUpAssetBalance`, `setTopUpAssetBalance`). `NHB` is the default because sponsored gas is paid from the sponsor's NHB balance (`paymasterSponsoredAsset`).

## Configuration

The policy is read from the `[global.Paymaster.AutoTopUp]` table (and its `Governance` sub-table) in the node configuration and parsed by `Global.PaymasterAutoTopUpConfig` in `config/global.go`.

| Key | Default | Meaning |
| --- | --- | --- |
| `Enabled` | `false` | The pathway does nothing unless this is true. |
| `Token` | `"NHB"` | `NHB` or `ZNHB` (case-insensitive; empty means `NHB`); any other value is a configuration error (`Global.PaymasterAutoTopUpConfig`, `config/global.go`). |
| `MinBalanceWei` | `"0"` | Floor. A top-up is considered only when the paymaster's balance of the top-up asset is below it; a floor of zero disables it. |
| `TopUpAmountWei` | `"0"` | Amount credited to the paymaster. |
| `DailyCapWei` | `"0"` | Cap on the funding account's total daily outflow for this paymaster, in the top-up asset. |
| `CooldownSeconds` | `0` | Minimum time between top-ups for one paymaster; zero means no cooldown. |
| `Governance.FundingAccount` | empty | Account debited. |
| `Governance.Minter` | empty | The execution operator. Field name in code: `Minter`. |
| `Governance.Approver` | empty | The approver. |
| `Governance.MinterRole` | `ROLE_PAYMASTER_AUTOFUND` | Role the operator must hold. An empty value in the configuration is replaced by this default at load time (`config/config.go`). |
| `Governance.ApproverRole` | `ROLE_PAYMASTER_ADMIN` | Role the approver must hold. An empty value in the configuration is replaced by this default at load time. |

Defaults are in `config/config.go` (`Paymaster.AutoTopUp`). When `Enabled` is true, `DailyCapWei` must be positive: configuration parsing returns an error otherwise, and `maybeAutoTopUpPaymaster` returns an error if it is ever handed a policy with a zero cap.

## Execution rules

A top-up runs only if all of the following hold; each check that fails after the balance test emits a failure event with the listed `reason` and changes nothing:

1. The policy is enabled, the token is `NHB` or `ZNHB`, `MinBalanceWei` is positive, and the paymaster's balance of the top-up asset is below it. (Otherwise there is no event.)
2. `TopUpAmountWei` is positive (`amount_not_configured`) and the paymaster address is non-zero (`paymaster_missing`).
3. If the governed top-up fee is above zero, the escrow fee destination account is set on the node (`treasury_missing`). The fee is `governance.ParamKeyPaymasterTopUpFeeWei`, default `0`.
4. The day's outflow plus the amount plus the fee does not exceed `DailyCapWei` (`daily_cap_exceeded`). The day is the UTC date of the block timestamp.
5. The cooldown has elapsed (`cooldown_active`).
6. `FundingAccount` is set (`funding_account_missing`).
7. The funding account is not the paymaster and, on the native transfer path, is not the sender or the recipient of the transaction being processed (`funding_account_in_use`), and, when a fee is taken, the escrow fee destination account is not the sender or the recipient either (`treasury_account_in_use`). The caller writes the sender and recipient objects after the top-up, and its write would overwrite the top-up's, so the top-up refuses to run instead of losing the debit.
8. `Minter` is set (`operator_missing`), and if `MinterRole` is non-empty it holds that role (`operator_role_missing`).
9. `Approver` is set and differs from `Minter` (both failures report `approver_missing`), and if `ApproverRole` is non-empty it holds that role (`approver_role_missing`).
10. The funding account holds at least the amount plus the fee (`funding_insufficient`).

On success the funding account is debited by amount plus fee, the paymaster is credited the amount, and any fee is credited to the escrow fee destination account (which may be the funding account or the paymaster itself; each address is loaded once and written once). If a later step of the same sponsored transaction fails (saving the account, recording paymaster usage), the top-up is reverted with `Rollback`.

The role checks are skipped when the policy's role name is empty. Through the node configuration a role name cannot be empty, because an empty value is replaced by its default at load time.

## Monitoring

- Event type `paymaster.autotopup` (`core/events/sponsorship.go`), emitted for each success and for each failure listed above. Attributes present when known: `paymaster`, `token`, `status` (`success` or `failure`), `reason`, `amountWei`, `balanceWei`, `feeWei` (only when a fee was taken) and `day`. `token` is `NHB` or `ZNHB`.
- Prometheus counters `nhb_paymaster_autotopups_total{outcome}` and `nhb_paymaster_autotopup_amount_wei_total{outcome}` (`observability/metrics.go`). The amount counter's help text says "minted"; the amount is transferred from the funding account.

## Turning it off

The only switch is `Enabled = false` in the node configuration, which takes effect when the node restarts with the new configuration. There is no runtime pause for this pathway in the code.
