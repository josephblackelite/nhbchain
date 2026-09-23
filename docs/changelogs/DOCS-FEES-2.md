# DOCS-FEES-2

## Summary

- Added the fee transparency dashboard reference document (`docs/transparency/fees-dashboard.md`): what the Grafana dashboard reads, the on-chain data available as inputs, and a note on reconciling the transfer fee.
- Published reusable SQL queries (`docs/queries/fees.sql`, SQLite and ClickHouse) and curl examples for the fee query methods (`docs/api/fees-query.md`). No API collection (Postman or similar) contains the fee methods.
- Shipped a Grafana dashboard definition covering domain totals, fee p95, free-tier burn down, and treasury route balances.

## Release Notes

This update introduces a consolidated transparency surface area for finance and operations teams.
The node has no export command and does not create the ClickHouse tables, so loading the data is left to whoever runs the dashboard: `docs/api/fees-query.md` describes the `fees.applied` event and how to derive the columns the queries assume. Those queries power
`docs/transparency/fees-dashboard.md` and the Grafana dashboard stored in `ops/grafana/dashboards/fees.json`.
