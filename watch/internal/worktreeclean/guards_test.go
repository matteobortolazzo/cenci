package worktreeclean

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/babysit"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// Guard tests (#1266). The in-use and babysit guards are driven through the real
// Classify over real-git fixtures, so a block is observed as the keep reason a
// caller would print; only the prefix trap and the snapshot read, which a git
// fixture cannot isolate cheaply, are tested directly.

const (
	liveTicket = 42
	staleIdle  = 30 * 24 * time.Hour
)

// noSandbox clears the ambient CENCI_SANDBOX (set in the dev container), which
// would otherwise make every window check fail closed and the table vacuous.
func noSandbox(t *testing.T) { t.Helper(); t.Setenv("CENCI_SANDBOX", "") }

// idleWorktrees is a repo with a long-idle, clean, pushed worktree per name: with
// no guard blocking, each classifies as stale.
func idleWorktrees(t *testing.T, names ...string) (*fixtureRepo, []*fixtureWT) {
	t.Helper()
	r := newOriginRepo(t)
	wts := make([]*fixtureWT, len(names))
	for i, name := range names {
		wts[i] = atMainWorktree(t, r, name, "branch-"+name)
		ageWorktree(t, wts[i], staleIdle)
	}
	return r, wts
}

func busy(ticket int, status string) watch.WindowState {
	return watch.WindowState{WindowName: fmt.Sprintf("%d-implement", ticket), Status: status}
}

// errDaemonDown is what a daemon that is not running looks like to the snapshot
// read: the one failure that is benign for a strict pass.
var errDaemonDown = fmt.Errorf("%w: dial unix /run/user/1000/cenci/cenci.sock: no such file or directory", watch.ErrDaemonUnreachable)

func TestInUseGuard_ClassifiesWhatIsLive(t *testing.T) {
	noSandbox(t)
	r, wts := idleWorktrees(t, "42-feature")
	w := wts[0]

	tests := []struct {
		name     string
		panes    []tmux.LivePane
		tmuxErr  error
		windows  []watch.WindowState
		snapErr  error
		nilSnap  bool // the read returns (nil, snapErr)
		strict   bool
		sandbox  bool
		want     KeepReason // "" = stale
		wantErr  bool
		wantCall bool // tmux must have been read
	}{
		{name: "nothing live", panes: []tmux.LivePane{{PaneID: "%1", WindowName: "scratch", CurrentPath: "/home/dev"}}, strict: true, wantCall: true},
		{name: "window named for the ticket", panes: []tmux.LivePane{{PaneID: "%1", WindowName: "42-implement", CurrentPath: "/home/dev"}}, want: KeepInUse, wantCall: true},
		{name: "window that is the bare ticket number", panes: []tmux.LivePane{{PaneID: "%1", WindowName: "42", CurrentPath: "/home/dev"}}, want: KeepInUse, wantCall: true},
		{name: "other tickets' windows", panes: []tmux.LivePane{
			{PaneID: "%1", WindowName: "4-implement", CurrentPath: "/home/dev"},
			{PaneID: "%2", WindowName: "420-implement", CurrentPath: "/home/dev"},
			{PaneID: "%4", WindowName: "add-dark-mode", CurrentPath: "/home/dev"},
		}, strict: true, wantCall: true},
		// classify.go reads the worktree "042-x" as ticket 42, so a window "042-x"
		// must count for it too (the safe direction).
		{name: "window with a leading-zero ticket prefix", panes: []tmux.LivePane{{PaneID: "%1", WindowName: "042-implement", CurrentPath: "/home/dev"}}, want: KeepInUse, wantCall: true},
		{name: "pane cwd is the worktree", panes: []tmux.LivePane{{PaneID: "%1", WindowName: "sh", CurrentPath: "{wt}"}}, want: KeepInUse, wantCall: true},
		{name: "pane cwd inside the worktree", panes: []tmux.LivePane{{PaneID: "%1", WindowName: "sh", CurrentPath: "{wt}/pkg/sub"}}, want: KeepInUse, wantCall: true},
		{name: "pane cwd in a sibling that shares the prefix", panes: []tmux.LivePane{{PaneID: "%1", WindowName: "sh", CurrentPath: "{wt}-other/sub"}}, strict: true, wantCall: true},
		{name: "running session", windows: []watch.WindowState{busy(liveTicket, "running")}, strict: true, want: KeepInUse, wantCall: true},
		{name: "need-input session", windows: []watch.WindowState{busy(liveTicket, "need-input")}, strict: true, want: KeepInUse, wantCall: true},
		{name: "tracked done window", windows: []watch.WindowState{busy(liveTicket, "done")}, strict: true, want: KeepInUse, wantCall: true},
		{name: "tracked idle window", windows: []watch.WindowState{busy(liveTicket, "idle")}, strict: true, want: KeepInUse, wantCall: true},
		{name: "tracked stopped window", windows: []watch.WindowState{busy(liveTicket, "stopped")}, strict: true, want: KeepInUse, wantCall: true},
		{name: "synthetic failed entry", windows: []watch.WindowState{busy(liveTicket, "failed")}, strict: true, wantCall: true},
		{name: "synthetic escalated entry", windows: []watch.WindowState{busy(liveTicket, "escalated")}, strict: true, wantCall: true},
		{name: "another ticket's running session", windows: []watch.WindowState{busy(43, "running")}, strict: true, wantCall: true},
		{name: "no tmux server running", tmuxErr: tmux.ErrNoServer, strict: true, wantCall: true},
		{name: "strict and tmux unreadable", tmuxErr: errors.New("error connecting to /tmp/tmux-1000/default"), strict: true, want: KeepInUseUnknown, wantErr: true, wantCall: true},
		{name: "lenient and tmux unreadable skips the window checks", tmuxErr: errors.New("error connecting to /tmp/tmux-1000/default"), wantCall: true},
		{name: "lenient and tmux unreadable still sees a busy session", tmuxErr: errors.New("boom"), windows: []watch.WindowState{busy(liveTicket, "running")}, want: KeepInUse, wantCall: true},
		{name: "empty pane cwd is unreadable", panes: []tmux.LivePane{{PaneID: "%1", WindowName: "sh", CurrentPath: ""}}, strict: true, want: KeepInUseUnknown, wantErr: true, wantCall: true},
		{name: "relative pane cwd is unreadable", panes: []tmux.LivePane{{PaneID: "%1", WindowName: "sh", CurrentPath: "repo/x"}}, strict: true, want: KeepInUseUnknown, wantErr: true, wantCall: true},
		{name: "unclean pane cwd is unreadable", panes: []tmux.LivePane{{PaneID: "%1", WindowName: "sh", CurrentPath: "/a/../b"}}, strict: true, want: KeepInUseUnknown, wantErr: true, wantCall: true},
		{name: "daemon not running and nothing live", snapErr: errDaemonDown, strict: true, wantCall: true},
		{name: "daemon not running but a window is live", snapErr: errDaemonDown, panes: []tmux.LivePane{{PaneID: "%1", WindowName: "42-implement", CurrentPath: "/home/dev"}}, strict: true, want: KeepInUse, wantCall: true},
		// #1300: only a daemon that is not running is benign; any other snapshot
		// failure leaves renamed tracked windows and busy sessions unseen.
		{name: "strict and the snapshot times out", snapErr: errors.New("the daemon sent no snapshot within 5s"), strict: true, want: KeepInUseUnknown, wantErr: true, wantCall: true},
		{name: "strict and the snapshot reply is malformed", snapErr: errors.New("decode snapshot: invalid character 'o' in literal null"), strict: true, want: KeepInUseUnknown, wantErr: true, wantCall: true},
		{name: "strict and the daemon sends no snapshot at all", nilSnap: true, strict: true, want: KeepInUseUnknown, wantErr: true, wantCall: true},
		{name: "strict and the daemon socket is permission denied", snapErr: fmt.Errorf("dial unix /run/user/1000/cenci/cenci.sock: %w", syscall.EACCES), strict: true, want: KeepInUseUnknown, wantErr: true, wantCall: true},
		{name: "strict and a snapshot failure beats a live window", snapErr: errors.New("the daemon sent no snapshot within 5s"), panes: []tmux.LivePane{{PaneID: "%1", WindowName: "42-implement", CurrentPath: "/home/dev"}}, strict: true, want: KeepInUseUnknown, wantErr: true, wantCall: true},
		{name: "lenient and the snapshot reply is malformed still sees a live window", snapErr: errors.New("decode snapshot: invalid character 'o' in literal null"), panes: []tmux.LivePane{{PaneID: "%1", WindowName: "42-implement", CurrentPath: "/home/dev"}}, want: KeepInUse, wantCall: true},
		{name: "lenient and the snapshot times out judges on what it read", snapErr: errors.New("the daemon sent no snapshot within 5s"), wantCall: true},
		{name: "strict inside the sandbox whatever the snapshot says", sandbox: true, strict: true, snapErr: errDaemonDown, want: KeepInUseUnknown, wantErr: true},
		{name: "strict inside the sandbox", sandbox: true, strict: true, want: KeepInUseUnknown, wantErr: true},
		{name: "sandbox beats a benign no-server answer", sandbox: true, strict: true, tmuxErr: tmux.ErrNoServer, want: KeepInUseUnknown, wantErr: true},
		{name: "lenient inside the sandbox skips the window checks", sandbox: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.sandbox {
				t.Setenv("CENCI_SANDBOX", "1")
			} else {
				noSandbox(t)
			}
			lister := &tmuxtest.LivePaneLister{Err: tt.tmuxErr}
			for _, p := range tt.panes {
				p.CurrentPath = strings.ReplaceAll(p.CurrentPath, "{wt}", w.Path)
				lister.Panes = append(lister.Panes, p)
			}
			snap := &watch.StateSnapshot{Windows: tt.windows}
			ls := ReadLiveState(context.Background(), LiveSources{
				ListPanes: lister.ListLivePanes,
				ReadSnapshot: func() (*watch.StateSnapshot, error) {
					if tt.nilSnap {
						return nil, tt.snapErr
					}
					return snap, tt.snapErr
				},
			})
			o := testOptions(nil)
			o.InUse = InUseGuard(ls, tt.strict)

			c := classifyOne(t, r, w, o)

			if tt.want == "" {
				wantVerdict(t, c, VerdictStale, "")
			} else {
				wantVerdict(t, c, VerdictKeep, tt.want)
			}
			if tt.wantErr && c.Err == nil {
				t.Error("Err = nil, want the cause of the unknown keep")
			}
			if tt.wantCall != (lister.Calls > 0) {
				t.Errorf("tmux read %d times, want a read = %v", lister.Calls, tt.wantCall)
			}
		})
	}
}

// ReadLiveState records which source it could not read, so a caller can say so;
// "no server running" is an answer (no windows), not a failure.
func TestReadLiveState_RecordsWhichSourceFailed(t *testing.T) {
	noSandbox(t)
	tests := []struct {
		name                   string
		tmuxErr, snapErr       error
		wantWindows, wantSnaps bool
	}{
		{name: "both readable"},
		{name: "no server running", tmuxErr: tmux.ErrNoServer},
		{name: "tmux failure", tmuxErr: errors.New("boom"), wantWindows: true},
		{name: "snapshot failure", snapErr: errors.New("daemon unreachable"), wantSnaps: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ls := ReadLiveState(context.Background(), LiveSources{
				ListPanes:    (&tmuxtest.LivePaneLister{Err: tt.tmuxErr}).ListLivePanes,
				ReadSnapshot: func() (*watch.StateSnapshot, error) { return &watch.StateSnapshot{}, tt.snapErr },
			})

			if (ls.WindowsErr != nil) != tt.wantWindows || (ls.SnapshotErr != nil) != tt.wantSnaps {
				t.Errorf("WindowsErr = %v, SnapshotErr = %v; want failed windows = %v, snapshot = %v",
					ls.WindowsErr, ls.SnapshotErr, tt.wantWindows, tt.wantSnaps)
			}
		})
	}
}

// Unit test, not a git fixture: a worktree must match its own ticket and path
// exactly. `42-a` and `42-ab` are siblings whose names share a prefix, and a
// pane inside one must not block the other; the same goes for ticket 4 against a
// window of 42 and for a ticketless worktree against the number 0.
func TestInUseGuard_MatchesTicketAndPathExactly(t *testing.T) {
	ls := LiveState{
		WindowTickets: map[int]bool{4: true, 0: true},
		PaneCwds:      []string{"/r/.worktrees/42-ab", "/r/.worktrees/42-ab/sub"},
	}
	tests := []struct {
		target Target
		want   bool
	}{
		{Target{Ticket: 42, Name: "42-a", LocalPath: "/r/.worktrees/42-a"}, false},
		{Target{Ticket: 42, Name: "42-ab", LocalPath: "/r/.worktrees/42-ab"}, true},
		{Target{Ticket: 43, Name: "43-x", LocalPath: "/r/.worktrees/43-x"}, false},
		{Target{Ticket: 4, Name: "4-x", LocalPath: "/r/.worktrees/4-x"}, true},
		{Target{Ticket: 0, Name: "scratch", LocalPath: "/r/.worktrees/scratch"}, false},
	}
	for _, tt := range tests {
		got, err := InUseGuard(ls, true)(context.Background(), tt.target)
		if err != nil || got != tt.want {
			t.Errorf("guard(%s) = %v, %v; want blocked = %v", tt.target.Name, got, err, tt.want)
		}
	}
}

// armBabysit writes a supervisor state the way a babysit that is still arming
// leaves it (PID 0, no live process to check) under a temp XDG_STATE_HOME, for
// the real babysit.BlocksCloseStrict to read.
func armBabysit(t *testing.T, ciStatus string, closing ...int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	dir := filepath.Join(home, "cenci", "babysit")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	issues, err := json.Marshal(closing)
	if err != nil {
		t.Fatal(err)
	}
	state := fmt.Sprintf(`{"schemaVersion":2,"pr":"790","repo":"o/r","agent":"claude","closingIssues":%s,"ciStatus":%q,"status":"arming","pid":0,"updatedAt":%q}`,
		issues, ciStatus, time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(dir, "abc123-790.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
}

func realBlocks(ticket string) (bool, string, error) {
	return babysit.BlocksCloseStrict(ticket, "", "")
}

func TestBabysitGuard_BlocksOnAnArmedSupervisorForTheTicket(t *testing.T) {
	noSandbox(t)
	r, wts := idleWorktrees(t, "42-feature", "scratch")
	feature, scratch := wts[0], wts[1]
	injected := errors.New("babysit state unreadable")

	tests := []struct {
		name     string
		ciStatus string
		closing  []int
		blocks   func(string) (bool, string, error)
		wt       *fixtureWT
		want     KeepReason // "" = stale
		wantErr  error      // c.Err must wrap it
	}{
		{name: "armed supervisor closes the ticket", ciStatus: "pending", closing: []int{liveTicket}, blocks: realBlocks, wt: feature, want: KeepBabysit},
		{name: "supervisor of another ticket", ciStatus: "pending", closing: []int{43}, blocks: realBlocks, wt: feature},
		{name: "supervisor whose CI is green", ciStatus: "green", closing: []int{liveTicket}, blocks: realBlocks, wt: feature},
		{name: "ticketless worktree is never asked", ciStatus: "pending", closing: []int{liveTicket},
			blocks: func(string) (bool, string, error) { return true, "always", nil }, wt: scratch},
		// #1300: a failed lookup is not "not armed".
		{name: "the lookup fails for a ticketed worktree", ciStatus: "pending", closing: []int{liveTicket},
			blocks: func(string) (bool, string, error) { return false, "", injected }, wt: feature, want: KeepBabysitUnknown, wantErr: injected},
		{name: "ticketless worktree is not asked even when the lookup would fail", ciStatus: "pending", closing: []int{liveTicket},
			blocks: func(string) (bool, string, error) { return false, "", injected }, wt: scratch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			armBabysit(t, tt.ciStatus, tt.closing...)
			o := testOptions(nil)
			o.Babysit = BabysitGuard(tt.blocks)

			c := classifyOne(t, r, tt.wt, o)

			if tt.want == "" {
				wantVerdict(t, c, VerdictStale, "")
			} else {
				wantVerdict(t, c, VerdictKeep, tt.want)
			}
			if tt.wantErr != nil && !errors.Is(c.Err, tt.wantErr) {
				t.Errorf("Err = %v, want it to wrap %v", c.Err, tt.wantErr)
			}
		})
	}
}

// With the real BlocksCloseStrict over a state directory holding a corrupt file,
// a ticketed worktree cannot be tied to any supervisor and is kept as unknown; a
// ticketless one is never asked and is judged on its own facts.
func TestBabysitGuard_KeepsATicketedWorktreeWhenTheStateIsCorrupt(t *testing.T) {
	noSandbox(t)
	r, wts := idleWorktrees(t, "42-feature", "scratch")
	feature, scratch := wts[0], wts[1]
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	dir := filepath.Join(home, "cenci", "babysit")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := testOptions(nil)
	o.Babysit = BabysitGuard(realBlocks)

	c := classifyOne(t, r, feature, o)

	wantVerdict(t, c, VerdictKeep, KeepBabysitUnknown)
	if c.Err == nil || !strings.Contains(c.Err.Error(), corrupt) {
		t.Errorf("Err = %v, want the cause to name %s", c.Err, corrupt)
	}
	wantVerdict(t, classifyOne(t, r, scratch, o), VerdictStale, "")
}

// serveUnix listens on a short temp socket path and, for each connection, writes
// payload (when non-empty) and then holds the connection open and silent.
func serveUnix(t *testing.T, payload string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ws")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			if payload != "" {
				_, _ = conn.Write([]byte(payload))
			}
			go func() { <-done; _ = conn.Close() }()
		}
	}()
	return path
}

// Unit tests: the bound on a daemon that accepts but never speaks cannot be
// isolated by a git fixture.
func TestReadSnapshotWithin(t *testing.T) {
	t.Run("a silent daemon gives up at the timeout", func(t *testing.T) {
		path := serveUnix(t, "")

		start := time.Now()
		snap, err := ReadSnapshotWithin(path, 150*time.Millisecond)

		if err == nil {
			t.Fatalf("ReadSnapshotWithin = %+v, nil; want a timeout error", snap)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("returned after %s, want it bounded by the timeout", elapsed)
		}
	})
	t.Run("a speaking daemon's snapshot comes back", func(t *testing.T) {
		path := serveUnix(t, `{"windows":[{"window_name":"42-implement","status":"running"}]}`+"\n")

		snap, err := ReadSnapshotWithin(path, 5*time.Second)

		if err != nil || snap == nil || len(snap.Windows) != 1 || snap.Windows[0].WindowName != "42-implement" {
			t.Errorf("ReadSnapshotWithin = %+v, %v; want the one window", snap, err)
		}
	})
	t.Run("a socket that denies the connection is not reported as unreachable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores socket permissions")
		}
		path := serveUnix(t, "")
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}

		_, err := ReadSnapshotWithin(path, time.Second)

		if err == nil || errors.Is(err, watch.ErrDaemonUnreachable) {
			t.Errorf("err = %v, want a non-nil error that is not watch.ErrDaemonUnreachable", err)
		}
	})
	t.Run("no daemon is reported as unreachable", func(t *testing.T) {
		_, err := ReadSnapshotWithin(filepath.Join(t.TempDir(), "none.sock"), time.Second)

		if !errors.Is(err, watch.ErrDaemonUnreachable) {
			t.Errorf("err = %v, want errors.Is(err, watch.ErrDaemonUnreachable)", err)
		}
	})
}

// Unit test (#1267): the daemon builds its guards from its own snapshot on the
// event loop, without a tmux or socket read. The closed set of synthetic statuses
// (failed, escalated: an attention overlay for tickets with no live window) is an
// edge an integration test would only reach indirectly, so it is pinned here
// against the exact maps the guards consult.
func TestSnapshotLiveState_HoldsTheWindowAndBusyTicketsOfASnapshot(t *testing.T) {
	tests := []struct {
		name        string
		windows     []watch.WindowState
		wantWindows []int
		wantBusy    []int
	}{
		{name: "running session is a window and busy", windows: []watch.WindowState{busy(42, "running")}, wantWindows: []int{42}, wantBusy: []int{42}},
		{name: "need-input session is a window and busy", windows: []watch.WindowState{busy(42, "need-input")}, wantWindows: []int{42}, wantBusy: []int{42}},
		{name: "done, idle and stopped windows are windows but not busy", windows: []watch.WindowState{busy(11, "done"), busy(12, "idle"), busy(13, "stopped")}, wantWindows: []int{11, 12, 13}},
		{name: "synthetic failed entry says nothing about a worktree", windows: []watch.WindowState{busy(42, "failed")}},
		{name: "synthetic escalated entry says nothing about a worktree", windows: []watch.WindowState{busy(42, "escalated")}},
		{name: "a failed overlay does not hide the live window of another ticket", windows: []watch.WindowState{busy(42, "failed"), busy(43, "running")}, wantWindows: []int{43}, wantBusy: []int{43}},
		{name: "windows without a ticket prefix are ignored", windows: []watch.WindowState{{WindowName: "add-dark-mode", Status: "running"}, {WindowName: "", Status: "running"}}},
		{name: "leading-zero prefix reads as the number, as classify does", windows: []watch.WindowState{busy(42, "idle"), {WindowName: "043-implement", Status: "running"}}, wantWindows: []int{42, 43}, wantBusy: []int{43}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ls := SnapshotLiveState(&watch.StateSnapshot{Windows: tt.windows})

			if got := trueKeys(ls.WindowTickets); !reflect.DeepEqual(got, orEmpty(tt.wantWindows)) {
				t.Errorf("WindowTickets = %v, want %v", got, tt.wantWindows)
			}
			if got := trueKeys(ls.BusyTickets); !reflect.DeepEqual(got, orEmpty(tt.wantBusy)) {
				t.Errorf("BusyTickets = %v, want %v", got, tt.wantBusy)
			}
			if ls.SnapshotErr != nil || ls.WindowsErr != nil {
				t.Errorf("SnapshotErr = %v, WindowsErr = %v, want neither: a snapshot is readable and tmux is not consulted", ls.SnapshotErr, ls.WindowsErr)
			}
			if len(ls.PaneCwds) != 0 {
				t.Errorf("PaneCwds = %v, want none: a snapshot knows no pane directories", ls.PaneCwds)
			}
		})
	}
}

// A nil snapshot is "could not read": SnapshotErr is set rather than an empty,
// confident answer.
func TestSnapshotLiveState_NilSnapshotIsAnError(t *testing.T) {
	if ls := SnapshotLiveState(nil); ls.SnapshotErr == nil {
		t.Error("SnapshotLiveState(nil).SnapshotErr = nil, want an error")
	}
}

// The refactor behind SnapshotLiveState must leave ReadLiveState's snapshot half
// unchanged: for the same snapshot both yield the same ticket sets.
func TestSnapshotLiveState_AgreesWithReadLiveState(t *testing.T) {
	noSandbox(t)
	snap := &watch.StateSnapshot{Windows: []watch.WindowState{
		busy(42, "running"), busy(43, "done"), busy(44, "failed"), busy(45, "escalated"), {WindowName: "scratch", Status: "idle"},
	}}

	got := SnapshotLiveState(snap)
	want := ReadLiveState(context.Background(), LiveSources{
		ListPanes:    (&tmuxtest.LivePaneLister{}).ListLivePanes,
		ReadSnapshot: func() (*watch.StateSnapshot, error) { return snap, nil },
	})

	if !reflect.DeepEqual(got.WindowTickets, want.WindowTickets) || !reflect.DeepEqual(got.BusyTickets, want.BusyTickets) {
		t.Errorf("SnapshotLiveState = windows %v busy %v, ReadLiveState = windows %v busy %v; want identical sets",
			got.WindowTickets, got.BusyTickets, want.WindowTickets, want.BusyTickets)
	}
	if len(got.WindowTickets) == 0 {
		t.Fatal("fixture premise broken: the snapshot yields no window tickets, so the comparison above is vacuous")
	}
}

// A guard built from SnapshotLiveState blocks the worktree TargetOf names, with
// no git involved: this is the check the daemon runs on its event loop.
func TestSnapshotLiveState_GuardBlocksTheWorktreeOfALiveWindow(t *testing.T) {
	ls := SnapshotLiveState(&watch.StateSnapshot{Windows: []watch.WindowState{busy(42, "idle")}})
	guard := InUseGuard(ls, false)
	live := Entry{Name: "42-feature", LocalPath: "/r/.worktrees/42-feature"}
	other := Entry{Name: "43-feature", LocalPath: "/r/.worktrees/43-feature"}

	if blocked, err := guard(context.Background(), TargetOf(live)); err != nil || !blocked {
		t.Errorf("guard(live window's worktree) = %v, %v; want blocked", blocked, err)
	}
	if blocked, err := guard(context.Background(), TargetOf(other)); err != nil || blocked {
		t.Errorf("guard(other ticket's worktree) = %v, %v; want not blocked", blocked, err)
	}
}

// Unit test: TargetOf must read the ticket exactly as classify.go does, because a
// guard is asked about the Target that Classify would build for the same entry.
func TestTargetOf(t *testing.T) {
	tests := []struct {
		name  string
		entry Entry
		want  Target
	}{
		{"ticket worktree", Entry{Name: "42-feature", LocalPath: "/r/.worktrees/42-feature"}, Target{Ticket: 42, Name: "42-feature", LocalPath: "/r/.worktrees/42-feature"}},
		{"leading zeros read as the number", Entry{Name: "042-x", LocalPath: "/r/.worktrees/042-x"}, Target{Ticket: 42, Name: "042-x", LocalPath: "/r/.worktrees/042-x"}},
		{"ticketless worktree", Entry{Name: "scratch", LocalPath: "/r/.worktrees/scratch"}, Target{Name: "scratch", LocalPath: "/r/.worktrees/scratch"}},
		{"bare number is no ticket prefix", Entry{Name: "42", LocalPath: "/r/.worktrees/42"}, Target{Name: "42", LocalPath: "/r/.worktrees/42"}},
		{"number beyond int32 is no ticket", Entry{Name: "99999999999-x", LocalPath: "/r/.worktrees/99999999999-x"}, Target{Name: "99999999999-x", LocalPath: "/r/.worktrees/99999999999-x"}},
		{"entry names are used as given, not the admin name", Entry{AdminName: "other", Name: "7-seven", LocalPath: "/r/.worktrees/7-seven"}, Target{Ticket: 7, Name: "7-seven", LocalPath: "/r/.worktrees/7-seven"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TargetOf(tt.entry); got != tt.want {
				t.Errorf("TargetOf(%+v) = %+v, want %+v", tt.entry, got, tt.want)
			}
		})
	}
}

// trueKeys lists the keys of m that are true, ascending.
func trueKeys(m map[int]bool) []int {
	keys := []int{}
	for k, v := range m {
		if v {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

func orEmpty(s []int) []int {
	if s == nil {
		return []int{}
	}
	return s
}
