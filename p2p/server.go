package p2p

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"nhbchain/crypto"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

const (
	handshakeTimeout   = 5 * time.Second
	readTimeout        = 90 * time.Second
	writeTimeout       = 5 * time.Second
	outboundQueueSize  = 64
	maxMessageSize     = 1 << 20 // 1 MiB
	handshakeNonceSize = 32

	malformedPenalty       = 2
	reputationBanThreshold = -6
	banDuration            = 15 * time.Minute
)

var errQueueFull = errors.New("peer outbound queue full")

type handshakeMessage struct {
	ChainID   uint64 `json:"chainId"`
	NodeID    string `json:"nodeId"`
	PubKey    []byte `json:"pubKey"`
	Nonce     []byte `json:"nonce"`
	Signature []byte `json:"signature"`
}

// Server coordinates peer connections and message dissemination.
type Server struct {
	listenAddr string
	handler    MessageHandler
	privKey    *crypto.PrivateKey
	nodeID     string
	chainID    uint64

	mu         sync.RWMutex
	peers      map[string]*Peer
	reputation map[string]int
	banned     map[string]time.Time
}

// NewServer creates a P2P server with authenticated handshakes.
func NewServer(listenAddr string, handler MessageHandler, privKey *crypto.PrivateKey, chainID uint64) *Server {
	nodeID := privKey.PubKey().Address().String()
	return &Server{
		listenAddr: listenAddr,
		handler:    handler,
		privKey:    privKey,
		nodeID:     nodeID,
		chainID:    chainID,
		peers:      make(map[string]*Peer),
		reputation: make(map[string]int),
		banned:     make(map[string]time.Time),
	}
}

// Start begins listening for inbound peers and negotiating handshakes.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return err
	}
	fmt.Printf("P2P server listening on %s (node %s)\n", s.listenAddr, s.nodeID)
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
	if err := s.initPeer(conn); err != nil {
		fmt.Printf("Inbound connection from %s rejected: %v\n", conn.RemoteAddr(), err)
		conn.Close()
	}
}

func (s *Server) initPeer(conn net.Conn) error {
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

	peer := newPeer(remote.NodeID, conn, reader, s)
	if err := s.registerPeer(peer); err != nil {
		return err
	}
	fmt.Printf("New peer connected: %s (%s)\n", peer.id, peer.remoteAddr)
	peer.start()
	return nil
}

func (s *Server) performHandshake(ctx context.Context, conn net.Conn, reader *bufio.Reader) (*handshakeMessage, error) {
	local, err := s.buildHandshake()
	if err != nil {
		return nil, fmt.Errorf("prepare handshake: %w", err)
	}
	if err := writeFrame(ctx, conn, local); err != nil {
		return nil, fmt.Errorf("send handshake: %w", err)
	}

	payload, err := readFrame(ctx, conn, reader)
	if err != nil {
		return nil, fmt.Errorf("read handshake: %w", err)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("empty handshake from peer")
	}

	var remote handshakeMessage
	if err := json.Unmarshal(payload, &remote); err != nil {
		return nil, fmt.Errorf("decode handshake: %w", err)
	}

	nodeID, verifyErr := s.verifyHandshake(&remote)
	if verifyErr != nil {
		return nil, verifyErr
	}
	remote.NodeID = nodeID
	return &remote, nil
}

func (s *Server) buildHandshake() (*handshakeMessage, error) {
	nonce := make([]byte, handshakeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate handshake nonce: %w", err)
	}
	pubKey := s.privKey.PubKey().PublicKey
	digest := handshakeDigest(s.chainID, nonce)
	sig, err := ethcrypto.Sign(digest, s.privKey.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("sign handshake: %w", err)
	}

	return &handshakeMessage{
		ChainID:   s.chainID,
		NodeID:    s.nodeID,
		PubKey:    ethcrypto.FromECDSAPub(pubKey),
		Nonce:     nonce,
		Signature: sig,
	}, nil
}

func handshakeDigest(chainID uint64, nonce []byte) []byte {
	buf := make([]byte, 8+len(nonce))
	binary.BigEndian.PutUint64(buf[:8], chainID)
	copy(buf[8:], nonce)
	sum := sha256.Sum256(buf)
	return sum[:]
}

func (s *Server) verifyHandshake(msg *handshakeMessage) (string, error) {
	if len(msg.Nonce) != handshakeNonceSize {
		return "", fmt.Errorf("invalid handshake nonce length: %d", len(msg.Nonce))
	}
	if len(msg.Signature) != 65 {
		return "", fmt.Errorf("invalid handshake signature length: %d", len(msg.Signature))
	}
	if len(msg.PubKey) == 0 {
		return "", fmt.Errorf("handshake missing public key")
	}

	pubKey, err := ethcrypto.UnmarshalPubkey(msg.PubKey)
	if err != nil {
		return "", fmt.Errorf("invalid public key: %w", err)
	}
	nodeID := crypto.NewAddress(crypto.NHBPrefix, ethcrypto.PubkeyToAddress(*pubKey).Bytes()).String()

	digest := handshakeDigest(msg.ChainID, msg.Nonce)
	if !ethcrypto.VerifySignature(msg.PubKey, digest, msg.Signature[:64]) {
		return nodeID, fmt.Errorf("invalid handshake signature")
	}
	if msg.NodeID != nodeID {
		return nodeID, fmt.Errorf("node ID mismatch: claimed %s expected %s", msg.NodeID, nodeID)
	}
	if msg.ChainID != s.chainID {
		return nodeID, fmt.Errorf("chain ID mismatch: remote %d local %d", msg.ChainID, s.chainID)
	}
	return nodeID, nil
}

func writeFrame(ctx context.Context, conn net.Conn, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
		defer conn.SetWriteDeadline(time.Time{})
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}

func readFrame(ctx context.Context, conn net.Conn, reader *bufio.Reader) ([]byte, error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		defer conn.SetReadDeadline(time.Time{})
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		return nil, err
	}
	return bytes.TrimSpace(line), nil
}

func (s *Server) registerPeer(peer *Peer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.peers[peer.id]; exists {
		return fmt.Errorf("peer %s already connected", peer.id)
	}
	if expiry, banned := s.banned[peer.id]; banned {
		if time.Now().After(expiry) {
			delete(s.banned, peer.id)
		} else {
			return fmt.Errorf("peer %s banned until %s", peer.id, expiry.Format(time.RFC3339))
		}
	}
	s.peers[peer.id] = peer
	return nil
}

func (s *Server) removePeer(peer *Peer, ban bool, reason error) {
	s.mu.Lock()
	if current, ok := s.peers[peer.id]; ok && current == peer {
		delete(s.peers, peer.id)
	}
	s.mu.Unlock()

	if ban {
		s.banPeer(peer.id)
		fmt.Printf("Peer %s disconnected and banned: %v\n", peer.id, reason)
		return
	}
	if reason != nil {
		fmt.Printf("Peer %s disconnected: %v\n", peer.id, reason)
	} else {
		fmt.Printf("Peer %s disconnected\n", peer.id)
	}
}

func (s *Server) isBanned(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.banned[id]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(s.banned, id)
		delete(s.reputation, id)
		return false
	}
	return true
}

func (s *Server) banPeer(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.banned[id] = time.Now().Add(banDuration)
	s.reputation[id] = reputationBanThreshold
}

func (s *Server) adjustReputation(id string, delta int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	rep := s.reputation[id] + delta
	s.reputation[id] = rep
	return rep
}

func (s *Server) handleProtocolViolation(peer *Peer, err error) {
	rep := s.adjustReputation(peer.id, -malformedPenalty)
	fmt.Printf("Protocol violation from %s: %v (reputation %d)\n", peer.id, err, rep)
	ban := rep <= reputationBanThreshold
	peer.terminate(ban, err)
}

// Connect dials a remote peer and establishes a secure session.
func (s *Server) Connect(addr string) error {
	dialer := &net.Dialer{Timeout: handshakeTimeout}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return err
	}
	if err := s.initPeer(conn); err != nil {
		conn.Close()
		return fmt.Errorf("handshake with %s failed: %w", addr, err)
	}
	fmt.Printf("Connected to peer: %s\n", addr)
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
			}
			peer.terminate(false, err)
		}
	}
	return errors.Join(errs...)
}

// Peer represents a remote participant in the network.
type Peer struct {
	id         string
	conn       net.Conn
	reader     *bufio.Reader
	outbound   chan *Message
	server     *Server
	remoteAddr string

	ctx    context.Context
	cancel context.CancelFunc

	closeOnce sync.Once
}

func newPeer(id string, conn net.Conn, reader *bufio.Reader, server *Server) *Peer {
	ctx, cancel := context.WithCancel(context.Background())
	return &Peer{
		id:         id,
		conn:       conn,
		reader:     reader,
		outbound:   make(chan *Message, outboundQueueSize),
		server:     server,
		remoteAddr: conn.RemoteAddr().String(),
		ctx:        ctx,
		cancel:     cancel,
	}
}

func (p *Peer) start() {
	go p.readLoop()
	go p.writeLoop()
}

func (p *Peer) Enqueue(msg *Message) error {
	select {
	case <-p.ctx.Done():
		return fmt.Errorf("peer shutting down")
	default:
	}

	select {
	case p.outbound <- msg:
		return nil
	case <-p.ctx.Done():
		return fmt.Errorf("peer shutting down")
	default:
		return errQueueFull
	}
}

func (p *Peer) readLoop() {
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		if err := p.conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			p.terminate(false, fmt.Errorf("set read deadline: %w", err))
			return
		}

		line, err := p.reader.ReadBytes('\n')
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				p.terminate(false, fmt.Errorf("peer %s read timeout", p.id))
				return
			}
			if errors.Is(err, io.EOF) {
				p.terminate(false, io.EOF)
				return
			}
			p.terminate(false, fmt.Errorf("read error: %w", err))
			return
		}

		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		if len(trimmed) > maxMessageSize {
			p.server.handleProtocolViolation(p, fmt.Errorf("message exceeds max size (%d bytes)", len(trimmed)))
			return
		}

		var msg Message
		if err := json.Unmarshal(trimmed, &msg); err != nil {
			p.server.handleProtocolViolation(p, fmt.Errorf("malformed message: %w", err))
			return
		}

		if err := p.server.handler.HandleMessage(&msg); err != nil {
			fmt.Printf("Error handling message from %s: %v\n", p.id, err)
		}
	}
}

func (p *Peer) writeLoop() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case msg, ok := <-p.outbound:
			if !ok {
				return
			}
			ctx, cancel := context.WithTimeout(p.ctx, writeTimeout)
			err := p.writeMessage(ctx, msg)
			cancel()
			if err != nil {
				p.terminate(false, fmt.Errorf("write error: %w", err))
				return
			}
		}
	}
}

func (p *Peer) writeMessage(ctx context.Context, msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := p.conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
		defer p.conn.SetWriteDeadline(time.Time{})
	}
	_, err = p.conn.Write(append(data, '\n'))
	return err
}

func (p *Peer) terminate(ban bool, reason error) {
	p.closeOnce.Do(func() {
		p.cancel()
		p.conn.Close()
		close(p.outbound)
		p.server.removePeer(p, ban, reason)
	})
}
