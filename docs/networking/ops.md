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
