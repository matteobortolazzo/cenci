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

// RunOnce gathers inputs (tickets, plans, snapshot, clock), runs the pure
// engine, logs every decision, then dispatches each ActionDispatch via run.Run
// (unless dryRun). prior, when non-nil, is the daily-quota tally: it is read in
// and incremented per successful dispatch. It returns the full decision table.
func RunOnce(cfg Config, ctrl run.Controller, dryRun bool, out io.Writer, prior *int) []Decision {
	if out == nil {
		out = os.Stdout
	}

	dirByRepo := make(map[string]string, len(cfg.Repos))
	for _, rc := range cfg.Repos {
		dirByRepo[rc.Repo] = rc.Dir
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

	priorVal := 0
	if prior != nil {
		priorVal = *prior
	}

	now := time.Now()

	decisions := Decide(Inputs{
		Tickets:  tickets,
		Plans:    plans,
		Snapshot: snap,
		Budgets:  buildBudgetProvider(cfg, now),
		Now:      now,
		Prior:    priorVal,
		Config:   cfg,
	})

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
		err := run.Run(run.Opts{
			Workflow: "implement",
			Ticket:   filepath.Join(".plans", filepath.Base(d.Plan.Path)),
			Agent:    d.Agent,
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
	}
	return decisions
}

// RunLoop reloads Config from configPath and runs RunOnce immediately and then
// on every interval tick, threading the daily-quota tally in memory (it resets
// on process restart — acceptable for #45). It blocks until the process exits.
func RunLoop(configPath string, ctrl run.Controller, interval time.Duration, out io.Writer) {
	prior := 0
	dispatchTick(configPath, ctrl, out, &prior)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		dispatchTick(configPath, ctrl, out, &prior)
	}
}

// dispatchTick is RunLoop's per-tick body: reload Config from configPath, then
// run one dispatch pass against the freshly-loaded config. On a reload error the
// tick is skipped entirely (no dispatch, prior left untouched) so a bad edit
// between ticks cannot crash the loop or silently dispatch against a stale
// config.
func dispatchTick(configPath string, ctrl run.Controller, out io.Writer, prior *int) {
	cfg, ok := reloadConfig(configPath, out)
	if !ok {
		return
	}
	RunOnce(cfg, ctrl, false, out, prior)
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

// formatDecision renders one decision as a single log line. Dispatch lines carry
// the resolved agent and plan file so the table is self-explanatory.
func formatDecision(d Decision) string {
	if d.Action == ActionDispatch && d.Plan != nil {
		return fmt.Sprintf("#%d dispatch (%s, %s): %s",
			d.Ticket.Number, d.Agent, filepath.Base(d.Plan.Path), d.Reason)
	}
	return fmt.Sprintf("#%d skip: %s", d.Ticket.Number, d.Reason)
}
