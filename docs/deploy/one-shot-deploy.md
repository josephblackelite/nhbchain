# One-Shot Deployment Script

`scripts/run_nhbcoin_node.sh` is a one-line wrapper that `exec`s
`scripts/bringup_production_stack.sh` with the same arguments. That script builds
and installs a single node (`bin/nhb`, run by the `nhb.service` systemd unit) on
a Linux host. It does not build or install `consensusd`, `p2pd`, the gateway or
any other service.

## What it does

The script prints seven steps (`scripts/bringup_production_stack.sh`):

1. Creates the service group and user (`nhb` by default, system account, shell
   `/usr/sbin/nologin`) and the directories `INSTALL_ROOT`, `INSTALL_ROOT/bin`,
   `ETC_DIR`, `ETC_DIR/examples` and `STATE_DIR`.
2. `rsync -a --delete` of the repository into `INSTALL_ROOT`, excluding `.git/`,
   `nhb-data/`, `nhb-data-local/`, `node.log`, `*.db`, `.svelte-kit/` and `build/`.
3. Installs `deploy/env/node.env.example` to `ETC_DIR/examples/` if it is not
   already there.
4. Requires `ETC_DIR/config.toml` and `ETC_DIR/node.env` to exist, then runs
   `scripts/verify_prod_config.sh -c ETC_DIR/config.toml`
   ([checks](../ops/release-checklist.md)).
5. Stops `nhb.service`, runs `pkill -f "/bin/nhb --config"`, then
   `scripts/build.sh` in `INSTALL_ROOT`, which runs `go mod tidy` and builds
   `bin/nhb` (`./cmd/nhb`) and `bin/nhb-cli` (`./cmd/nhb-cli`).
6. Renders `deploy/systemd/nhb.service` into `SYSTEMD_DIR` (substituting the
   paths, user and group), runs `systemctl daemon-reload`, and chowns
   `INSTALL_ROOT` and `STATE_DIR` to the service user. If `--reset-state` was
   given, it moves `INSTALL_ROOT/nhb-data` and `INSTALL_ROOT/nhb-data-local` (if
   present) into `STATE_DIR/backup-<UTC timestamp>/`. State is moved, not deleted.
7. `systemctl enable nhb.service`, then `systemctl restart nhb.service` unless
   `--skip-start` was given.

Required commands on the host: `go`, `python3`, `rsync`, `systemctl`, `install`
(and `sudo` unless run as root).

## Usage

```bash
bash scripts/run_nhbcoin_node.sh --reset-state   # move existing chain state aside first
bash scripts/run_nhbcoin_node.sh                 # keep existing state
bash scripts/run_nhbcoin_node.sh --skip-start    # install and build only
```

Options:

| Option | Default |
| ------ | ------- |
| `--install-root <path>` | `/opt/nhbchain` |
| `--etc-dir <path>` | `/etc/nhbchain` |
| `--state-dir <path>` | `/var/lib/nhbchain` |
| `--systemd-dir <path>` | `/etc/systemd/system` |
| `--service-user <name>` | `nhb` |
| `--service-group <name>` | `nhb` |
| `--reset-state` | off |
| `--skip-start` | off |
| `-h`, `--help` | |

The same values can be supplied as the environment variables `INSTALL_ROOT`,
`ETC_DIR`, `STATE_DIR`, `SYSTEMD_DIR`, `SERVICE_USER` and `SERVICE_GROUP`. Any
other option exits with `unknown option: <argument>` and a non-zero status.

## Required config

Both files must exist before you run the script:

- `/etc/nhbchain/config.toml`
- `/etc/nhbchain/node.env`

The systemd unit (`deploy/systemd/nhb.service`) runs
`/opt/nhbchain/bin/nhb --config /etc/nhbchain/config.toml` with
`WorkingDirectory=/opt/nhbchain`, `EnvironmentFile=/etc/nhbchain/node.env`,
`Restart=on-failure`, `RestartSec=5` and `LimitNOFILE=65535`.

`deploy/env/node.env.example` lists the variables the template sets:
`NHB_ENV`, `NHB_VALIDATOR_PASS`, `NHB_RPC_JWT_SECRET` and the
`OTEL_EXPORTER_OTLP_*` variables. `NHB_VALIDATOR_PASS` is the validator keystore
passphrase read at startup, and the repository's `config.toml` reads the JWT
secret for `[RPCJWT]` from `NHB_RPC_JWT_SECRET` (`HSSecretEnv`).

The script does not generate secrets; it only checks that the two files exist.

## Resetting state

`--reset-state` only moves `nhb-data` and `nhb-data-local` under `INSTALL_ROOT`.
If `DataDir` in your `config.toml` points elsewhere, that directory is not
touched.

A node that starts with an empty data directory (a fresh host, or after
`--reset-state`) does not sync the live network from block 1: block sync from
genesis is not supported on that chain. Bring a new node up with
`scripts/deployvalidator.sh` from a verified snapshot instead; see
[Onboarding a validator from a snapshot](../validators/snapshot-onboarding.md)
and [Snapshot operations](../ops/snapshots.md).

## After the run

The script ends by printing the checks `sudo systemctl status nhb.service` and
`sudo ss -ltnp | egrep ':8545'` (the RPC port in the repository's `config.toml`).
