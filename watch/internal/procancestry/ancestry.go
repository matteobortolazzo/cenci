// Package procancestry reads the process chain of a hook (`cenci notify`) with
// one bounded `ps` call and classifies its nearest Codex ancestor (#1530). Only
// pids and one single-client boolean ever leave the package: a command line is
// read here and dropped. It imports only the standard library.
package procancestry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxDepth is the most chain entries (the hook itself included) accepted as
	// proof; a deeper chain is an error, never a truncated proof.
	MaxDepth = 32
	// ReadTimeout bounds the whole `ps` read.
	ReadTimeout = time.Second
	// MaxOutputBytes caps the `ps` output the reader accepts.
	MaxOutputBytes = 1 << 20

	// waitDelay bounds how long the read waits for `ps` to release its output
	// after the context ends, so a stuck process cannot hold the hook past its
	// manifest timeout (watch/docs/go-gotchas.md).
	waitDelay = 500 * time.Millisecond
	// codexBinary is the argv[0] basename of the native Codex binary.
	codexBinary = "codex"
)

// Typed failures of a read. Every one means "no proof".
var (
	ErrTimeout         = errors.New("process ancestry read timed out")
	ErrTooDeep         = errors.New("process ancestry is deeper than the depth cap")
	ErrCycle           = errors.New("process ancestry has a cycle")
	ErrSelfNotListed   = errors.New("the hook process is not in the ps listing")
	ErrParentNotListed = errors.New("a parent process is not in the ps listing")
	ErrOutputTooLarge  = errors.New("ps output exceeds the size cap")
	ErrMalformed       = errors.New("ps output is malformed")
)

// sharedServerMarkers identifies the shared Codex app-server: the classification
// looks only at the nearest Codex ancestor, and that ancestor is the shared
// server when its command line contains an entry. Which ancestor the shared-server
// marker identifies must be settled by the native record in
// watch/docs/codex-tmux-association.md before its "Marker identifying the shared
// app-server" row is filled. The table stays empty until then, because a guessed
// marker that misses the shared server would bring back the cross-association of
// simultaneous clients.
var sharedServerMarkers []string

// Options injects the seams of ReadWith.
type Options struct {
	// Self is the hook's own pid, the first chain entry.
	Self int
	// Run returns the raw `ps -A -o pid= -o ppid= -o args=` output.
	Run func(ctx context.Context) ([]byte, error)
	// Markers overrides the production marker table when non-empty.
	Markers []string
}

// Result is what a successful read yields.
type Result struct {
	// PIDs is the chain as decimal pids, the hook first, ending at pid 1 (or at
	// the first process whose parent is 0).
	PIDs []string
	// SingleClient is true only when a nearest Codex ancestor exists, the marker
	// table is non-empty and that ancestor matches no marker.
	SingleClient bool
}

// proc is one `ps` row: its parent and command line.
type proc struct {
	ppid int
	args string
}

// Read reads the current process's ancestry with the real `ps`.
func Read(ctx context.Context) (Result, error) {
	return ReadWith(ctx, Options{Self: os.Getpid(), Run: runPS})
}

// ReadWith is Read with injected seams.
func ReadWith(ctx context.Context, opts Options) (Result, error) {
	out, err := opts.Run(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, exec.ErrWaitDelay) || ctx.Err() == context.DeadlineExceeded {
			return Result{}, fmt.Errorf("%w: %w", ErrTimeout, err)
		}
		return Result{}, fmt.Errorf("ps: %w", err)
	}
	if len(out) > MaxOutputBytes {
		return Result{}, ErrOutputTooLarge
	}
	procs, err := parse(out)
	if err != nil {
		return Result{}, err
	}
	chain, err := walk(procs, opts.Self)
	if err != nil {
		return Result{}, err
	}
	markers := opts.Markers
	if len(markers) == 0 {
		markers = sharedServerMarkers
	}
	pids := make([]string, len(chain))
	for i, pid := range chain {
		pids[i] = strconv.Itoa(pid)
	}
	return Result{PIDs: pids, SingleClient: singleClient(procs, chain, markers)}, nil
}

// runPS runs the real `ps`, bounded by ctx, a WaitDelay and the output cap.
func runPS(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=", "-o", "ppid=", "-o", "args=")
	cmd.WaitDelay = waitDelay
	out := &capWriter{limit: MaxOutputBytes}
	cmd.Stdout = out
	err := cmd.Run()
	if out.over {
		return out.buf.Bytes(), nil
	}
	if err != nil {
		// ctx.Err() is read before any deferred cancel and joined in: a deadline
		// kill surfaces as a bare "signal: killed" otherwise.
		return nil, errors.Join(err, ctx.Err())
	}
	return out.buf.Bytes(), nil
}

// capWriter buffers up to limit bytes. A write past the cap fails, which stops
// the copy from `ps`; over reports it and buf then holds more than limit bytes
// so the caller's size check rejects the read.
type capWriter struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.limit {
		w.over = true
		w.buf.Write(p[:min(len(p), w.limit+1-w.buf.Len())])
		return 0, ErrOutputTooLarge
	}
	return w.buf.Write(p)
}

// parse reads `pid ppid args` rows. A row needs two non-negative decimal pids;
// its args may hold anything. A malformed row anywhere fails the whole read, and
// so does a pid listed twice.
func parse(out []byte) (map[int]proc, error) {
	procs := make(map[int]proc)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2 {
			return nil, ErrMalformed
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid < 0 {
			return nil, ErrMalformed
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid < 0 {
			return nil, ErrMalformed
		}
		if _, dup := procs[pid]; dup {
			return nil, ErrMalformed
		}
		rest := strings.TrimLeft(line, " \t")
		rest = strings.TrimLeft(rest[len(fields[0]):], " \t")
		procs[pid] = proc{ppid: ppid, args: strings.TrimSpace(rest[len(fields[1]):])}
	}
	return procs, nil
}

// walk returns the pids from self up to pid 1, or up to the first process whose
// parent is 0.
func walk(procs map[int]proc, self int) ([]int, error) {
	if _, ok := procs[self]; !ok {
		return nil, ErrSelfNotListed
	}
	seen := make(map[int]bool)
	var chain []int
	for pid := self; ; {
		if seen[pid] {
			return nil, ErrCycle
		}
		seen[pid] = true
		chain = append(chain, pid)
		if len(chain) > MaxDepth {
			return nil, ErrTooDeep
		}
		ppid := procs[pid].ppid
		if pid == 1 || ppid == 0 {
			return chain, nil
		}
		if _, ok := procs[ppid]; !ok {
			return nil, ErrParentNotListed
		}
		pid = ppid
	}
}

// singleClient reports whether the nearest Codex ancestor above the hook exists
// and matches no marker. With no markers nothing can tell a single client from
// the shared server, so it is never a single client.
func singleClient(procs map[int]proc, chain []int, markers []string) bool {
	if len(markers) == 0 {
		return false
	}
	for _, pid := range chain[1:] {
		args := procs[pid].args
		if !isCodex(args) {
			continue
		}
		for _, marker := range markers {
			if strings.Contains(args, marker) {
				return false
			}
		}
		return true
	}
	return false
}

// isCodex reports whether argv[0] of a command line has the basename "codex".
func isCodex(args string) bool {
	argv0, _, _ := strings.Cut(args, " ")
	return filepath.Base(argv0) == codexBinary
}
