package dispatch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/planfile"
	"github.com/matteobortolazzo/cenci/watch/internal/run"
	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

// runFn is the spawn seam: applyDispatch calls it instead of run.Run directly
// so tests never ensure a real daemon or tmux window (same idiom as
// internal/daemon's spawn var).
var runFn = run.Run

// replanContextToken (#828) is the escape-hatch token appended to the run
// Ticket argument for an autonomous re-plan dispatch ("<number> replan"), so
// the dispatched implement session's --replan-requested branch
// (flow/skills/implement/SKILL.md:116) skips Phase 1's human-confirmation
// gate for the stale-plan branch -- an unattended session that landed on that
// gate would hang as a Working ticket until the reconciler reaped it.
// Literal shared across projects (#357, watch/AGENTS.md): grep both ends
// before ever renaming it.
const replanContextToken = "replan"

// dispatchTicketArg builds the run.Opts.Ticket argument for d, per kind
// (#828): the ordinary and resume kinds launch the matched plan file exactly
// as before; a planning-pickup dispatch (Plan == nil) launches the bare
// ticket number so the implement session's Phase 1 discovers there is no
// plan yet; an autonomous re-plan (Planning && Replan, Plan != nil) appends
// replanContextToken so the session's --replan-requested branch skips
// Phase 1's human-confirmation gate on the stale-plan path. WindowTicket is
// always the bare ticket number, set separately at the call site.
func dispatchTicketArg(d Decision) string {
	if d.Plan == nil {
		return strconv.Itoa(d.Ticket.Number)
	}
	if d.Planning && d.Replan {
		return strconv.Itoa(d.Ticket.Number) + " " + replanContextToken
	}
	return filepath.Join(".plans", filepath.Base(d.Plan.Path))
}

// dispatchDeps are the collected, impure inputs to a dispatch pass. Separating
// them from RunOnce lets applyDispatch be exercised without gh or a daemon.
type dispatchDeps struct {
	Tickets     []Ticket
	Plans       []Plan
	Snapshot    *watch.StateSnapshot
	Now         time.Time
	CurrentUser string
	// Answers maps planKey(repo, number) -> the resolved AnswerProbe for
	// every `Input Needed` ticket this pass (#827), resolved by
	// resolveAnswerProbes (resume.go) before applyDispatch/Decide ever run.
	Answers map[string]AnswerProbe

	// RepoAutonomy maps repo -> the resolved RepoAutonomy for that repo this
	// pass (#851), resolved by probeRepoAutonomies before applyDispatch/
	// Decide ever run -- threaded straight through to Inputs.RepoAutonomy.
	RepoAutonomy map[string]RepoAutonomy

	// PlanProbes maps planKey(repo, ticketId) -> the resolved PlanProbe for
	// every plan file ReadPlans encountered this pass (#852), merged across
	// every repo's ReadPlans call in RunOnce. Threaded straight through to
	// Inputs.PlanProbes so Decide can distinguish a verified-absent plan
	// from one that is broken in some way, without doing any I/O itself.
	PlanProbes map[string]PlanProbe
}

// RunOnce gathers inputs (tickets, plans, snapshot, clock), runs the pure
// engine, logs every decision, then dispatches each ActionDispatch via run.Run
// (unless dryRun). prior, when non-nil, is the daily-quota tally: it is read in
// and incremented per successful dispatch. It returns the full decision table
// alongside the first collection error encountered (CollectTickets, then
// per-repo ReadPlans), if any -- all existing logging is unchanged, this only
// additionally surfaces the error to the caller instead of swallowing it.
func RunOnce(cfg Config, ctrl run.Controller, mut TicketMutator, dryRun bool, out io.Writer, prior *int) ([]Decision, error) {
	if out == nil {
		out = os.Stdout
	}

	// Once per pass, per enrolled repo, sync local main with origin/main
	// before collecting tickets/plans (#822) -- so staleness and new
	// planning sessions never evaluate against stale code. RunOnce is the
	// sole owner of this sync: RunReconcileOnce also calls CollectTickets in
	// the same combined pass (combined.go) but deliberately passes a nil
	// map and never syncs, so a fetch+merge never runs twice per pass.
	syncs := syncMains(cfg.Repos, out, dryRun)

	// Repo-autonomy probe (#851): reads each repo's committed
	// `planning.autonomy` at its own resolved FreshRef (from syncs above),
	// so a Refined planning pickup or autonomous re-plan can no longer be
	// authorized by dispatch.planRefined alone.
	autonomies := probeRepoAutonomies(cfg.Repos, syncs, out)

	tickets, err := CollectTickets(cfg.Repos, syncStatuses(syncs), true, out)
	if err != nil {
		logf(out, "dispatch: collecting tickets: %v\n", err)
	}
	collectErr := err

	// Empty passes need no identity. This preserves dry-run/config diagnostics
	// in unauthenticated environments while every pass with collectible work
	// still fails closed before making a dispatch decision.
	currentUser := ""
	if len(tickets) > 0 {
		currentUser, err = currentGitHubLogin()
		if err != nil {
			logf(out, "dispatch: detecting current GitHub user: %v\n", err)
			return nil, fmt.Errorf("detecting current GitHub user: %w", err)
		}
	}

	plans, planProbes, err := readPlansForRepos(cfg.Repos, syncs, out)
	if err != nil && collectErr == nil {
		collectErr = err
	}

	snap, _ := ReadSnapshot(watch.DefaultSocketPath()) // nil on error ⇒ Decide skips safely

	// Probe every `Input Needed` ticket for a human answer to its escalation
	// (#827/#849), keyed by planKey so Decide's Inputs.Answers can look it
	// up -- mirrors RunReconcileOnce's countAttempts loop, a label-scoped
	// per-ticket gh read outside the collector. planByTicket (indexPlans,
	// decide.go) supplies each ticket's persisted escalation anchor fields
	// so the REST probe knows which comment ID/nonce to verify. A probe
	// failure fails closed (AnswerProbeUnresolved) and is only logged -- it
	// never feeds collectErr, matching fetchDependencyState (a gate input)
	// rather than countAttempts (a destructive-verdict input): one
	// transient gh hiccup must never paint the whole pass
	// dispatch_pass_failed.
	answers := resolveAnswerProbes(tickets, indexPlans(plans), out)

	decisions, applyErr := applyDispatch(cfg, dispatchDeps{
		Tickets:      tickets,
		Plans:        plans,
		Snapshot:     snap,
		Now:          time.Now(),
		CurrentUser:  currentUser,
		Answers:      answers,
		RepoAutonomy: autonomies,
		PlanProbes:   planProbes,
	}, ctrl, mut, dryRun, out, prior)
	return decisions, firstNonNil(collectErr, applyErr)
}

// readPlansForRepos reads every repo's .plans directory (#851), extracted
// from RunOnce's own inline loop so it can be exercised directly against a
// real temp repo (the dry-run/real-pass parity test at this seam). Each
// repo's commitsBehind closure is FreshRef-aware: when a repo's resolved
// FreshRef (from syncs, the syncMains map) is "HEAD" (the common case), nil
// is passed through -- ReadPlans' existing default wiring already reads
// planfile.CommitsBehind against local HEAD. Otherwise (the dry-run
// strictly-behind fast-forward-pending case, FreshRef == "origin/main"), the
// closure scopes the read to that ref via planfile.CommitsBehindRef, so
// dry-run's staleness computation matches the fetched blob the subsequent
// real pass would consume, rather than stale local HEAD. A repo absent from
// syncs (should not happen via RunOnce's own wiring, but defensive) falls
// back to "HEAD".
func readPlansForRepos(repos []RepoConfig, syncs map[string]mainSyncResult, out io.Writer) ([]Plan, map[string]PlanProbe, error) {
	var plans []Plan
	planProbes := map[string]PlanProbe{}
	var firstErr error
	for _, rc := range repos {
		ref := "HEAD"
		if s, ok := syncs[rc.Repo]; ok && s.FreshRef != "" {
			ref = s.FreshRef
		}
		var commitsBehind func(sha string, paths []string) (int, error)
		if ref != "HEAD" {
			dir, refCopy, repo := rc.Dir, ref, rc.Repo
			// The error is propagated, never swallowed to 0 (#852 AC5): a
			// commits-behind failure against the fetched ref must reach
			// ReadPlans so it classifies as PlanProbeStalenessError and
			// default-denies, rather than masquerading as "0 commits
			// behind" (unknown-fresh) and readmitting a ticket whose
			// freshness was never actually verified.
			commitsBehind = func(sha string, paths []string) (int, error) {
				n, err := planfile.CommitsBehindRef(dir, sha, refCopy, paths)
				if err != nil {
					logf(out, "dispatch: commits-behind check failed for repo %s @ %s: %v\n", repo, refCopy, err)
					return 0, err
				}
				return n, nil
			}
		}
		ps, probes, err := ReadPlans(rc.Repo, rc.Dir, commitsBehind, out)
		if err != nil {
			logf(out, "dispatch: reading plans in %s: %v\n", rc.Dir, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		plans = append(plans, ps...)
		for k, v := range probes {
			planProbes[k] = v
		}
	}
	return plans, planProbes, firstErr
}

// applyDispatch runs the pure engine over already-collected deps, logs, spawns
// each dispatch (unless dryRun), and synchronously claims each spawned ticket
// with the Working label so no later pass can re-pick it during the
// spawn→self-label window. A failed claim is logged and the pass continues —
// the spawn already happened, and the reconciler recovers label drift. It is
// the testable core of RunOnce.
func applyDispatch(cfg Config, deps dispatchDeps, ctrl run.Controller, mut TicketMutator, dryRun bool, out io.Writer, prior *int) ([]Decision, error) {
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
		Tickets:      deps.Tickets,
		Plans:        deps.Plans,
		Snapshot:     deps.Snapshot,
		Budgets:      buildBudgetProvider(cfg, deps.Now),
		Now:          deps.Now,
		Prior:        priorVal,
		CurrentUser:  deps.CurrentUser,
		Config:       cfg,
		Answers:      deps.Answers,
		RepoAutonomy: deps.RepoAutonomy,
		PlanProbes:   deps.PlanProbes,
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
		return decisions, nil
	}

	var firstErr error
	for _, d := range decisions {
		// A planning-pickup dispatch (#828) carries Plan == nil by design (no
		// plan file exists yet) -- admit it alongside the ordinary/resume/
		// re-plan kinds, all of which carry a non-nil Plan.
		if d.Action != ActionDispatch || (d.Plan == nil && !d.Planning) {
			continue
		}

		// Resume path (#827): swap Input Needed -> Working BEFORE spawning,
		// not after. A failed post-spawn claim on the ordinary path (below)
		// is safely recovered by the reconciler next pass; here it would
		// leave the ticket at Input Needed forever, so every subsequent
		// pass would re-resume it and spawn an unbounded stream of planning
		// sessions. A failed swap therefore skips the spawn entirely. This
		// "+Working -Input Needed" claim/rollback label-set contract is
		// shared with the manual lane (pipeline.ApplyLabelTransition's
		// "working" transition, watch/internal/pipeline/labels.go, #853):
		// both realize +Working -Input Needed on claim, and its exact
		// inverse (+Input Needed -Working) on rollback/restore -- here, on a
		// non-ErrWindowSpawned spawn failure (below); there, via the
		// "input-needed" transition or the reconciler's interrupted-resume
		// recovery.
		if d.Resume {
			if err := mut.EditLabels(d.Ticket.Repo, d.Ticket.Number, []string{labelWorking}, []string{labelInputNeeded}); err != nil {
				logf(out, "dispatch: #%d resume claim failed: %v\n", d.Ticket.Number, err)
				firstErr = firstNonNil(firstErr, err)
				continue
			}
		}

		err := runFn(run.Opts{
			Workflow:     "implement",
			Ticket:       dispatchTicketArg(d),
			WindowTicket: strconv.Itoa(d.Ticket.Number),
			Agent:        d.Agent,
			Model:        cfg.Model,
			Session:      cfg.Session,
			Dir:          dirByRepo[d.Ticket.Repo],
		}, ctrl)
		if err != nil {
			logf(out, "dispatch: #%d run failed: %v\n", d.Ticket.Number, err)
			firstErr = firstNonNil(firstErr, err)
			// A resume spawn failure needs a label decision (#853): a
			// demonstrably-created tmux window (ErrWindowSpawned, i.e. the
			// failure happened after ctrl.NewWindow already succeeded)
			// counts as confirmed-alive launch evidence -- Working is
			// retained, and the reconciler's interrupted-resume recovery is
			// the backstop if that session turns out to be dead. Any other
			// resume spawn failure rolls the claim back to Input Needed so
			// the ticket stays resumable on a later pass rather than being
			// stranded at Working with no live session.
			if d.Resume {
				if errors.Is(err, run.ErrWindowSpawned) {
					logf(out, "dispatch: #%d window created, retaining Working; reconciler will recover if the session is dead\n", d.Ticket.Number)
				} else if rerr := mut.EditLabels(d.Ticket.Repo, d.Ticket.Number, []string{labelInputNeeded}, []string{labelWorking}); rerr != nil {
					logf(out, "dispatch: #%d resume rollback failed: %v\n", d.Ticket.Number, rerr)
					firstErr = firstNonNil(firstErr, rerr)
				} else {
					logf(out, "dispatch: #%d resume spawn failed, rolled back to Input Needed\n", d.Ticket.Number)
				}
			}
			continue
		}
		if prior != nil {
			*prior++
		}
		// The resume path already claimed Working above, before the spawn;
		// the ordinary path claims it here, synchronously after, so no later
		// pass can re-pick the ticket during the spawn→self-label window.
		if !d.Resume {
			if err := mut.EditLabels(d.Ticket.Repo, d.Ticket.Number, []string{labelWorking}, nil); err != nil {
				logf(out, "dispatch: #%d claim label failed: %v\n", d.Ticket.Number, err)
				firstErr = firstNonNil(firstErr, err)
			}
		}
	}
	return decisions, firstErr
}

// RunLoop reloads Config from configPath and runs RunOnce immediately and then
// on every interval tick, threading the daily-quota tally in memory (it resets
// on process restart — acceptable for #45). modelOverride, when non-empty (the
// --model CLI flag), is re-applied to Config every tick, since each tick
// reloads Config fresh from disk and would otherwise drop it. It blocks until
// the process exits.
func RunLoop(configPath string, ctrl run.Controller, mut TicketMutator, interval time.Duration, out io.Writer, modelOverride string) error {
	prior := 0
	if err := dispatchTick(configPath, ctrl, mut, out, &prior, modelOverride); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if err := dispatchTick(configPath, ctrl, mut, out, &prior, modelOverride); err != nil {
			return err
		}
	}
	return nil
}

// dispatchTick is RunLoop's per-tick body: reload Config from configPath,
// re-apply modelOverride (if set) since the reload otherwise wins, then run
// one dispatch pass against the resulting config. On a reload error the tick
// is skipped entirely (no dispatch, prior left untouched) so a bad edit
// between ticks cannot crash the loop or silently dispatch against a stale
// config.
func dispatchTick(configPath string, ctrl run.Controller, mut TicketMutator, out io.Writer, prior *int, modelOverride string) error {
	cfg, ok := reloadConfig(configPath, out)
	if !ok {
		return fmt.Errorf("loading dispatch config")
	}
	if modelOverride != "" {
		cfg.Model = modelOverride
	}
	_, err := RunOnce(cfg, ctrl, mut, false, out, prior)
	return err
}

func firstNonNil(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
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
	// A planning-pickup dispatch (#828) carries Plan == nil by design -- no
	// plan file exists yet -- so it needs its own branch rather than falling
	// through to the skip: rendering below, which would silently break the
	// lazyboards " dispatch " substring contract for this dispatch kind.
	if d.Action == ActionDispatch && d.Planning {
		return fmt.Sprintf("%s#%d dispatch (%s): %s",
			d.Ticket.Repo, d.Ticket.Number, d.Agent, d.Reason)
	}
	return fmt.Sprintf("%s#%d skip: %s", d.Ticket.Repo, d.Ticket.Number, d.Reason)
}
