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
MinPeers = 12
OutboundPeers = 16
BanDurationSeconds = 3600
HandshakeTimeoutMs = 3000
DialBackoffSeconds = 30
PEX = true
```

### Runtime defaults

* **MinPeers / OutboundPeers** – the connection manager aims for at least 12
  total peers with 16 outbound slots reserved for proactive dials. Operators on
  larger hardware can raise both numbers symmetrically.
* **BanDurationSeconds** – manual and automated bans last one hour by default,
  aligning the reputation engine with the peerstore eviction window.
* **HandshakeTimeoutMs** – trimmed to 3 seconds in NET-2F to surface unhealthy
  endpoints faster while still tolerating transcontinental latency.
* **DialBackoffSeconds** – base delay (30s) applied before retrying failed
  connections. Retries exponential backoff to the configured cap; override this
  to tune aggressiveness for private clusters.
* **PEX** – enabled by default. Set to `false` on air-gapped validators that
  must not participate in peer exchange gossip.

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

## NAT & Port Forwarding

NET-2E instruments the connection manager with NAT awareness. On startup it logs
whether the configured `ListenAddress` maps to a private interface, a public IP,
or an ambiguous host string. When a private or unspecified address is detected
the manager emits a message indicating that UPnP port mapping is being skipped
because only a stub implementation is provided. These logs are the first line of
defence when diagnosing connectivity issues on residential or cloud networks.

### Recommended ports

* **TCP** – open the port specified in `ListenAddress` (default `:30303` if not
  overridden). Both inbound and outbound TCP are required for peer traffic.
* **UDP** – optional today, but reserve the same port range if you plan to layer
  discovery protocols that use UDP beacons.

### UPnP stub guidance

* The software never attempts real UPnP or NAT-PMP negotiations; the stub simply
  logs what would have been requested.
* Operators behind consumer routers should manually create a static port forward
  from the WAN interface to the node's LAN IP using the TCP port above.
* If your router exposes UPnP diagnostics, verify that no stale mappings exist
  for the node's port before reconfiguring.

### Cloud firewall examples

| Provider | Rule | Notes |
| -------- | ---- | ----- |
| AWS EC2 Security Group | Inbound TCP from `0.0.0.0/0` to port `30303` | Attach to the node instance. Combine with Network ACLs for defence in depth. |
| Google Cloud VPC Firewall | Ingress rule allowing TCP `30303` from `0.0.0.0/0` | Remember to select the correct network tags so the rule applies to the VM. |
| Azure NSG | Inbound security rule for TCP `30303` with priority < 200 | Pair with outbound allow-all to ensure the dialer can reach peers. |

Always coordinate cloud firewall rules with on-host firewalls (e.g. `ufw`,
`firewalld`). The node must accept inbound TCP on the configured port while also
allowing outbound ephemeral ports for handshakes.
