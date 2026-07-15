package ipc

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"sync"
)

const maxClients = 16

// Server listens on a Unix socket and broadcasts state snapshots to connected clients.
type Server struct {
	listener  net.Listener
	path      string
	mu        sync.Mutex
	clients   map[net.Conn]struct{}
	lastState []byte // cached JSON+newline of last broadcast
}

// NewServer creates a server listening on the given Unix socket path.
// It removes any stale socket file before binding, rejecting symlinks.
func NewServer(socketPath string) (*Server, error) {
	ln, err := safeListen(socketPath)
	if err != nil {
		return nil, err
	}
	return &Server{
		listener: ln,
		path:     socketPath,
		clients:  make(map[net.Conn]struct{}),
	}, nil
}

// Accept accepts connections until ctx is cancelled. Each new client
// immediately receives the last broadcast snapshot (if any).
func (s *Server) Accept(ctx context.Context) {
	go func() {
		<-ctx.Done()
		_ = s.listener.Close()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// Listener closed (shutdown).
			return
		}

		s.mu.Lock()
		if len(s.clients) >= maxClients {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.clients[conn] = struct{}{}
		// Send cached state to new client immediately.
		if s.lastState != nil {
			_, err := conn.Write(s.lastState)
			if err != nil {
				delete(s.clients, conn)
				_ = conn.Close()
				s.mu.Unlock()
				continue
			}
		}
		s.mu.Unlock()

		// Monitor for client disconnect so dead clients are
		// removed eagerly instead of waiting for the next Broadcast.
		go func() {
			buf := make([]byte, 1)
			_, _ = conn.Read(buf) // blocks until client closes
			s.mu.Lock()
			delete(s.clients, conn)
			s.mu.Unlock()
		}()
	}
}

// Broadcast JSON-encodes the snapshot and writes it as an NDJSON line to all clients.
// Broken clients are removed.
func (s *Server) Broadcast(snap StateSnapshot) {
	data, err := json.Marshal(snap)
	if err != nil {
		log.Printf("ipc: marshal error: %v", err)
		return
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastState = data

	for conn := range s.clients {
		if _, err := conn.Write(data); err != nil {
			_ = conn.Close()
			delete(s.clients, conn)
		}
	}
}

// Close shuts down the listener, closes all clients, and removes the socket file.
func (s *Server) Close() error {
	s.mu.Lock()
	for conn := range s.clients {
		_ = conn.Close()
	}
	s.clients = nil
	s.mu.Unlock()

	return closeUnixListener(s.listener, s.path)
}
