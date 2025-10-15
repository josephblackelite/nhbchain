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

## Securing the gRPC bridge

The `p2pd` daemon exposes a gRPC endpoint that consensusd connects to for
gossip. The listener now defaults to `127.0.0.1:9091` so fresh deployments are
never internet-facing accidentally. Operators should explicitly bind to a public
interface when deploying across hosts and configure authentication before doing
so.

The `[network_security]` section in `config.toml` drives both ends of the
connection. `consensusd` validates this block on startup and will refuse to run
when the shared secret resolves to an empty string. Use the inline example from
`config-local.toml` for lab environments (`AllowInsecure = true` with a short
`SharedSecret`) and promote to the production pattern below—external token via
`SharedSecretEnv`/`SharedSecretFile` plus TLS—before exposing the services
outside of localhost:

```toml
[network_security]
# Shared-secret token transmitted with every RPC. The value is resolved in the
# following order: environment variable, external file, inline string. When a
# shared secret is configured the client now requires TLS unless
# `AllowInsecure = true` is explicitly set for a throwaway lab environment.
SharedSecretEnv = "NHB_NETWORK_SHARED_SECRET"
SharedSecretFile = "/etc/nhb/network.token"
SharedSecret = ""
# Explicit opt-in is required for plaintext connections.
AllowInsecure = false
# Read-only RPCs (GetView/ListPeers) remain protected unless explicitly opened.
AllowUnauthenticatedReads = false
# Metadata header that carries the token (default: "authorization").
AuthorizationHeader = "x-nhb-network-token"

# TLS material for the p2pd server.
ServerTLSCertFile = "/etc/nhb/tls/p2pd.crt"
ServerTLSKeyFile  = "/etc/nhb/tls/p2pd.key"
# Optional client CA bundle enables mutual TLS and the AllowedClientCommonNames
# allowlist below.
ClientCAFile = "/etc/nhb/tls/consensus-ca.pem"
AllowedClientCommonNames = ["consensusd"]

# TLS material for the consensusd client.
ServerCAFile       = "/etc/nhb/tls/p2pd-ca.pem"
ClientTLSCertFile  = "/etc/nhb/tls/consensus.crt"
ClientTLSKeyFile   = "/etc/nhb/tls/consensus.key"
ServerName         = "p2pd.internal"
```

`p2pd` loads the server certificate (and optional client CA) to present TLS or
mTLS credentials via `grpc.Creds(...)`. When a shared secret is configured, the
service enforces it on `Gossip`, `DialPeer`, `BanPeer`, `GetView`, and
`ListPeers`; client certificates are validated against the
`AllowedClientCommonNames` allowlist when provided. `consensusd` consumes the
same block to dial with `grpc.WithTransportCredentials` and per-RPC metadata so
authenticated traffic continues to flow while unauthenticated requests are
rejected. Beginning with this release, both `consensusd` and `p2pd` refuse to
fall back to plaintext; the server now fails fast when TLS material is missing
unless `AllowInsecure = true` is explicitly set for a short-lived lab
environment, and at least one authenticator (shared secret or client
certificate) must be configured before either service will start.

Setting `AllowUnauthenticatedReads = true` re-opens `GetView` and `ListPeers` to
anonymous callers for debugging or observability tooling. This opt-in bypasses
token checks for those two RPCs only; writes (`Gossip`, `DialPeer`, `BanPeer`)
remain gated by the configured authenticators. Because unauthenticated reads
expose topology and scoring metadata, restrict the listener address or rely on
network-level ACLs when enabling the toggle.

## Listener binding policy

Production manifests now default to loopback bindings for every externally
reachable daemon. `config.toml`, the Docker Compose examples, and all Helm
values ship with `127.0.0.1` listeners so operators are forced to publish
services through a hardened reverse proxy, Service mesh, or load balancer. The
`scripts/verify_prod_config.sh` helper enforces this policy in CI by failing the
build whenever `0.0.0.0` or `AllowInsecure = true` appears in production
artifacts. Use a dedicated `config-dev.toml` (or explicit overrides) when you
need wildcard binds and plaintext transport for short-lived labs, and ensure the
`--allow-insecure` flag accompanies those experiments.

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

### Governance-backed registry

From NET-3A onward the preferred way to publish bootstrap peers is the
`network.seeds` governance parameter. The runtime merges the local config with
registry fallbacks and signed DNS records, allowing communities to rotate seeds
without shipping new binaries. See [`docs/networking/seeds.md`](./seeds.md) for a
complete walkthrough of the JSON payload, DNS signature format, and operator
runbooks.

## NAT & Port Forwarding

NET-2E instruments the connection manager with NAT awareness. On startup it logs
whether the configured `ListenAddress` maps to a private interface, a public IP,
or an ambiguous host string. When a private or unspecified address is detected
the manager emits a message indicating that UPnP port mapping is being skipped
because only a stub implementation is provided. These logs are the first line of
defence when diagnosing connectivity issues on residential or cloud networks.

## Mini-Mesh Runbook

The NET-2H integration test suite ships with a four-node “mini mesh” that
operators can reproduce locally to validate seed connectivity, authenticated
handshakes, and PEX-based peer gossip. The walkthrough below mirrors the test
topology: three healthy nodes forming a mesh plus a fourth node advertising the
wrong chain ID that should be rejected during the handshake.

### 1. Generate configs

All nodes share the same genesis hash and differ only in their listen ports and
seed lists. Adjust the ports if they clash with existing services.

```
# common genesis
GENESIS=$(printf 'ab%.0s' {1..32})

cat > n1.toml <<'CFG'
[p2p]
ListenAddress = "127.0.0.1:36656"
ChainID = 777
GenesisHash = "0x$GENESIS"
ClientVersion = "mesh/n1"
MinPeers = 3
OutboundPeers = 3
PEX = true
CFG

cat > n2.toml <<'CFG'
[p2p]
ListenAddress = "127.0.0.1:36657"
ChainID = 777
GenesisHash = "0x$GENESIS"
ClientVersion = "mesh/n2"
Seeds = ["REPLACE_N1@127.0.0.1:36656"]
MinPeers = 3
OutboundPeers = 3
PEX = true
CFG

cat > n3.toml <<'CFG'
[p2p]
ListenAddress = "127.0.0.1:36658"
ChainID = 777
GenesisHash = "0x$GENESIS"
ClientVersion = "mesh/n3"
Seeds = ["REPLACE_N1@127.0.0.1:36656"]
MinPeers = 3
OutboundPeers = 3
PEX = true
CFG

cat > wrong.toml <<'CFG'
[p2p]
ListenAddress = "127.0.0.1:36659"
ChainID = 778   # wrong chain
GenesisHash = "0x$GENESIS"
ClientVersion = "mesh/wrong"
Seeds = ["REPLACE_N1@127.0.0.1:36656"]
PEX = true
CFG
```

Start `n1`, capture its advertised `nodeId` from the logs, and replace the
`REPLACE_N1` placeholder in the other files with that value.

### 2. Launch the mesh

```
./build/nhbd --home ./n1 --config ./n1.toml &
./build/nhbd --home ./n2 --config ./n2.toml &
./build/nhbd --home ./n3 --config ./n3.toml &
./build/nhbd --home ./wrong --config ./wrong.toml &
```

Once `n2` and `n3` report successful handshakes with `n1`, run a PEX request to
prime peer exchange (the daemon issues periodic requests automatically, but the
manual command accelerates local testing):

```
curl -X POST localhost:36657/net/request_pex
curl -X POST localhost:36658/net/request_pex
```

Within a few seconds `n2` and `n3` should connect directly to each other using
the address learned from `n1`.

### 3. Inspect net_info

Query the RPC endpoint to verify the peer set. Each healthy node should report
the other two peers with `"state":"connected"` while the wrong-chain node only
sees transient dial attempts:

```
curl -s localhost:36657/net/info | jq '.peers[] | {id: .nodeId, state: .state}'
```

Sample output once the mesh stabilises:

```
{
  "id": "0xabc1...", "state": "connected"
}
{
  "id": "0xabc2...", "state": "connected"
}
```

The wrong-chain node’s RPC will show only its seed in `state:"dialing"` with
`lastError` mentioning the chain mismatch, confirming the handshake rejection.

### 4. Tear down

Stop the processes (`pkill nhbd` or `Ctrl+C`) and remove the temporary data
directories once you are done validating the workflow.

### Troubleshooting

* **Ports already in use** – change the `ListenAddress` ports in the configs or
  shut down the conflicting service.
* **Firewall interference** – ensure `localhost` TCP traffic is allowed. Local
  firewalls like `pf`, `ufw`, or `firewalld` can block loopback if configured
  aggressively.
* **Clock skew** – large time drifts (>5s) between nodes can trip handshake
  deadlines. Synchronise system clocks with NTP before running the test.
* **PEX not triggering** – reissue the manual `request_pex` RPC above and verify
  that `n1` still lists both peers in `net/info`; stale peerstore entries can be
  flushed by deleting the `p2p/peerstore` directory under each data directory.

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
