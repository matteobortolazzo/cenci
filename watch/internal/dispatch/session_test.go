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
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/run"
	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
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
}

func (c *sessionProbeController) CurrentSession() (string, error) {
	c.t.Helper()
	c.t.Fatal("dispatch spawn path must never consult ctrl.CurrentSession() -- the ambient fallback stays scoped to the interactive `cenci run` path only (#927)")
	return "", nil
}

func (c *sessionProbeController) IsGroupedSession(session string) (bool, error) { return false, nil }

func (c *sessionProbeController) NewWindow(session, name, shellCommand string) error { return nil }

func (c *sessionProbeController) SetWindowOption(target, key, value string) error { return nil }

func (c *sessionProbeController) HasSession(session string) (bool, error) {
	c.hasSessionCalls = append(c.hasSessionCalls, session)
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

// TestSessionGate_MissingSentinelDirectlyDetectable_ProbeError covers the
// other half of the AC's "missing" classification: a non-empty session for
// which HasSession itself errors (rather than cleanly reporting absent)
// classifies identically -- (false, err) and (false, nil) both land in
// "missing".
func TestSessionGate_MissingSentinelDirectlyDetectable_ProbeError(t *testing.T) {
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
	if !errors.Is(err, ErrSessionMissing) {
		t.Fatalf("applyDispatch err = %v, want errors.Is(err, ErrSessionMissing)", err)
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
// (the process keeps running, blocked on the ticker), while a genuine
// non-sentinel error (a corrupt config reload) still terminates it exactly
// as today.
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

// TestRunLoopNonSentinelErrorStillTerminates is the plain regression pin for
// "every other RunOnce error stays fatal exactly as today": a corrupt
// config.json on RunLoop's very first (pre-ticker) reload must still return
// a non-nil error immediately.
func TestRunLoopNonSentinelErrorStillTerminates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	corruptConfig(t, path)

	err := RunLoop(path, fakeController{}, &fakeMutator{}, time.Hour, io.Discard, "")
	if err == nil {
		t.Fatal("expected RunLoop to return a non-nil error on a non-sentinel reload failure")
	}
}

// -- Dry-run ----------------------------------------------------------------

// TestRunOnceDryRun_NoSessionsConfigured_NoProbesAndUnsetInTable covers the
// AC's "cenci dispatch --dry-run still prints its decision table with no
// sessions configured -- it resolves nothing, probes nothing, and spawns
// nothing" requirement.
func TestRunOnceDryRun_NoSessionsConfigured_NoProbesAndUnsetInTable(t *testing.T) {
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
	if len(decisions) != 1 || decisions[0].Action != ActionDispatch {
		t.Fatalf("decisions = %+v, want a single ActionDispatch decision", decisions)
	}
	if !strings.Contains(buf.String(), "(unset)") {
		t.Errorf("expected the decision table to show the unconfigured session as (unset), got %q", buf.String())
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
