# Fee operations runbook

Fee settings live under `[global.Fees]` in `config.toml`, and the node builds two
policies from them when it applies the global configuration at start
(`Node.SetGlobalConfig`, `core/node.go`). Use the Go spelling for key names (see
[Runtime Configuration Guardrails](./configuration.md#key-spelling-matters)).

## Configuration knobs

Defaults come from `defaultGlobalConfig` in `config/config.go`.

| Key | Meaning | Default |
| --- | ------- | ------- |
| `FreeTierTxPerMonth` | Free transactions per payer per UTC calendar month in the merchant-domain fee policy | `100` |
| `MDRBasisPoints` | Domain-level merchant discount rate once the free tier is used up | `150` |
| `OwnerWallet` | Address that receives routed fees for the domain policy | a built-in non-empty address |
| `Assets` (`[[global.Fees.Assets]]`: `Asset`, `MDRBasisPoints`, `OwnerWallet`) | Per-asset MDR and route wallet | `NHB` and `ZNHB`, each `150` bps with its own default wallet |
| `TransferFreeTierSpendWei` | Free-tier spend limit for the transfer policy, in wei; `0` disables the transfer policy | `1000000000000000000000` (1000 NHB at 18 decimals) |
| `TransferFreeTierWindow` | `lifetime` or `monthly` (anything else is rejected by validation; unrecognised values behave as `lifetime`) | `lifetime` |
| `TransferFeeCollector` | Address that receives transfer fees; when empty the node's escrow treasury address is used | empty |
| `TransferFeeBps` | Fee in basis points on an NHB transfer once the sender is past the free tier | `20` |
| `TransferFeeBpsZNHB` | Same for ZNHB transfers | `10` |

Validation: `TransferFreeTierSpendWei` must parse and be non-negative;
`TransferFreeTierWindow` must be empty, `lifetime` or `monthly`; if `ZNHB` is in
`Assets`, it needs a route wallet (`config/validate.go`). Those checks run at
`consensusd` start and in governance policy preflight (`native/gov/validate.go`).

## How the merchant-domain policy is applied

`applyTransactionFee` (`core/state_transition.go`) runs for NHB and ZNHB
transfers. It reads the transaction's `merchantAddr` field, lower-cases and trims
it, and looks it up as a fee domain. `buildFeePolicyFromConfig` defines three
domains from `[global.Fees]`: `pos`, `p2p` and `otc`. A transfer whose
`merchantAddr` is not one of those has no domain fee.

For a matching domain, `native/fees.Apply` counts the payer's transactions in the
current UTC month (`feeWindowStart`). While the count is below
`FreeTierTxPerMonth`, no fee is taken; the counter covers all assets together.
After that the fee is `value * MDRBasisPoints / 10000`, using the `MDRBasisPoints`
of the transferred asset's entry in `Assets` (an entry with `0` falls back to the
domain `MDRBasisPoints`). It is routed to that entry's `OwnerWallet` (falling
back to the domain `OwnerWallet`) and is never more than the transferred value.
An asset with no entry in `Assets` is not charged.

When a domain policy reaches `native/fees` without an explicit free-tier value
(`FreeTierTxPerMonthSet` false and `FreeTierTxPerMonth` 0), the node uses `100`
and logs `fees: domain <name> missing FreeTierTxPerMonth, defaulting to 100` once
per domain. Policies built from `[global.Fees]` always mark the value as set, so
this only applies to policies from other sources.

## ZNHB fees and the admin/treasury wallet

A ZNHB credit to, or debit from, the admin/treasury wallet that the ZNHB supply
invariant tracks (a transfer, a routed fee, an escrow release and the other
transaction types in `treasuryZNHBFlowTracked`) is booked into the Reward Pool
sub-ledger in the same state transition. A transaction that would move more ZNHB
off that wallet than the Reward Pool holds is rejected with
`znhb: treasury reward pool cannot cover this outflow`
(`core/znhb_treasury_pool.go`, `core/state_transition.go`).

## Monitoring

Each domain fee evaluation appends a `fees.applied` event
(`core/events/fees.go`). Its attributes include `payer`, `domain`, `asset`,
`grossWei`, `feeWei`, `netWei`, `policyVersion`, `ownerWallet` (hex, not bech32),
`freeTierApplied`, `freeTierLimit`, `freeTierRemaining`, `usageCount`,
`windowStartUnix` and `feeBps`.

- `freeTierRemaining` shows how many free transactions are left in the window.
- `windowStartUnix` is the Unix time of the first day of the UTC month; a new
  month starts a new window and the usage counter starts again.
- `ownerWallet` identifies the wallet the fee is routed to.

`ops/grafana/dashboards/fees.json` is a Grafana dashboard titled "NHB Fee Transparency".

## After changing the configuration

`[global.Fees]` is read when the node starts, so restart the nodes after editing
the file and confirm they load it (a malformed wallet stops startup with
`fees: invalid owner wallet` or `fees: invalid route wallet for <asset>`).
