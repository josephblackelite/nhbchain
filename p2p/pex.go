package p2p

import (
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	"nhbchain/observability/logging"
)

const (
	pexAddressTTL  = 60 * time.Minute
	pexResponseMax = 32

	// Everything below bounds what a remote peer can make this node hold or do.
	// pexBookMax caps the address book. Replies are only accepted for requests
	// this node sent: pexRequestTTL is how long such a request stays open and
	// pexMaxOutstanding how many may be open per peer. pexMaxTokenLen and
	// pexMaxAddressesPayload bound what a token and a reply may occupy, and
	// pexMaxAddrLen and pexMaxNodeIDLen what a single entry may. A peer may ask
	// for addresses pexRequestBurst times back to back and then once every
	// 1/pexRequestRate seconds. At most pexMaxProbes candidates are dialed at
	// the same time.
	pexBookMax             = 1024
	pexRequestTTL          = 5 * time.Minute
	pexMaxOutstanding      = 4
	pexMaxTokenLen         = 128
	pexMaxAddressesPayload = 64 << 10
	pexMaxAddrLen          = 255
	pexMaxNodeIDLen        = 128
	pexRequestRate         = 0.2
	pexRequestBurst        = 3
	pexMaxProbes           = 4
)

var errUnsolicitedPex = errors.New("unsolicited pex addresses")

type pexEntry struct {
	Addr     string
	NodeID   string
	LastSeen time.Time

	// verified entries belong to peers this node completed a handshake with (or
	// to configured seeds) and are the only ones handed out to other peers.
	// Entries learned from other peers stay unverified until the node itself
	// reaches them. Seeds are never evicted to make room. touched is the local
	// time the entry was added or refreshed and orders evictions.
	verified bool
	seed     bool
	touched  time.Time
}

type pexPeer interface {
	ID() string
	Enqueue(*Message) error
}

// pexOutstanding is a request this node sent and has not seen answered yet.
type pexOutstanding struct {
	token string
	sent  time.Time
}

// pexPeerState throttles how often one peer may ask for addresses.
type pexPeerState struct {
	bucket *tokenBucket
	seen   time.Time
}

type pexManager struct {
	server *Server

	ttl  time.Duration
	max  int
	mu   sync.Mutex
	book map[string]*pexEntry

	requests map[string][]pexOutstanding
	peers    map[string]*pexPeerState
	probes   chan struct{}

	logger *slog.Logger
}

func newPexManager(server *Server) *pexManager {
	logger := slog.Default().With(slog.String("component", "p2p_pex"))
	if server != nil {
		logger = server.log().With(slog.String("component", "p2p_pex"))
	}
	mgr := &pexManager{
		server:   server,
		ttl:      pexAddressTTL,
		max:      pexResponseMax,
		book:     make(map[string]*pexEntry),
		requests: make(map[string][]pexOutstanding),
		peers:    make(map[string]*pexPeerState),
		probes:   make(chan struct{}, pexMaxProbes),
		logger:   logger,
	}
	now := mgr.now()
	for _, seed := range server.seedSnapshot() {
		if seed.NodeID == "" || seed.Address == "" {
			continue
		}
		nodeID, addr := normalizeHex(seed.NodeID), strings.TrimSpace(seed.Address)
		mgr.book[nodeID] = &pexEntry{Addr: addr, NodeID: nodeID, LastSeen: now, verified: true, seed: true, touched: now}
	}
	return mgr
}

func (m *pexManager) now() time.Time {
	if m.server != nil && m.server.now != nil {
		return m.server.now()
	}
	return time.Now()
}

func (m *pexManager) log() *slog.Logger {
	if m == nil {
		return slog.Default().With(slog.String("component", "p2p_pex"))
	}
	if m.logger == nil {
		m.logger = slog.Default().With(slog.String("component", "p2p_pex"))
	}
	return m.logger
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
	for peerID, open := range m.requests {
		live := open[:0]
		for _, req := range open {
			if now.Sub(req.sent) <= pexRequestTTL {
				live = append(live, req)
			}
		}
		if len(live) == 0 {
			delete(m.requests, peerID)
			continue
		}
		m.requests[peerID] = live
	}
	for peerID, state := range m.peers {
		if now.Sub(state.seen) > ttl {
			delete(m.peers, peerID)
		}
	}
}

// makeRoomLocked evicts entries until one more fits in the book: entries that
// were only learned from other peers go first, then the least recently touched
// verified ones. Seeds are never evicted; it reports false when the book holds
// nothing but seeds.
func (m *pexManager) makeRoomLocked() bool {
	for len(m.book) >= pexBookMax {
		victim := ""
		var victimEntry *pexEntry
		for nodeID, entry := range m.book {
			if entry.seed {
				continue
			}
			better := victimEntry == nil
			if !better {
				switch {
				case entry.verified != victimEntry.verified:
					better = !entry.verified
				case !entry.touched.Equal(victimEntry.touched):
					better = entry.touched.Before(victimEntry.touched)
				default:
					better = nodeID < victim
				}
			}
			if better {
				victim, victimEntry = nodeID, entry
			}
		}
		if victimEntry == nil {
			return false
		}
		delete(m.book, victim)
	}
	return true
}

func (m *pexManager) generateToken() string {
	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", m.now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// validPexEndpoint reports whether an advertised node ID and address are
// well-formed and small enough to keep.
func validPexEndpoint(nodeID, addr string) bool {
	if nodeID == "" || addr == "" || len(nodeID) > pexMaxNodeIDLen || len(addr) > pexMaxAddrLen {
		return false
	}
	_, _, err := net.SplitHostPort(addr)
	return err == nil
}

// recordPeer notes a peer that this node has completed a handshake with.
func (m *pexManager) recordPeer(nodeID, addr string, seen time.Time) {
	nodeID = normalizeHex(nodeID)
	addr = strings.TrimSpace(addr)
	if !validPexEndpoint(nodeID, addr) {
		return
	}
	now := m.now()
	if seen.IsZero() || seen.After(now) {
		seen = now
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(now)
	entry := m.book[nodeID]
	if entry == nil {
		if !m.makeRoomLocked() {
			return
		}
		m.book[nodeID] = &pexEntry{Addr: addr, NodeID: nodeID, LastSeen: seen, verified: true, touched: now}
		return
	}
	if seen.After(entry.LastSeen) {
		entry.LastSeen = seen
	}
	if entry.Addr != addr {
		entry.Addr = addr
	}
	entry.verified = true
	entry.touched = now
}

// noteRequestSent remembers a request this node sent to a peer, so that the
// reply carrying the same token can be recognised.
func (m *pexManager) noteRequestSent(peerID, token string) {
	peerID = normalizeHex(peerID)
	token = strings.TrimSpace(token)
	if m == nil || peerID == "" || token == "" || len(token) > pexMaxTokenLen {
		return
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(now)
	open := append(m.requests[peerID], pexOutstanding{token: token, sent: now})
	if len(open) > pexMaxOutstanding {
		open = open[len(open)-pexMaxOutstanding:]
	}
	m.requests[peerID] = open
}

// expectingReply reports whether this node has a request open with the peer.
func (m *pexManager) expectingReply(peerID string) bool {
	if m == nil {
		return false
	}
	peerID = normalizeHex(peerID)
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, req := range m.requests[peerID] {
		if now.Sub(req.sent) <= pexRequestTTL {
			return true
		}
	}
	return false
}

// takeRequestLocked closes the open request with the given token, if any.
func (m *pexManager) takeRequestLocked(peerID, token string, now time.Time) bool {
	open := m.requests[peerID]
	for i, req := range open {
		if req.token != token || now.Sub(req.sent) > pexRequestTTL {
			continue
		}
		rest := append(append([]pexOutstanding(nil), open[:i]...), open[i+1:]...)
		if len(rest) == 0 {
			delete(m.requests, peerID)
		} else {
			m.requests[peerID] = rest
		}
		return true
	}
	return false
}

func (m *pexManager) handleRequest(peer pexPeer, req PexRequestPayload) error {
	if m == nil || peer == nil {
		return nil
	}
	token := strings.TrimSpace(req.Token)
	if len(token) > pexMaxTokenLen {
		return fmt.Errorf("pex token exceeds %d bytes", pexMaxTokenLen)
	}
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
	state := m.peers[peerID]
	if state == nil {
		state = &pexPeerState{bucket: newTokenBucket(pexRequestRate, pexRequestBurst)}
		m.peers[peerID] = state
	}
	state.seen = now
	if !state.bucket.allow(now) {
		// Asking again this soon is not answered.
		m.mu.Unlock()
		return nil
	}
	addrs := make([]PexAddress, 0, limit)
	for _, entry := range m.book {
		if entry == nil || !entry.verified {
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
	m.mu.Unlock()

	payload := PexAddressesPayload{Token: token, Addresses: addrs}
	msg, err := NewPexAddressesMessage(payload)
	if err != nil {
		return err
	}
	if err := peer.Enqueue(msg); err != nil && !errors.Is(err, errQueueFull) {
		return err
	}
	return nil
}

// handleAddresses takes in the reply to a request this node sent. A reply with
// no token, or with a token that does not belong to an open request to this
// peer, is dropped, and at most pexResponseMax entries of a reply are used.
// Nothing learned here is persisted: the addresses stay in the bounded book as
// unverified candidates, and only reach the peerstore once this node has itself
// completed a handshake with the peer (see initPeer).
func (m *pexManager) handleAddresses(peer pexPeer, payload PexAddressesPayload) {
	if m == nil || peer == nil {
		return
	}
	token := strings.TrimSpace(payload.Token)
	if token == "" || len(token) > pexMaxTokenLen {
		return
	}
	now := m.now()
	localID := ""
	if m.server != nil {
		localID = normalizeHex(m.server.nodeID)
	}
	peerID := normalizeHex(peer.ID())

	m.mu.Lock()
	m.pruneLocked(now)
	if !m.takeRequestLocked(peerID, token, now) {
		m.mu.Unlock()
		return
	}

	addresses := payload.Addresses
	if len(addresses) > pexResponseMax {
		addresses = addresses[:pexResponseMax]
	}
	var fresh []pexEntry
	for _, addr := range addresses {
		nodeID := normalizeHex(addr.NodeID)
		endpoint := strings.TrimSpace(addr.Addr)
		if !validPexEndpoint(nodeID, endpoint) {
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
		if lastSeen.IsZero() || lastSeen.After(now) {
			lastSeen = now
		}
		if now.Sub(lastSeen) > m.ttl {
			continue
		}
		entry := m.book[nodeID]
		if entry == nil {
			if !m.makeRoomLocked() {
				continue
			}
			entry = &pexEntry{NodeID: nodeID, Addr: endpoint, LastSeen: lastSeen, touched: now}
			m.book[nodeID] = entry
			fresh = append(fresh, *entry)
			continue
		}
		if entry.verified || entry.seed {
			// What another peer says never overrides what this node has seen itself.
			continue
		}
		if lastSeen.After(entry.LastSeen) {
			entry.LastSeen = lastSeen
		}
		entry.Addr = endpoint
		entry.touched = now
	}
	m.mu.Unlock()

	m.probeCandidates(fresh)
}

func (m *pexManager) forgetPeer(id string) {
	if m == nil || id == "" {
		return
	}
	id = normalizeHex(id)
	m.mu.Lock()
	delete(m.requests, id)
	delete(m.peers, id)
	m.mu.Unlock()
}

// probeCandidates dials newly learned, unverified addresses once each, a few at
// a time. A candidate is only ever stored by initPeer, after the handshake and
// registerPeer have both succeeded; one that cannot be reached is forgotten.
func (m *pexManager) probeCandidates(entries []pexEntry) {
	if m == nil || m.server == nil || m.server.dialFn == nil {
		return
	}
	for _, entry := range entries {
		select {
		case m.probes <- struct{}{}:
		default:
			return
		}
		go func(entry pexEntry) {
			defer func() { <-m.probes }()
			m.probe(entry)
		}(entry)
	}
}

func (m *pexManager) probe(entry pexEntry) {
	s := m.server
	if s.hasPeer(entry.NodeID) || s.isConnectedToAddress(entry.Addr) || s.isBanned(entry.NodeID) {
		return
	}
	s.mu.RLock()
	full := len(s.peers) >= s.cfg.MaxPeers || s.outboundCount >= s.cfg.MaxOutbound
	s.mu.RUnlock()
	if full {
		return
	}
	if err := s.Connect(entry.Addr); err != nil {
		m.mu.Lock()
		if current := m.book[entry.NodeID]; current != nil && !current.verified {
			delete(m.book, entry.NodeID)
		}
		m.mu.Unlock()
	}
}

type seedEndpoint struct {
	NodeID  string
	Address string
}

func parseSeedList(values []string, logger *slog.Logger) []seedEndpoint {
	if logger == nil {
		logger = slog.Default().With(slog.String("component", "p2p_pex"))
	}
	seeds := make([]seedEndpoint, 0, len(values))
	seen := make(map[string]struct{})
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		nodePart, addrPart, found := strings.Cut(trimmed, "@")
		if !found {
			logger.Warn("Ignoring seed: missing node identifier",
				logging.MaskField("seed", trimmed))
			continue
		}
		node := normalizeHex(nodePart)
		if node == "" {
			logger.Warn("Ignoring seed: empty node identifier",
				logging.MaskField("seed", trimmed))
			continue
		}
		addr := strings.TrimSpace(addrPart)
		if _, _, err := net.SplitHostPort(addr); err != nil {
			logger.Warn("Ignoring seed: invalid address",
				logging.MaskField("seed", trimmed),
				logging.MaskField("seed_address", addr),
				slog.Any("error", err))
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
