# Pay-by-Email Claimables

This page describes the email verification flow provided by `services/identity-gateway` and the claimable (hash-locked hold) rules implemented in the node (`core/claimable`, `core/node.go`, `core/state/manager.go`).

## Availability

**The on-chain half cannot be used on a running node today.** Creating, claiming and cancelling claimables are done through `identity_createClaimable`, `identity_claim`, `claimable_create`, `claimable_claim` and `claimable_cancel`, and all of them are disabled (HTTP 410, code `-32060`); no transaction type creates or claims a claimable. Only `claimable_get` works (see [`identity-api.md`](./identity-api.md)). The rules below describe the code behind the disabled methods. The email verification service (first section) works independently of the chain.

## Email verification (identity gateway)

1. `POST /identity/email/register` with `{"email", "aliasHint"}`: the gateway normalizes the address (trim, lower-case, NFKC), computes the salted hash, and dispatches a 6-digit code. The shipped binary only logs the code; it does not send email (`LogEmailer`).
2. `POST /identity/email/verify` with `{"email","code"}`: on success returns `emailHash` (`0x` plus 64 hex) and a single-use `bindToken` (valid 15 minutes).
3. `POST /identity/alias/bind-email` optionally links the verified hash to an alias, with the alias owner's signature.

Endpoint details: [`identity-gateway.md`](./identity-gateway.md). The gateway never sends the raw email to the node.

## Claimable rules (`core/claimable`, `Node.Identity*`, `Node.Claimable*`)

A claimable holds `amount` of `token` (`NHB` or `ZNHB`) from a payer until a claim, a cancel or expiry.

**Creation (`IdentityCreateClaimable`).** Parameters: `payer`, `recipient`, `token`, `amount`, `deadline`. `recipient` is either a 64-hex string (a 32-byte value, treated as an opaque secret hint) or an alias string (which becomes `identity.DeriveAliasID(alias)` with `RecipientKind` = alias). The hash lock stored is `keccak256(recipientHint)`; the preimage a claimer must present is the 32-byte hint itself. The claimable ID is derived by the state manager (`CreateClaimable`); the node records `chainId`, `nonce`, `createdAt`, `deadline` and `expiresAt`. The event `claimable.created` includes `recipientHint` only for alias-derived claimables; for an opaque hint it is zero, so the secret is not published.

**Claim (`IdentityClaim`).**

* Already claimed: returns the record unchanged (idempotent). Any other non-`init` status fails with `claimable: invalid state`.
* Alias-derived claimable: the alias must be registered (`recipient alias not registered yet` otherwise) and the `payee` must be that alias's primary address or one of its addresses (`claimable: unauthorized` otherwise). The public alias hash proves nothing by itself.
* Opaque-secret claimable: knowledge of the preimage is the only authorization.
* The state manager then checks that `now < deadline` (`claimable: deadline exceeded`), that `keccak256(preimage)` equals the hash lock (`claimable: invalid preimage`), and pays the amount from the claimable vault to the `payee`. Emits `claimable.claimed`.

**Funds.** Creation debits the payer and credits the claimable vault (`ClaimableCredit`); claim, cancel and expire pay out of that vault.

**Cancel (`ClaimableCancel`)**: only the payer (`claimable: unauthorized` otherwise), while `now <= deadline`; returns the funds to the payer; emits `claimable.cancelled`. **Expire (`ClaimableExpire`)**: callable by anyone once `now >= deadline` (`claimable: not expired` before); returns the funds to the payer; emits `claimable.expired`.

**Statuses:** `init`, `claimed`, `cancelled`, `expired`. Error strings: `claimable: not found`, `invalid token`, `amount must be positive`, `invalid preimage`, `unauthorized`, `deadline exceeded`, `not expired`, `invalid state`, `insufficient funds`.

## Events

| Event | Attributes |
| --- | --- |
| `claimable.created` | `id`, `payer`, `token`, `amount`, `deadline`, `createdAt`, `recipientHint` |
| `claimable.claimed` | `id`, `payer`, `payee`, `token`, `amount`, `recipientHint` |
| `claimable.cancelled`, `claimable.expired` | `id`, `payer`, `token`, `amount` |

Addresses are bech32; `id` and `recipientHint` are hex without `0x`.
