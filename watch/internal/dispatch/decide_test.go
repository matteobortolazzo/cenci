package dispatch

import (
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

// testConfig has generous caps so no gate trips unless a case sets it.
func testConfig() Config {
	return Config{
		ConcurrencyCap:         10,
		NeedInputThreshold:     100,
		DailyQuota:             100,
		PlanStalenessTolerance: 5,
		DefaultAgent:           "claude",
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
