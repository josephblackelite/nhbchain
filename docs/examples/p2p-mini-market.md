# P2P Mini-Market Walkthrough

The P2P mini-market demo pairs a lightweight marketplace UI with the dual-lock
escrow RPCs introduced for peer-to-peer NHB ⇄ ZNHB trades. This document outlines
the trade lifecycle, the pay intents returned to each counterparty, dispute
resolution outcomes, and the gateway endpoints exposed at
`https://api.nhbcoin.net`.

## Trade lifecycle

Dual-lock trades progress through the following aggregate states:

| State | Description |
|-------|-------------|
| `init` | Trade created. Both escrows exist but are unfunded. |
| `partial_funded` | Exactly one leg has been funded (`escrow_fund`). |
| `funded` | Both escrows funded. Trade is ready to settle. |
| `disputed` | Buyer or seller opened a dispute (`p2p_dispute`). Escrows are frozen. |
| `settled` | Atomic release executed (`p2p_settle` or arbitrator `release_both`). |
| `cancelled` | Trade cancelled voluntarily or via arbitrator refund outcome. |
| `expired` | Funding deadline elapsed and at least one leg refunded. |

Each escrow leg tracks its own status (`init`, `funded`, `released`, `refunded`,
`disputed`, `expired`). The UI polls `escrow_get` to surface both legs alongside
the aggregated trade status.

## Pay intents

`p2p_createTrade` returns two pay intents that encode where each party must send
funds:

```json
{
  "tradeId": "0x…",
  "escrowBaseId": "0x…",
  "escrowQuoteId": "0x…",
  "payIntents": {
    "seller": {
      "to": "nhb1escrowvault…",
      "token": "NHB",
      "amount": "1000000000000000000",
      "memo": "ESCROW:0xbase"
    },
    "buyer": {
      "to": "nhb1escrowvault…",
      "token": "ZNHB",
      "amount": "500000000000000000",
      "memo": "ESCROW:0xquote"
    }
  }
}
```

The mini-market renders each intent as a `znhb://pay` QR code and copies the raw
fields so test accounts can fund the escrow vaults manually. The memo is mandatory
and lets the module reconcile deposits with the pending escrow record.

## Dispute outcomes

Arbitrators resolve disputes using `p2p_resolve` with one of four outcomes:

| Outcome | Result |
|---------|--------|
| `release_both` | Releases both escrows to their payees (equivalent to mutual settle). |
| `refund_both` | Refunds both escrows to their original payers (trade cancels). |
| `release_base_refund_quote` | Releases the base leg to the buyer and refunds the quote leg to the buyer (seller loses). |
| `release_quote_refund_base` | Releases the quote leg to the seller and refunds the base leg to the seller (buyer loses). |

The UI provides controls to open a dispute and then exercise each resolution path.
Settlement and refunds remain atomic—either both legs execute or neither leg
moves.

## Gateway endpoints

The production gateway exposes REST endpoints at `https://api.nhbcoin.net`:

| Method & Path | Purpose |
|---------------|---------|
| `POST /p2p/offers` | (Optional) Persist an offer with idempotency guarantees. |
| `GET /p2p/offers` | Enumerate active offers for discovery. |
| `POST /p2p/accept` | Accept an offer, invoke `p2p_createTrade`, and return the dual pay intents. |
| `GET /p2p/trades/{id}` | Fetch trade status including escrow snapshots. |
| `POST /p2p/trades/{id}/settle` | Mutual settlement once both legs are funded. |
| `POST /p2p/trades/{id}/dispute` | Flag a dispute from either counterparty. |
| `POST /p2p/trades/{id}/resolve` | Arbitrator resolution using the outcomes above. |

All endpoints require the standard API key + HMAC headers. Mutating calls also
include the wallet signature of the buyer, seller, or arbitrator so the gateway
can assert on-chain authority.

The mini-market demo talks to the public RPC (`https://rpc.testnet.nhbcoin.net`) to keep
credentials client-side for self-hosted QA, but production operators integrate
with `api.nhbcoin.net` to leverage persistent offer storage, idempotency, and
webhook delivery of trade events.
