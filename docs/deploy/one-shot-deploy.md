# One-Shot Deployment Script

NHBChain now includes a single production bootstrap script intended to be the
public node-operator entrypoint:

* [scripts/run_nhbcoin_node.sh](/C:/Users/Mba1/Documents/Nwudu/nhbchain/scripts/run_nhbcoin_node.sh)

`run_nhbcoin_node.sh` delegates to the internal production bootstrap helper and is
intended for the initial production deployment or a fresh-genesis relaunch. It
performs the following in one flow:

1. prepares the runtime directories and service user
2. syncs the repo into the runtime install root
3. installs example server-side config templates
4. validates the required production config
5. stops old ad hoc processes
6. builds the node and founder-grade backend services
7. installs systemd units
8. fixes ownership and runtime permissions
9. optionally resets chain and service state
10. enables and starts the runtime services

## Typical usage

Fresh chain reset:

```bash
bash scripts/run_nhbcoin_node.sh --reset-state
```

Deploy without clearing state:

```bash
bash scripts/run_nhbcoin_node.sh
```

Deploy and skip service start:

```bash
bash scripts/run_nhbcoin_node.sh --skip-start
```

Enable OTC explicitly:

```bash
bash scripts/run_nhbcoin_node.sh --enable-otc
```

## Required config

These files must be prepared on the server first:

* `/etc/nhbchain/config.toml`
* `/etc/nhbchain/node.env`
* `/etc/nhbchain/payments-gateway.env`
* `/etc/nhbchain/payoutd.env`
* `/etc/nhbchain/payoutd.yaml`
* `/etc/nhbchain/policies.yaml`
* `/etc/nhbchain/ops-reporting.env`

Optional:

* `/etc/nhbchain/otc-gateway.env`

Templates are installed automatically into `/etc/nhbchain/examples/`.

## Important note

This script does not generate live secrets for you. API keys, JWT secrets, signer keys,
and bearer tokens must already exist in the server-side env/config files before you run
it.
