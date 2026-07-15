package watch

import (
	"bufio"
	"encoding/json"
	"net"
)

// snapshotMaxBytes bounds the size of a single StateSnapshot JSON line so a
// malformed or oversized stream cannot exhaust memory.
const snapshotMaxBytes = 65536 // max size of a single StateSnapshot JSON line

// Client is a streaming subscriber to a cenci daemon. It reads NDJSON
// StateSnapshot lines from a Unix socket connection. A Client is not safe for
// concurrent use; call ReadSnapshot from a single goroutine.
type Client struct {
	conn    net.Conn
	scanner *bufio.Scanner
}

// Dial connects to the cenci broadcast socket at the given path, typically
// the value returned by DefaultSocketPath. It returns an error if the socket
// does not exist or the daemon is not accepting connections.
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

// ReadSnapshot reads and decodes the next NDJSON line as a StateSnapshot. It
// blocks until the daemon sends the next snapshot. When the daemon closes the
// connection cleanly it returns net.ErrClosed; any other read or decode failure
// is returned as-is.
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

// Close closes the underlying connection. After Close, a blocked or subsequent
// ReadSnapshot returns an error.
func (c *Client) Close() error {
	return c.conn.Close()
}
