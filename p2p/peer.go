package p2p

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Peer struct {
	id            string
	conn          net.Conn
	reader        *bufio.Reader
	outbound      chan *Message
	server        *Server
	remoteAddr    string
	dialAddr      string
	inbound       bool
	persistent    bool
	clientVersion string

	limiter   *tokenBucket
	baseRate  float64
	baseBurst float64
	throttled bool

	ctx    context.Context
	cancel context.CancelFunc

	closeOnce sync.Once
	closed    chan struct{}

	// queuedBytes is the payload size currently waiting in outbound, capped by
	// queueByteLimit so that a slow reader cannot pin an unbounded amount of
	// memory behind the message-count limit. dropped and lastDropLog rate-limit
	// the log line about messages dropped for a slow peer.
	queuedBytes    atomic.Int64
	queueByteLimit int64
	dropped        atomic.Uint64
	lastDropLog    atomic.Int64
}

const (
	persistentPeerRateFloor  = 256.0
	persistentPeerBurstFloor = 1024.0
	persistentPeerRateFactor = 8.0

	// A peer's outbound queue holds at most outboundQueueSize messages and, in
	// bytes, queueBytesFactor times the largest message the server accepts (but
	// never less than minQueueBytes).
	queueBytesFactor     = 4
	minQueueBytes        = 4 << 20
	queueDropLogInterval = 10 * time.Second
)

func newPeer(id string, clientVersion string, conn net.Conn, reader *bufio.Reader, server *Server, inbound bool, persistent bool, dialAddr string) *Peer {
	ctx, cancel := context.WithCancel(context.Background())
	rate := server.ratePerPeer
	burst := server.rateBurst
	if persistent {
		rate = maxFloat(rate*persistentPeerRateFactor, persistentPeerRateFloor)
		burst = maxFloat(burst*persistentPeerRateFactor, persistentPeerBurstFloor)
	}
	if burst < rate {
		burst = rate
	}
	limiter := newTokenBucket(rate, burst)
	dialAddr = strings.TrimSpace(dialAddr)
	return &Peer{
		id:            id,
		conn:          conn,
		reader:        reader,
		outbound:      make(chan *Message, outboundQueueSize),
		server:        server,
		remoteAddr:    conn.RemoteAddr().String(),
		dialAddr:      dialAddr,
		inbound:       inbound,
		persistent:    persistent,
		limiter:       limiter,
		baseRate:      rate,
		baseBurst:     burst,
		clientVersion: clientVersion,
		ctx:           ctx,
		cancel:        cancel,
		closed:        make(chan struct{}),

		queueByteLimit: queueByteLimit(server.cfg.MaxMessageBytes),
	}
}

func queueByteLimit(maxMessageBytes int) int64 {
	limit := int64(maxMessageBytes) * queueBytesFactor
	if limit < minQueueBytes {
		limit = minQueueBytes
	}
	return limit
}

func messageSize(msg *Message) int64 {
	if msg == nil {
		return 0
	}
	return int64(len(msg.Payload))
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// ID returns the peer identifier.
func (p *Peer) ID() string {
	if p == nil {
		return ""
	}
	return p.id
}

func (p *Peer) setGreylisted(on bool) {
	if p == nil || p.limiter == nil {
		return
	}
	if on {
		p.limiter.setRate(p.baseRate*greylistRateMultiplier, p.baseBurst*greylistRateMultiplier)
	} else {
		p.limiter.setRate(p.baseRate, p.baseBurst)
	}
	p.throttled = on
}

func (p *Peer) start() {
	go p.readLoop()
	go p.writeLoop()
	go p.keepaliveLoop()
}

// Enqueue queues a message for this peer alone. It never blocks: when the queue
// is full, by count or by bytes, the message is not queued and errQueueFull is
// returned. A queue that cannot keep up costs that peer messages, not the
// connection.
func (p *Peer) Enqueue(msg *Message) error {
	select {
	case <-p.ctx.Done():
		return fmt.Errorf("peer shutting down")
	default:
	}
	if msg != nil && msg.Type == MsgTypePexRequest && p.server != nil {
		p.server.noteOutgoingPexRequest(p.id, msg)
	}

	size := messageSize(msg)
	if prev := p.queuedBytes.Add(size) - size; prev > 0 && prev+size > p.queueByteLimit {
		p.queuedBytes.Add(-size)
		return errQueueFull
	}
	select {
	case p.outbound <- msg:
		return nil
	case <-p.ctx.Done():
		p.queuedBytes.Add(-size)
		return fmt.Errorf("peer shutting down")
	default:
		p.queuedBytes.Add(-size)
		return errQueueFull
	}
}

// noteDrop counts a message dropped for this peer and reports whether it is time
// to log about it again.
func (p *Peer) noteDrop() (uint64, bool) {
	total := p.dropped.Add(1)
	now := time.Now().UnixNano()
	last := p.lastDropLog.Load()
	if now-last < int64(queueDropLogInterval) {
		return total, false
	}
	return total, p.lastDropLog.CompareAndSwap(last, now)
}

func (p *Peer) keepaliveLoop() {
	interval := p.server.cfg.PingInterval
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			nonce, err := randomUint64()
			if err != nil {
				fmt.Printf("keepalive nonce generation failed for %s: %v\n", p.id, err)
				continue
			}
			msg, err := NewPingMessage(nonce, time.Now())
			if err != nil {
				fmt.Printf("build ping for %s: %v\n", p.id, err)
				continue
			}
			if err := p.Enqueue(msg); err != nil {
				if errors.Is(err, errQueueFull) {
					// A full queue is a slow peer, not a dead one: ping again next tick.
					continue
				}
				fmt.Printf("enqueue ping to %s: %v\n", p.id, err)
				return
			}
		}
	}
}

func (p *Peer) readLoop() {
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		if err := p.conn.SetReadDeadline(time.Now().Add(p.server.cfg.ReadTimeout)); err != nil {
			p.terminate(false, fmt.Errorf("set read deadline: %w", err))
			return
		}

		maxBytes := p.server.cfg.MaxMessageBytes
		var (
			line  []byte
			total int
		)

		for {
			segment, err := p.reader.ReadSlice('\n')
			total += len(segment)

			overLimit := false
			exceededLen := total
			switch {
			case err == nil:
				payloadLen := total - 1
				if payloadLen > maxBytes {
					overLimit = true
					exceededLen = payloadLen
				}
			case errors.Is(err, bufio.ErrBufferFull):
				if total > maxBytes {
					overLimit = true
				}
			case err != nil:
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

			if overLimit {
				p.server.handleProtocolViolation(p, fmt.Errorf("message exceeds max size (%d bytes)", exceededLen))
				return
			}

			line = append(line, segment...)

			if err == nil {
				break
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}

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
		if len(trimmed) > maxBytes {
			p.server.handleProtocolViolation(p, fmt.Errorf("message exceeds max size (%d bytes)", len(trimmed)))
			return
		}

		if !p.persistent {
			now := time.Now()
			if !p.server.allowIP(p.remoteAddr, now) {
				p.server.handleRateLimit(p, false)
				return
			}
			if !p.limiter.allow(now) {
				p.server.handleRateLimit(p, false)
				return
			}
			if !p.server.allowGlobal(now) {
				p.server.handleRateLimit(p, true)
				return
			}
		}

		var msg Message
		if err := json.Unmarshal(trimmed, &msg); err != nil {
			p.server.handleProtocolViolation(p, fmt.Errorf("malformed message: %w", err))
			return
		}
		if p.server != nil {
			p.server.recordGossip("in", msg.Type)
		}

		handled, err := p.handleControlMessage(&msg)
		if err != nil {
			p.server.handleProtocolViolation(p, err)
			return
		}
		if handled {
			p.server.recordValidMessage(p.id)
			continue
		}

		if !p.server.admitRequest(p, &msg) {
			continue
		}
		if err := p.server.dispatch(p, &msg); err != nil {
			if p.server != nil && IsInvalidPayload(err) {
				p.server.handleProtocolViolation(p, err)
				return
			}
			fmt.Printf("Error handling message from %s: %v\n", p.id, err)
		}
		p.server.recordValidMessage(p.id)
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
			p.queuedBytes.Add(-messageSize(msg))
			ctx, cancel := context.WithTimeout(p.ctx, p.server.cfg.WriteTimeout)
			err := p.writeMessage(ctx, msg)
			cancel()
			if err != nil {
				p.server.adjustScore(p.id, -slowPenalty)
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
	if err == nil && p.server != nil && msg != nil {
		p.server.recordGossip("out", msg.Type)
	}
	return err
}

func (p *Peer) handleControlMessage(msg *Message) (bool, error) {
	switch msg.Type {
	case MsgTypePing:
		var payload PingPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return false, fmt.Errorf("malformed ping payload: %w", err)
		}
		pong, err := NewPongMessage(payload.Nonce, time.Now())
		if err != nil {
			return false, fmt.Errorf("build pong: %w", err)
		}
		// A pong the peer's full queue has no room for is dropped: the
		// connection is not ended over it.
		if err := p.Enqueue(pong); err != nil && !errors.Is(err, errQueueFull) {
			return false, fmt.Errorf("send pong: %w", err)
		}
		p.server.touchPeer(p.id)
		if payload.Timestamp > 0 {
			sent := time.Unix(0, payload.Timestamp)
			p.server.observeLatency(p.id, time.Since(sent))
		}
		return true, nil
	case MsgTypePong:
		var payload PongPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return false, fmt.Errorf("malformed pong payload: %w", err)
		}
		p.server.touchPeer(p.id)
		if payload.Timestamp > 0 {
			sent := time.Unix(0, payload.Timestamp)
			p.server.observeLatency(p.id, time.Since(sent))
		}
		return true, nil
	case MsgTypeHandshake, MsgTypeHandshakeAck:
		return true, nil
	case MsgTypePexRequest:
		var payload PexRequestPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return false, fmt.Errorf("malformed pex request: %w", err)
		}
		if err := p.server.handlePexRequest(p, payload); err != nil {
			return false, fmt.Errorf("handle pex request: %w", err)
		}
		return true, nil
	case MsgTypePexAddresses:
		if p.server.pex == nil {
			return true, nil
		}
		// Addresses are only taken as the reply to a request this node sent.
		// Anything else is refused before its payload is decoded.
		if !p.server.pex.expectingReply(p.id) {
			return false, errUnsolicitedPex
		}
		if len(msg.Payload) > pexMaxAddressesPayload {
			return false, fmt.Errorf("pex addresses payload exceeds %d bytes", pexMaxAddressesPayload)
		}
		var payload PexAddressesPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return false, fmt.Errorf("malformed pex addresses: %w", err)
		}
		p.server.handlePexAddresses(p, payload)
		return true, nil
	default:
		return false, nil
	}
}

func randomUint64() (uint64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(buf[:]), nil
}

// NHB-AUDIT-C7: terminate() and Enqueue() run concurrently from different
// goroutines with no shared lock. Enqueue's own ctx.Done() check and its
// actual send onto p.outbound are two separate, non-atomic steps -- if
// terminate() ran in between them, closing p.outbound, the send would hit
// a closed channel and panic ("send on closed channel"), crashing the
// entire validator process (no recover() exists anywhere in this
// package's call path), not just this one connection. Empirically
// reproduced under ordinary peer churn (228 panics per 200,000 trials in
// a faithful repro), so this needed no attacker, just normal network
// activity. The fix: stop closing p.outbound at all. writeLoop already
// exits via ctx.Done() alone (see its own select below), and Enqueue's
// send-vs-ctx.Done() race is completely safe once the channel is never
// closed -- a send to a still-open, buffered, unclosed channel can only
// succeed or hit the size-limited default branch, never panic; the
// channel and any message stuck in it are simply garbage-collected once
// nothing references this Peer anymore, which Go channels don't require
// an explicit close for.
func (p *Peer) terminate(ban bool, reason error) {
	p.closeOnce.Do(func() {
		p.cancel()
		p.conn.Close()
		close(p.closed)
		p.server.removePeer(p, ban, reason)
	})
}
