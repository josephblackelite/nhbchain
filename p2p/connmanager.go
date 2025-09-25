package p2p

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

const (
	defaultConnmgrCheckInterval = 3 * time.Second
)

type connManager struct {
	server         *Server
	seeds          []seedEndpoint
	store          *Peerstore
	now            func() time.Time
	quit           chan struct{}
	minPeers       int
	outboundTarget int
	maxPeers       int
	checkInterval  time.Duration
}

func newConnManager(server *Server) *connManager {
	if server == nil {
		return nil
	}
	mgr := &connManager{
		server:         server,
		seeds:          append([]seedEndpoint{}, server.seeds...),
		store:          server.peerstore,
		now:            server.now,
		quit:           make(chan struct{}),
		minPeers:       server.cfg.MinPeers,
		outboundTarget: server.cfg.OutboundPeers,
		maxPeers:       server.cfg.MaxPeers,
		checkInterval:  defaultConnmgrCheckInterval,
	}
	if mgr.now == nil {
		mgr.now = time.Now
	}
	if mgr.minPeers <= 0 || mgr.minPeers > mgr.maxPeers {
		mgr.minPeers = mgr.maxPeers / 2
		if mgr.minPeers <= 0 {
			mgr.minPeers = 1
		}
	}
	if mgr.outboundTarget <= 0 || mgr.outboundTarget > server.cfg.MaxOutbound {
		mgr.outboundTarget = server.cfg.MaxOutbound
	}
	if mgr.checkInterval <= 0 {
		mgr.checkInterval = defaultConnmgrCheckInterval
	}
	return mgr
}

func (m *connManager) start() {
	if m == nil {
		return
	}
	m.logNATStatus()
	go m.run()
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
		if m.server == nil {
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

func (m *connManager) run() {
	if m.server == nil {
		return
	}
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.enforceLimits()
			m.fillOutbound()
		case <-m.quit:
			return
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

func (m *connManager) enforceLimits() {
	if m.server == nil {
		return
	}
	now := m.now()
	peers := m.snapshotPeers(now)
	if len(peers) <= m.maxPeers || len(peers) == 0 {
		return
	}
	excess := len(peers) - m.maxPeers
	for excess > 0 {
		idx := victimPeerIndex(peers)
		if idx < 0 || idx >= len(peers) {
			return
		}
		peer := peers[idx]
		if peer.peer == nil {
			return
		}
		fmt.Printf("Connection manager pruning peer %s (score=%d, lastSeen=%s)\n",
			peer.peer.id, peer.score, peer.lastSeen.Format(time.RFC3339))
		peer.peer.terminate(false, fmt.Errorf("pruned by connection manager"))
		peers = append(peers[:idx], peers[idx+1:]...)
		excess--
	}
}

func (m *connManager) fillOutbound() {
	if m.server == nil {
		return
	}
	s := m.server
	s.mu.RLock()
	total := len(s.peers)
	outbound := s.outboundCount
	s.mu.RUnlock()
	if total >= m.maxPeers {
		return
	}
	slots := m.maxPeers - total
	neededOutbound := m.outboundTarget - outbound
	if neededOutbound < 0 {
		neededOutbound = 0
	}
	neededTotal := m.minPeers - total
	if neededTotal < 0 {
		neededTotal = 0
	}
	needed := neededOutbound
	if neededTotal > needed {
		needed = neededTotal
	}
	if needed > slots {
		needed = slots
	}
	if needed <= 0 {
		return
	}
	candidates := m.selectDialCandidates(needed * 2)
	count := 0
	for _, entry := range candidates {
		if count >= needed {
			break
		}
		addr := strings.TrimSpace(entry.Addr)
		if addr == "" {
			continue
		}
		if !m.reserveDial(addr) {
			continue
		}
		count++
		go m.dialAddress(entry)
	}
}

func (m *connManager) dialAddress(entry PeerstoreEntry) {
	addr := strings.TrimSpace(entry.Addr)
	if addr == "" || m.server == nil {
		return
	}
	defer m.releaseDial(addr)
	if err := m.server.Connect(addr); err != nil {
		fmt.Printf("Connection manager dial %s failed: %v\n", addr, err)
		m.server.scheduleReconnect(addr)
	}
}

func (m *connManager) reserveDial(addr string) bool {
	if m.server == nil {
		return false
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return false
	}
	m.server.dialMu.Lock()
	defer m.server.dialMu.Unlock()
	if _, pending := m.server.pendingDial[addr]; pending {
		return false
	}
	m.server.pendingDial[addr] = struct{}{}
	return true
}

func (m *connManager) releaseDial(addr string) {
	if m.server == nil {
		return
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return
	}
	m.server.dialMu.Lock()
	delete(m.server.pendingDial, addr)
	m.server.dialMu.Unlock()
}

func (m *connManager) selectDialCandidates(limit int) []PeerstoreEntry {
	results := make([]PeerstoreEntry, 0, limit)
	if limit <= 0 {
		return results
	}
	now := m.now()
	entries := m.peerstoreEntries()
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Score == entries[j].Score {
			return entries[i].LastSeen.After(entries[j].LastSeen)
		}
		return entries[i].Score > entries[j].Score
	})
	for _, entry := range entries {
		if len(results) >= limit {
			break
		}
		addr := strings.TrimSpace(entry.Addr)
		if addr == "" || entry.NodeID == "" {
			continue
		}
		if m.server.hasPeer(entry.NodeID) {
			continue
		}
		if m.server.isConnectedToAddress(addr) {
			continue
		}
		if m.server.isBanned(entry.NodeID) {
			continue
		}
		if m.store != nil {
			if m.store.IsBanned(entry.NodeID, now) {
				continue
			}
			next := m.store.NextDialAt(addr, now)
			if next.After(now) {
				continue
			}
		}
		results = append(results, entry)
	}
	if len(results) >= limit {
		return results[:limit]
	}
	for _, seed := range m.seeds {
		if len(results) >= limit {
			break
		}
		addr := strings.TrimSpace(seed.Address)
		if addr == "" {
			continue
		}
		if m.server.isConnectedToAddress(addr) {
			continue
		}
		if seed.NodeID != "" && m.server.isBanned(seed.NodeID) {
			continue
		}
		results = append(results, PeerstoreEntry{Addr: addr, NodeID: seed.NodeID})
	}
	if len(results) > limit {
		return results[:limit]
	}
	return results
}

func (m *connManager) snapshotPeers(now time.Time) []connectedPeer {
	if m.server == nil {
		return nil
	}
	statuses := make(map[string]ReputationStatus)
	if m.server.reputation != nil {
		statuses = m.server.reputation.Snapshot(now)
	}
	m.server.mu.RLock()
	peers := make([]*Peer, 0, len(m.server.peers))
	for _, peer := range m.server.peers {
		peers = append(peers, peer)
	}
	records := make(map[string]PeerRecord, len(m.server.records))
	for id, rec := range m.server.records {
		records[id] = *rec
	}
	m.server.mu.RUnlock()
	result := make([]connectedPeer, 0, len(peers))
	for _, peer := range peers {
		if peer == nil {
			continue
		}
		status := statuses[peer.id]
		rec := records[peer.id]
		lastSeen := rec.LastSeen
		if lastSeen.IsZero() {
			lastSeen = now
		}
		result = append(result, connectedPeer{
			peer:     peer,
			lastSeen: lastSeen,
			score:    status.Score,
			inbound:  peer.inbound,
			persist:  peer.persistent,
		})
	}
	return result
}

func victimPeerIndex(peers []connectedPeer) int {
	idx := -1
	for i := range peers {
		peer := peers[i]
		if peer.peer == nil || peer.persist {
			continue
		}
		if idx == -1 {
			idx = i
			continue
		}
		best := peers[idx]
		if peer.score < best.score {
			idx = i
			continue
		}
		if peer.score == best.score {
			if peer.lastSeen.Before(best.lastSeen) {
				idx = i
				continue
			}
			if peer.lastSeen.Equal(best.lastSeen) && peer.inbound && !best.inbound {
				idx = i
			}
		}
	}
	return idx
}

func (m *connManager) peerstoreEntries() []PeerstoreEntry {
	if m.store == nil {
		return nil
	}
	return m.store.Snapshot()
}

func (m *connManager) logNATStatus() {
	if m.server == nil {
		return
	}
	listen := strings.TrimSpace(m.server.cfg.ListenAddress)
	if listen == "" {
		fmt.Printf("Connection manager: listen address not configured; NAT status unknown\n")
		return
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		fmt.Printf("Connection manager: parse listen address %q: %v\n", listen, err)
		return
	}
	host = strings.TrimSpace(host)
	if host == "" {
		fmt.Printf("Connection manager: listening on all interfaces (port %s); assuming private network\n", port)
		m.logUPnPStub(port)
		return
	}
	ip := net.ParseIP(host)
	if ip == nil {
		fmt.Printf("Connection manager: listen host %q not an IP; NAT status unknown\n", host)
		return
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() {
		fmt.Printf("Connection manager: private or unspecified listen address %s detected\n", listen)
		m.logUPnPStub(port)
		return
	}
	fmt.Printf("Connection manager: public listen address %s detected\n", listen)
}

func (m *connManager) logUPnPStub(port string) {
	if port == "" {
		fmt.Printf("Connection manager: UPnP port mapping stub engaged (no port specified)\n")
	} else {
		fmt.Printf("Connection manager: attempting UPnP port mapping for tcp/%s (stub)\n", port)
	}
	fmt.Printf("Connection manager: UPnP functionality not implemented; please ensure manual port forwarding\n")
}

type connectedPeer struct {
	peer     *Peer
	lastSeen time.Time
	score    int
	inbound  bool
	persist  bool
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
	base := s.cfg.DialBackoff
	if base <= 0 {
		base = time.Second
	}
	limit := s.cfg.MaxDialBackoff
	if limit <= 0 {
		limit = maxDialBackoff
	}
	if delay == 0 {
		delay = base
	} else {
		delay *= 2
		if delay > limit {
			delay = limit
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

func (s *Server) hasPeer(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.peers[strings.TrimSpace(id)]
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
