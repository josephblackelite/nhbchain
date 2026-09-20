# Release Checklist

## Production configuration safety rails

`scripts/verify_prod_config.sh` checks a production TOML file before a release.
It needs `python3` with `tomllib` (Python 3.11+) or `tomli`. It is also run by
`scripts/bringup_production_stack.sh` against `/etc/nhbchain/config.toml`
([One-shot deployment](../deploy/one-shot-deploy.md)).

```bash
bash scripts/verify_prod_config.sh -c config/prod.toml
```

The script prints each violation as `[verify_prod_config] <message>` on stderr
and exits non-zero. It looks a key up the way the node's loader matches it: by
exact name first, then case-insensitively, and only in the places listed below.
`scripts/bugcheck.sh` also runs it against `config/prod.toml`.

### TLS must stay enabled

- `network_security.AllowInsecure` must be present and `false`.
- These must be non-empty strings: `network_security.ServerTLSCertFile`,
  `ServerTLSKeyFile`, `ClientTLSCertFile`, `ClientTLSKeyFile`, `ClientCAFile`,
  `ServerCAFile`.
- `ListenAddress` and `RPCAddress` (top-level keys) must not bind to an
  unspecified address (`0.0.0.0`, `::`, `*` or empty).
- The RPC listener must be one of two shapes, judged from the top-level keys:
  - `RPCAllowInsecure = true`: only when `RPCAddress` is a loopback address
    (TLS then terminates in a reverse proxy in front of the node), and
    `RPCAllowInsecureUnspecified` must not be `true`;
  - otherwise `RPCTLSCertFile`, `RPCTLSKeyFile` and `RPCTLSClientCAFile` must
    be non-empty strings (the node terminates TLS itself).

  The node itself refuses plaintext RPC on any other address (`rpc/http.go`).

### Pro-rate guardrails cannot be disabled

- `global.Loyalty.Dynamic.EnforceProRate` must be `true`.
- `global.Loyalty.Dynamic.EnableProRate` must be `true`.

In the node, `SetGlobalConfig` refuses to start when `NHB_ENV` is `prod` (the
default when `NHB_ENV` is unset) with `EnforceProRate` true and `EnableProRate`
false (`core/node.go`).

### Fee routing wallets must be set

- `global.Fees.OwnerWallet` must be a non-empty string.
- `global.Fees.Assets` must be a non-empty list, and every entry must have a
  non-empty `OwnerWallet`.

The script checks that a value is present, not that it is a valid address;
`config.ValidateConfig`, `Node.SetGlobalConfig` and the fee policy builder decide
what the node accepts (a malformed wallet stops startup, see [Fee operations
runbook](./fees.md)).

### Staking emission cap must be positive

- `global.Staking.MaxEmissionPerYearWei` must be defined and parse as an integer
  greater than zero (`int(value, 0)`, so `0x` prefixes are accepted).

### Modules must not be paused

- `global.Pauses` must be defined as a table, and no value in it may be `true`.

### Repository artifacts

After the checks above, the script also scans `config.toml`,
`deploy/compose/config`, `deploy/helm/values/{prod,staging,dev}` and
`deploy/helm/consensusd/values.yaml` in the repository it lives in. It fails if
any of them contains `0.0.0.0` or `AllowInsecure = true` on a non-comment line.
The chart-level `values.yaml` files other than `consensusd`'s are not scanned.
