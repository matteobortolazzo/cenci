package ipc

import (
	"encoding/json"
	"net"
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
