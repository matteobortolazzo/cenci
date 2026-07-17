package watch

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"syscall"
)

// snapshotMaxBytes bounds the size of a single StateSnapshot JSON line so a
// malformed or oversized stream cannot exhaust memory.
const snapshotMaxBytes = 65536 // max size of a single StateSnapshot JSON line

// ErrDaemonUnreachable wraps a Dial failure (socket missing or connection
// refused): the daemon itself was never reached. Callers use errors.Is to
// distinguish this from a post-dial failure (permission denied, corrupt
// NDJSON, decode error, mid-read I/O error) where the daemon was reached but
// something else went wrong.
var ErrDaemonUnreachable = fmt.Errorf("daemon unreachable")

// Client is a streaming subscriber to a cenci daemon. It reads NDJSON
// StateSnapshot lines from a Unix socket connection. A Client is not safe for
// concurrent use; call ReadSnapshot from a single goroutine.
type Client struct {
	conn    net.Conn
	scanner *bufio.Scanner
}

// Dial connects to the cenci broadcast socket at the given path, typically
// the value returned by DefaultSocketPath. It returns an error if the socket
// does not exist or the daemon is not accepting connections. A missing
// socket file or a refused connection means the daemon genuinely is not
// running, so that failure is wrapped in ErrDaemonUnreachable (detectable
// via errors.Is). Any other dial failure -- notably permission denied on the
// socket file -- means something is listening but the connection could not
// be established for another reason, so it is returned unwrapped.
func Dial(socketPath string) (*Client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("%w: %v", ErrDaemonUnreachable, err)
		}
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
