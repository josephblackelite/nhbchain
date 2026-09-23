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

* Make a snapshot of a running node and verify it the way a new validator will: `scripts/make-snapshot.sh` writes the archive and its manifest, and `nhb-snapshot verify` and `nhb-snapshot extract` check them against a pinned chain ID and genesis hash (see [snapshots-audit.md](./snapshots-audit.md)). The node's `sync_snapshot_export` and `sync_snapshot_import` RPC methods are retired and answer HTTP 410.
* Run the production-configuration check, `scripts/verify_prod_config.sh -c config/prod.toml`, against the configuration you intend to deploy. CI runs it on `config/prod.toml` and on `config.toml` (the `bugcheck-linux` job in `.github/workflows/ci.yml`); both pass on the release tree. The script checks only keys the node reads: TLS settings for `[network_security]` and the RPC listener (or a loopback plaintext RPC listener behind a proxy), no wildcard `ListenAddress` or `RPCAddress`, loyalty pro-rate flags, fee owner wallets, a positive `MaxEmissionPerYearWei`, and that no `[global.Pauses]` flag is set.
* Run `make bugcheck` and the targets in [../audit/index.md](../audit/index.md) on the release commit, and read the per-check logs in `logs/`. `scripts/bugcheck.sh` records a failing check as `failed` and exits 1 when a critical check fails.
* Confirm the transport and authentication settings in [transport.md](./transport.md) and [network-hardening.md](./network-hardening.md).

## Freeze procedures

* Verify that every validator runs the same consensus-relevant configuration (`[governance]`, `[potso.*]`, `[global.*]`, `QuorumCertActivationHeight`). `nhb-snapshot check-config --config FILE --genesis FILE` (`cmd/nhb-snapshot/pins.go`) checks a node configuration against the genesis file: `QuorumCertActivationHeight` must be 0, the `[global.Fees]` and `[[global.Fees.Assets]]` owner wallets, `[subscriptions] Treasury` and `[potso.rewards] TreasuryAddress` must match the genesis admin wallet and loyalty treasury. The shipped `config.toml` passes it against `config/genesis.relaunch.json`; the fee wallets in `config/prod.toml` are not the ones that genesis names, so `prod.toml` does not pass it and must have them replaced before use.
* Keep the genesis file byte-for-byte identical to `config/genesis.relaunch.json`: the chain ID is derived from its hash.
* Rehearse the flows that `tests/e2e` covers, remembering that those tests run against an in-process simulation (see [../audit/e2e-flows.md](../audit/e2e-flows.md)).

## Incident response

1. Confirm impact and assign an incident owner.
2. Notify stakeholders.
3. Apply mitigations (configuration changes, firewall rules, feature toggles) and record what was done.
4. Escalate to external partners if shared infrastructure is affected.
5. Record the timeline, root cause and remediation.

## Rollback & rollforward

* **Rollback.** A node is restored from a verified snapshot of the chain database, not through the RPC: `scripts/deployvalidator.sh --reset-state` downloads, verifies and unpacks a snapshot into a new directory, and only then stops the node and moves the old data directory aside, never deleting it; use it when the data directory is broken (see [../validators/snapshot-onboarding.md](../validators/snapshot-onboarding.md)). A snapshot manifest is not signed by anyone; the operator pins what to accept (see [snapshots-audit.md](./snapshots-audit.md)).
* **Rollforward.** Once a fix is validated, tag a new release, update the deployed artifacts and redeploy validators together. Fields such as `QuorumCertActivationHeight` in `config.toml` must be identical on every validator (see the comment in `config/config.go`).

## Validator hardening

* Keep the validator key out of the configuration file. The node loads it from an encrypted keystore (`ValidatorKeystorePath`, passphrase from the environment or an interactive prompt) or, when `ValidatorKMSEnv` or `ValidatorKMSURI` is set, from a hex-encoded private key held in an environment variable (`ValidatorKMSEnv = "NAME"` or `ValidatorKMSURI = "env://NAME"`). The `env` scheme is the only one implemented; there is no HSM or cloud KMS client (`loadValidatorKey`, `loadFromKMS` in `cmd/nhb/main.go`).
* The shipped `config.toml` binds `127.0.0.1:6001` for P2P and `127.0.0.1:8545` for RPC (a config file the node creates on first run gets `:6001` and `:8080`; `createDefault` in `config/config.go`); open only the ports your deployment needs and restrict them to the peers or proxies that need them.
* The node records what it has voted in `bft_sign_state.json` under `DataDir` (and the proof-of-lock state in `polc_lock.json`) so that a restart cannot make it sign a different vote for a round it already voted in (`cmd/nhb/main.go`, `consensus/bft/sign_state.go`). Never copy another validator's data directory or these files to a second validator; snapshots contain only chain database files (`cmd/nhb-snapshot/manifest.go`).
* Run the node as a non-root user with resource limits and ship its logs to your log system.

## Vulnerability disclosure

* Email: `security@nhbcoin.com`
* Encryption key: [`repository-pgp-key.asc`](./repository-pgp-key.asc); the fingerprint is in [bug-bounty.md](./bug-bounty.md).
* Timelines, safe harbor and embargo: [disclosure.md](./disclosure.md).
