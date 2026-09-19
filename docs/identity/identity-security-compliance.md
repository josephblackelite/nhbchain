# Identity Security Notes

Security-relevant behavior of the identity module and gateway as implemented. Sources: `core/identity`, `core/state_transition.go`, `rpc/identity_handlers.go`, `core/claimable`, `core/node.go`, `services/identity-gateway`.

## Authorization model

* **Username claim** (`TxTypeRegisterIdentity`): authorized by the transaction signature; the sender becomes the alias owner and primary address. First come, first served: uniqueness is the only protection against squatting. There is no reserved-name list, deposit, cooldown or dispute process in the code.
* **Alias changes** (rename, link/unlink address, set primary, avatar): no live path (RPC methods disabled, no transaction type). When they existed as RPC methods they were authorized by the RPC bearer token, not by a per-message owner signature.
* **Claimables**: write paths are disabled. The rules in the code are described in [`pay-by-email.md`](./pay-by-email.md): alias-derived claimables require the payee to own the alias at claim time; opaque-secret claimables are bearer instruments (whoever knows the preimage can claim before the deadline); claims are rejected once `now >= deadline`.
* **Read methods** (`identity_resolve`, `identity_reverse`, `claimable_get`) are public and unauthenticated.

## Homoglyphs and charset

Aliases are restricted to ASCII `a-z 0-9 . _ -` after lower-casing, 3 to 32 bytes. This avoids Unicode confusables; it does not prevent look-alike ASCII strings (for example `frankrocks` vs `frankr0cks`).

## Identity gateway

* Every endpoint requires an API key with an HMAC signature over method, path, body hash and timestamp, within a skew window (default 5 minutes). There is no nonce store; replay inside the window is possible except where an endpoint is idempotent through `Idempotency-Key` or consumes a single-use token.
* Email verification codes are 6 digits, valid 10 minutes by default, limited to 5 register calls per email hash per hour by default. There is no limit on verify attempts in the code.
* Binding an email to an alias needs both a single-use bind token (proof of inbox control) and a signature from the alias's primary address (proof of alias control) over `{aliasId, emailHash, bindToken}`.
* Only salted hashes of emails and digests of codes and tokens are stored. The salt is `IDENTITY_EMAIL_SALT`; changing it changes every email hash.
* The shipped binary logs verification codes instead of emailing them (`LogEmailer`), so the log contains the codes.

## Data on-chain

The alias record holds the alias, owner, primary, addresses, avatar reference and timestamps. A claimable holds the payer, token, amount, hash lock, recipient hint, deadline and identifiers. No email address is stored on-chain; an email hash can appear only as a claimable's recipient hint (and is not published in events for opaque hints).

For events and gateway records see [`audit.md`](./audit.md).
