package types

// HeartbeatPayload is embedded in the data field of heartbeat transactions.
type HeartbeatPayload struct {
	DeviceID  string `json:"deviceId"`
	Timestamp int64  `json:"timestamp"`
}
