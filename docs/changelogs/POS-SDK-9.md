# POS-SDK-9: NHB-Pay spec + NFC/NDEF + SDK examples

* Published the NHB Pay URI specification with canonical signing rules, updated
  intent fields, and an end-to-end example URI.
* Documented the NFC NDEF layouts for NHB Pay, including the dual URI/CBOR
  records and the expected signature bytes.
* Added SDK examples under `sdk/pos/examples/`: Go (`create_intent.go`) and
  TypeScript (`submit_and_watch.ts`) examples build the canonical NHB Pay
  intent string and sign it with ed25519, and `submit_and_watch.ts` and
  `subscriber.ts` stream finality updates via `pos.v1.Realtime/SubscribeFinality`.
* The `AuthorizePayment` gRPC call in those examples no longer works: the
  `pos.v1.Tx` gRPC service was retired and is not registered on the node
  (`rpc/http.go`, NHB-AUDIT-S3), so the call returns `Unimplemented`, and the
  signature is never attached to it. The working submission path is a
  client-signed `TxTypePOSAuthorize` transaction sent via `nhb_sendTransaction`.
  Treat the examples as references for canonical-string construction and
  realtime streaming only.
* Documented in `docs/api/gateway-pos.md` that the gateway exposes no
  `/api/pos/*` HTTP surface; status lookup is the node's
  `pos_getAuthorizationByIntentRef` JSON-RPC method.
