# Production Secrets Guide

This document explains where NHBChain production secrets belong and how to load them
safely when relaunching the network.

## Core rule

Production secrets belong on the servers running the backend services. They do not
belong in:

* the Git repository
* genesis files
* chain state
* the wallet frontend
* client-side JavaScript

For the current deployment model:

* the chain EC2 should hold the node, `payments-gateway`, `payoutd`, and
  `ops-reporting`
* the wallet EC2 should not hold payment-provider secrets or minting keys

## Recommended storage options

Use one of these patterns:

1. systemd environment file or service drop-in on the chain EC2
2. a secrets manager such as AWS Systems Manager Parameter Store or AWS Secrets Manager
3. file-based secrets with strict filesystem permissions where the service already
   supports `*_file` configuration

The repo should only contain example templates, never live values.

## `payments-gateway`

Set these on the chain EC2 host running `payments-gateway`:

* `PAY_GATEWAY_NODE_URL`
* `PAY_GATEWAY_NODE_TOKEN`
* `PAY_GATEWAY_NOW_API_KEY`
* `PAY_GATEWAY_NOW_IPN_SECRET`
* `PAY_GATEWAY_MINTER_KMS_ENV`
* `PAY_GATEWAY_DEFAULT_MINT_ASSET`
* `PAY_GATEWAY_SERVICE_FEE_BPS`
* optional `PAY_GATEWAY_NOW_BASE`
* optional `PAY_GATEWAY_QUOTE_TTL`
* optional `PAY_GATEWAY_ORACLE_TTL`
* optional `PAY_GATEWAY_ORACLE_DEVIATION`
* optional `PAY_GATEWAY_ORACLE_BREAKER`

Meaning:

* NOWPayments API and IPN secrets stay server-side only
* the minter key reference stays server-side only
* the node auth token stays server-side only

## `payoutd`

`payoutd` loads from YAML config plus secret indirection. Recommended production setup:

* keep the main config file on the chain EC2
* resolve sensitive values through:
  * `signer_key_env`
  * `signer_key_file`
  * `bearer_token_file`

Sensitive values for `payoutd` include:

* treasury wallet signer key
* consensus signer key
* admin bearer token
* TLS private keys

## `ops-reporting`

Set these on the chain EC2 host running `ops-reporting`:

* `OPS_REPORT_PAYMENTS_DB`
* `OPS_REPORT_ESCROW_DB`
* `OPS_REPORT_TREASURY_DB`
* `OPS_REPORT_PAYOUT_DB`
* `OPS_REPORT_BEARER_TOKEN`

Only the bearer token is truly secret, but database paths should still stay in
server-side configuration.

## OTC gateway

If the OTC stack is enabled, these remain server-side:

* `OTC_SWAP_API_KEY`
* `OTC_SWAP_API_SECRET`
* `OTC_IDENTITY_API_KEY`
* HSM client certificates and keys
* JWT verification secrets or public-key paths
* WebAuthn API keys where enabled

## Example deployment pattern

On Linux with systemd, a typical shape is:

* `/etc/nhbchain/payments-gateway.env`
* `/etc/nhbchain/ops-reporting.env`
* `/etc/nhbchain/payoutd.yaml`
* `/etc/nhbchain/secrets/`

Recommended permissions:

* env files: readable only by the service user and root
* secret files: `0600`
* config files with secret references: `0640` or stricter

## Rotation rule

If any provider key, IPN secret, or signer secret is ever pasted into chat, email, or
committed by mistake, rotate it before production use.
