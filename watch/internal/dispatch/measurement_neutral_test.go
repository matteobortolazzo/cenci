package dispatch

import (
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// Native Claude measurements (#1450/#1451) are display-only: dispatch must
// reach the same decisions whether or not a window carries one (#1452 P11,
// P47). Each scenario runs the dry-run dispatch pass twice over snapshots that
// differ only in native_measurement and asserts identical Decision lists. The
// "with" sample claims 100% of every quota window and 100% of the context, so
// any gate that did read it would flip.
//
// Because dispatch never reads the field, a red run is impossible by design.
// Each scenario therefore also pins the expected Action and Reason as a
// positive control: the five scenarios land on five different gates, so none
// of them can pass vacuously by short-circuiting before the budget, cap or
// sibling logic runs (watch/docs/test-strategy.md).

// neutralWant is one expected decision in a neutrality scenario.
type neutralWant struct {
	number int
	action Action
	reason string
}

// neutralScenario builds a fresh config and fresh deps per run, so the
// "without" and "with" passes never share mutable state.
type neutralScenario struct {
	name string
	cfg  func(t *testing.T) Config
	deps func(now time.Time) dispatchDeps
	want []neutralWant
	// control, when set, asserts the budget path the scenario claims to hit.
	control func(t *testing.T, cfg Config, now time.Time)
}

// neutralNow is the fixed pass clock; the native sample is fresh relative to it.
var neutralNow = time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

// withSnapshot swaps the happy-path deps' snapshot for one carrying the given
// running count and windows.
func withSnapshot(deps dispatchDeps, running int, windows ...watch.WindowState) dispatchDeps {
	deps.Snapshot = snapshot(running, 0, windows...)
	return deps
}

// saturatedNativeMeasurement is a fresh sample claiming the worst case on every
// axis: 100% of both quota windows used and 100% of the context window.
func saturatedNativeMeasurement(now time.Time) *watch.NativeMeasurement {
	percent := 100.0
	tokens := int64(200000)
	window := int64(200000)
	resets := now.Add(time.Hour).Format(time.RFC3339)
	return &watch.NativeMeasurement{
		MeasuredAt: now.Add(-time.Minute).Format(time.RFC3339),
		Source:     "session.measure",
		Context:    &watch.NativeContext{Tokens: &tokens, Window: &window, Percent: &percent},
		Quota: []watch.NativeQuotaWindow{
			{Kind: "five_hour", PercentUsed: &percent, ResetsAt: resets},
			{Kind: "weekly", PercentUsed: &percent, ResetsAt: resets},
		},
		Missing: []string{},
		Invalid: []string{},
	}
}

// addNativeMeasurements stamps the saturated sample on every window of snap.
func addNativeMeasurements(snap *watch.StateSnapshot, now time.Time) {
	for i := range snap.Windows {
		snap.Windows[i].NativeMeasurement = saturatedNativeMeasurement(now)
	}
}

func runNeutralPass(t *testing.T, cfg Config, deps dispatchDeps) []Decision {
	t.Helper()
	stubRunFn(t, func(run.Opts, run.Controller) error {
		t.Error("a dry-run pass must never spawn")
		return nil
	})
	decisions, err := applyDispatch(cfg, deps, fakeController{}, &fakeMutator{}, true, io.Discard, nil)
	if err != nil {
		t.Fatalf("applyDispatch returned unexpected error: %v", err)
	}
	return decisions
}

func TestNativeMeasurementNeutralDecisions(t *testing.T) {
	scenarios := []neutralScenario{
		{
			name: "dispatchable with an unrelated running claude window",
			cfg:  func(*testing.T) Config { return dispatchTestConfig() },
			deps: func(now time.Time) dispatchDeps {
				return withSnapshot(dispatchableDeps(now), 1,
					watch.WindowState{WindowName: "7-implement", Status: "running", Agent: "claude"})
			},
			want: []neutralWant{{42, ActionDispatch, "dispatch"}},
		},
		{
			name: "running count at the concurrency cap",
			cfg: func(*testing.T) Config {
				cfg := dispatchTestConfig()
				cfg.ConcurrencyCap = 1
				return cfg
			},
			deps: func(now time.Time) dispatchDeps {
				return withSnapshot(dispatchableDeps(now), 1,
					watch.WindowState{WindowName: "7-implement", Status: "running", Agent: "claude"})
			},
			want: []neutralWant{{42, ActionSkip, reasonConcurrencyCap}},
		},
		{
			name: "budget exhausted through agentBudgetFloors",
			cfg: func(*testing.T) Config {
				cfg := dispatchTestConfig()
				cfg.AgentBudgetFloors = map[string]float64{"claude": 0}
				return cfg
			},
			deps: func(now time.Time) dispatchDeps {
				return withSnapshot(dispatchableDeps(now), 1,
					watch.WindowState{WindowName: "7-implement", Status: "running", Agent: "claude"})
			},
			want: []neutralWant{{42, ActionSkip, "budget exhausted"}},
			control: func(t *testing.T, cfg Config, now time.Time) {
				t.Helper()
				provider := buildBudgetProvider(cfg, now)
				if _, ok := provider.(FloorProvider); !ok {
					t.Fatalf("scenario must reach the FloorProvider path, got %T", provider)
				}
				if b := provider.Budget("claude"); b.Unlimited || b.Remaining != 0 {
					t.Fatalf("claude budget = %+v, want a floor-pinned zero remaining", b)
				}
			},
		},
		{
			name: "agentLimits usage provider over an empty session dir",
			cfg: func(t *testing.T) Config {
				cfg := dispatchTestConfig()
				cfg.AgentLimits = map[string]AgentLimit{"claude": {FiveHourTokens: 1000, WeeklyTokens: 10000}}
				cfg.ClaudeSessionDir = t.TempDir()
				return cfg
			},
			deps: func(now time.Time) dispatchDeps {
				return withSnapshot(dispatchableDeps(now), 1,
					watch.WindowState{WindowName: "7-implement", Status: "running", Agent: "claude"})
			},
			want: []neutralWant{{42, ActionDispatch, "dispatch"}},
			control: func(t *testing.T, cfg Config, now time.Time) {
				t.Helper()
				budget := buildBudgetProvider(cfg, now)
				provider, ok := budget.(*UsageProvider)
				if !ok {
					t.Fatalf("scenario must reach the UsageProvider path, got %T", budget)
				}
				if got := provider.Headroom()["claude"]; got != 1 {
					t.Fatalf("claude headroom from an empty session dir = %v, want 1 (nothing used; native 100%% must not matter)", got)
				}
			},
		},
		{
			name: "ticket 42 owns a running window that blocks its sibling",
			cfg:  func(*testing.T) Config { return dispatchTestConfig() },
			deps: func(now time.Time) dispatchDeps {
				deps := dispatchableDeps(now)
				deps.Tickets = []Ticket{
					{Repo: "o/r", Number: 42, Labels: nil},
					authorized(Ticket{Repo: "o/r", Number: 43, Title: "Next child", Labels: []string{"Planned"}, Assignees: []string{"octocat"}}),
				}
				deps.Plans = []Plan{
					{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "planned", IsChild: true, ParentID: 40, Sha256: testPlanSha},
					{Repo: "o/r", Path: ".plans/43-y.md", TicketID: 43, Status: "planned", IsChild: true, ParentID: 40, Sha256: testPlanSha},
				}
				return withSnapshot(deps, 1,
					watch.WindowState{WindowName: "42-implement", Status: "running", Agent: "claude"})
			},
			want: []neutralWant{
				{42, ActionSkip, "not Planned"},
				{43, ActionSkip, "waiting on sibling #42"},
			},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			without := runNeutralPass(t, sc.cfg(t), sc.deps(neutralNow))

			withDeps := sc.deps(neutralNow)
			if len(withDeps.Snapshot.Windows) == 0 {
				t.Fatal("scenario must carry at least one window, or the native sample has nowhere to attach")
			}
			addNativeMeasurements(withDeps.Snapshot, neutralNow)
			for _, w := range withDeps.Snapshot.Windows {
				if w.NativeMeasurement == nil {
					t.Fatalf("window %q lost its native measurement; the 'with' pass would be vacuous", w.WindowName)
				}
			}
			cfg := sc.cfg(t)
			if sc.control != nil {
				sc.control(t, cfg, neutralNow)
			}
			with := runNeutralPass(t, cfg, withDeps)

			if !reflect.DeepEqual(without, with) {
				t.Fatalf("native measurements changed dispatch decisions\nwithout: %+v\nwith:    %+v", without, with)
			}

			if len(with) != len(sc.want) {
				t.Fatalf("decisions = %+v, want %d entries", with, len(sc.want))
			}
			for i, want := range sc.want {
				got := with[i]
				if got.Ticket.Number != want.number || got.Action != want.action || got.Reason != want.reason {
					t.Errorf("decision[%d] = #%d %s %q, want #%d %s %q",
						i, got.Ticket.Number, got.Action, got.Reason, want.number, want.action, want.reason)
				}
			}
		})
	}
}
