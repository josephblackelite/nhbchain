# Base Spend Rewards

The base spend reward pays ZNHB to the sender of a qualifying NHB transfer. It is funded from the loyalty treasury account (a ZNHB balance moved to the spender; nothing is minted) and runs alongside business program rewards ([`loyalty.md`](./loyalty.md)). Code: `native/loyalty/engine_base.go`, `native/loyalty/global.go`, `native/loyalty/params.go`, `core/state_transition.go` (`QueuePendingBaseReward`, `EndBlockRewards`).

## When it runs

`Engine.ApplyBaseReward` runs after each successful native NHB transfer (`TxTypeTransfer`) that has a recipient. Context: from = sender, to = recipient, token `NHB`, amount = transfer value. It does not run for other transaction types. If the `loyalty` module pause flag is set, nothing runs and no events are produced.

## Rate and formula

* `reward = amount * baseBps / 10000`, integer division (`BaseRewardBpsDenominator` = 10000).
* `DefaultBaseRewardBps` is 50 (0.50%). `GlobalConfig.Normalize` applies it whenever the stored `BaseBps` is zero. `BaseBps` cannot exceed 10000 (`Validate`).
* Example: a 20,000 wei payment earns `20000 * 50 / 10000` = 100 wei of ZNHB.
* The reward is then clamped in this order: `CapPerTx` (when greater than zero), the sender's remaining `DailyCapUser` for the UTC day, and the remaining `DailyCapCounterparty` budget for the sender/recipient pair for the UTC day. A zero cap disables that check.

## Configuration (`loyalty.GlobalConfig`)

The configuration is one record in state, written by the genesis loader from the `loyaltyGlobal` object of the genesis file (`core/genesis/spec.go`, `LoyaltyGlobalSpec`). Nothing changes it afterwards. All amounts are decimal strings in wei (18 decimals).

| Genesis field | Description |
|---------------|-------------|
| `active` | Enables base rewards. When false every payment emits `loyalty.base.skipped` with reason `inactive`. |
| `treasury` | Bech32 account that funds rewards (required). |
| `baseBps` | Rate in basis points; 0 selects the default of 50. |
| `minSpend` | Minimum NHB amount that qualifies. |
| `capPerTx` | Maximum ZNHB reward per payment. |
| `dailyCapUser` | Maximum ZNHB reward per sender per UTC day. |
| `dailyCapCounterparty` | Maximum ZNHB reward per unordered address pair per UTC day (A to B and B to A share one budget). Omitted or 0 disables it. |
| `seedZNHB` | When greater than 0, the genesis loader sets the treasury's ZNHB genesis allocation to this amount (`core/genesis/loader.go`). |
| `dynamic` | Daily budget and pro-rating settings, see [`policy.md`](./policy.md). |

The genesis files under `config/` (`genesis.relaunch.json`, the live network's genesis and the embedded default; `genesis.phase-e.json`; `genesis.local.json`) set `active` true, `baseBps` 50, `minSpend` 1 NHB (1e18), `capPerTx` 50 ZNHB (50e18), `dailyCapUser` 200 ZNHB (200e18) and no `dailyCapCounterparty`.

## Skip reasons

Each skip emits `loyalty.base.skipped` with a `reason`. In evaluation order: `config_error`, `inactive`, `missing_from_account`, `token_not_supported` (token is not NHB), `invalid_addresses`, `self_transfer`, `amount_not_positive`, `below_min_spend`, `no_reward_rate`, `reward_zero`, `daily_cap_reached`, `counterparty_daily_cap_reached`, `meter_error`, `treasury_error`, `treasury_insufficient` (treasury ZNHB balance below the reward).

## Settlement (pro-rating)

After the checks above the reward is added to the block's pending queue (`QueuePendingBaseReward`) and `loyalty.reward.proposed` is emitted. The per-user, per-pair and lifetime base meters and the accrual record are written at this point with the full proposed amount, and `loyalty.base.accrued` is emitted with that proposed amount.

The actual credit happens at the end of the block (`EndBlockRewards`) when the stored `dynamic.EnableProRate` is true, which is the default and the only value the genesis loader can produce: the day's remaining budget is compared with the queued demand and every reward is scaled by `min(1, budget / demand)`. Details and events are in [`policy.md`](./policy.md). Each payout is also capped by the treasury's remaining balance. The recipient of the credit is the sender (the spender).

If `EnableProRate` is false in the stored configuration, each reward is credited immediately (`settleBaseRewardImmediate`), limited only by the treasury balance.

`EndBlockRewards` books the net movement of the ZNHB Reward Pool for the whole payout step on every exit path, in the same state transition (`captureTreasuryPoolPosition` / `bookTreasuryPoolMovement`, `core/state_transition.go`, `core/znhb_treasury_pool.go`). This matters when the configured loyalty treasury is the node's admin/treasury wallet, whose ZNHB the pool ledger must always account for. It is not a transaction, so a shortfall in the Reward Pool is drawn from the Sale Pool instead of failing the block. If the spender is the treasury itself, the payout is credited on the treasury's own loaded account object so it nets to zero rather than overwriting the debit.

Because the accrued event and the meters record the proposed amount, the ZNHB actually received can be lower than `reward` in `loyalty.base.accrued` when pro-rating applies or the treasury runs low.

## Event `loyalty.base.accrued`

| Attribute | Description |
|-----------|-------------|
| `day` | UTC day, `YYYY-MM-DD`. |
| `token` | `NHB`. |
| `amount` | Spend amount (wei). |
| `from`, `to` | Lowercase hex addresses, no `0x`. |
| `reward` | Proposed ZNHB reward (wei). |
| `baseBps` | Basis points used. |

## Operations

* Keep the treasury account funded with ZNHB: rewards are skipped with `treasury_insufficient` when the balance is below one reward.
* The `loyalty` pause flag (`[global.Pauses] Loyalty`) stops rewards without producing events.
