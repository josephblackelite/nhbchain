# Runbook: p2pd Failover

This runbook covers recovering `p2pd` after a failure while keeping `consensusd` connected.
`consensusd` reaches `p2pd` over the gRPC `network.v1.NetworkService`
(`proto/network/v1`), whose methods are `Gossip`, `GetView`, `ListPeers`, `DialPeer` and
`BanPeer`.

## Defaults

* `p2pd` serves that gRPC service on `--grpc`, default `127.0.0.1:9091` (`cmd/p2pd/main.go`).
* `consensusd` dials it at `--p2p`, default `localhost:9091`, and serves its own gRPC on
  `--grpc`, default `127.0.0.1:9090` (`cmd/consensusd/main.go`).
* Both binaries read the same kind of `config.toml` (`--config`, default `./config.toml`).
  Their link is secured by the `[network_security]` section (TLS files, shared secret,
  `AllowInsecure`). Plaintext needs the `--allow-insecure` flag on both sides and a loopback
  address.
* `p2pd` keeps its peer store at `<DataDir>/p2p/peerstore` and its node identity at
  `<DataDir>/p2p/node_key.json`.

## 1. Detect

* `consensusd` writes `Failed to connect to p2pd at <target>: <error>` or
  `Network stream terminated: <error>` to stderr. It retries by itself with a delay that
  starts at 500 ms, doubles on each attempt and stops growing at 30 s
  (`maintainNetworkStream`), so a restarted `p2pd` is picked up without restarting
  `consensusd`.
* Relay queue metrics from the `p2pd` process: `nhb_network_relay_queue_enqueued_total`,
  `nhb_network_relay_queue_dropped_total` and `nhb_network_relay_queue_occupancy`
  (`network/metrics.go`).
* `p2pd` logs `relay queue saturated; dropping envelopes` with component `network_relay`
  when the share of dropped envelopes reaches `RelayDropLogRatio`, at most once per cooldown
  period (`network/relay.go`).

## 2. Validate the environment

1. Confirm the consensus node is running and advancing (its gRPC endpoint is the one set with
   `--grpc`).
2. Check CPU, memory and disk on the `p2pd` host.
3. Check that the TLS files and the shared-secret source named in `[network_security]` are
   present and readable, and that the peer store directory above is intact.

## 3. Bring up a replacement

A standby `p2pd` keeps the same node identity only if it has the same `node_key.json` and
peer store as the failed one. Start it with the same `config.toml`, `--genesis` and
`--grpc` values, then make sure `consensusd`'s `--p2p` target resolves to it. `consensusd`
re-dials the target with the backoff described above.

## 4. Restart the primary

1. Stop the `p2pd` process.
2. Start it again with the same flags.
3. Watch its log for `p2pd initialised and running` and for successful peer dials.

## 5. Post-recovery checks

* `consensusd` stops printing connection errors and the chain height advances.
* Call `ListPeers` (request `network.v1.ListPeersRequest`) on the `p2pd` gRPC address to list
  connected peers. Use the TLS material and the shared secret from `[network_security]`.
  Unauthenticated reads are only allowed when `AllowUnauthenticatedReads` is true.
* Compare `nhb_network_relay_queue_dropped_total` with
  `nhb_network_relay_queue_enqueued_total`, and keep `nhb_network_relay_queue_occupancy` well
  below the queue size.
* Queue tuning lives in `[network_security]`: `StreamQueueSize` (default `128` when unset or
  not positive) and `RelayDropLogRatio` (default `0.1`; a value above `1` is set to `1`).
  `p2pd` sizes the relay queue with it and `consensusd` sizes its client send queue with it
  (`SetSendQueueSize`), so change the value in the config used by both and restart both.

## 6. Root cause

* Review recent deployments and configuration changes.
* Check the validity dates of the TLS certificates.
* Check that the seeds and bootnodes in the configuration are reachable.
