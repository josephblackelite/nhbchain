# DOCS-FEES-2

## Summary

- Added the network-wide fee transparency dashboard reference document with methodology, metrics, and reconciliation checklist.
- Published reusable SQL queries and API collection examples for exporting fee data into SQLite and ClickHouse.
- Shipped a Grafana dashboard definition covering domain totals, fee p95, free-tier burn down, and treasury route balances.

## Release Notes

This update introduces a consolidated transparency surface area for finance and operations teams.
Follow the ingestion steps in `docs/api/fees-query.md` to hydrate the queries powering
`docs/transparency/fees-dashboard.md` and the Grafana dashboard stored in `ops/grafana/dashboards/fees.json`.
