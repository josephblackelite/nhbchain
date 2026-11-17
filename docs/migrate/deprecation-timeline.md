# JSON-RPC Compatibility Decommission Timeline

The gateway's `/rpc` endpoint is a compatibility dispatcher that accepts JSON-RPC requests and
forwards them to the REST surfaces of the upstream services (see
[Migrating from the legacy JSON-RPC node](./monolith-to-gateway.md) for the method table).
`gateway/compat/deprecations.yaml`, embedded in the gateway binary, defines a four-phase
shutdown plan for it. This page describes that plan and what the gateway does with it today.

## Where the gateway is now

The embedded plan sets `currentPhase: phase-a`. The gateway does not compute the phase from a
date: it uses whichever phase the file names as current. With `phase-a` the default
compatibility mode is `enabled`. Moving to a later phase means changing that file and
rebuilding.

## The phases

| Phase | Offset | Default | Flag behaviour |
| ----- | ------ | ------- | -------------- |
| `phase-a` Compatibility warning window | T+0 | enabled | Flag available to opt out (disable compat in staging environments). |
| `phase-b` Staging opt-out | T+30d | enabled | Opt-in flag (`--compat-mode=disabled`) to turn off compat in non-production environments. |
| `phase-c` Compatibility disabled by default | T+60d | disabled | Opt-in flag (`--compat-mode=enabled`) required to keep compat for straggler integrations. |
| `phase-d` Compatibility removal | T+90d | removed | Compatibility flag removed; legacy dispatcher deleted and final release tagged. |

The offsets are labels in the plan file. The code contains no timer. Only `phase-a` has a
banner text. For `phase-d` the code treats the default as disabled (`DefaultMode` in
`gateway/compat/deprecation.go`); the removal itself is not implemented.

## Operator controls

* The gateway accepts `--compat-mode` or the environment variable `NHB_COMPAT_MODE`, with the
  values `enabled`, `disabled` or `auto`. The flag wins over the variable. An empty value or
  `auto` follows the current phase's default. Any other value stops the gateway at startup.
* `enabled` registers the `/rpc` route; `disabled` does not register it. At startup the
  gateway logs `compatibility mode: requested=<mode> effective=<mode> enabled=<bool>`.

## Client impact

Every response the dispatcher itself writes, including its JSON-RPC error responses, carries these
headers (`writeJSON` in `gateway/compat/compat.go`, text from the current phase). A request that
the rate limiter or the authenticator refuses before it reaches the dispatcher (HTTP 429, 401 or
403) does not carry them.

* `Warning: 299 - "Monolithic JSON-RPC compatibility will be sunset in 90 days. Migrate to the service APIs and monitor the deprecation timeline."`
  (any double quote inside the banner is replaced by a single quote)
* `Link: <https://docs.nhbchain.net/migrate/deprecation-timeline>; rel="deprecation"; type="text/html"`
* `X-NHB-Compat-Phase: Phase A – Compatibility warning window`

If a client keeps tooling that talks to `/rpc`, surface these headers to the teams that own it.
