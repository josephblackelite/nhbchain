package p2p

import (
	"bufio"
	"bytes"
	"context"
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
	id         string
	conn       net.Conn
	reader     *bufio.Reader
	outbound   chan *Message
	server     *Server
	remoteAddr string
	dialAddr   string
	inbound    bool
	persistent bool

	limiter *tokenBucket

	ctx    context.Context
	cancel context.CancelFunc

	closeOnce sync.Once
	closed    chan struct{}
}

func newPeer(id string, conn net.Conn, reader *bufio.Reader, server *Server, inbound bool, persistent bool, dialAddr string) *Peer {
	ctx, cancel := context.WithCancel(context.Background())
	burst := server.cfg.MaxMsgsPerSecond * 2
	if burst < 1 {
		burst = 1
	}
	limiter := newTokenBucket(server.cfg.MaxMsgsPerSecond, burst)
	dialAddr = strings.TrimSpace(dialAddr)
	return &Peer{
		id:         id,
		conn:       conn,
		reader:     reader,
		outbound:   make(chan *Message, outboundQueueSize),
		server:     server,
		remoteAddr: conn.RemoteAddr().String(),
		dialAddr:   dialAddr,
		inbound:    inbound,
		persistent: persistent,
		limiter:    limiter,
		ctx:        ctx,
		cancel:     cancel,
		closed:     make(chan struct{}),
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
	return err
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
