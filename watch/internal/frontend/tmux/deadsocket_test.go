package tmux

// Ticket #1462: a non-empty tmux socket whose listing fails is declared dead,
// and its windows and sessions dropped, only on evidence beyond the failed
// listing: (a) its socket file is gone under a directory that still passes the
// trusted-directory check, or (b) its listing has failed continuously for
// -session-ttl while every session on it has been silent for -session-ttl. A
// failed listing on its own still removes nothing (#1429 Q9).
//
// Every case drives Sweep over tmuxtest.MockClient with a stubbed clock and a
// stubbed socketGone (the ambient filesystem must never decide a test: the
// socket paths below look like real trusted-directory paths). The one test of
// the real socketGone default isolates TMUX_TMPDIR in a temp directory.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/config"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	tmuxc "github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

const deadTTL = 2 * time.Hour

var (
	deadT0        = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	errServerGone = errors.New("error connecting to the socket (No such file or directory)")
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// deadFixture is a frontend over a mock with three servers (ambient, A with two
// panes, B with one), a stubbed clock and a stubbed socketGone answering from
// gone (everything is "not gone" until a test says otherwise).
type deadFixture struct {
	f        *Frontend
	mc       *tmuxtest.MockClient
	clock    *fakeClock
	gone     map[string]bool
	sessions map[string]*frontend.SessionState
}

func newDeadFixture(t *testing.T, ttl time.Duration) *deadFixture {
	t.Helper()
	mc := &tmuxtest.MockClient{
		Panes: []tmuxc.PaneInfo{socketPane("bash", "0", "%0", "default task")},
		PanesBySocket: map[string][]tmuxc.PaneInfo{
			sockA: {socketPane("bash", "0", "%1", "alpha one"), socketPane("bash", "1", "%2", "alpha two")},
			sockB: {socketPane("bash", "0", "%3", "beta task")},
		},
	}
	cfg := config.Default()
	cfg.SessionTTL = ttl
	fx := &deadFixture{
		f:        New(cfg, mc),
		mc:       mc,
		clock:    &fakeClock{t: deadT0},
		gone:     map[string]bool{},
		sessions: map[string]*frontend.SessionState{},
	}
	fx.f.now = fx.clock.now
	fx.f.socketGone = func(socket string) bool { return fx.gone[socket] }
	return fx
}

// track tracks a running session's window; the session's last event is "now".
func (fx *deadFixture) track(t *testing.T, id, pane, socket string) *frontend.SessionState {
	t.Helper()
	sess := runningOn(id, pane, socket)
	sess.LastEvent = fx.clock.now()
	touch(fx.f, sess)
	if fx.f.WindowInfo(id) == nil {
		t.Fatalf("setup: session %s was not tracked", id)
	}
	fx.sessions[id] = sess
	return sess
}

func (fx *deadFixture) fail(socket string) {
	if fx.mc.ListErrBySocket == nil {
		fx.mc.ListErrBySocket = map[string]error{}
	}
	fx.mc.ListErrBySocket[socket] = errServerGone
}

func (fx *deadFixture) recoverListing(socket string) { delete(fx.mc.ListErrBySocket, socket) }

// sweep runs one tick and applies the removals to the sessions map, as the
// daemon's runSweep does, so the next tick sees what the daemon would.
func (fx *deadFixture) sweep() []frontend.SweepAction {
	actions := fx.f.Sweep(fx.sessions)
	for _, a := range actions {
		if a.Remove {
			delete(fx.sessions, a.SessionKey)
		}
	}
	return actions
}

// removals maps each removed session key to whether its action carried PaneGone.
func removals(actions []frontend.SweepAction) map[string]bool {
	out := map[string]bool{}
	for _, a := range actions {
		if a.Remove {
			out[a.SessionKey] = a.PaneGone
		}
	}
	return out
}

func anyPaneGone(actions []frontend.SweepAction) bool {
	for _, a := range actions {
		if a.PaneGone {
			return true
		}
	}
	return false
}

// callMark is a snapshot of the mock's call records, to look at one tick's calls.
type callMark struct{ renames, windowOpts, opts int }

func (fx *deadFixture) mark() callMark {
	return callMark{len(fx.mc.Renames), len(fx.mc.WindowOpts), len(fx.mc.Opts)}
}

// requireNoCallsTo fails when any rename, window option or option call made since
// m targeted socket.
func (fx *deadFixture) requireNoCallsTo(t *testing.T, m callMark, socket string) {
	t.Helper()
	for _, r := range fx.mc.Renames[m.renames:] {
		if r.Socket == socket {
			t.Errorf("rename %+v targeted %s", r, socket)
		}
	}
	for _, o := range fx.mc.WindowOpts[m.windowOpts:] {
		if o.Socket == socket {
			t.Errorf("window option %+v targeted %s", o, socket)
		}
	}
	for _, o := range fx.mc.Opts[m.opts:] {
		if o.Socket == socket {
			t.Errorf("option %+v targeted %s", o, socket)
		}
	}
}

// requireTracked fails when the session's window is gone or the tick removed it.
func requireTracked(t *testing.T, f *Frontend, actions []frontend.SweepAction, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if f.WindowInfo(k) == nil {
			t.Errorf("session %s lost its window, want it kept", k)
		}
		if _, ok := removals(actions)[k]; ok {
			t.Errorf("actions = %+v, want session %s kept", actions, k)
		}
	}
}

// requireDead fails unless the tick removed every key without PaneGone and
// discarded its window.
func requireDead(t *testing.T, f *Frontend, actions []frontend.SweepAction, keys ...string) {
	t.Helper()
	got := removals(actions)
	for _, k := range keys {
		paneGone, removed := got[k]
		if !removed {
			t.Errorf("actions = %+v, want session %s removed", actions, k)
		} else if paneGone {
			t.Errorf("actions = %+v, want session %s removed without PaneGone", actions, k)
		}
		if f.WindowInfo(k) != nil {
			t.Errorf("session %s still has a tracked window, want it discarded", k)
		}
	}
	if anyPaneGone(actions) {
		t.Errorf("actions = %+v, want no PaneGone for a dead-socket removal", actions)
	}
}

// AC4, rule (a): the listing fails and the socket file is gone, so one tick drops
// the window and reports the session as removed without PaneGone, with no tmux
// call of any kind reaching the dead socket.
func TestSweepDropsAWindowWhoseSocketFileIsGone(t *testing.T) {
	fx := newDeadFixture(t, deadTTL)
	fx.track(t, "a1", "%1", sockA)
	fx.fail(sockA)
	fx.gone[sockA] = true
	m := fx.mark()

	actions := fx.sweep()

	requireDead(t, fx.f, actions, "a1")
	fx.requireNoCallsTo(t, m, sockA)
}

// AC5: after the tick that declared the socket dead, the next tick makes no
// ListPanes call for it (the mock's ListSockets record), while another server is
// still listed, so the sweep itself still runs.
func TestSweepDoesNotListADeadSocketOnTheNextTick(t *testing.T) {
	fx := newDeadFixture(t, deadTTL)
	fx.track(t, "a1", "%1", sockA)
	fx.track(t, "b", "%3", sockB)
	fx.fail(sockA)
	fx.gone[sockA] = true
	fx.mc.ListSockets = nil
	fx.sweep()
	if !slices.Contains(fx.mc.ListSockets, sockA) {
		t.Fatalf("ListPanes sockets on the declaring tick = %q, want %q attempted first", fx.mc.ListSockets, sockA)
	}

	fx.mc.ListSockets = nil
	fx.sweep()

	if want := []string{sockB}; !slices.Equal(fx.mc.ListSockets, want) {
		t.Errorf("ListPanes sockets on the next tick = %q, want %q", fx.mc.ListSockets, want)
	}
}

// AC6: with the socket file present (not gone), a failing listing alone removes
// nothing even for a session silent far longer than -session-ttl, until the
// failure streak itself reaches -session-ttl.
func TestSweepKeepsASocketWhileItsFailureStreakIsShorterThanTheTTL(t *testing.T) {
	fx := newDeadFixture(t, deadTTL)
	sess := fx.track(t, "a1", "%1", sockA)
	sess.LastEvent = deadT0.Add(-3 * deadTTL)
	fx.fail(sockA)
	m := fx.mark()

	for _, step := range []time.Duration{0, time.Hour, deadTTL - time.Hour - time.Second} {
		fx.clock.advance(step)
		requireTracked(t, fx.f, fx.sweep(), "a1")
	}

	fx.requireNoCallsTo(t, m, sockA)
}

// AC7: the socket file exists, the listing has failed for the full -session-ttl
// and the session has been silent for at least -session-ttl: the session is
// removed on that tick and its window is discarded without tmux calls. One second
// short of the TTL it is still kept, so the boundary is "at least".
func TestSweepExpiresASocketOnceTheFailureStreakAndSilenceReachTheTTL(t *testing.T) {
	fx := newDeadFixture(t, deadTTL)
	sess := fx.track(t, "a1", "%1", sockA)
	sess.LastEvent = deadT0.Add(-deadTTL)
	fx.fail(sockA)

	requireTracked(t, fx.f, fx.sweep(), "a1") // the streak starts here
	fx.clock.advance(deadTTL - time.Second)
	requireTracked(t, fx.f, fx.sweep(), "a1")
	fx.clock.advance(time.Second)
	m := fx.mark()
	actions := fx.sweep()

	requireDead(t, fx.f, actions, "a1")
	fx.requireNoCallsTo(t, m, sockA)
}

// AC8: a streak that has lasted -session-ttl is not enough while a session on the
// socket is within -session-ttl of its last event; every session on it must be
// silent. The same sessions are removed once the last active one has gone quiet
// for the full TTL (the boundary included).
func TestSweepKeepsASocketWhileASessionOnItHadAnEventWithinTheTTL(t *testing.T) {
	tests := []struct {
		name  string
		extra bool // a second, long-silent session on the same socket
	}{
		{"a single recently active session", false},
		{"one recently active and one silent session", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newDeadFixture(t, deadTTL)
			active := fx.track(t, "a1", "%1", sockA)
			keys := []string{"a1"}
			if tc.extra {
				silent := fx.track(t, "a2", "%2", sockA)
				silent.LastEvent = deadT0.Add(-3 * deadTTL)
				keys = append(keys, "a2")
			}
			fx.fail(sockA)
			requireTracked(t, fx.f, fx.sweep(), keys...) // the streak starts here

			fx.clock.advance(deadTTL + 10*time.Minute) // the streak is now longer than the TTL
			active.LastEvent = fx.clock.now().Add(-10 * time.Minute)
			m := fx.mark()
			requireTracked(t, fx.f, fx.sweep(), keys...)
			fx.requireNoCallsTo(t, m, sockA)

			fx.clock.advance(deadTTL - 10*time.Minute) // the active session is now silent for exactly the TTL
			requireDead(t, fx.f, fx.sweep(), keys...)
		})
	}
}

// AC10: a successful listing resets the failure streak, so a later failure has to
// last a full -session-ttl again. At the last "kept" check it is 2h55m since the
// first failure, which would expire the socket without the reset.
func TestASuccessfulListingResetsTheFailureStreak(t *testing.T) {
	fx := newDeadFixture(t, deadTTL)
	sess := fx.track(t, "a1", "%1", sockA)
	sess.LastEvent = deadT0.Add(-3 * deadTTL)

	fx.fail(sockA)
	requireTracked(t, fx.f, fx.sweep(), "a1") // streak starts at T0
	fx.clock.advance(time.Hour)
	fx.recoverListing(sockA)
	requireTracked(t, fx.f, fx.sweep(), "a1") // a successful listing resets it
	fx.clock.advance(5 * time.Minute)
	fx.fail(sockA)
	requireTracked(t, fx.f, fx.sweep(), "a1")  // a new streak starts at T0+1h5m
	fx.clock.advance(deadTTL - 10*time.Minute) // T0+2h55m: 2h55m since the first failure, 1h50m since the new one
	requireTracked(t, fx.f, fx.sweep(), "a1")

	fx.clock.advance(10 * time.Minute) // the new streak has lasted the full TTL
	requireDead(t, fx.f, fx.sweep(), "a1")
}

// Edge: with -session-ttl 0 rule (b) is disabled however long the failure lasts
// and however silent the session is, while rule (a) still declares the socket dead.
func TestSweepWithSessionTTLZeroDisablesOnlyRuleB(t *testing.T) {
	t.Run("rule (b) never fires", func(t *testing.T) {
		fx := newDeadFixture(t, 0)
		sess := fx.track(t, "a1", "%1", sockA)
		sess.LastEvent = deadT0.Add(-1000 * time.Hour)
		fx.fail(sockA)

		for _, step := range []time.Duration{0, 100 * time.Hour, 1000 * time.Hour} {
			fx.clock.advance(step)
			requireTracked(t, fx.f, fx.sweep(), "a1")
		}
	})

	t.Run("rule (a) still fires", func(t *testing.T) {
		fx := newDeadFixture(t, 0)
		fx.track(t, "a1", "%1", sockA)
		fx.fail(sockA)
		fx.gone[sockA] = true

		requireDead(t, fx.f, fx.sweep(), "a1")
	})
}

// Edge: the empty socket (the default server) is out of scope. Its listing keeps
// failing, socketGone would answer "gone" for anything, and the session stays
// tracked for any number of ticks while every tick still lists the default server.
func TestSweepNeverDeclaresTheDefaultServerDead(t *testing.T) {
	fx := newDeadFixture(t, deadTTL)
	sess := fx.track(t, "d", "%0", "")
	sess.LastEvent = deadT0.Add(-1000 * time.Hour)
	fx.f.socketGone = func(string) bool { return true }
	fx.mc.ListErr = errServerGone

	for _, step := range []time.Duration{0, deadTTL, 10 * deadTTL} {
		fx.clock.advance(step)
		fx.mc.ListSockets = nil
		actions := fx.sweep()

		requireTracked(t, fx.f, actions, "d")
		if want := []string{""}; !slices.Equal(fx.mc.ListSockets, want) {
			t.Errorf("ListPanes sockets = %q, want %q: the default server is still listed", fx.mc.ListSockets, want)
		}
	}
}

// Edge: when the trusted directory fails CheckTrustedSocket (here group-writable)
// an absent socket file is no evidence, so rule (a) does not fire and the socket
// is handled only by rule (b). The 0700 control uses the very same default
// socketGone and fires rule (a) on the first tick, so the 0770 case is not
// vacuous. This is the one test that does not stub socketGone: it isolates
// TMUX_TMPDIR in a temp directory instead.
func TestSweepWithAnUntrustedDirectoryFallsBackToRuleB(t *testing.T) {
	for _, tc := range []struct {
		name    string
		perm    os.FileMode
		ruleAOn bool
	}{
		{"control: a private directory fires rule (a)", 0o700, true},
		{"a group-writable directory leaves only rule (b)", 0o770, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMUX_TMPDIR", tmp)
			dir := filepath.Join(tmp, fmt.Sprintf("tmux-%d", os.Getuid()))
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, tc.perm); err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(dir, "work") // no file is ever created at it
			mc := &tmuxtest.MockClient{PanesBySocket: map[string][]tmuxc.PaneInfo{socket: {socketPane("bash", "0", "%1", "task")}}}
			cfg := config.Default()
			cfg.SessionTTL = deadTTL
			f := New(cfg, mc) // the real socketGone default
			clock := &fakeClock{t: deadT0}
			f.now = clock.now
			sess := runningOn("a1", "%1", socket)
			sess.LastEvent = deadT0.Add(-deadTTL)
			touch(f, sess)
			if f.WindowInfo("a1") == nil {
				t.Fatal("setup: the window was not tracked")
			}
			sessions := map[string]*frontend.SessionState{"a1": sess}
			mc.ListErrBySocket = map[string]error{socket: errServerGone}

			actions := f.Sweep(sessions)

			if tc.ruleAOn {
				requireDead(t, f, actions, "a1")
				return
			}
			requireTracked(t, f, actions, "a1")
			clock.advance(deadTTL)
			requireDead(t, f, f.Sweep(sessions), "a1")
		})
	}
}

// Edge: two sockets on one tick. A (file gone, listing fails) loses every window
// and session; B (listing succeeds) is swept normally, so its vanished pane is
// still a pane-gone removal with a restore on B; the ambient server is untouched.
func TestSweepDropsOnlyTheDeadSocketAndSweepsTheOthersNormally(t *testing.T) {
	f, mc, sessions := trackedOnSockets(t)
	f.socketGone = func(socket string) bool { return socket == sockA }
	mc.ListErrBySocket = map[string]error{sockA: errServerGone}
	mc.PanesBySocket[sockB] = nil // B's pane %3 is genuinely gone
	renames, opts, winOpts := len(mc.Renames), len(mc.Opts), len(mc.WindowOpts)

	actions := f.Sweep(sessions)

	got := removals(actions)
	for _, k := range []string{"a1", "a2"} {
		if paneGone, ok := got[k]; !ok || paneGone {
			t.Errorf("actions = %+v, want %s removed without PaneGone", actions, k)
		}
		if f.WindowInfo(k) != nil {
			t.Errorf("%s still has a tracked window", k)
		}
	}
	if paneGone, ok := got["b"]; !ok || !paneGone {
		t.Errorf("actions = %+v, want b removed with PaneGone: B was swept normally", actions)
	}
	if _, ok := got["d"]; ok || f.WindowInfo("d") == nil {
		t.Errorf("actions = %+v, want the default server's session untouched", actions)
	}
	restoredB := false
	for _, r := range mc.Renames[renames:] {
		if r.Socket == sockA {
			t.Errorf("rename %+v targeted the dead socket", r)
		}
		restoredB = restoredB || r.Socket == sockB
	}
	for _, o := range mc.WindowOpts[winOpts:] {
		if o.Socket == sockA {
			t.Errorf("window option %+v targeted the dead socket", o)
		}
	}
	for _, o := range mc.Opts[opts:] {
		if o.Socket == sockA {
			t.Errorf("option %+v targeted the dead socket", o)
		}
	}
	if !restoredB {
		t.Error("no restore reached B: its pane-gone cleanup did not run")
	}
}

// Edge: a window on the dead socket that no session owns (its session handed the
// window off and was removed) is discarded too, so the socket leaves the listing
// set and the next tick lists only the other server.
func TestSweepDiscardsADeadSocketWindowThatHasNoSessionKey(t *testing.T) {
	fx := newDeadFixture(t, deadTTL)
	a1 := fx.track(t, "a1", "%1", sockA)
	fx.track(t, "b", "%3", sockB)
	fx.f.OnSessionHandoff(a1)
	delete(fx.sessions, "a1")
	fx.fail(sockA)
	fx.gone[sockA] = true
	fx.mc.ListSockets = nil
	m := fx.mark()

	actions := fx.sweep()

	if !slices.Contains(fx.mc.ListSockets, sockA) {
		t.Fatalf("ListPanes sockets = %q, want A attempted while its window is still tracked", fx.mc.ListSockets)
	}
	if anyPaneGone(actions) {
		t.Errorf("actions = %+v, want no PaneGone for a dead-socket window", actions)
	}
	fx.requireNoCallsTo(t, m, sockA)

	fx.mc.ListSockets = nil
	fx.sweep()

	if want := []string{sockB}; !slices.Equal(fx.mc.ListSockets, want) {
		t.Errorf("ListPanes sockets on the next tick = %q, want %q: the keyless window must not keep A listed", fx.mc.ListSockets, want)
	}
	requireTracked(t, fx.f, nil, "b")
}

// A session on a dead socket that never got a window (tracking never succeeded,
// as with a forged event) is removed too, and the socket then leaves the set.
func TestSweepRemovesADeadSocketSessionThatNeverGotAWindow(t *testing.T) {
	fx := newDeadFixture(t, deadTTL)
	fx.sessions["ghost"] = runningOn("ghost", "%9", sockA)
	fx.track(t, "b", "%3", sockB)
	fx.fail(sockA)
	fx.gone[sockA] = true

	actions := fx.sweep()

	requireDead(t, fx.f, actions, "ghost")

	fx.mc.ListSockets = nil
	fx.sweep()

	if want := []string{sockB}; !slices.Equal(fx.mc.ListSockets, want) {
		t.Errorf("ListPanes sockets on the next tick = %q, want %q", fx.mc.ListSockets, want)
	}
}

// A dead socket's @cenci-headroom-* variables are dropped without a tmux call:
// the next headroom render sends nothing to the dead server (clearing them there
// would be one failing call per agent).
func TestDeadSocketHeadroomVariablesAreNotClearedOnTheDeadServer(t *testing.T) {
	fx := newDeadFixture(t, deadTTL)
	fx.f.RenderHeadroom(map[string]float64{"claude": 0.5})
	fx.track(t, "a1", "%1", sockA)
	if _, ok := tmuxtest.FindSocketOpt(fx.mc.Opts, sockA, "@cenci-headroom-claude"); !ok {
		t.Fatal("setup: the headroom variable was never set on the socket")
	}
	fx.fail(sockA)
	fx.gone[sockA] = true
	m := fx.mark()

	requireDead(t, fx.f, fx.sweep(), "a1")
	fx.f.RenderHeadroom(map[string]float64{"claude": 0.5})

	fx.requireNoCallsTo(t, m, sockA)
}
