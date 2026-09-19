# Seed Server Runbook

How seed nodes are advertised to the network. A seed is an ordinary `nhb` node
(`cmd/nhb`); the repository has no separate seed binary and no seed-only
configuration mode. Seed discovery is implemented in `p2p/seeds/registry.go` and
`cmd/nhb/main.go`; the helpers under `ops/seeds/tools` create and serve the DNS
records it reads.

## How nodes find seeds

A node builds its seed list from three sources (`cmd/nhb/main.go`):

1. `Seeds` under `[p2p]` in `config.toml`, each entry written
   `<nodeId>@<host:port>` (entries without `@` are ignored with a warning). Source
   label: `config`.
2. Static entries in the on-chain `network.seeds` registry. Source label:
   `registry.static` unless the entry sets `source`.
3. DNS TXT records published by an authority listed in the same registry. Source
   label: `dns:<domain>`.

The registry is the value of governance parameter `network.seeds` (one of the keys
in the default `Governance.AllowedParams`). It is JSON:

```json
{
  "version": 1,
  "refreshSeconds": 900,
  "authorities": [
    { "domain": "seeds.example.org", "algorithm": "ed25519",
      "publicKey": "<base64 ed25519 public key>",
      "lookup": "", "notBefore": 0, "notAfter": 0 }
  ],
  "static": [
    { "nodeId": "0x...", "address": "host:port", "source": "", "notBefore": 0, "notAfter": 0 }
  ]
}
```

`version` must be `1` (0 is read as 1). `algorithm` is optional: empty means `ed25519`, and any other value (compared case-insensitively) is rejected with `unsupported algorithm` (`p2p/seeds/registry.go`, `Authority.validate`).
`refreshSeconds` defaults to 900. `lookup` defaults to `_nhbseed.<domain>`. An
entry is used only between its `notBefore` and `notAfter` (Unix seconds) when set.
Each TXT record must start with `nhbseed:v1:` followed by base64 JSON
(`nodeId`, `address`, optional `notBefore`/`notAfter`, `signature`); it is accepted
only if the ed25519 signature verifies against the authority's `publicKey` for
`nodeId`, `address`, the validity window and the domain.

## 1. Build and run the seed node

```bash
go build -o nhb ./cmd/nhb
./nhb --config /etc/nhb/config.toml
```

The p2p listen address is `ListenAddress` in `config.toml` (the repository's
`config.toml` uses `127.0.0.1:6001`; set an address peers can reach). `nhb` flags:
`--config`, `--genesis`, `--allow-autogenesis`, `--allow-migrate`. The
`nhb.service` unit in `deploy/systemd` is the packaged way to run it
([One-shot deployment](../../docs/deploy/one-shot-deploy.md)).

## 2. Node identity

There is no identity-generation flag. On first start `nhb` creates
`<DataDir>/p2p/node_key.json` (`p2p.LoadOrCreateIdentity`) and keeps the peerstore
in `<DataDir>/p2p/peerstore`. Read the node ID from the `net_info` RPC (no
authentication needed):

```bash
curl -s -X POST http://<RPCAddress>/ -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"net_info","params":[]}'
```

The result contains `nodeId`, `peerCounts`, `chainId`, `genesisHash` and
`listenAddrs`.

## 3. Create a DNS authority record

`ops/seeds/tools/authority` generates a new ed25519 authority key pair on every
run and signs one seed record with it:

```bash
go run ./ops/seeds/tools/authority \
  --domain seeds.example.org \
  --host seed-a.example.org \
  --port 46656 \
  --node-id <0xNODEID> \
  --out authority.json
```

Flags: `--domain`, `--host`, `--node-id` (all required), `--port` (default
`46656`), `--lookup` (override the TXT name), `--not-before`, `--not-after`
(Unix seconds), `--out` (default `authority.json`). It writes `authority.json`
(mode 0600) containing `publicKey` **and `privateKey`**, and prints the TXT line
and a registry snippet with `domain`, `algorithm` and `publicKey`. Keep the file
private.

Because each run creates a new key, records for several seeds signed by separate
runs do not verify under a single `authorities` entry (an entry holds one public
key). The tool has no option to reuse an existing key.

Publish the printed TXT record at `_nhbseed.<domain>` (or your `--lookup` name).

`ops/seeds/tools/dnsstub` serves the TXT record from an `authority.json` for local
testing (`--authority`, `--listen` default `127.0.0.1:8053`, `--ttl` default 60).

## 4. Stage the governance change

Submit a governance parameter proposal that sets `network.seeds` to the registry
JSON above, then follow the normal voting, queue and execution steps. Payloads
that fail `seeds.Parse` are rejected by the governance engine
(`native/governance/engine.go`).

## 5. Check the result

- `net_info` shows the node's own `nodeId` and `listenAddrs`; `net_peers` lists
  connected peers. Both are unauthenticated JSON-RPC methods.
- `net_dial` with `{"target": "<target>"}` dials a peer (`Server.DialPeer` in
  `p2p/server.go` accepts a node ID or an address) and needs a JWT.
- The node re-resolves the registry every `refreshSeconds` (`p2p/server.go`).
- To inspect the DNS side, run `dig TXT _nhbseed.<domain>` and decode the
  base64 part after `nhbseed:v1:`.

## Files and directories

- `<DataDir>/p2p/node_key.json`: node identity.
- `<DataDir>/p2p/peerstore`: peerstore database.
