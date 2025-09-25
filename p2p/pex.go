package p2p

import (
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	pexAddressTTL  = 60 * time.Minute
	pexResponseMax = 32
)

type pexEntry struct {
	Addr     string
	NodeID   string
	LastSeen time.Time
}

type sentToken struct {
	token string
	seen  time.Time
}

type pexPeer interface {
	ID() string
	Enqueue(*Message) error
}

type pexManager struct {
	server *Server

	ttl  time.Duration
	max  int
	mu   sync.Mutex
	book map[string]*pexEntry

	seenTokens map[string]time.Time
	sentTokens map[string]sentToken
}

func newPexManager(server *Server) *pexManager {
	mgr := &pexManager{
		server:     server,
		ttl:        pexAddressTTL,
		max:        pexResponseMax,
		book:       make(map[string]*pexEntry),
		seenTokens: make(map[string]time.Time),
		sentTokens: make(map[string]sentToken),
	}
	now := mgr.now()
	for _, seed := range server.seeds {
		if seed.NodeID == "" || seed.Address == "" {
			continue
		}
		mgr.book[normalizeHex(seed.NodeID)] = &pexEntry{Addr: strings.TrimSpace(seed.Address), NodeID: normalizeHex(seed.NodeID), LastSeen: now}
	}
	return mgr
}

func (m *pexManager) now() time.Time {
	if m.server != nil && m.server.now != nil {
		return m.server.now()
	}
	return time.Now()
}

func (m *pexManager) pruneLocked(now time.Time) {
	ttl := m.ttl
	if ttl <= 0 {
		return
	}
	for nodeID, entry := range m.book {
		if now.Sub(entry.LastSeen) > ttl {
			delete(m.book, nodeID)
		}
	}
	for token, ts := range m.seenTokens {
		if now.Sub(ts) > ttl {
			delete(m.seenTokens, token)
		}
	}
	for peerID, sent := range m.sentTokens {
		if now.Sub(sent.seen) > ttl {
			delete(m.sentTokens, peerID)
		}
	}
}

func (m *pexManager) generateToken() string {
	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", m.now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

func (m *pexManager) recordPeer(nodeID, addr string, seen time.Time) {
	nodeID = normalizeHex(nodeID)
	addr = strings.TrimSpace(addr)
	if nodeID == "" || addr == "" {
		return
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return
	}
	now := m.now()
	if seen.IsZero() {
		seen = now
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(now)
	entry := m.book[nodeID]
	if entry == nil {
		m.book[nodeID] = &pexEntry{Addr: addr, NodeID: nodeID, LastSeen: seen}
		return
	}
	if seen.After(entry.LastSeen) {
		entry.LastSeen = seen
	}
	if entry.Addr != addr {
		entry.Addr = addr
	}
}

func (m *pexManager) handleRequest(peer pexPeer, req PexRequestPayload) error {
	if m == nil || peer == nil {
		return nil
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		token = m.generateToken()
	}
	now := m.now()
	limit := req.Limit
	if limit <= 0 || limit > m.max {
		limit = m.max
	}
	localID := ""
	if m.server != nil {
		localID = normalizeHex(m.server.nodeID)
	}
	peerID := normalizeHex(peer.ID())

	m.mu.Lock()
	m.pruneLocked(now)
	addrs := make([]PexAddress, 0, limit)
	for _, entry := range m.book {
		if entry == nil {
			continue
		}
		if entry.NodeID == "" || entry.Addr == "" {
			continue
		}
		if entry.NodeID == peerID || (localID != "" && entry.NodeID == localID) {
			continue
		}
		if m.server != nil && m.server.isBanned(entry.NodeID) {
			continue
		}
		if now.Sub(entry.LastSeen) > m.ttl {
			continue
		}
		addrs = append(addrs, PexAddress{Addr: entry.Addr, NodeID: entry.NodeID, LastSeen: entry.LastSeen})
	}
	if len(addrs) > 1 {
		rand.Shuffle(len(addrs), func(i, j int) {
			addrs[i], addrs[j] = addrs[j], addrs[i]
		})
	}
	if len(addrs) > limit {
		addrs = addrs[:limit]
	}
	m.seenTokens[token] = now
	m.sentTokens[peer.ID()] = sentToken{token: token, seen: now}
	m.mu.Unlock()

	payload := PexAddressesPayload{Token: token, Addresses: addrs}
	msg, err := NewPexAddressesMessage(payload)
	if err != nil {
		return err
	}
	return peer.Enqueue(msg)
}

func (m *pexManager) handleAddresses(peer pexPeer, payload PexAddressesPayload) {
	if m == nil || peer == nil {
		return
	}
	now := m.now()
	token := strings.TrimSpace(payload.Token)
	localID := ""
	if m.server != nil {
		localID = normalizeHex(m.server.nodeID)
	}
	peerID := normalizeHex(peer.ID())

	m.mu.Lock()
	m.pruneLocked(now)
	if token != "" {
		if sent, ok := m.sentTokens[peer.ID()]; ok && sent.token == token {
			delete(m.sentTokens, peer.ID())
			m.mu.Unlock()
			return
		}
		if seen, ok := m.seenTokens[token]; ok && now.Sub(seen) <= m.ttl {
			m.mu.Unlock()
			return
		}
		m.seenTokens[token] = now
	}

	newEntries := make([]pexEntry, 0, len(payload.Addresses))
	for _, addr := range payload.Addresses {
		nodeID := normalizeHex(addr.NodeID)
		endpoint := strings.TrimSpace(addr.Addr)
		if nodeID == "" || endpoint == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(endpoint); err != nil {
			continue
		}
		if localID != "" && nodeID == localID {
			continue
		}
		if m.server != nil && m.server.isBanned(nodeID) {
			continue
		}
		if nodeID == peerID {
			continue
		}
		lastSeen := addr.LastSeen
		if lastSeen.IsZero() {
			lastSeen = now
		}
		if now.Sub(lastSeen) > m.ttl {
			continue
		}
		entry := m.book[nodeID]
		if entry == nil {
			entry = &pexEntry{NodeID: nodeID, Addr: endpoint, LastSeen: lastSeen}
			m.book[nodeID] = entry
			newEntries = append(newEntries, *entry)
			continue
		}
		updated := false
		if lastSeen.After(entry.LastSeen) {
			entry.LastSeen = lastSeen
			updated = true
		}
		if entry.Addr != endpoint {
			entry.Addr = endpoint
			updated = true
		}
		if updated {
			newEntries = append(newEntries, *entry)
		}
	}
	m.mu.Unlock()

	if len(newEntries) == 0 {
		return
	}
	for _, entry := range newEntries {
		m.persist(entry)
	}
}

func (m *pexManager) forgetPeer(id string) {
	if m == nil || id == "" {
		return
	}
	m.mu.Lock()
	delete(m.sentTokens, id)
	m.mu.Unlock()
}

func (m *pexManager) persist(entry pexEntry) {
	if m.server == nil || m.server.peerstore == nil {
		return
	}
	rec := PeerstoreEntry{Addr: entry.Addr, NodeID: entry.NodeID, LastSeen: entry.LastSeen}
	if err := m.server.peerstore.Put(rec); err != nil {
		fmt.Printf("persist learned peer %s: %v\n", entry.NodeID, err)
	}
}

type seedEndpoint struct {
	NodeID  string
	Address string
}

func parseSeedList(values []string) []seedEndpoint {
	seeds := make([]seedEndpoint, 0, len(values))
	seen := make(map[string]struct{})
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		nodePart, addrPart, found := strings.Cut(trimmed, "@")
		if !found {
			fmt.Printf("Ignoring seed %q: missing node ID\n", trimmed)
			continue
		}
		node := normalizeHex(nodePart)
		if node == "" {
			fmt.Printf("Ignoring seed %q: empty node ID\n", trimmed)
			continue
		}
		addr := strings.TrimSpace(addrPart)
		if _, _, err := net.SplitHostPort(addr); err != nil {
			fmt.Printf("Ignoring seed %q: invalid address: %v\n", trimmed, err)
			continue
		}
		key := node + "@" + addr
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		seeds = append(seeds, seedEndpoint{NodeID: node, Address: addr})
	}
	return seeds
}
