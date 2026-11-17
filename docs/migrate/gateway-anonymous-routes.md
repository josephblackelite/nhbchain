# Migrating legacy anonymous gateway routes

The gateway authenticates requests with an HMAC-signed JWT. Anonymous access to a route is
an explicit opt-in in the gateway configuration. This page lists what the code requires
(`gateway/config/config.go`, `gateway/middleware/auth.go`, `cmd/gateway/main.go`).

## How authentication decides

* `auth.enabled` defaults to `true`, also when a YAML file is loaded but leaves it out
  (`applyAuthDefaults` in `gateway/config/config.go` runs before validation). Set it to `false`
  explicitly to let the middleware pass every request through without a token.
* `auth.allowAnonymous` defaults to `false`, whatever `auth.enabled` is.
* The authentication middleware is attached to a proxied route group only when that group is marked as
  requiring authentication (and always to the `/rpc` route when it is enabled). In `cmd/gateway/main.go` these groups require it: `/v1/lending`,
  `/v1/gov`, `/gov.v1.Msg`, `/v1/transactions` and `/v1/swap`. These do not: `/gov.v1.Query`,
  `/v1/consensus` and `/consensus.v1.ConsensusService`. `/healthz` and `/metrics` are separate
  handlers. The `/rpc` compatibility route has its own group (see the last section).
* On a group that requires it, a request skips the token check only when `auth.allowAnonymous`
  is true and the request path starts with one of the `auth.optionalPaths` entries
  (`strings.HasPrefix` on the full path). Any other request needs
  `Authorization: Bearer <token>`. The token must be HMAC-signed with `auth.hmacSecret`; the
  issuer and audience are checked when `auth.issuer` and `auth.audience` are set, and the
  scope named by `auth.scopeClaim` (default `scope`) must include the route's scope
  (`lending`, `gov` or `swap`; the scope is the `RequiredScopes` value of the group in
  `cmd/gateway/main.go`; `/v1/transactions` names no scope).

## Checklist

1. **Inventory anonymous consumers.** Find which routes are called without a token, for
   example `GET /v1/lending/markets` and `POST /v1/lending/markets/get` (both are mounted in
   `gateway/routes/lending.go`).
2. **Configure the gateway.** In the `auth` block set `allowAnonymous: true` and list each
   prefix under `optionalPaths`:

   ```yaml
   auth:
     enabled: true
     allowAnonymous: true
     optionalPaths:
       - /v1/lending/markets
   ```

   Because matching is by prefix, `/v1/lending/markets` also covers
   `/v1/lending/markets/get`. Use a trailing slash to cover only a subtree.
3. **Startup validation.** The gateway refuses to start when an `optionalPaths` entry is empty
   (`auth.optionalPaths[<i>] cannot be empty`), does not start with `/`
   (`auth.optionalPaths[<i>] must start with '/'`), or when `auth.enabled` and
   `auth.allowAnonymous` are both true and the list is empty
   (`auth.optionalPaths must list at least one entry when auth.allowAnonymous is true`).
4. **Redeploy and verify.** Restart every gateway replica, call each anonymous route without a
   token and confirm it succeeds, then call a protected route without a token and confirm the
   gateway answers `401 missing bearer token`. A bad token gets `401 invalid token`; a token
   without the route's scope gets `403 insufficient scope`.

Setting `allowAnonymous: false` or removing `optionalPaths` makes every authenticated route
require a token again.

## The `/rpc` compatibility route

The compatibility dispatcher (`/rpc`, see [monolith to gateway](./monolith-to-gateway.md)) is
mounted as its own group in `gateway/routes/router.go`, with the rate limit `compat` and the same
authenticator as the groups above, without a route scope. It therefore follows the rules in this
page: with `auth.enabled: true` a call needs a bearer token unless `auth.allowAnonymous` is true
and `/rpc` (or a prefix of it) is listed in `auth.optionalPaths`. Adding `/rpc` to
`optionalPaths` opens the whole route to callers without a token, and a call that arrives without a
token is not held to any scope by the dispatcher.

For a call that arrives with a token, the dispatcher checks the scope of the upstream that would
answer it before forwarding (`compat.ScopeGuard`, `cmd/gateway/main.go`): `lending`, `gov` or `swap`
for those upstreams, none for the consensus upstream. A missing scope answers the JSON-RPC error
`-32004 insufficient scope`. The caller's `Authorization` header is forwarded to the upstream.
Disable the route with `--compat-mode=disabled` if it must not be reachable.
