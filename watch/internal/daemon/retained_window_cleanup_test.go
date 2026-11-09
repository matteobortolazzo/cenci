package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tmuxfe "github.com/matteobortolazzo/cenci/watch/v2/internal/frontend/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/windowproc/windowproctest"
)

// The retained-window half of the window-cleanup pass (#1458): a window tmux still
// holds -- every pane dead, kept by remain-on-exit -- after the session that
// launched it ended, or after a daemon restart, is found from tmux's own
// inventory and closed only on a terminal ticket verdict, after two tmux-owned
// revalidations. It kills irreversibly, so every case drives the real pass over
// the fake tmux inventory (tmuxtest.RetainedServer) and asserts what was actually
// killed and logged. Each negative case carries a *control* retained window that
// must be closed: it proves the pass ran, so "nothing was killed" cannot pass
// vacuously.

// requireOnlyCloseLogs asserts that every window-cleanup: line in logs reports a
// close: the keep reasons that are silent on the tracked path stay silent here.
func requireOnlyCloseLogs(t *testing.T, logs string) {
	t.Helper()
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "window-cleanup:") && !strings.Contains(line, "closing") {
			t.Errorf("log line %q, want only close lines: this keep reason is silent", line)
		}
	}
}

// requireLogged asserts that logs holds a window-cleanup: line containing every
// one of wants.
func requireLogged(t *testing.T, logs string, wants ...string) {
	t.Helper()
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, "window-cleanup:") {
			continue
		}
		all := true
		for _, want := range wants {
			all = all && strings.Contains(line, want)
		}
		if all {
			return
		}
	}
	t.Errorf("log = %q, want one window-cleanup: line containing all of %q", logs, wants)
}

// editWindow changes the window of socket with tmux's id windowID, under the fake
// server's lock.
func editWindow(rs *tmuxtest.RetainedServer, socket, windowID string, fn func(w *tmuxtest.RetainedWindow)) {
	rs.Edit(func(s *tmuxtest.RetainedServer) {
		for i := range s.Windows[socket] {
			if s.Windows[socket][i].ID == windowID {
				fn(&s.Windows[socket][i])
			}
		}
	})
}

// hookProcKiller is the process-killer fake with a hook that runs once the
// processes of a window have been "killed", i.e. during the grace wait before
// kill-window.
type hookProcKiller struct {
	orderedProcKiller
	onKill  func(target string)
	procErr error // the report the process cleanup returns, when set
}

func (h *hookProcKiller) KillWindowProcesses(ctx context.Context, socket, target string) error {
	err := h.orderedProcKiller.KillWindowProcesses(ctx, socket, target)
	if h.onKill != nil {
		h.onKill(target)
	}
	if err == nil {
		err = h.procErr
	}
	return err
}

// endSession delivers SessionEnd for w's session on socket.
func (fx *wcFixture) endSession(w wcWindow, socket string) {
	fx.t.Helper()
	fx.d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: w.key, TmuxPane: w.pane, TmuxSocket: socket})
}

// ---------------------------------------------------------------------------
// Discovery: the retained window of a finished ticket is closed
// ---------------------------------------------------------------------------

// AC1: a window named <N> receives SessionStart, Stop and SessionEnd, its pane
// is then reported dead and the ticket is Finished: the next pass closes it
// through its recorded socket and %N, processes first, although d.sessions no
// longer has it. A socket other than the default server stays in the pass only
// while another tracked session records it, so that variant keeps a second,
// unfinished window tracked there.
func TestRetainedWindowCleanup_SessionEndThenFinishedClosesTheRetainedWindow(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		name := "default server"
		if recorded {
			name = "recorded socket"
		}
		t.Run(name, func(t *testing.T) {
			fx := newWCFixture(t)
			rs := fx.retained()
			socket := ""
			if recorded {
				socket = filepath.Join(trustedSocketDir(t, fx.d), "work")
			}
			dir, repo := fx.addRepo("alpha", "")
			ended := fx.addWindowOnSocket(socket, 782, dir)
			wantReads := []int{782}
			if recorded {
				fx.addWindowOnSocket(socket, 790, dir)
				fx.reader.script(repo, 790, ticketstate.NotFinished)
				wantReads = []int{782, 790}
			}
			fx.reader.script(repo, 782, ticketstate.Finished)
			fx.endSession(ended, socket)
			if _, tracked := fx.d.sessions[ended.key]; tracked {
				t.Fatal("the ended session is still in d.sessions, want the window found from tmux alone")
			}
			retained := fx.addRetained(socket, "@8", "782-implement", dir, ended.pane)
			logs := captureLog(t)

			fx.runPass()

			fx.requireClosed(retained)
			if want := []string{socket}; !slices.Equal(fx.fk.sockets, want) {
				t.Errorf("kill-window sockets = %q, want %q: the window's recorded socket", fx.fk.sockets, want)
			}
			fx.requireReads(readCall{Repo: repo, Nums: wantReads})
			requireLogged(t, logs.String(), "closing", "782")
			if got := rs.InventorySockets(); !slices.Contains(got, socket) {
				t.Errorf("inventory sockets = %q, want %q read", got, socket)
			}
		})
	}
}

// The kill goes through the real kill path (tmux.ExecClient, windowproc.Killer)
// over a fake tmux binary, so the recorded `-S <socket>` and the %N target are
// asserted in the ordered call log: the pane's processes are looked up on that
// server, and kill-window follows there.
func TestRetainedWindowCleanup_KillIsIssuedOnTheRecordedSocketWithThePaneId(t *testing.T) {
	fx := newWCFixture(t)
	fx.retained()
	socket := filepath.Join(trustedSocketDir(t, fx.d), "work")
	dir, repo := fx.addRepo("alpha", "")
	anchor := fx.addWindowOnSocket(socket, 790, dir) // keeps the socket in the pass
	fx.reader.script(repo, 790, ticketstate.NotFinished)
	fx.addRetained(socket, "@8", "782-implement", dir, "%7")
	fx.reader.script(repo, 782, ticketstate.Finished)
	fakes := fx.realKillPath(windowproctest.Spec{Panes: []windowproctest.Pane{
		{ID: "%7", PanePID: windowproctest.HarmlessPanePID(t), Dead: windowproctest.DeadYes},
	}})

	fx.runPass()

	// A dead host pane is listed (both listings) so it can be reported, and gets no
	// signal: the call log has no process call at all, only the kill-window.
	want := append(windowproctest.ListCallsOn(socket, "%7"), "tmux -S "+socket+" kill-window -t %7")
	if got := fakes.Calls(t); !slices.Equal(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s (the tracked window %s is NotFinished and stays)", strings.Join(got, "\n"), strings.Join(want, "\n"), anchor.pane)
	}
}

// AC2: a freshly constructed daemon -- empty d.sessions, no replayed hook events
// -- sees the same retained window on the default server and closes it on its
// first pass after the Finished verdict.
func TestRetainedWindowCleanup_FreshDaemonClosesTheRetainedWindowOnItsFirstPass(t *testing.T) {
	fx := newWCFixture(t)
	rs := fx.retained()
	dir, repo := fx.addRepo("alpha", "")
	w := fx.addRetained("", "@3", "782-implement", dir, "%7")
	fx.reader.script(repo, 782, ticketstate.Finished)
	if len(fx.d.sessions) != 0 {
		t.Fatalf("d.sessions = %d entries, want a daemon that has seen no events", len(fx.d.sessions))
	}

	fx.startAndApply()

	fx.requireClosed(w)
	if want := []string{""}; !slices.Equal(fx.fk.sockets, want) {
		t.Errorf("kill-window sockets = %q, want the default server %q", fx.fk.sockets, want)
	}
	if want := []string{""}; !slices.Equal(rs.InventorySockets(), want) {
		t.Errorf("inventory sockets = %q, want only the default server", rs.InventorySockets())
	}
}

// AC3: a tracked, finished window that also shows up in the inventory is one
// window: one ticket lookup for its number, one kill-window, and the kill goes
// through the tracked path (which never re-reads the window).
func TestRetainedWindowCleanup_TrackedWindowAlsoInTheInventoryIsReadAndKilledOnce(t *testing.T) {
	fx := newWCFixture(t)
	rs := fx.retained()
	dir, repo := fx.addRepo("alpha", "")
	tracked := fx.addWindow(782, dir)
	fx.addRetained("", "@1", "782-implement", dir, tracked.pane)
	fx.reader.script(repo, 782, ticketstate.Finished)

	fx.runPass()

	fx.requireClosed(tracked)
	fx.requireReads(readCall{Repo: repo, Nums: []int{782}})
	if n := rs.Count(tmuxtest.WindowRead); n != 0 {
		t.Errorf("window re-reads = %d, want 0: the tracked path owns a window that is also in the inventory", n)
	}
}

// A window of several dead panes is closed through its lowest pane number (%9,
// not the string-order %10), and its revalidation reads that same pane.
func TestRetainedWindowCleanup_MultiPaneWindowIsKilledThroughItsLowestPaneNumber(t *testing.T) {
	fx := newWCFixture(t)
	rs := fx.retained()
	dir, repo := fx.addRepo("alpha", "")
	fx.addRetained("", "@4", "782-implement", dir, "%10", "%9", "%12")
	fx.reader.script(repo, 782, ticketstate.Finished)

	fx.runPass()

	fx.requireClosed(wcWindow{pane: "%9"})
	for _, r := range rs.Reads() {
		if r.Kind == tmuxtest.WindowRead && r.Pane != "%9" {
			t.Errorf("re-read through %s, want the lowest pane %%9", r.Pane)
		}
	}
}

// Closing a retained window removes the pending-close entries of its panes (the
// SessionEnd path must never kill the same window again) and only those: the
// entry for the same pane id on another socket stays.
func TestRetainedWindowCleanup_CloseRemovesThePendingCloseEntriesOfItsPanes(t *testing.T) {
	fx := newWCFixture(t)
	fx.retained()
	dir, repo := fx.addRepo("alpha", "")
	w := fx.addRetained("", "@4", "782-implement", dir, "%3", "%4")
	fx.reader.script(repo, 782, ticketstate.Finished)
	other := filepath.Join(trustedSocketDir(t, fx.d), "other")
	for _, key := range []pendingKey{{pane: "%3"}, {pane: "%4"}, {socket: other, pane: "%3"}} {
		fx.d.registerPendingClose(ipc.PendingClose{PaneID: key.pane, Socket: key.socket, WindowName: "782-implement"})
	}

	fx.runPass()

	fx.requireClosed(w)
	for _, key := range []pendingKey{{pane: "%3"}, {pane: "%4"}} {
		if _, ok := fx.d.pending[key]; ok {
			t.Errorf("pending-close entry %+v still registered, want it removed", key)
		}
	}
	if _, ok := fx.d.pending[pendingKey{socket: other, pane: "%3"}]; !ok {
		t.Error("the pending-close entry of another server's %3 was removed, want it kept")
	}
}

// ---------------------------------------------------------------------------
// Keep cases: a retained window that must stay open
// ---------------------------------------------------------------------------

// keepRig is a retained control window of a finished ticket (#790) in repo alpha:
// the proof the pass ran, so a keep assertion cannot pass vacuously.
type keepRig struct {
	fx      *wcFixture
	rs      *tmuxtest.RetainedServer
	dir     string
	repo    ticketstate.Repo
	control wcWindow
}

func newKeepRig(t *testing.T) *keepRig {
	t.Helper()
	fx := newWCFixture(t)
	rs := fx.retained()
	dir, repo := fx.addRepo("alpha", "")
	control := fx.addRetained("", "@2", "790-implement", dir, "%2")
	fx.reader.script(repo, 790, ticketstate.Finished)
	return &keepRig{fx: fx, rs: rs, dir: dir, repo: repo, control: control}
}

// victim adds the retained window #783 the case expects to stay open.
func (r *keepRig) victim() wcWindow {
	return r.fx.addRetained("", "@1", "783-implement", r.dir, "%1")
}

// AC4: the ticket's state keeps the window open. An open PR or unfinished issue
// is silent (as on the tracked path); an Unknown verdict, a read error and a
// missing result are logged whatever -verbose says.
func TestRetainedWindowCleanup_TicketStateThatIsNotFinishedKeepsTheWindow(t *testing.T) {
	tests := []struct {
		name    string
		script  func(r *fakeTicketReader, repo ticketstate.Repo)
		wantLog string // "" = silent
	}{
		{"open PR or unfinished issue", func(r *fakeTicketReader, repo ticketstate.Repo) { r.script(repo, 783, ticketstate.NotFinished) }, ""},
		{"unknown verdict", func(r *fakeTicketReader, repo ticketstate.Repo) {
			r.scriptResult(repo, 783, ticketstate.Result{Verdict: ticketstate.Unknown})
		}, "unknown state"},
		{"read error", func(r *fakeTicketReader, repo ticketstate.Repo) {
			r.scriptErr(repo, 783, errors.New("gh: HTTP 502 for ticket 783"))
		}, "gh: HTTP 502 for ticket 783"},
		{"missing ticket result", func(r *fakeTicketReader, repo ticketstate.Repo) { r.omit(repo, 783) }, "no ticket state returned"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newKeepRig(t)
			rig.victim()
			tc.script(rig.fx.reader, rig.repo)
			logs := captureLog(t)

			rig.fx.runPass()

			rig.fx.requireClosed(rig.control)
			rig.fx.requireReads(readCall{Repo: rig.repo, Nums: []int{783, 790}})
			if tc.wantLog == "" {
				requireOnlyCloseLogs(t, logs.String())
			} else {
				requireLogged(t, logs.String(), tc.wantLog)
			}
		})
	}
}

// AC4: a malformed inventory row -- a window the reader dropped and reported,
// whether for a broken row or a pane_dead other than 0/1 -- is never closed, even
// with a Finished verdict scripted for its number, and the report is logged
// whatever -verbose says.
func TestRetainedWindowCleanup_MalformedInventoryRowKeepsItsWindowAndIsLogged(t *testing.T) {
	for _, report := range []string{
		`window "@1": malformed row "@1\t783-implement" in the window listing`,
		`window "@1": pane_dead "2" of pane "%1" is not 0 or 1`,
	} {
		t.Run(report, func(t *testing.T) {
			rig := newKeepRig(t)
			rig.rs.Edit(func(s *tmuxtest.RetainedServer) { s.Dropped = map[string][]string{"": {report}} })
			rig.fx.reader.script(rig.repo, 783, ticketstate.Finished)
			logs := captureLog(t)

			rig.fx.runPass()

			rig.fx.requireClosed(rig.control)
			requireLogged(t, logs.String(), report)
			for _, c := range rig.fx.reader.recorded() {
				if slices.Contains(c.Nums, 783) {
					t.Errorf("reader asked for ticket 783 (%+v), want a dropped row never evaluated", c)
				}
			}
		})
	}
}

// AC4: a window with no @cenci-repo-dir is never read or closed, silently (as on
// the tracked path).
func TestRetainedWindowCleanup_AbsentRepoDirKeepsTheWindowSilently(t *testing.T) {
	rig := newKeepRig(t)
	rig.fx.addRetained("", "@1", "783-implement", "", "%1")
	rig.fx.reader.script(rig.repo, 783, ticketstate.Finished)
	logs := captureLog(t)

	rig.fx.runPass()

	rig.fx.requireClosed(rig.control)
	rig.fx.requireReads(readCall{Repo: rig.repo, Nums: []int{790}})
	requireOnlyCloseLogs(t, logs.String())
}

// AC4: windowCleanup.enabled=false keeps the retained window with no GitHub read
// for its repo; the opt-out is logged only under -verbose.
func TestRetainedWindowCleanup_OptedOutRepoKeepsTheWindowAndLogsOnlyUnderVerbose(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprintf("verbose=%v", verbose), func(t *testing.T) {
			rig := newKeepRig(t)
			rig.fx.d.cfg.Verbose = verbose
			optedOutDir, optedOut := rig.fx.addRepo("beta", `{"windowCleanup":{"enabled":false}}`)
			rig.fx.addRetained("", "@1", "783-implement", optedOutDir, "%1")
			rig.fx.reader.script(optedOut, 783, ticketstate.Finished)
			logs := captureLog(t)

			rig.fx.runPass()

			rig.fx.requireClosed(rig.control)
			if n := rig.fx.reader.readsFor(optedOut); n != 0 {
				t.Errorf("reader calls for the opted-out repo = %d, want 0", n)
			}
			if verbose {
				requireLogged(t, logs.String(), "windowCleanup.enabled is false")
			} else {
				requireOnlyCloseLogs(t, logs.String())
			}
		})
	}
}

// AC4: an unresolvable repo and a malformed repo config keep the window and are
// logged whatever -verbose says.
func TestRetainedWindowCleanup_UnresolvableRepoAndMalformedConfigKeepTheWindowAndAreLogged(t *testing.T) {
	rig := newKeepRig(t)
	brokenDir := t.TempDir()
	rig.fx.resolver.fail(brokenDir, errors.New("git: not a repository"))
	malformedDir, malformed := rig.fx.addRepo("beta", `{"windowCleanup": {not json`)
	rig.fx.addRetained("", "@1", "783-implement", brokenDir, "%1")
	rig.fx.addRetained("", "@3", "784-implement", malformedDir, "%3")
	rig.fx.reader.script(malformed, 784, ticketstate.Finished)
	logs := captureLog(t)

	rig.fx.runPass()

	rig.fx.requireClosed(rig.control)
	requireLogged(t, logs.String(), "git: not a repository")
	requireLogged(t, logs.String(), filepath.Join(malformedDir, ".cenci", "config.json"))
	if n := rig.fx.reader.readsFor(malformed); n != 0 {
		t.Errorf("reader calls for the malformed-config repo = %d, want 0", n)
	}
}

// AC4: an inventory listing that fails or times out on one socket keeps that
// socket's windows open and is logged whatever -verbose says; the other socket's
// listing still runs (the control closes). tmux reporting no server on a socket is
// the one benign failure: nothing is running there, and nothing is logged.
func TestRetainedWindowCleanup_ListingFailureKeepsThatSocketsWindowsAndIsLogged(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantLog string // "" = silent
	}{
		{"listing error", errors.New("tmux list-panes: protocol version mismatch"), "protocol version mismatch"},
		{"listing timeout", fmt.Errorf("tmux list-panes: %w", context.DeadlineExceeded), "deadline exceeded"},
		{"no server running", fmt.Errorf("tmux list-panes: %w", tmux.ErrNoServer), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newKeepRig(t)
			socket := filepath.Join(trustedSocketDir(t, rig.fx.d), "work")
			rig.fx.addWindowOnSocket(socket, 795, rig.dir) // records the socket for the pass
			rig.fx.reader.script(rig.repo, 795, ticketstate.NotFinished)
			rig.fx.addRetained(socket, "@1", "783-implement", rig.dir, "%1")
			rig.fx.reader.script(rig.repo, 783, ticketstate.Finished)
			rig.rs.Edit(func(s *tmuxtest.RetainedServer) { s.InventoryErrs = map[string]error{socket: tc.err} })
			logs := captureLog(t)

			rig.fx.runPass()

			rig.fx.requireClosed(rig.control)
			if got := rig.rs.InventorySockets(); !slices.Contains(got, socket) || !slices.Contains(got, "") {
				t.Errorf("inventory sockets = %q, want both the default server and %q read", got, socket)
			}
			if tc.wantLog == "" {
				requireOnlyCloseLogs(t, logs.String())
			} else {
				requireLogged(t, logs.String(), tc.wantLog)
			}
		})
	}
}

// AC5/AC6: a window with a live pane is never closed whatever its ticket state:
// not a single pane at pane_dead=0, not one live pane among dead ones. Neither is
// read at all.
func TestRetainedWindowCleanup_WindowWithALivePaneIsNeverClosed(t *testing.T) {
	rig := newKeepRig(t)
	rig.rs.AddWindow("", tmuxtest.RetainedWindow{ID: "@1", Name: "783-implement", RepoDir: rig.dir,
		Panes: []tmuxtest.RetainedPane{{ID: "%1", Dead: false}}})
	rig.rs.AddWindow("", tmuxtest.RetainedWindow{ID: "@3", Name: "784-implement", RepoDir: rig.dir,
		Panes: []tmuxtest.RetainedPane{{ID: "%3", Dead: true}, {ID: "%4", Dead: false}, {ID: "%5", Dead: true}}})
	rig.fx.reader.script(rig.repo, 783, ticketstate.Finished)
	rig.fx.reader.script(rig.repo, 784, ticketstate.Finished)

	rig.fx.runPass()

	rig.fx.requireClosed(rig.control)
	rig.fx.requireReads(readCall{Repo: rig.repo, Nums: []int{790}})
}

// Q1: untracked discovery uses the tracked path's name rule. A name that is not
// the canonical "<N>-..." of ticket N >= 1 -- a leading zero, a sign, zero, an
// empty name, a prose slug, a digit run not followed by "-" -- is never read as
// (and never closed on the verdict of) another ticket; "<PR>-ci-repair" is a
// candidate for its PR number.
func TestRetainedWindowCleanup_NameRuleIsTheTrackedPathsRule(t *testing.T) {
	rig := newKeepRig(t)
	for i, name := range []string{"042-notes", "+7-notes", "0-notes", "", "pr-1455-has-failing-ci-checks", "1455x", "zsh"} {
		rig.fx.addRetained("", fmt.Sprintf("@%d", 20+i), name, rig.dir, fmt.Sprintf("%%%d", 20+i))
	}
	ciRepair := rig.fx.addRetained("", "@40", "42-ci-repair", rig.dir, "%40")
	for _, n := range []int{42, 7, 0, 1455} {
		rig.fx.reader.script(rig.repo, n, ticketstate.Finished)
	}

	rig.fx.runPass()

	rig.fx.requireClosed(rig.control, ciRepair)
	rig.fx.requireReads(readCall{Repo: rig.repo, Nums: []int{42, 790}})
}

// A window on a socket no tracked session records is outside the pass: after a
// restart only the default server and the sockets of tracked sessions are read,
// so a retained window elsewhere stays open (the documented limit).
func TestRetainedWindowCleanup_SocketNoTrackedSessionRecordsIsNotInventoried(t *testing.T) {
	rig := newKeepRig(t)
	other := filepath.Join(trustedSocketDir(t, rig.fx.d), "never-seen")
	rig.fx.addRetained(other, "@1", "783-implement", rig.dir, "%1")
	rig.fx.reader.script(rig.repo, 783, ticketstate.Finished)

	rig.fx.runPass()

	rig.fx.requireClosed(rig.control)
	if want := []string{""}; !slices.Equal(rig.rs.InventorySockets(), want) {
		t.Errorf("inventory sockets = %q, want only the default server %q", rig.rs.InventorySockets(), want)
	}
}

// The same %N on two servers is two panes. A tracked session owning %5 on socket B
// keeps B's retained window open, and never blocks the retained window that has
// the same %5 on socket A, which is closed on A alone.
func TestRetainedWindowCleanup_TheSamePaneIdOnTwoSocketsIsTwoPanes(t *testing.T) {
	fx := newWCFixture(t)
	fx.retained()
	dir := trustedSocketDir(t, fx.d)
	sockA, sockB := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	repoDir, repo := fx.addRepo("alpha", "")
	fx.addWindowOnSocketPane(sockA, "%8", 901, repoDir) // records socket A
	fx.addWindowOnSocketPane(sockB, "%5", 902, repoDir) // owns %5 on socket B
	for _, n := range []int{901, 902} {
		fx.reader.script(repo, n, ticketstate.NotFinished)
	}
	closedOnA := fx.addRetained(sockA, "@1", "31-implement", repoDir, "%5")
	fx.addRetained(sockB, "@1", "32-implement", repoDir, "%5")
	fx.reader.script(repo, 31, ticketstate.Finished)
	fx.reader.script(repo, 32, ticketstate.Finished)

	fx.runPass()

	fx.requireClosed(closedOnA)
	if want := []string{sockA}; !slices.Equal(fx.fk.sockets, want) {
		t.Errorf("kill-window sockets = %q, want only %q: B's %%5 is a tracked session's pane", fx.fk.sockets, want)
	}
}

// ---------------------------------------------------------------------------
// Apply: ownership and the babysit guard on the loop
// ---------------------------------------------------------------------------

// AC8: a tracked session that starts owning one of the window's panes between
// evaluation and apply makes the untracked kill skip.
func TestRetainedWindowCleanup_SessionThatClaimsThePaneBeforeApplyBlocksTheKill(t *testing.T) {
	rig := newKeepRig(t)
	rig.fx.addRetained("", "@1", "783-implement", rig.dir, "%1", "%6")
	rig.fx.reader.script(rig.repo, 783, ticketstate.Finished)

	res := rig.fx.evaluate()
	rig.fx.d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "claimer", TmuxPane: "%6"})
	if rig.fx.d.sessions["claimer"] == nil {
		t.Fatal("the claiming session is not tracked, want it in d.sessions")
	}
	rig.fx.d.applyWindowCleanup(res)

	rig.fx.requireClosed(rig.control)
}

// AC10: closeGuard blocks the retained window's ticket -> the window stays; the
// guard cleared -> the window, which has no d.sessions entry, is closed on the
// first pass after (not held for lacking a session).
func TestRetainedWindowCleanup_CloseGuardBlocksUntilItClears(t *testing.T) {
	rig := newKeepRig(t)
	guarded := rig.victim()
	rig.fx.reader.script(rig.repo, 783, ticketstate.Finished)
	var asked []string
	blocked := true
	rig.fx.d.closeGuard = func(ticket string) (bool, string) {
		asked = append(asked, ticket)
		return ticket == "783" && blocked, "babysit supervising PR #783, CI not green"
	}
	rig.fx.d.cfg.Verbose = true
	logs := captureLog(t)

	rig.fx.runPass()

	rig.fx.requireClosed(rig.control)
	if !slices.Contains(asked, "783") {
		t.Errorf("guard asked about %v, want it consulted with ticket 783 from the window name", asked)
	}
	requireLogged(t, logs.String(), "holding", "babysit supervising PR #783")

	blocked = false
	rig.fx.resetKills()
	rig.fx.runPass()

	rig.fx.requireClosed(guarded, rig.control)
}

// ---------------------------------------------------------------------------
// Kill: revalidated twice, through the window's own tmux-owned identity
// ---------------------------------------------------------------------------

// retainedMutations are the changes to a retained window that must stop its kill.
var retainedMutations = []struct {
	name   string
	mutate func(w *tmuxtest.RetainedWindow)
}{
	{"respawned live pane", func(w *tmuxtest.RetainedWindow) { w.Panes[0].Dead = false }},
	{"window renamed", func(w *tmuxtest.RetainedWindow) { w.Name = "999-other" }},
	{"repo dir changed", func(w *tmuxtest.RetainedWindow) { w.RepoDir = "/work/elsewhere" }},
	{"window id of the pane changed", func(w *tmuxtest.RetainedWindow) { w.ID = "@77" }},
	{"pane set changed", func(w *tmuxtest.RetainedWindow) {
		w.Panes = append(w.Panes, tmuxtest.RetainedPane{ID: "%99", Dead: true})
	}},
}

// AC7: a change to the window between evaluation and kill -- before the process
// cleanup, or during the grace wait before kill-window -- keeps the window open,
// logged whatever -verbose says. Before process cleanup nothing of it is touched;
// in the grace wait its processes were already cleaned up (the order is processes,
// then window) but kill-window is not issued.
func TestRetainedWindowCleanup_WindowChangedBeforeTheKillIsNeverClosed(t *testing.T) {
	for _, mutation := range retainedMutations {
		for _, phase := range []string{"before process cleanup", "during the grace wait"} {
			t.Run(mutation.name+"/"+phase, func(t *testing.T) {
				rig := newKeepRig(t)
				victim := rig.victim()
				rig.fx.reader.script(rig.repo, 783, ticketstate.Finished)
				change := func() { editWindow(rig.rs, "", "@1", mutation.mutate) }
				if phase == "before process cleanup" {
					rig.rs.BeforeRead = func(read tmuxtest.RetainedRead, _ int) {
						if read.Kind == tmuxtest.WindowRead && read.Pane == victim.pane {
							change()
						}
					}
				} else {
					rig.fx.d.procKiller = &hookProcKiller{
						orderedProcKiller: orderedProcKiller{order: rig.fx.order},
						onKill: func(target string) {
							if target == victim.pane {
								change()
							}
						},
					}
				}
				logs := captureLog(t)

				rig.fx.runPass()

				if want := []string{rig.control.pane}; !slices.Equal(rig.fx.fk.killed, want) {
					t.Errorf("windows killed = %v, want only the control %v", rig.fx.fk.killed, want)
				}
				events := rig.fx.order.snapshot()
				if slices.Contains(events, "window "+victim.pane) {
					t.Errorf("kill log = %v, want no kill-window for the changed window", events)
				}
				if processed := slices.Contains(events, "processes "+victim.pane); processed != (phase == "during the grace wait") {
					t.Errorf("kill log = %v, want the changed window's processes cleaned up only in the grace-wait case", events)
				}
				requireLogged(t, logs.String(), "keeping", victim.pane)
			})
		}
	}
}

// AC7: a revalidation read that fails keeps the window and is logged. The control
// here is a tracked finished window, which never takes the revalidation read.
func TestRetainedWindowCleanup_UnreadableRevalidationKeepsTheWindowAndIsLogged(t *testing.T) {
	fx := newWCFixture(t)
	rs := fx.retained()
	dir, repo := fx.addRepo("alpha", "")
	control := fx.addWindow(790, dir)
	fx.addRetained("", "@9", "783-implement", dir, "%9")
	fx.reader.script(repo, 790, ticketstate.Finished)
	fx.reader.script(repo, 783, ticketstate.Finished)
	rs.Edit(func(s *tmuxtest.RetainedServer) {
		s.ReadErrs = map[string]error{"": errors.New("tmux list-windows: no row for window \"@9\"")}
	})
	logs := captureLog(t)

	fx.runPass()

	fx.requireClosed(control)
	requireLogged(t, logs.String(), "keeping", "no row for window")
}

// A target pane that is already gone (tmux.ErrWindowGone from the revalidation
// read) counts as closed -- logged under -verbose only -- and no other target is
// tried: nothing is killed, and no process cleanup runs for it.
func TestRetainedWindowCleanup_PaneThatIsGoneBeforeTheKillIsAlreadyClosed(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprintf("verbose=%v", verbose), func(t *testing.T) {
			rig := newKeepRig(t)
			rig.fx.d.cfg.Verbose = verbose
			victim := rig.victim()
			rig.fx.reader.script(rig.repo, 783, ticketstate.Finished)
			rig.rs.BeforeRead = func(read tmuxtest.RetainedRead, _ int) {
				if read.Kind == tmuxtest.WindowRead && read.Pane == victim.pane {
					rig.rs.Edit(func(s *tmuxtest.RetainedServer) {
						s.Windows[""] = slices.DeleteFunc(s.Windows[""], func(w tmuxtest.RetainedWindow) bool { return w.ID == "@1" })
					})
				}
			}
			logs := captureLog(t)

			rig.fx.runPass()

			if want := []string{rig.control.pane}; !slices.Equal(rig.fx.fk.killed, want) {
				t.Errorf("windows killed = %v, want only the control %v: no other target for a gone window", rig.fx.fk.killed, want)
			}
			if events := rig.fx.order.snapshot(); slices.Contains(events, "processes "+victim.pane) {
				t.Errorf("kill log = %v, want no process cleanup for a window that is gone", events)
			}
			if verbose {
				requireLogged(t, logs.String(), "already gone", victim.pane)
			} else if strings.Contains(logs.String(), "already gone") {
				t.Errorf("log = %q, want \"already gone\" only under -verbose", logs.String())
			}
		})
	}
}

// The shared kill checks its context before kill-window: a daemon cancelled (or a
// bound exhausted) during the process cleanup never issues the window kill, for a
// retained window and for a tracked one alike.
func TestRetainedWindowCleanup_ContextCancelledDuringProcessCleanupIssuesNoKillWindow(t *testing.T) {
	for _, retained := range []bool{true, false} {
		name := "tracked window"
		if retained {
			name = "retained window"
		}
		t.Run(name, func(t *testing.T) {
			fx := newWCFixture(t)
			dir, repo := fx.addRepo("alpha", "")
			var target string
			if retained {
				fx.retained()
				target = fx.addRetained("", "@1", "782-implement", dir, "%1").pane
			} else {
				target = fx.addWindow(782, dir).pane
			}
			fx.reader.script(repo, 782, ticketstate.Finished)
			var cancel context.CancelFunc
			fx.d.runOffLoop = func(_ time.Duration, fn func(ctx context.Context)) {
				var ctx context.Context
				ctx, cancel = context.WithCancel(context.Background())
				defer cancel()
				fn(ctx)
			}
			fx.d.procKiller = &hookProcKiller{
				orderedProcKiller: orderedProcKiller{order: fx.order},
				onKill:            func(string) { cancel() },
			}

			fx.runPass()

			if events := fx.order.snapshot(); !slices.Equal(events, []string{"processes " + target}) {
				t.Errorf("kill log = %v, want the process cleanup that ran and no kill-window after the cancel", events)
			}
		})
	}
}

// The kill is scheduled by pane id, so renumber-windows (a close shifts every later
// index) cannot redirect the second retained kill onto the bystander that took its
// index.
func TestRetainedWindowCleanup_RenumberingNeverRedirectsARetainedKill(t *testing.T) {
	fx := newWCFixture(t)
	fx.retained()
	dir, repo := fx.addRepo("alpha", "")
	first := fx.addRetained("", "@1", "782-implement", dir, "%0")
	second := fx.addRetained("", "@2", "783-implement", dir, "%1")
	fx.addRetained("", "@3", "784-implement", dir, "%2")
	fx.reader.script(repo, 782, ticketstate.Finished)
	fx.reader.script(repo, 783, ticketstate.Finished)
	fx.reader.script(repo, 784, ticketstate.NotFinished)
	model := &tmuxModel{renumber: true}
	for i, pane := range []string{"%0", "%1", "%2"} {
		model.windows = append(model.windows, modelWindow{pane: pane, index: i})
	}
	fx.d.killer, fx.d.procKiller = model, model
	runner := &deferredRunner{}
	fx.d.runOffLoop = runner.run

	fx.runPass()
	runner.drain()

	want := []string{first.pane, second.pane}
	slices.Sort(model.killedWindows)
	if !slices.Equal(model.killedWindows, want) {
		t.Errorf("windows killed = %v, want exactly %v: the bystander %%2 must survive", model.killedWindows, want)
	}
}

// modelHookProcKiller is the tmuxModel as the process killer, with a hook that runs
// once a window's processes have been "killed" -- the grace wait before kill-window.
type modelHookProcKiller struct {
	model  *tmuxModel
	onKill func(target string)
}

func (h *modelHookProcKiller) KillWindowProcesses(ctx context.Context, socket, target string) error {
	err := h.model.KillWindowProcesses(ctx, socket, target)
	h.onKill(target)
	return err
}

// A freed index that a newly dispatched window takes: the retained kill is scheduled
// by pane id, so the window that reused the index of a retained window closed in the
// meantime is never killed -- not its processes, not the window. The retained
// window's second revalidation passed just before it was closed by hand, so
// kill-window reaches tmux and finds the pane gone, which counts as closed rather
// than an error. The control without the reuse closes both retained windows and
// still leaves the unfinished bystander.
func TestRetainedWindowCleanup_IndexReusedByANewWindowNeverRedirectsARetainedKill(t *testing.T) {
	const newPane = "%90"
	for _, reuse := range []bool{true, false} {
		name := "index reused by a new window"
		if !reuse {
			name = "control: no reuse"
		}
		t.Run(name, func(t *testing.T) {
			fx := newWCFixture(t)
			fx.retained()
			dir, repo := fx.addRepo("alpha", "")
			first := fx.addRetained("", "@1", "782-implement", dir, "%0")
			second := fx.addRetained("", "@2", "783-implement", dir, "%1")
			bystander := fx.addRetained("", "@3", "784-implement", dir, "%2")
			fx.reader.script(repo, 782, ticketstate.Finished)
			fx.reader.script(repo, 783, ticketstate.Finished)
			fx.reader.script(repo, 784, ticketstate.NotFinished)
			model := &tmuxModel{}
			for i, pane := range []string{first.pane, second.pane, bystander.pane} {
				model.windows = append(model.windows, modelWindow{pane: pane, index: i})
			}
			fx.d.killer = model
			fx.d.procKiller = &modelHookProcKiller{model: model, onKill: func(target string) {
				if reuse && target == second.pane {
					// The second window was closed by hand; a new one took its index. The
					// first close already shifted slice positions, so find it by pane.
					model.mu.Lock()
					i := slices.IndexFunc(model.windows, func(w modelWindow) bool { return w.pane == second.pane })
					model.mu.Unlock()
					model.replace(i, newPane)
				}
			}}
			logs := captureLog(t)

			fx.runPass()

			wantKilled := []string{first.pane, second.pane}
			if reuse {
				wantKilled = []string{first.pane}
			}
			slices.Sort(model.killedWindows)
			if !slices.Equal(model.killedWindows, wantKilled) {
				t.Errorf("windows killed = %v, want %v", model.killedWindows, wantKilled)
			}
			for _, killed := range append(slices.Clone(model.killedWindows), model.killedProcs...) {
				if killed == newPane || killed == bystander.pane {
					t.Errorf("killed %s, want the new window %s and the bystander %s to survive", killed, newPane, bystander.pane)
				}
			}
			if strings.Contains(logs.String(), "error killing") {
				t.Errorf("log = %q, want a pane that is already gone to count as closed, not an error", logs.String())
			}
		})
	}
}

// A dead pane whose launch ID is recorded without its container is reported
// malformed by the process cleanup (an error, and no host signal or launch-ID kill:
// windowproc's own test pins that). That failure never blocks the window kill: the
// retained window is still closed by kill-window on its recorded socket and %N when
// its tmux-owned identity is unchanged, and the daemon logs the report.
func TestRetainedWindowCleanup_DeadPaneWithLaunchIDButNoContainerStillClosesTheWindow(t *testing.T) {
	fx := newWCFixture(t)
	fx.retained()
	socket := filepath.Join(trustedSocketDir(t, fx.d), "work")
	dir, repo := fx.addRepo("alpha", "")
	fx.addWindowOnSocket(socket, 790, dir) // keeps the socket in the pass
	fx.reader.script(repo, 790, ticketstate.NotFinished)
	fx.addRetained(socket, "@8", "782-implement", dir, "%7")
	fx.reader.script(repo, 782, ticketstate.Finished)
	fakes := fx.realKillPath(windowproctest.Spec{Panes: []windowproctest.Pane{{
		ID: "%7", PanePID: windowproctest.HarmlessPanePID(t), Dead: windowproctest.DeadYes,
		LaunchID: "0123456789abcdef0123456789abcdef", Runtime: "docker", // no container
	}}})
	logs := captureLog(t)

	fx.runPass()

	want := append(windowproctest.ListCallsOn(socket, "%7"), "tmux -S "+socket+" kill-window -t %7")
	if got := fakes.Calls(t); !slices.Equal(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s (no runtime exec or signal for the malformed pane, then kill-window)", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	requireLogged(t, logs.String(), "error killing processes", "%7", "@cenci-container")
}

// The same malformed-pane report with the process cleanup modelled as failing: the
// window is closed through its recorded socket and %N only while its tmux-owned
// identity is unchanged. A change during the grace wait -- every kind the
// revalidation checks -- keeps the window, although its processes were already
// cleaned up and reported.
func TestRetainedWindowCleanup_MalformedPaneReportClosesTheWindowOnlyWhileItsIdentityIsUnchanged(t *testing.T) {
	report := errors.New("pane %7: @cenci-launch-id is set but @cenci-container is empty")
	type outcome struct {
		name   string
		mutate func(w *tmuxtest.RetainedWindow) // nil: unchanged
	}
	cases := []outcome{{name: "unchanged"}}
	for _, m := range retainedMutations {
		cases = append(cases, outcome{name: m.name, mutate: m.mutate})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newWCFixture(t)
			rs := fx.retained()
			socket := filepath.Join(trustedSocketDir(t, fx.d), "work")
			dir, repo := fx.addRepo("alpha", "")
			fx.addWindowOnSocket(socket, 790, dir) // keeps the socket in the pass
			fx.reader.script(repo, 790, ticketstate.NotFinished)
			victim := fx.addRetained(socket, "@8", "782-implement", dir, "%7")
			fx.reader.script(repo, 782, ticketstate.Finished)
			fx.d.procKiller = &hookProcKiller{
				orderedProcKiller: orderedProcKiller{order: fx.order},
				procErr:           report,
				onKill: func(target string) {
					if tc.mutate != nil && target == victim.pane {
						editWindow(rs, socket, "@8", tc.mutate)
					}
				},
			}
			logs := captureLog(t)

			fx.runPass()

			events := fx.order.snapshot()
			if !slices.Contains(events, "processes "+victim.pane) {
				t.Errorf("kill log = %v, want the process cleanup that reported the malformed pane to have run", events)
			}
			requireLogged(t, logs.String(), "error killing processes", "@cenci-container")
			if tc.mutate == nil {
				if want := []string{victim.pane}; !slices.Equal(fx.fk.killed, want) {
					t.Errorf("windows killed = %v, want %v: a failed process cleanup never blocks kill-window", fx.fk.killed, want)
				}
				if want := []string{socket}; !slices.Equal(fx.fk.sockets, want) {
					t.Errorf("kill-window sockets = %q, want %q: the window's recorded socket", fx.fk.sockets, want)
				}
				return
			}
			if len(fx.fk.killed) != 0 {
				t.Errorf("windows killed = %v, want none: the window changed during the grace wait", fx.fk.killed)
			}
			requireLogged(t, logs.String(), "keeping", victim.pane)
		})
	}
}

// ---------------------------------------------------------------------------
// The pass: bounds, in-flight, cancellation
// ---------------------------------------------------------------------------

// AC11: with no tracked session and an empty inventory the pass makes no ticket
// read; its worker runs under windowCleanupPassTimeout.
func TestRetainedWindowCleanup_EmptyInventoryMakesNoTicketReadAndIsBoundedByThePassTimeout(t *testing.T) {
	fx := newWCFixture(t)
	rs := fx.retained()
	fx.addRepo("alpha", "")

	fx.startAndApply()

	if calls := fx.reader.recorded(); len(calls) != 0 {
		t.Errorf("reader calls = %+v, want none for an empty inventory", calls)
	}
	if n := rs.Count(tmuxtest.InventoryRead); n != 1 {
		t.Errorf("inventory reads = %d, want 1 (the default server)", n)
	}
	if len(fx.fk.killed) != 0 {
		t.Errorf("windows killed = %v, want none", fx.fk.killed)
	}
	if len(fx.runner.timeouts) == 0 || fx.runner.timeouts[0] != windowCleanupPassTimeout {
		t.Errorf("off-loop timeouts = %v, want the pass bounded by %v", fx.runner.timeouts, windowCleanupPassTimeout)
	}
}

// productionRunnerRig is a daemon on the production goroutine runner, over a
// retained control window of a finished ticket, whose cleanup tick is 10ms.
func productionRunnerRig(t *testing.T) (*wcFixture, *tmuxtest.RetainedServer, *targetKiller) {
	t.Helper()
	cfg := testConfig()
	cfg.WindowCleanupInterval = 10 * time.Millisecond
	mc := &tmuxtest.MockClient{}
	d := newDaemon(cfg, tmuxfe.New(cfg, mc), make(chan ipc.HookEvent, 16), &mockReaper{})
	killer := &targetKiller{killed: make(chan string, 64)}
	d.killer = killer
	fx := newWCFixtureFor(t, d, mc)
	rs := fx.retained()
	dir, repo := fx.addRepo("alpha", "")
	fx.addRetained("", "@2", "790-implement", dir, "%2")
	fx.reader.script(repo, 790, ticketstate.Finished)
	return fx, rs, killer
}

// startLoop runs d.loop until the returned cancel is called (also on cleanup);
// done closes when the loop has exited.
func startLoop(t *testing.T, d *Daemon) (cancel func(), done <-chan struct{}) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_ = d.loop(ctx, make(chan ipc.AttentionUpdate))
	}()
	t.Cleanup(func() {
		stop()
		<-finished
	})
	return stop, finished
}

// AC11: a cleanup tick that arrives while a pass is in flight -- here blocked in
// its inventory read -- starts no second inventory read; once released, the
// pass's verdict reaches the loop and closes the window.
func TestRetainedWindowCleanup_TickWhileAPassIsInFlightStartsNoSecondInventoryRead(t *testing.T) {
	const wait = 5 * time.Second
	fx, rs, killer := productionRunnerRig(t)
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	rs.BeforeRead = func(read tmuxtest.RetainedRead, n int) {
		if read.Kind == tmuxtest.InventoryRead && n == 1 {
			<-gate
		}
	}
	startLoop(t, fx.d)

	deadline := time.Now().Add(wait)
	for rs.Count(tmuxtest.InventoryRead) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the first pass never read the inventory")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // ~20 more cleanup ticks while the read is blocked
	if n := rs.Count(tmuxtest.InventoryRead); n != 1 {
		t.Fatalf("inventory reads while the first is blocked = %d, want exactly 1 (one pass in flight)", n)
	}

	release()
	select {
	case target := <-killer.killed:
		if target != "%2" {
			t.Errorf("kill-window target = %q, want %q", target, "%2")
		}
	case <-time.After(wait):
		t.Fatal("the released pass never closed the retained window")
	}
}

// AC11: cancelling the daemon's context while a kill is between its steps (here
// blocked in the first revalidation read) issues neither the process cleanup nor
// kill-window once the read returns: the worker's context derives from the loop's.
func TestRetainedWindowCleanup_CancellingTheDaemonIssuesNoKillAfterCancel(t *testing.T) {
	const wait = 5 * time.Second
	fx, rs, _ := productionRunnerRig(t)
	order := &killOrder{}
	fx.d.killer = &orderedWindowKiller{inner: &fakeKiller{}, order: order}
	fx.d.procKiller = &orderedProcKiller{order: order}
	var workers sync.WaitGroup
	runOffLoop := fx.d.runOffLoop
	fx.d.runOffLoop = func(timeout time.Duration, fn func(ctx context.Context)) {
		workers.Add(1)
		runOffLoop(timeout, func(ctx context.Context) {
			defer workers.Done()
			fn(ctx)
		})
	}
	inRevalidation := make(chan struct{})
	gate := make(chan struct{})
	var startedOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	rs.BeforeRead = func(read tmuxtest.RetainedRead, _ int) {
		if read.Kind == tmuxtest.WindowRead {
			startedOnce.Do(func() { close(inRevalidation) })
			<-gate
		}
	}
	cancel, loopDone := startLoop(t, fx.d)

	select {
	case <-inRevalidation:
	case <-time.After(wait):
		t.Fatal("the kill never reached its revalidation read")
	}
	cancel()
	select {
	case <-loopDone:
	case <-time.After(wait):
		t.Fatal("the loop did not stop after its context was cancelled")
	}
	release()
	workers.Wait()

	if events := order.snapshot(); len(events) != 0 {
		t.Errorf("kill log = %v, want no process cleanup and no kill-window after the daemon was cancelled", events)
	}
}

// ---------------------------------------------------------------------------
// The daemon's own server has two spellings: "" and its own socket path
// ---------------------------------------------------------------------------

// daemonTmuxEnv sets the daemon's own $TMUX, the variable a bare `tmux` resolves
// its server from: socket is the first field, as in a pane of that server. An
// empty socket clears it, the daemon of a plain shell (and a test run from inside
// a developer's tmux session).
func daemonTmuxEnv(t *testing.T, socket string) {
	t.Helper()
	value := ""
	if socket != "" {
		value = socket + ",4321,0"
	}
	t.Setenv("TMUX", value)
}

// A session on the daemon's own server records the server's socket path (a hook
// event carries $TMUX's first field; <trusted dir>/default when the daemon has no
// $TMUX) while the pass reads that server as "". They are one server: a tracked
// window that also appears in the "" inventory is read once (one inventory read of
// the server, one ticket lookup) and killed once, through the tracked path on its
// recorded socket.
func TestRetainedWindowCleanup_TrackedWindowRecordedUnderTheDefaultSocketPathIsReadAndKilledOnce(t *testing.T) {
	daemonTmuxEnv(t, "")
	fx := newWCFixture(t)
	rs := fx.retained()
	def := filepath.Join(trustedSocketDir(t, fx.d), "default")
	dir, repo := fx.addRepo("alpha", "")
	tracked := fx.addWindowOnSocket(def, 782, dir)
	fx.addRetained("", "@1", "782-implement", dir, tracked.pane)
	fx.reader.script(repo, 782, ticketstate.Finished)

	fx.runPass()

	fx.requireClosed(tracked)
	if want := []string{def}; !slices.Equal(fx.fk.sockets, want) {
		t.Errorf("kill-window sockets = %q, want one kill on the tracked window's recorded socket %q", fx.fk.sockets, want)
	}
	if want := []string{""}; !slices.Equal(rs.InventorySockets(), want) {
		t.Errorf("inventory sockets = %q, want the default server read once, as %q", rs.InventorySockets(), want)
	}
	fx.requireReads(readCall{Repo: repo, Nums: []int{782}})
	if n := rs.Count(tmuxtest.WindowRead); n != 0 {
		t.Errorf("window re-reads = %d, want 0: the tracked path owns the window", n)
	}
}

// A tracked session on the default server's recorded socket path that is running
// or waiting for input holds its window: the "" inventory's copy of it is the
// tracked path's, never an untracked candidate that bypasses the hold. The control
// is a retained window of another finished ticket on the same server.
func TestRetainedWindowCleanup_BusyTrackedSessionOnTheDefaultSocketPathHoldsItsWindow(t *testing.T) {
	for _, tc := range []struct{ name, eventType string }{
		{"running", "UserPromptSubmit"},
		{"need-input", "PermissionRequest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			daemonTmuxEnv(t, "")
			fx := newWCFixture(t)
			fx.retained()
			def := filepath.Join(trustedSocketDir(t, fx.d), "default")
			dir, repo := fx.addRepo("alpha", "")
			busy := fx.addWindowOnSocket(def, 783, dir)
			fx.addRetained("", "@1", "783-implement", dir, busy.pane)
			control := fx.addRetained("", "@2", "790-implement", dir, "%90")
			fx.reader.script(repo, 783, ticketstate.Finished)
			fx.reader.script(repo, 790, ticketstate.Finished)
			fx.d.handleEvent(ipc.HookEvent{EventType: tc.eventType, SessionID: busy.key, TmuxPane: busy.pane, TmuxSocket: def})

			fx.runPass()

			fx.requireClosed(control)
		})
	}
}

// Control: a socket that is not the default server's is another server and is
// still inventoried on its own, sorted after "" -- the default server's recorded
// path adds no read of its own.
func TestRetainedWindowCleanup_NonDefaultSocketIsStillInventoriedSeparately(t *testing.T) {
	daemonTmuxEnv(t, "")
	fx := newWCFixture(t)
	rs := fx.retained()
	trusted := trustedSocketDir(t, fx.d)
	def, work := filepath.Join(trusted, "default"), filepath.Join(trusted, "work")
	dir, repo := fx.addRepo("alpha", "")
	onDefault := fx.addWindowOnSocket(def, 782, dir)
	fx.addRetained("", "@1", "782-implement", dir, onDefault.pane)
	anchor := fx.addWindowOnSocket(work, 790, dir)
	retained := fx.addRetained(work, "@5", "791-implement", dir, "%50")
	fx.reader.script(repo, 782, ticketstate.Finished)
	fx.reader.script(repo, 790, ticketstate.NotFinished)
	fx.reader.script(repo, 791, ticketstate.Finished)

	fx.runPass()

	if want := []string{"", work}; !slices.Equal(rs.InventorySockets(), want) {
		t.Errorf("inventory sockets = %q, want %q: the default server once, the other server separately", rs.InventorySockets(), want)
	}
	fx.requireClosed(onDefault, retained)
	if slices.Contains(fx.fk.killed, anchor.pane) {
		t.Errorf("windows killed = %v, want the unfinished tracked window %s kept", fx.fk.killed, anchor.pane)
	}
}

// When the trusted socket directory cannot be resolved the default server's path
// is unknown, so the daemon cannot fold it: the guard instead also keys every
// tracked pane under "", so the "" inventory's copy of a tracked window is still
// the tracked path's -- killed once, and held while the session is busy. The
// degraded mode is reported once per pass, whatever -verbose says.
func TestRetainedWindowCleanup_UnresolvableSocketDirKeepsTheTrackedPathsOwnershipOfItsWindow(t *testing.T) {
	for _, tc := range []struct{ name, eventType string }{
		{"idle", ""},
		{"running", "UserPromptSubmit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			daemonTmuxEnv(t, "")
			fx := newWCFixture(t)
			fx.retained()
			def := filepath.Join(trustedSocketDir(t, fx.d), "default")
			dir, repo := fx.addRepo("alpha", "")
			tracked := fx.addWindowOnSocket(def, 783, dir)
			fx.addRetained("", "@1", "783-implement", dir, tracked.pane)
			control := fx.addRetained("", "@2", "790-implement", dir, "%90")
			fx.reader.script(repo, 783, ticketstate.Finished)
			fx.reader.script(repo, 790, ticketstate.Finished)
			if tc.eventType != "" {
				fx.d.handleEvent(ipc.HookEvent{EventType: tc.eventType, SessionID: tracked.key, TmuxPane: tracked.pane, TmuxSocket: def})
			}
			fx.d.tmuxSocketDir = func() (string, error) { return "", errors.New("no tmux tmpdir") }
			logs := captureLog(t)

			fx.runPass()

			want := []wcWindow{control}
			if tc.eventType == "" {
				want = append(want, tracked)
			}
			fx.requireClosed(want...)
			// One line for the pass, not one per tracked pane or per closed candidate:
			// the closes above each ran the apply step.
			if n := strings.Count(logs.String(), unresolvedSocketDirLog); n != 1 {
				t.Errorf("%q logged %d times, want once for the pass, log:\n%s", unresolvedSocketDirLog, n, logs)
			}
			if !strings.Contains(logs.String(), "no tmux tmpdir") {
				t.Errorf("log = %q, want the seam's error carried", logs)
			}
		})
	}
}

// unresolvedSocketDirLog is the line a pass logs when the trusted socket directory
// is needed to name the daemon's own server and cannot be resolved.
const unresolvedSocketDirLog = "window-cleanup: tmux socket directory unresolved"

// A daemon spawned from a pane of another tmux server S inherits $TMUX, so its ""
// server is S, not <trusted dir>/default: a bare `tmux` prefers $TMUX. A session on
// S records S's path, which therefore folds into "" -- a tracked window read as ""
// is read once and killed once, and a busy one holds. Without the fold the ""
// inventory's copy of a tracked window on S was an untracked candidate that
// bypassed the hold.
func TestRetainedWindowCleanup_DaemonSpawnedFromAnotherServerFoldsThatServersPathIntoItsOwn(t *testing.T) {
	for _, tc := range []struct{ name, eventType string }{
		{"idle", ""},
		{"running", "UserPromptSubmit"},
		{"need-input", "PermissionRequest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newWCFixture(t)
			rs := fx.retained()
			s := filepath.Join(trustedSocketDir(t, fx.d), "cenci")
			daemonTmuxEnv(t, s)
			dir, repo := fx.addRepo("alpha", "")
			tracked := fx.addWindowOnSocket(s, 783, dir)
			fx.addRetained("", "@1", "783-implement", dir, tracked.pane)
			control := fx.addRetained("", "@2", "790-implement", dir, "%90")
			fx.reader.script(repo, 783, ticketstate.Finished)
			fx.reader.script(repo, 790, ticketstate.Finished)
			if tc.eventType != "" {
				fx.d.handleEvent(ipc.HookEvent{EventType: tc.eventType, SessionID: tracked.key, TmuxPane: tracked.pane, TmuxSocket: s})
			}

			fx.runPass()

			want := []wcWindow{control}
			if tc.eventType == "" {
				want = append(want, tracked)
			}
			fx.requireClosed(want...)
			if want := []string{""}; !slices.Equal(rs.InventorySockets(), want) {
				t.Errorf("inventory sockets = %q, want the daemon's server read once, as %q", rs.InventorySockets(), want)
			}
			for _, r := range rs.Reads() {
				if r.Kind == tmuxtest.WindowRead && r.Pane == tracked.pane {
					t.Errorf("re-read through %s, want none: the tracked path owns that window", r.Pane)
				}
			}
		})
	}
}

// The mirror of the case above: while the daemon's "" is S, the real default
// server's socket path (<trusted dir>/default) is a DIFFERENT server. It is
// inventoried as its own socket with no folding, and a "" candidate that shares a
// pane id with a pending close recorded on it does not delete that entry.
func TestRetainedWindowCleanup_RealDefaultPathWhileTheDaemonIsOnAnotherServerIsItsOwnServer(t *testing.T) {
	fx := newWCFixture(t)
	rs := fx.retained()
	trusted := trustedSocketDir(t, fx.d)
	s, def := filepath.Join(trusted, "cenci"), filepath.Join(trusted, "default")
	daemonTmuxEnv(t, s)
	dir, repo := fx.addRepo("alpha", "")
	anchor := fx.addWindowOnSocket(def, 790, dir) // records def, so the pass reads it
	onDaemonServer := fx.addRetained("", "@1", "792-implement", dir, "%50")
	onDefault := fx.addRetained(def, "@7", "793-implement", dir, "%70")
	fx.reader.script(repo, 790, ticketstate.NotFinished)
	fx.reader.script(repo, 792, ticketstate.Finished)
	fx.reader.script(repo, 793, ticketstate.Finished)
	// The same pane id on the real default server: another server's close, which the
	// "" window's close must leave registered. Its own spelling of "" is removed.
	for _, key := range []pendingKey{{pane: "%50"}, {socket: def, pane: "%50"}, {socket: s, pane: "%50"}} {
		fx.d.registerPendingClose(ipc.PendingClose{PaneID: key.pane, Socket: key.socket, WindowName: "792-implement"})
	}

	fx.runPass()

	if want := []string{"", def}; !slices.Equal(rs.InventorySockets(), want) {
		t.Errorf("inventory sockets = %q, want %q: the real default path is another server, read as its own socket", rs.InventorySockets(), want)
	}
	fx.requireClosed(onDaemonServer, onDefault)
	if slices.Contains(fx.fk.killed, anchor.pane) {
		t.Errorf("windows killed = %v, want the unfinished tracked window %s kept", fx.fk.killed, anchor.pane)
	}
	for _, key := range []pendingKey{{pane: "%50"}, {socket: s, pane: "%50"}} {
		if _, ok := fx.d.pending[key]; ok {
			t.Errorf("pending-close entry %+v still registered, want it removed with the daemon server's window", key)
		}
	}
	if _, ok := fx.d.pending[pendingKey{socket: def, pane: "%50"}]; !ok {
		t.Error("the pending-close entry of the real default server's %50 was removed by a window of another server, want it kept")
	}
}

// A $TMUX whose first field is not a socket path does not name the server: the
// name falls back to <trusted dir>/default, so the round-1 fold still applies.
func TestRetainedWindowCleanup_TmuxEnvWithoutASocketPathFallsBackToTheDefaultPath(t *testing.T) {
	fx := newWCFixture(t)
	rs := fx.retained()
	def := filepath.Join(trustedSocketDir(t, fx.d), "default")
	t.Setenv("TMUX", ",4321,0")
	dir, repo := fx.addRepo("alpha", "")
	tracked := fx.addWindowOnSocket(def, 782, dir)
	fx.addRetained("", "@1", "782-implement", dir, tracked.pane)
	fx.reader.script(repo, 782, ticketstate.Finished)

	fx.runPass()

	fx.requireClosed(tracked)
	if want := []string{""}; !slices.Equal(rs.InventorySockets(), want) {
		t.Errorf("inventory sockets = %q, want the default server read once, as %q", rs.InventorySockets(), want)
	}
}

// $TMUX alone names the server, so the trusted socket directory is not needed and
// its failing is not reported: the fold still works and no degraded-mode line is
// logged.
func TestRetainedWindowCleanup_TmuxEnvNamingTheServerNeedsNoSocketDirAndLogsNothingWhenItFails(t *testing.T) {
	fx := newWCFixture(t)
	fx.retained()
	s := filepath.Join(trustedSocketDir(t, fx.d), "cenci")
	daemonTmuxEnv(t, s)
	dir, repo := fx.addRepo("alpha", "")
	tracked := fx.addWindowOnSocket(s, 782, dir)
	fx.addRetained("", "@1", "782-implement", dir, tracked.pane)
	fx.reader.script(repo, 782, ticketstate.Finished)
	fx.d.tmuxSocketDir = func() (string, error) { return "", errors.New("no tmux tmpdir") }
	logs := captureLog(t)

	fx.runPass()

	fx.requireClosed(tracked)
	if strings.Contains(logs.String(), unresolvedSocketDirLog) {
		t.Errorf("log = %q, want no degraded-mode line: $TMUX named the server without the directory", logs)
	}
}

// ---------------------------------------------------------------------------
// An exhausted kill bound is reported; a daemon shutdown is not
// ---------------------------------------------------------------------------

// The per-window bound expiring during the process cleanup skips kill-window, and
// nothing retries the window (its registry entry is already gone), so the skip is
// logged whatever -verbose says -- for the retained caller and for a caller with no
// revalidation alike. A daemon-shutdown cancel at the same point stays quiet unless
// -verbose, and also issues no kill-window.
func TestRetainedWindowCleanup_ExpiredBoundIsLoggedWhateverVerboseSaysAndShutdownIsNot(t *testing.T) {
	for _, retained := range []bool{true, false} {
		for _, expire := range []bool{true, false} {
			name := "tracked window"
			if retained {
				name = "retained window"
			}
			if expire {
				name += "/bound expired"
			} else {
				name += "/daemon cancelled"
			}
			t.Run(name, func(t *testing.T) {
				fx := newWCFixture(t)
				fx.d.cfg.Verbose = false
				dir, repo := fx.addRepo("alpha", "")
				var target string
				if retained {
					fx.retained()
					target = fx.addRetained("", "@1", "782-implement", dir, "%1").pane
				} else {
					target = fx.addWindow(782, dir).pane
				}
				fx.reader.script(repo, 782, ticketstate.Finished)
				var jobCtx context.Context
				var stop context.CancelFunc
				fx.d.runOffLoop = func(_ time.Duration, fn func(ctx context.Context)) {
					if expire {
						jobCtx, stop = context.WithTimeout(context.Background(), 250*time.Millisecond)
					} else {
						jobCtx, stop = context.WithCancel(context.Background())
					}
					defer stop()
					fn(jobCtx)
				}
				fx.d.procKiller = &hookProcKiller{
					orderedProcKiller: orderedProcKiller{order: fx.order},
					onKill: func(string) {
						if !expire {
							stop()
						}
						<-jobCtx.Done()
					},
				}
				logs := captureLog(t)

				fx.runPass()

				if events := fx.order.snapshot(); !slices.Equal(events, []string{"processes " + target}) {
					t.Errorf("kill log = %v, want the process cleanup that ran and no kill-window", events)
				}
				if expire {
					requireLogged(t, logs.String(), "not closing", target, context.DeadlineExceeded.Error())
				} else if strings.Contains(logs.String(), "not closing") {
					t.Errorf("log = %q, want a daemon-shutdown cancel quiet without -verbose", logs.String())
				}
			})
		}
	}
}
