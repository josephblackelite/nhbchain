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
}

func newPeer(id string, clientVersion string, conn net.Conn, reader *bufio.Reader, server *Server, inbound bool, persistent bool, dialAddr string) *Peer {
	ctx, cancel := context.WithCancel(context.Background())
	rate := server.ratePerPeer
	burst := server.rateBurst
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
	}
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
		if len(trimmed) > p.server.cfg.MaxMessageBytes {
			p.server.handleProtocolViolation(p, fmt.Errorf("message exceeds max size (%d bytes)", len(trimmed)))
			return
		}

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

		if err := p.server.handler.HandleMessage(&msg); err != nil {
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
		if err := p.Enqueue(pong); err != nil {
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

func (p *Peer) terminate(ban bool, reason error) {
	p.closeOnce.Do(func() {
		p.cancel()
		p.conn.Close()
		close(p.outbound)
		close(p.closed)
		p.server.removePeer(p, ban, reason)
	})
}
