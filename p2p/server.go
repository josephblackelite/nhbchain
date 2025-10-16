package p2p

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"nhbchain/crypto"
	"nhbchain/observability/logging"
	"nhbchain/p2p/seeds"
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

var (
	ErrPeerUnknown     = errors.New("p2p: unknown peer")
	ErrPeerBanned      = errors.New("p2p: peer is banned")
	ErrDialTargetEmpty = errors.New("p2p: empty dial target")
	ErrInvalidAddress  = errors.New("p2p: invalid dial address")
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
	MinPeers         int
	OutboundPeers    int
	Bootnodes        []string
	PersistentPeers  []string
	Seeds            []string
	SeedOrigins      []SeedOrigin
	SeedRegistry     *seeds.Registry
	SeedResolver     seeds.Resolver
	SeedRefresh      time.Duration
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
	DialBackoff      time.Duration
	MaxDialBackoff   time.Duration
	EnablePEX        bool
}

// SeedOrigin captures the provenance metadata for a seed entry supplied via
// configuration or the on-chain registry.
type SeedOrigin struct {
	NodeID    string
	Address   string
	Source    string
	NotBefore int64
	NotAfter  int64
}

func normalizeSeedOrigins(cfg ServerConfig, logger *slog.Logger) (static []SeedOrigin, dynamic []SeedOrigin) {
	if logger == nil {
		logger = slog.Default().With(slog.String("component", "p2p_server"))
	}
	origins := cfg.SeedOrigins
	if len(origins) == 0 {
		endpoints := parseSeedList(cfg.Seeds, logger)
		static = make([]SeedOrigin, 0, len(endpoints))
		for _, ep := range endpoints {
			static = append(static, SeedOrigin{NodeID: ep.NodeID, Address: ep.Address, Source: "config"})
		}
		return static, nil
	}
	for _, origin := range origins {
		node, addr, err := normalizeSeedComponents(origin.NodeID, origin.Address)
		if err != nil {
			logger.Warn("Ignoring configured seed origin",
				logging.MaskField("seed_id", origin.NodeID),
				logging.MaskField("seed_address", origin.Address),
				slog.Any("error", err))
			continue
		}
		source := strings.TrimSpace(origin.Source)
		if source == "" {
			source = "config"
		}
		normalized := SeedOrigin{
			NodeID:    node,
			Address:   addr,
			Source:    source,
			NotBefore: origin.NotBefore,
			NotAfter:  origin.NotAfter,
		}
		if strings.HasPrefix(strings.ToLower(source), "config") {
			static = append(static, normalized)
		} else {
			dynamic = append(dynamic, normalized)
		}
	}
	return static, dynamic
}

func normalizeSeedComponents(nodeID, address string) (string, string, error) {
	node := normalizeHex(nodeID)
	if node == "" {
		return "", "", fmt.Errorf("missing node ID")
	}
	addr := strings.TrimSpace(address)
	if addr == "" {
		return "", "", fmt.Errorf("missing address")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return "", "", fmt.Errorf("invalid address: %w", err)
	}
	return node, addr, nil
}

type dialFunc func(context.Context, string) (net.Conn, error)

// Server coordinates peer connections and message dissemination.
type Server struct {
	cfg     ServerConfig
	handler MessageHandler
	privKey *crypto.PrivateKey
	nodeID  string
	genesis []byte

	logger *slog.Logger

	mu               sync.RWMutex
	peers            map[string]*Peer
	inboundCount     int
	outboundCount    int
	metrics          map[string]*peerMetrics
	byAddr           map[string]string
	persistentIDs    map[string]struct{}
	records          map[string]*PeerRecord
	metricsCollector *networkMetrics

	listenMu    sync.RWMutex
	listenAddrs []string

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

	peerstore *Peerstore

	dialMu      sync.Mutex
	pendingDial map[string]struct{}
	backoff     map[string]time.Duration
	persistent  map[string]struct{}

	seedMu       sync.RWMutex
	seeds        []seedEndpoint
	seedInfos    []SeedInfo
	staticSeeds  []SeedOrigin
	dynamicSeeds []SeedOrigin
	seedRegistry *seeds.Registry
	seedResolver seeds.Resolver
	seedRefresh  time.Duration
	seedQuit     chan struct{}
	connMgr      *connManager
	connMgrOnce  sync.Once

	pex *pexManager
}

func cloneSeedOrigins(origins []SeedOrigin) []SeedOrigin {
	if len(origins) == 0 {
		return nil
	}
	out := make([]SeedOrigin, len(origins))
	copy(out, origins)
	return out
}

func seedOriginActive(origin SeedOrigin, now time.Time) bool {
	if origin.NotBefore > 0 && now.Unix() < origin.NotBefore {
		return false
	}
	if origin.NotAfter > 0 && now.Unix() > origin.NotAfter {
		return false
	}
	return true
}

func (s *Server) currentTime() time.Time {
	if s == nil || s.now == nil {
		return time.Now()
	}
	return s.now()
}

func (s *Server) installStaticSeeds(origins []SeedOrigin) {
	if s == nil {
		return
	}
	s.seedMu.Lock()
	s.staticSeeds = cloneSeedOrigins(origins)
	endpoints := s.rebuildSeedsLocked(s.currentTime())
	s.seedMu.Unlock()
	if s.connMgr != nil {
		s.connMgr.updateSeeds(endpoints)
	}
}

func (s *Server) setDynamicSeeds(origins []SeedOrigin) {
	if s == nil {
		return
	}
	s.seedMu.Lock()
	s.dynamicSeeds = cloneSeedOrigins(origins)
	endpoints := s.rebuildSeedsLocked(s.currentTime())
	s.seedMu.Unlock()
	if s.connMgr != nil {
		s.connMgr.updateSeeds(endpoints)
	}
}

func (s *Server) refreshSeedRegistry() error {
	if s == nil || s.seedRegistry == nil {
		return nil
	}
	resolver := s.seedResolver
	if resolver == nil {
		resolver = seeds.DefaultResolver()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resolved, err := s.seedRegistry.Resolve(ctx, s.currentTime(), resolver)
	if len(resolved) > 0 {
		origins := make([]SeedOrigin, 0, len(resolved))
		for _, entry := range resolved {
			origins = append(origins, SeedOrigin{
				NodeID:    entry.NodeID,
				Address:   entry.Address,
				Source:    entry.Source,
				NotBefore: entry.NotBefore,
				NotAfter:  entry.NotAfter,
			})
		}
		s.setDynamicSeeds(origins)
	}
	return err
}

func (s *Server) seedRotationLoop() {
	if s == nil || s.seedRegistry == nil {
		return
	}
	interval := s.seedRefresh
	if interval <= 0 {
		interval = s.seedRegistry.RefreshInterval()
	}
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := s.refreshSeedRegistry(); err != nil {
				s.log().Warn("Seed registry refresh failed",
					slog.Any("error", err))
			}
		case <-s.seedQuit:
			return
		}
	}
}

func (s *Server) rebuildSeedsLocked(now time.Time) []seedEndpoint {
	combined := make([]SeedOrigin, 0, len(s.staticSeeds)+len(s.dynamicSeeds))
	combined = append(combined, s.staticSeeds...)
	combined = append(combined, s.dynamicSeeds...)
	seen := make(map[string]struct{}, len(combined))
	endpoints := make([]seedEndpoint, 0, len(combined))
	infos := make([]SeedInfo, 0, len(combined))
	for _, origin := range combined {
		if !seedOriginActive(origin, now) {
			continue
		}
		ep := seedEndpoint{NodeID: origin.NodeID, Address: origin.Address}
		key := ep.NodeID + "@" + ep.Address
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		endpoints = append(endpoints, ep)
		infos = append(infos, SeedInfo{
			NodeID:    origin.NodeID,
			Address:   origin.Address,
			Source:    origin.Source,
			NotBefore: origin.NotBefore,
			NotAfter:  origin.NotAfter,
		})
	}
	s.seeds = endpoints
	s.seedInfos = infos
	return append([]seedEndpoint(nil), endpoints...)
}

func (s *Server) seedSnapshot() []seedEndpoint {
	if s == nil {
		return nil
	}
	s.seedMu.RLock()
	defer s.seedMu.RUnlock()
	out := make([]seedEndpoint, len(s.seeds))
	copy(out, s.seeds)
	return out
}

func (s *Server) seedInfoSnapshot() []SeedInfo {
	if s == nil {
		return nil
	}
	s.seedMu.RLock()
	defer s.seedMu.RUnlock()
	out := make([]SeedInfo, len(s.seedInfos))
	copy(out, s.seedInfos)
	return out
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

// PeerNetInfo captures operational state for observability RPCs.
type PeerNetInfo struct {
	NodeID      string    `json:"nodeId"`
	Address     string    `json:"addr"`
	Direction   string    `json:"direction"`
	State       string    `json:"state"`
	Score       int       `json:"score"`
	LastSeen    time.Time `json:"lastSeen"`
	Fails       int       `json:"fails"`
	BannedUntil time.Time `json:"bannedUntil,omitempty"`
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

// SeedInfo summarises a configured seed endpoint exposed through RPC.
type SeedInfo struct {
	NodeID    string `json:"nodeId"`
	Address   string `json:"address"`
	Source    string `json:"source"`
	NotBefore int64  `json:"notBefore,omitempty"`
	NotAfter  int64  `json:"notAfter,omitempty"`
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
	Seeds      []SeedInfo    `json:"seeds"`
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
	if cfg.MinPeers <= 0 || cfg.MinPeers > cfg.MaxPeers {
		cfg.MinPeers = cfg.MaxPeers / 2
		if cfg.MinPeers <= 0 {
			cfg.MinPeers = 1
		}
	}
	if cfg.OutboundPeers <= 0 || cfg.OutboundPeers > cfg.MaxOutbound {
		cfg.OutboundPeers = cfg.MaxOutbound
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
	if cfg.DialBackoff <= 0 {
		cfg.DialBackoff = time.Second
	}
	if cfg.MaxDialBackoff <= 0 {
		cfg.MaxDialBackoff = maxDialBackoff
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

	baseLogger := slog.Default().With(slog.String("component", "p2p_server"))

	server := &Server{
		cfg:              cfg,
		handler:          handler,
		privKey:          privKey,
		nodeID:           nodeID,
		genesis:          cloneBytes(cfg.GenesisHash),
		logger:           baseLogger,
		peers:            make(map[string]*Peer),
		metrics:          make(map[string]*peerMetrics),
		byAddr:           make(map[string]string),
		persistentIDs:    make(map[string]struct{}),
		records:          make(map[string]*PeerRecord),
		metricsCollector: newNetworkMetrics(),
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
		listenAddrs:      []string{},
		seedResolver:     cfg.SeedResolver,
		seedRegistry:     cfg.SeedRegistry,
		seedRefresh:      cfg.SeedRefresh,
		seedQuit:         make(chan struct{}),
	}

	if server.seedResolver == nil {
		server.seedResolver = seeds.DefaultResolver()
	}
	if server.seedRegistry != nil && server.seedRefresh <= 0 {
		server.seedRefresh = server.seedRegistry.RefreshInterval()
	}

	staticOrigins, dynamicOrigins := normalizeSeedOrigins(cfg, server.log())
	server.installStaticSeeds(staticOrigins)
	if len(dynamicOrigins) > 0 {
		server.setDynamicSeeds(dynamicOrigins)
	}
	if server.seedRegistry != nil && len(dynamicOrigins) == 0 {
		if err := server.refreshSeedRegistry(); err != nil {
			server.log().Warn("Seed registry lookup failed",
				slog.Any("error", err))
		}
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

	if cfg.EnablePEX {
		server.pex = newPexManager(server)
	}

	server.addListenAddress(cfg.ListenAddress)

	if server.seedRegistry != nil {
		go server.seedRotationLoop()
	}

	return server
}

// SetPeerstore attaches a persistent peerstore to the server for dial metadata.
func (s *Server) SetPeerstore(store *Peerstore) {
	s.peerstore = store
}

func (s *Server) startConnManager() {
	s.connMgrOnce.Do(func() {
		mgr := newConnManager(s)
		if mgr == nil {
			return
		}
		s.connMgr = mgr
		mgr.start()
	})
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
	s.log().Info("P2P server listening",
		logging.MaskField("listen_address", ln.Addr().String()),
		slog.Uint64("chain_id", s.cfg.ChainID),
		slog.String("genesis", summarizeHash(s.genesis)),
		logging.MaskField("node_id", s.nodeID),
		slog.String("client_version", s.cfg.ClientVersion))
	s.addListenAddress(ln.Addr().String())

	s.startConnManager()
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
		s.log().Warn("Inbound connection rejected",
			logging.MaskField("peer_address", conn.RemoteAddr().String()),
			slog.Any("error", err))
		conn.Close()
	}
}

func (s *Server) initPeer(conn net.Conn, inbound bool, persistent bool, dialAddr string) (err error) {
	if s.metricsCollector != nil {
		start := s.now()
		defer func() {
			outcome := "success"
			if err != nil {
				outcome = "failure"
			}
			s.metricsCollector.recordHandshake(outcome)
			_ = start
		}()
	}
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

	trimmedDial := strings.TrimSpace(dialAddr)
	addresses := append([]string{}, remote.addrs...)
	if len(addresses) == 0 && trimmedDial != "" {
		addresses = append(addresses, trimmedDial)
	}
	if len(addresses) == 0 {
		addresses = append(addresses, conn.RemoteAddr().String())
	}

	now := s.now()
	if s.pex != nil {
		for _, addr := range addresses {
			s.pex.recordPeer(remote.nodeID, addr, now)
		}
	}

	primaryAddr := ""
	if len(addresses) > 0 {
		primaryAddr = strings.TrimSpace(addresses[0])
	}

	if s.peerstore != nil && primaryAddr != "" {
		entry := PeerstoreEntry{Addr: primaryAddr, NodeID: remote.nodeID}
		if err := s.peerstore.Put(entry); err != nil {
			s.log().Warn("Failed to persist peer entry",
				logging.MaskField("peer_id", remote.nodeID),
				logging.MaskField("peer_address", entry.Addr),
				slog.Any("error", err))
		}
		if _, err := s.peerstore.RecordSuccess(remote.nodeID, now); err != nil {
			s.log().Warn("Failed to record peer success",
				logging.MaskField("peer_id", remote.nodeID),
				slog.Any("error", err))
		}
	}

	if trimmedDial == "" {
		trimmedDial = primaryAddr
	}

	peer := newPeer(remote.nodeID, remote.ClientVersion, conn, reader, s, inbound, persistent, trimmedDial)
	if err := s.registerPeer(peer); err != nil {
		return err
	}
	s.log().Info("Peer connected",
		logging.MaskField("peer_id", peer.id),
		logging.MaskField("peer_address", peer.remoteAddr),
		slog.String("client_version", remote.ClientVersion),
		slog.Bool("inbound", inbound))
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
	if s.metricsCollector != nil {
		s.metricsCollector.observePeerStatus(remote.nodeID, ReputationStatus{Score: score})
	}
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

func (s *Server) handlePexRequest(peer pexPeer, payload PexRequestPayload) error {
	if s == nil || peer == nil {
		return nil
	}
	if s.pex == nil {
		return nil
	}
	return s.pex.handleRequest(peer, payload)
}

func (s *Server) handlePexAddresses(peer pexPeer, payload PexAddressesPayload) {
	if s == nil || peer == nil {
		return
	}
	if s.pex == nil {
		return
	}
	s.pex.handleAddresses(peer, payload)
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

	if s.metricsCollector != nil {
		s.metricsCollector.removePeer(peer.id)
	}

	if s.pex != nil {
		s.pex.forgetPeer(peer.id)
	}

	if ban {
		s.applyBan(peer.id, peer.persistent)
		s.log().Warn("Peer disconnected and banned",
			logging.MaskField("peer_id", peer.id),
			logging.MaskField("peer_address", peer.remoteAddr),
			slog.Any("error", reason))
	} else if reason != nil {
		s.log().Info("Peer disconnected",
			logging.MaskField("peer_id", peer.id),
			logging.MaskField("peer_address", peer.remoteAddr),
			slog.Any("error", reason))
	} else {
		s.log().Info("Peer disconnected",
			logging.MaskField("peer_id", peer.id),
			logging.MaskField("peer_address", peer.remoteAddr))
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
		s.markDialFailure(addr)
		return err
	}

	persistent := s.isPersistent(addr)
	if err := s.initPeer(conn, false, persistent, addr); err != nil {
		conn.Close()
		s.markDialFailure(addr)
		return fmt.Errorf("handshake with %s failed: %w", addr, err)
	}
	s.log().Info("Outbound connection established",
		logging.MaskField("peer_address", addr))
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
				s.log().Warn("Peer send queue full",
					logging.MaskField("peer_id", peer.id))
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
	view.Seeds = s.seedInfoSnapshot()
	return view
}

// ListenAddresses returns the configured and discovered listen addresses.
func (s *Server) ListenAddresses() []string {
	if s == nil {
		return nil
	}
	s.listenMu.RLock()
	defer s.listenMu.RUnlock()
	if len(s.listenAddrs) == 0 {
		return nil
	}
	out := make([]string, len(s.listenAddrs))
	copy(out, s.listenAddrs)
	return out
}

// NodeID exposes the derived node identifier.
func (s *Server) NodeID() string {
	if s == nil {
		return ""
	}
	return s.nodeID
}

// NetPeers returns enriched peer metadata for operator diagnostics.
func (s *Server) NetPeers() []PeerNetInfo {
	if s == nil {
		return nil
	}
	now := s.now()
	statuses := make(map[string]ReputationStatus)
	if s.reputation != nil {
		statuses = s.reputation.Snapshot(now)
	}

	storeEntries := make(map[string]PeerstoreEntry)
	if s.peerstore != nil {
		for _, entry := range s.peerstore.Snapshot() {
			id := normalizeHex(entry.NodeID)
			if id == "" {
				continue
			}
			storeEntries[id] = entry
		}
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

	s.dialMu.Lock()
	pending := make(map[string]struct{}, len(s.pendingDial))
	for addr := range s.pendingDial {
		pending[strings.TrimSpace(addr)] = struct{}{}
	}
	s.dialMu.Unlock()

	results := make([]PeerNetInfo, 0, len(peers)+len(storeEntries)+len(statuses))
	seen := make(map[string]struct{}, len(peers)+len(storeEntries)+len(statuses))

	for _, peer := range peers {
		id := normalizeHex(peer.id)
		status := statuses[id]
		rec := records[id]

		info := PeerNetInfo{
			NodeID:    id,
			Direction: directionForPeer(peer),
			State:     "connected",
			Score:     status.Score,
			Address:   strings.TrimSpace(peer.remoteAddr),
			LastSeen:  rec.LastSeen,
		}
		if info.Address == "" {
			info.Address = strings.TrimSpace(peer.dialAddr)
		}
		if info.LastSeen.IsZero() {
			info.LastSeen = now
		}
		if status.Banned {
			info.BannedUntil = status.Until
		}
		if rec.Score != 0 && info.Score == 0 {
			info.Score = rec.Score
		}
		if entry, ok := storeEntries[id]; ok {
			info.Fails = entry.Fails
			if entry.Addr != "" && info.Address == "" {
				info.Address = strings.TrimSpace(entry.Addr)
			}
			if entry.LastSeen.After(info.LastSeen) {
				info.LastSeen = entry.LastSeen
			}
			if entry.BannedUntil.After(info.BannedUntil) {
				info.BannedUntil = entry.BannedUntil
			}
			delete(storeEntries, id)
		}
		results = append(results, info)
		seen[id] = struct{}{}
	}

	for id, entry := range storeEntries {
		info := PeerNetInfo{
			NodeID:   id,
			Address:  strings.TrimSpace(entry.Addr),
			Score:    int(math.Round(entry.Score)),
			LastSeen: entry.LastSeen,
			Fails:    entry.Fails,
			State:    "known",
		}
		if status, ok := statuses[id]; ok {
			if status.Score != 0 {
				info.Score = status.Score
			}
			if status.Banned && status.Until.After(info.BannedUntil) {
				info.BannedUntil = status.Until
			}
		}
		if entry.BannedUntil.After(info.BannedUntil) {
			info.BannedUntil = entry.BannedUntil
		}
		if info.BannedUntil.After(now) {
			info.State = "banned"
		}
		if _, ok := pending[info.Address]; ok && info.State != "connected" {
			info.State = "dialing"
		}
		results = append(results, info)
		seen[id] = struct{}{}
	}

	for id, status := range statuses {
		if _, ok := seen[id]; ok {
			continue
		}
		info := PeerNetInfo{NodeID: id, Score: status.Score, State: "tracked"}
		if status.Banned {
			info.State = "banned"
			info.BannedUntil = status.Until
		}
		results = append(results, info)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].NodeID < results[j].NodeID
	})
	return results
}

// DialPeer queues a manual dial respecting configured backoff and bans.
func (s *Server) DialPeer(target string) error {
	if s == nil {
		return fmt.Errorf("%w", ErrDialTargetEmpty)
	}
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return fmt.Errorf("%w", ErrDialTargetEmpty)
	}

	now := s.now()
	addr := ""
	nodeID := ""

	if looksLikeNodeID(trimmed) {
		nodeID = normalizeHex(trimmed)
		if s.hasPeer(nodeID) {
			return nil
		}
		if s.peerstore != nil {
			if entry, ok := s.peerstore.ByNodeID(nodeID); ok {
				addr = strings.TrimSpace(entry.Addr)
				if entry.BannedUntil.After(now) {
					return fmt.Errorf("%w: banned until %s", ErrPeerBanned, entry.BannedUntil.Format(time.RFC3339))
				}
			}
		}
		if addr == "" {
			for _, seed := range s.seedSnapshot() {
				if normalizeHex(seed.NodeID) == nodeID {
					addr = strings.TrimSpace(seed.Address)
					break
				}
			}
		}
		if addr == "" {
			return fmt.Errorf("%w: %s", ErrPeerUnknown, nodeID)
		}
	} else {
		addr = trimmed
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidAddress, err)
		}
		if s.isConnectedToAddress(addr) {
			return nil
		}
		if s.peerstore != nil {
			if entry, ok := s.peerstore.Get(addr); ok {
				nodeID = normalizeHex(entry.NodeID)
				if entry.BannedUntil.After(now) {
					return fmt.Errorf("%w: banned until %s", ErrPeerBanned, entry.BannedUntil.Format(time.RFC3339))
				}
			}
		}
	}

	if nodeID != "" {
		if s.isBanned(nodeID) {
			until := now.Add(s.cfg.PeerBanDuration)
			if s.reputation != nil {
				if banned, expiry := s.reputation.BanInfo(nodeID, now); banned {
					until = expiry
				}
			}
			return fmt.Errorf("%w: banned until %s", ErrPeerBanned, until.Format(time.RFC3339))
		}
		if s.peerstore != nil && s.peerstore.IsBanned(nodeID, now) {
			return fmt.Errorf("%w: peerstore ban active", ErrPeerBanned)
		}
	}

	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("%w", ErrInvalidAddress)
	}

	if s.isConnectedToAddress(addr) {
		return nil
	}

	wait := time.Duration(0)
	if s.peerstore != nil {
		next := s.peerstore.NextDialAt(addr, now)
		if next.After(now) {
			wait = next.Sub(now)
		}
	}

	s.dialMu.Lock()
	if backoff := s.backoff[addr]; backoff > wait {
		wait = backoff
	}
	s.dialMu.Unlock()

	return s.enqueueDial(addr, wait)
}

// BanPeer applies an operator ban and disconnects the peer immediately.
func (s *Server) BanPeer(nodeID string, duration time.Duration) error {
	if s == nil {
		return fmt.Errorf("%w", ErrPeerUnknown)
	}
	normalized := normalizeHex(nodeID)
	if normalized == "" {
		return fmt.Errorf("%w", ErrPeerUnknown)
	}

	now := s.now()
	known := s.hasPeer(normalized)
	var storeEntry *PeerstoreEntry
	if s.peerstore != nil {
		if entry, ok := s.peerstore.ByNodeID(normalized); ok {
			copy := entry
			storeEntry = &copy
			known = true
		}
	}
	if !known {
		for _, seed := range s.seedSnapshot() {
			if normalizeHex(seed.NodeID) == normalized {
				known = true
				break
			}
		}
	}
	if !known {
		return fmt.Errorf("%w: %s", ErrPeerUnknown, normalized)
	}

	if duration <= 0 {
		duration = s.cfg.PeerBanDuration
	}
	until := now.Add(duration)

	persistent := s.isPersistentPeer(normalized)
	s.applyBan(normalized, persistent)
	if s.reputation != nil {
		s.reputation.SetBan(normalized, until, now)
	}
	if storeEntry != nil && s.peerstore != nil {
		if err := s.peerstore.SetBan(normalized, until); err != nil {
			s.log().Warn("Failed to record ban in peerstore",
				logging.MaskField("peer_id", normalized),
				slog.Any("error", err))
		}
	}

	s.mu.RLock()
	peer := s.peers[normalized]
	s.mu.RUnlock()
	if peer != nil {
		peer.terminate(true, fmt.Errorf("peer banned by operator"))
	}
	if storeEntry != nil && storeEntry.Addr != "" {
		s.dialMu.Lock()
		delete(s.pendingDial, strings.TrimSpace(storeEntry.Addr))
		s.dialMu.Unlock()
	}
	return nil
}

func (s *Server) enqueueDial(addr string, wait time.Duration) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("%w", ErrInvalidAddress)
	}

	s.dialMu.Lock()
	if _, pending := s.pendingDial[addr]; pending {
		s.dialMu.Unlock()
		return nil
	}
	s.pendingDial[addr] = struct{}{}
	s.dialMu.Unlock()

	go func(delay time.Duration, target string) {
		if delay > 0 {
			timer := time.NewTimer(delay)
			<-timer.C
		}
		err := s.Connect(target)
		s.dialMu.Lock()
		delete(s.pendingDial, target)
		s.dialMu.Unlock()
		if err != nil {
			s.log().Warn("Manual dial failed",
				logging.MaskField("peer_address", target),
				slog.Any("error", err))
			s.scheduleReconnect(target)
		} else {
			s.resetBackoff(target)
		}
	}(wait, addr)

	return nil
}

func (s *Server) addListenAddress(addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return
	}
	s.listenMu.Lock()
	for _, existing := range s.listenAddrs {
		if existing == addr {
			s.listenMu.Unlock()
			return
		}
	}
	s.listenAddrs = append(s.listenAddrs, addr)
	s.listenMu.Unlock()
}

func looksLikeNodeID(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, "@:") {
		return false
	}
	if strings.HasPrefix(trimmed, "0x") || strings.HasPrefix(trimmed, "0X") {
		trimmed = trimmed[2:]
	}
	if trimmed == "" {
		return false
	}
	for _, ch := range trimmed {
		switch {
		case ch >= '0' && ch <= '9':
		case ch >= 'a' && ch <= 'f':
		case ch >= 'A' && ch <= 'F':
		default:
			return false
		}
	}
	return true
}

func (s *Server) handleRateLimit(peer *Peer, global bool) {
	if global {
		s.log().Warn("Global rate cap exceeded",
			logging.MaskField("peer_id", peer.id))
		peer.terminate(false, fmt.Errorf("global rate cap exceeded"))
		return
	}
	if s.reputation != nil {
		status := s.reputation.MarkMisbehavior(peer.id, s.now())
		s.updatePeerRecordScore(peer.id, status.Score)
		if s.metricsCollector != nil {
			s.metricsCollector.observePeerStatus(peer.id, status)
		}
	}
	status := s.adjustScore(peer.id, -ratePenalty)
	s.log().Warn("Peer exceeded rate limit",
		logging.MaskField("peer_id", peer.id),
		slog.Int("score", status.Score))
	peer.terminate(status.Banned, fmt.Errorf("peer rate limit exceeded"))
}

func (s *Server) recordValidMessage(id string) {
	s.touchPeer(id)
	s.updatePeerMetrics(id, true)
	if s.reputation != nil {
		status := s.reputation.MarkUseful(id, s.now())
		s.updatePeerRecordScore(id, status.Score)
		if s.metricsCollector != nil {
			s.metricsCollector.observePeerStatus(id, status)
		}
	}
	if validMessageReward != 0 {
		s.adjustScore(id, validMessageReward)
	}
}

func (s *Server) observeLatency(id string, latency time.Duration) {
	if s == nil || id == "" || latency <= 0 || s.reputation == nil {
		return
	}
	status := s.reputation.ObserveLatency(id, latency, s.now())
	s.updatePeerRecordScore(id, status.Score)
	if s.metricsCollector != nil {
		s.metricsCollector.observePeerStatus(id, status)
	}
}

func (s *Server) recordGossip(direction string, msgType byte) {
	if s == nil || s.metricsCollector == nil {
		return
	}
	s.metricsCollector.recordGossip(direction, msgType)
}

func (s *Server) handleProtocolViolation(peer *Peer, err error) {
	if s.reputation != nil {
		status := s.reputation.MarkMisbehavior(peer.id, s.now())
		s.updatePeerRecordScore(peer.id, status.Score)
		if s.metricsCollector != nil {
			s.metricsCollector.observePeerStatus(peer.id, status)
		}
	}
	if s.updatePeerMetrics(peer.id, false) {
		s.log().Warn("Protocol violation: invalid message rate",
			logging.MaskField("peer_id", peer.id),
			slog.Any("error", err))
		peer.terminate(true, fmt.Errorf("invalid message rate: %w", err))
		return
	}

	status := s.adjustScore(peer.id, -malformedPenalty)
	s.log().Warn("Protocol violation",
		logging.MaskField("peer_id", peer.id),
		slog.Any("error", err),
		slog.Int("score", status.Score),
		slog.Bool("banned", status.Banned))
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
	if s.metricsCollector != nil {
		s.metricsCollector.observePeerStatus(id, status)
	}
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

func (s *Server) log() *slog.Logger {
	if s == nil {
		return slog.Default().With(slog.String("component", "p2p_server"))
	}
	if s.logger == nil {
		s.logger = slog.Default().With(slog.String("component", "p2p_server"))
	}
	return s.logger
}

func cloneBytes(input []byte) []byte {
	if input == nil {
		return nil
	}
	cp := make([]byte, len(input))
	copy(cp, input)
	return cp
}
