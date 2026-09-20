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
and exits non-zero. Key names are matched exactly as written below (TOML keys are
case-sensitive).

### TLS must stay enabled

- `network_security.AllowInsecure` must be `false`.
- `RPCAllowInsecure` must be `false`. The script looks for it at the top level,
  under `[global]`, under `[network_security]`, and under `[global.Staking]`, and
  uses the first one found.
- These must be non-empty strings: `network_security.ServerTLSCertFile`,
  `ServerTLSKeyFile`, `ClientTLSCertFile`, `ClientTLSKeyFile`, `ClientCAFile`,
  `ServerCAFile`.
- RPC TLS paths must be set, at the top level (`RPCTLSCertFile`, `RPCTLSKeyFile`,
  `RPCTLSClientCAFile`) or in one of the other places the script checks
  (`global.RPC.TLSCertFile` / `TLSKeyFile` / `TLSClientCAFile`,
  `global.Staking.RPCTLS*`, `network_security.RPCTLS*`).
- `ListenAddress` and `RPCAddress` must not bind to an unspecified address
  (`0.0.0.0`, `::` or `*`).

### Pro-rate guardrails cannot be disabled

- `global.Loyalty.Dynamic.EnforceProRate` must be `true`.
- `global.Loyalty.Dynamic.enableprorate` (lower case, as the script spells it)
  must be `true`.

In the node, `SetGlobalConfig` refuses to start when `NHB_ENV` is `prod` (the
default when `NHB_ENV` is unset) with `EnforceProRate` true and `EnableProRate`
false (`core/node.go`). The loader reads the key as `EnableProRate`.

### Fee routing wallets must be set

- `global.Fees.owner_wallet` must be a non-empty string.
- `global.Fees.Assets` must be a non-empty list, and every entry must have a
  non-empty `owner_wallet`.

The script checks the snake_case spelling `owner_wallet`. The node's loader
(`config.Fees`, `config.FeeAsset`) reads `OwnerWallet`, and it ignores keys it
does not recognise, so a file can satisfy the script without setting the value
the node uses. `defaultGlobalConfig` supplies non-empty default wallets when
`OwnerWallet` is absent.

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
