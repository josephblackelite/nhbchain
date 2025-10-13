# Fee policy

The NHB fee stack balances affordability for low-volume users with predictable
merchant pricing and safety guardrails. This page summarises the default policy
that the network governance committee stewards and enumerates the knobs that can
be tuned through parameter proposals.

## Free tier

Wallets receive a **monthly allowance of 100 NHB-sponsored transactions** that
is shared across NHB and ZNHB transfers. Transactions that qualify for the free
tier are debited against the sender's usage for the current UTC calendar month.
Balances reset automatically at the start of each month. Once the allowance is
exhausted the standard fee schedule applies. The allowance can be reconfigured
via governance (see [fee parameters](../governance/fee-params.md)). Domains can
opt into per-asset tracking, but the default aggregates both assets so the 100
free transactions cover any combination of NHB and ZNHB activity.

Operators can monitor network-wide utilisation with the JSON-RPC method
`fees_getMonthlyStatus`, which surfaces the active window, free-tier
transactions consumed, and the most recent rollover month. The CLI mirrors the
endpoint as `nhb-cli fees status` for quick checks during incident response.

### Eligibility

* **Retail wallets:** always accrue against the free tier for POS and P2P flows.
* **Developer/test accounts:** can be marked as *exempt* to preserve sandbox
  behaviour. Exemptions are tracked on-chain and audited during governance
  reviews.
* **High-volume merchants and OTC desks:** do not consume free-tier capacity;
  their fees are charged according to the MDR schedule below.

### Exhaustion handling

The gateway and node expose the current allowance and trailing usage so clients
can warn users before the cutoff. Once exhausted, the transaction estimator
quotes the paid rate and the intent must include sufficient balance to cover the
fee.

## Merchant discount rate (MDR)

Point-of-sale payments are charged an **ad valorem fee of 1.5%** of the payment
amount (`pos_mdr_bps = 150`). The MDR applies after any free-tier exemption is
processed. Governance may adjust the basis points value and the protocol
propagates the new rate in the next epoch.

* **Split settlements:** When MDR applies, the paymaster withholds the fee from
the merchant leg before settlement. The withheld amount is routed according to
the [fee routing policy](./routing.md).
* **Pass-through sponsorship:** If a merchant funds the transaction directly,
the MDR still applies unless the merchant wallet is on the *exemptions* list.
* **Asset-specific overrides:** Each fee domain advertises the assets it
  accepts (e.g. NHB, ZNHB) and maps them to bespoke MDR basis points and routing
  wallets. Governance can, for example, charge 150 bps on NHB while routing
  ZNHB through a different wallet at 200 bps. Omitted asset entries fall back to
  the domain default.

## Minimum and maximum fee guards

To keep fee collection predictable at the extremes the policy enforces both a
floor and a ceiling:

* `min_fee`: the absolute minimum charged when MDR × amount would fall below the
  threshold (e.g. micro-payments).
* `max_fee_bps`: the upper bound expressed in basis points. For large tickets
  the effective fee is `min(MDR, max_fee_bps)`.

Governance can adjust either guard alongside the MDR value. All parameters are
covered in [governance controls](../governance/fee-params.md).

## Exemptions

The exemptions list enumerates wallet addresses or classification tags that are
not charged the default fees. Exemptions can be scoped by domain (POS, P2P, OTC)
or by program (e.g. public-goods campaign). Operators should keep the list short
and justify each entry in governance discussions. Changes are enacted through a
parameter proposal that updates the on-chain `FeePolicy` record.

## Domain matrix

| Domain | Default payer | Fee schedule | Notes |
| --- | --- | --- | --- |
| POS | Merchant paymaster | 1.5% MDR with min/max guards | Free tier applies to sponsored consumer wallets. Merchant exemptions override MDR. |
| P2P | Sender wallet | Flat fee derived from `nhb_payment_fee_bps`; free tier applies to wallets under allowance. | OTC desks can be exempted from the free tier to avoid subsidy abuse. |
| OTC | Desk operator | MDR or fixed quote depending on deal leg; honours min/max guards. | Desk exemptions apply when governance designates regulated partners. |

## Governance and monitoring

* The parameter set is audited quarterly; deltas require governance approval.
* Fee metrics are exported via the observability stack and surfaced in the POS
  and OTC dashboards. Monitoring alerts operators when the free-tier utilisation
  exceeds 80% or MDR revenue drifts outside forecast bands.
* Refer to [fee routing](./routing.md) for the distribution of collected fees
  across the NHB ecosystem wallets.
