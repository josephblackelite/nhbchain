# Merchant Tooling Guide

A pointer page for integrators building escrow and loyalty flows. Each item links to the document that describes it from the code.

## Escrow

* State machine, transaction types, fees, arbitration: [`../escrow/escrow.md`](../escrow/escrow.md).
* Transaction routing inside the state processor: [`../escrow/hardened-engine.md`](../escrow/hardened-engine.md).
* REST gateway for relayed escrow actions (`services/escrow-gateway`): [`../escrow/gateway-api.md`](../escrow/gateway-api.md) and [`../escrow/nhbchain-escrow-gateway.md`](../escrow/nhbchain-escrow-gateway.md).
* Milestone projects: [`../escrow/milestones.md`](../escrow/milestones.md) (only the read method `escrow_milestoneGet` is live; the write methods answer HTTP 410).
* Command line: `nhb-cli escrow ...` (see [`../escrow/escrow.md`](../escrow/escrow.md) section 8).

## Payee alias lookup for escrows (API gateway)

The API gateway (`gateway/routes/wallet.go`) adds one route under the `consensus` route prefix: `GET /wallet/escrows/{escrowID}` (with the sample `gateway/config.yaml` prefix that is `/v1/consensus/wallet/escrows/{escrowID}`). It calls the node's `escrow_get` and, when `identity_reverse` finds an alias for the payee, adds `payeeAlias` and `payeeAliasId`. The response has `id`, `status`, `payer`, `payee`, `token`, `amount` and the optional alias fields. An unknown escrow returns HTTP 404 (`escrow not found`).

## Loyalty

* Programs, base reward, transactions, RPC: [`../loyalty/loyalty.md`](../loyalty/loyalty.md), [`../loyalty/payouts.md`](../loyalty/payouts.md), [`../loyalty/paymaster.md`](../loyalty/paymaster.md).

## Identity

* Aliases, resolution, avatars, pay-by-username and pay-by-email: [`../identity/identity.md`](../identity/identity.md) and the pages linked from it.
