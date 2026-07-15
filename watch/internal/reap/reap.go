// Package reap defines the seam the daemon uses to trigger the sandbox
// orphan reaper (`cenci sandbox reap-orphans`) after detecting a pane-gone
// tmux-backed session, and once at daemon startup (#292). All container
// knowledge (naming, docker/podman detection, exec plumbing, kill semantics)
// stays in internal/sandbox/launcher; this package only knows how to fire a
// single-flight, non-blocking, non-fatal invocation.
package reap

import (
	"log"
	"os"
	"os/exec"
	"sync/atomic"
)

// Reaper is the seam the daemon injects; ExecReaper is the production
// implementation and mockReaper (in internal/daemon tests) the test double.
type Reaper interface {
	Reap()
}

// ExecReaper self-execs `cenci sandbox reap-orphans` asynchronously, with a
// single-flight guard so concurrent Reap() calls coalesce into one run.
type ExecReaper struct {
	verbose bool
	running atomic.Bool
	// run is injectable for tests; defaults to re-invoking this very binary
	// (os.Executable) as `<self> sandbox reap-orphans`. A subprocess (rather
	// than calling launcher.ReapOrphans in-process) keeps the daemon isolated
	// from any crash in the reap pass and preserves the single-flight
	// semantics across the process boundary.
	run func() error
}

var _ Reaper = (*ExecReaper)(nil)

// NewExecReaper creates an ExecReaper with the default run implementation.
func NewExecReaper(verbose bool) *ExecReaper {
	return &ExecReaper{
		verbose: verbose,
		run: func() error {
			self, err := os.Executable()
			if err != nil {
				return err
			}
			return exec.Command(self, "sandbox", "reap-orphans").Run()
		},
	}
}

// Reap triggers a reap pass in the background and returns immediately. A
// second call while one is already in flight is coalesced (no-op) — the
// in-flight run already covers the current state. Failures (unresolvable
// binary, non-zero exit) are logged (verbose-only) and dropped, never retried
// automatically; the next pane-gone event or daemon restart retries
// naturally.
func (r *ExecReaper) Reap() {
	if !r.running.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer r.running.Store(false)
		if err := r.run(); err != nil {
			if r.verbose {
				log.Printf("reap: cenci sandbox reap-orphans failed: %v", err)
			}
			return
		}
		if r.verbose {
			log.Printf("reap: cenci sandbox reap-orphans completed")
		}
	}()
}
