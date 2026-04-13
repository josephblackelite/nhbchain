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
nhb-cli id set-alias \
  --addr nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm \
  --alias frankrocks
```

**Response (JSON)**

```json
{"ok":true}
```

## Add Address

```bash
nhb-cli id add-address \
  --owner nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm \
  --alias frankrocks \
  --addr nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y
```

**Response (JSON)**

```json
{
  "alias": "frankrocks",
  "aliasId": "0x5e2c4fd5...",
  "primary": "nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm",
  "addresses": [
    "nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm",
    "nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y"
  ],
  "createdAt": 1718216400,
  "updatedAt": 1718217000
}
```

## Remove Address

```bash
nhb-cli id remove-address \
  --owner nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm \
  --alias frankrocks \
  --addr nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y
```

## Set Primary Address

```bash
nhb-cli id set-primary \
  --owner nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm \
  --alias frankrocks \
  --addr nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y
```

**Response (JSON)**

```json
{
  "alias": "frankrocks",
  "aliasId": "0x5e2c4fd5...",
  "primary": "nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y",
  "addresses": [
    "nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y",
    "nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm"
  ],
  "createdAt": 1718216400,
  "updatedAt": 1718217600
}
```

## Rename Alias

```bash
nhb-cli id rename \
  --owner nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm \
  --alias frankrocks \
  --new-alias frankr0cks
```

## Resolve Alias

```bash
nhb-cli id resolve --alias frankr0cks
```

**Sample Output**

```json
{
  "alias": "frankr0cks",
  "aliasId": "0x7be9a4c1...",
  "primary": "nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y",
  "addresses": [
    "nhb1alt4vrc6j9j9r4w0l5z7p3yyd86x8k6qfsu8y",
    "nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgpprm"
  ],
  "avatarRef": "https://cdn.nhb/id/frankr0cks.png",
  "createdAt": 1718216400,
  "updatedAt": 1718218200
}
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
