# Snapshot Operations Guide

Snapshots let operators bootstrap validators and archive historical state efficiently. This guide documents creation, distribution, and restoration procedures.

## Snapshot cadence

- Produce full snapshots weekly for each network (devnet, staging, mainnet).
- Generate incremental snapshots daily between full exports.
- Retain at least four weeks of snapshots to support delayed recovery.

## Creation workflow

1. Pause heavy background jobs to reduce I/O contention.
2. Run `nhbchain snapshot export --output /data/snapshots/<height>.tar.gz` on a healthy validator.
3. Record the app hash, block height, and exporter node ID in the snapshot manifest.
4. Resume background jobs and confirm the validator rejoins consensus.

## Distribution

- Upload snapshots to the dedicated object storage bucket with server-side encryption.
- Generate SHA256 checksums and publish them alongside download URLs.
- Restrict write access to snapshot buckets and monitor access logs.

## Restoration

1. Download the desired snapshot and verify the checksum.
2. Extract to the node data directory: `tar -xzf <height>.tar.gz -C $NHB_HOME`.
3. Start `consensusd` with `--statesync.snapshot-height <height>` if state sync is required.
4. Monitor logs for fast-sync completion and compare app hash to the manifest.

## Validation

- After each snapshot cycle, perform a restore test in staging.
- Track restore duration and document any manual interventions required.
- Update this guide if tooling or retention policies change.
