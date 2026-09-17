# Wallet gateway examples

This page collects ready-made calls for wallet backends that proxy signed
transactions through the gateway. All examples assume the gateway is reachable
at `https://app.nhbcoin.com`, the consensus node expects bearer authentication,
and the wallet server holds the `NHB_RPC_TOKEN` secret.

## Sending a ZNHB transfer with `curl`

```bash
curl -s \
  -X POST \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $NHB_RPC_TOKEN" \
  -d '{
        "jsonrpc":"2.0",
        "id":99,
        "method":"nhb_sendTransaction",
        "params":[
          {
            "chainId":"0x4e4842",
            "type":16,
            "nonce":7,
            "to":"0x1b9b9fb69f2c6c9c1d4c1c4e7b999b20461ab29f",
            "value":"0x2386f26fc10000",
            "gasLimit":"0x61a8",
            "gasPrice":"0x3b9aca00",
            "data":"0x",
            "r":"0xc1efc6c2f0c3f3d71e2c195911edbf7a7e8bc2bd52d4b3f6b14d4b0e54738b62",
            "s":"0x27a1a8e31f42d8c3e65d021779f8921bb5ca5066a8b0f67fc6f2df548b6e2771",
            "v":"0x1b"
          }
        ]
      }' \
  https://app.nhbcoin.com/v1/transactions/send
```

Successful requests return the JSON-RPC response from the node, confirming that
the transaction hit the mempool. Any `401` or `429` errors indicate the standard
authentication and rate limit guards are still enforced through the gateway.

## Sending the same payload with Postman

1. Create a `POST` request pointing at
   `https://app.nhbcoin.com/v1/transactions/send`.
2. Add headers: `Content-Type: application/json` and
   `Authorization: Bearer {{NHB_RPC_TOKEN}}`.
3. Switch the body to **raw** JSON and paste the transaction payload.
4. Send the request; Postman will display the JSON-RPC response returned by the
   validator node.

These flows match the expectations outlined in
[`docs/transactions/znhb-transfer.md`](../transactions/znhb-transfer.md), but
move through the hardened gateway path so the bearer token never leaves your
backend.
