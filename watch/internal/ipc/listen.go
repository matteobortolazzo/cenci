package ipc

import (
	"net"
	"os"
)

// closeUnixListener closes the listener and removes the socket file.
// The socket file is removed unconditionally (best-effort) regardless of
// whether the listener close succeeds. Returns the listener close error.
func closeUnixListener(ln net.Listener, path string) error {
	err := ln.Close()
	_ = os.Remove(path)
	return err
}
