package ticketstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// maxConfigBytes caps how much of `.cenci/config.json` is read. A real config is
// a few hundred bytes; the cap only bounds a hostile or runaway file.
const maxConfigBytes = 64 << 10

// CleanupEnabled reports whether window cleanup is enabled for the repo at
// toplevel, per `windowCleanup.enabled` in its working-tree
// `.cenci/config.json`. A missing file, block or key means enabled; an
// unreadable or malformed config, or a wrongly typed block or key (including a
// JSON null), is an error so the caller can fail open rather than guess.
//
// The config is decoded in two stages through map[string]json.RawMessage so
// keys match exactly: struct-tag decoding would also accept a differently
// cased key (go-gotchas rule 15).
func CleanupEnabled(toplevel string) (bool, error) {
	path := filepath.Join(toplevel, ".cenci", "config.json")
	data, err := readBoundedRegularFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return false, fmt.Errorf("parsing %s: %w", path, err)
	}
	if top == nil {
		return false, fmt.Errorf("parsing %s: top level is null, want an object", path)
	}
	rawBlock, ok := top["windowCleanup"]
	if !ok {
		return true, nil
	}

	var block map[string]json.RawMessage
	if err := json.Unmarshal(rawBlock, &block); err != nil {
		return false, fmt.Errorf("%s: windowCleanup must be an object: %w", path, err)
	}
	if block == nil {
		return false, fmt.Errorf("%s: windowCleanup must be an object, got null", path)
	}
	rawEnabled, ok := block["enabled"]
	if !ok {
		return true, nil
	}

	// A *bool distinguishes JSON null (nil pointer) from false: decoding null
	// into a plain bool is a silent no-op that would read as "disabled"
	// without an error (go-gotchas rule 27).
	var enabled *bool
	if err := json.Unmarshal(rawEnabled, &enabled); err != nil {
		return false, fmt.Errorf("%s: windowCleanup.enabled must be a boolean: %w", path, err)
	}
	if enabled == nil {
		return false, fmt.Errorf("%s: windowCleanup.enabled must be a boolean, got null", path)
	}
	return *enabled, nil
}

// readBoundedRegularFile reads path only when it is a regular file of at most
// maxConfigBytes. The config lives in a working tree, so it may be a committed
// FIFO or a symlink to a device: the open is non-blocking (a FIFO would
// otherwise block until a writer appears), the type is checked on the open
// handle rather than the path (no stat/open race), and the read is capped.
func readBoundedRegularFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only handle: a close error cannot lose data

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file (mode %s)", info.Mode().Type())
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("larger than the %d-byte limit", maxConfigBytes)
	}
	return data, nil
}
