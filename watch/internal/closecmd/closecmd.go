// Package closecmd implements `cenci close`: resolving a ticket number
// or window name to the exact tmux windows the daemon's registry knows
// about, and killing the non-busy ones. The daemon-owned snapshot is the
// single source of truth for session/window location, so this package never
// re-derives a tmux target itself the way lazyboards' own cleanup config
// used to (a bare window name only resolves within the caller's own tmux
// session — see ticket #314).
package closecmd

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

// Action describes what Run decided to do about one matched window.
type Action string

const (
	ActionClosed         Action = "closed"          // window was killed
	ActionWouldClose     Action = "would-close"     // --dry-run: window would be killed
	ActionSkippedBusy    Action = "skipped-busy"    // running/need-input and --force not set
	ActionSkippedBabysit Action = "skipped-babysit" // a live babysit supervisor still owns the ticket's PR (#787)
)

// Decision records the outcome for one matched window.
type Decision struct {
	Window watch.WindowState
	Action Action
	// Reason carries the guard's human-readable explanation for an
	// ActionSkippedBabysit decision (e.g. "babysit supervising PR #790, CI
	// not green"). Empty for every other action, whose rendering is derived
	// from Window alone (#787).
	Reason string
}

// windowKiller is the minimal seam Run needs to kill a tmux window.
// Production wiring is (*tmux.ExecClient).KillWindow. Kept as a small
// consumer-defined interface rather than growing tmux.Client, mirroring the
// launcher-facing-method precedent documented on tmux.ExecClient.
type windowKiller interface {
	KillWindow(target string) error
}

// Opts holds the resolved options for one close invocation.
type Opts struct {
	// Target is the ticket number or exact window name to match.
	Target string
	// Force closes windows even if their status is running/need-input.
	Force bool
	// DryRun reports decisions without killing any window.
	DryRun bool
	// SocketPath is the cenci broadcast socket to read a snapshot from.
	SocketPath string
	// ReadSnapshot performs a one-shot daemon snapshot read. Production:
	// dispatch.ReadSnapshot. Injected so tests can simulate daemon-down /
	// daemon-up without a real socket.
	ReadSnapshot func(socketPath string) (*watch.StateSnapshot, error)
	// Killer kills a resolved tmux window target. Production:
	// (*tmux.ExecClient).KillWindow.
	Killer windowKiller
	// Register, if non-nil, registers a busy-skipped window with the daemon
	// as pending-close so the daemon retries the close itself at the
	// session's SessionEnd (#522). Production: a closure wrapping
	// ipc.SendPendingClose against ipc.DefaultEventSocketPath(). Called only
	// for busy-skip decisions and never under --dry-run/--force. Best-effort:
	// a Register error never fails Run or changes rendered decisions, since
	// the snapshot read already succeeded.
	Register func(watch.WindowState) error
	// Guard, if non-nil, reports whether a window's ticket is still owned by
	// a live `cenci babysit` supervisor whose PR has not gone green, plus a
	// reason for the skip line (#787). Production: a closure over
	// babysit.BlocksClose bound to the caller's repo root. Injected like
	// ReadSnapshot/Killer/Register so this package's tests stay
	// filesystem-free. It is consulted only for windows whose name carries a
	// ticket number and never under --force; it must fail open (report
	// false) on any error, and must make no network calls — lazyboards runs
	// `cenci close` on every board refresh.
	Guard func(ticket string) (bool, string)
}

// Run reads a one-shot snapshot from the daemon, matches Target against the
// window registry, and closes every non-busy match (or every match, with
// Force). A daemon read failure is a hard fail-safe: Run returns before any
// matching or killing happens, so a stopped or unreachable daemon results in
// zero tmux calls. Individual kill failures are collected rather than
// aborting the rest of the matches (mirrors the per-repo best-effort
// collection in internal/dispatch/collect.go's CollectTickets).
func Run(opts Opts) ([]Decision, error) {
	snap, err := opts.ReadSnapshot(opts.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("reading cenci snapshot: %w", err)
	}

	matches := matchWindows(opts.Target, snap)

	var decisions []Decision
	var errs []error
	for _, w := range matches {
		// Status values are compared against detect.Status.String() output,
		// never a literal copied from a doc comment: pkg/watch's WindowState
		// doc comment still (incorrectly) says "need_input", but the daemon
		// actually serializes detect.StatusNeedInput.String(), which is
		// hyphenated "need-input".
		busy := w.Status == detect.StatusRunning.String() || w.Status == detect.StatusNeedInput.String()
		if busy && !opts.Force {
			decisions = append(decisions, Decision{Window: w, Action: ActionSkippedBusy})
			// Registration is best-effort and only meaningful outside
			// --dry-run (a dry run never records daemon-side state) and
			// never reached under --force (handled above by the busy
			// guard). A failed registration must not fail Run — the
			// snapshot read already succeeded and rendered decisions must
			// stand regardless (#522).
			if !opts.DryRun && opts.Register != nil {
				_ = opts.Register(w)
			}
			continue
		}
		// A window whose agent has already gone idle can still be the
		// working window of a PR `cenci babysit` is supervising — Phase 9
		// arms the supervisor as its last act, so the session ends while CI
		// is still running (#787). Unlike a busy skip this registers no
		// pending-close: the guard is tied to the PR's lifetime, not the
		// session's, and the daemon re-checks the guard on its own deferred
		// closes.
		if opts.Guard != nil && !opts.Force {
			if ticket := detect.TicketFromWindowName(w.WindowName); ticket != "" {
				if blocked, reason := opts.Guard(ticket); blocked {
					decisions = append(decisions, Decision{Window: w, Action: ActionSkippedBabysit, Reason: reason})
					continue
				}
			}
		}
		if opts.DryRun {
			decisions = append(decisions, Decision{Window: w, Action: ActionWouldClose})
			continue
		}
		// "=" forces an exact session-name match so a target never
		// accidentally resolves against a same-prefixed session.
		target := fmt.Sprintf("=%s:%s", w.Session, w.WindowIndex)
		if kerr := opts.Killer.KillWindow(target); kerr != nil {
			errs = append(errs, fmt.Errorf("killing %s (%s): %w", w.WindowName, target, kerr))
			continue
		}
		decisions = append(decisions, Decision{Window: w, Action: ActionClosed})
	}
	return decisions, errors.Join(errs...)
}

// matchWindows returns every window in snap matching target. A numeric
// target matches windows named exactly the number or prefixed "<number>-"
// (the "<ticket>-<skill>" naming convention; see pkg/watch.WindowState's
// WindowName doc). The prefix check requires the separating hyphen so "42"
// never matches "420-x" (generalizes matchingWindow in
// internal/dispatch/reconcile.go, which returns only the first match, to
// return every match). A non-numeric target matches windows with that exact
// WindowName.
func matchWindows(target string, snap *watch.StateSnapshot) []watch.WindowState {
	if snap == nil {
		return nil
	}
	var out []watch.WindowState
	if n, err := strconv.Atoi(target); err == nil {
		prefix := strconv.Itoa(n)
		for _, w := range snap.Windows {
			if w.WindowName == prefix || strings.HasPrefix(w.WindowName, prefix+"-") {
				out = append(out, w)
			}
		}
		return out
	}
	for _, w := range snap.Windows {
		if w.WindowName == target {
			out = append(out, w)
		}
	}
	return out
}
