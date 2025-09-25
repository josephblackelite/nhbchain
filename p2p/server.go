package p2p

import (
	"bufio"
	"context"
	"encoding/hex"
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
	defaultHandshakeTimeout = 5 * time.Second
	outboundQueueSize       = 64

	defaultMaxPeers       = 64
	defaultPeerBan        = 15 * time.Minute
	defaultReadTimeout    = 90 * time.Second
	defaultWriteTimeout   = 5 * time.Second
	defaultMaxMessageSize = 1 << 20
	defaultMsgRate        = 32.0
	defaultBurstRate      = 200.0
	defaultPingInterval   = 30 * time.Second
	defaultPingTimeout    = 2 * time.Minute
	maxDialBackoff        = time.Minute

	malformedPenalty   = -malformedMessagePenaltyDelta
	validMessageReward = heartbeatRewardDelta
	slowPenalty        = 5
	ratePenalty        = -spamPenaltyDelta

	greylistRateMultiplier = 0.25

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
	RateMsgsPerSec   float64
	RateBurst        float64
	BanScore         int
	GreyScore        int
	HandshakeTimeout time.Duration
	PingInterval     time.Duration
	PingTimeout      time.Duration
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
	metrics       map[string]*peerMetrics
	byAddr        map[string]string
	persistentIDs map[string]struct{}
	records       map[string]*PeerRecord

	dialFn           dialFunc
	now              func() time.Time
	globalLimit      *tokenBucket
	ipLimiter        *ipRateLimiter
	reputation       *ReputationManager
	ratePerPeer      float64
	rateBurst        float64
	handshakeTimeout time.Duration
	pingTimeout      time.Duration
	nonceGuard       *nonceGuard

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

// PeerRecord tracks persistent metadata about a peer.
type PeerRecord struct {
	NodeID    string
	FirstSeen time.Time
	LastSeen  time.Time
	Version   string
	Score     int
}

// PeerInfo captures the public status of a connected peer.
type PeerInfo struct {
	NodeID     string    `json:"nodeId"`
	Direction  string    `json:"dir"`
	Persistent bool      `json:"persistent"`
	RemoteAddr string    `json:"remoteAddr"`
	DialAddr   string    `json:"dialAddr,omitempty"`
	Version    string    `json:"version"`
	Score      int       `json:"score"`
	Greylisted bool      `json:"greylisted"`
	Banned     bool      `json:"banned"`
	FirstSeen  time.Time `json:"firstSeen"`
	LastSeen   time.Time `json:"lastSeen"`
}

// NetworkCounts represents current peer counts.
type NetworkCounts struct {
	Total    int `json:"total"`
	Inbound  int `json:"inbound"`
	Outbound int `json:"outbound"`
}

// NetworkLimits captures configured quotas.
type NetworkLimits struct {
	MaxPeers    int     `json:"maxPeers"`
	MaxInbound  int     `json:"maxInbound"`
	MaxOutbound int     `json:"maxOutbound"`
	Rate        float64 `json:"rateMsgsPerSec"`
	Burst       float64 `json:"burst"`
	BanScore    int     `json:"banScore"`
	GreyScore   int     `json:"greyScore"`
}

// NetworkSelf describes the local node identity.
type NetworkSelf struct {
	NodeID          string `json:"nodeId"`
	ProtocolVersion uint32 `json:"protocolVersion"`
	ClientVersion   string `json:"clientVersion"`
}

// NetworkView summarizes the current P2P server status.
type NetworkView struct {
	NetworkID  uint64        `json:"networkId"`
	Genesis    string        `json:"genesisHash"`
	Counts     NetworkCounts `json:"counts"`
	Limits     NetworkLimits `json:"limits"`
	Self       NetworkSelf   `json:"self"`
	Bootnodes  []string      `json:"bootnodes"`
	Persistent []string      `json:"persistentPeers"`
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
	if cfg.RateMsgsPerSec <= 0 {
		cfg.RateMsgsPerSec = defaultMsgRate
	}
	if cfg.RateBurst <= 0 {
		cfg.RateBurst = defaultBurstRate
	}
	if cfg.BanScore <= 0 {
		cfg.BanScore = 100
	}
	if cfg.GreyScore <= 0 || cfg.GreyScore >= cfg.BanScore {
		cfg.GreyScore = 50
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = defaultHandshakeTimeout
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = defaultPingInterval
	}
	if cfg.PingTimeout <= 0 {
		cfg.PingTimeout = defaultPingTimeout
	}

	uniqBoot := uniqueStrings(cfg.Bootnodes)
	uniqPersist := uniqueStrings(cfg.PersistentPeers)
	cfg.Bootnodes = append([]string{}, uniqBoot...)
	cfg.PersistentPeers = append([]string{}, uniqPersist...)

	nodeID := deriveNodeID(privKey)

	rep := NewReputationManager(ReputationConfig{
		GreyScore:        cfg.GreyScore,
		BanScore:         cfg.BanScore,
		BanDuration:      cfg.PeerBanDuration,
		GreylistDuration: time.Minute,
	})

	server := &Server{
		cfg:              cfg,
		handler:          handler,
		privKey:          privKey,
		nodeID:           nodeID,
		genesis:          cloneBytes(cfg.GenesisHash),
		peers:            make(map[string]*Peer),
		metrics:          make(map[string]*peerMetrics),
		byAddr:           make(map[string]string),
		persistentIDs:    make(map[string]struct{}),
		records:          make(map[string]*PeerRecord),
		dialFn:           defaultDialer,
		now:              time.Now,
		backoff:          make(map[string]time.Duration),
		pendingDial:      make(map[string]struct{}),
		persistent:       make(map[string]struct{}),
		reputation:       rep,
		ratePerPeer:      cfg.RateMsgsPerSec,
		rateBurst:        cfg.RateBurst,
		handshakeTimeout: cfg.HandshakeTimeout,
		pingTimeout:      cfg.PingTimeout,
		nonceGuard:       newNonceGuard(handshakeReplayWindow),
	}

	for _, addr := range uniqBoot {
		server.persistent[strings.TrimSpace(addr)] = struct{}{}
	}
	for _, addr := range uniqPersist {
		server.persistent[strings.TrimSpace(addr)] = struct{}{}
	}

	burst := cfg.RateBurst * float64(cfg.MaxPeers)
	if burst < cfg.RateMsgsPerSec {
		burst = cfg.RateMsgsPerSec
	}
	server.globalLimit = newTokenBucket(cfg.RateMsgsPerSec*float64(cfg.MaxPeers), burst)
	server.ipLimiter = newIPRateLimiter(cfg.RateMsgsPerSec, cfg.RateBurst)

	return server
}

func defaultDialer(ctx context.Context, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: defaultHandshakeTimeout}
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

func (s *Server) handleInbound(conn net.Conn) {
	if err := s.initPeer(conn, true, false, ""); err != nil {
		fmt.Printf("Inbound connection from %s rejected: %v\n", conn.RemoteAddr(), err)
		conn.Close()
	}
}

func (s *Server) initPeer(conn net.Conn, inbound bool, persistent bool, dialAddr string) error {
	reader := bufio.NewReader(conn)
	ctx, cancel := context.WithTimeout(context.Background(), s.handshakeTimeout)
	defer cancel()

	remote, err := s.performHandshake(ctx, conn, reader)
	if err != nil {
		return err
	}
	if remote.nodeID == "" {
		return fmt.Errorf("handshake missing node identity")
	}
	if remote.nodeID == s.nodeID {
		return fmt.Errorf("self connection not allowed")
	}
	if s.isBanned(remote.nodeID) {
		return fmt.Errorf("peer %s is currently banned", remote.nodeID)
	}

	s.recordPeerHandshake(remote)

	peer := newPeer(remote.nodeID, remote.ClientVersion, conn, reader, s, inbound, persistent, dialAddr)
	if err := s.registerPeer(peer); err != nil {
		return err
	}
	fmt.Printf("New peer connected: %s (%s) client=%s\n", peer.id, peer.remoteAddr, remote.ClientVersion)
	peer.start()
	return nil
}

func (s *Server) recordPeerHandshake(remote *handshakePacket) {
	if remote == nil {
		return
	}
	seen := s.now()
	score := 0
	if s.reputation != nil {
		score = s.reputation.Score(remote.nodeID, seen)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.records[remote.nodeID]
	if rec == nil {
		rec = &PeerRecord{NodeID: remote.nodeID, FirstSeen: seen}
		s.records[remote.nodeID] = rec
	}
	if rec.FirstSeen.IsZero() {
		rec.FirstSeen = seen
	}
	rec.Version = remote.ClientVersion
	rec.LastSeen = seen
	rec.Score = score
}

func (s *Server) touchPeer(id string) {
	if id == "" {
		return
	}
	seen := s.now()
	s.mu.Lock()
	if rec, ok := s.records[id]; ok {
		if rec.FirstSeen.IsZero() {
			rec.FirstSeen = seen
		}
		rec.LastSeen = seen
	}
	s.mu.Unlock()
}

func (s *Server) updatePeerRecordScore(id string, score int) {
	if id == "" {
		return
	}
	s.mu.Lock()
	if rec, ok := s.records[id]; ok {
		rec.Score = score
	}
	s.mu.Unlock()
}

func (s *Server) registerPeer(peer *Peer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.peers[peer.id]; exists {
		return fmt.Errorf("peer %s already connected", peer.id)
	}
	if banned, until := s.reputation.BanInfo(peer.id, s.now()); banned {
		return fmt.Errorf("peer %s banned until %s", peer.id, until.Format(time.RFC3339))
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
	if peer.persistent {
		s.persistentIDs[peer.id] = struct{}{}
	}
	return nil
}

func (s *Server) removePeer(peer *Peer, ban bool, reason error) {
	seen := s.now()
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
		if peer.persistent {
			delete(s.persistentIDs, peer.id)
		}
		if rec, ok := s.records[peer.id]; ok {
			if rec.FirstSeen.IsZero() {
				rec.FirstSeen = seen
			}
			rec.LastSeen = seen
		}
	}
	s.mu.Unlock()

	if ban {
		s.applyBan(peer.id, peer.persistent)
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

// Connect dials a remote peer and establishes a secure session.
func (s *Server) Connect(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("empty address")
	}
	if s.isConnectedToAddress(addr) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.handshakeTimeout)
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

func (s *Server) allowIP(remote string, now time.Time) bool {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	return s.ipLimiter == nil || s.ipLimiter.allow(host, now)
}

func (s *Server) updatePeerGreylist(id string, grey bool) {
	s.mu.RLock()
	peer := s.peers[id]
	s.mu.RUnlock()
	if peer != nil {
		peer.setGreylisted(grey)
	}
}

// SnapshotPeers returns the current connected peers with reputation data.
func (s *Server) SnapshotPeers() []PeerInfo {
	now := s.now()
	statuses := make(map[string]ReputationStatus)
	if s.reputation != nil {
		statuses = s.reputation.Snapshot(now)
	}

	s.mu.RLock()
	peers := make([]*Peer, 0, len(s.peers))
	for _, peer := range s.peers {
		peers = append(peers, peer)
	}
	records := make(map[string]PeerRecord, len(s.records))
	for id, rec := range s.records {
		records[id] = *rec
	}
	s.mu.RUnlock()

	results := make([]PeerInfo, 0, len(peers))
	for _, peer := range peers {
		status := statuses[peer.id]
		s.updatePeerRecordScore(peer.id, status.Score)
		rec := records[peer.id]
		version := peer.clientVersion
		firstSeen := now
		lastSeen := now
		if rec.NodeID != "" {
			if !rec.FirstSeen.IsZero() {
				firstSeen = rec.FirstSeen
			}
			if !rec.LastSeen.IsZero() {
				lastSeen = rec.LastSeen
			}
			if rec.Version != "" {
				version = rec.Version
			}
		}
		info := PeerInfo{
			NodeID:     peer.id,
			Direction:  directionForPeer(peer),
			Persistent: peer.persistent,
			RemoteAddr: peer.remoteAddr,
			DialAddr:   peer.dialAddr,
			Version:    version,
			Score:      status.Score,
			Greylisted: status.Greylisted,
			Banned:     status.Banned,
			FirstSeen:  firstSeen,
			LastSeen:   lastSeen,
		}
		results = append(results, info)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].NodeID < results[j].NodeID })
	return results
}

func (s *Server) SnapshotNetwork() NetworkView {
	s.mu.RLock()
	peerCount := len(s.peers)
	inbound := s.inboundCount
	outbound := s.outboundCount
	s.mu.RUnlock()

	view := NetworkView{
		NetworkID: s.cfg.ChainID,
		Genesis:   hex.EncodeToString(s.genesis),
		Counts: NetworkCounts{
			Total:    peerCount,
			Inbound:  inbound,
			Outbound: outbound,
		},
		Limits: NetworkLimits{
			MaxPeers:    s.cfg.MaxPeers,
			MaxInbound:  s.cfg.MaxInbound,
			MaxOutbound: s.cfg.MaxOutbound,
			Rate:        s.ratePerPeer,
			Burst:       s.rateBurst,
			BanScore:    s.cfg.BanScore,
			GreyScore:   s.cfg.GreyScore,
		},
		Self: NetworkSelf{
			NodeID:          s.nodeID,
			ProtocolVersion: protocolVersion,
			ClientVersion:   s.cfg.ClientVersion,
		},
	}
	view.Bootnodes = append([]string{}, s.cfg.Bootnodes...)
	view.Persistent = append([]string{}, s.cfg.PersistentPeers...)
	return view
}

func (s *Server) handleRateLimit(peer *Peer, global bool) {
	if global {
		fmt.Printf("Dropping message from %s due to global rate cap\n", peer.id)
		peer.terminate(false, fmt.Errorf("global rate cap exceeded"))
		return
	}
	status := s.adjustScore(peer.id, -ratePenalty)
	fmt.Printf("Peer %s exceeded rate limit (score %d)\n", peer.id, status.Score)
	peer.terminate(status.Banned, fmt.Errorf("peer rate limit exceeded"))
}

func (s *Server) recordValidMessage(id string) {
	s.touchPeer(id)
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

	status := s.adjustScore(peer.id, -malformedPenalty)
	fmt.Printf("Protocol violation from %s: %v (score %d)\n", peer.id, err, status.Score)
	peer.terminate(status.Banned, err)
}

func (s *Server) adjustScore(id string, delta int) ReputationStatus {
	if s.reputation == nil {
		return ReputationStatus{}
	}
	persistent := s.isPersistentPeer(id)
	status := s.reputation.Adjust(id, delta, s.now(), persistent)
	s.updatePeerGreylist(id, status.Greylisted)
	s.updatePeerRecordScore(id, status.Score)
	return status
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
	if s.reputation == nil {
		return false
	}
	return s.reputation.IsBanned(id, s.now())
}

func (s *Server) applyBan(id string, persistent bool) {
	if id == "" {
		return
	}
	if s.reputation == nil {
		return
	}
	s.reputation.Adjust(id, -s.cfg.BanScore, s.now(), persistent)
	s.mu.Lock()
	delete(s.metrics, id)
	s.mu.Unlock()
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

func (s *Server) isPersistentPeer(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.persistentIDs[id]
	return ok
}

func directionForPeer(peer *Peer) string {
	if peer == nil {
		return ""
	}
	if peer.inbound {
		return "inbound"
	}
	return "outbound"
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
