# NHBChain Loyalty Engine: Developer Guide

This guide describes the loyalty module as implemented in `native/loyalty`, the transaction handlers in `core/state_transition.go`, the RPC handlers in `rpc/loyalty_handlers.go` and the CLI in `cmd/nhb-cli`. Related pages: [`payouts.md`](./payouts.md) (base reward), [`paymaster.md`](./paymaster.md), [`policy.md`](./policy.md) (daily budget and pro-rating), [`rewards.md`](./rewards.md) (epoch emissions, a separate mechanism).

## 1. Overview

Loyalty pays **ZNHB** to a user (the spender) when the user pays **NHB**. There are two independent mechanisms, both evaluated by `Engine.OnTransactionSuccess` (`native/loyalty/loyalty.go`):

1. **Base reward.** A chain-wide reward funded from the loyalty treasury account configured in the stored `GlobalConfig`.
2. **Business program reward.** A reward defined by a merchant's program and funded from the paymaster address of the merchant's business.

**What triggers them.** `OnTransactionSuccess` is called from the state processor after a successful native NHB transfer (`TxTypeTransfer`, both execution paths in `core/state_transition.go`) that has a recipient. The context is: from = the sender, to = the recipient, token `NHB`, amount = the transfer value. Nothing else calls it: escrow releases, POS captures, ZNHB transfers and mints do not trigger loyalty rewards. Both engines require the spend token to be `NHB`; both pay ZNHB.

The engine is skipped entirely when the `loyalty` module pause flag is set (`nativecommon.Guard`, `[global.Pauses] Loyalty` in `config.toml`).

Every outcome of an evaluation is an event: a success emits `loyalty.base.accrued` / `loyalty.program.accrued`, and every non-qualifying case emits `loyalty.base.skipped` / `loyalty.program.skipped` with a `reason` attribute. Because the engine runs for every qualifying NHB transfer, a transfer whose recipient is not a merchant with a program emits a skip event with reason `program_not_found` (or `program_lookup_error`, `business_not_found`, ...).

## 2. Roles and identifiers

| Concept | Description |
|---------|-------------|
| **Business** | Registered by an owner address (`RegisterBusiness`). ID is a 32-byte value minted from a global counter: the counter, big-endian, in the last 8 bytes (first business is `0x00...01`). You cannot predict your ID; look it up with `loyalty_listBusinesses`. |
| **Merchant** | An address added to a business. An address can belong to at most one business (`loyalty: merchant already assigned`). A business's owner is not a merchant unless added explicitly. |
| **Program** | A reward configuration created by a merchant of the business. The 32-byte program ID is chosen by the client. The program's owner is the transaction sender. |
| **Paymaster** | An address set on the business; its ZNHB balance pays program rewards. Setting it moves no funds. One owner can have a paymaster on only one business at a time (`loyalty: paymaster already assigned`). Because rewards are debited from the paymaster's own balance and the named wallet does not sign the assignment, a wallet can be named only by itself or with its recorded opt-in; see section 5. |
| **`ROLE_LOYALTY_ADMIN`** | On-chain role (`RoleLoyaltyAdmin`) that may act on any business or program in place of the owner. Granted through the role allowlist governance flow (`config.toml` `AllowedRoles`). |

A program pays the merchants' customers: at accrual time the engine finds the recipient of the transfer (the merchant), takes the first program owned by that merchant address (programs indexed by owner, sorted by ID) that is active for the current time, and finds the merchant's business to get the paymaster.

## 3. Base reward

Implemented in `native/loyalty/engine_base.go` (`ApplyBaseReward`). Details: [`payouts.md`](./payouts.md).

* Pays `amount * baseBps / 10000` ZNHB to the sender, taken from the treasury. `baseBps` defaults to 50 (0.50%) when zero.
* Clamped by `capPerTx`, `dailyCapUser` (per sender, per UTC day) and `dailyCapCounterparty` (per unordered address pair, per UTC day).
* Skipped when: the config is inactive, the token is not NHB, sender equals recipient (`self_transfer`), the amount is below `minSpend`, or the treasury holds less than the reward.
* Configuration is stored on-chain in the `GlobalConfig` record and is written only by the genesis loader (`loyaltyGlobal` in the genesis file). There is no transaction or RPC that changes it after genesis.

## 4. Business programs

### 4.1 Fields

`loyalty.Program` (`native/loyalty/types.go`) and the transaction payload (`loyaltyProgramPayload`):

| Payload field | Program field | Meaning |
|---------------|---------------|---------|
| `id` | `ID` | 32-byte hex program ID. Required on create. |
| `businessId` | | 32-byte hex business ID. Create only. |
| `pool` | `Pool` | Bech32 address, required, stored and echoed in events. The accrual engine does not read it: rewards are paid from the business paymaster. |
| `tokenSymbol` | `TokenSymbol` | Must be a registered token; accrual only pays programs whose token is `ZNHB` (otherwise `reward_token_not_supported`). |
| `rewardMode` | `RewardMode` | `"bps"` (default) or `"fixed"`. |
| `accrualBps` | `AccrualBps` | Reward = `spend * accrualBps / 10000` in bps mode. Maximum 100000. |
| `fixedRewardWei` | `FixedRewardWei` | Flat reward per qualifying payment in fixed mode; must be positive in fixed mode. |
| `minSpendWei` | `MinSpendWei` | Minimum NHB payment that qualifies. |
| `capPerTx` | `CapPerTx` | Maximum reward for one payment (reward is clamped to it). |
| `dailyCapUser` | `DailyCapUser` | Maximum reward per sender per UTC day. |
| `dailyCapProgram` | `DailyCapProgram` | Maximum total reward per UTC day across all senders. |
| `epochCapProgram`, `epochLengthSeconds` | `EpochCapProgram`, `EpochLengthSeconds` | Maximum total reward per epoch window; `epochLengthSeconds` must be greater than zero when an epoch cap is set. Epoch key = `timestamp / epochLengthSeconds`. |
| `issuanceCapUser` | `IssuanceCapUser` | Lifetime maximum reward per sender for this program. |
| `startTime`, `endTime` | `StartTime`, `EndTime` | Unix seconds; `0` means unbounded; `endTime` must not be earlier than `startTime` when set. |
| `active` | `Active` | Defaults to true when omitted. |

All amounts are decimal strings in wei (18 decimals); omitted amounts are zero, and zero caps mean "no such cap". There is no `owner`, `includeP2P` or `metadata` field.

**Anti-sybil requirement** (`sanitizeProgram`): every create and update is rejected with `loyalty: invalid program` unless `dailyCapProgram` or `epochCapProgram` is greater than zero.

### 4.2 Accrual order (`ApplyProgramReward`)

For each qualifying NHB transfer:

1. Resolve the program and business (skip reasons `merchant_missing`, `program_not_found`, `business_not_found`, ...). While scanning the merchant's programs (`LoyaltyProgramsByOwner`), `resolveProgram` passes over any program that is paused or outside `startTime`/`endTime` and uses the first one that is active in the window. If none qualifies, the skip reason is `program_not_found`: a paused, not-yet-started or ended program is reported as `program_not_found`, not as a distinct reason. The reasons `program_inactive`, `not_started` and `program_ended` exist in `ApplyProgramReward` but are only reachable when the caller sets `ProgramRewardContext.ProgramHint`, and nothing outside `native/loyalty/engine_program.go` and its tests sets it, so the transfer path never emits them.
2. Spend token must be `NHB` (`token_not_supported`); program token must be `ZNHB` (`reward_token_not_supported`); amount at least `minSpendWei` (`below_min_spend`).
3. Compute the reward: fixed mode uses `fixedRewardWei`; bps mode uses `amount * accrualBps / 10000` (`no_reward_rate` when zero, `reward_zero` when it rounds to zero).
4. Clamp in this order: `capPerTx`, remaining `dailyCapUser`, remaining `dailyCapProgram`, remaining epoch cap, remaining `issuanceCapUser`. If a remaining allowance is zero the accrual is skipped (`daily_cap_reached`, `daily_program_cap_reached`, `epoch_cap_reached`, `issuance_cap_reached`).
5. The business must have a paymaster (`paymaster_missing`) whose ZNHB balance covers the reward (`paymaster_insufficient`). See [`paymaster.md`](./paymaster.md) for the reserve check.
6. The reward moves from the paymaster to the sender and both account writes are persisted by the engine itself (`ApplyProgramReward` loads each address once; if the paymaster is the sender the move nets to zero). If loading or storing the sender's account fails the debit is undone and the skip reason is `recipient_error` or `recipient_persist_error`; a failed write of the paymaster account is `paymaster_persist_error`. Then the meters are updated, an accrual record is appended, and `loyalty.program.accrued` is emitted. Settlement of the NHB transfer itself never depends on this outcome. The engine is handed a state view (`loyaltyRewardState`, `core/state_transition.go`) that, when the paymaster or the sender is the node's admin/treasury wallet, moves the ZNHB Reward Pool by the same amount as that account write, so the pool ledger stays equal to the wallet's ZNHB; a pool that cannot cover it refuses the write.

Program rewards are credited immediately; they do not go through the pro-rating queue.

Meters (`core/state`, keyed by program): per-sender per-day total, program per-day total, per-day accrual count, program per-epoch total, per-sender lifetime total, program lifetime total, and a per-day list of accrual records.

## 5. Transactions

Administration is done with signed native transactions submitted through `nhb_sendTransaction`. The loyalty write RPC methods (`loyalty_createBusiness`, `loyalty_setPaymaster`, `loyalty_addMerchant`, `loyalty_removeMerchant`, `loyalty_createProgram`, `loyalty_updateProgram`, `loyalty_pauseProgram`, `loyalty_resumeProgram`) are disabled and return HTTP 410 with code `-32060` (`loyaltyRPCDisabledMessage`). The transaction sender is the acting party; there are no `caller` or `owner` payload fields. `tx.Data` is JSON (RLP also accepted, `decodeCreateEscrowPayload`). Transactions return no result; read the outcome with the read methods or the events.

| Type | Value | `tx.Data` | Authorization |
|------|-------|-----------|---------------|
| `TxTypeCreateLoyaltyBusiness` | `0x42` | `{"name": "..."}` | Anyone; the sender becomes the owner. Name must not be empty. Subject to the `loyalty` quota. |
| `TxTypeLoyaltySetPaymaster` | `0x43` | `{"businessId": "0x...", "paymaster": "nhb1..."}` (omit `paymaster` to clear) | See the paymaster rules below. Emits `loyalty.paymaster.rotated` when the paymaster changes. |
| `TxTypeLoyaltyAddMerchant` | `0x44` | `{"businessId": "0x...", "merchant": "nhb1..."}` | Business owner or `ROLE_LOYALTY_ADMIN` (checked by the handler). |
| `TxTypeLoyaltyRemoveMerchant` | `0x45` | same | same; fails with `loyalty: merchant not found` if the address is not a merchant of that business. |
| `TxTypeCreateLoyaltyProgram` | `0x46` | program payload (section 4.1) | Sender must be a merchant of `businessId` or hold `ROLE_LOYALTY_ADMIN`. The sender becomes the program owner. Subject to the `loyalty` quota. Emits `loyalty.program.created`. |
| `TxTypeUpdateLoyaltyProgram` | `0x47` | program payload; `id` required | Existing program's owner or `ROLE_LOYALTY_ADMIN`. Full replace of the mutable fields; `id` and owner cannot change. Emits `loyalty.program.updated`. |
| `TxTypePauseLoyaltyProgram` | `0x48` | `{"id": "0x..."}` | Program owner or `ROLE_LOYALTY_ADMIN`. Idempotent. Emits `loyalty.program.paused` when the state changes. |
| `TxTypeResumeLoyaltyProgram` | `0x49` | `{"id": "0x..."}` | same; emits `loyalty.program.resumed`. |

**Paymaster rules** (`Registry.SetPaymaster`, `native/loyalty/registry_business.go`; the handler is `applyLoyaltySetPaymaster`). The named wallet never signs the assignment, so:

* A business owner (without `ROLE_LOYALTY_ADMIN`) may name only its own wallet, or clear the paymaster. Naming any other wallet fails with `loyalty: paymaster must be the caller's own wallet unless assigned by a loyalty admin`.
* A `ROLE_LOYALTY_ADMIN` holder may name another wallet only if that wallet is the business owner or has recorded an opt-in for this business. Otherwise the transaction fails with `loyalty: paymaster has not consented`.
* Recording an opt-in: any sender that is neither the owner nor an admin sends `TxTypeLoyaltySetPaymaster` with its own address as `paymaster` and the business ID. That transaction succeeds, stores the opt-in (per business) and emits no event; it does not change the business. The opt-in is consumed by the assignment it authorizes.
* Any other sender, or an owner-less clear from a stranger, fails with `loyalty: unauthorized`.

Removing a merchant stops future accruals through that merchant; rewards already paid are not reversed.

### Sample create-program payload

```json
{
  "businessId": "0x0000000000000000000000000000000000000000000000000000000000000001",
  "id": "0x<64 hex characters chosen by the client>",
  "pool": "nhb1...",
  "tokenSymbol": "ZNHB",
  "rewardMode": "bps",
  "accrualBps": 500,
  "minSpendWei": "100000000000000000",
  "capPerTx": "5000000000000000000",
  "dailyCapUser": "10000000000000000000",
  "dailyCapProgram": "500000000000000000000",
  "startTime": 1730400000,
  "endTime": 1762032000,
  "active": true
}
```

## 6. Read RPC methods

Requests use one parameter object: `{"jsonrpc":"2.0","id":1,"method":"loyalty_listPrograms","params":[{"businessId":"0x..."}]}`. None of these methods require authentication. IDs are 32-byte hex, with or without `0x`.

| Method | Params | Result |
|--------|--------|--------|
| `loyalty_getBusiness` | `{"businessId"}` | `{id, owner, name, paymaster, merchants[]}` (addresses bech32; `paymaster` empty when unset). |
| `loyalty_listBusinesses` | `{"owner": "nhb1..."}` | Array of business objects owned by the address, sorted by ID. |
| `loyalty_listPrograms` | `{"businessId"}` | Programs owned by the business's merchants, sorted by ID: `id, owner, pool, tokenSymbol, rewardMode, accrualBps, fixedRewardWei, minSpendWei, capPerTx, dailyCapUser, dailyCapProgram, epochCapProgram, epochLengthSeconds, issuanceCapUser, startTime, endTime, active`. No pagination parameters. |
| `loyalty_programStats` | `{"programId", "day"}` (`day` = `YYYY-MM-DD`, UTC) | `{rewardsPaid, txCount, capUsage, lifetimeRewardsPaid}`, all strings. `rewardsPaid` is the program's total for that day; `txCount` counts accruals that day; `capUsage` is `rewardsPaid / dailyCapProgram` to 4 decimals, or `null` when the program has no `dailyCapProgram`; `lifetimeRewardsPaid` is the never-reset total. Unknown program: HTTP 404 with code `-32602`, message `program not found`. Days recorded before these meters were always written read as `"0"`. |
| `loyalty_listAccruals` | `{"programId", "day"}` | Array of `{programId, address, amount, kind, txHash, timestamp}`, one per accrual that day. |
| `loyalty_userDaily` | `{"user": "nhb1...", "programId", "day"}` | Decimal string: what the user accrued from the program that day. |
| `loyalty_paymasterBalance` | `{"businessId"}` | Decimal string: the paymaster address's ZNHB balance, `"0"` if none is set. |
| `loyalty_resolveUsername` | `{"username"}` | Bech32 address; HTTP 404 with `-32602` when unknown. |
| `loyalty_userQR` | `{"address"}` or `{"username"}` | `{"address": "nhb1...", "payload": "nhb:nhb1..."}`. |

`nhb_getLoyaltyBudgetStatus` (no parameters; `handleGetLoyaltyBudgetStatus`, `rpc/explorer_handlers.go`) reports the chain-wide base-reward budget from the loyalty engine's own state (`Node.LoyaltyBudgetStatus`): `budgetRemaining` (wei of ZNHB left of today's budget), `paidToday`, `proposedToday` (wei), `day` (`YYYYMMDD`, UTC), `resetAt` (Unix seconds of the next UTC midnight), `twapScalingFactor` (paid divided by proposed as a decimal string with at most six decimals, `"1.0"` while nothing was cut; the name is historical) and `guardFallback` only while the price guard is using a fallback price. It is not split per merchant.

Error codes: `-32602` for invalid or unknown inputs (including 404 "business not found"), `-32000` for internal failures, `-32060` for the disabled write methods.

## 7. CLI

`nhb-cli` (`cmd/nhb-cli/main.go`). The RPC endpoint defaults to `http://localhost:8080`, or the `RPC_URL` environment variable, or the `--rpc <url>` flag. Every command that fails (bad usage, load error, RPC error) exits non-zero. Commands that send a transaction (`sendTransaction` in `cmd/nhb-cli/main.go` makes an authenticated call) fail with `privileged RPC call requires NHB_RPC_TOKEN to be set` unless the `NHB_RPC_TOKEN` environment variable holds a bearer token; `nhb-cli rpc-token` prints one when run on the node host with `NHB_RPC_JWT_SECRET` set. The read commands call the RPC without a token. A sent transaction is only queued; read the outcome afterwards. Write commands sign with the key file given as the **last** argument; there is no caller or owner argument.

```bash
nhb-cli loyalty-create-business <name> <key_file>
nhb-cli loyalty-list-businesses <owner>                       # find the assigned businessId
nhb-cli loyalty-set-paymaster <businessId> <paymaster> <key_file>   # an owner may name only its own address; see section 5
nhb-cli loyalty-add-merchant <businessId> <merchant> <key_file>
nhb-cli loyalty-remove-merchant <businessId> <merchant> <key_file>
nhb-cli loyalty-create-program <businessId> '<programSpecJSON>' <key_file>   # generates "id" if the spec has none
nhb-cli loyalty-update-program '<programSpecJSON with "id">' <key_file>       # full replace
nhb-cli loyalty-pause-program <programId> <key_file>
nhb-cli loyalty-resume-program <programId> <key_file>
nhb-cli loyalty-get-business <businessId>
nhb-cli loyalty-list-programs <businessId>
nhb-cli loyalty-program-stats <programId> <day>
nhb-cli loyalty-user-daily <user> <programId> <day>
nhb-cli loyalty-paymaster-balance <businessId>
nhb-cli loyalty-resolve-username <username>
nhb-cli loyalty-user-qr <username|address> <value>
```

Transactions are sent with gas limit 50000 and gas price 1.

## 8. Events

Attribute values are strings. Addresses in events are lowercase hex without `0x`.

| Event | Attributes |
|-------|------------|
| `loyalty.program.created` | `id`, `owner`, `pool`, `tokenSymbol`, `accrualBps`, `rewardMode` (0 = bps, 1 = fixed), `fixedRewardWei` |
| `loyalty.program.updated` | `id`, `active`, `accrualBps`, `rewardMode`, `fixedRewardWei`, `minSpendWei`, `capPerTx`, `dailyCapUser`, `dailyCapProgram`, `epochCapProgram`, `epochLengthSeconds`, `issuanceCapUser`, `startTime`, `endTime`, `pool`, `tokenSymbol` |
| `loyalty.program.paused`, `loyalty.program.resumed` | `id`, `owner`, `caller` |
| `loyalty.paymaster.rotated` | `businessId`, `owner`, `caller`, `oldPaymaster`, `newPaymaster` |
| `loyalty.program.accrued` | `day`, `token`, `amount`, `from`, `to`, `programId`, `rewardMode`, `accrualBps` (bps mode) or `fixedRewardWei` (fixed mode), `rewardToken`, `programOwner`, `paymaster`, `merchant`, `reward` |
| `loyalty.program.skipped` | the same attributes as `accrued` where known, plus `reason` and reason-specific extras (for example `dailyCap`, `available`) |
| `loyalty.program.paymaster_warning` | program attributes plus `balance`, `reserveMin` (see [`paymaster.md`](./paymaster.md)) |
| `loyalty.base.accrued` | `day`, `token`, `amount`, `from`, `to`, `reward`, `baseBps` |
| `loyalty.base.skipped` | `day`, `token`, `amount`, `from`, `to`, `reason`, plus extras |
| `loyalty.reward.proposed` | `tx_hash`, `amount` (base reward queued) |
| `loyalty.budget.prorated`, `loyalty.price.fallback`, `loyalty.smoothing.tick` | see [`policy.md`](./policy.md) |

`RegisterBusiness` and `AddMerchantAddress`/`RemoveMerchantAddress` emit no event.

Events are held by the node for the block that produced them and are not a persistent store. The durable record is the accrual index behind `loyalty_listAccruals` and the meters behind `loyalty_programStats` and `loyalty_userDaily`.

## 9. Configuration and limits

* **Pause:** `[global.Pauses] Loyalty` (`config.toml`).
* **Quota:** `[global.Quotas.Loyalty]` (`MaxRequestsPerMin`; the sample `config.toml` sets 6000) gates `TxTypeCreateLoyaltyBusiness` and `TxTypeCreateLoyaltyProgram` only. The other loyalty transactions are not quota-gated.
* **Base reward and dynamic policy:** see [`payouts.md`](./payouts.md) and [`policy.md`](./policy.md).
* **Governance:** the parameters `loyalty.dynamic.*` are accepted governance parameter names (`native/governance`); see [`policy.md`](./policy.md) for what actually reads the loyalty configuration at run time.

## 10. Errors

Registry errors (`native/loyalty/errors.go`) appear as transaction failures: `loyalty: unauthorized`, `loyalty: program already exists`, `loyalty: program not found`, `loyalty: invalid program`, `loyalty: immutable field`, `loyalty: token not registered`, `loyalty: accrual bps too high`, `loyalty: business not found`, `loyalty: invalid business`, `loyalty: paymaster already assigned`, `loyalty: paymaster has not consented`, `loyalty: paymaster must be the caller's own wallet unless assigned by a loyalty admin`, `loyalty: merchant already assigned`, `loyalty: merchant not found`. Handler-level failures use messages such as `loyaltyCreateProgram: unauthorized: caller is not a registered merchant of the business and lacks ROLE_LOYALTY_ADMIN`.

**Troubleshooting**

* Program does not apply: check that the merchant address (the transfer recipient) is a merchant of the business, owns an active program inside its `startTime`/`endTime`, and that the business has a paymaster with a sufficient ZNHB balance. Only the first eligible program of a merchant is evaluated.
* Look at `loyalty.program.skipped` / `loyalty.base.skipped` events and their `reason`. A program that is paused or outside its `startTime`/`endTime` window shows up as `program_not_found`.
* Module paused: `go run ./examples/docs/ops/read_pauses` shows the pause flags.
