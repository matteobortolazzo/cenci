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
	eventReadDeadline  = 5 * time.Second
	eventWriteDeadline = 5 * time.Second
	eventChanCap       = 64
	maxEventConns      = 64
	eventMaxBytes      = 4096
)

// EventReceiver listens on a Unix socket for hook events from cenci notify.
type EventReceiver struct {
	listener     net.Listener
	path         string
	events       chan HookEvent
	pendingClose chan PendingClose
	activeSem    chan struct{}

	// armHandler is the synchronous, injectable babysit-arm handler (#1094).
	// It executes on the connection goroutine, outside any daemon loop, so it
	// must stay pure/bounded over whatever state it closes over. Nil until
	// SetArmHandler is called; a request arriving before then still nacks
	// (fail closed) rather than falling through to Events().
	armHandler func(ArmRequest) ArmResponse
}

// NewEventReceiver creates a receiver listening on the given Unix socket path.
func NewEventReceiver(socketPath string) (*EventReceiver, error) {
	ln, err := safeListen(socketPath)
	if err != nil {
		return nil, err
	}
	return &EventReceiver{
		listener:     ln,
		path:         socketPath,
		events:       make(chan HookEvent, eventChanCap),
		pendingClose: make(chan PendingClose, eventChanCap),
		activeSem:    make(chan struct{}, maxEventConns),
	}, nil
}

// Events returns the channel that delivers parsed hook events.
func (r *EventReceiver) Events() <-chan HookEvent {
	return r.events
}

// PendingCloses returns the channel that delivers pending-close
// registrations sent by SendPendingClose (#522).
func (r *EventReceiver) PendingCloses() <-chan PendingClose {
	return r.pendingClose
}

// SetArmHandler installs the synchronous handler invoked for each incoming
// babysit-arm request (#1094); call before Accept. A request that arrives
// before any handler is installed still gets a nack response rather than
// being silently dropped or routed to Events() (fail closed).
func (r *EventReceiver) SetArmHandler(h func(ArmRequest) ArmResponse) {
	r.armHandler = h
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

	var envelope struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
		log.Printf("event: invalid JSON: %v", err)
		return
	}

	if envelope.Kind == pendingCloseKind {
		var pc PendingClose
		if err := json.Unmarshal(scanner.Bytes(), &pc); err != nil {
			log.Printf("event: invalid pending-close JSON: %v", err)
			return
		}
		select {
		case r.pendingClose <- pc:
		default:
			log.Printf("event: pending-close channel full, dropping %s:%s", pc.Session, pc.WindowIndex)
		}
		return
	}

	if envelope.Kind == armRequestKind {
		r.handleArmRequestConn(conn, scanner.Bytes())
		return
	}

	// Every other kind (including empty/absent, the pre-#522 wire format)
	// routes to the existing hook-event path unchanged.
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

// armRequestUnmarshalNack is the stable nack reason for a babysit-arm
// envelope whose body fails to unmarshal into ArmRequest (#1094).
const armRequestUnmarshalNack = "malformed arm request"

// armRequestNoHandlerNack is the stable nack reason for a babysit-arm
// request arriving before SetArmHandler was ever called (#1094 fail-closed
// contract).
const armRequestNoHandlerNack = "no arm handler installed"

// handleArmRequestConn implements the write-then-close half of the
// babysit-arm contract (#1094): unmarshal the request body, dispatch to the
// injected handler (nacking, fail closed, on a nil handler or an unmarshal
// failure without ever invoking the handler), then write exactly one
// ack-or-nack JSON line under a write deadline before the caller's deferred
// conn.Close() runs.
func (r *EventReceiver) handleArmRequestConn(conn net.Conn, raw []byte) {
	var resp ArmResponse
	var req ArmRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		log.Printf("event: invalid arm-request JSON: %v", err)
		resp = ArmResponse{OK: false, Reason: armRequestUnmarshalNack}
	} else if r.armHandler == nil {
		log.Printf("event: arm request received with no handler installed")
		resp = ArmResponse{OK: false, Reason: armRequestNoHandlerNack}
	} else {
		resp = r.armHandler(req)
	}

	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("event: marshal arm response: %v", err)
		return
	}
	data = append(data, '\n')
	_ = conn.SetWriteDeadline(time.Now().Add(eventWriteDeadline))
	if _, err := conn.Write(data); err != nil {
		log.Printf("event: write arm response: %v", err)
	}
}

// Close shuts down the listener and removes the socket file.
func (r *EventReceiver) Close() error {
	return closeUnixListener(r.listener, r.path)
}
