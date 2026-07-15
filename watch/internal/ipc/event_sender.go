package ipc

import (
	"encoding/json"
	"net"
)

// SendEvent connects to the event socket, writes the event as a JSON line, and disconnects.
func SendEvent(socketPath string, event HookEvent) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = conn.Write(data)
	return err
}
