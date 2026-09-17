# Swap and Mint Rail

Founder mainnet treats swaps as a custody-backed payment rail:

* users can pay with supported external crypto such as `BTC`, `USDT`, or
  `USDC`
* treasury and reconciliation recognise the final USD-equivalent value received
* the chain mints `NHB` from that final recognised value
* `ZNHB` is not the swap mint asset on founder mainnet

The off-chain settlement service(s) authorised to mint, and their API contract,
are deployment details outside the scope of this node's public source (see
[`docs/escrow/mint-settlement.md`](../escrow/mint-settlement.md) for the
on-chain `mint_with_sig` RPC that any such service submits to).

## Core Rule

`1 NHB = $1`

The mint basis is the final USD-equivalent value that the custody and
reconciliation layer recognises as received. The external asset is just the
funding rail.

## Secret Placement

Provider credentials and mint-signing material belong on the backend service
host, not in the wallet frontend and not in genesis.

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
