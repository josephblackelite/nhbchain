# Devnet Migration: State Schema Version Guard

The node records a schema version in state under the key `state/version`. The version this
code expects is `StateVersion = 2` (`core/state/version.go`), which added persistent staking
fields. On startup the node compares the stored version with `2`:

* A missing version counts as `0`.
* If the versions differ, startup fails with `state: schema version mismatch: on-disk=<n>
  expected=2; pass --allow-migrate to bypass the guard`.
* Passing `--allow-migrate` skips the comparison. It does not migrate anything.

Both `nhb` (`cmd/nhb`) and `consensusd` (`cmd/consensusd`) accept
`--allow-migrate` ("Allow starting with a mismatched state schema (manual migrations
only)") and pass it to `core.NewNode`. New databases written by a genesis load or by
`core/blockchain.go` are stamped with the current version.

## When the guard trips

The code stamps `state/version` only when it creates a state from genesis
(`core/genesis/loader.go`, `core/blockchain.go`). No routine rewrites the version of an
existing database, so the way to move a development network onto a build with a newer
schema is to wipe the state and start from genesis again.

## Steps

1. **Announce maintenance** and stop anything that submits transactions to the network.
2. **Stop the node processes and back up the data directory.**

   ```bash
   tar -czf nhb-devnet-backup.tgz /path/to/datadir
   ```

   `DataDir` is the `DataDir` value of the node's `config.toml`.
3. **Clear the old state.** The node also keeps its peer store and node identity in
   `<DataDir>/p2p/` (`peerstore` and `node_key.json`, see `cmd/nhb/main.go` and
   `cmd/p2pd/main.go`). Removing the whole directory therefore creates a new node identity.
   To keep the identity, keep `p2p/node_key.json` (and the `p2p/peerstore` directory)
   when you clear the rest.
4. **Provide the genesis file.** Start the node with `--genesis <file>` (or the `NHB_GENESIS`
   environment variable or `GenesisFile` in `config.toml`). When a genesis file is given,
   `nhb` loads and validates it and writes the resolved spec to
   `<DataDir>/genesis.resolved.json` itself (`cmd/nhb/main.go`), so no separate tool is needed
   to create that file. Without a genesis file the node only creates one automatically when
   started with `--allow-autogenesis`, which is marked DEV ONLY.
5. **Start the upgraded binaries.** After bootstrapping from genesis the stored version equals
   the binary's, and later restarts do not need `--allow-migrate`.
6. **Verify health.** Confirm the nodes synchronise, produce blocks and answer RPC, then
   tell participants the network is back.

## Notes

* Use `--allow-migrate` only while performing a manual migration you have designed yourself.
  Automation that starts `nhb` or `consensusd` should not pass it by default.
* Both binaries pass the flag to `core.NewNode`, which calls `EnsureStateVersion`
  (`core/node.go`), so the guard behaves the same in both.
