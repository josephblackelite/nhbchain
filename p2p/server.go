package p2p

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"nhbchain/crypto"
)

const (
	handshakeTimeout   = 5 * time.Second
	outboundQueueSize  = 64
	handshakeNonceSize = 32

	defaultMaxPeers       = 64
	defaultPeerBan        = 15 * time.Minute
	defaultReadTimeout    = 90 * time.Second
	defaultWriteTimeout   = 5 * time.Second
	defaultMaxMessageSize = 1 << 20
	defaultMsgRate        = 32.0
	maxDialBackoff        = time.Minute

	malformedPenalty       = 2
	validMessageReward     = 1
	reputationBanThreshold = -6
	slowPenalty            = 1
	ratePenalty            = 3

	invalidRateWindow        = time.Minute
	invalidRateThresholdPerc = 50
	invalidRateSampleSize    = 5
)

var errQueueFull = errors.New("peer outbound queue full")

// ServerConfig encapsulates runtime settings for the p2p server.
type ServerConfig struct {
	ListenAddress    string
	ChainID          uint64
	GenesisHash      []byte
	ClientVersion    string
	MaxPeers         int
	MaxInbound       int
	MaxOutbound      int
	Bootnodes        []string
	PersistentPeers  []string
	PeerBanDuration  time.Duration
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	MaxMessageBytes  int
	MaxMsgsPerSecond float64
}

type dialFunc func(context.Context, string) (net.Conn, error)

// Server coordinates peer connections and message dissemination.
type Server struct {
	cfg     ServerConfig
	handler MessageHandler
	privKey *crypto.PrivateKey
	nodeID  string
	genesis []byte

	mu            sync.RWMutex
	peers         map[string]*Peer
	inboundCount  int
	outboundCount int
	banned        map[string]time.Time
	scores        map[string]int
	metrics       map[string]*peerMetrics
	byAddr        map[string]string

	dialFn      dialFunc
	now         func() time.Time
	globalLimit *tokenBucket

	dialMu      sync.Mutex
	pendingDial map[string]struct{}
	backoff     map[string]time.Duration
	persistent  map[string]struct{}
}

// peerMetrics tracks message quality for a peer.
type peerMetrics struct {
	windowStart time.Time
	total       int
	invalid     int
}

// NewServer creates a P2P server with authenticated handshakes.
func NewServer(handler MessageHandler, privKey *crypto.PrivateKey, cfg ServerConfig) *Server {
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = ":0"
	}
	if cfg.ClientVersion == "" {
		cfg.ClientVersion = "nhbchain/node"
	}
	if cfg.MaxPeers <= 0 {
		cfg.MaxPeers = defaultMaxPeers
	}
	if cfg.MaxInbound <= 0 || cfg.MaxInbound > cfg.MaxPeers {
		cfg.MaxInbound = cfg.MaxPeers
	}
	if cfg.MaxOutbound <= 0 || cfg.MaxOutbound > cfg.MaxPeers {
		cfg.MaxOutbound = cfg.MaxPeers
	}
	if cfg.PeerBanDuration <= 0 {
		cfg.PeerBanDuration = defaultPeerBan
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = defaultReadTimeout
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = defaultWriteTimeout
	}
	if cfg.MaxMessageBytes <= 0 {
		cfg.MaxMessageBytes = defaultMaxMessageSize
	}
	if cfg.MaxMsgsPerSecond <= 0 {
		cfg.MaxMsgsPerSecond = defaultMsgRate
	}

	uniqBoot := uniqueStrings(cfg.Bootnodes)
	uniqPersist := uniqueStrings(cfg.PersistentPeers)
	cfg.Bootnodes = append([]string{}, uniqBoot...)
	cfg.PersistentPeers = append([]string{}, uniqPersist...)

	nodeID := privKey.PubKey().Address().String()

	server := &Server{
		cfg:         cfg,
		handler:     handler,
		privKey:     privKey,
		nodeID:      nodeID,
		genesis:     cloneBytes(cfg.GenesisHash),
		peers:       make(map[string]*Peer),
		banned:      make(map[string]time.Time),
		scores:      make(map[string]int),
		metrics:     make(map[string]*peerMetrics),
		byAddr:      make(map[string]string),
		dialFn:      defaultDialer,
		now:         time.Now,
		backoff:     make(map[string]time.Duration),
		pendingDial: make(map[string]struct{}),
		persistent:  make(map[string]struct{}),
	}

	for _, addr := range uniqBoot {
		server.persistent[strings.TrimSpace(addr)] = struct{}{}
	}
	for _, addr := range uniqPersist {
		server.persistent[strings.TrimSpace(addr)] = struct{}{}
	}

	burst := cfg.MaxMsgsPerSecond * float64(cfg.MaxPeers)
	if burst < cfg.MaxMsgsPerSecond {
		burst = cfg.MaxMsgsPerSecond
	}
	server.globalLimit = newTokenBucket(cfg.MaxMsgsPerSecond*float64(cfg.MaxPeers), burst)

	return server
}

func defaultDialer(ctx context.Context, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: handshakeTimeout}
	return d.DialContext(ctx, "tcp", addr)
}

// Start begins listening for inbound peers and negotiating handshakes.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.ListenAddress)
	if err != nil {
		return err
	}
	fmt.Printf("NHB P2P listening on %s | chain=%d | genesis=%s | node=%s | client=%s\n",
		ln.Addr().String(), s.cfg.ChainID, summarizeHash(s.genesis), s.nodeID, s.cfg.ClientVersion)

	go s.startDialers()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				continue
			}
			return err
		}
		go s.handleInbound(conn)
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

func (s *Server) handleInbound(conn net.Conn) {
	if err := s.initPeer(conn, true, false, ""); err != nil {
		fmt.Printf("Inbound connection from %s rejected: %v\n", conn.RemoteAddr(), err)
		conn.Close()
	}
}

func (s *Server) initPeer(conn net.Conn, inbound bool, persistent bool, dialAddr string) error {
	reader := bufio.NewReader(conn)
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()

	remote, err := s.performHandshake(ctx, conn, reader)
	if err != nil {
		return err
	}
	if remote.NodeID == s.nodeID {
		return fmt.Errorf("self connection not allowed")
	}
	if s.isBanned(remote.NodeID) {
		return fmt.Errorf("peer %s is currently banned", remote.NodeID)
	}

	peer := newPeer(remote.NodeID, conn, reader, s, inbound, persistent, dialAddr)
	if err := s.registerPeer(peer); err != nil {
		return err
	}
	fmt.Printf("New peer connected: %s (%s) client=%s\n", peer.id, peer.remoteAddr, remote.ClientVersion)
	peer.start()
	return nil
}

func (s *Server) registerPeer(peer *Peer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.peers[peer.id]; exists {
		return fmt.Errorf("peer %s already connected", peer.id)
	}
	if expiry, banned := s.banned[peer.id]; banned {
		if s.now().After(expiry) {
			delete(s.banned, peer.id)
			delete(s.scores, peer.id)
		} else {
			return fmt.Errorf("peer %s banned until %s", peer.id, expiry.Format(time.RFC3339))
		}
	}
	if len(s.peers) >= s.cfg.MaxPeers {
		return fmt.Errorf("maximum peers reached")
	}
	if peer.inbound {
		if s.inboundCount >= s.cfg.MaxInbound {
			return fmt.Errorf("maximum inbound peers reached")
		}
		s.inboundCount++
	} else {
		if s.outboundCount >= s.cfg.MaxOutbound {
			return fmt.Errorf("maximum outbound peers reached")
		}
		s.outboundCount++
	}
	s.peers[peer.id] = peer
	if peer.dialAddr != "" {
		s.byAddr[peer.dialAddr] = peer.id
	}
	return nil
}

func (s *Server) removePeer(peer *Peer, ban bool, reason error) {
	s.mu.Lock()
	if current, ok := s.peers[peer.id]; ok && current == peer {
		delete(s.peers, peer.id)
		if peer.inbound {
			if s.inboundCount > 0 {
				s.inboundCount--
			}
		} else {
			if s.outboundCount > 0 {
				s.outboundCount--
			}
		}
		if peer.dialAddr != "" {
			delete(s.byAddr, peer.dialAddr)
		}
	}
	s.mu.Unlock()

	if ban {
		s.banPeer(peer.id)
		fmt.Printf("Peer %s disconnected and banned: %v\n", peer.id, reason)
	} else if reason != nil {
		fmt.Printf("Peer %s disconnected: %v\n", peer.id, reason)
	} else {
		fmt.Printf("Peer %s disconnected\n", peer.id)
	}

	if peer.persistent && !peer.inbound {
		s.scheduleReconnect(peer.dialAddr)
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

// Connect dials a remote peer and establishes a secure session.
func (s *Server) Connect(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("empty address")
	}
	if s.isConnectedToAddress(addr) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()

	conn, err := s.dialFn(ctx, addr)
	if err != nil {
		return err
	}

	persistent := s.isPersistent(addr)
	if err := s.initPeer(conn, false, persistent, addr); err != nil {
		conn.Close()
		return fmt.Errorf("handshake with %s failed: %w", addr, err)
	}
	fmt.Printf("Connected to peer: %s\n", addr)
	s.resetBackoff(addr)
	return nil
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

// Broadcast sends a message to all connected peers with backpressure.
func (s *Server) Broadcast(msg *Message) error {
	s.mu.RLock()
	peers := make([]*Peer, 0, len(s.peers))
	for _, peer := range s.peers {
		peers = append(peers, peer)
	}
	s.mu.RUnlock()

	var errs []error
	for _, peer := range peers {
		if err := peer.Enqueue(msg); err != nil {
			errs = append(errs, fmt.Errorf("peer %s: %w", peer.id, err))
			if errors.Is(err, errQueueFull) {
				fmt.Printf("Peer %s send queue full, disconnecting\n", peer.id)
				peer.server.adjustScore(peer.id, -slowPenalty)
			}
			peer.terminate(false, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Server) allowGlobal(now time.Time) bool {
	return s.globalLimit == nil || s.globalLimit.allow(now)
}

func (s *Server) handleRateLimit(peer *Peer, global bool) {
	if global {
		fmt.Printf("Dropping message from %s due to global rate cap\n", peer.id)
		peer.terminate(false, fmt.Errorf("global rate cap exceeded"))
		return
	}
	rep := s.adjustScore(peer.id, -ratePenalty)
	fmt.Printf("Peer %s exceeded rate limit (score %d)\n", peer.id, rep)
	ban := rep <= reputationBanThreshold
	peer.terminate(ban, fmt.Errorf("peer rate limit exceeded"))
}

func (s *Server) recordValidMessage(id string) {
	s.updatePeerMetrics(id, true)
	if validMessageReward != 0 {
		s.adjustScore(id, validMessageReward)
	}
}

func (s *Server) handleProtocolViolation(peer *Peer, err error) {
	if s.updatePeerMetrics(peer.id, false) {
		fmt.Printf("Protocol violation from %s: %v (banned: invalid rate)\n", peer.id, err)
		peer.terminate(true, fmt.Errorf("invalid message rate: %w", err))
		return
	}

	rep := s.adjustScore(peer.id, -malformedPenalty)
	fmt.Printf("Protocol violation from %s: %v (score %d)\n", peer.id, err, rep)
	ban := rep <= reputationBanThreshold
	peer.terminate(ban, err)
}

func (s *Server) adjustScore(id string, delta int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	score := s.scores[id] + delta
	s.scores[id] = score
	return score
}

func (s *Server) updatePeerMetrics(id string, valid bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	metrics := s.metrics[id]
	now := s.now()
	if metrics == nil {
		metrics = &peerMetrics{windowStart: now}
		s.metrics[id] = metrics
	}

	if now.Sub(metrics.windowStart) > invalidRateWindow {
		metrics.windowStart = now
		metrics.total = 0
		metrics.invalid = 0
	}

	metrics.total++
	if !valid {
		metrics.invalid++
		if metrics.total >= invalidRateSampleSize && metrics.invalid*100 >= invalidRateThresholdPerc*metrics.total {
			metrics.windowStart = now
			metrics.total = 0
			metrics.invalid = 0
			return true
		}
	}

	return false
}

func (s *Server) isBanned(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.banned[id]
	if !ok {
		return false
	}
	if s.now().After(expiry) {
		delete(s.banned, id)
		delete(s.scores, id)
		return false
	}
	return true
}

func (s *Server) banPeer(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.banned[id] = s.now().Add(s.cfg.PeerBanDuration)
	s.scores[id] = reputationBanThreshold
	delete(s.metrics, id)
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{})
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func summarizeHash(input []byte) string {
	if len(input) == 0 {
		return ""
	}
	if len(input) <= 8 {
		return fmt.Sprintf("%x", input)
	}
	return fmt.Sprintf("%x…%x", input[:4], input[len(input)-4:])
}

func cloneBytes(input []byte) []byte {
	if input == nil {
		return nil
	}
	cp := make([]byte, len(input))
	copy(cp, input)
	return cp
}
