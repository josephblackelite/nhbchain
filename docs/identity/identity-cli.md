# `nhb-cli` Identity Commands

The `nhb-cli` tool includes subcommands under `nhb-cli id` for looking up identity records.

> **Retired:** `set-alias`, `set-avatar`, `add-address`, `remove-address`, `set-primary`, `rename`, `create-claimable` and
> `claim` called node methods that are retired (HTTP 410, JSON-RPC error `-32060`): they changed validator-local state outside
> block execution. Each of these commands now says so and exits non-zero without contacting the node. Only `resolve` and
> `reverse` are served. The sections below describe the retired commands as they behaved and are kept for reference.

## Common Flags

* `--rpc`: JSON-RPC endpoint (default from the `RPC_URL` environment variable, else `http://localhost:8080`).

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
  --payer nhb1payer... \
  --recipient 0x3a4b... \
  --token NHB \
  --amount 10.00 \
  --deadline 1718736000
```

CLI prints the raw JSON-RPC result (`claimId`, `expiresAt`, etc). Notify the recipient with the claim information.

## Claim Funds

```bash
nhb-cli id claim \
  --id 0x92fd... \
  --payee nhb1recipient... \
  --preimage 0x3a4b...
```

**Output**

```json
{"ok":true,"token":"NHB","amount":"25"}
```

---

Tips:

* Output is always raw JSON — no flag needed for machine-readable responses.
* For advanced scripting, pipe outputs into `jq`.
