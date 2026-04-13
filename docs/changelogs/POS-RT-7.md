# POS-RT-7: realtime finality stream

* Added `pos.v1.Realtime/SubscribeFinality` gRPC stream and `/ws/pos/finality`
  WebSocket endpoint for live POS transaction updates.
* Streams emit `tx_update` messages for `pending` and `finalized` statuses with
  resume cursors.
* Documented subscription flow, reconnection semantics, and provided a
  TypeScript subscriber example.
