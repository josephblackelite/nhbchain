# Release Checklist

## Production Configuration Safety Rails

The `scripts/verify_prod_config.sh` helper validates the production TOML
configuration before every release. Run it locally with the exact file that
will be deployed:

```bash
bash scripts/verify_prod_config.sh -c config/prod.toml
```

The script fails fast when any of the following guardrails are missing. Update
the configuration and rerun the command until it passes.

### TLS must stay enabled

* `network_security.AllowInsecure` **must** remain `false`.
* `RPCAllowInsecure` (or its legacy placement inside the `global` block)
  **must** remain `false`.
* All TLS certificate, private key, and CA bundle paths are required. Populate
  the `network_security.*` and `RPCTLS*` fields with the real file locations
  before shipping.

**Remediation:** provision the correct certificates and secrets or point the
config at the mounted paths provided by the secret management system. Never set
`AllowInsecure` to `true` in production.

### Pro-rate guardrails cannot be disabled

* Both `global.loyalty.Dynamic.enableprorate` and
  `global.loyalty.Dynamic.EnforceProRate` must be `true`.

**Remediation:** if a testing override disabled either flag, revert the change
before cutting the release. Production nodes refuse to boot without the
proration queue.

### Fee routing wallets must be set

* `global.fees.owner_wallet` must contain the main fee collector address.
* Every entry in `[[global.fees.assets]]` needs a non-empty `owner_wallet`.

**Remediation:** fill in the treasury addresses for every asset that will be
settled on mainnet. Leaving these blank forces the check to fail.

### Staking emission caps must be positive

* `global.staking.MaxEmissionPerYearWei` must parse to an integer greater than
  zero.

**Remediation:** confirm the yearly emission target with the economics team and
update the value. A zero or negative cap breaks reward distribution.

### Critical modules cannot be paused

* No value under `global.pauses` may be `true`.

**Remediation:** ensure the release config unpauses any modules that were
halted for incident response or testing. Paused modules keep consensus, mempool,
staking, or other services offline indefinitely.

Once all conditions pass, commit the updated configuration alongside the release
artifacts so CI enforces the same gates.
