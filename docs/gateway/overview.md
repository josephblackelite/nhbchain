# Gateway Overview

> [!WARNING]
> The `/rpc` JSON-RPC compatibility endpoint is on a staged removal plan
> (`gateway/compat/deprecations.yaml`, `currentPhase: phase-a`). See the
> [JSON-RPC decommission timeline](../migrate/deprecation-timeline.md).

`cmd/gateway` is an HTTP reverse proxy built on `chi`. It authenticates and
rate-limits requests, then forwards them to backend services selected by URL
prefix. Source: `cmd/gateway/main.go`, `gateway/routes/router.go`.

## Routes

The route table is hard-coded in `cmd/gateway/main.go`. The route prefix is
stripped from the path before the request is proxied (`gateway/routes/proxy.go`).

| Prefix | Backend | Bearer token | Required scope | Rate-limit key |
| ------ | ------- | ------------ | -------------- | -------------- |
| `/v1/lending` | `lendingd` | required | `lending` | `lending` |
| `/v1/swap` | `swapd` | required | `swap` | `swap` |
| `/v1/gov` | `governd` | required | `gov` | `gov` |
| `/gov.v1.Msg` | `governd` | required | `gov` | `gov` |
| `/gov.v1.Query` | `governd` | none | none | `gov` |
| `/v1/transactions` | `consensusd` | required | none | `consensus` |
| `/v1/consensus` | `consensusd` | none | none | `consensus` |
| `/consensus.v1.ConsensusService` | `consensusd` | none | none | `consensus` |

Other endpoints served by the gateway itself:

- `GET /healthz` returns `200 ok`.
- `GET /metrics` serves the gateway's Prometheus registry (see Observability).
- `/rpc` is registered only when the compatibility dispatcher is enabled (see
  Compatibility mode).

Three prefixes have dedicated handlers in front of the generic proxy:

- `/v1/lending`: `GET /markets`, `POST /markets/get`, `POST /positions/get`,
  `POST /supply`, `POST /withdraw`, `POST /borrow`, `POST /repay`,
  `POST /collateral/deposit`, `POST /collateral/withdraw`, `POST /liquidate`.
  These call the lending service over gRPC (`gateway/routes/lending.go`).
- `/v1/transactions`: `POST /send`. See
  [Transaction submission via the gateway](./transactions.md).
- `/v1/consensus`: `GET /wallet/escrows/{escrowID}`
  (`gateway/routes/wallet.go`). Every other path under this prefix is proxied.

The four backend names `lendingd`, `swapd`, `governd` and `consensusd` are all
required. This repository builds `lendingd`, `governd`, `consensusd` and `p2pd`;
it contains no binary, chart or compose service for `swapd`, so the `/v1/swap`
route and the `swapd` default endpoint have no backend here. The name is
a literal identifier in `cmd/gateway/main.go` and must be used as the
`services[].name` key.

## CORS

Every response carries `Access-Control-Allow-Origin: *`,
`Access-Control-Allow-Methods: GET, POST, OPTIONS`,
`Access-Control-Allow-Headers: Content-Type, Authorization` and
`Access-Control-Allow-Credentials: false`. `OPTIONS` requests are answered with
`204` (`cmd/gateway/main.go`, `gateway/middleware/cors.go`).

## Authentication

Bearer tokens are HMAC-signed JWTs (`gateway/middleware/auth.go`):

- Only HMAC signing methods are accepted, verified with `auth.hmacSecret`.
- `auth.issuer` and `auth.audience`, when set, must match the token's `iss` and
  `aud` claims.
- Scopes are read from the claim named by `auth.scopeClaim` (default `scope`),
  either as a space-separated string or as an array.
- Clock-skew leeway is `auth.clockSkew` (default 2 minutes, capped at 2 minutes).
- Responses: `401 missing bearer token`, `401 invalid token`,
  `403 insufficient scope`.

Rate limiting runs before authentication on each route.

### Secure defaults

`auth.enabled` defaults to `true` and `auth.allowAnonymous` defaults to `false`
(`gateway/config/config.go`). Anonymous access requires
`auth.allowAnonymous: true` **and** at least one entry in `auth.optionalPaths`
(the loader rejects the config otherwise). Each optional path must start with
`/` and is matched as a string prefix against the request path. Anonymous access
only affects routes that require a token in the table above.

```yaml
auth:
  hmacSecret: "<shared HMAC secret>"
  allowAnonymous: true
  optionalPaths:
    - /v1/lending/markets
    - /v1/lending/markets/get
```

The gateway does not expand environment variables inside the YAML file; put the
literal secret in the file (for example through a templated ConfigMap, as the
Helm chart does with `secrets.gatewayHMAC`).

The loader returns `auth.enabled must be explicitly set for sensitive
deployments` when `auth.enabled` is omitted and any of `security.autoUpgradeHTTP`,
`security.tlsCertFile`, `security.tlsKeyFile` or `security.tlsClientCAFile` is
set. Set `auth.enabled: true` or `false` explicitly in those deployments.

`deploy/compose/config/gateway.yaml` sets `auth.enabled: false` for local use;
do not carry that into shared environments.

## Configuration

The gateway takes `--config <path>` to a YAML file (`gateway/config/config.go`).
Defaults apply for every omitted key:

| Key | Default |
| --- | ------- |
| `listen` | `:8080` |
| `readTimeout`, `writeTimeout` | `30s` |
| `idleTimeout` | `120s` |
| `observability.serviceName` | `nhb-gateway` |
| `observability.metrics`, `.tracing`, `.logRequests` | `true` |
| `observability.metricsPrefix` | `gateway` |
| `auth.enabled` | `true` |
| `auth.scopeClaim` | `scope` |
| `auth.allowAnonymous` | `false` |
| `auth.clockSkew` | `2m` |
| `security.allowInsecure` | `false` |

Other keys: `services[]` (`name`, `endpoint`, `timeout`, `insecureSkipVerify`),
`rateLimits[]` (`id`, `requestsPerMinute` or `ratePerSecond`, `burst`, `paths`),
`auth.hmacSecret`, `auth.issuer`, `auth.audience`, `auth.optionalPaths`,
`security.autoUpgradeHTTP`, `security.tlsCertFile`, `security.tlsKeyFile`,
`security.tlsClientCAFile`. A relative TLS path is resolved against the config
file's directory. `cmd/gateway` reads only `name` and `endpoint` from each
`services[]` entry and only `id`, `requestsPerMinute`/`ratePerSecond` and `burst`
from each `rateLimits[]` entry; `timeout`, `insecureSkipVerify` and `paths` are
parsed but not used. Backend HTTP clients use fixed timeouts (10 s for the
transaction route, 15 s for the compatibility dispatcher).

Flags and environment variables:

| Name | Effect |
| ---- | ------ |
| `--compat-mode` (`enabled`, `disabled`, `auto`) | Overrides `NHB_COMPAT_MODE` |
| `NHB_COMPAT_MODE` | Same values; default `auto` |
| `--allow-insecure` | Dev only, see Transport security |
| `NHB_ENV` | Environment name; `dev` relaxes the HTTPS rules |
| `NHB_GATEWAY_LENDING_URL`, `NHB_GATEWAY_SWAP_URL`, `NHB_GATEWAY_GOV_URL`, `NHB_GATEWAY_CONSENSUS_URL` | Backend endpoint overrides |
| `NHB_GATEWAY_AUTO_HTTPS` | Boolean; overrides `security.autoUpgradeHTTP`. Has no effect on `http://` endpoints, see Transport security |
| `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_INSECURE` | OpenTelemetry export (see [Observability](../ops/observability.md)) |

### Backend endpoints

Built-in defaults, overridden first by the `NHB_GATEWAY_*_URL` variables and then
by matching `services[]` entries in the YAML file:

| Service | Default endpoint |
| ------- | ---------------- |
| `lendingd` | `http://127.0.0.1:7101` |
| `swapd` | `http://127.0.0.1:7102` |
| `governd` | `http://127.0.0.1:7103` |
| `consensusd` | `http://127.0.0.1:7104` |

### Rate limits

Limits are token buckets keyed by rate-limit key and client identity. The client
identity is, in order: the `X-API-Key` header, `X-Real-IP`, `X-Forwarded-For`,
then the remote address. Exceeding a bucket returns `429`. `requestsPerMinute`
is converted to a per-second rate; `ratePerSecond` takes precedence when both
are set. If `rateLimits` is empty, the gateway uses these built-in limits:
`lending` 2/s burst 20, `swap` 1/s burst 10, `gov` 1/s burst 10, `consensus`
4/s burst 40. A route whose key has no entry is not limited.

### Transport security

The gateway checks these rules at startup (`cmd/gateway/main.go`):

- Unless `NHB_ENV=dev`, every backend endpoint (including the built-in defaults
  above) must use `https://`, otherwise it exits with
  `<service> endpoint must use https:// when NHB_ENV=<env>`. This check runs
  before the auto-upgrade step, so `security.autoUpgradeHTTP: true` and
  `NHB_GATEWAY_AUTO_HTTPS=true` cannot migrate an `http://` endpoint: outside
  `dev` the process has already exited, and in `dev` `EnforceSecureScheme`
  returns `http://` endpoints unchanged (`gateway/config/config.go`). Configure
  `https://` endpoints directly. In `dev`, plaintext is allowed.
- The gateway itself needs `security.tlsCertFile` and `security.tlsKeyFile` (TLS
  1.2 minimum). Setting `security.tlsClientCAFile` requires and verifies client
  certificates. Without a certificate and key it exits unless
  `security.allowInsecure: true` or `--allow-insecure` is set.
- `security.allowInsecure` and `--allow-insecure` require `NHB_ENV=dev` and a
  loopback `listen` address (`127.0.0.1`, `::1` or `localhost`).

## Compatibility mode

`/rpc` accepts JSON-RPC (single or batch, body limit 1 MiB) and translates
selected method names to backend REST calls through the table in
`gateway/compat/mapping.go` (for example `lending_getMarket`, `swap_limits`,
`gov_getProposal`, `consensus_status`). Unknown methods return `-32601`.

- `--compat-mode` / `NHB_COMPAT_MODE` accept `enabled`, `disabled` or `auto`.
  `auto` follows the active phase in `deprecations.yaml`; `phase-a` enables it.
- Responses carry `Warning`, `Link` and `X-NHB-Compat-Phase` headers.
- The `/rpc` handler is registered outside the per-route authentication and
  rate-limit middleware, and it does not forward the caller's headers to the
  backend.

## Observability

- `GET /metrics` exposes `<metricsPrefix>_requests_total{route,method,status}`
  and `<metricsPrefix>_request_duration_seconds{route,method}`
  (`gateway/middleware/observability.go`). The request counters are recorded
  only when `observability.metrics` or `observability.tracing` is true.
- The gateway wraps its handler in `otelhttp` only when `observability.tracing`
  is true, and injects trace context into proxied requests.
- Request logging is controlled by `observability.logRequests`.
