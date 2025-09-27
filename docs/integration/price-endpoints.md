# Price Endpoints

Third-party businesses (exchanges, merchants, and DEX operators) can poll `swapd` for indicative prices before initiating a mint
or redeem flow through the public gateway. The recommended workflow is:

1. Call the partner-facing gateway to request a voucher (unchanged).
2. Optionally query the `swapd` HTTP API to validate that the latest oracle snapshot matches internal expectations.
3. Proceed with the mint/redeem submission once the throttle check confirms available capacity.

## HTTP Resources

`swapd` exposes the following JSON endpoints that are safe to proxy into partner networks:

- `GET /healthz` – liveness probe.
- `GET /admin/policy` – current throttle window and limits.
- `POST /admin/throttle/check` – atomically reserve a mint or redeem slot. Payload: `{ "action": "mint" }` or `{ "action": "redeem" }`.

Partners that require historical price data can access the SQLite database directly (read-only) or consume periodic CSV exports
produced by operations from the `oracle_samples` and `oracle_snapshots` tables.

> **Note:** No raw customer PII is stored inside swapd. Only aggregated price and throttle metadata is persisted, ensuring the
> service can be safely mirrored to analytics environments.
