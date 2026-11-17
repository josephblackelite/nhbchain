# Creator Studio

`examples/creator-studio` is a Next.js 13 app (workspace member `@nhb/examples-creator-studio`). It was written for the creator flow publish, tip, stake, payout. Today only the payout-ledger read still works; the write flows are retired in the node.

## Current status

The node's RPC layer answers `creator_publish`, `creator_tip`, `creator_stake`, `creator_unstake` and `creator_payouts` with `claim: true` with HTTP `410` (`creatorRPCDisabledMessage` in `rpc/creator_handlers.go`). `creator_payouts` with `claim` unset or false is live and requires `Authorization: Bearer <token>`.

There is no creator transaction type in `core/types/transaction.go`, and nothing in the node calls the `Node.CreatorPublish`, `CreatorTip`, `CreatorStake`, `CreatorUnstake` or `CreatorClaimPayouts` methods, so there is currently no way to publish, tip, stake or claim.

In the UI (`pages/index.tsx`) the Publish, Tip, Stake and Claim controls are disabled and only log a note that the method is retired. Only "Refresh Ledger" makes a request.

## Running it

```bash
cd examples
cp .env.example creator-studio/.env.local
yarn install
cd creator-studio
yarn dev
```

`NHB_RPC_URL` (default `http://localhost:8545` if unset) and `NHB_RPC_TOKEN` are read by the proxy route. `.env.example` sets both.

## The RPC proxy

The browser calls `POST /api/rpc` (`pages/api/rpc.ts`) with `{method, params}`. The route:

- rejects anything but `POST` (`405`), a missing `method` (`400`), and any method outside a fixed allowlist (`403`): `creator_publish`, `creator_tip`, `creator_stake`, `creator_payouts` (`creator_unstake` is not in the list);
- forwards the call to `NHB_RPC_URL` as JSON-RPC 2.0, adding `Authorization: Bearer <token>` when a token is set. The token is `NHB_RPC_TOKEN`, falling back to `NEXT_PUBLIC_NHB_RPC_TOKEN`;
- returns the node's status and body.

The proxy does not authenticate the visitor. Every method in its allowlist is a write on behalf of an address the caller supplies, so do not expose this app on a public network without adding per-visitor authentication.

## `creator_payouts`

Request parameter: a single object `{"caller": "<creator bech32 address>", "claim": false}`. Exactly one parameter object is required (`exactly one parameter object expected`); an invalid `caller` gives `invalid caller address`.

Result (`creatorPayoutsResult`): `creator`, `pending`, `totalTips`, `totalYield` (decimal strings, base units), `lastPayout` (Unix seconds), `claimed` (always `"0"` in the read path). The UI labels these amounts "wei".

## Event names

`native/creator/events.go` defines these event types: `creator.content.published`, `creator.content.tipped`, `creator.fan.staked`, `creator.fan.unstaked`, `creator.payout.accrued`. They are emitted by the creator engine, which is only reachable through the `Node.Creator*` methods listed above; since no RPC handler or transaction calls those methods, no live path emits them.

For the module's design, see [`docs/creator/overview.md`](../creator/overview.md).
