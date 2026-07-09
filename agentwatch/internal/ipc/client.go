package ipc

import (
	"bufio"
	"encoding/json"
	"net"
)

const snapshotMaxBytes = 65536 // max size of a single StateSnapshot JSON line

// Client connects to the IPC server and reads state snapshots.
type Client struct {
	conn    net.Conn
	scanner *bufio.Scanner
}

// Dial connects to the Unix socket at the given path.
func Dial(socketPath string) (*Client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, err
	}
	s := bufio.NewScanner(conn)
	s.Buffer(make([]byte, 4096), snapshotMaxBytes)
	return &Client{
		conn:    conn,
		scanner: s,
	}, nil
}

// ReadSnapshot reads and decodes the next NDJSON line as a StateSnapshot.
func (c *Client) ReadSnapshot() (*StateSnapshot, error) {
	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return nil, err
		}
		return nil, net.ErrClosed
	}
	var snap StateSnapshot
	if err := json.Unmarshal(c.scanner.Bytes(), &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// Close closes the connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
