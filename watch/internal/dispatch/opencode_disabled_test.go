package dispatch

import (
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
)

func TestDisabledOpenCodeDoesNotDispatchOrChangeLabels(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for _, mode := range []string{"ordinary", "resume", "planning"} {
		for _, selection := range []string{"label", "default", "default with whitespace"} {
			t.Run(mode+"/"+selection, func(t *testing.T) {
				cfg := dispatchTestConfig()
				deps := dispatchableDeps(now)
				if mode == "resume" {
					deps = resumeDispatchDeps(now)
				}
				if mode == "planning" {
					cfg.PlanRefined = true
					deps = planningDispatchDeps(now)
				}
				if selection == "label" {
					deps.Tickets[0].Agent = "opencode"
				} else {
					cfg.DefaultAgent = "opencode"
					if selection == "default with whitespace" {
						cfg.DefaultAgent = " opencode "
					}
				}
				cfg.AgentPreference = []string{"codex", "claude"}
				stubRunFn(t, func(run.Opts, run.Controller) error {
					t.Fatal("disabled primary must never spawn or silently switch agents")
					return nil
				})
				mut := &fakeMutator{}
				prior := 0
				got, err := applyDispatch(cfg, deps, fakeController{}, mut, false, nil, &prior)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != 1 || got[0].Action != ActionSkip || !strings.Contains(got[0].Reason, "OpenCode is temporarily disabled") {
					t.Fatalf("expected disabled skip: %+v", got)
				}
				if len(mut.labelEdits) != 0 || prior != 0 {
					t.Fatalf("refusal changed labels/quota: %+v, %d", mut.labelEdits, prior)
				}
			})
		}
	}
}

func TestDisabledOpenCodeFallbackIsSkipped(t *testing.T) {
	in := baseInputs()
	in.Budgets = FloorProvider{Floors: map[string]float64{"claude": 0}}
	in.Config.AgentPreference = []string{"opencode", "codex"}
	assertDecisions(t, Decide(in), []wantDecision{{42, ActionDispatch, "dispatch", "codex"}})
}
