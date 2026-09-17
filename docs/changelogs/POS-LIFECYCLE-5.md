# POS-LIFECYCLE-5

## Summary

* Added a POS payment lifecycle engine that supports authorizing, capturing, and
  voiding ZapNHB transactions with automatic expiry handling.
* Emitted structured events for authorization, capture, and void milestones to
  keep downstream services synchronized.
* Documented the lifecycle, error codes, and new RPC messages in
  `docs/specs/pos-lifecycle.md`.
* Introduced unit tests covering partial captures, double-capture rejection, and
  automatic voiding on expiry.

## Testing

* `go test ./native/pos...`
