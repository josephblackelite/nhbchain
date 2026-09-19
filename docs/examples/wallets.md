# Wallet gateway examples

This page collects ready-made calls for wallet backends that proxy signed
transactions through the gateway. All examples assume the gateway is reachable
at `https://gateway.example`, the consensus node expects bearer authentication,
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
            "chainId":5130306,
            "type":16,
            "nonce":7,
            "to":"G5uftp8sbJwdTBxOe5mbIEYasp8=",
            "value":10000000000000000,
            "gasLimit":25000,
            "gasPrice":1,
            "data":"",
            "r":SIGNATURE_R,
            "s":SIGNATURE_S,
            "v":SIGNATURE_V
          }
        ]
      }' \
  https://gateway.example/v1/transactions/send
```

Replace `SIGNATURE_R`, `SIGNATURE_S` and `SIGNATURE_V` with the decimal values of
your signature. The gateway decodes the transaction with Go's standard JSON
rules, so numeric fields are JSON numbers and `to`/`data` are base64 (see
[`docs/gateway/transactions.md`](../gateway/transactions.md)). Successful
requests return the JSON-RPC response from the node, whose result is `0x` plus
the transaction hash. Any `401` or `429` errors indicate the standard
authentication and rate limit guards are still enforced through the gateway.

## Sending the same payload with Postman

1. Create a `POST` request pointing at
   `https://gateway.example/v1/transactions/send`.
2. Add headers: `Content-Type: application/json` and
   `Authorization: Bearer {{NHB_RPC_TOKEN}}`.
3. Switch the body to **raw** JSON and paste the transaction payload.
4. Send the request; Postman will display the JSON-RPC response returned by the
   validator node.

These flows match the expectations outlined in
[`docs/transactions/znhb-transfer.md`](../transactions/znhb-transfer.md), but
move through the hardened gateway path so the bearer token never leaves your
backend.
