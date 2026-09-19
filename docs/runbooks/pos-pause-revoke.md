# POS Pause & Revoke Runbook

This runbook covers pausing a merchant's sponsorship and revoking a device. Both are flags in
the POS registry that only affect sponsored transactions; a raw transfer is never blocked by
them. Read [POS merchant and device onboarding](./pos-onboarding.md) first: it lists the
registry records, the messages, and the current behaviour of the transaction path.

## Effect of the flags

`CheckPOSRegistry` (`core/tx/checks.go`) runs as part of `EvaluateSponsorship`
(`core/sponsorship.go`) before any cap check:

* Merchant `Paused = true`: the sponsorship status is `throttled` with the reason
  `merchant sponsorship paused`.
* Device `Revoked = true`: `throttled` with `device sponsorship revoked`.
* Both changes apply to the next evaluation. In the NHB transfer path a sponsored transaction
  that is not `ready` is rejected with `transaction sponsorship rejected: status=throttled
  reason=<reason>` and a `tx.sponsorship.failed` event; it is not executed with sender-paid
  gas (`core/state_transition.go`). Transfers that do not name a paymaster are not affected.

## Pause or resume a merchant

* `native/pos/registry.go` implements `PauseMerchant` and `ResumeMerchant`. Both create the
  merchant record when it is missing, and setting the same value again changes nothing except
  the stored nonce, expiry and chain ID.
* The messages are `pos.v1.MsgPauseMerchant` and `pos.v1.MsgResumeMerchant`, sent as a
  `TxTypePOSRegistry` transaction. As described in the onboarding runbook, the transaction
  handler `applyPOSRegistry` does not currently route these messages to the registry methods:
  a pause message is decoded as a merchant registration and fails with `pos: stale nonce`, and
  there is no handler branch for resume. Do not treat a submitted pause or resume as effective
  until you have read the flag back.
* Verify by reading `pos/merchant/<address>` (`Manager.POSGetMerchant`) and by running
  `tx_previewSponsorship` for a sponsored transaction that names the merchant: the result must
  show `throttled` with `merchant sponsorship paused` while the flag is set.

## Revoke or restore a device

* `RevokeDevice` and `RestoreDevice` require an existing device record (`pos: device <id> not
  registered` otherwise), keep the merchant binding and only flip `Revoked`.
* The messages are `pos.v1.MsgRevokeDevice` and `pos.v1.MsgRestoreDevice`. Neither has a
  branch in `applyPOSRegistry`, so as the code stands a submitted transaction with either
  message does not change the flag.
* Verify by reading `pos/device/<device id>` (`Manager.POSGetDevice`; a missing device returns
  "not found") and with `tx_previewSponsorship`, which must show `throttled` with
  `device sponsorship revoked`.

## Clean up records

`Manager.POSDeleteMerchant` and `Manager.POSDeleteDevice` exist in `core/state/pos_registry.go`.
No transaction type calls them.
