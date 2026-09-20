# Fee policy

The chain has two independent fee mechanisms, both configured from the
`[global.Fees]` block of the node config (see
[fee configuration](../governance/fee-params.md)). Neither is adjustable by a
governance proposal.

## 1. Protocol transfer fee (`TransferGasPolicy`)

Source: `core/transfer_gas_policy.go`, and the NHB and ZNHB transfer paths in
`core/state_transition.go`.

Applies to `TxTypeTransfer` (NHB) and `TxTypeTransferZNHB` (ZNHB).

- **Free tier.** Each wallet has a tracked spend per asset (NHB and ZNHB are
  tracked separately), summed from the `Value` of its transfers, in a window
  that is `lifetime` or `monthly` (UTC calendar month) according to
  `TransferFreeTierWindow`. A transfer is fee-free while the wallet's tracked
  spend before that transfer is below `TransferFreeTierSpendWei`
  (default 1,000 tokens, `1000000000000000000000` wei). The transfer that takes
  the tracked spend past the limit is still free; afterwards every transfer is
  charged. The spend is recorded after each transfer
  (`TransferGasSpendAdd`, `core/state/transfer_gas.go`).
- **Fee.** Once the free tier is used up the fee is
  `floor(value * bps / 10000)` in the transferred asset, with `bps` =
  `TransferFeeBps` for NHB (default `20`, 0.20%) and `TransferFeeBpsZNHB` for
  ZNHB (default `10`, 0.10%). The sender pays the fee on top of `value`; a
  sender whose balance cannot cover `value + fee` fails with an insufficient
  balance error.
- **Collector.** The fee is credited to `TransferFeeCollector`, or to the
  node's escrow fee treasury address when that is empty. If the collector is
  the chain's admin wallet, a ZNHB fee is also added to the ZNHB Reward Pool
  ledger, and a ZNHB transfer sent from the admin wallet is debited from the
  Reward Pool ledger, to keep `CheckZNHBSupplyInvariant` true
  (`core/state_transition.go`).
- **Sponsored NHB transfers.** When a paymaster sponsors an NHB transfer
  (`EvaluateSponsorship`), the paymaster's balance pays the fee instead of the
  sender.
- **Disabling.** `buildTransferGasPolicyFromConfig` marks the policy
  `Enabled = false` when `TransferFreeTierSpendWei` is `0` or empty. In that
  state the NHB path still computes the fee from `TransferFeeBps` and deducts
  it from the sender, but the code that credits the collector is gated on
  `Enabled`, so the amount is not credited to any account
  (`applyEvmTransaction`, `core/state_transition.go`). The ZNHB path
  (`applyTransferZNHB`) does not check `Enabled` when crediting: it still
  charges the ZNHB fee, without a free tier, and credits it to the collector.
  To turn the fee off, set `TransferFeeBps` and `TransferFeeBpsZNHB` to `0`
  rather than only zeroing the free-tier limit.

Query methods:

| Method | Params | Result |
| --- | --- | --- |
| `fees_getTransferStatus` | `[{"address": "<bech32>"}]` | NHB status: `window`, `window_key`, `spentWei`, `freeLimitWei`, `remainingWei`, `eligible`, `nextResetUnix` (omitted when zero). |
| `fees_getTransferQuote` | `[{"address": "<bech32>", "asset": "NHB" or "ZNHB", "amountWei": "<positive integer>"}]` | `eligible`, `feeWei` (`"0"` while eligible), `feeBps`. |

(`rpc/fees_handlers.go`.)

## 2. Fee domains (`fees.Apply`)

Source: `native/fees/apply.go`, `applyTransactionFee` in
`core/state_transition.go`, `buildFeePolicyFromConfig` in `core/node.go`.

This mechanism runs for `TxTypeTransfer` and `TxTypeTransferZNHB` when the
transaction's `MerchantAddress` field, trimmed and lower-cased
(`NormalizeDomain`, `native/fees/apply.go`), equals a configured domain name,
so the match is case-insensitive. `buildFeePolicyFromConfig` configures three
domains with the same policy: `pos`, `p2p` and `otc`. A transaction whose `MerchantAddress` is empty
or is any other string (for example a merchant address used for sponsorship)
skips this mechanism.

For a matching transaction:

- **Free tier.** A counter per payer, domain and UTC month (`FeesGetCounter`).
  While the counter is below `FreeTierTxPerMonth` (default `100`) no fee is
  charged. The counter is shared by NHB and ZNHB (the aggregate scope), because
  the configuration builder does not set per-asset tracking.
- **Fee.** After that, `fee = floor(value * bps / 10000)`, where `bps` is the
  asset entry's `MDRBasisPoints` (an asset entry with `0` inherits
  `MDRBasisPoints`, default `150`, 1.5%). The fee never exceeds the value.
- **Payer and routing.** The fee is debited from the sender's balance in
  addition to the transferred amount, and credited to the asset's
  `OwnerWallet` (`fees: missing route wallet for asset <asset>` when it is
  unset). See [fee routing](./routing.md). The `fees.applied` event's `netWei`
  is `value - fee` as computed by `fees.Apply`; the transfer itself credits the
  recipient the full `value`.
- **Records.** Each evaluation updates the monthly usage counters, the
  per-domain/asset/wallet totals, and emits `fees.applied`
  ([accounting](./accounting.md)).

Per-asset overrides come from `[[global.Fees.Assets]]`: an asset without an
entry has no fee rate and is not charged (only NHB gets a default entry when no
assets are configured at all, `DomainPolicy.normalized`). There is no
exemption list, minimum or maximum fee guard, or on-chain fee-policy record in
the code.

Query methods:

| Method | Params | Result |
| --- | --- | --- |
| `fees_getMonthlyStatus` | none | `window_yyyymm`, `used` (free-tier transactions consumed this UTC month), `remaining` (network-wide allowance minus `used`: the allowance grows by `FreeTierTxPerMonth` the first time each payer is seen in the month), `last_rollover_yyyymm`. `nhb-cli fees status` calls it. |
| `fees_listTotals` | `[{"domain": "pos"}]` | Array of `{domain, wallet, grossWei, feeWei, netWei}`, one per accumulated (domain, asset, wallet) record; the JSON has no `asset` field. |
