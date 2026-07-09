package ipc

import (
	"os"
	"testing"
)

func TestCloseUnixListener_RemovesSocketFile(t *testing.T) {
	path := tempSocket(t)

	ln, err := safeListen(path)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the socket file exists before close.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket file should exist before close: %v", err)
	}

	if err := closeUnixListener(ln, path); err != nil {
		t.Fatalf("closeUnixListener() error: %v", err)
	}

	// Verify the socket file has been removed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected socket file to be removed, got stat error: %v", err)
	}
}
