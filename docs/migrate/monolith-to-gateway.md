# Migrating from the Legacy JSON-RPC Node

The gateway (`cmd/gateway`) can expose a JSON-RPC endpoint at `/rpc` that translates each
request into a REST call on an upstream service. New integrations should call the
service-specific REST and gRPC routes the gateway proxies directly. The `/rpc` endpoint is
controlled by `--compat-mode` / `NHB_COMPAT_MODE` and follows the plan in the
[decommission timeline](./deprecation-timeline.md).

## Method mapping

These are the lending, governance and consensus entries of `DefaultMappings` in
`gateway/compat/mapping.go`; the map contains further entries that this page does not list. The dispatcher sends the JSON-RPC
`params` value as the request body (or `{}` when there are no params) for `POST`, and sends no
body for `GET`. It calls the upstream at the path shown, under the base URL configured for that
upstream.

| Legacy JSON-RPC method | Gateway path | HTTP method | Upstream |
| ---------------------- | ------------ | ----------- | -------- |
| `lending_getMarket` | `/v1/lending/markets/get` | `POST` | lending |
| `lend_getPools` | `/v1/lending/pools` | `GET` | lending |
| `lend_createPool` | `/v1/lending/pools` | `POST` | lending |
| `lending_getUserAccount` | `/v1/lending/accounts/get` | `POST` | lending |
| `lending_supplyNHB` | `/v1/lending/supply` | `POST` | lending |
| `lending_withdrawNHB` | `/v1/lending/withdraw` | `POST` | lending |
| `lending_depositZNHB` | `/v1/lending/collateral/deposit` | `POST` | lending |
| `lending_withdrawZNHB` | `/v1/lending/collateral/withdraw` | `POST` | lending |
| `lending_borrowNHB` | `/v1/lending/borrow` | `POST` | lending |
| `lending_borrowNHBWithFee` | `/v1/lending/borrow/with-fee` | `POST` | lending |
| `lending_repayNHB` | `/v1/lending/repay` | `POST` | lending |
| `lending_liquidate` | `/v1/lending/liquidate` | `POST` | lending |
| `gov_getProposal` | `/v1/gov/proposals/get` | `POST` | governance |
| `gov_listProposals` | `/v1/gov/proposals` | `GET` | governance |
| `gov_getTally` | `/v1/gov/proposals/tally` | `POST` | governance |
| `gov_submitProposal` | `/v1/gov/proposals` | `POST` | governance |
| `gov_vote` | `/v1/gov/votes` | `POST` | governance |
| `gov_deposit` | `/v1/gov/deposits` | `POST` | governance |
| `consensus_status` | `/v1/consensus/status` | `GET` | consensus |
| `consensus_validators` | `/v1/consensus/validators` | `GET` | consensus |
| `consensus_block` | `/v1/consensus/block` | `POST` | consensus |

The mapping only defines what the dispatcher sends: a plain HTTP request to the upstream's base
URL plus the path in the table. The code does not tie a base URL to a binary in this repository.
`services/lendingd` and `services/governd` serve gRPC only (`grpc.NewServer` in their `main.go`; the
sample configs listen on `:50053` and `:50061`), `cmd/consensusd` serves gRPC only, and nothing in
this repository serves the REST paths above. The gateway's own `/v1/lending` routes
(`gateway/routes/lending.go`) turn REST calls into gRPC calls to the lending service, but the `/rpc`
dispatcher does not use them. A call through `/rpc` therefore succeeds only when whatever answers
at the configured upstream URL implements the path.

## Behaviour of `/rpc`

* It accepts one JSON-RPC object or a batch array of at most 10 calls (a larger batch is refused
  as a whole with `-32600 batch of <n> calls exceeds the limit of 10`, and nothing is forwarded).
  It reads at most 1 MiB of the request body; a longer body is cut at that size and then fails to
  decode.
* Error codes (`gateway/compat/compat.go`):
  * `-32700` when the request body cannot be read or is not valid JSON (`read body`,
    `decode request`, `decode batch`).
  * `-32600 empty request body` when the body is empty or only whitespace.
  * `-32601 method not found` for a method with no mapping.
  * `-32001 service unavailable` when the mapping names an upstream that is not registered
    in the dispatcher.
  * `-32004 insufficient scope` when the caller's token lacks the scope of the upstream that
    would answer (see below). In a batch each call is judged on its own.
  * `-32602` when the upstream HTTP request cannot be built (`build request: ...`).
  * `-32002` when the upstream cannot be reached (message `upstream error: <cause>`).
  * `-32003 read response: ...` when the upstream response body cannot be read.
  * `-32000 upstream error` when the upstream answers with HTTP status 400 or above, with the
    upstream body in `data`.
* A successful upstream body is returned unchanged as the JSON-RPC `result`. An empty body
  becomes `null`.
* Each call to an upstream uses a 15 second HTTP client timeout.
* The caller's `Authorization` header is sent to the upstream with each call, so an upstream that
  checks it sees the caller's own credential.

## Upstream endpoints and configuration

The gateway keeps a base URL for each of four upstreams, with a default for each
(`ensureServiceConfig` in `cmd/gateway/main.go`). Defaults for
the three described on this page are `http://127.0.0.1:7101` (`lendingd`), `http://127.0.0.1:7103`
(`governd`) and `http://127.0.0.1:7104` (`consensusd`). They can be overridden with
`NHB_GATEWAY_LENDING_URL`, `NHB_GATEWAY_GOV_URL` and `NHB_GATEWAY_CONSENSUS_URL`. The fourth
upstream has its own fixed name, default and environment variable in the same function.

The `services` list in the gateway YAML overrides an endpoint too, and a YAML entry wins over
the environment variable. Each entry has `name`, `endpoint`, `timeout` and
`insecureSkipVerify`, but only `name` and `endpoint` have any effect:

* `name` must be exactly one of the fixed upstream names (`lendingd`, `governd`, `consensusd`,
  or the fourth name in `ensureServiceConfig`). An entry with any other name is accepted but
  never used. Entries with an empty `name` or `endpoint` are skipped.
* `timeout` is parsed (`gateway/config/config.go`) but never applied: the gateway builds each
  upstream as `compat.Service{Name, BaseURL}` (`cmd/gateway/main.go`) and the `/rpc` HTTP
  client always uses a fixed 15 second timeout (`NewDispatcher` in `gateway/compat/compat.go`).
* `insecureSkipVerify` is parsed but not read anywhere in the gateway code.

Unless `NHB_ENV=dev`, every upstream URL must use `https://`; this check runs when the
endpoints are collected, before `security.autoUpgradeHTTP` can apply, so an `http://` URL is
rejected outside dev even with auto-upgrade enabled.

## Authentication and rate limits

Routes under `/v1/lending`, `/v1/gov`, `/gov.v1.Msg` and `/v1/transactions` (and the other
route groups marked `RequireAuth` in `cmd/gateway/main.go`) require
`Authorization: Bearer <token>`, an HMAC-signed JWT checked against `auth.hmacSecret`. Groups
with `RequiredScopes` also require that scope (`lending` for `/v1/lending`, `gov` for the gov
groups). The
`/v1/consensus`, `/consensus.v1.ConsensusService` and `/gov.v1.Query` groups do not require a
token. Details and the anonymous-access settings are in
[gateway anonymous routes](./gateway-anonymous-routes.md).

Rate limits come from the `rateLimits` list in the gateway YAML. Each entry has an `id`, a
rate (`ratePerSecond`, or `requestsPerMinute` divided by 60 when `ratePerSecond` is not above
zero) and a `burst`. The `id` is the route group's rate-limit key: `lending`, `gov` and
`consensus` (`/v1/transactions` uses `consensus`) among the groups described here. Entries
with an empty `id` are skipped. The entries also have a `paths` field; it is parsed but never
used, since only the `id`, rate and `burst` are read (`cmd/gateway/main.go`). When no entry
has an `id` the gateway uses 2/s burst 20 for `lending`, 1/s burst 10 for `gov`, and 4/s
burst 40 for `consensus` (plus a default for the group not described here).

The `/rpc` route is registered as its own group (`gateway/routes/router.go`). A request to it
goes through the rate limit under the id `compat` (the gateway adds 2/s burst 20 when the YAML has
no entry with that id; `gateway/config.yaml` lists the same values) and then the same
authenticator as the routes above, with no scope required at that step. Whether a token is
needed follows the same rules as for the other routes: with `auth.enabled: true` a token is
required unless `auth.allowAnonymous` is true and the path matches `auth.optionalPaths`
([gateway anonymous routes](./gateway-anonymous-routes.md)). The dispatcher then holds each call to the scope
of the upstream that would answer it (`compat.ScopeGuard`, `cmd/gateway/main.go`): `lending` for
the lending upstream, `gov` for the governance upstream and `swap` for the swap upstream; the
consensus upstream asks for none. A request that came in without a token (authentication off, or
an open path) is not held to scopes here.
