# Migrating legacy anonymous gateway routes

Historically the gateway allowed a handful of REST endpoints to be queried
without a bearer token by default. Operators often relied on this behaviour for
public dashboards (lending market data) or health probes. As part of the
authentication hardening work, `auth.allowAnonymous` now defaults to `false`
whenever `auth.enabled` is set to `true`, and the configuration loader requires
explicit opt-in for every anonymous prefix.

Use the checklist below to preserve any existing unauthenticated traffic while
upgrading:

1. **Inventory anonymous consumers.** Audit load balancer logs and dashboards to
   determine which routes are accessed without a token. Common examples include
   `GET /v1/lending/markets` and the derived `POST /v1/lending/markets/get`
   lookup. Document every prefix that must remain public.
2. **Annotate the gateway configuration.** Under the `auth` block, set
   `allowAnonymous: true` and list each prefix beneath `optionalPaths`. Prefixes
   are matched using `strings.HasPrefix`, so include a trailing slash if you
   intend to cover an entire subtree.
   ```yaml
   auth:
     enabled: true
     allowAnonymous: true
     optionalPaths:
       - /v1/lending/markets
       - /v1/lending/markets/get
   ```
3. **Redeploy the gateways.** Roll out the updated configuration (or Helm
   values) to every gateway replica. The service will refuse to start if the
   optional path list is missing or malformed when anonymous access is enabled,
   preventing accidental regressions.
4. **Verify access.** Issue requests against each anonymous endpoint to confirm
   they succeed without a bearer token, and re-run your authentication smoke
   tests to ensure protected APIs still enforce JWT validation.

Removing `optionalPaths` (or setting `allowAnonymous: false`) now forces the
entire REST surface to require authentication, aligning the gateway with the new
secure-by-default posture.
