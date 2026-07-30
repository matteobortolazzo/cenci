package dispatch

import (
	"fmt"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

// testConfig has generous caps so no gate trips unless a case sets it.
// PipelineStageGate matches DefaultConfig()'s default-on value (#732) so the
// whole existing gate suite runs with the stage gate live; every existing
// test ticket is StageProbeAbsent (the zero value), which always passes the
// gate, so no existing expectation changes.
func testConfig() Config {
	return Config{
		ConcurrencyCap:         10,
		NeedInputThreshold:     100,
		DailyQuota:             100,
		PlanStalenessTolerance: 5,
		DefaultAgent:           "claude",
		PipelineStageGate:      true,
	}
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
