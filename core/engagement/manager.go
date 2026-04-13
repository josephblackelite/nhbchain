package engagement

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Manager tracks registered devices and enforces basic heartbeat semantics
// (rate limits and replay protection) before heartbeats are materialised on
// chain.
type Manager struct {
	mu      sync.Mutex
	config  Config
	now     func() time.Time
	devices map[string]*deviceState
}

type deviceState struct {
	id        string
	token     string
	address   [20]byte
	lastStamp int64
}

// NewManager constructs a manager with the provided configuration.
func NewManager(cfg Config) *Manager {
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return &Manager{
		config:  cfg,
		now:     time.Now,
		devices: make(map[string]*deviceState),
	}
}

// SetNow overrides the time source. It is intended for tests.
func (m *Manager) SetNow(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

// RegisterDevice associates a device identifier with a validator address and
// returns a freshly generated authentication token for subsequent heartbeats.
func (m *Manager) RegisterDevice(address [20]byte, deviceID string) (string, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return "", fmt.Errorf("device id required")
	}

	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("token generation failed: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.devices[deviceID]
	if !ok {
		state = &deviceState{id: deviceID}
		m.devices[deviceID] = state
	}
	state.token = token
	state.address = address
	state.lastStamp = 0

	return token, nil
}

// SubmitHeartbeat validates the provided credentials and timestamp against the
// configured rate limits. It returns the timestamp that should be embedded in
// the on-chain heartbeat payload.
func (m *Manager) SubmitHeartbeat(deviceID, token string, timestamp int64) (int64, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return 0, fmt.Errorf("device id required")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, fmt.Errorf("token required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.devices[deviceID]
	if !ok {
		return 0, fmt.Errorf("unknown device")
	}
	if subtle.ConstantTimeCompare([]byte(state.token), []byte(token)) == 0 {
		return 0, fmt.Errorf("invalid token")
	}

	if timestamp == 0 {
		timestamp = m.now().UTC().Unix()
	}
	if timestamp <= 0 {
		return 0, fmt.Errorf("invalid timestamp")
	}
	if state.lastStamp != 0 {
		if timestamp <= state.lastStamp {
			return 0, fmt.Errorf("heartbeat replay")
		}
		minDelta := int64(m.config.HeartbeatInterval.Seconds())
		if timestamp-state.lastStamp < minDelta {
			return 0, fmt.Errorf("heartbeat rate limited")
		}
	}

	state.lastStamp = timestamp
	return timestamp, nil
}
