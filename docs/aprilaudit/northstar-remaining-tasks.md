# NHBChain North Star Remaining Tasks

Date: 2026-04-12

This document is the remaining-work compass for relaunching NHBChain after the chain
reset. It assumes the hard-fork class protocol fixes already landed and the current
focus is finishing product and operations readiness.

Founder-model items already aligned in code and genesis:

* `ZNHB` is treated as fixed-supply on founder mainnet
* protocol base rewards fund the spender, not the merchant
* base rewards default to `50 bps` with a `25-100 bps` operating band
* merchant loyalty remains paymaster-funded and additive to the protocol reward
* staking and loyalty rewards are treasury-funded, not inflationary on founder mainnet

## 1. Launch-Critical Infrastructure

* Rotate all NOWPayments credentials that were exposed in chat and issue fresh secrets.
* Stand up production env files or a secrets manager for:
  * `payments-gateway`
  * `payoutd`
  * `ops-reporting`
  * node RPC auth
  * minter KMS key references
* Configure reverse proxy and TLS for:
  * `chain.nhbcoin.com`
  * `pay.nhbcoin.com`
  * optional `ops.nhbcoin.com`
* Lock internal service ports behind localhost or security groups where possible.
* Produce systemd/PM2/container startup units for node, `payments-gateway`,
  `payoutd`, and `ops-reporting`.

## 2. Founder-Grade Payment Rail Completion

* Complete the inbound swap-mint UI integration in `nhbportal` using the new
  `payCurrency` / `mintAsset` quote semantics.
* Add push or websocket balance refresh in the wallet after mint settlement.
* Confirm the exact fee policy you want for the inbound mint rail:
  * pass-through provider fees only
  * NHBChain service fee
  * combined markup
* Make the wallet and finance docs explicit that NHB minting follows the final
  USD-equivalent value recognized by NOWPayments custody and reconciliation as
  received.
* Add configurable success/cancel redirect URLs for NOWPayments checkout flows.
* Add more explicit quote breakdown fields if you want network fee, service fee, and
  markup shown separately in the wallet.

## 3. Cash-Out Rail Completion

* Complete the end-to-end NHB to `USDT` / `USDC` withdrawal initiation path in the
  wallet app.
* Keep NOWPayments payout execution and custody reporting explicit in operator and
  wallet-facing documentation so the off-ramp model remains auditable.
* Tune the now-implemented payout controls for production policy:
  * per-user and per-destination thresholds
  * hourly and daily velocity values
  * high-risk partner and region approval thresholds
  * active screening lists and hold procedures
* Add richer payout receipt evidence if finance/compliance teams need provider payout
  identifiers alongside the on-chain attestation.

## 4. Treasury and Reconciliation

* Add actual cold-wallet execution adapters or MPC/custody workflow integration for
  approved treasury instructions.
* Expand treasury reconciliation into reserve and inventory accounting across:
  * mint
  * payout
  * treasury
  * settlement
* Add downloadable finance exports that join mint, payout, and treasury records into
  one end-of-day reporting package.
* Keep reconciliation logic aligned to the chosen treasury model: NOWPayments custody
  for inbound settlement and payouts.

## 5. Loyalty and Commerce Product Completion

* Finish wallet-side loyalty presentation for end users:
  * protocol reward preview
  * merchant bonus preview
  * settled reward history
  * running `ZNHB` balance
* Make wallet and merchant docs explicit that base rewards apply to qualifying NHB
  commerce flows, not arbitrary wallet-to-wallet transfers.
* Add merchant tooling for:
  * paymaster funding status
  * loyalty program reward rate
  * reward issuance history
  * low-balance alerts
* Finalize treasury bucket accounting for founder operations:
  * loyalty treasury
  * staking/security treasury
  * merchant growth / ecosystem reserve
  * OTC / distribution reserve

## 6. Merchant and Commerce Layer

* Expand operator reporting from trade status to full merchant settlement exports with
  net/gross/fee views.
* Add merchant settlement webhooks and downloadable reports that product and finance can
  hand to partners directly.
* Tighten subscription/recurring payments if those are part of the immediate launch
  promise.
* Complete the native lending product activation path if banking partners are expected
  near launch.

## 7. Wallet Security and Identity

* Audit `nhbportal` cryptographic recovery design and remove any unsafe deterministic
  key-regeneration assumptions.
* Move toward a production-grade recovery model:
  * encrypted backup
  * passkeys
  * guardians
  * MPC/account abstraction
* Align wallet UX with the chain's explicit transfer sponsorship and mint/cash-out
  semantics.

## 8. Production Readiness and Ops

* Create first-run deployment guides for a fresh chain reset:
  * genesis generation
  * validator bootstrap
  * node startup
  * service startup
  * DNS and TLS wiring
* Add dashboards and alerts for:
  * mint webhook failures
  * payout failures
  * treasury low-balance conditions
  * reconciliation drift
  * node finality and mempool health
* Prepare incident runbooks for:
  * payment provider outage
  * webhook outage
  * treasury underfunding
  * signer/KMS failure
  * payout rail halt

## 9. Relaunch Checklist

* regenerate genesis
* clear old local state and databases
* issue fresh secrets
* start node
* start `payments-gateway`
* start `payoutd`
* start `ops-reporting`
* verify inbound mint quote -> invoice -> webhook -> mint
* verify outbound cash-out intent -> payout -> receipt
* verify merchant trade reporting
* verify treasury reporting
* verify wallet live balance refresh

## Current Readiness Summary

Already materially in place:

* protocol hardening and transfer fixes
* fee-threshold transfer policy
* lending daemon wiring
* treasury wallet execution
* treasury maker-checker workflow
* mint reporting
* unified operator reporting across mint, merchant, treasury, and payout
* founder-grade split for inbound `crypto -> NHB` quote and invoice semantics
* founder-grade fixed-supply `ZNHB` loyalty model

Still remaining before a confident public rerun:

* final production deployment wiring on the actual chain EC2
* wallet integration completion
* live-provider credential rotation and secure storage
* richer settlement/reconciliation exports
* production wallet recovery redesign
* end-to-end OTC office and retailer operating playbooks
