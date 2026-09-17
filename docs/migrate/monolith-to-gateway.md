# Migrating from the Legacy JSON-RPC Node

The gateway keeps the public JSON-RPC surface available under `/rpc` while
backfilling requests to dedicated services. New integrations should migrate to
the service-specific REST and gRPC surfaces exposed behind the gateway.

## Method Mapping

| Legacy JSON-RPC method | Gateway path | HTTP method | Backend service |
| ---------------------- | ------------ | ----------- | --------------- |
| `lending_getMarket` | `/v1/lending/markets/get` | `POST` | `lendingd` |
| `lend_getPools` | `/v1/lending/pools` | `GET` | `lendingd` |
| `lend_createPool` | `/v1/lending/pools` | `POST` | `lendingd` |
| `lending_getUserAccount` | `/v1/lending/accounts/get` | `POST` | `lendingd` |
| `lending_supplyNHB` | `/v1/lending/supply` | `POST` | `lendingd` |
| `lending_withdrawNHB` | `/v1/lending/withdraw` | `POST` | `lendingd` |
| `lending_depositZNHB` | `/v1/lending/collateral/deposit` | `POST` | `lendingd` |
| `lending_withdrawZNHB` | `/v1/lending/collateral/withdraw` | `POST` | `lendingd` |
| `lending_borrowNHB` | `/v1/lending/borrow` | `POST` | `lendingd` |
| `lending_borrowNHBWithFee` | `/v1/lending/borrow/with-fee` | `POST` | `lendingd` |
| `lending_repayNHB` | `/v1/lending/repay` | `POST` | `lendingd` |
| `lending_liquidate` | `/v1/lending/liquidate` | `POST` | `lendingd` |
| `swap_submitVoucher` | `/v1/swap/voucher/submit` | `POST` | `swapd` |
| `swap_voucher_get` | `/v1/swap/voucher/get` | `POST` | `swapd` |
| `swap_voucher_list` | `/v1/swap/voucher/list` | `POST` | `swapd` |
| `swap_voucher_export` | `/v1/swap/voucher/export` | `POST` | `swapd` |
| `swap_limits` | `/v1/swap/limits` | `GET` | `swapd` |
| `swap_provider_status` | `/v1/swap/providers/status` | `GET` | `swapd` |
| `swap_burn_list` | `/v1/swap/burn/list` | `GET` | `swapd` |
| `swap_voucher_reverse` | `/v1/swap/voucher/reverse` | `POST` | `swapd` |
| `gov_getProposal` | `/v1/gov/proposals/get` | `POST` | `governd` |
| `gov_listProposals` | `/v1/gov/proposals` | `GET` | `governd` |
| `gov_getTally` | `/v1/gov/proposals/tally` | `POST` | `governd` |
| `gov_submitProposal` | `/v1/gov/proposals` | `POST` | `governd` |
| `gov_vote` | `/v1/gov/votes` | `POST` | `governd` |
| `gov_deposit` | `/v1/gov/deposits` | `POST` | `governd` |
| `consensus_status` | `/v1/consensus/status` | `GET` | `consensusd` |
| `consensus_validators` | `/v1/consensus/validators` | `GET` | `consensusd` |
| `consensus_block` | `/v1/consensus/block` | `POST` | `consensusd` |

Legacy JSON-RPC clients can continue posting to `/rpc`. The gateway converts the
request into the corresponding REST call shown above, forwards it to the
appropriate service, and wraps the response back into a JSON-RPC envelope.

## Authentication and Rate Limits

All mutating endpoints require a bearer token signed with the configured JWT/OAuth
secret. Supply the token via the standard `Authorization: Bearer <token>` header.
The gateway applies per-route rate limits which can be tuned in the configuration
file under the `rateLimits` section.
