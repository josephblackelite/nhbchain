# Loyalty Paymaster Funding and Caps

Business loyalty programs pay rewards from the ZNHB balance of the business's **paymaster** address (`Business.Paymaster`, set with `TxTypeLoyaltySetPaymaster`; see [`loyalty.md`](./loyalty.md)). This page describes how the engine treats that balance and the program caps. Code: `native/loyalty/engine_program.go`, `native/loyalty/types.go`.

## Funding

* Setting a paymaster only stores an address on the business. Funding is an ordinary ZNHB transfer to that address.
* At accrual time the engine requires a paymaster (`paymaster_missing` otherwise) whose ZNHB balance is at least the reward (`paymaster_insufficient` otherwise). A skipped reward never blocks the NHB transfer that triggered it.
* `loyalty_paymasterBalance {businessId}` returns the paymaster address's current ZNHB balance as a decimal string (`"0"` when no paymaster is set). It does not report any reserved or pending amount.
* An owner can have a paymaster on only one business at a time (`loyalty: paymaster already assigned`).

## Reserve check (not reachable today)

`Business` has a `PaymasterReserveMin` field, and `ApplyProgramReward` uses it when it is greater than zero:

* If `balance - reward` would fall below `PaymasterReserveMin`, the reward is skipped with reason `throttled — low reserve` (attributes `available`, `reserveMin`).
* If `balance - reward` is at or below `PaymasterReserveMin * 120 / 100` (that is, within 20% above the reserve), the engine emits `loyalty.program.paymaster_warning` with `balance` (the projected balance) and `reserveMin` in addition to the standard program attributes.

**No transaction, RPC method or genesis setting in this repository writes `PaymasterReserveMin`.** It is always unset (nil), so the reserve check and the warning event never fire. Only the plain balance checks above are in effect.

## Program caps

| Field | Effect |
|-------|--------|
| `capPerTx` | Clamps a single reward. |
| `dailyCapUser` | Maximum reward per sender per UTC day; the reward is clamped to the remaining allowance. |
| `dailyCapProgram` | Maximum total reward per UTC day across all senders. |
| `epochCapProgram` with `epochLengthSeconds` | Maximum total reward per epoch window (`timestamp / epochLengthSeconds`). |
| `issuanceCapUser` | Lifetime maximum reward per sender for the program. |

Caps are applied in that order after the reward is computed, and each one clamps the reward to what remains. When no allowance remains the accrual is skipped with `daily_cap_reached`, `daily_program_cap_reached`, `epoch_cap_reached` or `issuance_cap_reached`. A cap of zero disables it, except that a program must have a positive `dailyCapProgram` or `epochCapProgram` (`loyalty: invalid program` otherwise). `epochLengthSeconds` must be positive whenever `epochCapProgram` is positive.

## Tokens

Spend must be in `NHB` (`token_not_supported` otherwise) and the program's token must be `ZNHB` (`reward_token_not_supported` otherwise).

## Observability

* `loyalty.program.skipped` carries `reason` plus context (`available`, `reserveMin`, `dailyCap`, `epochCap`, `epoch`, `issuanceCap` depending on the reason).
* Meters written on every accrual: per-sender per-day, program per-day total, program per-day count, program per-epoch total (only when an epoch cap is set), per-sender lifetime total (only when an issuance cap is set), and a program lifetime total.
* Exposed by RPC: the program's daily total, daily count and lifetime total via `loyalty_programStats`, the per-sender daily total via `loyalty_userDaily`, and individual accruals via `loyalty_listAccruals`. The epoch and per-sender lifetime meters are not exposed by any RPC method.
