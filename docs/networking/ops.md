# Networking Operations

## Peerstore Files

The peerstore database lives under the node data directory. By default the
server opens `$(DataDir)/p2p/peerstore` and creates the directory tree if it is
missing. The NET-2B implementation ships with LevelDB out of the box. BadgerDB
remains a viable alternative for bespoke deployments, but the recommended
choice is LevelDB because it offers smaller on-disk footprints for the modest
record counts typical of validator peers.

### Maintenance

* **Backups** – the database can be copied while the node is offline. For online
  snapshots use filesystem-level tools (`btrfs`, `zfs`, or LVM snapshots) to
  capture a consistent view before copying. The LevelDB manifest and log files
  are self-contained, so restoring from a backup is as simple as replacing the
  peerstore directory and restarting the node.
* **Restores** – stop the node, replace the `peerstore` directory with the
  desired backup, and restart. The server will automatically reload all records
  and rehydrate in-memory indexes.
* **Compaction** – LevelDB performs background compaction automatically. If the
  database size grows due to experimentation or repeated imports, you can invoke
  `leveldbutil compact $(DataDir)/p2p/peerstore` during a maintenance window to
  reclaim disk space.

Changing the backing store type requires a clean shutdown. If migrating from a
custom Badger deployment back to the stock LevelDB instance, wipe the
`peerstore` directory before restarting so the server can initialise a fresh
store.

## Seeds

Seeds are static peers that a freshly booted node will dial to discover the
network. Each entry follows the `pubkey@host:port` format where `pubkey` is the
remote node's 0x-prefixed NodeID published by the operator. The host component
may be an IP address or DNS name. Keep DNS TTLs modest so updates propagate
quickly and avoid pointing seeds at load-balanced records; the dialer expects a
single deterministic endpoint per entry.

### Example configuration

```
[p2p]
Seeds = [
  "0x1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd@seed-1.nhb.example.org:46656",
  "0x5678ef015678ef015678ef015678ef015678ef015678ef015678ef015678ef01@seed-2.nhb.example.org:46656",
]
```

### Quick local verification

The repository ships a focused unit test that exercises the seed dialer against
loopback pipes. Run it after editing your configuration to ensure dialing and
backoff behave as expected:

```
go test ./p2p -run SeedDialer -count=1
```

The test drives both the failure path (incrementing the peerstore counter) and a
successful handshake, mirroring the behaviour of a live node without requiring a
full network deployment.
