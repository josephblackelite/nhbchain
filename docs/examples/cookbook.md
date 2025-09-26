# NHBCoin RPC & REST Cookbook

This cookbook shows how to move between the public JSON-RPC endpoints (`https://rpc.nhbcoin.net`) and the escrow REST gateway (`https://api.nhbcoin.net`). Each scenario can be run against devnet or testnet by flipping environment variables, and every command is safe to copy/paste.

---

## 1. Environment targets

| Network | JSON-RPC | WebSocket | REST gateway |
|---------|----------|-----------|--------------|
| **Testnet** | `https://rpc.nhbcoin.net` | `wss://ws.nhbcoin.net` | `https://api.nhbcoin.net/escrow/v1` |
| **Devnet** | `https://rpc.devnet.nhbcoin.net` | `wss://ws.devnet.nhbcoin.net` | `https://api.devnet.nhbcoin.net/escrow/v1` |

Set the targets once per shell:

```bash
export NHB_RPC_URL=https://rpc.nhbcoin.net
export NHB_WS_URL=wss://ws.nhbcoin.net
export NHB_API_BASE=https://api.nhbcoin.net/escrow/v1
export NHB_ADDRESS=nhb1exampleaddress000000000000000000000000
export NHB_API_KEY=your-escrow-api-key            # required for REST writes/reads
export NHB_API_SECRET=your-escrow-api-secret      # HMAC secret paired with the API key
```

> Replace `NHB_ADDRESS` with a funded wallet on the chosen network. For devnet you can request faucet funds; for testnet coordinate with the NHB team. Keep API credentials secret.

---

## 2. Scenario A – Account snapshot via JSON-RPC

Use `nhb_getBalance` to retrieve balances, staking metadata, and human-readable alias data for any account.

```bash
curl -sS "$NHB_RPC_URL" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"nhb_getBalance","params":["'"$NHB_ADDRESS"'"]}' | jq
```

Expected response (values vary per account):

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "address": "nhb1exampleaddress000000000000000000000000",
    "balanceNHB": "125000000",
    "balanceZNHB": "50000000",
    "stake": "75000000",
    "lockedZNHB": "25000000",
    "delegatedValidator": "nhb1validatoralias0000000000000000000000",
    "pendingUnbonds": [],
    "username": "merchant.alpha",
    "nonce": 42,
    "engagementScore": 1180
  }
}
```

Error handling examples:

- Missing address → HTTP 400 with `"message": "address parameter required"`.
- Invalid Bech32 string → HTTP 400 with `"message": "failed to decode address"`.

### Verify balances/events

1. Confirm `balanceNHB` matches the explorer balance for the address.
2. When running on devnet, send a small transfer (e.g. via `nhb-cli send`) and re-run the command—`nonce` should increment and the new transaction should appear in Scenario B below.

---

## 3. Scenario B – Monitor the latest activity

Leverage the helper scripts to combine multiple RPC calls and surface recent transactions.

### Go helper

```bash
NHB_ADDRESS=$NHB_ADDRESS \
NHB_RPC_URL=${NHB_RPC_URL:-https://rpc.nhbcoin.net} \
NHB_API_BASE=${NHB_API_BASE:-https://api.nhbcoin.net/escrow/v1} \
NHB_API_KEY=$NHB_API_KEY \
NHB_API_SECRET=$NHB_API_SECRET \
go run ./examples/cookbook/go
```

What it does:

1. Calls `nhb_getBalance` and prints a formatted JSON snapshot.
2. Calls `nhb_getLatestTransactions` (count = 10) and lists recent transfers.
3. If REST credentials are present, signs a HMAC request and queries `GET /trades?buyer=...` on the escrow gateway to surface settled trades.

Sample terminal output:

```
RPC base: https://rpc.nhbcoin.net
REST base: https://api.nhbcoin.net/escrow/v1
Address: nhb1example...

==> nhb_getBalance
{
  "address": "nhb1example...",
  "balanceNHB": "125000000",
  "balanceZNHB": "50000000",
  "stake": "75000000",
  "username": "merchant.alpha",
  "nonce": 42,
  "engagementScore": 1180
}

==> nhb_getLatestTransactions
 1. nhb1payor... -> nhb1example... (25000000)
 2. nhb1example... -> nhb1payee... (10000000)

==> GET /trades (escrow gateway)
trade trd_01HXYZ... status SETTLED amount 35000000
```

### JavaScript helper

The Node.js variant exposes the same flow and is handy for automation pipelines:

```bash
node ./examples/cookbook/js/index.mjs
```

> Requires Node.js 18+ (for the built-in `fetch`). Both helpers exit early with descriptive messages if required environment variables are missing or if the RPC returns an error code.

### Verify balances/events

- After submitting a test transfer on devnet, re-run either helper: the recent transaction list should include the new hash.
- For escrow flows, expect `status` to move through `TRADE_INIT` → `FUNDED` → `SETTLED`. The helper reports the last five settled trades for the configured buyer.

---

## 4. Scenario C – Escrow audit via REST

All REST calls must be signed. The signature formula matches the escrow gateway specification:

```
signature = Base64(HMAC-SHA256(api_secret, method + "\n" + path_with_query + "\n" + body + "\n" + timestamp))
```

A one-liner using Python and `curl` keeps things copy/paste friendly:

```bash
python3 - <<'PY'
import base64, hashlib, hmac, os, time, urllib.parse, json, urllib.request
api_base = os.environ.get('NHB_API_BASE', 'https://api.nhbcoin.net/escrow/v1')
api_key = os.environ['NHB_API_KEY']
api_secret = os.environ['NHB_API_SECRET']
address = os.environ['NHB_ADDRESS']
path = f"/trades?buyer={urllib.parse.quote(address)}&status=SETTLED&limit=5"
timestamp = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
msg = '\n'.join(['GET', path, '', timestamp]).encode()
sig = base64.b64encode(hmac.new(api_secret.encode(), msg, hashlib.sha256).digest()).decode()
req = urllib.request.Request(api_base + path, method='GET', headers={
    'Accept': 'application/json',
    'X-API-Key': api_key,
    'X-Timestamp': timestamp,
    'X-Signature': sig,
})
with urllib.request.urlopen(req) as resp:
    print(resp.read().decode())
PY
```

Expected JSON envelope:

```json
{
  "data": [
    {
      "id": "trd_01HXYZ...",
      "buyer": "nhb1example...",
      "seller": "nhb1market...",
      "status": "SETTLED",
      "amount": "35000000",
      "token": "NHB",
      "settled_at": "2024-06-24T18:11:03Z"
    }
  ],
  "paging": {
    "next_cursor": null,
    "prev_cursor": null,
    "limit": 5
  }
}
```

To check SLA health for the same environment, swap the path to `/metrics/sla`—the signature logic stays identical.

### Verify balances/events

- Compare `amount` and `settled_at` against on-chain settlement transactions returned by `nhb_getLatestTransactions`.
- For devnet dispute testing, change the query to `status=DISPUTED` and confirm the trade history reflects the dispute event.

---

## 5. Postman collection

Import [`examples/postman/NHB.postman_collection.json`](../../examples/postman/NHB.postman_collection.json) into Postman. The collection contains:

- Four JSON-RPC requests (`nhb_getBalance`, `nhb_getLatestBlocks`, `nhb_getLatestTransactions`, `nhb_getEpochSummary`).
- Two REST requests (`GET /trades`, `GET /metrics/sla`).

Fill in the collection variables (`rpc_base`, `api_base`, `wallet_address`, `api_key`, `api_secret`). The pre-request script automatically signs REST calls using the HMAC recipe above. Switch to devnet by editing the base URLs (e.g., `https://rpc.devnet.nhbcoin.net`).

---

## 6. Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `rpc responded with status 401` | RPC bearer token required for privileged methods. | Stick to public methods listed above or include the bearer token header. |
| `gateway returned status 401` | Missing/invalid `X-API-Key` or HMAC signature. | Re-issue credentials and ensure the timestamp is RFC 3339 within ±60 seconds. |
| Empty `data` array on escrow call | Address has no trades matching the filter. | Drop the `status` filter or query on `seller=` instead of `buyer=`. |
| `invalid address parameter` | Input is not Bech32 (`nhb1...`). | Copy the address from `nhb-cli` or explorer and retry. |

With these recipes you can validate balances, watch real-time activity, and audit escrow settlements on both devnet and testnet without digging through raw node logs.
