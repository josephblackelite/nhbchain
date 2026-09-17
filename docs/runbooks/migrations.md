# Devnet Migration: Staking Schema Upgrade

This runbook documents the procedure required to migrate development networks to
the staking-aware state schema introduced with `StateVersion` 2. The upgrade
adds persistent staking metadata to the global state. Nodes compiled from this
release refuse to start when the on-disk schema version does not match the
expected value unless the operator explicitly opts in to manual migration mode
via `--allow-migrate`.

## Prerequisites

* Access to each devnet node's data directory.
* The upgraded `nhb` and `consensusd` binaries (or containers) that include the
  staking schema guard.
* The resolved genesis file that will seed the upgraded chain.

## Migration Steps

1. **Announce maintenance** – Notify participants that the devnet will undergo a
   short maintenance window. Pause automated jobs that publish transactions to
   the network.
2. **Capture a snapshot** – Stop the node processes and take a full backup of the
   existing data directory. A simple archive is sufficient for devnets:

   ```bash
   tar -czf nhb-devnet-backup.tgz /path/to/datadir
   ```

   The backup provides a safety net in case the new schema needs to be rolled
   back or inspected.
3. **Clear the old state** – Remove the contents of the data directory. The new
   staking fields are seeded at genesis, so the legacy database cannot be reused.

   ```bash
   rm -rf /path/to/datadir/*
   ```
4. **Regenerate genesis** – Populate the data directory with the updated genesis
   state. For devnets this typically means running the operator tooling that
   renders a fresh resolved genesis JSON and writing it to
   `/path/to/datadir/genesis.resolved.json`.
5. **Restart nodes** – Launch the upgraded binaries. When running in manual
   migration workflows (for example, verifying data before wiping the store),
   supply `--allow-migrate` to temporarily bypass the guard. After bootstrapping
   from the regenerated genesis the stored schema version matches the binary and
   the flag is no longer required.
6. **Verify health** – Confirm that the nodes synchronise, produce blocks, and
   expose healthy RPC endpoints. Once validated, inform participants that the
   devnet is back online.

## Notes

* The guard is enforced in both `nhb` and `consensusd`. Ensure any automation
  that starts these services is updated to include `--allow-migrate` only when a
  manual migration is underway.
* The schema version is persisted in state; subsequent restarts without
  `--allow-migrate` succeed once the node has booted from the upgraded genesis.
