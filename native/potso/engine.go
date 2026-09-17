package potso

import (
	"errors"
	"fmt"
	"math"
	"sync"

	"nhbchain/observability/metrics"
)

// ErrHeartbeatRateLimited indicates that an address has exceeded the per-epoch
// heartbeat allowance and must wait until the next epoch to resume.
var ErrHeartbeatRateLimited = errors.New("potso: heartbeat rate limit exceeded")

// Engine tracks runtime metrics and abuse signals for POTSO heartbeats.
type Engine struct {
	mu              sync.Mutex
	params          EngineParams
	currentEpoch    uint64
	heartbeats      map[[20]byte]uint64
	totalHeartbeats uint64
	totalUptime     uint64
	uniquePeers     map[[20]byte]struct{}
	telemetry       *metrics.PotsoMetrics
}

// NewEngine constructs a heartbeat engine with the supplied parameters.
func NewEngine(params EngineParams) (*Engine, error) {
	if err := params.Validate(); err != nil {
		return nil, err
	}
	return &Engine{
		params:       params,
		currentEpoch: math.MaxUint64,
		heartbeats:   make(map[[20]byte]uint64),
		uniquePeers:  make(map[[20]byte]struct{}),
		telemetry:    metrics.Potso(),
	}, nil
}

// SetParams replaces the runtime parameters after validation.
func (e *Engine) SetParams(params EngineParams) error {
	if e == nil {
		return fmt.Errorf("potso: engine not initialised")
	}
	if err := params.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	e.params = params
	e.mu.Unlock()
	return nil
}

// Reset clears the cached per-epoch state. Useful when reward configuration
// changes or during tests.
func (e *Engine) Reset() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.currentEpoch = math.MaxUint64
	e.heartbeats = make(map[[20]byte]uint64)
	e.uniquePeers = make(map[[20]byte]struct{})
	e.totalHeartbeats = 0
	e.totalUptime = 0
	e.mu.Unlock()
}

// Precheck validates whether the address may submit another heartbeat within
// the epoch. No state is mutated if the heartbeat is rejected.
func (e *Engine) Precheck(addr [20]byte, epoch uint64) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ensureEpoch(epoch)
	if e.params.MaxHeartbeatsPerEpoch > 0 {
		if count := e.heartbeats[addr]; count >= e.params.MaxHeartbeatsPerEpoch {
			e.recordRateLimited(addr, epoch)
			return ErrHeartbeatRateLimited
		}
	}
	return nil
}

// Commit records an accepted heartbeat, updating metrics and abuse counters.
func (e *Engine) Commit(addr [20]byte, epoch, delta uint64) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ensureEpoch(epoch)
	e.heartbeats[addr]++
	e.totalHeartbeats++
	e.totalUptime += delta
	if _, exists := e.uniquePeers[addr]; !exists {
		e.uniquePeers[addr] = struct{}{}
		if e.telemetry != nil {
			e.telemetry.SetHeartbeatUniquePeers(epoch, float64(len(e.uniquePeers)))
		}
	}
	if e.telemetry != nil {
		e.telemetry.IncHeartbeat(addrString(addr), epoch)
		avg := 0.0
		if e.totalHeartbeats > 0 {
			avg = float64(e.totalUptime) / float64(e.totalHeartbeats)
		}
		e.telemetry.SetHeartbeatAvgSession(epoch, avg)
	}
}

// ObserveWashEngagement marks that an address accrued meter progress while
// emissions are disabled.
func (e *Engine) ObserveWashEngagement(addr [20]byte, epoch uint64) {
	if e == nil {
		return
	}
	if e.telemetry != nil {
		e.telemetry.IncHeartbeatWash(addrString(addr), epoch)
	}
}

func (e *Engine) ensureEpoch(epoch uint64) {
	if e.currentEpoch == epoch {
		return
	}
	e.currentEpoch = epoch
	e.heartbeats = make(map[[20]byte]uint64)
	e.uniquePeers = make(map[[20]byte]struct{})
	e.totalHeartbeats = 0
	e.totalUptime = 0
	if e.telemetry != nil {
		e.telemetry.InitHeartbeatEpoch(epoch)
	}
}

func (e *Engine) recordRateLimited(addr [20]byte, epoch uint64) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.IncHeartbeatRateLimited(addrString(addr), epoch)
}

func addrString(addr [20]byte) string {
	return fmt.Sprintf("0x%x", addr)
}
