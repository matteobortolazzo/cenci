package dispatch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// testConfig has generous caps so no gate trips unless a case sets it.
// PipelineStageGate matches DefaultConfig()'s default-on value (#732) so the
// whole existing gate suite runs with the stage gate live; every existing
// test ticket is StageProbeAbsent (the zero value), which always passes the
// gate, so no existing expectation changes. PlanRefined is pinned to false
// (#828), matching DefaultConfig()'s default-deny value, so the whole
// existing gate suite runs with stage-aware pickup off -- every flag-on
// behavior is opted into explicitly per test.
func testConfig() Config {
	return Config{
		ConcurrencyCap:         10,
		NeedInputThreshold:     100,
		DailyQuota:             100,
		PlanStalenessTolerance: 5,
		DefaultAgent:           "claude",
		PipelineStageGate:      true,
		PlanRefined:            false,
	}
}

// dispatchTestConfig extends testConfig with a single enrolled repo ("o/r")
// carrying a configured session (#927): the per-repo session gate skips every
// ActionDispatch decision for a repo with no configured/live session, so any
// applyDispatch/RunOnce test that expects a real spawn to happen must use
// this fixture instead of the bare testConfig() (which enrolls no repos at
// all). testConfig() itself is intentionally left unchanged -- it is also
// the fixture for the pure-engine suite and RunOnce's empty-pass test, both
// of which must keep exercising zero enrolled repos.
func dispatchTestConfig() Config {
	cfg := testConfig()
	cfg.Repos = []RepoConfig{{Repo: "o/r", Session: "test-session"}}
	return cfg
}

// leanAutonomy builds an Inputs.RepoAutonomy map granting repo the lean
// classification (#851): dispatch.planRefined alone is insufficient to
// authorize unattended planning/re-plan -- every existing PlanRefined: true
// fixture must also carry an explicit lean grant for its own repo to keep
// passing once the autonomy gate exists.
func leanAutonomy(repo string) map[string]RepoAutonomy {
	return map[string]RepoAutonomy{repo: RepoAutonomyLean}
}

func snapshot(running, needInput int, windows ...watch.WindowState) *watch.StateSnapshot {
	return &watch.StateSnapshot{
		Windows: windows,
		Summary: watch.StatusSummary{Running: running, NeedInput: needInput},
	}
}

// baseInputs is the happy path: one Planned ticket #42 with a planned, fresh
// plan and a reachable, idle daemon.
func baseInputs() Inputs {
	return Inputs{
		Tickets:     []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}}},
		Plans:       []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "planned"}},
		Snapshot:    snapshot(0, 0),
		Budgets:     FloorProvider{},
		Now:         time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		CurrentUser: "octocat",
		Config:      testConfig(),
	}
}

type wantDecision struct {
	number    int
	action    Action
	reason    string
	wantAgent string // asserted only when non-empty
}

func assertDecisions(t *testing.T, got []Decision, want []wantDecision) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d decisions, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Ticket.Number != w.number {
			t.Errorf("[%d] number = %d, want %d", i, g.Ticket.Number, w.number)
		}
		if g.Action != w.action {
			t.Errorf("[%d] #%d action = %q, want %q", i, w.number, g.Action, w.action)
		}
		if g.Reason != w.reason {
			t.Errorf("[%d] #%d reason = %q, want %q", i, w.number, g.Reason, w.reason)
		}
		if w.wantAgent != "" && g.Agent != w.wantAgent {
			t.Errorf("[%d] #%d agent = %q, want %q", i, w.number, g.Agent, w.wantAgent)
		}
		if w.action == ActionDispatch && g.Plan == nil {
			t.Errorf("[%d] #%d dispatch decision has nil Plan", i, w.number)
		}
	}
}

func TestDecideGates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(in *Inputs)
		want   []wantDecision
	}{
		{
			name: "happy dispatch",
			want: []wantDecision{{42, ActionDispatch, "dispatch", "claude"}},
		},
		{
			name:   "not Planned",
			mutate: func(in *Inputs) { in.Tickets[0].Labels = nil },
			want:   []wantDecision{{42, ActionSkip, "not Planned", ""}},
		},
		{
			name:   "already Working",
			mutate: func(in *Inputs) { in.Tickets[0].Labels = []string{"Planned", "Working"} },
			want:   []wantDecision{{42, ActionSkip, "already Working", ""}},
		},
		{
			name:   "Blocked",
			mutate: func(in *Inputs) { in.Tickets[0].Labels = []string{"Planned", "Blocked"} },
			want:   []wantDecision{{42, ActionSkip, "blocked", ""}},
		},
		{
			name:   "open PR",
			mutate: func(in *Inputs) { in.Tickets[0].HasOpenPR = true },
			want:   []wantDecision{{42, ActionSkip, "open PR exists", ""}},
		},
		{
			name:   "unassigned",
			mutate: func(in *Inputs) { in.Tickets[0].Assignees = nil },
			want:   []wantDecision{{42, ActionSkip, "unassigned", ""}},
		},
		{
			name:   "assigned to another user",
			mutate: func(in *Inputs) { in.Tickets[0].Assignees = []string{"hubot"} },
			want:   []wantDecision{{42, ActionSkip, "assigned to @hubot", ""}},
		},
		{
			name:   "multiple assignees",
			mutate: func(in *Inputs) { in.Tickets[0].Assignees = []string{"octocat", "hubot"} },
			want:   []wantDecision{{42, ActionSkip, "multiple assignees", ""}},
		},
		{
			name:   "assignment comparison is case insensitive",
			mutate: func(in *Inputs) { in.Tickets[0].Assignees = []string{"OctoCat"} },
			want:   []wantDecision{{42, ActionDispatch, "dispatch", "claude"}},
		},
		{
			name:   "no plan",
			mutate: func(in *Inputs) { in.Plans = nil },
			want:   []wantDecision{{42, ActionSkip, "no plan file", ""}},
		},
		{
			name:   "plan not ready",
			mutate: func(in *Inputs) { in.Plans[0].Status = "draft" },
			want:   []wantDecision{{42, ActionSkip, "plan not ready", ""}},
		},
		{
			name:   "stale plan",
			mutate: func(in *Inputs) { in.Plans[0].CommitsBehind = 10 },
			want:   []wantDecision{{42, ActionSkip, "plan stale, re-plan", ""}},
		},
		{
			name:   "fresh at tolerance boundary dispatches",
			mutate: func(in *Inputs) { in.Plans[0].CommitsBehind = 5 },
			want:   []wantDecision{{42, ActionDispatch, "dispatch", ""}},
		},
		{
			name:   "daemon unreachable",
			mutate: func(in *Inputs) { in.Snapshot = nil },
			want:   []wantDecision{{42, ActionSkip, "daemon unreachable", ""}},
		},
		{
			name: "need-input pause",
			mutate: func(in *Inputs) {
				in.Config.NeedInputThreshold = 1
				in.Snapshot = snapshot(0, 1)
			},
			want: []wantDecision{{42, ActionSkip, "need-input pause", ""}},
		},
		{
			name: "concurrency cap reached",
			mutate: func(in *Inputs) {
				in.Config.ConcurrencyCap = 1
				in.Snapshot = snapshot(1, 0)
			},
			want: []wantDecision{{42, ActionSkip, "concurrency cap reached", ""}},
		},
		{
			name: "daily quota reached",
			mutate: func(in *Inputs) {
				in.Config.DailyQuota = 1
				in.Prior = 1
			},
			want: []wantDecision{{42, ActionSkip, "daily quota reached", ""}},
		},
		{
			name: "quiet hours",
			mutate: func(in *Inputs) {
				in.Config.QuietHours = &QuietHours{StartHour: 22, EndHour: 7}
				in.Now = time.Date(2026, 7, 10, 23, 0, 0, 0, time.UTC)
			},
			want: []wantDecision{{42, ActionSkip, "quiet hours", ""}},
		},
		{
			name:   "budget exhausted",
			mutate: func(in *Inputs) { in.Budgets = FloorProvider{Floors: map[string]float64{"claude": 0}} },
			want:   []wantDecision{{42, ActionSkip, "budget exhausted", ""}},
		},

		// -- #732: persisted pipeline stage gate --------------------------

		{
			name: "stage gate: finalized + present skips with the reset-to-redispatch reason",
			mutate: func(in *Inputs) {
				in.Tickets[0].Stage = "finalized"
				in.Tickets[0].StageProbe = StageProbePresent
			},
			want: []wantDecision{{42, ActionSkip, "pipeline finalized (reset to re-dispatch)", ""}},
		},
		{
			name: "stage gate: executed + present still dispatches (RecoveryRetry path unchanged)",
			mutate: func(in *Inputs) {
				in.Tickets[0].Stage = "executed"
				in.Tickets[0].StageProbe = StageProbePresent
			},
			want: []wantDecision{{42, ActionDispatch, "dispatch", "claude"}},
		},
		{
			name: "stage gate: prepared + present dispatches (not gated)",
			mutate: func(in *Inputs) {
				in.Tickets[0].Stage = "prepared"
				in.Tickets[0].StageProbe = StageProbePresent
			},
			want: []wantDecision{{42, ActionDispatch, "dispatch", "claude"}},
		},
		{
			name: "stage gate: waiting_for_input + present dispatches (not gated)",
			mutate: func(in *Inputs) {
				in.Tickets[0].Stage = "waiting_for_input"
				in.Tickets[0].StageProbe = StageProbePresent
			},
			want: []wantDecision{{42, ActionDispatch, "dispatch", "claude"}},
		},
		{
			name: "stage gate: waiting_for_plan_approval + present dispatches (not gated)",
			mutate: func(in *Inputs) {
				in.Tickets[0].Stage = "waiting_for_plan_approval"
				in.Tickets[0].StageProbe = StageProbePresent
			},
			want: []wantDecision{{42, ActionDispatch, "dispatch", "claude"}},
		},
		{
			name: "stage gate: plan_approved + present dispatches (not gated)",
			mutate: func(in *Inputs) {
				in.Tickets[0].Stage = "plan_approved"
				in.Tickets[0].StageProbe = StageProbePresent
			},
			want: []wantDecision{{42, ActionDispatch, "dispatch", "claude"}},
		},
		{
			name: "stage gate: reviewed + present dispatches (not gated)",
			mutate: func(in *Inputs) {
				in.Tickets[0].Stage = "reviewed"
				in.Tickets[0].StageProbe = StageProbePresent
			},
			want: []wantDecision{{42, ActionDispatch, "dispatch", "claude"}},
		},
		{
			name: "stage gate: probe error skips with the unreadable reason",
			mutate: func(in *Inputs) {
				in.Tickets[0].StageProbe = StageProbeError
			},
			want: []wantDecision{{42, ActionSkip, "pipeline state unreadable", ""}},
		},
		{
			name: "stage gate: explicit absent probe dispatches (matches the zero-value/no-state-file behavior)",
			mutate: func(in *Inputs) {
				in.Tickets[0].StageProbe = StageProbeAbsent
			},
			want: []wantDecision{{42, ActionDispatch, "dispatch", "claude"}},
		},
		{
			name: "stage gate: present + unrecognized stage string defensively skips with the unreadable reason (Q3)",
			mutate: func(in *Inputs) {
				in.Tickets[0].Stage = "bogus"
				in.Tickets[0].StageProbe = StageProbePresent
			},
			want: []wantDecision{{42, ActionSkip, "pipeline state unreadable", ""}},
		},
		{
			name: "stage gate: unrecognized StageProbe enum value skips with its own distinct reason",
			mutate: func(in *Inputs) {
				in.Tickets[0].StageProbe = StageProbe("wat")
			},
			want: []wantDecision{{42, ActionSkip, "pipeline stage probe unrecognized", ""}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInputs()
			if tc.mutate != nil {
				tc.mutate(&in)
			}
			assertDecisions(t, Decide(in), tc.want)
		})
	}
}

func TestDecideAgentRouting(t *testing.T) {
	// agent:<name> label wins.
	in := baseInputs()
	in.Tickets[0].Labels = []string{"Planned"}
	in.Tickets[0].Agent = "codex"
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "codex"}})

	// No label ⇒ config DefaultAgent.
	in = baseInputs()
	in.Config.DefaultAgent = "opencode"
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "opencode"}})
}

func TestDecideAgentFallback(t *testing.T) {
	exhausted := func(agents ...string) BudgetProvider {
		floors := make(map[string]float64, len(agents))
		for _, a := range agents {
			floors[a] = 0
		}
		return FloorProvider{Floors: floors}
	}

	t.Run("primary exhausted falls back to preference list", func(t *testing.T) {
		in := baseInputs()
		in.Budgets = exhausted("claude")
		in.Config.AgentPreference = []string{"claude", "codex"}
		assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "codex"}})
	})

	t.Run("all agents exhausted skips", func(t *testing.T) {
		in := baseInputs()
		in.Budgets = exhausted("claude", "codex")
		in.Config.AgentPreference = []string{"claude", "codex"}
		assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "budget exhausted", ""}})
	})

	t.Run("ticket label overrides default, falls back through prefs", func(t *testing.T) {
		in := baseInputs()
		in.Tickets[0].Agent = "codex"
		in.Budgets = exhausted("codex")
		in.Config.AgentPreference = []string{"claude", "codex"}
		// codex exhausted, falls back to claude (next in pref list not yet tried)
		assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
	})

	t.Run("no preference list uses default agent only", func(t *testing.T) {
		in := baseInputs()
		in.Budgets = exhausted("claude")
		// No AgentPreference set; default agent exhausted → skip
		assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "budget exhausted", ""}})
	})

	t.Run("unlimited agent always passes", func(t *testing.T) {
		in := baseInputs()
		in.Budgets = FloorProvider{} // all unlimited
		in.Config.AgentPreference = []string{"claude", "codex"}
		assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
	})
}

// TestDecideAgentFallbackToOpenCodeWhenPreferredExhausted is a #488
// regression/confirmation test: agentPreference and Decide's budget-fallback
// walk are already fully agent-neutral (opencode is just another string),
// so this is expected to pass without any dispatch package changes — it
// pins that "opencode" specifically (not just an arbitrary agent name)
// survives as the exhausted-fallback target and stays Unlimited absent a
// floor/reader for it.
func TestDecideAgentFallbackToOpenCodeWhenPreferredExhausted(t *testing.T) {
	in := baseInputs()
	in.Budgets = FloorProvider{Floors: map[string]float64{"claude": 0, "codex": 0}}
	in.Config.AgentPreference = []string{"claude", "codex", "opencode"}
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "opencode"}})
}

func TestDecideOrderingDeterminism(t *testing.T) {
	in := baseInputs()
	// Supplied out of order; output must be sorted by ticket number.
	in.Tickets = []Ticket{
		{Repo: "o/r", Number: 99, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		{Repo: "o/r", Number: 7, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		{Repo: "o/r", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
	}
	in.Plans = []Plan{
		{Repo: "o/r", Path: ".plans/99.md", TicketID: 99, Status: "planned"},
		{Repo: "o/r", Path: ".plans/7.md", TicketID: 7, Status: "planned"},
		{Repo: "o/r", Path: ".plans/42.md", TicketID: 42, Status: "planned"},
	}
	got := Decide(in)
	nums := []int{got[0].Ticket.Number, got[1].Ticket.Number, got[2].Ticket.Number}
	want := []int{7, 42, 99}
	for i := range want {
		if nums[i] != want[i] {
			t.Fatalf("order = %v, want %v", nums, want)
		}
	}
}

func TestDecideMultiDispatchRespectsCaps(t *testing.T) {
	in := baseInputs()
	in.Config.ConcurrencyCap = 2
	in.Snapshot = snapshot(0, 0)
	in.Tickets = []Ticket{
		{Repo: "o/r", Number: 1, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		{Repo: "o/r", Number: 2, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		{Repo: "o/r", Number: 3, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
	}
	in.Plans = []Plan{
		{Repo: "o/r", Path: ".plans/1.md", TicketID: 1, Status: "planned"},
		{Repo: "o/r", Path: ".plans/2.md", TicketID: 2, Status: "planned"},
		{Repo: "o/r", Path: ".plans/3.md", TicketID: 3, Status: "planned"},
	}
	// Two dispatch (filling the cap of 2), the third is capped.
	assertDecisions(t, Decide(in), []wantDecision{
		{1, ActionDispatch, "dispatch", ""},
		{2, ActionDispatch, "dispatch", ""},
		{3, ActionSkip, "concurrency cap reached", ""},
	})
}

func TestDecideSiblingSerialization(t *testing.T) {
	// Two children of parent #40; #41 already Working ⇒ #42 waits on it.
	t.Run("sibling working blocks child", func(t *testing.T) {
		in := baseInputs()
		in.Tickets = []Ticket{
			{Repo: "o/r", Number: 41, Labels: []string{"Working"}},
			{Repo: "o/r", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		}
		in.Plans = []Plan{
			{Repo: "o/r", Path: ".plans/41.md", TicketID: 41, Status: "planned", IsChild: true, ParentID: 40},
			{Repo: "o/r", Path: ".plans/42.md", TicketID: 42, Status: "planned", IsChild: true, ParentID: 40},
		}
		assertDecisions(t, Decide(in), []wantDecision{
			{41, ActionSkip, "not Planned", ""},
			{42, ActionSkip, "waiting on sibling #41", ""},
		})
	})

	// A sibling with a running window (no Working label) also blocks.
	t.Run("sibling running window blocks child", func(t *testing.T) {
		in := baseInputs()
		in.Tickets = []Ticket{
			{Repo: "o/r", Number: 41, Labels: nil},
			{Repo: "o/r", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		}
		in.Plans = []Plan{
			{Repo: "o/r", Path: ".plans/41.md", TicketID: 41, Status: "planned", IsChild: true, ParentID: 40},
			{Repo: "o/r", Path: ".plans/42.md", TicketID: 42, Status: "planned", IsChild: true, ParentID: 40},
		}
		in.Snapshot = snapshot(1, 0, watch.WindowState{WindowName: "41-foo", Status: "running"})
		assertDecisions(t, Decide(in), []wantDecision{
			{41, ActionSkip, "not Planned", ""},
			{42, ActionSkip, "waiting on sibling #41", ""},
		})
	})

	// Both children ready ⇒ only one dispatches per pass; the other waits.
	t.Run("only one sibling dispatched per pass", func(t *testing.T) {
		in := baseInputs()
		in.Tickets = []Ticket{
			{Repo: "o/r", Number: 41, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
			{Repo: "o/r", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		}
		in.Plans = []Plan{
			{Repo: "o/r", Path: ".plans/41.md", TicketID: 41, Status: "planned", IsChild: true, ParentID: 40},
			{Repo: "o/r", Path: ".plans/42.md", TicketID: 42, Status: "planned", IsChild: true, ParentID: 40},
		}
		assertDecisions(t, Decide(in), []wantDecision{
			{41, ActionDispatch, "dispatch", ""},
			{42, ActionSkip, "waiting on sibling #41", ""},
		})
	})

	// Children of different parents do not block each other.
	t.Run("different parents both dispatch", func(t *testing.T) {
		in := baseInputs()
		in.Tickets = []Ticket{
			{Repo: "o/r", Number: 41, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
			{Repo: "o/r", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		}
		in.Plans = []Plan{
			{Repo: "o/r", Path: ".plans/41.md", TicketID: 41, Status: "planned", IsChild: true, ParentID: 30},
			{Repo: "o/r", Path: ".plans/42.md", TicketID: 42, Status: "planned", IsChild: true, ParentID: 40},
		}
		assertDecisions(t, Decide(in), []wantDecision{
			{41, ActionDispatch, "dispatch", ""},
			{42, ActionDispatch, "dispatch", ""},
		})
	})

	// A non-child ticket is never gated by the sibling rule.
	t.Run("non-child never blocked", func(t *testing.T) {
		in := baseInputs()
		in.Tickets = []Ticket{
			{Repo: "o/r", Number: 41, Labels: []string{"Working"}},
			{Repo: "o/r", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		}
		in.Plans = []Plan{
			{Repo: "o/r", Path: ".plans/41.md", TicketID: 41, Status: "planned", IsChild: true, ParentID: 40},
			{Repo: "o/r", Path: ".plans/42.md", TicketID: 42, Status: "planned"}, // not a child
		}
		assertDecisions(t, Decide(in), []wantDecision{
			{41, ActionSkip, "not Planned", ""},
			{42, ActionDispatch, "dispatch", ""},
		})
	})

	// A sibling plan whose ticket is gone (closed between fetch and Decide) is
	// not active, so the remaining child proceeds.
	t.Run("closed sibling ticket does not block", func(t *testing.T) {
		in := baseInputs()
		in.Tickets = []Ticket{
			{Repo: "o/r", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		}
		in.Plans = []Plan{
			{Repo: "o/r", Path: ".plans/41.md", TicketID: 41, Status: "planned", IsChild: true, ParentID: 40}, // ticket closed/absent
			{Repo: "o/r", Path: ".plans/42.md", TicketID: 42, Status: "planned", IsChild: true, ParentID: 40},
		}
		assertDecisions(t, Decide(in), []wantDecision{
			{42, ActionDispatch, "dispatch", ""},
		})
	})
}

// TestDecideMultiRepoNoNumberCollision guards the (repo, ticketId) plan key:
// two repos each have issue #42 with distinct plans, and each must match its own
// plan rather than colliding on the bare number.
func TestDecideMultiRepoNoNumberCollision(t *testing.T) {
	in := baseInputs()
	in.Tickets = []Ticket{
		{Repo: "o/a", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
		{Repo: "o/b", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
	}
	in.Plans = []Plan{
		{Repo: "o/a", Path: ".plans/42-a.md", TicketID: 42, Status: "planned"},
		{Repo: "o/b", Path: ".plans/42-b.md", TicketID: 42, Status: "planned"},
	}
	got := Decide(in)
	if len(got) != 2 {
		t.Fatalf("got %d decisions, want 2", len(got))
	}
	for _, d := range got {
		if d.Action != ActionDispatch {
			t.Fatalf("#%d in %s: action %q, want dispatch (reason %q)", d.Ticket.Number, d.Ticket.Repo, d.Action, d.Reason)
		}
		if d.Plan == nil || d.Plan.Repo != d.Ticket.Repo {
			t.Errorf("#%d in %s matched wrong plan: %+v", d.Ticket.Number, d.Ticket.Repo, d.Plan)
		}
	}
}

// TestDecideMultiRepoSiblingsIndependent guards that a Working sibling in one
// repo does not block a same-numbered child of a same-numbered parent in another.
func TestDecideMultiRepoSiblingsIndependent(t *testing.T) {
	in := baseInputs()
	in.Tickets = []Ticket{
		{Repo: "o/a", Number: 41, Labels: []string{"Working"}},                                 // active sibling in repo a
		{Repo: "o/b", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}}, // child in repo b, parent #40
	}
	in.Plans = []Plan{
		{Repo: "o/a", Path: ".plans/41.md", TicketID: 41, Status: "planned", IsChild: true, ParentID: 40},
		{Repo: "o/b", Path: ".plans/42.md", TicketID: 42, Status: "planned", IsChild: true, ParentID: 40},
	}
	assertDecisions(t, Decide(in), []wantDecision{
		{41, ActionSkip, "not Planned", ""},
		{42, ActionDispatch, "dispatch", ""},
	})
}

func TestQuietHoursContains(t *testing.T) {
	wrap := QuietHours{StartHour: 22, EndHour: 7}
	for h, want := range map[int]bool{23: true, 3: true, 6: true, 7: false, 12: false, 22: true, 21: false} {
		now := time.Date(2026, 7, 10, h, 0, 0, 0, time.UTC)
		if got := wrap.Contains(now); got != want {
			t.Errorf("wrap.Contains(hour=%d) = %v, want %v", h, got, want)
		}
	}
	day := QuietHours{StartHour: 9, EndHour: 17}
	for h, want := range map[int]bool{8: false, 9: true, 16: true, 17: false, 20: false} {
		now := time.Date(2026, 7, 10, h, 0, 0, 0, time.UTC)
		if got := day.Contains(now); got != want {
			t.Errorf("day.Contains(hour=%d) = %v, want %v", h, got, want)
		}
	}
	// Disabled window (start == end) never matches.
	off := QuietHours{StartHour: 0, EndHour: 0}
	if off.Contains(time.Date(2026, 7, 10, 3, 0, 0, 0, time.UTC)) {
		t.Error("start==end must disable the window")
	}
}

// -- #732: pipeline stage gate, dedicated tests ------------------------------

// TestTicketStageProbeZeroValueIsAbsent locks in AC1's specific requirement:
// StageProbeAbsent must be the zero value ("") so every pre-#732 Ticket
// construction site (reconcile paths, existing tests) keeps today's
// behavior unchanged without being touched.
func TestTicketStageProbeZeroValueIsAbsent(t *testing.T) {
	var tk Ticket
	if tk.StageProbe != StageProbeAbsent {
		t.Errorf("zero-value Ticket.StageProbe = %q, want StageProbeAbsent", tk.StageProbe)
	}
	if StageProbeAbsent != "" {
		t.Errorf("StageProbeAbsent = %q, want the empty string (the zero value)", string(StageProbeAbsent))
	}
}

// TestDecideStageGateDisabled_KillSwitchDispatchesFinalizedTicket covers the
// AC's kill switch: Config.PipelineStageGate = false must dispatch a
// finalized ticket that would otherwise be gated, without changing any
// other gate's behavior.
func TestDecideStageGateDisabled_KillSwitchDispatchesFinalizedTicket(t *testing.T) {
	in := baseInputs()
	in.Config.PipelineStageGate = false
	in.Tickets[0].Stage = "finalized"
	in.Tickets[0].StageProbe = StageProbePresent

	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
}

// TestDecideStageGate_OrderingSitsBetweenOpenPRAndAssigneeGates proves the
// stage gate is evaluated after the HasOpenPR check but before the assignee
// gate (AC: "immediately after the HasOpenPR check ... before the assignee
// gate"), so the first-failing-gate skip reason names the real cause in
// both directions.
func TestDecideStageGate_OrderingSitsBetweenOpenPRAndAssigneeGates(t *testing.T) {
	t.Run("finalized ticket with an open PR reports open PR exists first", func(t *testing.T) {
		in := baseInputs()
		in.Tickets[0].Stage = "finalized"
		in.Tickets[0].StageProbe = StageProbePresent
		in.Tickets[0].HasOpenPR = true

		assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "open PR exists", ""}})
	})

	t.Run("finalized unassigned ticket reports the stage-gate reason, not unassigned", func(t *testing.T) {
		in := baseInputs()
		in.Tickets[0].Stage = "finalized"
		in.Tickets[0].StageProbe = StageProbePresent
		in.Tickets[0].Assignees = nil

		assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "pipeline finalized (reset to re-dispatch)", ""}})
	})
}

// TestDecideStageGate_Determinism locks in that Decide stays pure with the
// stage-probe fields on Inputs: identical Inputs, run twice, yield an
// identical ordered []Decision.
func TestDecideStageGate_Determinism(t *testing.T) {
	in := baseInputs()
	in.Tickets[0].Stage = "finalized"
	in.Tickets[0].StageProbe = StageProbePresent

	got1 := Decide(in)
	got2 := Decide(in)
	if len(got1) != len(got2) {
		t.Fatalf("non-deterministic decision count: %d vs %d", len(got1), len(got2))
	}
	for i := range got1 {
		if got1[i].Ticket.Number != got2[i].Ticket.Number || got1[i].Action != got2[i].Action || got1[i].Reason != got2[i].Reason {
			t.Errorf("[%d] non-deterministic: %+v vs %+v", i, got1[i], got2[i])
		}
	}
}

// -- #822: local main sync gate, dedicated tests -----------------------------

// TestDecideMainSyncGate_DivergedRepoSkipsEveryTicket covers plan test 13: a
// diverged repo (o/a) must skip EVERY ticket in it with exactly "local main
// diverged" -- including a ticket that would otherwise report "not Planned",
// which locks in that the main-sync gate is evaluated first in decideTicket's
// chain, before the board-state gates. A synced repo (o/b) dispatches
// normally in the same pass, proving the gate is per-repo, not global.
func TestDecideMainSyncGate_DivergedRepoSkipsEveryTicket(t *testing.T) {
	in := baseInputs()
	in.Tickets = []Ticket{
		{Repo: "o/a", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}, MainSync: MainSyncDiverged},
		{Repo: "o/a", Number: 43, Labels: nil, MainSync: MainSyncDiverged}, // would otherwise report "not Planned"
		{Repo: "o/b", Number: 44, Labels: []string{"Planned"}, Assignees: []string{"octocat"}, MainSync: MainSyncSynced},
	}
	in.Plans = []Plan{
		{Repo: "o/a", Path: ".plans/42-x.md", TicketID: 42, Status: "planned"},
		{Repo: "o/b", Path: ".plans/44-x.md", TicketID: 44, Status: "planned"},
	}
	assertDecisions(t, Decide(in), []wantDecision{
		{42, ActionSkip, "local main diverged", ""},
		{43, ActionSkip, "local main diverged", ""},
		{44, ActionDispatch, "dispatch", "claude"},
	})
}

// TestDecideMainSyncGate_FailedSkipsWithDistinctReason covers plan test 14:
// MainSyncFailed skips with exactly "local main sync failed", asserted
// content-specifically as NOT "local main diverged" (watch/docs/error-handling.md
// #446 -- a regression collapsing the two failure classes must be caught).
func TestDecideMainSyncGate_FailedSkipsWithDistinctReason(t *testing.T) {
	in := baseInputs()
	in.Tickets[0].MainSync = MainSyncFailed

	got := Decide(in)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, "local main sync failed", ""}})
	if got[0].Reason == "local main diverged" {
		t.Fatalf("reason %q must not collapse into the diverged reason", got[0].Reason)
	}
}

// TestDecideMainSyncGate_FetchFailedIsUngated covers plan test 15: a
// transient `git fetch` failure must never gate dispatch -- it self-heals
// next pass.
func TestDecideMainSyncGate_FetchFailedIsUngated(t *testing.T) {
	in := baseInputs()
	in.Tickets[0].MainSync = MainSyncFetchFailed
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
}

// TestDecideMainSyncGate_ExplicitSkippedIsUngated covers plan test 16: an
// explicit MainSyncSkipped (feature branch checked out, detached HEAD, empty
// dir, non-main default branch) must dispatch normally.
func TestDecideMainSyncGate_ExplicitSkippedIsUngated(t *testing.T) {
	in := baseInputs()
	in.Tickets[0].MainSync = MainSyncSkipped
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
}

// TestDecideMainSyncGate_ZeroValueIsUngated covers plan test 17: a Ticket
// literal that never sets MainSync at all (every pre-#822 construction site,
// including the reconciler's nil-sync-map CollectTickets call) must dispatch
// normally -- the zero value is the permissive one.
func TestDecideMainSyncGate_ZeroValueIsUngated(t *testing.T) {
	in := baseInputs() // in.Tickets[0].MainSync left at its zero value
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
}

// TestDecideMainSyncGate_UnrecognizedValueSkipsWithDistinctReason covers plan
// test 18: an unregistered MainSync value must default-deny with its own
// distinct reason, not silently collapse into either known failure reason
// (watch/docs/go-gotchas.md #598 requires the enum switch's default branch be
// asserted).
func TestDecideMainSyncGate_UnrecognizedValueSkipsWithDistinctReason(t *testing.T) {
	in := baseInputs()
	in.Tickets[0].MainSync = MainSync("bogus")

	got := Decide(in)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, "local main sync probe unrecognized", ""}})
	if got[0].Reason == "local main diverged" || got[0].Reason == "local main sync failed" {
		t.Fatalf("unrecognized MainSync must not collapse into a known reason, got %q", got[0].Reason)
	}
}

// -- #825: Depends on #N dependency gate -------------------------------------

// TestDecideDependencyGate_SingleClosedDependencyDispatches covers plan test
// 17: a single DependencyStateClosed dependency does not gate dispatch.
func TestDecideDependencyGate_SingleClosedDependencyDispatches(t *testing.T) {
	in := baseInputs()
	in.Tickets[0].DependsOn = []int{100}
	in.Tickets[0].DependencyStates = map[int]DependencyState{100: DependencyStateClosed}

	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
}

// TestDecideDependencyGate_SingleOpenDependencySkipsWaiting covers plan test
// 18: a single DependencyStateOpen dependency skips with exactly
// "waiting on dependency #<N>".
func TestDecideDependencyGate_SingleOpenDependencySkipsWaiting(t *testing.T) {
	in := baseInputs()
	in.Tickets[0].DependsOn = []int{100}
	in.Tickets[0].DependencyStates = map[int]DependencyState{100: DependencyStateOpen}

	want := fmt.Sprintf(reasonDependencyWaitingFmt, 100)
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, want, ""}})
}

// TestDecideDependencyGate_SingleUnresolvedDependencySkipsDistinctReason
// covers plan test 19: DependencyStateUnresolved skips with exactly
// "dependency #<N> unresolved", content-specifically asserted NOT equal to
// the waiting reason (#446 -- distinct failure classes must never collapse).
func TestDecideDependencyGate_SingleUnresolvedDependencySkipsDistinctReason(t *testing.T) {
	in := baseInputs()
	in.Tickets[0].DependsOn = []int{100}
	in.Tickets[0].DependencyStates = map[int]DependencyState{100: DependencyStateUnresolved}

	got := Decide(in)
	want := fmt.Sprintf(reasonDependencyUnresolvedFmt, 100)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, want, ""}})
	if got[0].Reason == fmt.Sprintf(reasonDependencyWaitingFmt, 100) {
		t.Fatalf("unresolved reason %q must not collapse into the waiting reason", got[0].Reason)
	}
}

// TestDecideDependencyGate_MapLookupMissSkipsStateUnrecognized covers plan
// test 20 (Q2): a DependsOn entry with no matching key in DependencyStates
// (map lookup miss) skips with exactly "dependency #<N> state unrecognized",
// content-specifically distinct from both other dependency reasons --
// #598's asserted-default-branch requirement, matching reasonStageProbeUnknown
// and reasonMainSyncUnknown's existing convention.
func TestDecideDependencyGate_MapLookupMissSkipsStateUnrecognized(t *testing.T) {
	in := baseInputs()
	in.Tickets[0].DependsOn = []int{100}
	in.Tickets[0].DependencyStates = map[int]DependencyState{} // 100 absent: lookup miss

	got := Decide(in)
	want := fmt.Sprintf(reasonDependencyStateUnknownFmt, 100)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, want, ""}})
	if got[0].Reason == fmt.Sprintf(reasonDependencyWaitingFmt, 100) || got[0].Reason == fmt.Sprintf(reasonDependencyUnresolvedFmt, 100) {
		t.Fatalf("map-lookup-miss reason %q must not collapse into either known dependency reason", got[0].Reason)
	}
}

// TestDecideDependencyGate_LowestNumberedBlockerReported covers plan test 21:
// two open dependencies supplied out of order (DependsOn: [50, 10]) report
// the lowest blocking number (#10), mirroring blockingSibling's existing
// "lowest number reported, for determinism" convention.
func TestDecideDependencyGate_LowestNumberedBlockerReported(t *testing.T) {
	in := baseInputs()
	in.Tickets[0].DependsOn = []int{50, 10}
	in.Tickets[0].DependencyStates = map[int]DependencyState{50: DependencyStateOpen, 10: DependencyStateOpen}

	want := fmt.Sprintf(reasonDependencyWaitingFmt, 10)
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, want, ""}})
}

// TestDecideDependencyGate_OnlyActuallyBlockingDependencyReported covers plan
// test 22: a lower-numbered CLOSED dependency and a higher-numbered OPEN one
// (DependsOn: [3, 9], #3 closed) report only the actually-blocking #9.
func TestDecideDependencyGate_OnlyActuallyBlockingDependencyReported(t *testing.T) {
	in := baseInputs()
	in.Tickets[0].DependsOn = []int{3, 9}
	in.Tickets[0].DependencyStates = map[int]DependencyState{3: DependencyStateClosed, 9: DependencyStateOpen}

	want := fmt.Sprintf(reasonDependencyWaitingFmt, 9)
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, want, ""}})
}

// TestDecideDependencyGate_EmptyDependsOnIsUngated covers plan test 23: the
// zero value (nil DependsOn) -- every pre-#825 Ticket literal -- dispatches
// normally, unaffected by the new gate.
func TestDecideDependencyGate_EmptyDependsOnIsUngated(t *testing.T) {
	in := baseInputs() // in.Tickets[0].DependsOn left at its zero value (nil)
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
}

// TestDecideDependencyGate_OrderingAfterPlanFreshness covers plan test 24: a
// ticket with both a stale plan and an open dependency skips with
// "plan stale, re-plan" -- proving Pickup rule 4 (plan freshness) is
// evaluated before the dependency gate, per the plan's placement decision.
func TestDecideDependencyGate_OrderingAfterPlanFreshness(t *testing.T) {
	in := baseInputs()
	in.Plans[0].CommitsBehind = 10 // stale: exceeds testConfig's tolerance of 5
	in.Tickets[0].DependsOn = []int{100}
	in.Tickets[0].DependencyStates = map[int]DependencyState{100: DependencyStateOpen}

	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "plan stale, re-plan", ""}})
}

// TestDecideDependencyGate_OrderingBeforeSiblingSerialization covers plan
// test 25: a plan-fresh, dependency-blocked child ticket whose sibling is
// also active skips with the dependency reason, not "waiting on sibling
// #N" -- proving the dependency gate is evaluated before the (renumbered)
// Pickup rule 6 sibling-serialization check.
func TestDecideDependencyGate_OrderingBeforeSiblingSerialization(t *testing.T) {
	in := baseInputs()
	in.Tickets = []Ticket{
		{Repo: "o/r", Number: 41, Labels: []string{"Working"}},
		{
			Repo: "o/r", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"},
			DependsOn:        []int{100},
			DependencyStates: map[int]DependencyState{100: DependencyStateOpen},
		},
	}
	in.Plans = []Plan{
		{Repo: "o/r", Path: ".plans/41.md", TicketID: 41, Status: "planned", IsChild: true, ParentID: 40},
		{Repo: "o/r", Path: ".plans/42.md", TicketID: 42, Status: "planned", IsChild: true, ParentID: 40},
	}

	want := fmt.Sprintf(reasonDependencyWaitingFmt, 100)
	assertDecisions(t, Decide(in), []wantDecision{
		{41, ActionSkip, "not Planned", ""},
		{42, ActionSkip, want, ""},
	})
}

// -- #826: unattended planner escalation, dispatch regression tests --------

// TestDecideEscalatedCounterNeverTripsNeedInputPause is the plan's
// Implementation Order step 6 / Test Strategy table regression: Decide's
// fleet-wide "need-input pause" gate reads only Snapshot.Summary.NeedInput
// (decide.go's `in.Snapshot.Summary.NeedInput >= in.Config.NeedInputThreshold`
// check) -- the new Escalated counter must never feed it, so a fleet with
// several escalated (Input Needed) tickets and zero live need-input sessions
// must still dispatch an unrelated, otherwise-eligible ticket normally. This
// is the epic's core acceptance criterion: "one escalation must never freeze
// the whole fleet."
func TestDecideEscalatedCounterNeverTripsNeedInputPause(t *testing.T) {
	in := baseInputs()
	in.Config.NeedInputThreshold = 1
	in.Snapshot = &watch.StateSnapshot{
		Summary: watch.StatusSummary{Running: 0, NeedInput: 0, Escalated: 5},
	}
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
}

// -- #827: dispatch auto-resume of Input Needed tickets ----------------------

// resumeInputs is the resume happy path (#827): one Input Needed ticket #42
// with an awaiting-input draft and an Answered probe -- every gate passes.
func resumeInputs() Inputs {
	return Inputs{
		Tickets:     []Ticket{{Repo: "o/r", Number: 42, Labels: []string{"Input Needed"}, Assignees: []string{"octocat"}}},
		Plans:       []Plan{{Repo: "o/r", Path: ".plans/42-x.md", TicketID: 42, Status: "awaiting-input"}},
		Snapshot:    snapshot(0, 0),
		Budgets:     FloorProvider{},
		Now:         time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		CurrentUser: "octocat",
		Config:      testConfig(),
		Answers:     map[string]AnswerProbe{"o/r#42": AnswerProbeAnswered},
	}
}

// TestDecideResumeGate covers the resume variant of the gate chain: the
// happy resume, each AnswerProbe class's own distinct skip reason (including
// the switch-default/map-miss case), the plan-status swap, and every
// non-board/non-plan gate (Blocked, open PR, Working-drift, multiple
// assignees, dependency, nil snapshot, concurrency cap, quiet hours, budget)
// still gating a resume identically to the ordinary path. A stale draft
// still resumes because freshness (rule 4) is deliberately skipped when
// resuming.
func TestDecideResumeGate(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(in *Inputs)
		wantAction Action
		wantReason string
		wantResume bool
	}{
		{
			name:       "happy resume",
			wantAction: ActionDispatch,
			wantReason: "resume — human answered",
			wantResume: true,
		},
		{
			name:       "probe waiting skips with its own distinct reason",
			mutate:     func(in *Inputs) { in.Answers["o/r#42"] = AnswerProbeWaiting },
			wantAction: ActionSkip,
			wantReason: reasonAnswerWaiting,
		},
		{
			name:       "probe unresolved skips with its own distinct reason",
			mutate:     func(in *Inputs) { in.Answers["o/r#42"] = AnswerProbeUnresolved },
			wantAction: ActionSkip,
			wantReason: reasonAnswerUnresolved,
		},
		{
			name:       "probe map miss falls into the switch default, distinct from every known reason",
			mutate:     func(in *Inputs) { delete(in.Answers, "o/r#42") },
			wantAction: ActionSkip,
			wantReason: reasonAnswerProbeUnknown,
		},
		{
			// #849: resumeGateSkip has a dedicated case for
			// AnswerProbeAnchorUnset, distinct from the shared switch
			// default's reasonAnswerProbeUnknown.
			name:       "probe anchor_unset skips with its own distinct reason (#849)",
			mutate:     func(in *Inputs) { in.Answers["o/r#42"] = AnswerProbeAnchorUnset },
			wantAction: ActionSkip,
			wantReason: reasonAnswerAnchorUnset,
		},
		{
			// #849: resumeGateSkip has a dedicated case for
			// AnswerProbeAnchorMismatch, distinct from the shared switch
			// default's reasonAnswerProbeUnknown.
			name:       "probe anchor_mismatch skips with its own distinct reason (#849)",
			mutate:     func(in *Inputs) { in.Answers["o/r#42"] = AnswerProbeAnchorMismatch },
			wantAction: ActionSkip,
			wantReason: reasonAnswerAnchorMismatch,
		},
		{
			name:       "unrecognized AnswerProbe enum value hits the same switch default",
			mutate:     func(in *Inputs) { in.Answers["o/r#42"] = AnswerProbe("bogus") },
			wantAction: ActionSkip,
			wantReason: reasonAnswerProbeUnknown,
		},
		{
			// #882: the replying login was positively denied current
			// repository write permission (read/triage/none) -- distinct
			// from every anchor/probe-unresolved reason above.
			name:       "probe unauthorized (positively denied write permission) skips with its own distinct reason (#882)",
			mutate:     func(in *Inputs) { in.Answers["o/r#42"] = AnswerProbeUnauthorized },
			wantAction: ActionSkip,
			wantReason: reasonAnswerUnauthorized,
		},
		{
			name:       "probe permission login_invalid skips with its own distinct reason (#882)",
			mutate:     func(in *Inputs) { in.Answers["o/r#42"] = AnswerProbeLoginInvalid },
			wantAction: ActionSkip,
			wantReason: reasonAnswerPermissionLoginInvalid,
		},
		{
			name:       "probe permission_api_error skips with its own distinct reason (#882)",
			mutate:     func(in *Inputs) { in.Answers["o/r#42"] = AnswerProbePermissionAPIError },
			wantAction: ActionSkip,
			wantReason: reasonAnswerPermissionAPIError,
		},
		{
			name:       "probe permission_timeout skips with its own distinct reason (#882)",
			mutate:     func(in *Inputs) { in.Answers["o/r#42"] = AnswerProbePermissionTimeout },
			wantAction: ActionSkip,
			wantReason: reasonAnswerPermissionTimeout,
		},
		{
			name:       "probe permission_truncated skips with its own distinct reason (#882)",
			mutate:     func(in *Inputs) { in.Answers["o/r#42"] = AnswerProbePermissionTruncated },
			wantAction: ActionSkip,
			wantReason: reasonAnswerPermissionTruncated,
		},
		{
			name:       "probe permission_malformed skips with its own distinct reason (#882)",
			mutate:     func(in *Inputs) { in.Answers["o/r#42"] = AnswerProbePermissionMalformed },
			wantAction: ActionSkip,
			wantReason: reasonAnswerPermissionMalformed,
		},
		{
			name:       "probe permission_missing_field skips with its own distinct reason (#882)",
			mutate:     func(in *Inputs) { in.Answers["o/r#42"] = AnswerProbePermissionMissingField },
			wantAction: ActionSkip,
			wantReason: reasonAnswerPermissionMissingField,
		},
		{
			name:       "probe permission_unknown_value skips with its own distinct reason (#882)",
			mutate:     func(in *Inputs) { in.Answers["o/r#42"] = AnswerProbePermissionUnknownValue },
			wantAction: ActionSkip,
			wantReason: reasonAnswerPermissionUnknownValue,
		},
		{
			name:       "probe permission_cap_exhausted skips with its own distinct reason (#882)",
			mutate:     func(in *Inputs) { in.Answers["o/r#42"] = AnswerProbePermissionCapExhausted },
			wantAction: ActionSkip,
			wantReason: reasonAnswerPermissionCapExhausted,
		},
		{
			name:       "draft already status: planned (not awaiting-input) skips with a distinct reason",
			mutate:     func(in *Inputs) { in.Plans[0].Status = "planned" },
			wantAction: ActionSkip,
			wantReason: reasonDraftNotAwaitingInput,
		},
		{
			name:       "no plan file still gates a resume",
			mutate:     func(in *Inputs) { in.Plans = nil },
			wantAction: ActionSkip,
			wantReason: "no plan file",
		},
		{
			name:       "Blocked still gates a resume",
			mutate:     func(in *Inputs) { in.Tickets[0].Labels = []string{"Input Needed", "Blocked"} },
			wantAction: ActionSkip,
			wantReason: "blocked",
		},
		{
			name:       "open PR still gates a resume",
			mutate:     func(in *Inputs) { in.Tickets[0].HasOpenPR = true },
			wantAction: ActionSkip,
			wantReason: "open PR exists",
		},
		{
			name:       "Working-drift still gates a resume",
			mutate:     func(in *Inputs) { in.Tickets[0].Labels = []string{"Input Needed", "Working"} },
			wantAction: ActionSkip,
			wantReason: "already Working",
		},
		{
			name:       "multiple assignees still gates a resume",
			mutate:     func(in *Inputs) { in.Tickets[0].Assignees = []string{"octocat", "hubot"} },
			wantAction: ActionSkip,
			wantReason: "multiple assignees",
		},
		{
			name: "an open dependency still gates a resume",
			mutate: func(in *Inputs) {
				in.Tickets[0].DependsOn = []int{100}
				in.Tickets[0].DependencyStates = map[int]DependencyState{100: DependencyStateOpen}
			},
			wantAction: ActionSkip,
			wantReason: fmt.Sprintf(reasonDependencyWaitingFmt, 100),
		},
		{
			name:       "nil snapshot still gates a resume",
			mutate:     func(in *Inputs) { in.Snapshot = nil },
			wantAction: ActionSkip,
			wantReason: "daemon unreachable",
		},
		{
			name: "concurrency cap still gates a resume",
			mutate: func(in *Inputs) {
				in.Config.ConcurrencyCap = 1
				in.Snapshot = snapshot(1, 0)
			},
			wantAction: ActionSkip,
			wantReason: "concurrency cap reached",
		},
		{
			name: "quiet hours still gates a resume",
			mutate: func(in *Inputs) {
				in.Config.QuietHours = &QuietHours{StartHour: 22, EndHour: 7}
				in.Now = time.Date(2026, 7, 10, 23, 0, 0, 0, time.UTC)
			},
			wantAction: ActionSkip,
			wantReason: "quiet hours",
		},
		{
			name:       "budget exhausted still gates a resume",
			mutate:     func(in *Inputs) { in.Budgets = FloorProvider{Floors: map[string]float64{"claude": 0}} },
			wantAction: ActionSkip,
			wantReason: "budget exhausted",
		},
		{
			name:       "a stale draft still resumes (freshness deliberately skipped)",
			mutate:     func(in *Inputs) { in.Plans[0].CommitsBehind = 999 },
			wantAction: ActionDispatch,
			wantReason: "resume — human answered",
			wantResume: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := resumeInputs()
			if tc.mutate != nil {
				tc.mutate(&in)
			}
			got := Decide(in)
			if len(got) != 1 {
				t.Fatalf("got %d decisions, want 1: %+v", len(got), got)
			}
			d := got[0]
			if d.Action != tc.wantAction {
				t.Errorf("action = %q, want %q (reason %q)", d.Action, tc.wantAction, d.Reason)
			}
			if d.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", d.Reason, tc.wantReason)
			}
			if d.Resume != tc.wantResume {
				t.Errorf("Resume = %v, want %v", d.Resume, tc.wantResume)
			}
		})
	}
}

// TestDecideResumeGate_AnchorReasonsAreContentDistinct pins rule #446/#598
// for the two #849 anchor skip reasons: reasonAnswerAnchorUnset and
// reasonAnswerAnchorMismatch must never collapse into each other or into any
// pre-#849 resume-gate reason.
func TestDecideResumeGate_AnchorReasonsAreContentDistinct(t *testing.T) {
	in := resumeInputs()
	in.Answers["o/r#42"] = AnswerProbeAnchorUnset
	gotUnset := Decide(in)[0].Reason

	in2 := resumeInputs()
	in2.Answers["o/r#42"] = AnswerProbeAnchorMismatch
	gotMismatch := Decide(in2)[0].Reason

	if gotUnset == gotMismatch {
		t.Fatalf("AnswerProbeAnchorUnset and AnswerProbeAnchorMismatch produced the identical reason %q, want distinct reasons (#446)", gotUnset)
	}
	if gotUnset != reasonAnswerAnchorUnset {
		t.Errorf("AnswerProbeAnchorUnset reason = %q, want %q", gotUnset, reasonAnswerAnchorUnset)
	}
	if gotMismatch != reasonAnswerAnchorMismatch {
		t.Errorf("AnswerProbeAnchorMismatch reason = %q, want %q", gotMismatch, reasonAnswerAnchorMismatch)
	}
}

// TestDecideResumeGate_PermissionReasonsAreAllPairwiseDistinct pins #446/#598
// for #882's nine new permission-failure skip reasons: every value must
// resolve to its own exact reason string, never collapsing into a sibling
// permission-failure reason, an anchor reason, or the shared switch-default
// reasonAnswerProbeUnknown.
func TestDecideResumeGate_PermissionReasonsAreAllPairwiseDistinct(t *testing.T) {
	probes := []AnswerProbe{
		AnswerProbeWaiting,
		AnswerProbeUnresolved,
		AnswerProbeAnchorUnset,
		AnswerProbeAnchorMismatch,
		AnswerProbeUnauthorized,
		AnswerProbeLoginInvalid,
		AnswerProbePermissionAPIError,
		AnswerProbePermissionTimeout,
		AnswerProbePermissionTruncated,
		AnswerProbePermissionMalformed,
		AnswerProbePermissionMissingField,
		AnswerProbePermissionUnknownValue,
		AnswerProbePermissionCapExhausted,
		AnswerProbe("bogus-unrecognized-value"),
	}

	seen := make(map[string]AnswerProbe, len(probes))
	for _, p := range probes {
		in := resumeInputs()
		in.Answers["o/r#42"] = p
		reason, gated := resumeGateSkip(in.Tickets[0], p)
		if !gated {
			t.Fatalf("resumeGateSkip(%q) = (%q, %v), want gated=true for a non-answered probe", p, reason, gated)
		}
		if prior, ok := seen[reason]; ok {
			t.Fatalf("AnswerProbe values %q and %q both produced the identical reason %q, want pairwise-distinct reasons (#446/#598)", prior, p, reason)
		}
		seen[reason] = p
	}
}

// TestDecideResumeCountsIntoDispatchedThisPass covers the Test Strategy
// table's "a resume counts into dispatchedThisPass" case: a resumed ticket
// fills the concurrency cap exactly like an ordinary dispatch, so a second,
// otherwise-eligible ordinary Planned ticket in the same pass is capped.
func TestDecideResumeCountsIntoDispatchedThisPass(t *testing.T) {
	in := resumeInputs()
	in.Config.ConcurrencyCap = 1
	in.Tickets = append(in.Tickets, Ticket{Repo: "o/r", Number: 43, Labels: []string{"Planned"}, Assignees: []string{"octocat"}})
	in.Plans = append(in.Plans, Plan{Repo: "o/r", Path: ".plans/43-x.md", TicketID: 43, Status: "planned"})

	got := Decide(in)
	if len(got) != 2 {
		t.Fatalf("got %d decisions, want 2: %+v", len(got), got)
	}
	if got[0].Ticket.Number != 42 || got[0].Action != ActionDispatch || !got[0].Resume {
		t.Fatalf("#42 = %+v, want a resumed dispatch", got[0])
	}
	if got[1].Ticket.Number != 43 || got[1].Action != ActionSkip || got[1].Reason != "concurrency cap reached" {
		t.Fatalf("#43 = %+v, want a concurrency-cap skip (proves the resume filled the cap)", got[1])
	}
}

// TestDecideNonInputNeededTicketIgnoresAnswersMap covers the Test Strategy
// table's non-regression case: a ticket that never carried Input Needed
// dispatches byte-identically to the pre-#827 behavior, Resume=false, even
// when Inputs.Answers happens to hold an entry keyed to it.
func TestDecideNonInputNeededTicketIgnoresAnswersMap(t *testing.T) {
	in := baseInputs()
	in.Answers = map[string]AnswerProbe{planKey("o/r", 42): AnswerProbeAnswered}

	got := Decide(in)
	assertDecisions(t, got, []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
	if got[0].Resume {
		t.Error("a non-Input-Needed ticket must never resume, regardless of a populated Answers map")
	}
}

// -- #828: stage-aware Refined pickup and autonomous re-plan ----------------

// planningCandidateInputs is the planning-pickup happy path: one Refined
// ticket #42 with no plan file and dispatch.planRefined enabled.
func planningCandidateInputs() Inputs {
	in := baseInputs()
	in.Config.PlanRefined = true
	in.RepoAutonomy = leanAutonomy("o/r")
	in.Tickets[0].Labels = []string{"Refined"}
	in.Plans = nil
	return in
}

// TestDecidePlanRefinedFlagOff_RefinedNoPlanSkipsNotPlanned locks in the
// flag-off regression: a Refined ticket with no plan must skip with exactly
// "not Planned", byte-identical to pre-#828 behavior, even though it would be
// a planning candidate if the flag were on.
func TestDecidePlanRefinedFlagOff_RefinedNoPlanSkipsNotPlanned(t *testing.T) {
	in := baseInputs()
	in.Tickets[0].Labels = []string{"Refined"}
	in.Plans = nil
	// PlanRefined stays false via testConfig()'s pinned default.
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "not Planned", ""}})
}

// TestDecidePlanRefinedFlagOff_StalePlanSkipsPlanStaleRePlan locks in the
// flag-off regression: a stale plan must still terminally skip with exactly
// "plan stale, re-plan", byte-identical to pre-#828 behavior.
func TestDecidePlanRefinedFlagOff_StalePlanSkipsPlanStaleRePlan(t *testing.T) {
	in := baseInputs()
	in.Plans[0].CommitsBehind = 10
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "plan stale, re-plan", ""}})
}

// TestDecidePlanningPickup_RefinedNoPlanDispatches covers the flag-on
// planning-pickup happy path: Refined + no plan -> ActionDispatch,
// Planning: true, Replan: false, Plan == nil, reason
// "plan — Refined, no plan file".
func TestDecidePlanningPickup_RefinedNoPlanDispatches(t *testing.T) {
	in := planningCandidateInputs()

	got := Decide(in)
	if len(got) != 1 {
		t.Fatalf("got %d decisions, want 1: %+v", len(got), got)
	}
	d := got[0]
	if d.Action != ActionDispatch {
		t.Fatalf("action = %q, want %q (reason %q)", d.Action, ActionDispatch, d.Reason)
	}
	if !d.Planning {
		t.Error("Planning = false, want true")
	}
	if d.Replan {
		t.Error("Replan = true, want false")
	}
	if d.Plan != nil {
		t.Errorf("Plan = %+v, want nil", d.Plan)
	}
	if want := "plan — Refined, no plan file"; d.Reason != want {
		t.Errorf("reason = %q, want %q", d.Reason, want)
	}
}

// TestDecidePlanningPickup_RefinedWithExistingPlanUnchanged covers the Test
// Strategy table's "Refined + an existing plan -> unchanged behavior" case:
// a Refined ticket that lacks the Planned label is never a planning
// candidate once a plan already exists (planning requires Plan == nil), so
// it still skips "not Planned" exactly as before #828.
func TestDecidePlanningPickup_RefinedWithExistingPlanUnchanged(t *testing.T) {
	in := baseInputs()
	in.Config.PlanRefined = true
	in.RepoAutonomy = leanAutonomy("o/r")
	in.Tickets[0].Labels = []string{"Refined"} // no Planned label; a plan file exists
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "not Planned", ""}})
}

// TestDecidePlanningPickup_RefinedPlannedNoPlanStillNoPlanFile covers the
// Test Strategy table's "Refined + Planned + no plan -> still 'no plan
// file'" case: the planning-candidate predicate explicitly excludes tickets
// that already carry Planned (the Assumptions section's race-avoidance
// rule), so this stays the reconciler's plan-invalid territory, not a
// planning dispatch.
func TestDecidePlanningPickup_RefinedPlannedNoPlanStillNoPlanFile(t *testing.T) {
	in := baseInputs()
	in.Config.PlanRefined = true
	in.RepoAutonomy = leanAutonomy("o/r")
	in.Tickets[0].Labels = []string{"Refined", "Planned"}
	in.Plans = nil
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "no plan file", ""}})
}

// TestDecidePlanningPickup_StillGatedByEarlierBoardStateChecks covers the
// Test Strategy table's "planning candidate still skipped by each earlier
// gate" requirement: main-sync, Working, Blocked, open PR, the stage gate,
// and the assignee gate all still gate a planning candidate with their
// today's reasons.
func TestDecidePlanningPickup_StillGatedByEarlierBoardStateChecks(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(in *Inputs)
		want   wantDecision
	}{
		{
			name:   "main sync diverged",
			mutate: func(in *Inputs) { in.Tickets[0].MainSync = MainSyncDiverged },
			want:   wantDecision{42, ActionSkip, "local main diverged", ""},
		},
		{
			name:   "already Working",
			mutate: func(in *Inputs) { in.Tickets[0].Labels = []string{"Refined", "Working"} },
			want:   wantDecision{42, ActionSkip, "already Working", ""},
		},
		{
			name:   "Blocked",
			mutate: func(in *Inputs) { in.Tickets[0].Labels = []string{"Refined", "Blocked"} },
			want:   wantDecision{42, ActionSkip, "blocked", ""},
		},
		{
			name:   "open PR",
			mutate: func(in *Inputs) { in.Tickets[0].HasOpenPR = true },
			want:   wantDecision{42, ActionSkip, "open PR exists", ""},
		},
		{
			name: "stage gate finalized",
			mutate: func(in *Inputs) {
				in.Tickets[0].Stage = "finalized"
				in.Tickets[0].StageProbe = StageProbePresent
			},
			want: wantDecision{42, ActionSkip, "pipeline finalized (reset to re-dispatch)", ""},
		},
		{
			name:   "unassigned",
			mutate: func(in *Inputs) { in.Tickets[0].Assignees = nil },
			want:   wantDecision{42, ActionSkip, "unassigned", ""},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := planningCandidateInputs()
			tc.mutate(&in)
			assertDecisions(t, Decide(in), []wantDecision{tc.want})
		})
	}
}

// TestDecidePlanningPickup_StillGatedByLaterGates covers the Test Strategy
// table's "planning candidate still skipped by each later gate" requirement:
// the dependency gate, daemon-unreachable, need-input pause, concurrency
// cap, daily quota, quiet hours, and budget gates all still gate a planning
// candidate with their today's reasons.
func TestDecidePlanningPickup_StillGatedByLaterGates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(in *Inputs)
		want   wantDecision
	}{
		{
			name: "dependency gate",
			mutate: func(in *Inputs) {
				in.Tickets[0].DependsOn = []int{100}
				in.Tickets[0].DependencyStates = map[int]DependencyState{100: DependencyStateOpen}
			},
			want: wantDecision{42, ActionSkip, fmt.Sprintf(reasonDependencyWaitingFmt, 100), ""},
		},
		{
			name:   "daemon unreachable",
			mutate: func(in *Inputs) { in.Snapshot = nil },
			want:   wantDecision{42, ActionSkip, "daemon unreachable", ""},
		},
		{
			name: "need-input pause",
			mutate: func(in *Inputs) {
				in.Config.NeedInputThreshold = 1
				in.Snapshot = snapshot(0, 1)
			},
			want: wantDecision{42, ActionSkip, "need-input pause", ""},
		},
		{
			name: "concurrency cap reached",
			mutate: func(in *Inputs) {
				in.Config.ConcurrencyCap = 1
				in.Snapshot = snapshot(1, 0)
			},
			want: wantDecision{42, ActionSkip, "concurrency cap reached", ""},
		},
		{
			name: "daily quota reached",
			mutate: func(in *Inputs) {
				in.Config.DailyQuota = 1
				in.Prior = 1
			},
			want: wantDecision{42, ActionSkip, "daily quota reached", ""},
		},
		{
			name: "quiet hours",
			mutate: func(in *Inputs) {
				in.Config.QuietHours = &QuietHours{StartHour: 22, EndHour: 7}
				in.Now = time.Date(2026, 7, 10, 23, 0, 0, 0, time.UTC)
			},
			want: wantDecision{42, ActionSkip, "quiet hours", ""},
		},
		{
			name:   "budget exhausted",
			mutate: func(in *Inputs) { in.Budgets = FloorProvider{Floors: map[string]float64{"claude": 0}} },
			want:   wantDecision{42, ActionSkip, "budget exhausted", ""},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := planningCandidateInputs()
			tc.mutate(&in)
			assertDecisions(t, Decide(in), []wantDecision{tc.want})
		})
	}
}

// TestDecidePlanningPickup_CountsIntoDispatchedThisPass covers the Test
// Strategy table's "a planning dispatch counts into dispatchedThisPass
// (multi-ticket cap test)" requirement: a planning dispatch fills the
// concurrency cap exactly like an ordinary dispatch.
func TestDecidePlanningPickup_CountsIntoDispatchedThisPass(t *testing.T) {
	in := planningCandidateInputs()
	in.Config.ConcurrencyCap = 1
	in.Tickets = append(in.Tickets, Ticket{Repo: "o/r", Number: 43, Labels: []string{"Refined"}, Assignees: []string{"octocat"}})

	got := Decide(in)
	if len(got) != 2 {
		t.Fatalf("got %d decisions, want 2: %+v", len(got), got)
	}
	if got[0].Ticket.Number != 42 || got[0].Action != ActionDispatch || !got[0].Planning {
		t.Fatalf("#42 = %+v, want a planning dispatch", got[0])
	}
	if got[1].Ticket.Number != 43 || got[1].Action != ActionSkip || got[1].Reason != "concurrency cap reached" {
		t.Fatalf("#43 = %+v, want a concurrency-cap skip (proves the planning dispatch filled the cap)", got[1])
	}
}

// TestDecideReplan_StalePlanFlagOnDispatches covers the flag-on autonomous
// re-plan happy path: a stale plan with PlanRefined on -> ActionDispatch,
// Planning: true, Replan: true, Plan != nil, reason "re-plan — plan stale".
func TestDecideReplan_StalePlanFlagOnDispatches(t *testing.T) {
	in := baseInputs()
	in.Config.PlanRefined = true
	in.RepoAutonomy = leanAutonomy("o/r")
	in.Plans[0].CommitsBehind = 10 // stale: exceeds testConfig's tolerance of 5

	got := Decide(in)
	if len(got) != 1 {
		t.Fatalf("got %d decisions, want 1: %+v", len(got), got)
	}
	d := got[0]
	if d.Action != ActionDispatch {
		t.Fatalf("action = %q, want %q (reason %q)", d.Action, ActionDispatch, d.Reason)
	}
	if !d.Planning || !d.Replan {
		t.Errorf("Planning = %v, Replan = %v, want both true", d.Planning, d.Replan)
	}
	if d.Plan == nil {
		t.Error("Plan = nil, want non-nil (the stale plan itself)")
	}
	if want := "re-plan — plan stale"; d.Reason != want {
		t.Errorf("reason = %q, want %q", d.Reason, want)
	}
}

// TestDecideReplan_StillBlockedByDependencyGate covers the Test Strategy
// table's "a re-plan is still blocked by the dependency gate" ordering
// assertion, mirroring TestDecideDependencyGate_OrderingAfterPlanFreshness:
// with the flag ON, a stale-plan-plus-open-dependency ticket now falls
// through rule 4 (replanning, not skipping) and is skipped by the dependency
// gate instead, reporting the dependency reason rather than
// "plan stale, re-plan".
func TestDecideReplan_StillBlockedByDependencyGate(t *testing.T) {
	in := baseInputs()
	in.Config.PlanRefined = true
	in.RepoAutonomy = leanAutonomy("o/r")
	in.Plans[0].CommitsBehind = 10 // stale
	in.Tickets[0].DependsOn = []int{100}
	in.Tickets[0].DependencyStates = map[int]DependencyState{100: DependencyStateOpen}

	want := fmt.Sprintf(reasonDependencyWaitingFmt, 100)
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, want, ""}})
}

// TestDecideReplan_StillBlockedBySiblingSerialization covers the Test
// Strategy table's "a re-plan is still blocked by ... sibling serialization"
// requirement: a stale, plan-fresh-gate-passing child ticket whose sibling
// is active still waits on it.
func TestDecideReplan_StillBlockedBySiblingSerialization(t *testing.T) {
	in := baseInputs()
	in.Config.PlanRefined = true
	in.RepoAutonomy = leanAutonomy("o/r")
	in.Tickets = []Ticket{
		{Repo: "o/r", Number: 41, Labels: []string{"Working"}},
		{Repo: "o/r", Number: 42, Labels: []string{"Planned"}, Assignees: []string{"octocat"}},
	}
	in.Plans = []Plan{
		{Repo: "o/r", Path: ".plans/41.md", TicketID: 41, Status: "planned", IsChild: true, ParentID: 40},
		{Repo: "o/r", Path: ".plans/42.md", TicketID: 42, Status: "planned", IsChild: true, ParentID: 40, CommitsBehind: 10},
	}
	assertDecisions(t, Decide(in), []wantDecision{
		{41, ActionSkip, "not Planned", ""},
		{42, ActionSkip, "waiting on sibling #41", ""},
	})
}

// TestDecideReplan_StillBlockedByCapacityAndBudgetGates covers the Test
// Strategy table's "a re-plan is still blocked by ... capacity/budget gates"
// requirement.
func TestDecideReplan_StillBlockedByCapacityAndBudgetGates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(in *Inputs)
		want   wantDecision
	}{
		{
			name: "concurrency cap reached",
			mutate: func(in *Inputs) {
				in.Config.ConcurrencyCap = 1
				in.Snapshot = snapshot(1, 0)
			},
			want: wantDecision{42, ActionSkip, "concurrency cap reached", ""},
		},
		{
			name: "daily quota reached",
			mutate: func(in *Inputs) {
				in.Config.DailyQuota = 1
				in.Prior = 1
			},
			want: wantDecision{42, ActionSkip, "daily quota reached", ""},
		},
		{
			name:   "budget exhausted",
			mutate: func(in *Inputs) { in.Budgets = FloorProvider{Floors: map[string]float64{"claude": 0}} },
			want:   wantDecision{42, ActionSkip, "budget exhausted", ""},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInputs()
			in.Config.PlanRefined = true
			in.RepoAutonomy = leanAutonomy("o/r")
			in.Plans[0].CommitsBehind = 10
			tc.mutate(&in)
			assertDecisions(t, Decide(in), []wantDecision{tc.want})
		})
	}
}

// -- #851: gated checkout states (NotMain/Detached/Missing) gate every
// pickup type; per-repository lean-authorization gate -------------------
//
// Design note (Phase 3/red): decide.go does not define reason constants for
// these new gates yet (that is Phase 4's job), so the exact skip-reason
// strings below are literal, chosen here as the contract Phase 4 must
// satisfy, mirroring the existing reasonMainDiverged/reasonMainSyncFailed
// convention:
//   - MainSyncNotMain  -> "local main not on main"
//   - MainSyncDetached -> "local main detached"
//   - MainSyncMissing  -> "local main missing"
//   - RepoAutonomyInteractive -> "repo autonomy not lean"
//   - RepoAutonomyMissing    -> "repo config missing"
//   - RepoAutonomyMalformed  -> "repo config malformed"
//   - RepoAutonomyUnreadable -> "repo config unreadable"
//   - unrecognized/nil-map-miss/zero-value RepoAutonomy -> "repo autonomy probe unrecognized"
//   - the composed stale/re-plan-denied-by-autonomy reason is
//     fmt.Sprintf("plan stale, re-plan blocked: %s", <the autonomy reason above>)

// TestDecideMainSyncGate_NewCheckoutStatesGateEveryPickupType covers the
// plan's Q&A 1 resolution: each of the three new gated checkout states
// (NotMain, Detached, Missing) must gate ALL pickup types for that repo --
// an ordinary already-Planned dispatch, a fresh Refined planning candidate,
// and an autonomous re-plan candidate alike -- not only planning/re-plan,
// exactly like the existing MainSyncDiverged/MainSyncFailed gates.
func TestDecideMainSyncGate_NewCheckoutStatesGateEveryPickupType(t *testing.T) {
	tests := []struct {
		name       string
		mainSync   MainSync
		wantReason string
	}{
		{"not main", MainSyncNotMain, "local main not on main"},
		{"detached", MainSyncDetached, "local main detached"},
		{"missing", MainSyncMissing, "local main missing"},
	}
	for _, tc := range tests {
		t.Run(tc.name+"/ordinary Planned pickup", func(t *testing.T) {
			in := baseInputs()
			in.Tickets[0].MainSync = tc.mainSync
			assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, tc.wantReason, ""}})
		})
		t.Run(tc.name+"/fresh Refined planning candidate", func(t *testing.T) {
			in := planningCandidateInputs()
			in.Tickets[0].MainSync = tc.mainSync
			assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, tc.wantReason, ""}})
		})
		t.Run(tc.name+"/autonomous re-plan candidate", func(t *testing.T) {
			in := baseInputs()
			in.Config.PlanRefined = true
			in.RepoAutonomy = leanAutonomy("o/r")
			in.Plans[0].CommitsBehind = 10 // stale
			in.Tickets[0].MainSync = tc.mainSync
			assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, tc.wantReason, ""}})
		})
	}

	// Content-specific cross-checks (#446/#598): the three new reasons must
	// never collapse into each other or into the pre-existing
	// diverged/failed reasons.
	reasons := map[string]string{
		"not main": "local main not on main",
		"detached": "local main detached",
		"missing":  "local main missing",
	}
	seen := map[string]string{}
	for name, r := range reasons {
		if prior, ok := seen[r]; ok {
			t.Fatalf("both %q and %q share the reason %q -- must be distinct", prior, name, r)
		}
		seen[r] = name
		if r == reasonMainDiverged || r == reasonMainSyncFailed {
			t.Fatalf("%q must not collapse into an existing mainsync reason (%q or %q)", r, reasonMainDiverged, reasonMainSyncFailed)
		}
	}
}

// TestDecideMainSyncGate_SyncedIsUngated covers the plan's requirement that
// MainSyncSynced (unlike the three new checkout states above) stays ungated,
// unchanged by #851.
func TestDecideMainSyncGate_SyncedIsUngated(t *testing.T) {
	in := baseInputs()
	in.Tickets[0].MainSync = MainSyncSynced
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
}

// TestDecideAutonomyGate_Matrix covers the full RepoAutonomy config-state
// matrix for a fresh Refined planning candidate: lean allows the planning
// dispatch through; every other classification denies with its own distinct
// reason (default-deny).
func TestDecideAutonomyGate_Matrix(t *testing.T) {
	tests := []struct {
		name       string
		autonomy   RepoAutonomy
		wantAction Action
		wantReason string
	}{
		{"lean dispatches", RepoAutonomyLean, ActionDispatch, "plan — Refined, no plan file"},
		{"interactive denies", RepoAutonomyInteractive, ActionSkip, "repo autonomy not lean"},
		{"missing config denies", RepoAutonomyMissing, ActionSkip, "repo config missing"},
		{"malformed config denies", RepoAutonomyMalformed, ActionSkip, "repo config malformed"},
		{"unreadable config denies", RepoAutonomyUnreadable, ActionSkip, "repo config unreadable"},
		// #877: a repo this pass never confirmed a successful `git fetch
		// origin` for denies with its own distinct, explicitly retryable
		// reason -- never silently falls back to a prior pass's verdict.
		{"fetch unconfirmed denies", RepoAutonomyFetchUnconfirmed, ActionSkip, reasonAutonomyFetchUnconfirmed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := planningCandidateInputs()
			in.RepoAutonomy = map[string]RepoAutonomy{"o/r": tc.autonomy}
			got := Decide(in)
			if len(got) != 1 {
				t.Fatalf("got %d decisions, want 1: %+v", len(got), got)
			}
			if got[0].Action != tc.wantAction {
				t.Errorf("action = %q, want %q (reason %q)", got[0].Action, tc.wantAction, got[0].Reason)
			}
			if got[0].Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got[0].Reason, tc.wantReason)
			}
		})
	}
}

// TestDecideAutonomyGate_NilMapDefaultDenies covers the "no permissive zero
// value" contract (mirroring DependencyState/AnswerProbe): a nil
// Inputs.RepoAutonomy map (no probe result at all for this repo) must
// default-deny with its own distinct reason, not silently permit.
func TestDecideAutonomyGate_NilMapDefaultDenies(t *testing.T) {
	in := planningCandidateInputs()
	in.RepoAutonomy = nil
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "repo autonomy probe unrecognized", ""}})
}

// TestDecideAutonomyGate_ZeroValueDefaultDenies covers the same "no
// permissive zero value" contract for an explicit zero-value RepoAutonomy(""
// ) entry (a map lookup hit on an unset value), which must land in the same
// default-deny branch as the nil-map case above, not its own separate
// silent pass-through.
func TestDecideAutonomyGate_ZeroValueDefaultDenies(t *testing.T) {
	in := planningCandidateInputs()
	in.RepoAutonomy = map[string]RepoAutonomy{"o/r": RepoAutonomy("")}
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "repo autonomy probe unrecognized", ""}})
}

// TestDecideAutonomyGate_UnrecognizedValueDefaultDenies covers an
// unregistered RepoAutonomy value (a future/garbled probe result) hitting
// the same default-deny branch, per #598's asserted-default-branch
// requirement.
func TestDecideAutonomyGate_UnrecognizedValueDefaultDenies(t *testing.T) {
	in := planningCandidateInputs()
	in.RepoAutonomy = map[string]RepoAutonomy{"o/r": RepoAutonomy("bogus")}
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "repo autonomy probe unrecognized", ""}})
}

// TestDecideAutonomyGate_UnrecognizedValueDoesNotCollapseIntoFetchUnconfirmed
// guards against a regression that maps every unrecognized/default case onto
// the new #877 reasonAutonomyFetchUnconfirmed reason: an unregistered
// RepoAutonomy value must still resolve the pre-existing
// reasonAutonomyUnknown, distinct from the new fetch-specific reason
// (#446/#598 content-specific default-branch assertion).
func TestDecideAutonomyGate_UnrecognizedValueDoesNotCollapseIntoFetchUnconfirmed(t *testing.T) {
	in := planningCandidateInputs()
	in.RepoAutonomy = map[string]RepoAutonomy{"o/r": RepoAutonomy("bogus")}
	got := Decide(in)
	if len(got) != 1 {
		t.Fatalf("got %d decisions, want 1: %+v", len(got), got)
	}
	if got[0].Reason != reasonAutonomyUnknown {
		t.Errorf("reason = %q, want %q", got[0].Reason, reasonAutonomyUnknown)
	}
	if got[0].Reason == reasonAutonomyFetchUnconfirmed {
		t.Fatalf("an unrecognized RepoAutonomy value must not collapse into reasonAutonomyFetchUnconfirmed, got %q", got[0].Reason)
	}
}

// TestDecideAutonomyGate_FetchUnconfirmed_HoldsFreshPlanningPickup covers the
// #877 fetch-gating requirement directly at the planning-pickup site: a repo
// whose fetch did not succeed this pass (RepoAutonomyFetchUnconfirmed) must
// hold a fresh Refined planning candidate with its own distinct, explicitly
// retryable reason.
func TestDecideAutonomyGate_FetchUnconfirmed_HoldsFreshPlanningPickup(t *testing.T) {
	in := planningCandidateInputs()
	in.RepoAutonomy = map[string]RepoAutonomy{"o/r": RepoAutonomyFetchUnconfirmed}
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, reasonAutonomyFetchUnconfirmed, ""}})
}

// TestDecideAutonomyGate_PlanRefinedFalseIgnoresAutonomyEntirely is the
// plan's Assumptions-section regression: autonomy is only ever consulted
// when Config.PlanRefined is true, so every PlanRefined: false decision must
// stay byte-identical to today (the happy-path "dispatch" reason) no matter
// what RepoAutonomy says -- the fleet-wide kill switch off means the new
// gate is never even reached.
func TestDecideAutonomyGate_PlanRefinedFalseIgnoresAutonomyEntirely(t *testing.T) {
	autonomies := []RepoAutonomy{
		RepoAutonomyLean, RepoAutonomyInteractive, RepoAutonomyMissing,
		RepoAutonomyMalformed, RepoAutonomyUnreadable, RepoAutonomy("bogus"), RepoAutonomy(""),
	}
	for _, a := range autonomies {
		t.Run(string(a), func(t *testing.T) {
			in := baseInputs() // PlanRefined stays false via testConfig()'s pinned default
			in.RepoAutonomy = map[string]RepoAutonomy{"o/r": a}
			assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
		})
	}
}

// TestDecideAutonomyGate_OrdinaryPlannedPickupUnaffectedByAutonomy covers
// the plan's explicit "ordinary Planned pickup on a lean-gated repo is
// unaffected by autonomy (only by checkout state)" requirement: with
// PlanRefined true (the fleet kill switch on) but a ticket that is already
// Planned with a fresh (non-stale) plan -- never a planning or re-plan
// candidate -- every RepoAutonomy value, including the denying ones, must
// leave the ordinary dispatch untouched.
func TestDecideAutonomyGate_OrdinaryPlannedPickupUnaffectedByAutonomy(t *testing.T) {
	autonomies := []RepoAutonomy{
		RepoAutonomyLean, RepoAutonomyInteractive, RepoAutonomyMissing,
		RepoAutonomyMalformed, RepoAutonomyUnreadable,
		// #877: a fetch outage must never hold ordinary already-Planned
		// implementation work -- only freshness-dependent planning/re-plan.
		RepoAutonomyFetchUnconfirmed,
	}
	for _, a := range autonomies {
		t.Run(string(a), func(t *testing.T) {
			in := baseInputs()
			in.Config.PlanRefined = true
			in.RepoAutonomy = map[string]RepoAutonomy{"o/r": a}
			assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
		})
	}
}

// TestDecideAutonomyGate_ReplanDeniedComposesStaleAndAutonomyReason covers
// the plan's "composed reason string on the stale/re-plan path" requirement:
// a stale plan with PlanRefined on but a denying RepoAutonomy must not
// terminate with the flag-off literal "plan stale, re-plan" (which would
// misleadingly imply the flag itself is off) nor with the bare autonomy
// reason alone (which would lose the staleness context) -- it composes both
// facts into one reason.
func TestDecideAutonomyGate_ReplanDeniedComposesStaleAndAutonomyReason(t *testing.T) {
	tests := []struct {
		name       string
		autonomy   RepoAutonomy
		wantReason string
	}{
		{"interactive", RepoAutonomyInteractive, "plan stale, re-plan blocked: repo autonomy not lean"},
		{"missing config", RepoAutonomyMissing, "plan stale, re-plan blocked: repo config missing"},
		{"malformed config", RepoAutonomyMalformed, "plan stale, re-plan blocked: repo config malformed"},
		{"unreadable config", RepoAutonomyUnreadable, "plan stale, re-plan blocked: repo config unreadable"},
		// #877: an autonomous re-plan on a fetch outage composes the same
		// staleness-plus-autonomy shape, naming the fetch-specific reason.
		{"fetch unconfirmed", RepoAutonomyFetchUnconfirmed, "plan stale, re-plan blocked: " + reasonAutonomyFetchUnconfirmed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInputs()
			in.Config.PlanRefined = true
			in.RepoAutonomy = map[string]RepoAutonomy{"o/r": tc.autonomy}
			in.Plans[0].CommitsBehind = 10 // stale
			got := Decide(in)
			assertDecisions(t, got, []wantDecision{{42, ActionSkip, tc.wantReason, ""}})
			if got[0].Reason == "plan stale, re-plan" {
				t.Fatalf("a denied re-plan must not collapse into the flag-off literal %q", "plan stale, re-plan")
			}
		})
	}
}

// TestDecideAutonomyGate_ReplanLeanDispatches is the replan-side positive
// counterpart: a stale plan with PlanRefined on and a lean RepoAutonomy
// dispatches as an autonomous re-plan exactly as before #851 (the existing
// TestDecideReplan_StalePlanFlagOnDispatches, now updated to grant lean
// explicitly, covers the full assertion shape; this pins the matrix
// membership alongside the denied cases above for a single readable table).
func TestDecideAutonomyGate_ReplanLeanDispatches(t *testing.T) {
	in := baseInputs()
	in.Config.PlanRefined = true
	in.RepoAutonomy = leanAutonomy("o/r")
	in.Plans[0].CommitsBehind = 10 // stale
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "re-plan — plan stale", "claude"}})
}

// -- planning.attended fleet switch (#1086) ----------------------------------
//
// RepoAutonomyAttended is produced only by RunOnce's narrowing step (never by
// the probe), mapping RepoAutonomyLean -> RepoAutonomyAttended when the fleet
// planning.attended switch is on. Decide/autonomyGateSkip need no notion of
// "attended is on" -- they only need a case for the new closed-set value,
// exactly like every other RepoAutonomy denial class.

// TestAutonomyGateSkip_AttendedDenies pins the direct unit-level mapping:
// autonomyGateSkip denies RepoAutonomyAttended with its own distinct reason,
// reasonAutonomyAttended, naming attended mode as the cause rather than
// falling through to the misleading "repo autonomy not lean"
// (reasonAutonomyInteractive) message the ticket forbids.
func TestAutonomyGateSkip_AttendedDenies(t *testing.T) {
	reason, gated := autonomyGateSkip(RepoAutonomyAttended)
	if !gated {
		t.Fatal("autonomyGateSkip(RepoAutonomyAttended) = (_, false), want gated")
	}
	if reason != reasonAutonomyAttended {
		t.Errorf("reason = %q, want %q", reason, reasonAutonomyAttended)
	}
	if reason == reasonAutonomyInteractive {
		t.Fatal("attended must not reuse reasonAutonomyInteractive (misleading: the repo IS lean)")
	}
}

// TestDecideAutonomyGate_AttendedDeniesFreshPlanningPickup covers the AC: a
// fresh Refined planning candidate whose repo autonomy has been narrowed to
// RepoAutonomyAttended (attended mode on, lean -> attended) is denied with
// the attended-specific reason, not the generic "repo autonomy not lean".
func TestDecideAutonomyGate_AttendedDeniesFreshPlanningPickup(t *testing.T) {
	in := planningCandidateInputs()
	in.RepoAutonomy = map[string]RepoAutonomy{"o/r": RepoAutonomyAttended}
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, reasonAutonomyAttended, ""}})
}

// TestDecideAutonomyGate_ReplanDeniedComposesStaleAndAttendedReason covers
// the AC: an autonomous re-plan candidate stale beyond tolerance in an
// attended-narrowed repo composes the staleness fact with the attended
// reason ("plan stale, re-plan blocked: <attended reason>"), mirroring every
// other denying RepoAutonomy class, rather than either the flag-off literal
// "plan stale, re-plan" or the bare attended reason alone.
func TestDecideAutonomyGate_ReplanDeniedComposesStaleAndAttendedReason(t *testing.T) {
	in := baseInputs()
	in.Config.PlanRefined = true
	in.RepoAutonomy = map[string]RepoAutonomy{"o/r": RepoAutonomyAttended}
	in.Plans[0].CommitsBehind = 10 // stale
	want := "plan stale, re-plan blocked: " + reasonAutonomyAttended
	got := Decide(in)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, want, ""}})
	if got[0].Reason == "plan stale, re-plan" {
		t.Fatalf("a denied re-plan must not collapse into the flag-off literal %q", "plan stale, re-plan")
	}
}

// TestDecideAutonomyGate_OtherClassesUnmaskedByAttended pins the
// narrowing-only invariant's decide.go half: adding RepoAutonomyAttended to
// autonomyGateSkip's switch must never collapse any pre-existing denying
// class's reason into reasonAutonomyAttended -- interactive/missing/
// malformed/unreadable/fetch_unconfirmed each keep their own byte-for-byte
// reason.
func TestDecideAutonomyGate_OtherClassesUnmaskedByAttended(t *testing.T) {
	tests := []struct {
		autonomy   RepoAutonomy
		wantReason string
	}{
		{RepoAutonomyInteractive, reasonAutonomyInteractive},
		{RepoAutonomyMissing, reasonAutonomyMissing},
		{RepoAutonomyMalformed, reasonAutonomyMalformed},
		{RepoAutonomyUnreadable, reasonAutonomyUnreadable},
		{RepoAutonomyFetchUnconfirmed, reasonAutonomyFetchUnconfirmed},
	}
	for _, tc := range tests {
		t.Run(string(tc.autonomy), func(t *testing.T) {
			in := planningCandidateInputs()
			in.RepoAutonomy = map[string]RepoAutonomy{"o/r": tc.autonomy}
			got := Decide(in)
			if len(got) != 1 {
				t.Fatalf("got %d decisions, want 1: %+v", len(got), got)
			}
			if got[0].Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got[0].Reason, tc.wantReason)
			}
			if got[0].Reason == reasonAutonomyAttended {
				t.Fatalf("must not collapse into reasonAutonomyAttended, got %q", got[0].Reason)
			}
		})
	}
}

// -- #852 AC2: mixed valid and malformed dependency lines --------------------

// TestDecideDependencyGate_MixedValidAndMalformedDependency_HeldWithTruncatedToken
// covers AC2: a body containing both a valid, already-closed dependency
// (#10) and a malformed/overflowing token holds the ticket, naming the
// malformed token in the skip reason, and that token is truncated to
// maxDependencyTokenBytes so a hostile digit run can never flood the log
// line.
func TestDecideDependencyGate_MixedValidAndMalformedDependency_HeldWithTruncatedToken(t *testing.T) {
	hugeDigits := strings.Repeat("9", maxDependencyTokenBytes*2)
	body := "Depends on #10\nDepends on #" + hugeDigits

	nums, anomalies := parseDependsOn(body)
	if !equalInts(nums, []int{10}) {
		t.Fatalf("nums = %v, want [10] (the huge digit run must never resolve to a valid dependency)", nums)
	}
	if len(anomalies) != 1 {
		t.Fatalf("anomalies = %v, want exactly one anomaly for the huge digit run", anomalies)
	}
	if len(anomalies[0]) > maxDependencyTokenBytes {
		t.Fatalf("anomaly token length = %d bytes, want truncated to at most maxDependencyTokenBytes (%d)", len(anomalies[0]), maxDependencyTokenBytes)
	}

	in := baseInputs()
	in.Tickets[0].DependsOn = nums
	in.Tickets[0].DependencyStates = map[int]DependencyState{10: DependencyStateClosed}
	in.Tickets[0].DependencyAnomalies = anomalies

	got := Decide(in)
	if len(got) != 1 {
		t.Fatalf("got %d decisions, want 1: %+v", len(got), got)
	}
	d := got[0]
	if d.Action != ActionSkip {
		t.Fatalf("action = %q, want %q (a malformed dependency token must hold the ticket)", d.Action, ActionSkip)
	}
	if !strings.Contains(d.Reason, anomalies[0]) {
		t.Errorf("reason %q does not name the malformed token %q", d.Reason, anomalies[0])
	}
}

// -- #852 AC3: verified plan absence vs. plan-probe errors -------------------

// TestDecidePlanProbeGate_VerifiedAbsence_SkipsNoPlanFile covers AC3's
// "missing" half: a ticket with no PlanProbes entry at all (verified
// absence, the zero value) skips with the existing "no plan file" text --
// proving the new plan-probe gate does not disturb pre-#852 behavior for a
// genuinely absent plan.
func TestDecidePlanProbeGate_VerifiedAbsence_SkipsNoPlanFile(t *testing.T) {
	in := baseInputs()
	in.Plans = nil
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "no plan file", ""}})
}

// TestDecidePlanProbeGate_UnreadablePlan_SkipsWithDistinctReason covers
// AC3's "unreadable" half: a plan file that exists but could not be read
// (PlanProbeReadError) must skip with a reason distinct from "no plan
// file" -- proving "missing" and "unreadable" produce different decisions
// rather than collapsing into the same skip.
func TestDecidePlanProbeGate_UnreadablePlan_SkipsWithDistinctReason(t *testing.T) {
	in := baseInputs()
	in.Plans = nil
	in.PlanProbes = map[string]PlanProbe{"o/r#42": PlanProbeReadError}

	got := Decide(in)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, reasonPlanProbeUnreadable, ""}})
	if got[0].Reason == "no plan file" {
		t.Fatalf("an unreadable plan must not collapse into the verified-absence reason, got %q", got[0].Reason)
	}
}

// -- #852 AC4: a mid-write/malformed plan never launches fresh planning -----

// TestDecidePlanProbeGate_MidWriteMalformedPlan_NeverLaunchesFreshPlanning
// covers AC4, the ticket's headline regression: a Refined ticket (not
// Planned) whose .plans/<id>-*.md exists but is mid-write (truncated front
// matter) must classify PlanProbeParseError and skip -- never fall through
// to the stage-aware planning-pickup gate and launch a fresh, duplicate
// planning session just because ReadPlans produced no matched Plan for it.
func TestDecidePlanProbeGate_MidWriteMalformedPlan_NeverLaunchesFreshPlanning(t *testing.T) {
	in := planningCandidateInputs() // Refined, PlanRefined on, Plans == nil
	in.PlanProbes = map[string]PlanProbe{"o/r#42": PlanProbeParseError}

	got := Decide(in)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, reasonPlanProbeMalformed, ""}})
	if got[0].Planning {
		t.Fatal("Planning = true, want false -- a plan-probe error must never become a fresh planning candidate")
	}
	if got[0].Action == ActionDispatch {
		t.Fatal("a plan-probe error must never dispatch")
	}
}

// -- #852 AC5: staleness-calculation errors ----------------------------------

// TestDecidePlanProbeGate_StalenessError_SkipsRatherThanTreatingAsFresh
// covers AC5: a plan whose CommitsBehind could not be computed
// (PlanProbeStalenessError) must skip dispatch rather than be silently
// treated as 0-commits-behind (unknown-fresh).
func TestDecidePlanProbeGate_StalenessError_SkipsRatherThanTreatingAsFresh(t *testing.T) {
	in := baseInputs() // Plans[0].CommitsBehind left at its zero value (0)
	in.PlanProbes = map[string]PlanProbe{"o/r#42": PlanProbeStalenessError}

	got := Decide(in)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, reasonPlanProbeStale, ""}})
}

// TestDecidePlanProbeGate_StalenessError_NeverGatesAResumingTicket covers
// AC5's resume-path counter-test: an Input Needed + awaiting-input plan
// with a staleness-probe error still resumes -- staleness is never gated on
// the resume path (rule 4's freshness check is already deliberately skipped
// there).
func TestDecidePlanProbeGate_StalenessError_NeverGatesAResumingTicket(t *testing.T) {
	in := resumeInputs()
	in.PlanProbes = map[string]PlanProbe{"o/r#42": PlanProbeStalenessError}

	got := Decide(in)
	if len(got) != 1 {
		t.Fatalf("got %d decisions, want 1: %+v", len(got), got)
	}
	if got[0].Action != ActionDispatch || !got[0].Resume {
		t.Fatalf("decision = %+v, want a resumed dispatch (staleness must never gate the resume path)", got[0])
	}
}

// -- #852: default-branch coverage (watch/docs/go-gotchas.md #598) ----------

// TestDecidePlanProbeGate_UnregisteredValueSkipsWithDistinctReason covers
// the Test Strategy's default-branch-coverage requirement: an unregistered
// PlanProbe value must default-deny with its own distinct
// reasonPlanProbeUnknown, not silently collapse into any known reason.
func TestDecidePlanProbeGate_UnregisteredValueSkipsWithDistinctReason(t *testing.T) {
	in := baseInputs()
	in.Plans = nil
	in.PlanProbes = map[string]PlanProbe{"o/r#42": PlanProbe("bogus")}

	got := Decide(in)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, reasonPlanProbeUnknown, ""}})
	for _, r := range []string{"no plan file", reasonPlanProbeUnreadable, reasonPlanProbeMalformed, reasonPlanProbeStale} {
		if got[0].Reason == r {
			t.Fatalf("unrecognized PlanProbe must not collapse into %q", r)
		}
	}
}

// -- #884 AC2-AC4: the new PlanProbe classes (ambiguous, identity mismatch,
// path anomaly) ---------------------------------------------------------

// TestDecidePlanProbeGate_Ambiguous_SkipsWithDistinctReason covers AC3: two
// or more claims on one ticket key must skip with reasonPlanProbeAmbiguous,
// distinct from every other plan-probe reason.
func TestDecidePlanProbeGate_Ambiguous_SkipsWithDistinctReason(t *testing.T) {
	in := baseInputs()
	in.Plans = nil
	in.PlanProbes = map[string]PlanProbe{"o/r#42": PlanProbeAmbiguous}

	got := Decide(in)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, reasonPlanProbeAmbiguous, ""}})
}

// TestDecidePlanProbeGate_IdentityMismatch_SkipsWithDistinctReason covers
// AC2/Q2: a filename/front-matter identity mismatch must skip with
// reasonPlanProbeIdentityMismatch, distinct from every other plan-probe
// reason.
func TestDecidePlanProbeGate_IdentityMismatch_SkipsWithDistinctReason(t *testing.T) {
	in := baseInputs()
	in.Plans = nil
	in.PlanProbes = map[string]PlanProbe{"o/r#42": PlanProbeIDMismatch}

	got := Decide(in)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, reasonPlanProbeIdentityMismatch, ""}})
}

// TestDecidePlanProbeGate_PathAnomaly_SkipsWithDistinctReason covers AC6: a
// symlink/directory entry attributable to this ticket must skip with
// reasonPlanProbePathAnomaly, distinct from every other plan-probe reason.
func TestDecidePlanProbeGate_PathAnomaly_SkipsWithDistinctReason(t *testing.T) {
	in := baseInputs()
	in.Plans = nil
	in.PlanProbes = map[string]PlanProbe{"o/r#42": PlanProbePathAnomaly}

	got := Decide(in)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, reasonPlanProbePathAnomaly, ""}})
}

// -- #884 Q5: the plan-inventory gate (rule 0.5) -----------------------------

// TestDecidePlanInventoryGate_Verified_ZeroValuePermissive covers the
// permissive-zero-value contract: an Inputs.PlanInventories map miss (or a
// nil map, baseInputs' own default) never gates -- the happy path dispatches
// exactly as it did before #884.
func TestDecidePlanInventoryGate_Verified_ZeroValuePermissive(t *testing.T) {
	in := baseInputs()
	if in.PlanInventories != nil {
		t.Fatal("test assumption broken: baseInputs must leave PlanInventories nil")
	}
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
}

// TestDecidePlanInventoryGate_Unreadable_HoldsOrdinaryResumeAndPlanningAlike
// covers Q5's headline guarantee: an unreadable `.plans` directory holds
// EVERY ticket in the repo -- ordinary Planned dispatch, resume (Input
// Needed), and planning pickup (Refined) alike -- each with the same
// reasonPlanInventoryUnreadable reason, since a partial/unreadable
// enumeration can never prove no second file claims any given ticket.
func TestDecidePlanInventoryGate_Unreadable_HoldsOrdinaryResumeAndPlanningAlike(t *testing.T) {
	inventories := map[string]PlanInventory{"o/r": PlanInventoryUnreadable}

	ordinary := baseInputs()
	ordinary.PlanInventories = inventories
	assertDecisions(t, Decide(ordinary), []wantDecision{{42, ActionSkip, reasonPlanInventoryUnreadable, ""}})

	resume := resumeInputs()
	resume.PlanInventories = inventories
	assertDecisions(t, Decide(resume), []wantDecision{{42, ActionSkip, reasonPlanInventoryUnreadable, ""}})

	planning := planningCandidateInputs()
	planning.PlanInventories = inventories
	got := Decide(planning)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, reasonPlanInventoryUnreadable, ""}})
	if got[0].Planning {
		t.Fatal("Planning = true, want false -- an unreadable inventory must never launch a fresh planning pickup")
	}
}

// TestDecidePlanInventoryGate_Partial_HoldsWithDistinctReason covers the
// Partial half of Q5: a mid-enumeration-partial read holds with its own
// distinct reason, not collapsed into the unreadable reason.
func TestDecidePlanInventoryGate_Partial_HoldsWithDistinctReason(t *testing.T) {
	in := baseInputs()
	in.PlanInventories = map[string]PlanInventory{"o/r": PlanInventoryPartial}

	got := Decide(in)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, reasonPlanInventoryPartial, ""}})
	if got[0].Reason == reasonPlanInventoryUnreadable {
		t.Fatal("a partial read must not collapse into the unreadable reason")
	}
}

// TestDecidePlanInventoryGate_UnregisteredValueSkipsWithDistinctReason
// covers the Test Strategy's default-branch-coverage requirement
// (watch/docs/go-gotchas.md #598): an unregistered PlanInventory value must
// default-deny with its own distinct reasonPlanInventoryUnknown, not
// silently collapse into any known reason or (worse) the permissive zero
// value.
func TestDecidePlanInventoryGate_UnregisteredValueSkipsWithDistinctReason(t *testing.T) {
	in := baseInputs()
	in.PlanInventories = map[string]PlanInventory{"o/r": PlanInventory("bogus")}

	got := Decide(in)
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, reasonPlanInventoryUnknown, ""}})
	for _, r := range []string{reasonPlanInventoryUnreadable, reasonPlanInventoryPartial} {
		if got[0].Reason == r {
			t.Fatalf("unrecognized PlanInventory must not collapse into %q", r)
		}
	}
}

// -- #881: open-PR inventory completeness gate (openPRGateSkip) -------------
//
// Design note (Phase 3/red): decide.go does not define reason constants or
// openPRGateSkip yet (that is Phase 4's job), so the exact skip-reason
// strings below are literal, chosen here as the observable contract Phase 4
// must satisfy -- mirroring the #851 section's own precedent above (hardcode
// the literal string, not a not-yet-existing decide.go symbol, since the
// rendered string is the real downstream/log-facing behavior, independent of
// whatever Go identifier Phase 4 picks for it):
//   - OpenPRProbeCapExhausted -> "open PR state incomplete: pagination cap exhausted"
//   - OpenPRProbeTruncated    -> "open PR state incomplete: closing-issue references truncated"
//   - OpenPRProbeMalformed    -> "open PR state unreadable: malformed page"
//   - OpenPRProbeTimeout      -> "open PR state unreadable: probe timed out"
//   - OpenPRProbeUnreadable   -> "open PR state unreadable: pagination failed"
//   - unrecognized/default    -> "open PR probe unrecognized"
//
// The OpenPRProbe enum constants themselves (types.go, unlike decide.go's
// reasons) are referenced symbolically below, exactly as openpr_test.go
// already must: both files need the same not-yet-existing type either way,
// so there is no decoupling benefit to hardcoding the enum's underlying
// string the way there is for decide.go's reason text.
//
// openPRGateSkip is called in decideTicket immediately BEFORE the existing
// `if t.HasOpenPR` check (Files to Modify), so an affected repo reports one
// uniform reason across all its tickets rather than falling through to the
// ordinary "open PR exists" check on a probe that could not actually prove
// there is no open PR.

// TestTicketOpenPRProbeZeroValueIsComplete locks in Q2's resolution:
// OpenPRProbeComplete must be the zero value ("") so every pre-#881 Ticket
// construction site (reconcile paths, the ~100 existing test literals across
// this package) keeps today's behavior unchanged without being touched.
func TestTicketOpenPRProbeZeroValueIsComplete(t *testing.T) {
	var tk Ticket
	if tk.OpenPRProbe != OpenPRProbeComplete {
		t.Errorf("zero-value Ticket.OpenPRProbe = %q, want OpenPRProbeComplete", tk.OpenPRProbe)
	}
	if OpenPRProbeComplete != "" {
		t.Errorf("OpenPRProbeComplete = %q, want the empty string (the zero value)", string(OpenPRProbeComplete))
	}
}

// TestDecideOpenPRGate_EachFailureClassSkipsWithDistinctReason covers AC2:
// every non-complete OpenPRProbe classification gates dispatch with its own
// content-specific reason (#446/#598) -- never a bare non-empty check, and
// never collapsing into any other class's reason.
func TestDecideOpenPRGate_EachFailureClassSkipsWithDistinctReason(t *testing.T) {
	tests := []struct {
		name       string
		probe      OpenPRProbe
		wantReason string
	}{
		{"cap exhausted", OpenPRProbeCapExhausted, "open PR state incomplete: pagination cap exhausted"},
		{"truncated", OpenPRProbeTruncated, "open PR state incomplete: closing-issue references truncated"},
		{"malformed", OpenPRProbeMalformed, "open PR state unreadable: malformed page"},
		{"timeout", OpenPRProbeTimeout, "open PR state unreadable: probe timed out"},
		{"unreadable", OpenPRProbeUnreadable, "open PR state unreadable: pagination failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInputs()
			in.Tickets[0].OpenPRProbe = tc.probe
			assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, tc.wantReason, ""}})
		})
	}

	seen := map[string]string{}
	for _, tc := range tests {
		if prior, ok := seen[tc.wantReason]; ok {
			t.Fatalf("both %q and %q share the reason %q -- must be distinct", prior, tc.name, tc.wantReason)
		}
		seen[tc.wantReason] = tc.name
	}
}

// TestDecideOpenPRGate_UnrecognizedProbeValueSkipsWithDistinctReason covers
// openPRGateSkip's `default:` branch -- an unregistered OpenPRProbe value
// must default-deny with its own distinct reason, not silently collapse
// into any of the five known classes above, per #598's asserted-default-
// branch requirement.
func TestDecideOpenPRGate_UnrecognizedProbeValueSkipsWithDistinctReason(t *testing.T) {
	in := baseInputs()
	in.Tickets[0].OpenPRProbe = OpenPRProbe("bogus")

	got := Decide(in)
	want := "open PR probe unrecognized"
	assertDecisions(t, got, []wantDecision{{42, ActionSkip, want, ""}})
	for _, r := range []string{
		"open PR state incomplete: pagination cap exhausted",
		"open PR state incomplete: closing-issue references truncated",
		"open PR state unreadable: malformed page",
		"open PR state unreadable: probe timed out",
		"open PR state unreadable: pagination failed",
	} {
		if got[0].Reason == r {
			t.Fatalf("unrecognized OpenPRProbe must not collapse into %q", r)
		}
	}
}

// TestDecidePlanInventoryGate_OtherRepoUnaffected covers the per-repo
// scoping half of Q5: an unreadable inventory in one repo must never gate a
// ticket in a different repo.
func TestDecidePlanInventoryGate_OtherRepoUnaffected(t *testing.T) {
	in := baseInputs()
	in.PlanInventories = map[string]PlanInventory{"other/repo": PlanInventoryUnreadable}

	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
}

// TestDecideOpenPRGate_OrderingSitsImmediatelyBeforeHasOpenPRCheck proves the
// gate's placement (Files to Modify: "immediately before the existing `if
// t.HasOpenPR` check"): a non-complete probe reports its own reason even
// when HasOpenPR also happens to be true (the probe could not actually prove
// there is no open PR, so its own incompleteness reason must win), while a
// complete probe with HasOpenPR true falls through unchanged to today's
// "open PR exists" skip -- a pure regression guard that the new gate never
// perturbs the existing check for a healthy probe.
func TestDecideOpenPRGate_OrderingSitsImmediatelyBeforeHasOpenPRCheck(t *testing.T) {
	t.Run("non-complete probe reports its own reason even when HasOpenPR is also true", func(t *testing.T) {
		in := baseInputs()
		in.Tickets[0].OpenPRProbe = OpenPRProbeUnreadable
		in.Tickets[0].HasOpenPR = true
		assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "open PR state unreadable: pagination failed", ""}})
	})

	t.Run("complete probe with HasOpenPR true falls through to the ordinary open-PR-exists check unchanged", func(t *testing.T) {
		in := baseInputs()
		in.Tickets[0].OpenPRProbe = OpenPRProbeComplete
		in.Tickets[0].HasOpenPR = true
		assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, "open PR exists", ""}})
	})
}

// TestDecideOpenPRGate_ZeroValueIsUngated covers Q2's permissive-zero
// contract end-to-end: a Ticket literal that never sets OpenPRProbe at all
// (every pre-#881 construction site) dispatches normally, unaffected by the
// new gate.
func TestDecideOpenPRGate_ZeroValueIsUngated(t *testing.T) {
	in := baseInputs() // in.Tickets[0].OpenPRProbe left at its zero value
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "claude"}})
}

// TestDecideOpenPRGate_GatesEveryPickupType covers AC6's explicit
// requirement to cover both an ordinary Planned pickup and a Refined
// planning pickup, plus the autonomous re-plan pickup for completeness,
// mirroring TestDecideMainSyncGate_NewCheckoutStatesGateEveryPickupType
// (#851)'s "gates ALL pickup types" pattern: an incomplete open-PR inventory
// must hold every kind of dispatch for the affected ticket, not only the
// ordinary already-Planned path.
func TestDecideOpenPRGate_GatesEveryPickupType(t *testing.T) {
	const wantReason = "open PR state unreadable: pagination failed"

	t.Run("ordinary Planned pickup", func(t *testing.T) {
		in := baseInputs()
		in.Tickets[0].OpenPRProbe = OpenPRProbeUnreadable
		assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, wantReason, ""}})
	})
	t.Run("fresh Refined planning candidate", func(t *testing.T) {
		in := planningCandidateInputs()
		in.Tickets[0].OpenPRProbe = OpenPRProbeUnreadable
		assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, wantReason, ""}})
	})
	t.Run("autonomous re-plan candidate", func(t *testing.T) {
		in := baseInputs()
		in.Config.PlanRefined = true
		in.RepoAutonomy = leanAutonomy("o/r")
		in.Plans[0].CommitsBehind = 10 // stale
		in.Tickets[0].OpenPRProbe = OpenPRProbeUnreadable
		assertDecisions(t, Decide(in), []wantDecision{{42, ActionSkip, wantReason, ""}})
	})
}
