# Seed Server Runbook

This runbook describes how to bootstrap and operate an independent NHB seed
server. It assumes a fresh Linux host (Ubuntu 24.04 or similar), a DNS zone you
control, and access to the governance process to stage `network.seeds`
proposals.

## 1. Provision the host

1. **Create a dedicated VM** with a static public IP. Allocate at least 2 vCPU,
   4 GiB RAM and 40 GiB of SSD-backed storage.
2. **Harden the OS**: update packages, create a dedicated `nhb` user, enable the
   firewall and restrict inbound traffic to TCP/46656 and SSH.
   ```bash
   sudo apt update && sudo apt upgrade -y
   sudo adduser --system --group nhb
   sudo ufw default deny incoming
   sudo ufw allow 22/tcp
   sudo ufw allow 46656/tcp
   sudo ufw enable
   ```
3. **Install dependencies**: Go toolchain (1.23+), Git, and Supervisor/`systemd`
   to manage the process.

## 2. Build the seed binary

1. Clone the repository and build the node:
   ```bash
   git clone https://github.com/nhbchain/nhbchain.git
   cd nhbchain
   go build ./cmd/nhb
   sudo install -o nhb -g nhb -m 0755 nhb /usr/local/bin/nhb
   ```
2. Copy the default configuration and adjust it for the seed role. Disable the
   validator and RPC services, and leave `[p2p].Seeds` empty – the governance
   registry will backfill them at runtime.

## 3. Generate the node identity

1. Use the bundled helper to create a long-term P2P identity:
   ```bash
   sudo -u nhb /usr/local/bin/nhb --config /etc/nhb/config.toml --generate-identity
   ```
   This writes `peerstore/node_key.json` which contains the Ed25519 key used to
   derive the NodeID advertised in seed lists.
2. Record the NodeID from the startup logs or via `nhbctl net_info`. You will
   publish it alongside the seed address.

## 4. Publish DNS entries

1. Generate an authority key and seed record using the helper script shipped in
   this repository:
   ```bash
   go run ./ops/seeds/tools/authority \\
     --domain seeds.mainnet.example.org \\
     --host seed-a.mainnet.example.org \\
     --port 46656 \\
     --node-id <0xNODEID> \\
     --out authority.json
   ```
   The tool prints the TXT payload (`nhbseed:v1:...`) and the public key that
   must be inserted into the `network.seeds` governance payload.
2. Add the TXT record to your DNS provider with a short TTL (60–300 seconds).
   Example record:
   ```
   _nhbseed.seeds.mainnet.example.org.  300  IN TXT  "nhbseed:v1:<base64 payload>"
   ```
3. If you operate multiple seeds, repeat the process for each host and include
   all TXT blobs under the authority lookup name.

## 5. Stage the governance proposal

1. Prepare a `network.seeds` JSON payload referencing the new authority public
   key and the canonical seed addresses. Include static fallback entries pointing
   at the same hosts to cover temporary DNS outages.
2. Submit the proposal, monitor voting, and queue execution once it passes.
   Remember to keep the previous DNS records live until the proposal executes so
   existing nodes can bridge the rotation.

## 6. Deploy the seed service

1. Create a systemd unit `/etc/systemd/system/nhb-seed.service`:
   ```ini
   [Unit]
   Description=NHB seed node
   After=network.target

   [Service]
   User=nhb
   Group=nhb
   ExecStart=/usr/local/bin/nhb --config /etc/nhb/config.toml
   Restart=on-failure
   RestartSec=5s
   LimitNOFILE=65536

   [Install]
   WantedBy=multi-user.target
   ```
2. Enable and start the service:
   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable --now nhb-seed.service
   ```
3. Tail the logs and confirm the node announces the correct listen address and
   reports the merged seed catalogue:
   ```bash
   journalctl -u nhb-seed.service -f
   ```

## 7. Health checks & monitoring

* **DNS resolution** – run `dig TXT _nhbseed.seeds.mainnet.example.org` from a
  remote host and verify the signed payload matches the expected NodeID/address.
* **P2P reachability** – from another NHB node run `nhbctl net_dial` targeting
  the seed. Ensure the handshake succeeds and the node appears in `net_peers`.
* **Registry refresh** – inspect `net_info` to confirm the seed is listed with
  `source="dns:seeds.mainnet.example.org"` and that the refresh timestamp in the
  logs updates periodically.
* **Alerting** – hook the service into your monitoring stack. Alert on process
  crashes, TCP listener failures, and DNS lookup errors.

## 8. Rotation & retirement

1. Stage the replacement DNS records and governance payload as described above.
2. Once the new entries are active and healthy, remove the old TXT records and
   decommission the retired seed host.
3. Keep historical seeds in the governance payload until the majority of the
   network has upgraded to avoid stranding lagging nodes.

## Appendix: Files & directories

* `/etc/nhb/config.toml` – seed node configuration.
* `/var/lib/nhb/p2p/peerstore` – LevelDB peerstore; safe to delete if the host is
  rebuilt.
* `/var/log/nhb` – optional log directory if you redirect `systemd` output.

## Appendix: Emergency procedures

* **DNS authority compromised** – submit a proposal removing the authority and
  pointing to static fallbacks while you rotate keys.
* **Seed host offline** – update the DNS record with a new IP (keeping the same
  NodeID) and restart the service. Because the signed payload binds the host and
  port, you must regenerate it if the TCP endpoint changes.
* **Registry unavailable** – the runtime continues using the last known DNS set
  plus any static fallbacks. Update `[p2p].Seeds` in configs only as a last
  resort; the registry should remain the source of truth.
