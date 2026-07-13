package dispatch

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/run"
	"github.com/matteobortolazzo/agent-stack/agentwatch/pkg/watch"
)

// runFn is the spawn seam: applyDispatch calls it instead of run.Run directly
// so tests never ensure a real daemon or tmux window (same idiom as
// internal/daemon's spawn var).
var runFn = run.Run

// dispatchDeps are the collected, impure inputs to a dispatch pass. Separating
// them from RunOnce lets applyDispatch be exercised without gh or a daemon.
type dispatchDeps struct {
	Tickets  []Ticket
	Plans    []Plan
	Snapshot *watch.StateSnapshot
	Now      time.Time
}

// RunOnce gathers inputs (tickets, plans, snapshot, clock), runs the pure
// engine, logs every decision, then dispatches each ActionDispatch via run.Run
// (unless dryRun). prior, when non-nil, is the daily-quota tally: it is read in
// and incremented per successful dispatch. It returns the full decision table.
func RunOnce(cfg Config, ctrl run.Controller, mut TicketMutator, dryRun bool, out io.Writer, prior *int) []Decision {
	if out == nil {
		out = os.Stdout
	}

	tickets, err := CollectTickets(cfg.Repos)
	if err != nil {
		logf(out, "dispatch: collecting tickets: %v\n", err)
	}

	var plans []Plan
	for _, rc := range cfg.Repos {
		ps, err := ReadPlans(rc.Repo, rc.Dir, nil)
		if err != nil {
			logf(out, "dispatch: reading plans in %s: %v\n", rc.Dir, err)
			continue
		}
		plans = append(plans, ps...)
	}

	snap, _ := ReadSnapshot(watch.DefaultSocketPath()) // nil on error ⇒ Decide skips safely

	return applyDispatch(cfg, dispatchDeps{
		Tickets:  tickets,
		Plans:    plans,
		Snapshot: snap,
		Now:      time.Now(),
	}, ctrl, mut, dryRun, out, prior)
}

// applyDispatch runs the pure engine over already-collected deps, logs, spawns
// each dispatch (unless dryRun), and synchronously claims each spawned ticket
// with the Working label so no later pass can re-pick it during the
// spawn→self-label window. A failed claim is logged and the pass continues —
// the spawn already happened, and the reconciler recovers label drift. It is
// the testable core of RunOnce.
func applyDispatch(cfg Config, deps dispatchDeps, ctrl run.Controller, mut TicketMutator, dryRun bool, out io.Writer, prior *int) []Decision {
	if out == nil {
		out = os.Stdout
	}

	dirByRepo := make(map[string]string, len(cfg.Repos))
	for _, rc := range cfg.Repos {
		dirByRepo[rc.Repo] = rc.Dir
	}

	priorVal := 0
	if prior != nil {
		priorVal = *prior
	}

	decisions := Decide(Inputs{
		Tickets:  deps.Tickets,
		Plans:    deps.Plans,
		Snapshot: deps.Snapshot,
		Budgets:  buildBudgetProvider(cfg, deps.Now),
		Now:      deps.Now,
		Prior:    priorVal,
		Config:   cfg,
	})

	// Surface the pinned model in the same log stream as the decision table so
	// it's never silent: a dispatch pass with no Model set falls back to
	// agents.*.model (or, absent that, whatever ambient default the agent CLI
	// itself resolves), which is precisely the drift this field exists to make
	// visible and overridable.
	if cfg.Model != "" {
		logf(out, "dispatch: model override %q\n", cfg.Model)
	}

	for _, d := range decisions {
		logf(out, "%s\n", formatDecision(d))
	}

	if dryRun {
		return decisions
	}

	for _, d := range decisions {
		if d.Action != ActionDispatch || d.Plan == nil {
			continue
		}
		err := runFn(run.Opts{
			Workflow: "implement",
			Ticket:   filepath.Join(".plans", filepath.Base(d.Plan.Path)),
			Agent:    d.Agent,
			Model:    cfg.Model,
			Session:  cfg.Session,
			Dir:      dirByRepo[d.Ticket.Repo],
		}, ctrl)
		if err != nil {
			logf(out, "dispatch: #%d run failed: %v\n", d.Ticket.Number, err)
			continue
		}
		if prior != nil {
			*prior++
		}
		if err := mut.EditLabels(d.Ticket.Repo, d.Ticket.Number, []string{labelWorking}, nil); err != nil {
			logf(out, "dispatch: #%d claim label failed: %v\n", d.Ticket.Number, err)
		}
	}
	return decisions
}

// RunLoop reloads Config from configPath and runs RunOnce immediately and then
// on every interval tick, threading the daily-quota tally in memory (it resets
// on process restart — acceptable for #45). modelOverride, when non-empty (the
// --model CLI flag), is re-applied to Config every tick, since each tick
// reloads Config fresh from disk and would otherwise drop it. It blocks until
// the process exits.
func RunLoop(configPath string, ctrl run.Controller, mut TicketMutator, interval time.Duration, out io.Writer, modelOverride string) {
	prior := 0
	dispatchTick(configPath, ctrl, mut, out, &prior, modelOverride)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		dispatchTick(configPath, ctrl, mut, out, &prior, modelOverride)
	}
}

// dispatchTick is RunLoop's per-tick body: reload Config from configPath,
// re-apply modelOverride (if set) since the reload otherwise wins, then run
// one dispatch pass against the resulting config. On a reload error the tick
// is skipped entirely (no dispatch, prior left untouched) so a bad edit
// between ticks cannot crash the loop or silently dispatch against a stale
// config.
func dispatchTick(configPath string, ctrl run.Controller, mut TicketMutator, out io.Writer, prior *int, modelOverride string) {
	cfg, ok := reloadConfig(configPath, out)
	if !ok {
		return
	}
	if modelOverride != "" {
		cfg.Model = modelOverride
	}
	RunOnce(cfg, ctrl, mut, false, out, prior)
}

// reloadConfig loads Config from configPath, logging and reporting failure so
// dispatchTick and combinedTick can skip their tick uniformly on a bad reload.
func reloadConfig(configPath string, out io.Writer) (Config, bool) {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		logf(out, "dispatch: loading config: %v\n", err)
		return Config{}, false
	}
	return cfg, true
}

// buildBudgetProvider returns a UsageProvider when AgentLimits are configured,
// falling back to the #45 FloorProvider otherwise.
func buildBudgetProvider(cfg Config, now time.Time) BudgetProvider {
	if len(cfg.AgentLimits) == 0 {
		return FloorProvider{Floors: cfg.AgentBudgetFloors}
	}

	readers := make(map[string]TokenReader, len(cfg.AgentLimits))
	for agent := range cfg.AgentLimits {
		switch agent {
		case "claude":
			dir := cfg.ClaudeSessionDir
			if dir == "" {
				home, _ := os.UserHomeDir()
				dir = filepath.Join(home, ".claude", "projects")
			}
			readers[agent] = &ClaudeTokenReader{BaseDir: dir}
		case "codex":
			dbPath := cfg.CodexDBPath
			if dbPath == "" {
				home, _ := os.UserHomeDir()
				dbPath = filepath.Join(home, ".codex", "state_5.sqlite")
			}
			readers[agent] = &CodexTokenReader{DBPath: dbPath}
		}
	}

	return &UsageProvider{
		Readers: readers,
		Limits:  cfg.AgentLimits,
		Floors:  cfg.AgentBudgetFloors,
		Now:     now,
		cache:   make(map[string]Budget),
	}
}

// logf writes a formatted progress line, ignoring write errors (logging to a
// terminal must never abort a dispatch pass).
func logf(out io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(out, format, args...)
}

// formatDecision renders one decision as a single log line, prefixed with
// owner/repo so multi-repo fleet output is unambiguous. Dispatch lines carry
// the resolved agent and plan file so the table is self-explanatory. The
// ` skip:` / ` dispatch ` substrings are load-bearing: downstream consumers
// (lazyboards) classify lines by matching on them.
func formatDecision(d Decision) string {
	if d.Action == ActionDispatch && d.Plan != nil {
		return fmt.Sprintf("%s#%d dispatch (%s, %s): %s",
			d.Ticket.Repo, d.Ticket.Number, d.Agent, filepath.Base(d.Plan.Path), d.Reason)
	}
	return fmt.Sprintf("%s#%d skip: %s", d.Ticket.Repo, d.Ticket.Number, d.Reason)
}
