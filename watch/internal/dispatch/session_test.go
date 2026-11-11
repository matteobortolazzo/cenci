package dispatch

// Ticket #927: route each repo's dispatches to its own tmux session
// (repos[].session), and add a per-repo pre-mutation gate (backed by
// run.Controller.HasSession) that skips an entire repo's ActionDispatch
// decisions -- before any label mutation -- when its session is unconfigured
// or absent from tmux. Two new sentinels (ErrSessionUnconfigured,
// ErrSessionMissing) surface the skip; the ambient ctrl.CurrentSession()
// fallback is deleted from the dispatch spawn path only (run.go:163-176, the
// interactive `cenci run` path, is unchanged and out of scope here).
//
// This file is written in the Red (test-first) phase: RepoConfig.Session,
// Config.LegacyTopLevelSession, Config.Path, and the session.go gate/
// sentinels do not exist yet, so this whole package intentionally fails to
// compile until the Green phase lands the schema and gate. That is the
// expected state for this phase -- see the plan's Implementation Order.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// sessionProbeController is a run.Controller for the per-repo session gate
// suite: HasSession classifies session names from a canned live/error map
// and records every call (for probe-scoping assertions), while
// CurrentSession fails the test outright -- the #927 AC's "the dispatch
// spawn path never consults ctrl.CurrentSession()" regression guard. Every
// test in this file that reaches the spawn path uses this controller (not
// fakeController, which is deliberately kept dumb for the unrelated reload
// suite), so a future reintroduction of the ambient fallback into the
// dispatch package fails loudly here.
type sessionProbeController struct {
	t         *testing.T
	live      map[string]bool  // session name -> exists
	probeErrs map[string]error // session name -> HasSession error (takes precedence over live)

	hasSessionCalls []string
	// hasSessionSockets and windowSockets record the tmux socket of every
	// HasSession probe and every NewWindow call, in order (#1431).
	hasSessionSockets []string
	windowSockets     []string
}

func (c *sessionProbeController) CurrentSession(socket string) (string, error) {
	c.t.Helper()
	c.t.Fatal("dispatch spawn path must never consult ctrl.CurrentSession() -- the ambient fallback stays scoped to the interactive `cenci run` path only (#927)")
	return "", nil
}

func (c *sessionProbeController) IsGroupedSession(socket, session string) (bool, error) {
	return false, nil
}

func (c *sessionProbeController) NewWindow(socket, session, name, shellCommand string) error {
	c.windowSockets = append(c.windowSockets, socket)
	return nil
}

func (c *sessionProbeController) SetWindowOption(socket, target, key, value string) error {
	return nil
}

func (c *sessionProbeController) HasSession(socket, session string) (bool, error) {
	c.hasSessionCalls = append(c.hasSessionCalls, session)
	c.hasSessionSockets = append(c.hasSessionSockets, socket)
	if err, ok := c.probeErrs[session]; ok {
		return false, err
	}
	return c.live[session], nil
}

var _ run.Controller = (*sessionProbeController)(nil)

// -- Sentinel directness (watch/docs/error-handling.md rule 1) --------------

// TestSessionGate_UnconfiguredSentinelDirectlyDetectable is the direct
// package-boundary test for ErrSessionUnconfigured: a repo with an empty
// session and >=1 dispatchable decision must return an error satisfying
// errors.Is(err, ErrSessionUnconfigured), independent of the higher-level
// token assertions (TestPassError in combined_test.go).
func TestSessionGate_UnconfiguredSentinelDirectlyDetectable(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("an unconfigured-session repo must never spawn")
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Session: ""}}
	ctrl := &sessionProbeController{t: t, live: map[string]bool{}}
	mut := &fakeMutator{}
	prior := 0

	_, err := applyDispatch(cfg, dispatchableDeps(now), ctrl, mut, false, io.Discard, &prior)
	if !errors.Is(err, ErrSessionUnconfigured) {
		t.Fatalf("applyDispatch err = %v, want errors.Is(err, ErrSessionUnconfigured)", err)
	}
}

// TestSessionGate_MissingSentinelDirectlyDetectable is ErrSessionMissing's
// direct counterpart: a repo with a non-empty session that HasSession
// reports absent must return an error satisfying errors.Is(err,
// ErrSessionMissing).
func TestSessionGate_MissingSentinelDirectlyDetectable(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a repo whose configured session is absent from tmux must never spawn")
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Session: "ghost"}}
	ctrl := &sessionProbeController{t: t, live: map[string]bool{}} // "ghost" absent
	mut := &fakeMutator{}
	prior := 0

	_, err := applyDispatch(cfg, dispatchableDeps(now), ctrl, mut, false, io.Discard, &prior)
	if !errors.Is(err, ErrSessionMissing) {
		t.Fatalf("applyDispatch err = %v, want errors.Is(err, ErrSessionMissing)", err)
	}
}

// TestSessionGate_ProbeFailedSentinelDirectlyDetectable: a non-empty session
// for which HasSession itself errors is "could not check", not "absent"
// (#1464) -- it wraps ErrSessionProbeFailed and never ErrSessionMissing.
func TestSessionGate_ProbeFailedSentinelDirectlyDetectable(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a repo whose session probe errored must never spawn")
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Session: "ghost"}}
	ctrl := &sessionProbeController{t: t, probeErrs: map[string]error{"ghost": errors.New("tmux: no server running on socket")}}
	mut := &fakeMutator{}
	prior := 0

	_, err := applyDispatch(cfg, dispatchableDeps(now), ctrl, mut, false, io.Discard, &prior)
	if !errors.Is(err, ErrSessionProbeFailed) || errors.Is(err, ErrSessionMissing) {
		t.Fatalf("applyDispatch err = %v, want errors.Is(err, ErrSessionProbeFailed) and not ErrSessionMissing", err)
	}
}

// -- Per-repo routing ---------------------------------------------------

// TestApplyDispatchRoutesEachRepoToItsOwnSession covers the AC's core
// routing requirement: two repos carrying two different session values,
// both with a dispatchable decision this pass, spawn into two different
// run.Opts.Session values.
func TestApplyDispatchRoutesEachRepoToItsOwnSession(t *testing.T) {
	var captured []run.Opts
	stubRunFn(t, func(opts run.Opts, _ run.Controller) error {
		captured = append(captured, opts)
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{
		{Repo: "o/a", Session: "session-a"},
		{Repo: "o/b", Session: "session-b"},
	}
	ctrl := &sessionProbeController{t: t, live: map[string]bool{"session-a": true, "session-b": true}}
	mut := &fakeMutator{}
	prior := 0

	deps := dispatchDeps{
		Tickets: []Ticket{
			{Repo: "o/a", Number: 1, Title: "A", Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
			{Repo: "o/b", Number: 2, Title: "B", Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		},
		Plans: []Plan{
			{Repo: "o/a", Path: ".plans/1-a.md", TicketID: 1, Status: "planned"},
			{Repo: "o/b", Path: ".plans/2-b.md", TicketID: 2, Status: "planned"},
		},
		Snapshot:    &watch.StateSnapshot{},
		Now:         now,
		CurrentUser: "octocat",
	}
	authorizedAll(deps.Tickets, deps.Plans)

	if _, err := applyDispatch(cfg, deps, ctrl, mut, false, io.Discard, &prior); err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}

	if len(captured) != 2 {
		t.Fatalf("expected 2 spawns, got %d: %+v", len(captured), captured)
	}
	got := map[string]string{}
	for _, opts := range captured {
		got[opts.WindowTicket] = opts.Session
	}
	if got["1"] != "session-a" || got["2"] != "session-b" {
		t.Errorf("sessions = %+v, want {1: session-a, 2: session-b}", got)
	}
}

// -- Isolation ------------------------------------------------------------

// TestApplyDispatchIsolatesUnconfiguredRepoFromLiveRepo covers the AC's
// isolation requirement: repo A resolves a live session, repo B is
// unconfigured, both have dispatchable decisions this pass -- A spawns and
// claims its label, B does neither, and the pass still returns the session
// sentinel.
func TestApplyDispatchIsolatesUnconfiguredRepoFromLiveRepo(t *testing.T) {
	var captured []run.Opts
	stubRunFn(t, func(opts run.Opts, _ run.Controller) error {
		captured = append(captured, opts)
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{
		{Repo: "o/live", Session: "work"},
		{Repo: "o/unconfigured", Session: ""},
	}
	ctrl := &sessionProbeController{t: t, live: map[string]bool{"work": true}}
	mut := &fakeMutator{}
	prior := 0

	deps := dispatchDeps{
		Tickets: []Ticket{
			{Repo: "o/live", Number: 1, Title: "A", Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
			{Repo: "o/unconfigured", Number: 2, Title: "B", Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		},
		Plans: []Plan{
			{Repo: "o/live", Path: ".plans/1-a.md", TicketID: 1, Status: "planned"},
			{Repo: "o/unconfigured", Path: ".plans/2-b.md", TicketID: 2, Status: "planned"},
		},
		Snapshot:    &watch.StateSnapshot{},
		Now:         now,
		CurrentUser: "octocat",
	}
	authorizedAll(deps.Tickets, deps.Plans)

	_, err := applyDispatch(cfg, deps, ctrl, mut, false, io.Discard, &prior)
	if !errors.Is(err, ErrSessionUnconfigured) {
		t.Fatalf("applyDispatch err = %v, want errors.Is(err, ErrSessionUnconfigured) (the pass still surfaces the skip)", err)
	}

	if len(captured) != 1 || captured[0].WindowTicket != "1" {
		t.Fatalf("expected exactly one spawn for o/live#1, got %+v", captured)
	}
	if len(mut.labelEdits) != 1 || mut.labelEdits[0].repo != "o/live" || mut.labelEdits[0].number != 1 {
		t.Fatalf("expected exactly one label claim for o/live#1, got %+v", mut.labelEdits)
	}
}

// TestApplyDispatchRealErrorSurvivesAlongsideSessionSentinel is the review
// fix regression test: a session-gate sentinel firing for one repo in a pass
// must never discard a real per-ticket spawn error from a different,
// correctly-configured repo in the same pass. Before this fix,
// firstNonNil(sessionErr, firstErr) picked only sessionErr here, silently
// dropping the live repo's spawn failure -- an operator would see only
// "session unconfigured" while a ticket was actually stranded at Working
// with a dead spawn on an unrelated, correctly-configured repo.
func TestApplyDispatchRealErrorSurvivesAlongsideSessionSentinel(t *testing.T) {
	stubRunFn(t, func(opts run.Opts, _ run.Controller) error {
		if opts.WindowTicket == "2" {
			return errors.New("tmux exploded on live repo")
		}
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{
		{Repo: "o/unconfigured", Session: ""},
		{Repo: "o/live", Session: "work"},
	}
	ctrl := &sessionProbeController{t: t, live: map[string]bool{"work": true}}
	mut := &fakeMutator{}
	prior := 0

	deps := dispatchDeps{
		Tickets: []Ticket{
			{Repo: "o/unconfigured", Number: 1, Title: "A", Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
			{Repo: "o/live", Number: 2, Title: "B", Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		},
		Plans: []Plan{
			{Repo: "o/unconfigured", Path: ".plans/1-a.md", TicketID: 1, Status: "planned"},
			{Repo: "o/live", Path: ".plans/2-b.md", TicketID: 2, Status: "planned"},
		},
		Snapshot:    &watch.StateSnapshot{},
		Now:         now,
		CurrentUser: "octocat",
	}
	authorizedAll(deps.Tickets, deps.Plans)

	_, err := applyDispatch(cfg, deps, ctrl, mut, false, io.Discard, &prior)
	if !errors.Is(err, ErrSessionUnconfigured) {
		t.Fatalf("applyDispatch err = %v, want errors.Is(err, ErrSessionUnconfigured) to still hold", err)
	}
	if err == nil || !strings.Contains(err.Error(), "tmux exploded on live repo") {
		t.Fatalf("applyDispatch err = %v, want the real spawn failure from o/live#2 to survive alongside the session sentinel", err)
	}
}

// -- Ambient-fallback regression guard --------------------------------------

// TestApplyDispatchNeverConsultsAmbientCurrentSession is an explicit,
// standalone pin of the #927 AC ("the dispatch spawn path never consults
// ctrl.CurrentSession() -- config is the only source") against the full
// happy path: a fully-configured, live-session repo that actually
// dispatches. Every other test in this file also uses
// sessionProbeController, so a regression here would fail the whole suite,
// but this test names the guard explicitly.
func TestApplyDispatchNeverConsultsAmbientCurrentSession(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error { return nil })

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Session: "work"}}
	ctrl := &sessionProbeController{t: t, live: map[string]bool{"work": true}}
	mut := &fakeMutator{}
	prior := 0

	if _, err := applyDispatch(cfg, dispatchableDeps(now), ctrl, mut, false, io.Discard, &prior); err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}
}

// -- Pre-mutation ordering, both classifications ----------------------------

// TestApplyDispatchResumeGatedByUnconfiguredSession_NoLabelMutation covers
// the AC's "the gate runs before the decision loop's first label mutation"
// requirement for the unconfigured classification: a d.Resume decision in an
// unconfigured repo must leave labels untouched.
func TestApplyDispatchResumeGatedByUnconfiguredSession_NoLabelMutation(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a gated repo must never spawn a resume")
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Session: ""}}
	ctrl := &sessionProbeController{t: t, live: map[string]bool{}}
	mut := &fakeMutator{}
	prior := 0

	_, err := applyDispatch(cfg, resumeDispatchDeps(now), ctrl, mut, false, io.Discard, &prior)
	if !errors.Is(err, ErrSessionUnconfigured) {
		t.Fatalf("applyDispatch err = %v, want errors.Is(err, ErrSessionUnconfigured)", err)
	}
	if len(mut.labelEdits) != 0 {
		t.Errorf("expected zero label mutations (the gate must run before the resume claim), got %+v", mut.labelEdits)
	}
}

// TestApplyDispatchResumeGatedByMissingSession_NoLabelMutation is the
// missing-classification counterpart: a d.Resume decision in a repo whose
// HasSession returns false must also leave labels untouched.
func TestApplyDispatchResumeGatedByMissingSession_NoLabelMutation(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a gated repo must never spawn a resume")
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Session: "ghost"}}
	ctrl := &sessionProbeController{t: t, live: map[string]bool{}}
	mut := &fakeMutator{}
	prior := 0

	_, err := applyDispatch(cfg, resumeDispatchDeps(now), ctrl, mut, false, io.Discard, &prior)
	if !errors.Is(err, ErrSessionMissing) {
		t.Fatalf("applyDispatch err = %v, want errors.Is(err, ErrSessionMissing)", err)
	}
	if len(mut.labelEdits) != 0 {
		t.Errorf("expected zero label mutations (the gate must run before the resume claim), got %+v", mut.labelEdits)
	}
}

// -- Probe scoping ------------------------------------------------------

// TestApplyDispatchNoDispatchableDecisions_ZeroHasSessionProbes covers the
// AC's "the gate fires only for repos that actually have >=1 ActionDispatch
// decision this pass" requirement: a repo with an unset session but zero
// dispatchable decisions must trigger zero HasSession calls and no error.
func TestApplyDispatchNoDispatchableDecisions_ZeroHasSessionProbes(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("no decision this pass should dispatch")
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Session: ""}}
	ctrl := &sessionProbeController{t: t, live: map[string]bool{}}
	mut := &fakeMutator{}
	prior := 0

	// A Backlog ticket never becomes ActionDispatch -- zero ActionDispatch
	// decisions this pass.
	deps := dispatchDeps{
		Tickets:     []Ticket{{Repo: "o/r", Number: 42, Title: "Fix thing", Labels: []string{"Backlog"}, Assignees: []string{"octocat"}}},
		Snapshot:    &watch.StateSnapshot{},
		Now:         now,
		CurrentUser: "octocat",
	}

	if _, err := applyDispatch(cfg, deps, ctrl, mut, false, io.Discard, &prior); err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}
	if len(ctrl.hasSessionCalls) != 0 {
		t.Errorf("expected zero HasSession probes (no dispatchable decision this pass), got %v", ctrl.hasSessionCalls)
	}
}

// TestApplyDispatchGatedRepo_AtMostOneHasSessionProbe covers the AC's "at
// most one HasSession probe per gated repo per pass" requirement: two
// dispatchable decisions in the same repo must still probe HasSession only
// once.
func TestApplyDispatchGatedRepo_AtMostOneHasSessionProbe(t *testing.T) {
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a gated repo must never spawn")
		return nil
	})

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Session: "ghost"}}
	ctrl := &sessionProbeController{t: t, live: map[string]bool{}}
	mut := &fakeMutator{}
	prior := 0

	deps := dispatchDeps{
		Tickets: []Ticket{
			{Repo: "o/r", Number: 1, Title: "A", Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
			{Repo: "o/r", Number: 2, Title: "B", Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		},
		Plans: []Plan{
			{Repo: "o/r", Path: ".plans/1-a.md", TicketID: 1, Status: "planned"},
			{Repo: "o/r", Path: ".plans/2-b.md", TicketID: 2, Status: "planned"},
		},
		Snapshot:    &watch.StateSnapshot{},
		Now:         now,
		CurrentUser: "octocat",
	}

	_, err := applyDispatch(cfg, deps, ctrl, mut, false, io.Discard, &prior)
	if !errors.Is(err, ErrSessionMissing) {
		t.Fatalf("applyDispatch err = %v, want errors.Is(err, ErrSessionMissing)", err)
	}
	if len(ctrl.hasSessionCalls) != 1 {
		t.Errorf("expected exactly one HasSession probe per gated repo per pass, got %d: %v", len(ctrl.hasSessionCalls), ctrl.hasSessionCalls)
	}
}

// -- RunOnce: sentinel outranks a same-pass collection error -----------------

// TestRunOnceSessionSentinelOutranksSamePassCollectionError covers the AC's
// "both sentinels remain discoverable via errors.Is on that returned error
// even when a same-pass collection error ... also occurred" requirement: a
// second, unrelated repo whose .plans directory is unreadable (a genuine
// collectErr) must never mask the session sentinel from the gated repo.
func TestRunOnceSessionSentinelOutranksSamePassCollectionError(t *testing.T) {
	mainSyncGitEnv(t)
	serveChainSnapshot(t, watch.StateSnapshot{}) // reachable, idle daemon, so o/healthy#42 actually reaches ActionDispatch
	local, _ := initOriginAndLocal(t)
	planSha := gitTest(t, local, "rev-parse", "HEAD")
	writePlan(t, local, "42-x.md", "---\nticketId: 42\nstatus: planned\nplanCommitSha: "+planSha+"\n---\nbody\n")

	brokenDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(brokenDir, ".plans"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("an unconfigured-session repo must never spawn")
		return nil
	})

	installFakeGHOnPath(t, `
case "$1 $2" in
  "issue list")
    case "$*" in
      *"o/healthy"*) printf '[{"number":42,"title":"Fix thing","labels":[{"name":"Planned"}],"assignees":[{"login":"octocat"}]}]' ;;
      *) printf '[]' ;;
    esac
    ;;
  "api graphql") printf '`+emptyOpenPRPageJSON+`' ;;
  "api user") printf 'octocat\n' ;;
  *) exit 1 ;;
esac
`)

	cfg := testConfig()
	cfg.Repos = []RepoConfig{
		{Repo: "o/healthy", Dir: local, Session: ""},
		{Repo: "o/broken", Dir: brokenDir},
	}

	var buf bytes.Buffer
	_, err := RunOnce(cfg, fakeController{}, &fakeMutator{}, false, &buf, nil)
	if !errors.Is(err, ErrSessionUnconfigured) {
		t.Fatalf("RunOnce err = %v, want errors.Is(err, ErrSessionUnconfigured) even with a same-pass collection error", err)
	}
}

// -- Loop resilience ------------------------------------------------------

// TestRunLoopSurvivesSessionSentinel_NonSentinelStillTerminates covers the
// AC's loop-resilience requirement in one test: RunLoop's pre-ticker
// immediate dispatchTick call must treat both new sentinels as non-fatal
// (the process keeps running, blocked on the ticker). A genuine non-sentinel
// pass error still terminating the loop is pinned by
// TestDispatchTickFilterRunLoopStaysFatalOnPassError (#1385: a bad config
// reload is no longer such an error).
func TestRunLoopSurvivesSessionSentinel_NonSentinelStillTerminates(t *testing.T) {
	mainSyncGitEnv(t)
	serveChainSnapshot(t, watch.StateSnapshot{}) // reachable, idle daemon, so o/r#42 actually reaches ActionDispatch
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	local, _ := initOriginAndLocal(t)
	planSha := gitTest(t, local, "rev-parse", "HEAD")
	writePlan(t, local, "42-x.md", "---\nticketId: 42\nstatus: planned\nplanCommitSha: "+planSha+"\n---\nbody\n")

	writeFile(t, path, fmt.Sprintf(`{"dispatch": {"repos": [{"repo": "o/r", "dir": %q, "session": ""}]}}`, local))

	installFakeGHOnPath(t, `
case "$1 $2" in
  "issue list") printf '[{"number":42,"title":"Fix thing","labels":[{"name":"Planned"}],"assignees":[{"login":"octocat"}]}]' ;;
  "api graphql") printf '`+emptyOpenPRPageJSON+`' ;;
  "api user") printf 'octocat\n' ;;
  *) exit 1 ;;
esac
`)

	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("an unconfigured-session repo must never spawn")
		return nil
	})

	done := make(chan error, 1)
	go func() {
		done <- RunLoop(path, fakeController{}, &fakeMutator{}, time.Hour, io.Discard, "")
	}()

	select {
	case err := <-done:
		t.Fatalf("RunLoop returned %v after its pre-ticker sentinel tick, want it to keep running (the sentinel is non-fatal)", err)
	case <-time.After(200 * time.Millisecond):
		// Still running -- the sentinel did not terminate the loop.
	}

	// RunLoop is now blocked reading its hour-long ticker; the goroutine is
	// intentionally left running until the test binary exits (RunLoop has no
	// cancellation seam), mirroring this suite's other infinite-loop guards.
}

// -- Dry-run ----------------------------------------------------------------

// TestRunOnceDryRun_NoSessionsConfigured_NoProbesAndUnconfiguredSkip:
// `--dry-run --once` still prints its decision table with no sessions
// configured, probes nothing and spawns nothing, and (#1464) reports the
// config-known `session unconfigured` as the ticket's skip reason.
func TestRunOnceDryRun_NoSessionsConfigured_NoProbesAndUnconfiguredSkip(t *testing.T) {
	mainSyncGitEnv(t)
	serveChainSnapshot(t, watch.StateSnapshot{}) // reachable, idle daemon, so o/r#42 actually reaches ActionDispatch
	local, _ := initOriginAndLocal(t)
	planSha := gitTest(t, local, "rev-parse", "HEAD")
	writePlan(t, local, "42-x.md", "---\nticketId: 42\nstatus: planned\nplanCommitSha: "+planSha+"\n---\nbody\n")

	installFakeGHOnPath(t, runOnceFakeGHWithIdentity(`{"name":"Planned"}`))
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("dry-run must never spawn")
		return nil
	})

	ctrl := &sessionProbeController{t: t, live: map[string]bool{}}
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: local, Session: ""}}

	var buf bytes.Buffer
	decisions, err := RunOnce(cfg, ctrl, &fakeMutator{}, true, &buf, nil)
	if err != nil {
		t.Fatalf("dry-run RunOnce returned unexpected error: %v", err)
	}
	if len(ctrl.hasSessionCalls) != 0 {
		t.Errorf("expected zero HasSession probes on dry-run, got %v", ctrl.hasSessionCalls)
	}
	assertDecisions(t, decisions, []wantDecision{{42, ActionSkip, "session unconfigured", ""}})
	if !strings.Contains(buf.String(), "o/r#42 skip: session unconfigured") {
		t.Errorf("expected the decision table to show the session unconfigured skip, got %q", buf.String())
	}
}

// -- #1464: session eligibility resolved before Decide ----------------------

// recordClears swaps clearLaunchFn for a counter of the tickets it would clear.
func recordClears(t *testing.T) *[]string {
	t.Helper()
	var cleared []string
	restore := clearLaunchFn
	clearLaunchFn = func(o pipeline.ArtifactOpts) error {
		cleared = append(cleared, o.ID)
		return nil
	}
	t.Cleanup(func() { clearLaunchFn = restore })
	return &cleared
}

// TestSessionGate_ThreeDistinctFailures: a missing repos[].session, an absent
// configured session and a HasSession error give three distinct reasons and
// three distinct sentinels, each with zero EditLabels and zero launch-state
// clears for the failed repo. The two tmux remedies name `cenci up` and the
// probed server (#1433), never a bare default-server tmux command.
func TestSessionGate_ThreeDistinctFailures(t *testing.T) {
	const probedSocket = "/s/probed"
	sentinels := []error{ErrSessionUnconfigured, ErrSessionMissing, ErrSessionProbeFailed}
	tests := []struct {
		name      string
		session   string
		ctrl      func(t *testing.T) *sessionProbeController
		reason    string
		sentinel  error
		remedy    string
		forbidden string
		onServer  bool   // the line names `cenci up` and the probed tmux server
		alsoNames string // a second option the line must also offer, when non-empty
	}{
		{"unconfigured", "", func(t *testing.T) *sessionProbeController {
			return &sessionProbeController{t: t}
		}, "session unconfigured", ErrSessionUnconfigured, "set repos[].session in", "", false, ""},
		{"absent", "ghost", func(t *testing.T) *sessionProbeController {
			return &sessionProbeController{t: t, live: map[string]bool{}}
		}, "session absent", ErrSessionMissing, "cenci up", "tmux new-session -d -s", true, "cenci dispatch enroll --session"},
		{"probe error", "ghost", func(t *testing.T) *sessionProbeController {
			return &sessionProbeController{t: t, probeErrs: map[string]error{"ghost": errors.New("tmux: lost server")}}
		}, "session probe failed", ErrSessionProbeFailed, "tmux: lost server", "tmux has-session -t", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TMUX", probedSocket+",4242,0")
			failOnSpawn(t)
			cleared := recordClears(t)
			for _, deps := range []dispatchDeps{dispatchableDeps(time.Now()), resumeDispatchDeps(time.Now())} {
				cfg := testConfig()
				cfg.Path = "/cfg/config.json"
				cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: t.TempDir(), Session: tt.session}}
				mut := &fakeMutator{}
				var buf bytes.Buffer
				decisions, err := applyDispatch(cfg, deps, tt.ctrl(t), mut, false, &buf, nil)
				assertDecisions(t, decisions, []wantDecision{{42, ActionSkip, tt.reason, ""}})
				for _, s := range sentinels {
					if got, want := errors.Is(err, s), s == tt.sentinel; got != want {
						t.Errorf("errors.Is(err, %v) = %v, want %v (err = %v)", s, got, want, err)
					}
				}
				if len(mut.labelEdits) != 0 {
					t.Errorf("EditLabels calls = %+v, want none", mut.labelEdits)
				}
				if !strings.Contains(buf.String(), tt.remedy) {
					t.Errorf("log missing the repo's remedy %q:\n%s", tt.remedy, buf.String())
				}
				if tt.forbidden != "" && strings.Contains(buf.String(), tt.forbidden) {
					t.Errorf("log still prints the default-server command %q:\n%s", tt.forbidden, buf.String())
				}
				if tt.alsoNames != "" && !strings.Contains(buf.String(), tt.alsoNames) {
					t.Errorf("log must also offer %q:\n%s", tt.alsoNames, buf.String())
				}
				if tt.onServer {
					for _, want := range []string{"cenci up", probedSocket} {
						if !strings.Contains(buf.String(), want) {
							t.Errorf("log must name %q:\n%s", want, buf.String())
						}
					}
				}
			}
			if len(*cleared) != 0 {
				t.Errorf("launch-state clears = %v, want none", *cleared)
			}
		})
	}
}

// TestSessionGate_ErrorAndFalseInSamePass: a HasSession error for one repo and
// false for another in the same pass stay two distinct reasons and sentinels.
func TestSessionGate_ErrorAndFalseInSamePass(t *testing.T) {
	failOnSpawn(t)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/a", Session: "a-work"}, {Repo: "o/b", Session: "b-work"}}
	ctrl := &sessionProbeController{t: t, live: map[string]bool{"b-work": false}, probeErrs: map[string]error{"a-work": errors.New("tmux: timeout")}}
	decisions, err := applyDispatch(cfg, plannedAcross(time.Now(), repoTicket{"o/a", 1}, repoTicket{"o/b", 2}), ctrl, &fakeMutator{}, false, io.Discard, nil)
	assertDecisions(t, decisions, []wantDecision{{1, ActionSkip, "session probe failed", ""}, {2, ActionSkip, "session absent", ""}})
	if !errors.Is(err, ErrSessionProbeFailed) || !errors.Is(err, ErrSessionMissing) {
		t.Errorf("err = %v, want both ErrSessionProbeFailed and ErrSessionMissing", err)
	}
	if errors.Is(err, ErrSessionUnconfigured) {
		t.Errorf("err = %v, must not wrap ErrSessionUnconfigured", err)
	}
}

// TestSessionGate_ProbeAndSpawnUseTrimmedConfiguredSession: both the
// eligibility probe and the spawn use the trimmed repos[].session, on the
// resolved launch socket; ctrl.CurrentSession is never called
// (sessionProbeController fails the test if it is).
func TestSessionGate_ProbeAndSpawnUseTrimmedConfiguredSession(t *testing.T) {
	spawns := captureSpawns(t)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Session: "  work  "}}
	ctrl := &sessionProbeController{t: t, live: map[string]bool{"work": true}}
	if _, err := applyDispatch(cfg, dispatchableDeps(time.Now()), ctrl, &fakeMutator{}, false, io.Discard, nil); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got, want := ctrl.hasSessionCalls, []string{"work"}; !reflect.DeepEqual(got, want) {
		t.Errorf("HasSession calls = %v, want %v", got, want)
	}
	if got, want := spawnIdentities(*spawns), []string{"42@work"}; !reflect.DeepEqual(got, want) {
		t.Errorf("spawns = %v, want %v", got, want)
	}
}

// -- #1431: the gate probes the socket the spawn will use ---------------------

// spawnThroughRealRun records each spawn's dispatch-built run.Opts, then runs
// the real launcher over the pass's controller (host launch, no daemon start,
// no user config), so the socket NewWindow receives is the one run.Run itself
// resolved from those Opts.
func spawnThroughRealRun(t *testing.T) *[]run.Opts {
	t.Helper()
	var got []run.Opts
	stubRunFn(t, func(o run.Opts, ctrl run.Controller) error {
		got = append(got, o)
		o.ConfigPath = filepath.Join(t.TempDir(), "none.json")
		o.EnsureDaemon = func() {}
		o.SandboxSet, o.Sandbox = true, false
		return run.Run(o, ctrl)
	})
	return &got
}

// The gate's HasSession probe, run.Opts.TmuxSocket and the NewWindow the real
// launcher then issues all carry one socket, for a pass started inside a tmux
// pane ($TMUX) and for a headless pass (the cenci socket under the trusted
// directory). A divergence would probe one server and spawn on another.
func TestSessionGate_ProbeAndSpawnShareOneSocket(t *testing.T) {
	headlessDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, tmuxEnv, want string
	}{
		{"inside a tmux pane", "/s/a,4242,0", "/s/a"},
		{"headless", "", filepath.Join(headlessDir, fmt.Sprintf("tmux-%d", os.Getuid()), "cenci")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMUX", tc.tmuxEnv)
			t.Setenv("TMUX_TMPDIR", headlessDir)
			spawns := spawnThroughRealRun(t)
			cfg := testConfig()
			cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: t.TempDir(), Session: "work"}}
			ctrl := &sessionProbeController{t: t, live: map[string]bool{"work": true}}

			if _, err := applyDispatch(cfg, dispatchableDeps(time.Now()), ctrl, &fakeMutator{}, false, io.Discard, nil); err != nil {
				t.Fatalf("applyDispatch: %v", err)
			}

			if len(*spawns) != 1 {
				t.Fatalf("spawns = %d, want 1", len(*spawns))
			}
			if got := (*spawns)[0].TmuxSocket; got != tc.want {
				t.Errorf("run.Opts.TmuxSocket = %q, want %q", got, tc.want)
			}
			if want := []string{tc.want}; !reflect.DeepEqual(ctrl.hasSessionSockets, want) {
				t.Errorf("HasSession sockets = %q, want %q", ctrl.hasSessionSockets, want)
			}
			if want := []string{tc.want}; !reflect.DeepEqual(ctrl.windowSockets, want) {
				t.Errorf("NewWindow sockets = %q, want %q", ctrl.windowSockets, want)
			}
		})
	}
}

// $TMUX with an empty socket field cannot name a server. Every probed repo is
// "could not check" (never "absent", never a probe on the ambient server), no
// session is probed, nothing spawns, and the cause is logged once.
func TestSessionGate_UnresolvableSocketIsProbeFailed(t *testing.T) {
	t.Setenv("TMUX", ",4242,0")
	failOnSpawn(t)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: t.TempDir(), Session: "work"}}
	ctrl := &sessionProbeController{t: t, live: map[string]bool{"work": true}}
	var buf bytes.Buffer

	decisions, err := applyDispatch(cfg, dispatchableDeps(time.Now()), ctrl, &fakeMutator{}, false, &buf, nil)

	assertDecisions(t, decisions, []wantDecision{{42, ActionSkip, "session probe failed", ""}})
	if !errors.Is(err, ErrSessionProbeFailed) || errors.Is(err, ErrSessionMissing) {
		t.Errorf("err = %v, want ErrSessionProbeFailed and not ErrSessionMissing", err)
	}
	if len(ctrl.hasSessionCalls) != 0 {
		t.Errorf("HasSession calls = %v, want none: an unresolvable socket is never probed or guessed", ctrl.hasSessionCalls)
	}
	if n := strings.Count(buf.String(), "tmux socket is malformed"); n != 1 {
		t.Errorf("log mentions the unresolvable socket %d times, want once:\n%s", n, buf.String())
	}
}

// A dry run probes nothing, so it resolves no socket either: a broken $TMUX must
// not turn a dry run into a failure.
func TestSessionGate_DryRunResolvesNoSocket(t *testing.T) {
	t.Setenv("TMUX", ",4242,0")
	restore := resolveLaunchSocket
	resolveLaunchSocket = func() (string, error) {
		t.Error("a dry run must not resolve a tmux socket")
		return "", errors.New("unexpected resolve")
	}
	t.Cleanup(func() { resolveLaunchSocket = restore })
	failOnSpawn(t)
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Dir: t.TempDir(), Session: "work"}}
	ctrl := &sessionProbeController{t: t, live: map[string]bool{"work": true}}

	if _, err := applyDispatch(cfg, dispatchableDeps(time.Now()), ctrl, &fakeMutator{}, true, io.Discard, nil); err != nil {
		t.Fatalf("dry-run applyDispatch: %v", err)
	}
	if len(ctrl.hasSessionCalls) != 0 {
		t.Errorf("HasSession calls = %v, want none on a dry run", ctrl.hasSessionCalls)
	}
}

// -- Legacy top-level dispatch.session detection -----------------------------

// TestLoadConfig_LegacyTopLevelSessionDetectedReadOnly covers the AC's
// read-only detection requirement: a present, non-empty top-level
// dispatch.session sets Config.LegacyTopLevelSession, and is never
// back-filled onto any repos[] entry.
func TestLoadConfig_LegacyTopLevelSessionDetectedReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{"dispatch": {"session": "legacy-work", "repos": [{"repo": "o/r", "dir": "/abs/dir"}]}}`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned unexpected error: %v", err)
	}
	if !cfg.LegacyTopLevelSession {
		t.Error("LegacyTopLevelSession = false, want true (a non-empty top-level dispatch.session key is present)")
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].Session != "" {
		t.Errorf("Repos = %+v, want the legacy value NEVER back-filled onto repos[].session", cfg.Repos)
	}
	if cfg.Path != path {
		t.Errorf("Config.Path = %q, want the resolved path %q", cfg.Path, path)
	}
}

// TestLoadConfig_EmptyLegacyTopLevelSessionNotDetected covers Q&A #2: a
// present-but-empty legacy value dropped no real configuration, so it must
// not trigger the deprecation detection.
func TestLoadConfig_EmptyLegacyTopLevelSessionNotDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{"dispatch": {"session": "", "repos": [{"repo": "o/r", "dir": "/abs/dir"}]}}`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned unexpected error: %v", err)
	}
	if cfg.LegacyTopLevelSession {
		t.Error("LegacyTopLevelSession = true, want false (an empty legacy value dropped no real configuration, per Q&A #2)")
	}
}

// TestLoadConfig_WhitespaceOnlyLegacyTopLevelSessionNotDetected extends Q&A
// #2's "empty" case to a whitespace-only value, which is equally devoid of
// real configuration.
func TestLoadConfig_WhitespaceOnlyLegacyTopLevelSessionNotDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{"dispatch": {"session": "   ", "repos": [{"repo": "o/r", "dir": "/abs/dir"}]}}`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned unexpected error: %v", err)
	}
	if cfg.LegacyTopLevelSession {
		t.Error("LegacyTopLevelSession = true, want false (a whitespace-only legacy value is treated the same as empty, per Q&A #2)")
	}
}

// TestRunOnce_LegacyTopLevelSessionLogsDeprecationLine_NeverGates covers the
// AC's "one deprecation line is logged per pass naming those repos and the
// resolved config path. It never reaches LastError" requirement: the legacy
// detection is read-only, so RunOnce's returned error must never wrap either
// session sentinel on account of it alone.
func TestRunOnce_LegacyTopLevelSessionLogsDeprecationLine_NeverGates(t *testing.T) {
	mainSyncGitEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	repoDir := t.TempDir()
	writeFile(t, path, fmt.Sprintf(`{"dispatch": {"session": "legacy-work", "repos": [{"repo": "o/r", "dir": %q}]}}`, repoDir))

	installFakeGHOnPath(t, "printf 'gh unavailable in this test env\\n' >&2\nexit 1\n")
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Fatal("a legacy-only config must never dispatch (no repos[].session was ever back-filled)")
		return nil
	})

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned unexpected error: %v", err)
	}

	var buf bytes.Buffer
	prior := 0
	_, runErr := RunOnce(cfg, fakeController{}, &fakeMutator{}, false, &buf, &prior)
	if runErr == nil {
		t.Fatal("expected RunOnce to return the gh-unavailable collection error")
	}
	if errors.Is(runErr, ErrSessionUnconfigured) || errors.Is(runErr, ErrSessionMissing) {
		t.Errorf("legacy detection is read-only, never a gate -- RunOnce err must not wrap a session sentinel here, got %v", runErr)
	}

	log := buf.String()
	if !strings.Contains(log, "o/r") {
		t.Errorf("expected the deprecation line to name the repo (o/r), got log %q", log)
	}
	if !strings.Contains(log, path) {
		t.Errorf("expected the deprecation line to name the resolved config path (%s), got log %q", path, log)
	}
}

// -- Enroll round-trip -------------------------------------------------------

// TestEnrollRepo_PreservesHandEditedSessionOnExistingEntry covers the AC's
// "a hand-edited session survives a re-enroll" requirement, and (#933) that
// the dir-change call's effective return value reports that preserved
// session.
func TestEnrollRepo_PreservesHandEditedSessionOnExistingEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{"dispatch": {"repos": [{"repo": "o/r", "dir": "/old/dir", "session": "hand-edited"}]}}`)

	changed, effective, err := EnrollRepo(path, RepoIdentity{Repo: "o/r", Dir: "/new/dir"}, "")
	if err != nil {
		t.Fatalf("EnrollRepo returned unexpected error: %v", err)
	}
	if !changed {
		t.Error("changed = false, want true (dir changed)")
	}
	if effective != "hand-edited" {
		t.Errorf("effective = %q, want the hand-edited session preserved across the dir-change call", effective)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned unexpected error: %v", err)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("Repos = %+v, want exactly 1 entry", cfg.Repos)
	}
	if cfg.Repos[0].Dir != "/new/dir" {
		t.Errorf("Dir = %q, want updated to /new/dir", cfg.Repos[0].Dir)
	}
	if cfg.Repos[0].Session != "hand-edited" {
		t.Errorf("Session = %q, want the hand-edited value preserved across re-enroll", cfg.Repos[0].Session)
	}
}

// TestEnrollRepo_TrimsWhitespaceInReportedSession covers a hand-edited config
// entry whose session carries stray whitespace (never written by the CLI,
// which trims before storing, but reachable via a hand edit or external
// sync). EnrollRepo's effective return and QueryEnrollment's Session must
// both report the trimmed form, matching buildSessionByRepo's trimming in
// session.go -- otherwise status/enroll output would claim a repo is
// configured with a session the dispatch gate treats as unconfigured.
func TestEnrollRepo_TrimsWhitespaceInReportedSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{"dispatch": {"repos": [{"repo": "o/r", "dir": "/d", "session": "  padded  "}]}}`)

	_, effective, err := EnrollRepo(path, RepoIdentity{Repo: "o/r", Dir: "/d"}, "")
	if err != nil {
		t.Fatalf("EnrollRepo returned unexpected error: %v", err)
	}
	if effective != "padded" {
		t.Errorf("effective = %q, want whitespace-trimmed %q", effective, "padded")
	}

	got, qerr := QueryEnrollment(path, "o/r")
	if qerr != nil {
		t.Fatalf("QueryEnrollment returned unexpected error: %v", qerr)
	}
	if got.Session != "padded" {
		t.Errorf("QueryEnrollment.Session = %q, want whitespace-trimmed %q", got.Session, "padded")
	}
}

// TestEnrollRepo_NewEntryRoundTripsWithEmptySession covers the AC's "a newly
// enrolled entry round-trips with an empty session" requirement -- this
// test predates #933's --session flag and passes "" (not provided).
func TestEnrollRepo_NewEntryRoundTripsWithEmptySession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	changed, _, err := EnrollRepo(path, RepoIdentity{Repo: "o/r", Dir: "/abs/dir"}, "")
	if err != nil {
		t.Fatalf("EnrollRepo returned unexpected error: %v", err)
	}
	if !changed {
		t.Error("changed = false, want true (fresh enrollment)")
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned unexpected error: %v", err)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("Repos = %+v, want exactly 1 entry", cfg.Repos)
	}
	if cfg.Repos[0].Session != "" {
		t.Errorf("Session = %q, want empty on a freshly enrolled entry (no --session flag in this ticket)", cfg.Repos[0].Session)
	}
}
