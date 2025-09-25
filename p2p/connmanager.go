package p2p

import (
	"fmt"
	"strings"
	"time"
)

type connManager struct {
	server *Server
	seeds  []seedEndpoint
	store  *Peerstore
	now    func() time.Time
	quit   chan struct{}
}

func newConnManager(server *Server) *connManager {
	mgr := &connManager{
		server: server,
		seeds:  append([]seedEndpoint{}, server.seeds...),
		store:  server.peerstore,
		now:    server.now,
		quit:   make(chan struct{}),
	}
	if mgr.now == nil {
		mgr.now = time.Now
	}
	return mgr
}

func (m *connManager) start() {
	if len(m.seeds) == 0 {
		return
	}
	for _, seed := range m.seeds {
		seedCopy := seed
		go m.runSeedLoop(seedCopy)
	}
}

func (m *connManager) stop() {
	select {
	case <-m.quit:
	default:
		close(m.quit)
	}
}

func (m *connManager) runSeedLoop(seed seedEndpoint) {
	for {
		if !m.ensureReady(seed) {
			return
		}
		if m.server.isConnectedToAddress(seed.Address) {
			if !m.wait(5 * time.Second) {
				return
			}
			continue
		}
		if err := m.server.Connect(seed.Address); err != nil {
			fmt.Printf("Seed dial %s (%s) failed: %v\n", seed.Address, seed.NodeID, err)
			m.markFailure(seed)
			continue
		}
	}
}

func (m *connManager) ensureReady(seed seedEndpoint) bool {
	for {
		if m.shouldStop() {
			return false
		}
		if m.store != nil {
			entry := PeerstoreEntry{Addr: seed.Address, NodeID: seed.NodeID}
			if err := m.store.Put(entry); err != nil {
				fmt.Printf("Persist seed %s: %v\n", seed.Address, err)
			}
			now := m.now()
			if m.store.IsBanned(seed.NodeID, now) {
				next := m.store.NextDialAt(seed.Address, now)
				if !m.waitUntil(next) {
					return false
				}
				continue
			}
			next := m.store.NextDialAt(seed.Address, now)
			if next.After(now) {
				if !m.waitUntil(next) {
					return false
				}
				continue
			}
		}
		return true
	}
}

func (m *connManager) markFailure(seed seedEndpoint) {
	if m.store == nil {
		return
	}
	if _, err := m.store.RecordFail(seed.NodeID, m.now()); err != nil {
		fmt.Printf("Record seed failure %s: %v\n", seed.NodeID, err)
	}
}

func (m *connManager) waitUntil(target time.Time) bool {
	delay := time.Until(target)
	if delay <= 0 {
		return true
	}
	return m.wait(delay)
}

func (m *connManager) wait(delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-m.quit:
		return false
	}
}

func (m *connManager) shouldStop() bool {
	select {
	case <-m.quit:
		return true
	default:
		return false
	}
}

func (s *Server) startDialers() {
	seen := make(map[string]struct{})
	addresses := append([]string{}, s.cfg.Bootnodes...)
	addresses = append(addresses, s.cfg.PersistentPeers...)
	for _, addr := range addresses {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		go func(target string) {
			if err := s.Connect(target); err != nil {
				fmt.Printf("Bootstrap dial %s failed: %v\n", target, err)
				s.scheduleReconnect(target)
			}
		}(addr)
	}
}

func (s *Server) scheduleReconnect(addr string) {
	if addr == "" {
		return
	}
	if s.isConnectedToAddress(addr) {
		return
	}
	s.dialMu.Lock()
	if _, pending := s.pendingDial[addr]; pending {
		s.dialMu.Unlock()
		return
	}
	delay := s.backoff[addr]
	if delay == 0 {
		delay = time.Second
	} else {
		delay *= 2
		if delay > maxDialBackoff {
			delay = maxDialBackoff
		}
	}
	s.pendingDial[addr] = struct{}{}
	s.backoff[addr] = delay
	s.dialMu.Unlock()

	go func(wait time.Duration) {
		timer := time.NewTimer(wait)
		<-timer.C
		s.dialMu.Lock()
		delete(s.pendingDial, addr)
		s.dialMu.Unlock()
		if err := s.Connect(addr); err != nil {
			fmt.Printf("Reconnect to %s failed: %v\n", addr, err)
			s.scheduleReconnect(addr)
		} else {
			s.resetBackoff(addr)
		}
	}(delay)
}

func (s *Server) resetBackoff(addr string) {
	s.dialMu.Lock()
	s.backoff[addr] = 0
	s.dialMu.Unlock()
}

func (s *Server) isPersistent(addr string) bool {
	s.dialMu.Lock()
	defer s.dialMu.Unlock()
	_, ok := s.persistent[strings.TrimSpace(addr)]
	return ok
}

func (s *Server) isConnectedToAddress(addr string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.byAddr[strings.TrimSpace(addr)]
	return ok
}

func (s *Server) markDialFailure(addr string) {
	if s.peerstore == nil {
		return
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return
	}
	rec, ok := s.peerstore.Get(addr)
	if !ok || rec.NodeID == "" {
		return
	}
	if _, err := s.peerstore.RecordFail(rec.NodeID, s.now()); err != nil {
		fmt.Printf("record dial failure %s: %v\n", rec.NodeID, err)
	}
}
