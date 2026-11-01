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

	// babysitStopHandler and babysitStatusHandler are the synchronous,
	// injectable handlers for the babysit-stop / babysit-status kinds (#1390),
	// run on the connection goroutine like armHandler. Nil until the matching
	// setter is called; a request arriving before then nacks (fail closed).
	babysitStopHandler   func(BabysitStopRequest) BabysitStopResponse
	babysitStatusHandler func(BabysitStatusRequest) BabysitStatusResponse
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

// SetBabysitStopHandler installs the synchronous handler invoked for each
// incoming babysit-stop request (#1390); call before Accept. A request that
// arrives before any handler is installed still gets a nack.
func (r *EventReceiver) SetBabysitStopHandler(h func(BabysitStopRequest) BabysitStopResponse) {
	r.babysitStopHandler = h
}

// SetBabysitStatusHandler installs the synchronous handler invoked for each
// incoming babysit-status request (#1390); call before Accept.
func (r *EventReceiver) SetBabysitStatusHandler(h func(BabysitStatusRequest) BabysitStatusResponse) {
	r.babysitStatusHandler = h
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

	if envelope.Kind == babysitStopKind {
		r.handleBabysitStopConn(conn, scanner.Bytes())
		return
	}

	if envelope.Kind == babysitStatusKind {
		r.handleBabysitStatusConn(conn, scanner.Bytes())
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

	writeResponseLine(conn, "arm", resp)
}

// Stable nack reasons for the babysit-stop / babysit-status envelopes (#1390),
// mirroring the arm ones above. They carry no host detail.
const (
	babysitStopUnmarshalNack   = "malformed babysit-stop request"
	babysitStopNoHandlerNack   = "no babysit-stop handler installed"
	babysitStatusUnmarshalNack = "malformed babysit-status request"
	babysitStatusNoHandlerNack = "no babysit-status handler installed"
)

// handleBabysitStopConn is handleArmRequestConn's counterpart for the
// babysit-stop kind (#1390): fail closed on a malformed body or a missing
// handler without invoking anything, otherwise run the handler, then write
// exactly one reply line before the caller closes the connection.
func (r *EventReceiver) handleBabysitStopConn(conn net.Conn, raw []byte) {
	var resp BabysitStopResponse
	var req BabysitStopRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		log.Printf("event: invalid babysit-stop JSON: %v", err)
		resp = BabysitStopResponse{OK: false, Reason: babysitStopUnmarshalNack}
	} else if r.babysitStopHandler == nil {
		log.Printf("event: babysit-stop request received with no handler installed")
		resp = BabysitStopResponse{OK: false, Reason: babysitStopNoHandlerNack}
	} else {
		resp = r.babysitStopHandler(req)
	}
	writeResponseLine(conn, "babysit-stop", resp)
}

// handleBabysitStatusConn is the babysit-status counterpart (#1390).
func (r *EventReceiver) handleBabysitStatusConn(conn net.Conn, raw []byte) {
	var resp BabysitStatusResponse
	var req BabysitStatusRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		log.Printf("event: invalid babysit-status JSON: %v", err)
		resp = BabysitStatusResponse{OK: false, Reason: babysitStatusUnmarshalNack}
	} else if r.babysitStatusHandler == nil {
		log.Printf("event: babysit-status request received with no handler installed")
		resp = BabysitStatusResponse{OK: false, Reason: babysitStatusNoHandlerNack}
	} else {
		resp = r.babysitStatusHandler(req)
	}
	writeResponseLine(conn, "babysit-status", resp)
}

// writeResponseLine marshals resp and writes it as one newline-terminated
// line under eventWriteDeadline; what names the kind in log lines.
func writeResponseLine(conn net.Conn, what string, resp any) {
	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("event: marshal %s response: %v", what, err)
		return
	}
	data = append(data, '\n')
	_ = conn.SetWriteDeadline(time.Now().Add(eventWriteDeadline))
	if _, err := conn.Write(data); err != nil {
		log.Printf("event: write %s response: %v", what, err)
	}
}

// Close shuts down the listener and removes the socket file.
func (r *EventReceiver) Close() error {
	return closeUnixListener(r.listener, r.path)
}
