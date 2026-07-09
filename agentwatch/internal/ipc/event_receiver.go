package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"net"
	"time"
)

const (
	eventReadDeadline = 5 * time.Second
	eventChanCap      = 64
	maxEventConns     = 64
	eventMaxBytes     = 4096
)

// EventReceiver listens on a Unix socket for hook events from agentwatch notify.
type EventReceiver struct {
	listener  net.Listener
	path      string
	events    chan HookEvent
	activeSem chan struct{}
}

// NewEventReceiver creates a receiver listening on the given Unix socket path.
func NewEventReceiver(socketPath string) (*EventReceiver, error) {
	ln, err := safeListen(socketPath)
	if err != nil {
		return nil, err
	}
	return &EventReceiver{
		listener:  ln,
		path:      socketPath,
		events:    make(chan HookEvent, eventChanCap),
		activeSem: make(chan struct{}, maxEventConns),
	}, nil
}

// Events returns the channel that delivers parsed hook events.
func (r *EventReceiver) Events() <-chan HookEvent {
	return r.events
}

// Accept accepts connections until ctx is cancelled. Each connection sends one
// JSON line (a HookEvent), which is parsed and sent to the events channel.
func (r *EventReceiver) Accept(ctx context.Context) {
	go func() {
		<-ctx.Done()
		_ = r.listener.Close()
	}()

	for {
		conn, err := r.listener.Accept()
		if err != nil {
			return // listener closed
		}
		select {
		case r.activeSem <- struct{}{}:
		default:
			log.Printf("event: connection limit reached (%d), rejecting", maxEventConns)
			_ = conn.Close()
			continue
		}
		go func() {
			defer func() { <-r.activeSem }()
			r.handleConn(conn)
		}()
	}
}

func (r *EventReceiver) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(eventReadDeadline))

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 512), eventMaxBytes)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			log.Printf("event: read error: %v", err)
		}
		return
	}

	var event HookEvent
	if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
		log.Printf("event: invalid JSON: %v", err)
		return
	}

	select {
	case r.events <- event:
	default:
		log.Printf("event: channel full, dropping %s event", event.EventType)
	}
}

// Close shuts down the listener and removes the socket file.
func (r *EventReceiver) Close() error {
	return closeUnixListener(r.listener, r.path)
}
