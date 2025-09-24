package p2p

import (
	"fmt"
	"strings"
	"time"
)

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
