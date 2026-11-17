# Fee routing

Where the two fee mechanisms described in [fee policy](./policy.md) credit
their proceeds. Everything here is on-chain account crediting done by
`core/state_transition.go`; there is no separate fee-router module and no
routing event: the only fee event is `fees.applied`
([accounting](./accounting.md)).

## Domain fees

`applyTransactionFee` debits the fee from the payer (the sender, since the
configuration builder never sets `FeePayer` to `recipient`) and credits it as
follows.

- The asset's route wallet comes from `[[global.Fees.Assets]]`; an asset entry
  with no `OwnerWallet` uses `[global.Fees] OwnerWallet`
  (`buildFeePolicyFromConfig`, `core/node.go`). If the resolved wallet is the
  zero address and a fee is due, the transaction fails with `fees: missing
  route wallet for asset <ASSET>`.
- **Buyback share.** For NHB fees, when the node has a genesis buyback
  configuration (`hasBuybackConfig`), `feeShareBps` of the fee (default `2000`,
  20%, or the value set by a `policy.buybackParams` proposal) is credited to the
  buyback accrual account instead of the route wallet, and the accrual ledger
  is updated. With no buyback configuration the whole fee goes to the route
  wallet. See [tokenomics](../tokenomics/tokenomics.md#6-the-treasury-buyback-engine).
- **ZNHB fees.** The fee is credited to the route wallet as ordinary balance.
  When the route wallet is the transfer's own sender or recipient, the credit
  is applied to that account's in-flight object so the transfer's own write
  does not overwrite it. `applyTransactionFee` does not adjust the Sale Pool or
  Reward Pool ledgers itself; when the route wallet is the admin (treasury)
  wallet, `executeTransaction` books the net ZNHB movement into the Reward Pool
  ledger in the same state transition (`treasuryZNHBFlowTracked` includes
  `TxTypeTransferZNHB`, `core/znhb_treasury_pool.go`).

The `ValidateConfig` rule in `config/validate.go` requires an `OwnerWallet` for
`ZNHB` in `[[global.Fees.Assets]]` whenever a ZNHB asset entry exists
(`cmd/consensusd` runs it at startup and refuses to start on a problem;
`cmd/nhb` logs the same problems as `configuration problem` warnings).

## Transfer fees

The protocol transfer fee is credited to `TransferFeeCollector`, or to the
node's escrow fee treasury when unset. There is no buyback share on this fee.
When the collector is the chain's admin wallet, ZNHB transfer fees are also
credited to the ZNHB Reward Pool ledger. Details are in
[fee policy](./policy.md#1-protocol-transfer-fee-transfergaspolicy).

## Configuration

See [fee configuration](../governance/fee-params.md). Key names are the
PascalCase Go field names (`OwnerWallet`, `Assets`, `Asset`, `MDRBasisPoints`);
the TOML decoder prefers an exact match but also accepts a different case.
Routing targets and rates are node configuration and are not changed by any
governance proposal.

## Observing routing

Each domain-fee evaluation emits `fees.applied` with `ownerWallet` (hex) and
`asset` (`core/events/fees.go`); the accumulated totals per route wallet are returned by
`fees_listTotals` ([accounting](./accounting.md)).
