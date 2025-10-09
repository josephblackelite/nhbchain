# Pay-by-Email Claimables

Pay-by-email extends NHBChain payments to recipients that have not yet claimed an alias. Funds are held in a lightweight on-chain claimable until the recipient verifies their email address and presents the shared secret returned by the identity gateway. This document covers the verification flow, JSON-RPC/CLI usage, webhook notifications, and abuse controls.

## Verification Flow

1. **Initiate verification** – the sender (or wallet on their behalf) calls the identity gateway `POST /identity/email/register` endpoint with the raw email address and optional alias hint. The gateway normalises (`lower + NFKC`), salts, and HMAC-hashes the address before queuing an out-of-band code.
2. **Recipient verifies** – the recipient follows the emailed link and submits the one-time code via `POST /identity/email/verify`. Success returns the salted `emailHash` that is safe to share with wallets and nodes.
3. **Wallet stores secret** – wallets persist the returned hash locally. This 32-byte value becomes the claim preimage supplied during `identity_claim`.
4. **Optional alias binding** – once the recipient registers an alias they may opt-in to bind the verified hash to their alias for directory lookups (see `identity_setAlias` + gateway bind APIs).

The node never sees raw email addresses. All on-chain state references the 32-byte salted hash only.

## Creating Claimables

Use the authenticated JSON-RPC method `identity_createClaimable` to escrow funds for a hashed recipient.

```json
{
  "jsonrpc": "2.0",
  "id": 41,
  "method": "identity_createClaimable",
  "params": [
    {
      "payer": "nhb1payer...",
      "recipient": "0x3a4b...",    // salted email hash
      "token": "NHB",
      "amount": "25",
      "deadline": 1718822400
    }
  ]
}
```

**Response**

```json
{
  "claimId": "0x92fd...",
  "recipientHint": "0x3a4b...",
  "token": "NHB",
  "amount": "25",
  "expiresAt": 1718822400,
  "createdAt": 1718736000
}
```

CLI equivalent:

```
nhb-cli id create-claimable \
  --payer nhb1payer... \
  --recipient 0x3a4b... \
  --token NHB \
  --amount 25 \
  --deadline 1718822400
```

### Recipient Hint

`recipient` must be either:

* A salted email hash returned by the gateway (32-byte hex string), or
* An alias string – the node derives the aliasId and uses it as the hint. This is useful when paying aliases that are registered but unresolved for a linked address.

Internally the node stores the 32-byte hint alongside the keccak hash used for the claim lock. The preimage supplied during claim must match this hint exactly. If the hint corresponds to an alias identifier, the node also requires the payee address to be currently bound to that alias—attempts from unrelated accounts are rejected—while email-hash claimables continue to rely solely on the shared preimage.

## Claiming Funds

Recipients call the authenticated `identity_claim` RPC once they control the email hash.

```json
{
  "jsonrpc": "2.0",
  "id": 42,
  "method": "identity_claim",
  "params": [
    {
      "claimId": "0x92fd...",
      "payee": "nhb1recipient...",
      "preimage": "0x3a4b..."
    }
  ]
}
```

**Response**

```json
{
  "ok": true,
  "token": "NHB",
  "amount": "25"
}
```

CLI equivalent:

```
nhb-cli id claim \
  --id 0x92fd... \
  --payee nhb1recipient... \
  --preimage 0x3a4b...
```

On success the node debits the claimable vault and credits the `payee`. Replays are idempotent and return `{ "ok": true }` without double-paying.

## Webhook Notifications

Gateway operators typically subscribe to node events to drive webhooks. Relevant event types:

* `claimable.created` – includes `id`, `payer`, `token`, `amount`, `recipientHint`, `deadline`.
* `claimable.claimed` – includes `id`, `payer`, `payee`, `token`, `amount`, `recipientHint`.
* `claimable.expired` / `claimable.cancelled` – emitted when funds return to the payer.

Suggested webhook payload for a successful claim:

```json
{
  "event": "identity.claimable.claimed",
  "id": "0x92fd...",
  "payee": "nhb1recipient...",
  "token": "NHB",
  "amount": "25",
  "recipientHint": "0x3a4b...",
  "claimedAt": 1718740000
}
```

Include an HMAC signature header so merchants can verify authenticity.

## Abuse Handling & Limits

* **TTL** – `deadline` must be in the future; once reached the payer can reclaim funds. Default wallet UX sets 7–14 days.
* **Rate limits** – gateway enforces per-IP and per-email attempt caps (5/hour default). Node RPCs inherit standard bearer-token throttles.
* **Anomaly detection** – monitor claim events for abnormal velocity by hint or payer. Flag repeated failures, mismatched preimages, or rapidly reused hints.
* **PII minimisation** – only salted hashes leave the gateway. Wallet logs should redact raw emails and hints whenever possible.
* **Idempotency** – clients may safely retry create/claim calls; the node returns stable results if nothing changes.

For a deeper look at alias records and avatar usage, see [identity.md](./identity.md) and [avatars.md](./avatars.md).
