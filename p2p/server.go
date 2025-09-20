package p2p

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
)

// Server now holds a MessageHandler interface, not a concrete Node.
type Server struct {
	listenAddr string
	handler    MessageHandler
	peers      map[net.Conn]bool
	mu         sync.RWMutex
}

func NewServer(listenAddr string, handler MessageHandler) *Server {
	return &Server{
		listenAddr: listenAddr,
		handler:    handler,
		peers:      make(map[net.Conn]bool),
	}
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return err
	}
	fmt.Printf("P2P server listening on %s\n", s.listenAddr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go s.handleConnection(conn)
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	s.mu.Lock()
	s.peers[conn] = true
	s.mu.Unlock()
	fmt.Printf("New peer connected: %s\n", conn.RemoteAddr())

	defer func() {
		s.mu.Lock()
		delete(s.peers, conn)
		s.mu.Unlock()
		conn.Close()
	}()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var msg Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		// The server's only job is to forward the raw message to the handler.
		if err := s.handler.HandleMessage(&msg); err != nil {
			fmt.Printf("Error handling message: %v\n", err)
		}
	}
}

func (s *Server) Connect(addr string) error {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Printf("Connected to peer: %s\n", addr)
	go s.handleConnection(conn)
	return nil
}

func (s *Server) send(conn net.Conn, msg *Message) error {
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(msgBytes, '\n'))
	return err
}

func (s *Server) Broadcast(msg *Message) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for conn := range s.peers {
		if err := s.send(conn, msg); err != nil {
		}
	}
	return nil
}
