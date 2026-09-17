# Network Seed Registry

The network seed system couples a governance-managed registry with signed DNS
records so operators can rotate bootstrap peers without rebuilding every node
binary. Each node merges three sources of truth when it starts:

1. **Static configuration** – entries from `[p2p].Seeds` in the local TOML file.
2. **Registry static fallbacks** – emergency entries embedded in the
   `network.seeds` governance parameter.
3. **DNS authorities** – TXT records signed by the authorities enumerated in the
   registry.

The merged catalogue is exposed via `net_info` so observability tooling can see
exactly which seeds were considered.

## Governance payload structure

Governance proposals targeting `network.seeds` must submit a JSON payload with
this shape:

```json
{
  "version": 1,
  "refreshSeconds": 900,
  "authorities": [
    {
      "domain": "seeds.mainnet.example.org",
      "algorithm": "ed25519",
      "publicKey": "<base64 public key>",
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

* `version` currently must be `1`.
* `refreshSeconds` controls how frequently running nodes poll DNS authorities.
  Omit or set to zero to fall back to 15 minutes.
* Each authority entry points at a DNS zone and the base64-encoded Ed25519
  public key used to verify TXT payloads. `notBefore` / `notAfter` act as a
  timelock for the authority itself.
* Static entries serve as an emergency fallback when DNS or the registry is
  unreachable. They use the same `notBefore` / `notAfter` semantics to schedule
  rotations.

The governance engine validates the payload during proposal submission; invalid
DNS keys or empty entries are rejected before voting starts.

## DNS record format

Authorities publish seed endpoints as TXT records prefixed with `nhbseed:v1:`.
The suffix is a base64-encoded JSON object with the following schema:

```json
{
  "nodeId": "0x1234...",
  "address": "seed1.mainnet.example.org:46656",
  "notBefore": 1700000000,
  "notAfter": 1700600000,
  "signature": "<base64 Ed25519 signature>"
}
```

Nodes construct the verification message as the lowercase node ID, address and
Unix activation bounds separated by newlines followed by the authority domain:

```
<nodeId>\n<address>\n<notBefore>\n<notAfter>\n<domain>
```

The Ed25519 signature must be produced over this exact byte sequence. `notBefore`
and `notAfter` default to zero which means “active immediately” and “no expiry”.
Any entry outside its activation window is ignored until the window opens again.

## Runtime behaviour

On boot the P2P server performs the following steps:

1. Normalise local config seeds and mark them with the source `config`.
2. Parse the `network.seeds` registry (if present) and record static fallbacks.
3. Query each active DNS authority; verified records are tagged with
   `dns:<authority>`.
4. Merge, de-duplicate and expose the final list to the dialer and RPC layer.
5. Periodically refresh authorities on the configured cadence. If a refresh
   fails the node keeps the last known-good set of DNS seeds and always preserves
   config/static entries.

The connection manager tracks the merged catalogue and spawns dial loops for new
seeds immediately. Seeds removed from DNS or the registry wind down gracefully
once their activation window ends.

## Governance rotation workflow

1. **Prepare new seed identities** – operators generate Ed25519 keys for each
   DNS authority, derive node IDs for the replacement peers, and produce signed
   TXT payloads.
2. **Stage DNS updates** – publish the new `nhbseed:v1:` records with sensible
   TTLs (60–300 seconds) and leave the old records in place until the proposal
   executes.
3. **Submit proposal** – craft a `network.seeds` payload that includes the new
   authority public keys and optional static fallback entries. Use `notBefore`
   timestamps to align on-chain activation with DNS TTL expiry if needed.
4. **Execute & monitor** – once the timelock expires and the proposal executes
   the runtime begins honouring the new catalogue. Monitor `net_info` or the
   node logs (`Seed registry refresh failed` messages) for any anomalies.

Because authorities can be timelocked independently, the community can stage a
rotation days in advance while keeping DNS live traffic on the previous cohort.

## Local verification

You can exercise the full discovery pipeline on a workstation without external
infrastructure:

1. **Generate a temporary authority**
   ```bash
   go run ./ops/seeds/tools/authority \
     --domain dev.seeds.local \
     --output authority.json
   ```
   The helper (documented in the runbook) prints an Ed25519 keypair and a sample
   TXT record payload.

2. **Create a `network.seeds` payload** that references the generated key and a
   static fallback:
   ```json
   {
     "version": 1,
     "authorities": [
       {
         "domain": "dev.seeds.local",
         "algorithm": "ed25519",
         "publicKey": "<from authority.json>",
         "lookup": "_nhbseed.dev.seeds.local"
       }
     ],
     "static": [
       {
         "nodeId": "0xfeed...",
         "address": "127.0.0.1:46656",
         "source": "registry.static"
       }
     ]
   }
   ```

3. **Populate the on-chain parameter** by importing the JSON into your devnet
   state (or using `nhbctl gov execute --param network.seeds --file ...`).

4. **Serve the TXT record locally**. The runbook includes a minimal Go program
   (`ops/seeds/tools/dnsstub`) that reads `authority.json` and responds to
   `_nhbseed.dev.seeds.local` lookups on `127.0.0.1:8053`.

5. **Start the node** with a config that omits `[p2p].Seeds` and export
   `NHB_DNS=127.0.0.1:8053` so the resolver targets the stub. On startup the
   logs should include the merged catalogue. Any DNS resolution failures are
   logged but the static entries remain active.

6. **Inspect `net_info`** to confirm the resolved seed is reported with
   `source="dns:dev.seeds.local"` and that the static fallback carries the
   `registry.static` tag.

These steps demonstrate discovery with only registry and DNS inputs—no static
configuration is required.

## Additional references

* [`docs/networking/ops.md`](./ops.md) summarises operational checklists.
* [`ops/seeds/runbook.md`](../../ops/seeds/runbook.md) contains a full
  provisioning playbook for independent seed operators.
