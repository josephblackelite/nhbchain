# Avatar Specification

Aliases can present avatars to improve recognition and reduce payment errors. Avatars are referenced on-chain as immutable
strings (`avatarRef`); the chain itself stores only the string and does not host or retrieve the underlying media.

## Allowed Sources

| Source | Format | Notes |
| --- | --- | --- |
| HTTPS URL | `https://cdn.nhb/...` or partner CDN. | Must use TLS 1.2+. Wallets should enforce HTTPS and check MIME type. |
| Blob-shaped reference | `blob://<cid>`-formatted string. | There is no on-chain blob storage or blob RPC. The node only checks
  for the `blob://` prefix; resolving the CID to actual media is entirely up to the client (e.g. a wallet-configured IPFS
  gateway or partner CDN). |

## Size & Content Rules

* Maximum file size: **512 KB** for HTTPS uploads; **256 KB** for on-chain blobs.
* Supported MIME types: `image/png`, `image/jpeg`, `image/webp`, `image/svg+xml` (SVG sanitized server-side).
* Aspect ratio: ideally 1:1. Wallets should display within a 128×128 px circle or rounded square.
* Content policy forbids violence, nudity, hateful symbols, QR codes, or misleading brand usage. Gateway rejects uploads failing
  automated or manual review.

## Caching Guidance

* Wallets may cache avatars for 24 hours. Include `ETag` or `Last-Modified` headers.
* Respect CDN caching directives; avoid hotlinking third-party domains outside partner registry.
* Provide blurhash or placeholder color derived from aliasId for offline UX.

## Updating Avatars

1. Owner hosts the avatar media externally (HTTPS CDN) or otherwise obtains a `blob://`-shaped reference; there is no gateway
   upload endpoint.
2. Owner calls `identity_setAvatar(ownerAddr, avatarRef)` over authenticated RPC to update the on-chain record. The node only
   checks that `avatarRef` has an `https://` or `blob://` prefix — it performs no content validation or storage.
3. Event `identity.alias.avatarUpdated` notifies subscribers to refresh caches.

## Recommended Client Behavior

* Fallback to generated identicon (e.g., BLAKE3 aliasId hashed to color palette) when no avatar set.
* Preload avatars when scanning QR codes or directory listings.
* Display moderation badges for avatars flagged by governance (future field `avatarFlag`).

## RPC & CLI Exposure

* JSON-RPC: `identity_setAvatar(addressBech32, avatarRef)` (authenticated).
* CLI: `nhb-cli id set-avatar --addr nhb1... --avatar https://cdn/...`.

## Security Notes

* Wallets must enforce content type after download; reject mismatched MIME signatures.
* Avoid embedding avatar binary directly into QR codes or URIs; rely on references to prevent bloat.
* For blob references, validate CID before fetching to avoid SSRF.

For additional context on alias management, see [identity.md](./identity.md) and [identity-gateway.md](./identity-gateway.md).
