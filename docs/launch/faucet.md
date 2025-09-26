# Testnet Faucet Operations

The public faucet provides rate-limited Test ZNHB (TZNHB) to developers onboarding to the NHBChain testnet. This document explains request flows, rate limits, and abuse mitigations for launch.

## Endpoint Summary

| Method | URL | Description |
| --- | --- | --- |
| `POST` | `https://faucet.testnet.nhbchain.io/api/claim` | Request a TZNHB drip to a Bech32 address |
| `GET` | `https://faucet.testnet.nhbchain.io/api/limits/<address>` | Inspect remaining daily quota |
| `GET` | `https://faucet.testnet.nhbchain.io/api/health` | Health probe used by monitoring |

All endpoints require HTTPS. Cross-origin requests from the docs portal and explorer are allowed via CORS; other origins must use the CLI or direct HTTP clients.

## Request Workflow

```bash
curl -X POST https://faucet.testnet.nhbchain.io/api/claim \
  -H 'Content-Type: application/json' \
  -d '{"address":"tnhb1abcd...","channel":"docs"}'
```

Successful responses return a transaction hash that can be tracked on the explorer. If the rate limit is exceeded, the API responds with HTTP 429 and the next available request window in seconds.

## Rate Limits

* **Per Address:** 25 TZNHB per claim, maximum 10 claims per rolling 24 hours.
* **Per IP:** 100 claims per day. Shared office networks should request a partner quota increase.
* **Captcha:** Web UI requires hCaptcha verification. CLI users must provide a signed message proving address ownership.

Quota resets occur at midnight UTC. Excessive violations trigger automated firewall rules and security review.

## Abuse & Security Controls

* Address ownership validation using signature-based challenges for API clients.
* Geo-distributed rate limiters with Redis-backed counters and automatic blocklist propagation.
* Manual override via `scripts/faucet/unblock.sh <address>` for legitimate requests that hit safeguards.
* Logging integrated with SIEM for anomaly detection and correlation with disclosure reports.

## Support Playbook

1. Confirm the user is targeting the correct chain (`nhb-testnet-1`) and using a `tnhb` Bech32 prefix.
2. Check the `/limits/<address>` response for remaining quota.
3. Review alerting channels (`#ops-faucet`) for throttling or downtime notices.
4. For escalations, page the on-call SRE and reference the [release process](../security/release-process.md#incident-response).

## Abuse Policy

The faucet is a shared resource intended for development and integration testing. Automated draining, resale of test tokens, or evasion of rate limits results in:

1. Immediate blocklisting of addresses and IP ranges.
2. Disclosure to partner teams relying on faucet availability.
3. Investigation under the vulnerability disclosure framework.
4. Potential suspension from private beta programs.

Report suspected abuse or vulnerabilities to `security@nhbcoin.net` with reproduction details and request IDs.
