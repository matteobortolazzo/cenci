package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// SendEvent connects to the event socket, writes the event as a JSON line, and disconnects.
func SendEvent(socketPath string, event HookEvent) error {
	return sendLine(socketPath, event)
}

// pendingCloseWire is the on-wire envelope for a PendingClose message: the
// "kind" discriminator field plus the promoted PendingClose fields. Kept
// separate from PendingClose itself so the in-memory type used by
// PendingCloses()/registerPendingClose stays a plain value comparable with
// ==, with no wire-only field leaking into daemon/test code.
type pendingCloseWire struct {
	Kind string `json:"kind"`
	PendingClose
}

// SendPendingClose connects to the event socket, writes pc as a JSON line
// tagged with the "pending-close" kind discriminator, and disconnects. It
// mirrors SendEvent's fire-and-forget pattern into the same socket (#522).
func SendPendingClose(socketPath string, pc PendingClose) error {
	return sendLine(socketPath, pendingCloseWire{Kind: pendingCloseKind, PendingClose: pc})
}

// armRequestWire is the on-wire envelope for an ArmRequest: the "kind"
// discriminator field plus the promoted ArmRequest fields, mirroring
// pendingCloseWire (#1094).
type armRequestWire struct {
	Kind string `json:"kind"`
	ArmRequest
}

// armResponseDeadline bounds how long SendArmRequest waits for the daemon's
// response line before giving up (#1094 auto-adopted answer #7). The
// request/response both fit inside the shared eventMaxBytes frame, so no
// framing change is needed.
const armResponseDeadline = 5 * time.Second

// ErrArmUndelivered classifies a SendArmRequest failure where the request
// itself was never written to the wire -- a dial failure (no socket, no
// daemon). Nothing was written, so nothing can have spawned: the babysit
// client maps this to "not armed", not "arm status unknown" (#1094
// auto-adopted answer #8).
var ErrArmUndelivered = errors.New("arm request could not be delivered")

// ErrArmNoResponse classifies every SendArmRequest failure where the request
// was written but no usable response line came back: a write failure, a
// clean EOF with no response, a read-deadline timeout, or an unparseable
// response. The babysit client maps this to "arm status unknown", distinct
// from a nacked "not armed" (#1094 auto-adopted answer #8).
var ErrArmNoResponse = errors.New("arm request sent but no response received")

// SendArmRequest connects to the event socket, writes req as a JSON line
// tagged with the babysit-arm kind discriminator, and reads back the
// daemon's single ack-or-nack response line under armResponseDeadline
// (#1094). Errors are classified into ErrArmUndelivered (dial failure) or
// ErrArmNoResponse (every other failure mode), per the ticket's auto-adopted
// answer #8.
func SendArmRequest(socketPath string, req ArmRequest) (ArmResponse, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return ArmResponse{}, fmt.Errorf("%w: %v", ErrArmUndelivered, err)
	}
	defer func() { _ = conn.Close() }()

	data, err := json.Marshal(armRequestWire{Kind: armRequestKind, ArmRequest: req})
	if err != nil {
		// A marshal failure happens strictly before any write to the wire, so
		// per ErrArmUndelivered's documented contract ("the request itself
		// was never written to the wire") this classifies as undelivered, not
		// as ErrArmNoResponse. Currently unreachable -- no ArmRequest field
		// can fail to marshal today -- but classified correctly for when one
		// might.
		return ArmResponse{}, fmt.Errorf("%w: marshal request: %v", ErrArmUndelivered, err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return ArmResponse{}, fmt.Errorf("%w: write request: %v", ErrArmNoResponse, err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(armResponseDeadline)); err != nil {
		return ArmResponse{}, fmt.Errorf("%w: set read deadline: %v", ErrArmNoResponse, err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return ArmResponse{}, fmt.Errorf("%w: read response: %v", ErrArmNoResponse, err)
	}

	var resp ArmResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
		return ArmResponse{}, fmt.Errorf("%w: unmarshal response: %v", ErrArmNoResponse, err)
	}
	return resp, nil
}

func sendLine(socketPath string, v any) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = conn.Write(data)
	return err
}
