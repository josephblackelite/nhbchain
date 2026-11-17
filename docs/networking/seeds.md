# Network Seed Registry

A node combines up to three sources of seed peers (`p2p/seeds/registry.go`,
`p2p/server.go`):

1. **Static configuration**: `[p2p] Seeds` entries in the local TOML, each
   `0xNODEID@host:port`. Entries without a node ID are ignored with a warning.
2. **Registry static fallbacks**: the `static` list of the on-chain `network.seeds`
   governance parameter.
3. **DNS authorities**: TXT records signed by the Ed25519 keys listed in the same
   registry.

The merged catalogue is what the connection manager dials (one dial loop per
active seed) and what `p2p_info` returns under `seeds` (see
[net-rpc.md](net-rpc.md)). Each entry carries its source: `config`,
`registry.static` (or the entry's own `source` string), or `dns:<domain>`.

**The registry is read once, when the process starts.** `cmd/nhb` and `cmd/p2pd`
load the `network.seeds` parameter at start-up (`Node.NetworkSeedsParam`,
`core/node.go` line 5144) and parse it once; no code reloads it afterwards. A
governance change to `network.seeds` reaches a node only after that node
restarts. While running, the node re-resolves DNS for the registry it started
with.

## Registry payload

The `network.seeds` value is a JSON object (`seeds.Registry`):

```json
{
  "version": 1,
  "refreshSeconds": 900,
  "authorities": [
    {
      "domain": "seeds.mainnet.example.org",
      "algorithm": "ed25519",
      "publicKey": "<base64 Ed25519 public key>",
      "lookup": "_nhbseed.seeds.mainnet.example.org",
      "notBefore": 1700000000,
      "notAfter": 0
    }
  ],
  "static": [
    {
      "nodeId": "0x1234...",
      "address": "seed1.mainnet.example.org:46656",
      "source": "registry.static",
      "notBefore": 0,
      "notAfter": 0
    }
  ]
}
```

Rules enforced by `seeds.Parse` (which the governance engine also runs when a
proposal touching `network.seeds` is submitted, `native/governance/engine.go`
line 421):

- `version` may be omitted (treated as `1`); any other value than `1` is
  rejected.
- `refreshSeconds` is how often running nodes re-query DNS; `0` or omitted means
  15 minutes.
- Each authority needs a non-empty `domain`, `algorithm` `ed25519` (or empty),
  and a base64 `publicKey` that decodes to 32 bytes. `lookup` is the TXT name to
  query and defaults to `_nhbseed.<domain>`. `notBefore` / `notAfter` (Unix
  seconds, `0` = unset) bound when the authority is used; `notAfter` must not be
  before `notBefore`.
- Each static entry needs a `nodeId` and an `address` that is `host:port`, with
  the same optional `notBefore` / `notAfter`.
- An empty payload is rejected. The parameter must also be in the governance
  allow-list (`network.seeds` is in the repo `config.toml` `AllowedParams`).

## DNS record format

An authority publishes each seed as a TXT record whose value is

```
nhbseed:v1:<base64 of the JSON below>
```

```json
{
  "nodeId": "0x1234...",
  "address": "seed1.mainnet.example.org:46656",
  "notBefore": 1700000000,
  "notAfter": 1700600000,
  "signature": "<base64 Ed25519 signature>"
}
```

The signature is over these bytes (`buildSigningMessage`), where `nodeId` is
normalized to lower-case `0x` form and the domain is lower-cased:

```
<nodeId>\n<address>\n<notBefore>\n<notAfter>\n<domain>
```

`notBefore` and `notAfter` are decimal integers (`0` when absent). A record
outside its window is skipped; a record with an invalid signature or format is
reported as an error and skipped while valid records from the same lookup are
still used.

## Runtime behaviour

At start (`cmd/nhb/main.go`, `cmd/p2pd/main.go`):

1. Config seeds are normalized and tagged `config`.
2. If `network.seeds` exists, the registry is parsed. Its active static entries
   and the DNS records resolved with a 5-second timeout are added, skipping
   duplicates.
3. The merged list is handed to the P2P server, which drops entries outside their
   activation window and de-duplicates by `nodeId@address`.
4. If a registry is present, a loop re-resolves it every `refreshSeconds`
   (15 minutes by default). After a refresh that returns at least one seed, the
   registry-sourced (non-config) seeds are replaced by the new result; a refresh
   that returns nothing leaves the previous registry-sourced seeds in place.
   Failures are logged as `Seed registry refresh failed`. Config seeds are never
   removed.
5. New seeds get a dial loop immediately; a seed that leaves the catalogue or
   whose window ends stops being dialed.

DNS lookups use Go's default resolver (`seeds.DefaultResolver()`); there is no
setting that points a node at a different DNS server.

## Rotating seeds through governance

1. **Generate keys and records.** Use the helper (see below) to create an Ed25519
   authority key and the signed TXT record for each seed.
2. **Publish the TXT records** at the authority's lookup name.
3. **Submit a proposal** that sets `network.seeds`. With `nhb-cli`
   (`cmd/nhb-cli/gov.go`), where `seeds-payload.json` contains
   `{"network.seeds": { ...registry... }}`:

   ```bash
   nhb-cli gov propose --kind param.update --payload @seeds-payload.json \
     --key proposer.key --deposit 1000e18
   ```

   The other steps are `nhb-cli gov vote`, `finalize`, `queue` and `execute`,
   each taking `--id` and `--key`. Voting period, timelock, quorum and deposit
   come from the `[governance]` settings.
4. **Restart nodes** so they read the new registry (see above).

## Local verification with the helpers

`ops/seeds/tools/authority` and `ops/seeds/tools/dnsstub` build a signed record
and serve it over DNS:

```bash
go run ./ops/seeds/tools/authority \
  --domain dev.seeds.local --host 127.0.0.1 --port 46656 \
  --node-id 0xFEED... --out authority.json
```

Flags: `--domain`, `--host` and `--node-id` are required; `--port` (default
`46656`), `--lookup`, `--not-before`, `--not-after`, `--out` (default
`authority.json`). It writes `authority.json` (mode `0600`, includes the private
key) containing the domain, lookup name, node ID, address, public and private
keys and the TXT value, and prints the TXT record and a registry `authorities`
snippet.

```bash
go run ./ops/seeds/tools/dnsstub --authority authority.json --listen 127.0.0.1:8053 --ttl 60
```

The stub answers TXT queries for the lookup name over UDP and TCP. Because a node
resolves through the operating system's resolver, using the stub with a running
node requires configuring that resolver yourself.

See also [ops.md](ops.md) and [`ops/seeds/runbook.md`](../../ops/seeds/runbook.md).
