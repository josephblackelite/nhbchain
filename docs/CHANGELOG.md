# Documentation Changelog

## Unreleased

- Documented the RPC trusted-proxy policy, per-source transaction quota, and timeout/TLS requirements across networking, overview, governance, and operations guides so operators know how to harden their nodes.
- Updated example workspace materials (`README`, `.env.example`, Postman collection) to describe the RPC configuration knobs and mempool guidance for local testing. The `NHB_*` variables in `examples/.env.example` are documentation placeholders that no code reads; the node is configured through `config.toml`. The values there are suggestions, not code defaults (code default `[mempool] MaxTransactions` is 4000, `config/config.go` `DefaultMempoolMaxTransactions`; the shipped `config.toml` sets 5000).
- Added integration runbook notes and migration steps for SDK consumers to handle HTTP 429/`-32020` responses and align mempool limits.
- Aligned loyalty and treasury docs to the founder fixed-supply model: protocol base rewards default to 50 bps (0.50%), and ZNHB is documented as fixed-supply (it cannot be minted).
