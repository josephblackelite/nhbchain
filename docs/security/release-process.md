# Security Release & Freeze Process

This page describes how security reports and pre-release checks are handled, and points to the code and scripts in this repository that support each step. Reporter-facing timelines (acknowledgement, triage, resolution, embargo) are defined once, in [disclosure.md](./disclosure.md).

## Audit intake

1. **Submission channel.** Reports arrive at `security@nhbcoin.com` (the address in `.well-known/security.txt`); see [disclosure.md](./disclosure.md) for how to encrypt them.
2. **Acknowledgement and response times.** As stated in [disclosure.md](./disclosure.md).
3. **Initial assessment.** Rate severity and identify the affected modules and versions.
4. **Issue tracking.** Log each report privately with: severity, affected versions, exploit prerequisites, mitigation status.
5. **Communication.** Share sanitised status with the release owners.

If an issue risks funds or validator safety, treat it as a release blocker regardless of its severity rating.

## Pre-release checklist

* Validate a state snapshot export and its verification path: `sync_snapshot_export` produces the chunk files and an unsigned manifest, and `sync_snapshot_import` verifies signatures, chunk hashes and the state root (see [snapshots-audit.md](./snapshots-audit.md)).
* Run the production-configuration check, `scripts/verify_prod_config.sh -c config/prod.toml`, against the configuration you intend to deploy. The checked-in `config/prod.toml` currently fails it (wildcard `ListenAddress` and `RPCAddress`, empty fee `owner_wallet` values), so the check only passes on a configuration you have completed.
* Run `make bugcheck` and the targets in [../audit/index.md](../audit/index.md) on the release commit. Read the per-check logs in `logs/` rather than the summary: `scripts/bugcheck.sh` currently records failing checks as passed and exits before writing its JSON summary (see the caveats in [../audit/index.md](../audit/index.md)).
* Confirm the transport and authentication settings in [transport.md](./transport.md) and [network-hardening.md](./network-hardening.md).
* Follow the launch documentation: [testnet](../launch/testnet.md), [faucet](../launch/faucet.md), [explorer](../launch/explorer.md).

## Freeze procedures

* Validate state sync snapshots and signature bundles.
* Verify hardened configuration templates for validators, seeds, RPC, and faucet nodes.
* Confirm seed rotation and faucet abuse scripts are operational.
* Rehearse swap, escrow, identity, POTSO, and governance end-to-end scenarios.
* Ensure all public endpoints (RPC/REST/WS, faucet API, explorer) pass synthetic monitoring checks.
* Publish launch documentation.

## Incident response

1. Confirm impact and assign an incident owner.
2. Notify stakeholders.
3. Apply mitigations (configuration changes, firewall rules, feature toggles) and record what was done.
4. Escalate to external partners if shared infrastructure is affected.
5. Record the timeline, root cause and remediation.

## Rollback & rollforward

* **Rollback.** A node can be restored from a snapshot with `sync_snapshot_import`, which requires a manifest signed by at least two thirds of the voting power of the node's current validator set (`core/sync/manifest_verify.go`). Note that nothing in this repository produces those signatures.
* **Rollforward.** Once a fix is validated, tag a new release, update the deployed artifacts and redeploy validators together. Fields such as `QuorumCertActivationHeight` in `config.toml` must be identical on every validator (see the comment in `config/config.go`).

## Validator hardening

* Keep the validator key out of the configuration file. The node loads it from an encrypted keystore (`ValidatorKeystorePath`, passphrase from the environment or an interactive prompt) or, when `ValidatorKMSEnv` or `ValidatorKMSURI` is set, from a hex-encoded private key held in an environment variable (`ValidatorKMSEnv = "NAME"` or `ValidatorKMSURI = "env://NAME"`). The `env` scheme is the only one implemented; there is no HSM or cloud KMS client (`loadValidatorKey`, `loadFromKMS` in `cmd/nhb/main.go`).
* The default P2P listen address is `:6001` (`config/config.go`) and the shipped `config.toml` binds `127.0.0.1:6001` for P2P and `127.0.0.1:8545` for RPC; open only the ports your deployment needs and restrict them to the peers or proxies that need them.
* Run the node as a non-root user with resource limits and ship its logs to your log system.

## Vulnerability disclosure

* Email: `security@nhbcoin.com`
* Encryption key: [`repository-pgp-key.asc`](./repository-pgp-key.asc); the fingerprint is in [bug-bounty.md](./bug-bounty.md).
* Timelines, safe harbor and embargo: [disclosure.md](./disclosure.md).
