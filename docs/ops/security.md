# Gateway TLS Enforcement

The gateway refuses to connect to plaintext JSON-RPC endpoints outside of the
`dev` environment. When `NHB_ENV` is set to any other value, service endpoints
must use `https://` URLs.

For operators that are in the process of migrating endpoints, set one of the
following configuration options to automatically upgrade existing `http://`
values to HTTPS:

- Set `security.autoUpgradeHTTP: true` in the gateway configuration file.
- Export `NHB_GATEWAY_AUTO_HTTPS=true` in the gateway environment.

When auto-upgrade is enabled the gateway will transparently rewrite the scheme
before proxying requests. This keeps production safe by enforcing TLS while
avoiding downtime during transition periods.
