package worktreeclean

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// snapshotTimeout bounds the production daemon-snapshot read: an unreachable
// daemon fails at once, and one that accepts but never speaks must not hang a
// pass.
const snapshotTimeout = 5 * time.Second

// Snapshot statuses the daemon synthesizes for tickets with no live window
// (dispatch's attention overlay); they say nothing about a worktree being in use.
const (
	statusFailed    = "failed"
	statusEscalated = "escalated"
)

// LiveState is what is live right now, read once per pass from tmux, the daemon
// snapshot and the sandbox gate. Guards built from it are pure, so one pass sees
// one consistent picture.
type LiveState struct {
	// WindowTickets holds the tickets with a live tmux window or any tracked
	// non-synthetic snapshot window (a tracked window is renamed to its task, so
	// its name alone does not show the ticket).
	WindowTickets map[int]bool
	// PaneCwds holds the cleaned absolute working directory of every readable pane.
	PaneCwds []string
	// BusyTickets holds the tickets with a running or need-input session.
	BusyTickets map[int]bool
	// WindowsErr is why the tmux windows and pane directories could not be fully
	// read (inside the sandbox, a tmux failure other than "no server running", or
	// a pane directory that is empty, relative or unclean); WindowTickets and
	// PaneCwds then hold only what was readable. SnapshotErr is why the daemon
	// snapshot could not be read.
	WindowsErr, SnapshotErr error
}

// LiveSources are the seams ReadLiveState reads through; a nil field uses the
// production source.
type LiveSources struct {
	// ListPanes defaults to tmux.ListLivePanes.
	ListPanes func() ([]tmux.LivePane, error)
	// ReadSnapshot defaults to one bounded read of the daemon's default socket.
	ReadSnapshot func() (*watch.StateSnapshot, error)
	// InSandbox defaults to CENCI_SANDBOX=1 in the environment.
	InSandbox func() bool
}

// ReadLiveState gathers a LiveState from src. Inside the sandbox tmux is not
// consulted at all: the host's windows are out of reach, so whatever a
// sandbox-local tmux says about "no server" is not an answer.
func ReadLiveState(ctx context.Context, src LiveSources) LiveState {
	if src.ListPanes == nil {
		src.ListPanes = tmux.ListLivePanes
	}
	if src.ReadSnapshot == nil {
		src.ReadSnapshot = func() (*watch.StateSnapshot, error) {
			return ReadSnapshotWithin(watch.DefaultSocketPath(), snapshotTimeout)
		}
	}
	if src.InSandbox == nil {
		src.InSandbox = func() bool { return os.Getenv("CENCI_SANDBOX") == "1" }
	}

	ls := LiveState{WindowTickets: map[int]bool{}, BusyTickets: map[int]bool{}}
	if err := ctx.Err(); err != nil {
		ls.WindowsErr, ls.SnapshotErr = err, err
		return ls
	}

	if src.InSandbox() {
		ls.WindowsErr = errors.New("inside the sandbox the host's tmux windows cannot be read")
	} else {
		ls.readPanes(src.ListPanes)
	}
	ls.readSnapshot(src.ReadSnapshot)
	return ls
}

func (ls *LiveState) readPanes(list func() ([]tmux.LivePane, error)) {
	panes, err := list()
	if err != nil {
		if !errors.Is(err, tmux.ErrNoServer) {
			ls.WindowsErr = fmt.Errorf("read tmux windows: %w", err)
		}
		return
	}
	for _, p := range panes {
		if n := windowTicket(p.WindowName); n > 0 {
			ls.WindowTickets[n] = true
		}
		if !filepath.IsAbs(p.CurrentPath) || filepath.Clean(p.CurrentPath) != p.CurrentPath {
			ls.WindowsErr = fmt.Errorf("tmux pane %s has no usable working directory (%q)", p.PaneID, p.CurrentPath)
			continue
		}
		ls.PaneCwds = append(ls.PaneCwds, p.CurrentPath)
	}
}

func (ls *LiveState) readSnapshot(read func() (*watch.StateSnapshot, error)) {
	snap, err := read()
	if err != nil {
		ls.SnapshotErr = fmt.Errorf("read daemon snapshot: %w", err)
		return
	}
	ls.applySnapshot(snap)
}

// applySnapshot records the window and busy tickets of snap; a nil snap sets
// SnapshotErr.
func (ls *LiveState) applySnapshot(snap *watch.StateSnapshot) {
	if snap == nil {
		ls.SnapshotErr = errors.New("read daemon snapshot: no snapshot")
		return
	}
	for _, w := range snap.Windows {
		n := windowTicket(w.WindowName)
		if n == 0 || w.Status == statusFailed || w.Status == statusEscalated {
			continue
		}
		ls.WindowTickets[n] = true
		if w.Status == detect.StatusRunning.String() || w.Status == detect.StatusNeedInput.String() {
			ls.BusyTickets[n] = true
		}
	}
}

// SnapshotLiveState is the LiveState a daemon snapshot alone yields: no tmux read,
// no sandbox gate. It holds the same window and busy tickets ReadLiveState takes
// from a snapshot (synthetic failed/escalated entries excluded), so the daemon can
// build guards from its own in-memory state without a git, tmux or socket call.
// A nil snapshot sets SnapshotErr.
func SnapshotLiveState(snap *watch.StateSnapshot) LiveState {
	ls := LiveState{WindowTickets: map[int]bool{}, BusyTickets: map[int]bool{}}
	ls.applySnapshot(snap)
	return ls
}

// TargetOf is the Target a guard is asked about for e: the ticket its dir name
// carries (0 when none), the name and the local path.
func TargetOf(e Entry) Target {
	ticket, _ := ticketOf(e.Name)
	return Target{Ticket: ticket, Name: e.Name, LocalPath: e.LocalPath}
}

// windowTicket is the ticket a window name carries, or 0. It reads the number
// the way ticketOf reads a worktree name ("042-x" is ticket 42 for both), so a
// window never fails to match the worktree classify.go attributes to its ticket.
func windowTicket(name string) int {
	n, err := strconv.Atoi(detect.TicketFromWindowName(name))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// InUseGuard blocks a worktree whose ticket has a live window, whose directory
// holds a pane's working directory, or whose ticket has a running or need-input
// session. A worktree without a ticket is matched by directory only. With strict,
// anything in ls.StrictErr (an unreadable window list, or a daemon snapshot that
// failed for a reason other than the daemon not running) is an error and the
// worktree is kept as unknown, ahead of any block; without strict the checks run
// on what was readable.
func InUseGuard(ls LiveState, strict bool) Guard {
	return func(_ context.Context, t Target) (bool, error) {
		if strict {
			if err := ls.StrictErr(); err != nil {
				return false, err
			}
		}
		if t.Ticket > 0 && (ls.WindowTickets[t.Ticket] || ls.BusyTickets[t.Ticket]) {
			return true, nil
		}
		for _, cwd := range ls.PaneCwds {
			if cwd == t.LocalPath || strings.HasPrefix(cwd, t.LocalPath+"/") {
				return true, nil
			}
		}
		return false, nil
	}
}

// BabysitGuard blocks a worktree whose ticket has an armed babysit supervisor,
// as answered by blocks (babysit.BlocksCloseStrict in production). An error from
// blocks is returned, so the worktree is kept as unknown rather than read as
// "not armed". A ticketless worktree is never asked.
func BabysitGuard(blocks func(ticket string) (bool, string, error)) Guard {
	return func(_ context.Context, t Target) (bool, error) {
		if t.Ticket == 0 {
			return false, nil
		}
		blocked, _, err := blocks(strconv.Itoa(t.Ticket))
		return blocked, err
	}
}

// StrictErr is why the in-use status is unknown under strict mode, or nil: the
// tmux windows could not be fully read, or the daemon snapshot failed for any
// reason other than the daemon not running. A daemon that is not running (an
// error wrapping watch.ErrDaemonUnreachable) is benign: there is no snapshot
// to miss, and only tmux is checked. Everything else -- a timeout, a malformed
// or closed reply, permission denied, no snapshot at all -- hides renamed
// tracked windows and busy sessions, so it is an unknown.
func (ls LiveState) StrictErr() error {
	if ls.WindowsErr != nil {
		return ls.WindowsErr
	}
	if ls.SnapshotErr != nil && !errors.Is(ls.SnapshotErr, watch.ErrDaemonUnreachable) {
		return ls.SnapshotErr
	}
	return nil
}

// ReadSnapshotWithin reads one daemon snapshot from the socket at path, giving
// up after timeout. A daemon that is not running wraps watch.ErrDaemonUnreachable.
func ReadSnapshotWithin(path string, timeout time.Duration) (*watch.StateSnapshot, error) {
	client, err := watch.Dial(path)
	if err != nil {
		return nil, err
	}
	// Closing the connection also ends the reading goroutine on a timeout.
	defer func() { _ = client.Close() }()

	type result struct {
		snap *watch.StateSnapshot
		err  error
	}
	done := make(chan result, 1)
	go func() {
		snap, err := client.ReadSnapshot()
		done <- result{snap, err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.snap, r.err
	case <-timer.C:
		return nil, fmt.Errorf("the daemon sent no snapshot within %s", timeout)
	}
}
