# Lending Service Cookbook

The cookbook will evolve alongside the dedicated lending service. For now it
tracks the placeholder behaviour so integrators are aware of the current
limitations.

## Querying markets

The `GetMarket` and `ListMarkets` RPCs are exposed but return `UNIMPLEMENTED`
until the service is wired to the consensus query API.

## Managing positions

All write operations (`SupplyAsset`, `WithdrawAsset`, `BorrowAsset`,
`RepayAsset`) are intentionally disabled while the new lending engine is being
ported. Client applications should continue to rely on the legacy JSON-RPC flows
until an upcoming milestone replaces them with fully managed transactions.
