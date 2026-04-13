# Swap and Mint Rail

Founder mainnet treats swaps as a custody-backed payment rail:

* users can pay with supported external crypto such as `BTC`, `USDT`, or
  `USDC`
* treasury and reconciliation recognise the final USD-equivalent value received
* the chain mints `NHB` from that final recognised value
* `ZNHB` is not the swap mint asset on founder mainnet

For the current API contract and request/response details, use
[`docs/api/swap-mint.md`](../api/swap-mint.md).

## Core Rule

`1 NHB = $1`

The mint basis is the final USD-equivalent value that the custody and
reconciliation layer recognises as received. The external asset is just the
funding rail.

## Live Roles

* `payments-gateway` creates quotes and invoices, verifies provider callbacks,
  and submits `NHB` mint settlement to the chain.
* `payoutd` handles `NHB -> USDT/USDC` cash-out execution and reporting.
* `ops-reporting` exposes mint, payout, treasury, and reconciliation views for
  operators.

## Secret Placement

Provider credentials and mint-signing material belong on the backend service
host, not in the wallet frontend and not in genesis.

Typical environment variables include:

* `PAY_GATEWAY_NOW_API_KEY`
* `PAY_GATEWAY_NOW_IPN_SECRET`
* `PAY_GATEWAY_NODE_URL`
* `PAY_GATEWAY_NODE_TOKEN`
* `PAY_GATEWAY_MINTER_KMS_ENV`

## Founder-Mainnet Asset Policy

* `NHB` remains the settlement mint asset.
* `ZNHB` remains fixed-supply, pre-minted at genesis, and mint-paused after
  launch.
* Merchant and protocol rewards pay existing `ZNHB` from funded treasuries or
  paymasters; they do not mint fresh `ZNHB`.

## Legacy Note

Older swap prototypes in the repo may still refer to `ZNHB` voucher minting.
Those references should be treated as historical development artifacts, not the
founder mainnet settlement model.
