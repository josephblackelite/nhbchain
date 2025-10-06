# Fee policy governance parameters

The network stores the fee configuration in the `feepolicy` module. Governance
proposals can adjust the knobs below without a code upgrade. Use this page as a
reference when drafting parameter-change or fee-routing proposals.

## Adjustable parameters

| Key | Description |
| --- | --- |
| `free_tier_tx_count` | Monthly allowance of NHB-sponsored transactions per wallet (default `1000`). |
| `nhb_payment_fee_bps` | Basis points applied to non-POS payments once a wallet exhausts the free tier. |
| `pos_mdr_bps` | Merchant discount rate for POS payments, expressed in basis points (default `150`). |
| `min_fee` | Absolute minimum fee (in NHB) charged when ad valorem calculations fall below the threshold. |
| `max_fee_bps` | Upper bound on fees, capped as a basis-points multiple of the transaction amount. |
| `exemptions` | Address or tag list that bypasses standard fees. Entries can be scoped per domain (POS, P2P, OTC). |
| `owner_fee_wallet_nhb` | NHB-denominated wallet receiving the network fee share. |
| `znhb_proceeds_wallet` | Stablecoin wallet for routing ZNHB fee proceeds. |
| `owner_fee_wallet_usdc` | Off-chain custody wallet referenced for USDC settlements. |

Parameter values live under the `feepolicy.params` path. The governance service
validates changes before accepting a proposal.

## Inspecting the current policy

Use the governance CLI to inspect the active parameters. The command prints the
full JSON configuration, which you can diff locally before staging an update.

```bash
nhbctl gov query params --module feepolicy
```

To extract a single field (e.g. the POS MDR) pipe the output through `jq`:

```bash
nhbctl gov query params --module feepolicy | jq '.pos_mdr_bps'
```

## Drafting a fee policy update

The example below raises the POS MDR to 1.75%, increases the minimum fee to 50
micros, and adds a temporary exemption for a public-goods campaign wallet. Save
the payload to `docs/examples/gov/fee-policy-proposal.json` and submit it with
`nhbctl`.

```bash
nhbctl gov propose param \
  --title "Adjust POS MDR" \
  --summary "Raise MDR to 175 bps, bump min fee, add campaign exemption" \
  --deposit 1000000znhb \
  --module feepolicy \
  --param-file docs/examples/gov/fee-policy-proposal.json
```

The referenced JSON file contains the following structure:

<!-- embed:docs/examples/gov/fee-policy-proposal.json -->
```json
{
  "module": "feepolicy",
  "changes": {
    "pos_mdr_bps": 175,
    "min_fee": 50,
    "exemptions": [
      {
        "domain": "POS",
        "address": "nhb1publicgoodscampaign00000000000000000000",
        "tag": "public_goods_q4",
        "expires_at": "2025-12-31T23:59:59Z"
      }
    ]
  },
  "metadata": {
    "reason": "Seasonal MDR uplift and subsidy carve-out",
    "effective_epoch": 482
  }
}
```

Before submitting, fetch the current parameters to capture a "before" snapshot:

```bash
nhbctl gov query params --module feepolicy > /tmp/feepolicy-before.json
```

After the proposal executes, re-run the query and compare:

```bash
nhbctl gov query params --module feepolicy > /tmp/feepolicy-after.json
jq -s '.[0], .[1]' /tmp/feepolicy-before.json /tmp/feepolicy-after.json
```

## Drafting a fee routing update

Routing targets use the same module. When rotating treasury wallets or adjusting
splits, create a proposal similar to the example stored in
`docs/examples/gov/fee-routing-proposal.json`.

```bash
nhbctl gov propose param \
  --title "Rotate fee wallets" \
  --summary "Update owner and ZNHB proceeds wallets" \
  --deposit 1000000znhb \
  --module feepolicy \
  --param-file docs/examples/gov/fee-routing-proposal.json
```

The payload looks like:

<!-- embed:docs/examples/gov/fee-routing-proposal.json -->
```json
{
  "module": "feepolicy",
  "changes": {
    "owner_fee_wallet_nhb": "nhb1ownerwalletrotatedxxxxxxxxxxxxxxxxxxxx",
    "znhb_proceeds_wallet": "nhb1stablewalletrotatedxxxxxxxxxxxxxxxxxx",
    "owner_fee_wallet_usdc": "usdc1custodyvaultrotatedxxxxxxxxxxxxxxxx"
  },
  "metadata": {
    "reason": "Treasury wallet rotation for FY25",
    "effective_epoch": 490
  }
}
```

Query the addresses before and after execution:

```bash
nhbctl gov query params --module feepolicy | jq '{owner_fee_wallet_nhb, znhb_proceeds_wallet, owner_fee_wallet_usdc}'
```

For additional context on how each parameter affects runtime behaviour see the
[fee policy](../fees/policy.md) and [fee routing](../fees/routing.md) guides.
