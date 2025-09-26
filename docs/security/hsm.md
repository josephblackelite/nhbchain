# HSM Signing Architecture

The OTC gateway delegates mint voucher signing to the custodial HSM cluster. The integration is implemented in `services/otc-gateway/hsm/` and enforces mutual TLS for every request. The client loads its certificate and key from the environment and validates the remote endpoint against a dedicated CA bundle.

## Components

- **HSM proxy** – Exposes a REST endpoint (`POST /sign`) that accepts a keccak256 digest and a key label. It returns a secp256k1 signature and the distinguished name (DN) of the signer certificate.
- **OTC gateway** – Wraps the proxy inside a hardened HTTP client that always presents its client certificate and validates responses before persisting them.
- **MINTER_NHB key** – The signing key assigned to the OTC minting workflow. The key label defaults to `MINTER_NHB` but can be overridden via `OTC_HSM_KEY_LABEL` when multiple tenants exist.

## Security Controls

- **mTLS** – The gateway must be provisioned with `OTC_HSM_CLIENT_CERT`, `OTC_HSM_CLIENT_KEY`, and `OTC_HSM_CA_CERT`. Without all three, the service refuses to start.
- **Pinned base URL** – `OTC_HSM_BASE_URL` selects the proxy host. Requests are rejected unless the host matches the supplied CA chain.
- **Signer DN audit** – Responses include the signer DN; the gateway stores the value alongside every voucher signature to support post-incident forensics.
- **Timeouts** – The HTTP client applies a 10-second timeout to prevent stalled connections.

## Environment Variables

| Variable | Description |
| --- | --- |
| `OTC_HSM_BASE_URL` | Base URL for the HSM proxy (e.g. `https://hsm.internal.nhb`) |
| `OTC_HSM_CA_CERT` | Path to the PEM-encoded CA bundle used to validate the proxy |
| `OTC_HSM_CLIENT_CERT` | Path to the client certificate granted by the HSM operators |
| `OTC_HSM_CLIENT_KEY` | Path to the unencrypted client private key |
| `OTC_HSM_KEY_LABEL` | Optional key alias (defaults to `MINTER_NHB`) |
| `OTC_HSM_SIGNER_DN` | Optional static DN override when the proxy omits it |

The gateway halts at startup when any required value is missing, preventing the service from running without secure signing.
