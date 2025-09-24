# `nhb-cli` Identity Commands

The `nhb-cli` tool includes subcommands under `nhb-cli id` for interacting with the identity module. Ensure your CLI is
configured with the correct node endpoint (`--node`) and chain ID.

## Common Flags

* `--node`: JSON-RPC endpoint (default from config).
* `--chain-id`: chain ID (e.g., `187001`).
* `--key`: local key name or path to signing key.
* `--expiry`: optional signature expiry override (seconds from now).

All mutating commands prompt for confirmation before broadcasting unless `--yes` is provided.

## Register Alias

```bash
nhb-cli id register \
  --alias frankrocks \
  --owner nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpxxx \
  --primary nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm \
  --sig 0xSIG
```

**Response**

```
✔ Alias registered
Alias ID    : 0x5e2c4fd5...
Primary     : nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm
Created At  : 2024-06-12T18:20:00Z
```

## Add Address

```bash
nhb-cli id add-address \
  --alias frankrocks \
  --addr nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y \
  --sig 0xSIG
```

**Response**

```
✔ Address linked
Addresses: nhb1qyqszqgp..., nhb1alt4vrc6...
```

## Remove Address

```bash
nhb-cli id remove-address \
  --alias frankrocks \
  --addr nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y \
  --sig 0xSIG
```

## Set Primary Address

```bash
nhb-cli id set-primary \
  --alias frankrocks \
  --addr nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y \
  --sig 0xSIG
```

**Output**

```
✔ Primary address updated: nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y
```

## Rename Alias

```bash
nhb-cli id rename \
  --id 0x5e2c4fd5... \
  --new frankr0cks \
  --sig 0xSIG
```

## Fetch Alias Record

```bash
nhb-cli id get --alias frankrocks
```

**Sample Output**

```
Alias       : frankrocks
Alias ID    : 0x5e2c4fd5...
Owner       : nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpxxx
Primary     : nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm
Addresses   :
  - nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm (primary)
  - nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y
Avatar      : https://cdn.nhb/id/frankrocks.png
Created At  : 2024-06-12T18:20:00Z
Updated At  : 2024-06-12T18:45:10Z
Version     : 4
```

## Resolve Alias

```bash
nhb-cli id resolve --alias frankrocks
```

**Output**

```
Alias   : frankrocks
Primary : nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm
Addresses:
  - nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm
  - nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y
Avatar  : https://cdn.nhb/id/frankrocks.png
```

## Create Claimable (Pay by Email)

```bash
nhb-cli id create-claimable \
  --email-hash 0x92fd... \
  --token NHB \
  --amount 10.00 \
  --expiry 1718736000 \
  --sig 0xSIG
```

CLI prints the `claimId` and expiry timestamp. Notify the recipient with the claim information.

## Claim Funds

```bash
nhb-cli id claim \
  --claim-id 0x92fd... \
  --sig 0xRECIPSIG
```

**Output**

```
✔ Claim settled
Amount : 10.00 NHB
To     : nhb1primary...
Tx     : 0xabc123...
```

---

Tips:

* Use `--json` flag on any command to get machine-readable responses.
* Combine with `nhb-cli events tail --topic identity.*` to monitor alias activity.
* For advanced scripting, pipe outputs into `jq`.
