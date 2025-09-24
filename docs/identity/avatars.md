# Avatar Specification

Aliases can present avatars to improve recognition and reduce payment errors. Avatars are referenced on-chain as immutable
strings (`avatarRef`) and retrieved via HTTPS or on-chain blob storage.

## Allowed Sources

| Source | Format | Notes |
| --- | --- | --- |
| HTTPS URL | `https://cdn.nhb/...` or partner CDN. | Must use TLS 1.2+. Wallets should enforce HTTPS and check MIME type. |
| Blob reference | `blob://<cid>` referencing on-chain stored blob. | CIDs follow NHBChain blob module rules; wallets fetch via node
  blob RPC. |

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

1. Owner uploads media via [`POST /identity/avatars/upload`](./identity-gateway.md#post-identityavatarsupload).
2. Gateway returns canonical `avatarRef` (HTTPS URL or `blob://` reference) after validation.
3. Owner signs `identity_setAvatar(alias, avatarRef, sig)` to update on-chain record.
4. Event `identity.alias.avatarUpdated` notifies subscribers to refresh caches.

## Recommended Client Behavior

* Fallback to generated identicon (e.g., BLAKE3 aliasId hashed to color palette) when no avatar set.
* Preload avatars when scanning QR codes or directory listings.
* Display moderation badges for avatars flagged by governance (future field `avatarFlag`).

## Security Notes

* Wallets must enforce content type after download; reject mismatched MIME signatures.
* Avoid embedding avatar binary directly into QR codes or URIs; rely on references to prevent bloat.
* For blob references, validate CID before fetching to avoid SSRF.

For additional context on alias management, see [identity.md](./identity.md) and [identity-gateway.md](./identity-gateway.md).
