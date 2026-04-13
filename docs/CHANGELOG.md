# Documentation Changelog

## Unreleased

- Documented the RPC trusted-proxy policy, per-source transaction quota, and timeout/TLS requirements across networking, overview, governance, and operations guides so operators know how to harden their nodes.
- Updated example workspace materials (`README`, `.env.example`, Postman collection) to surface the new RPC configuration knobs and mempool guidance for local testing.
- Added integration runbook notes and migration steps for SDK consumers to handle HTTP 429/`-32020` responses and align mempool limits.
- Aligned loyalty and treasury docs to the founder fixed-supply model: protocol base rewards now default to 50 bps (0.50%), rewards are treasury/paymaster-funded, and ZNHB is documented as fixed-supply rather than inflationary.
