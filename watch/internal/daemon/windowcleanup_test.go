package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	tmuxfe "github.com/matteobortolazzo/cenci/watch/v2/internal/frontend/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

// The window-cleanup pass (#1251) closes the windows of tickets whose work is
// over. It kills irreversibly, so every case below drives the pass across the
// frontend seam (a real tmux frontend over tmuxtest.MockClient, the window
// tracked through handleEvent, @cenci-repo-dir preset as a window option) and
// asserts on what was actually killed. Each negative case carries a *control*
// window in a healthy repo that must be closed: it proves the pass ran, so a
// "nothing was killed" assertion cannot pass vacuously.

// killOrder is a concurrency-safe ordered log shared by the process-killer and
// window-killer fakes, so a test can assert the processes are killed first.
type killOrder struct {
	mu     sync.Mutex
	events []string
}

func (o *killOrder) add(event string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

func (o *killOrder) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.events)
}

func (o *killOrder) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = nil
}

// orderedProcKiller is the window-process killer fake: it records the target it
// was asked to kill into the shared order log.
type orderedProcKiller struct{ order *killOrder }

func (p *orderedProcKiller) KillWindowProcesses(_ context.Context, target string) error {
	p.order.add("processes " + target)
	return nil
}

// orderedWindowKiller wraps the suite's fakeKiller so kill-window shows up in
// the shared order log as well as in fakeKiller.killed.
type orderedWindowKiller struct {
	inner *fakeKiller
	order *killOrder
}

func (k *orderedWindowKiller) KillWindow(target string) error {
	k.order.add("window " + target)
	return k.inner.KillWindow(target)
}

// wcWindow is one tracked agent window of a fixture.
type wcWindow struct {
	key   string // daemon session key
	pane  string
	index string
}

// target is the exact kill-window target the daemon must use for the window: its
// pane id, which tmux neither reuses nor shifts (a "=main:<index>" target is
// re-resolved at kill time and can land on another window).
func (w wcWindow) target() string { return w.pane }

type wcFixture struct {
	t        *testing.T
	d        *Daemon
	mc       *tmuxtest.MockClient
	reader   *fakeTicketReader
	resolver *fakeResolver
	fk       *fakeKiller // nil on the production-runner fixture
	order    *killOrder  // nil on the production-runner fixture
	windows  int
}

// newWCFixture is a synchronous test daemon (newTestDaemon) with recording
// window and process killers and the cleanup seams pointed at the fakes. The
// opt-out reader is the real ticketstate.CleanupEnabled.
func newWCFixture(t *testing.T) *wcFixture {
	t.Helper()
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	fx := newWCFixtureFor(t, d, mc)
	fx.fk = d.killer.(*fakeKiller)
	fx.order = &killOrder{}
	d.killer = &orderedWindowKiller{inner: fx.fk, order: fx.order}
	d.procKiller = &orderedProcKiller{order: fx.order}
	return fx
}

func newWCFixtureFor(t *testing.T, d *Daemon, mc *tmuxtest.MockClient) *wcFixture {
	t.Helper()
	fx := &wcFixture{t: t, d: d, mc: mc, reader: &fakeTicketReader{}, resolver: &fakeResolver{}}
	d.closeGuard = nil
	d.cleanupReader = fx.reader
	d.newCleanupResolver = func() repoResolver { return fx.resolver }
	d.cleanupEnabled = ticketstate.CleanupEnabled
	return fx
}

// addRepo creates a checkout directory (with .cenci/config.json when
// configJSON is non-empty) and registers it with the resolver as acme/<name>.
func (fx *wcFixture) addRepo(name, configJSON string) (string, ticketstate.Repo) {
	fx.t.Helper()
	dir := fx.t.TempDir()
	if configJSON != "" {
		if err := os.MkdirAll(filepath.Join(dir, ".cenci"), 0o755); err != nil {
			fx.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".cenci", "config.json"), []byte(configJSON), 0o644); err != nil {
			fx.t.Fatal(err)
		}
	}
	repo := ticketstate.Repo{Owner: "acme", Name: name}
	fx.resolver.add(dir, repo)
	return dir, repo
}

// addWindow tracks an idle, done agent window named "<ticket>-implement" whose
// @cenci-repo-dir is repoDir ("" leaves the option unset).
func (fx *wcFixture) addWindow(ticket int, repoDir string) wcWindow {
	fx.t.Helper()
	return fx.addNamedWindow(fmt.Sprintf("%d-implement", ticket), repoDir)
}

// addNamedWindow tracks a window the way `cenci run` leaves one: named by the
// launcher with automatic-rename off, the agent's first events delivered.
func (fx *wcFixture) addNamedWindow(name, repoDir string) wcWindow {
	fx.t.Helper()
	idx := fx.windows
	fx.windows++
	w := wcWindow{key: fmt.Sprintf("sess%d", idx), pane: fmt.Sprintf("%%%d", idx), index: strconv.Itoa(idx)}
	fx.mc.Panes = append(fx.mc.Panes, tmux.PaneInfo{
		SessionName: "main", WindowIndex: w.index, WindowName: name, PaneIndex: "0",
		PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: w.pane,
	})
	if fx.mc.WindowOptValues == nil {
		fx.mc.WindowOptValues = make(map[string]string)
	}
	windowTarget := "main:" + w.index
	fx.mc.WindowOptValues[windowTarget+":automatic-rename"] = "off"
	if repoDir != "" {
		fx.mc.WindowOptValues[windowTarget+":"+frontend.RepoDirOption] = repoDir
	}
	fx.event(w, "SessionStart")
	fx.event(w, "Stop")
	return w
}

func (fx *wcFixture) event(w wcWindow, eventType string) {
	fx.d.handleEvent(ipc.HookEvent{EventType: eventType, SessionID: w.key, TmuxPane: w.pane})
}

func (fx *wcFixture) deps() cleanupDeps {
	return cleanupDeps{resolver: fx.d.newCleanupResolver(), enabled: fx.d.cleanupEnabled, reader: fx.d.cleanupReader}
}

// evaluate runs the snapshot (loop side) and evaluate (worker side) steps.
func (fx *wcFixture) evaluate() windowCleanupResult {
	return evaluateWindowCleanup(context.Background(), fx.deps(), fx.d.snapshotCleanupCandidates())
}

// runPass runs one whole pass: snapshot, evaluate, apply.
func (fx *wcFixture) runPass() {
	fx.d.applyWindowCleanup(fx.evaluate())
}

// resetKills forgets the kills recorded so far.
func (fx *wcFixture) resetKills() {
	fx.fk.killed = nil
	fx.order.reset()
}

// requireClosed asserts that exactly want were closed, each exactly once, with
// its processes killed before the window itself (#1253), and that nothing else
// was killed.
func (fx *wcFixture) requireClosed(want ...wcWindow) {
	fx.t.Helper()
	var wantTargets []string
	for _, w := range want {
		wantTargets = append(wantTargets, w.target())
	}
	slices.Sort(wantTargets)
	got := slices.Clone(fx.fk.killed)
	slices.Sort(got)
	if !slices.Equal(got, wantTargets) {
		fx.t.Errorf("killed windows = %v, want exactly %v", got, wantTargets)
	}
	events := fx.order.snapshot()
	if len(events) != 2*len(want) {
		fx.t.Errorf("kill log = %v, want one process kill and one window kill per closed window (%d windows)", events, len(want))
	}
	for _, target := range wantTargets {
		proc, win := slices.Index(events, "processes "+target), slices.Index(events, "window "+target)
		if proc < 0 || win < 0 || proc > win {
			fx.t.Errorf("kill log = %v, want the processes of %s killed before the window", events, target)
		}
	}
}

// requireReads asserts the exact reader calls (repo, sorted ticket numbers).
func (fx *wcFixture) requireReads(want ...readCall) {
	fx.t.Helper()
	got := fx.reader.recorded()
	for i := range got {
		slices.Sort(got[i].Nums)
	}
	if len(got) != len(want) {
		fx.t.Fatalf("reader calls = %+v, want %+v", got, want)
	}
	for _, w := range want {
		if !slices.ContainsFunc(got, func(c readCall) bool { return c.Repo == w.Repo && slices.Equal(c.Nums, w.Nums) }) {
			fx.t.Errorf("reader calls = %+v, want one for %+v", got, w)
		}
	}
}

// ---------------------------------------------------------------------------
// AC-mapped cases
// ---------------------------------------------------------------------------

// Case 1 / AC1: a window whose issue was closed while no lazyboards instance ran
// is closed on the next pass, its processes first, and the close is logged.
func TestWindowCleanup_ClosedIssueWindowIsKilledProcessesFirstAndLogged(t *testing.T) {
	fx := newWCFixture(t)
	dir, repo := fx.addRepo("alpha", "")
	w := fx.addWindow(782, dir)
	fx.reader.script(repo, 782, ticketstate.Finished)
	logs := captureLog(t)

	fx.runPass()

	fx.requireClosed(w)
	fx.requireReads(readCall{Repo: repo, Nums: []int{782}})
	for _, want := range []string{"window-cleanup:", "main:0"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log = %q, want the close logged with %q", logs.String(), want)
		}
	}
}

// Case 2 / AC2: an open issue whose closing PR merged is reported Finished by the
// reader and closed too; the whole repo is read in one batch.
func TestWindowCleanup_OpenIssueWithMergedClosingPrIsKilledInOneBatchedRead(t *testing.T) {
	fx := newWCFixture(t)
	dir, repo := fx.addRepo("alpha", "")
	closedIssue := fx.addWindow(782, dir)
	mergedPR := fx.addWindow(790, dir)
	fx.addWindow(791, dir)
	fx.reader.script(repo, 782, ticketstate.Finished)
	fx.reader.script(repo, 790, ticketstate.Finished)
	fx.reader.script(repo, 791, ticketstate.NotFinished)

	fx.runPass()

	fx.requireClosed(closedIssue, mergedPR)
	fx.requireReads(readCall{Repo: repo, Nums: []int{782, 790, 791}})
}

// Case 3 / AC3: an open issue with no merged closing PR is never closed.
func TestWindowCleanup_NotFinishedTicketWindowIsNeverKilled(t *testing.T) {
	fx := newWCFixture(t)
	dir, repo := fx.addRepo("alpha", "")
	fx.addWindow(783, dir)
	control := fx.addWindow(782, dir)
	fx.reader.script(repo, 783, ticketstate.NotFinished)
	fx.reader.script(repo, 782, ticketstate.Finished)

	fx.runPass()

	fx.requireClosed(control)
}

// Case 4 / AC4: a window that turns running or need-input after the verdict was
// computed (between evaluate and apply) is not closed: busy status is re-checked
// on the loop immediately before the kill.
func TestWindowCleanup_WindowThatTurnsBusyBetweenEvaluateAndApplyIsNotKilled(t *testing.T) {
	for _, tc := range []struct{ name, eventType string }{
		{"running", "UserPromptSubmit"},
		{"need-input", "PermissionRequest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newWCFixture(t)
			dir, repo := fx.addRepo("alpha", "")
			busy := fx.addWindow(783, dir)
			control := fx.addWindow(782, dir)
			fx.reader.script(repo, 783, ticketstate.Finished)
			fx.reader.script(repo, 782, ticketstate.Finished)

			res := fx.evaluate()
			fx.event(busy, tc.eventType)
			fx.d.applyWindowCleanup(res)

			fx.requireClosed(control)
		})
	}
}

// Case 5 / AC5: a window the babysit guard blocks is never closed; the same
// guard unblocked lets the next pass close it.
func TestWindowCleanup_BabysitGuardBlocksTheCloseUntilItClears(t *testing.T) {
	fx := newWCFixture(t)
	dir, repo := fx.addRepo("alpha", "")
	guarded := fx.addWindow(782, dir)
	control := fx.addWindow(790, dir)
	fx.reader.script(repo, 782, ticketstate.Finished)
	fx.reader.script(repo, 790, ticketstate.Finished)
	var asked []string
	blocked := true
	fx.d.closeGuard = func(ticket string) (bool, string) {
		asked = append(asked, ticket)
		return ticket == "782" && blocked, "babysit supervising PR #790, CI not green"
	}

	fx.runPass()

	fx.requireClosed(control)
	if !slices.Contains(asked, "782") {
		t.Errorf("guard asked about %v, want it consulted with ticket 782 from the window name", asked)
	}

	blocked = false
	fx.resetKills()
	fx.runPass()

	fx.requireClosed(guarded, control)
}

// Case 6a / AC6: a read error for one number closes nothing for that number,
// while a finished number in the same repo is closed; the failure is logged.
func TestWindowCleanup_ReadErrorForOneNumberSparesOnlyThatNumber(t *testing.T) {
	fx := newWCFixture(t)
	dir, repo := fx.addRepo("alpha", "")
	fx.addWindow(783, dir)
	finished := fx.addWindow(782, dir)
	fx.reader.scriptErr(repo, 783, errors.New("gh: HTTP 502 for ticket 783"))
	fx.reader.script(repo, 782, ticketstate.Finished)
	logs := captureLog(t)

	fx.runPass()

	fx.requireClosed(finished)
	for _, want := range []string{"window-cleanup:", "gh: HTTP 502 for ticket 783"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log = %q, want the failed read logged with %q", logs.String(), want)
		}
	}
}

// Case 6a' / AC6: the kill gate fails open for every reader answer that is not a
// clean Finished verdict, not only for a read error: an Unknown verdict with no
// error, a number the reader left out of its map, and a Finished verdict that
// carries an error. Only the control window (a clean Finished) may be closed.
func TestWindowCleanup_ReaderAnswerThatIsNotACleanFinishedVerdictClosesNothing(t *testing.T) {
	tests := []struct {
		name    string
		script  func(r *fakeTicketReader, repo ticketstate.Repo, n int)
		wantLog string
	}{
		{"unknown verdict without an error", func(r *fakeTicketReader, repo ticketstate.Repo, n int) {
			r.scriptResult(repo, n, ticketstate.Result{Verdict: ticketstate.Unknown})
		}, "unknown state"},
		{"number missing from the reader's map", func(r *fakeTicketReader, repo ticketstate.Repo, n int) {
			r.omit(repo, n)
		}, "no ticket state returned"},
		{"finished verdict carrying an error", func(r *fakeTicketReader, repo ticketstate.Repo, n int) {
			r.scriptResult(repo, n, ticketstate.Result{Verdict: ticketstate.Finished, Err: errors.New("gh: partial node for ticket")})
		}, "gh: partial node for ticket"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newWCFixture(t)
			dir, repo := fx.addRepo("alpha", "")
			fx.addWindow(783, dir)
			control := fx.addWindow(782, dir)
			tc.script(fx.reader, repo, 783)
			fx.reader.script(repo, 782, ticketstate.Finished)
			logs := captureLog(t)

			fx.runPass()

			fx.requireClosed(control)
			fx.requireReads(readCall{Repo: repo, Nums: []int{782, 783}})
			if !strings.Contains(logs.String(), tc.wantLog) {
				t.Errorf("log = %q, want the non-finished answer logged with %q", logs.String(), tc.wantLog)
			}
		})
	}
}

// Case 6b / AC6: a whole-repo read failure closes nothing in that repo (not even
// a number the script says is finished), is logged, and leaves other repos alone.
func TestWindowCleanup_WholeRepoReadFailureClosesNothingForThatRepo(t *testing.T) {
	fx := newWCFixture(t)
	failingDir, failing := fx.addRepo("beta", "")
	healthyDir, healthy := fx.addRepo("alpha", "")
	fx.addWindow(790, failingDir)
	fx.addWindow(791, failingDir)
	control := fx.addWindow(782, healthyDir)
	fx.reader.script(failing, 790, ticketstate.Finished)
	fx.reader.script(failing, 791, ticketstate.Finished)
	fx.reader.failRepo(failing, errors.New("gh: rate limited for beta"))
	fx.reader.script(healthy, 782, ticketstate.Finished)
	logs := captureLog(t)

	fx.runPass()

	fx.requireClosed(control)
	for _, want := range []string{"window-cleanup:", "gh: rate limited for beta"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log = %q, want the failed repo read logged with %q", logs.String(), want)
		}
	}
}

// Case 7a / AC7: a window without @cenci-repo-dir is never a candidate: it is not
// closed and its ticket number never reaches the reader.
func TestWindowCleanup_WindowWithoutRepoDirIsNeverReadOrKilled(t *testing.T) {
	fx := newWCFixture(t)
	dir, repo := fx.addRepo("alpha", "")
	fx.addWindow(783, "")
	control := fx.addWindow(782, dir)
	fx.reader.script(repo, 783, ticketstate.Finished)
	fx.reader.script(repo, 782, ticketstate.Finished)

	fx.runPass()

	fx.requireClosed(control)
	fx.requireReads(readCall{Repo: repo, Nums: []int{782}})
}

// Case 7b / AC7: a window whose repo cannot be resolved is skipped and never
// closed, whatever the reader would have said.
func TestWindowCleanup_UnresolvableRepoDirWindowIsSkippedAndNeverKilled(t *testing.T) {
	fx := newWCFixture(t)
	dir, repo := fx.addRepo("alpha", "")
	brokenDir := fx.t.TempDir()
	fx.resolver.fail(brokenDir, errors.New("git: not a repository"))
	fx.addWindow(783, brokenDir)
	control := fx.addWindow(782, dir)
	// Were the broken window resolved to any repo, this script would close it.
	fx.reader.script(repo, 783, ticketstate.Finished)
	fx.reader.script(repo, 782, ticketstate.Finished)

	fx.runPass()

	fx.requireClosed(control)
	fx.requireReads(readCall{Repo: repo, Nums: []int{782}})
}

// Case 8a / AC8: windowCleanup.enabled=false makes no GitHub read for the repo
// and closes nothing in it.
func TestWindowCleanup_OptedOutRepoMakesNoReadsAndClosesNothing(t *testing.T) {
	fx := newWCFixture(t)
	optedOutDir, optedOut := fx.addRepo("beta", `{"windowCleanup":{"enabled":false}}`)
	healthyDir, healthy := fx.addRepo("alpha", "")
	fx.addWindow(783, optedOutDir)
	control := fx.addWindow(782, healthyDir)
	fx.reader.script(optedOut, 783, ticketstate.Finished)
	fx.reader.script(healthy, 782, ticketstate.Finished)

	fx.runPass()

	fx.requireClosed(control)
	if n := fx.reader.readsFor(optedOut); n != 0 {
		t.Errorf("reader calls for the opted-out repo = %d, want 0 (calls: %+v)", n, fx.reader.recorded())
	}
	fx.requireReads(readCall{Repo: healthy, Nums: []int{782}})
}

// Case 8b / AC8: a malformed repo config skips that repo for the pass, logs it
// and reads nothing for it, while a second repo is still processed.
func TestWindowCleanup_MalformedRepoConfigSkipsThatRepoAndLogsIt(t *testing.T) {
	fx := newWCFixture(t)
	malformedDir, malformed := fx.addRepo("beta", `{"windowCleanup": {not json`)
	healthyDir, healthy := fx.addRepo("alpha", "")
	fx.addWindow(783, malformedDir)
	control := fx.addWindow(782, healthyDir)
	fx.reader.script(malformed, 783, ticketstate.Finished)
	fx.reader.script(healthy, 782, ticketstate.Finished)
	logs := captureLog(t)

	fx.runPass()

	fx.requireClosed(control)
	if n := fx.reader.readsFor(malformed); n != 0 {
		t.Errorf("reader calls for the malformed-config repo = %d, want 0 (calls: %+v)", n, fx.reader.recorded())
	}
	configPath := filepath.Join(malformedDir, ".cenci", "config.json")
	for _, want := range []string{"window-cleanup:", configPath} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log = %q, want the skipped repo logged with %q", logs.String(), want)
		}
	}
}

// Case 9 / Decisions: a pending-close entry for the same session:index is removed
// when the pass closes the window (so the SessionEnd path cannot kill it again),
// and an entry for a window the pass did not close is kept.
func TestWindowCleanup_CloseRemovesThePendingCloseEntryForThatWindow(t *testing.T) {
	fx := newWCFixture(t)
	dir, repo := fx.addRepo("alpha", "")
	closed := fx.addWindow(782, dir)
	kept := fx.addWindow(783, dir)
	fx.reader.script(repo, 782, ticketstate.Finished)
	fx.reader.script(repo, 783, ticketstate.NotFinished)
	fx.d.registerPendingClose(ipc.PendingClose{PaneID: closed.pane, Session: "main", WindowIndex: closed.index, WindowName: "782-implement"})
	fx.d.registerPendingClose(ipc.PendingClose{PaneID: kept.pane, Session: "main", WindowIndex: kept.index, WindowName: "783-implement"})

	fx.runPass()

	fx.requireClosed(closed)
	if _, ok := fx.d.pending[closed.pane]; ok {
		t.Error("pending-close entry of the closed window still registered, want it removed")
	}
	if _, ok := fx.d.pending[kept.pane]; !ok {
		t.Error("pending-close entry of the window that was not closed was removed, want it kept")
	}

	fx.event(closed, "SessionEnd")
	fx.requireClosed(closed)
}

// Case 10 / AC4 family: a window reused by another pane, or renumbered, between
// snapshot and apply is not closed: the kill must hit the window that was
// evaluated, never whatever now has its index.
func TestWindowCleanup_WindowReusedOrRenumberedBetweenSnapshotAndApplyIsNotKilled(t *testing.T) {
	tests := []struct {
		name   string
		change func(fx *wcFixture, w wcWindow)
	}{
		{"index reused by another pane", func(fx *wcFixture, w wcWindow) {
			fx.mc.Panes[0] = tmux.PaneInfo{
				SessionName: "main", WindowIndex: w.index, WindowName: "999-other", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "⠋ writing tests", PaneID: "%9",
			}
			fx.d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sessNew", TmuxPane: "%9"})
		}},
		{"window renumbered", func(fx *wcFixture, w wcWindow) {
			fx.mc.Panes[0].WindowIndex = "7"
			fx.event(w, "Stop")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newWCFixture(t)
			dir, repo := fx.addRepo("alpha", "")
			moved := fx.addWindow(783, dir)
			control := fx.addWindow(782, dir)
			fx.reader.script(repo, 783, ticketstate.Finished)
			fx.reader.script(repo, 782, ticketstate.Finished)

			res := fx.evaluate()
			tc.change(fx, moved)
			fx.d.applyWindowCleanup(res)

			fx.requireClosed(control)
		})
	}
}

// Unit test, because the tmux frontend reads a window's name and @cenci-repo-dir
// once when it starts tracking, so no event sequence can make WindowInfo report
// a renamed or re-pointed window for the same pane: matches() is the last line of
// defence and is pinned directly. Every field of the evaluated identity must be
// compared.
func TestCleanupCandidate_MatchesOnlyTheExactWindowThatWasEvaluated(t *testing.T) {
	cand := cleanupCandidate{
		session: "main", windowIndex: "3", windowName: "782-implement", paneID: "%4", repoDir: "/work/alpha",
	}
	same := func() *frontend.WindowInfo {
		return &frontend.WindowInfo{Session: "main", WindowIndex: "3", WindowName: "782-implement", PaneID: "%4", RepoDir: "/work/alpha"}
	}
	tests := []struct {
		name   string
		mutate func(wi *frontend.WindowInfo)
		want   bool
	}{
		{"unchanged window", func(*frontend.WindowInfo) {}, true},
		{"window renamed", func(wi *frontend.WindowInfo) { wi.WindowName = "999-other" }, false},
		{"repo dir changed", func(wi *frontend.WindowInfo) { wi.RepoDir = "/work/beta" }, false},
		{"renamed and repo dir changed", func(wi *frontend.WindowInfo) { wi.WindowName = "999-other"; wi.RepoDir = "/work/beta" }, false},
		{"window renumbered", func(wi *frontend.WindowInfo) { wi.WindowIndex = "2" }, false},
		{"session changed", func(wi *frontend.WindowInfo) { wi.Session = "other" }, false},
		{"another pane in the window", func(wi *frontend.WindowInfo) { wi.PaneID = "%9" }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wi := same()
			tc.mutate(wi)
			if got := cand.matches(wi); got != tc.want {
				t.Errorf("matches(%+v) = %v, want %v", *wi, got, tc.want)
			}
		})
	}
	if cand.matches(nil) {
		t.Error("matches(nil) = true, want false for a window that is no longer tracked")
	}
}

// tmuxModel is a tiny tmux server for the stable-target regression tests: it
// resolves a kill target the way tmux does at kill time ("=main:<index>" by
// index, "%N" by pane id) against its *current* windows, and can renumber them
// after a close (renumber-windows on). Everything it kills is recorded by pane
// id, so a test sees which window a target really landed on.
type tmuxModel struct {
	mu            sync.Mutex
	windows       []modelWindow // in index order
	renumber      bool
	killedWindows []string
	killedProcs   []string
}

type modelWindow struct {
	pane  string
	index int
}

// find returns the position of the window target currently names.
func (m *tmuxModel) find(target string) (int, bool) {
	for i, w := range m.windows {
		if target == "=main:"+strconv.Itoa(w.index) || target == w.pane {
			return i, true
		}
	}
	return 0, false
}

func (m *tmuxModel) KillWindowProcesses(_ context.Context, target string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i, ok := m.find(target); ok {
		m.killedProcs = append(m.killedProcs, m.windows[i].pane)
	}
	return nil
}

func (m *tmuxModel) KillWindow(target string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.find(target)
	if !ok {
		return fmt.Errorf("tmux kill-window: %w", tmux.ErrWindowGone)
	}
	m.killedWindows = append(m.killedWindows, m.windows[i].pane)
	m.windows = slices.Delete(m.windows, i, i+1)
	if m.renumber {
		for j := range m.windows {
			m.windows[j].index = j
		}
	}
	return nil
}

// replace puts a freshly created window with the given pane id at the index
// held by the window at position i (the old one closed, the index was reused).
func (m *tmuxModel) replace(i int, pane string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.windows[i].pane = pane
}

// deferredRunner queues the daemon's off-loop kills instead of running them, so a
// test can change the tmux server between the loop scheduling a kill and the kill
// executing -- the real gap, since the process kill waits out a grace period.
type deferredRunner struct{ fns []func(ctx context.Context) }

func (r *deferredRunner) run(_ time.Duration, fn func(ctx context.Context)) {
	r.fns = append(r.fns, fn)
}

func (r *deferredRunner) drain() {
	for _, fn := range r.fns {
		fn(context.Background())
	}
	r.fns = nil
}

// stableTargetFixture tracks three windows (indexes 0, 1, 2) over the model: the
// first two are finished tickets, the third a ticket that is not finished and so
// must never be touched. The returned runner holds the scheduled kills.
func stableTargetFixture(t *testing.T, renumber bool) (fx *wcFixture, model *tmuxModel, runner *deferredRunner, first, second, bystander wcWindow) {
	t.Helper()
	fx = newWCFixture(t)
	dir, repo := fx.addRepo("alpha", "")
	first = fx.addWindow(782, dir)
	second = fx.addWindow(783, dir)
	bystander = fx.addWindow(784, dir)
	fx.reader.script(repo, 782, ticketstate.Finished)
	fx.reader.script(repo, 783, ticketstate.Finished)
	fx.reader.script(repo, 784, ticketstate.NotFinished)
	model = &tmuxModel{renumber: renumber}
	for i, w := range []wcWindow{first, second, bystander} {
		model.windows = append(model.windows, modelWindow{pane: w.pane, index: i})
	}
	fx.d.killer, fx.d.procKiller = model, model
	runner = &deferredRunner{}
	fx.d.runOffLoop = runner.run
	return fx, model, runner, first, second, bystander
}

// schedule runs the loop-side half of a pass with the closes ordered first-window
// first, leaving the kills queued in the runner.
func schedule(fx *wcFixture) {
	res := fx.evaluate()
	slices.SortFunc(res.closable, func(a, b cleanupCandidate) int { return strings.Compare(a.windowIndex, b.windowIndex) })
	fx.d.applyWindowCleanup(res)
}

// With renumber-windows on, the first close shifts the second finished window
// down an index and the bystander into the second's old index. The kills are
// scheduled before either runs, so a target by session:index would make the
// second kill land on the bystander -- an unchecked, possibly busy agent.
func TestWindowCleanup_RenumberingAfterTheFirstCloseNeverRedirectsTheSecondKill(t *testing.T) {
	fx, model, runner, first, second, bystander := stableTargetFixture(t, true)

	schedule(fx)
	runner.drain()

	want := []string{first.pane, second.pane}
	if !slices.Equal(model.killedWindows, want) {
		t.Errorf("windows killed = %v, want exactly %v (bystander is %s)", model.killedWindows, want, bystander.pane)
	}
	if !slices.Equal(model.killedProcs, want) {
		t.Errorf("window processes killed = %v, want exactly %v (bystander is %s)", model.killedProcs, want, bystander.pane)
	}
}

// With renumber-windows off, a finished window closed by hand while its kill is
// pending frees an index that a freshly dispatched agent window takes. The
// pending kill must find its window gone -- counted as closed -- and leave the
// new window alone.
func TestWindowCleanup_IndexReusedByANewWindowBeforeTheKillNeverRedirectsIt(t *testing.T) {
	fx, model, runner, first, _, _ := stableTargetFixture(t, false)
	logs := captureLog(t)

	schedule(fx)
	model.replace(1, "%90") // the second finished window closed; a new agent window got its index
	runner.drain()

	if want := []string{first.pane}; !slices.Equal(model.killedWindows, want) {
		t.Errorf("windows killed = %v, want only %v: the new window %%90 must survive", model.killedWindows, want)
	}
	if want := []string{first.pane}; !slices.Equal(model.killedProcs, want) {
		t.Errorf("window processes killed = %v, want only %v: the new window %%90 must survive", model.killedProcs, want)
	}
	if strings.Contains(logs.String(), "error killing") {
		t.Errorf("log = %q, want a window that is already gone to count as closed, not an error", logs.String())
	}
}

type failingProcKiller struct{ err error }

func (f failingProcKiller) KillWindowProcesses(context.Context, string) error { return f.err }

type failingWindowKiller struct{ err error }

func (f failingWindowKiller) KillWindow(string) error { return f.err }

// A failed process kill and a failed kill-window are each logged under the
// window-cleanup prefix, naming the window; a window that is already gone is
// counted as closed (logged only under -verbose) and is not an error.
func TestWindowCleanup_KillFailuresAreLoggedUnderTheWindowCleanupPrefix(t *testing.T) {
	setup := func(t *testing.T) (*wcFixture, wcWindow) {
		fx := newWCFixture(t)
		dir, repo := fx.addRepo("alpha", "")
		w := fx.addWindow(782, dir)
		fx.reader.script(repo, 782, ticketstate.Finished)
		return fx, w
	}

	t.Run("process kill and kill-window errors", func(t *testing.T) {
		fx, w := setup(t)
		fx.d.procKiller = failingProcKiller{err: errors.New("boom processes")}
		fx.d.killer = failingWindowKiller{err: errors.New("boom window")}
		logs := captureLog(t)

		fx.runPass()

		for _, want := range []string{
			"window-cleanup: error killing processes of main:" + w.index + " (pane " + w.pane + "): boom processes",
			"window-cleanup: error killing main:" + w.index + " (pane " + w.pane + "): boom window",
		} {
			if !strings.Contains(logs.String(), want) {
				t.Errorf("log = %q, want it to contain %q", logs.String(), want)
			}
		}
	})

	t.Run("window already gone", func(t *testing.T) {
		fx, w := setup(t)
		fx.d.cfg.Verbose = true
		fx.d.killer = failingWindowKiller{err: fmt.Errorf("tmux kill-window: %w", tmux.ErrWindowGone)}
		logs := captureLog(t)

		fx.runPass()

		if want := "window-cleanup: main:" + w.index + " (pane " + w.pane + ") already gone, counted as closed"; !strings.Contains(logs.String(), want) {
			t.Errorf("log = %q, want it to contain %q", logs.String(), want)
		}
		if strings.Contains(logs.String(), "error killing") {
			t.Errorf("log = %q, want a gone window not reported as an error", logs.String())
		}
	})
}

// The recorded launch directory is a raw tmux option value, so the failure line
// that prints it quotes it: a newline in it cannot forge a second log line.
func TestWindowCleanup_ResolverFailureLogQuotesTheRecordedDirectory(t *testing.T) {
	fx := newWCFixture(t)
	dir, repo := fx.addRepo("alpha", "")
	forged := "/tmp/x\n2026/01/01 00:00:00 window-cleanup: closing main:0 (forged)"
	fx.resolver.fail(forged, errors.New("git: not a repository"))
	fx.addWindow(783, forged)
	control := fx.addWindow(782, dir)
	fx.reader.script(repo, 782, ticketstate.Finished)
	logs := captureLog(t)

	fx.runPass()

	fx.requireClosed(control)
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "(forged)") && !strings.Contains(line, "skipping windows launched in") {
			t.Errorf("log line %q is forged by the directory's newline, want it quoted", line)
		}
	}
	if want := strconv.Quote(forged); !strings.Contains(logs.String(), want) {
		t.Errorf("log = %q, want the directory quoted as %s", logs.String(), want)
	}
}

// A ticket number with a leading zero in a window name is not the ticket with
// that numeric value: "042-x" must never be read as (and closed on the verdict
// of) ticket 42.
func TestWindowCleanup_LeadingZeroWindowNameIsNotReadAsAnotherTicket(t *testing.T) {
	fx := newWCFixture(t)
	dir, repo := fx.addRepo("alpha", "")
	fx.addNamedWindow("042-notes", dir)
	control := fx.addWindow(782, dir)
	fx.reader.script(repo, 42, ticketstate.Finished)
	fx.reader.script(repo, 782, ticketstate.Finished)

	fx.runPass()

	fx.requireClosed(control)
	fx.requireReads(readCall{Repo: repo, Nums: []int{782}})
}

// ---------------------------------------------------------------------------
// Loop scheduling
// ---------------------------------------------------------------------------

// runLoop runs d.loop on its own goroutine until until() reports true (checked
// every few milliseconds) or maxWait passes, then stops it and waits for it to
// exit, so the caller can read daemon-owned state race-free afterwards. until
// may be nil to run for the full maxWait.
func runLoop(t *testing.T, d *Daemon, until func() bool, maxWait time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = d.loop(ctx, make(chan ipc.AttentionUpdate))
	}()
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) && (until == nil || !until()) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon loop did not stop after its context was cancelled")
	}
}

// The cleanup interval fires the pass: with a short interval the loop reads the
// ticket state and closes the finished ticket's window, processes first.
func TestLoop_WindowCleanupIntervalFiresThePass(t *testing.T) {
	fx := newWCFixture(t)
	dir, repo := fx.addRepo("alpha", "")
	w := fx.addWindow(782, dir)
	fx.reader.script(repo, 782, ticketstate.Finished)
	fx.d.cfg.WindowCleanupInterval = 20 * time.Millisecond

	runLoop(t, fx.d, func() bool { return slices.Contains(fx.order.snapshot(), "window "+w.target()) }, 5*time.Second)

	if fx.reader.readsFor(repo) == 0 {
		t.Fatal("the loop never read the ticket state: the interval did not fire the pass")
	}
	if !slices.Contains(fx.fk.killed, w.target()) {
		t.Fatalf("killed = %v, want %s closed by the pass", fx.fk.killed, w.target())
	}
	events := fx.order.snapshot()
	if proc, win := slices.Index(events, "processes "+w.target()), slices.Index(events, "window "+w.target()); proc < 0 || proc > win {
		t.Errorf("kill log = %v, want the processes killed before the window", events)
	}
}

// listCounter counts the sweep ticks (each calls ListPanes once) safely across the
// loop goroutine and the test goroutine.
type listCounter struct {
	*tmuxtest.MockClient
	lists atomic.Int64
}

func (c *listCounter) ListPanes() ([]tmux.PaneInfo, error) {
	c.lists.Add(1)
	return c.MockClient.ListPanes()
}

// A non-positive interval disables the pass, and the 1s sweep tick alone never
// runs it: with a 10ms sweep and a candidate window that would be closed, the
// reader is never called. The loop runs until it has really completed many sweep
// ticks (bounded by a deadline, not a fixed sleep), which proves it ran.
func TestLoop_WindowCleanupNeverRunsWhenDisabledOrOnTheSweepTick(t *testing.T) {
	const wantSweeps = 20
	for _, tc := range []struct {
		name     string
		interval time.Duration
	}{
		{"zero interval disables the pass", 0},
		{"negative interval disables the pass", -time.Second},
		{"sweep ticks alone do not run a 1h-interval pass", time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.WindowCleanupInterval = tc.interval
			mc := &tmuxtest.MockClient{}
			counter := &listCounter{MockClient: mc}
			d := newDaemon(cfg, tmuxfe.New(cfg, counter), make(chan ipc.HookEvent, 16), &mockReaper{})
			killer := &targetKiller{killed: make(chan string, 8)}
			d.killer = killer
			fx := newWCFixtureFor(t, d, mc)
			dir, repo := fx.addRepo("alpha", "")
			fx.addWindow(782, dir)
			fx.reader.script(repo, 782, ticketstate.Finished)
			listsBefore := counter.lists.Load()

			runLoop(t, d, func() bool { return counter.lists.Load()-listsBefore >= wantSweeps }, 10*time.Second)

			if sweeps := counter.lists.Load() - listsBefore; sweeps < wantSweeps {
				t.Fatalf("only %d sweep ticks ran, want the loop to have run %d 10ms sweeps", sweeps, wantSweeps)
			}
			if calls := fx.reader.recorded(); len(calls) != 0 {
				t.Errorf("reader calls = %+v, want none", calls)
			}
			if len(killer.killed) != 0 {
				t.Errorf("a window was killed (%d kill-window calls), want none", len(killer.killed))
			}
		})
	}
}

// At most one pass is in flight: with the production goroutine runner and a
// reader that blocks, further ticks make no second read. Releasing the reader
// delivers the pass's verdicts back to the loop, which closes the window.
func TestLoop_WindowCleanupBlockedReaderStartsNoSecondPassAndLaterCloses(t *testing.T) {
	const wait = 5 * time.Second
	cfg := testConfig()
	cfg.WindowCleanupInterval = 10 * time.Millisecond
	mc := &tmuxtest.MockClient{}
	d := newDaemon(cfg, tmuxfe.New(cfg, mc), make(chan ipc.HookEvent, 16), &mockReaper{})
	killer := &targetKiller{killed: make(chan string, 64)}
	d.killer = killer
	fx := newWCFixtureFor(t, d, mc)
	dir, repo := fx.addRepo("alpha", "")
	w := fx.addWindow(782, dir)
	fx.reader.script(repo, 782, ticketstate.Finished)
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	fx.reader.beforeRead = func(ctx context.Context) {
		select {
		case <-gate:
		case <-ctx.Done():
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = d.loop(ctx, make(chan ipc.AttentionUpdate))
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(wait)
	for len(fx.reader.recorded()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the first pass never read the ticket state")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // ~20 more cleanup ticks while the read is blocked
	if calls := fx.reader.recorded(); len(calls) != 1 {
		t.Fatalf("reader calls while the first read is blocked = %+v, want exactly 1 (one pass in flight)", calls)
	}
	select {
	case target := <-killer.killed:
		t.Fatalf("kill-window of %s ran before the blocked read returned", target)
	default:
	}

	release()
	select {
	case target := <-killer.killed:
		if target != w.target() {
			t.Errorf("kill-window target = %q, want %q", target, w.target())
		}
	case <-time.After(wait):
		t.Fatal("the released pass's verdict never closed the window")
	}
}
