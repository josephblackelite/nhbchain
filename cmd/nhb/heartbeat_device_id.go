package main

import "fmt"

// validatorHeartbeatDeviceID names the device of a validator's own heartbeat.
// The device id is written into the heartbeat transaction and from there into
// public chain data (nhb_getLatestTransactions shows it to anyone), so it must
// not carry anything about the host. It used to be the host name plus four
// bytes of the validator address, and on the live validators the host name is
// the cloud provider's private address ('ip-172-31-16-199'), which published
// the private network address of both validators (ledger PL-EXT-88).
func validatorHeartbeatDeviceID(validatorAddr []byte) string {
	prefix := validatorAddr
	if len(prefix) > 4 {
		prefix = prefix[:4]
	}
	return fmt.Sprintf("validator-%x", prefix)
}
