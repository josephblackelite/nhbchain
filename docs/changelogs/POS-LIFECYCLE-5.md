# POS-LIFECYCLE-5

## Summary

* Added a POS payment lifecycle engine that supports authorizing, capturing, and
  voiding ZapNHB transactions with automatic expiry handling.
* Emitted structured events for authorization, capture, and void milestones to
  keep downstream services synchronized.
* Documented the lifecycle, error codes, and events in
  `docs/specs/pos-lifecycle.md`. The `MsgAuthorizePayment`/`MsgCapturePayment`/
  `MsgVoidPayment` proto messages belong to the `pos.v1.Tx` gRPC service, which
  has since been retired and is not registered on the node (`rpc/http.go`).
  Reachable entry points are the `TxTypePOSAuthorize`/`Capture`/`Void`
  transactions (submitted via `nhb_sendTransaction`) and the JSON-RPC methods
  `pos_getAuthorization` and `pos_getAuthorizationByIntentRef`. `pos_sweepVoids` answers HTTP 410 (`rpc/http.go` `handlePOSSweepVoids`); every block voids expired authorizations itself.
* Introduced unit tests covering partial captures, double-capture rejection, and
  automatic voiding on expiry.

## Testing

* `go test ./native/pos/...`
