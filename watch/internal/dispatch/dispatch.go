package dispatch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/capacity"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/planfile"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// runFn is the spawn seam: applyDispatch calls it instead of run.Run directly
// so tests never ensure a real daemon or tmux window (same idiom as
// internal/daemon's spawn var).
var runFn = run.Run

// resolveLaunchSocket is the seam over the tmux socket a real pass probes and
// spawns on (#1431); tests stub it so no ambient $TMUX decides the socket.
var resolveLaunchSocket = tmux.ResolveSocket

// clearLaunchFn is the recorded-launch clearing seam (#1292): applyDispatch
// calls it before each spawn so tests can inject a failure.
var clearLaunchFn = pipeline.ClearLaunch

// dailyQuotaFn is the persisted daily launch counter seam (#1566). applyDispatch
// calls it only when Config.DailyQuota is set, so a pass with the quota off
// never opens, reads or writes the capacity store. Tests point it at a temp
// directory (TestMain, useQuotaDir) so no ambient state directory is touched.
var dailyQuotaFn = func() *capacity.DailyQuota { return capacity.NewDailyQuota(capacity.Dir()) }

// readDailyQuota resolves Inputs.DailyQuota for one pass (#1566). A nil counter
// (quota off) reads nothing and returns the zero state, which the gate ignores.
// A real pass reads under the store lock (Used); a dry run takes no lock and
// creates nothing (Peek). A failed read yields the path-free reason as
// Unreadable and logs it once per pass, so the gate blocks every dispatch.
func readDailyQuota(q *capacity.DailyQuota, now time.Time, dryRun bool, out io.Writer) DailyQuotaState {
	if q == nil {
		return DailyQuotaState{}
	}
	read := q.Used
	if dryRun {
		read = q.Peek
	}
	used, err := read(now)
	if err != nil {
		logf(out, "dispatch: daily quota state unreadable: %s\n", err)
		return DailyQuotaState{Unreadable: err.Error()}
	}
	return DailyQuotaState{Read: true, Used: used}
}

// readDailyQuotaFn is the seam over readDailyQuota, so a test can fail the read
// that follows a successful pending-launch flush.
var readDailyQuotaFn = readDailyQuota

// pendingLaunches counts launches (#1572) whose capacity.DailyQuota.Record
// failed after a successful spawn, tagged with their local day. It lives in
// memory only: the failure it covers is an unwritable store, so it cannot be
// persisted, and a process exit drops it. One process resolves one capacity
// directory, so one package-level value serves every pass. The mutex is held
// across the flush, which may wait for the store's lock timeout; passes run one
// at a time in both loops.
var pendingLaunches struct {
	mu  sync.Mutex
	day string
	n   int
}

// localDay is now's local calendar date. It applies the same rule as the
// unexported capacity.day (now.In(time.Local), "2006-01-02"), so a pending
// launch's day compares equal to the day the store counts it on; keep the two
// in step.
func localDay(now time.Time) string { return now.In(time.Local).Format("2006-01-02") }

// addPendingLaunch counts one unrecorded launch on now's local day. A pending
// count from another day is dropped first: the gate only counts today.
func addPendingLaunch(now time.Time) {
	pendingLaunches.mu.Lock()
	defer pendingLaunches.mu.Unlock()
	today := localDay(now)
	if pendingLaunches.day != today {
		pendingLaunches.day, pendingLaunches.n = today, 0
	}
	pendingLaunches.n++
}

// pendingLaunchState returns the pending launch day and count (tests).
func pendingLaunchState() (day string, n int) {
	pendingLaunches.mu.Lock()
	defer pendingLaunches.mu.Unlock()
	return pendingLaunches.day, pendingLaunches.n
}

// resetPendingLaunches clears the pending launches (tests).
func resetPendingLaunches() {
	pendingLaunches.mu.Lock()
	defer pendingLaunches.mu.Unlock()
	pendingLaunches.day, pendingLaunches.n = "", 0
}

// settlePendingLaunches records the launches whose Record failed earlier
// (#1572), before the pass reads the count. It returns "" when nothing blocks
// the pass, or the path-free reason the pass must fail closed on:
//   - nothing pending, or pending launches from a day other than today's: ""
//     (a real pass discards the latter, because the gate only counts today; a
//     dry run changes nothing);
//   - a dry run with launches pending for today: reasonDailyQuotaRecordPending,
//     because a dry run never writes;
//   - otherwise all of them are recorded in one RecordN write: "" on success
//     (the count clears), the write error's text on failure (the count stays).
func settlePendingLaunches(q *capacity.DailyQuota, now time.Time, dryRun bool, out io.Writer) string {
	pendingLaunches.mu.Lock()
	defer pendingLaunches.mu.Unlock()
	if pendingLaunches.n == 0 {
		return ""
	}
	if pendingLaunches.day != localDay(now) {
		if !dryRun {
			pendingLaunches.day, pendingLaunches.n = "", 0
		}
		return ""
	}
	if dryRun {
		logf(out, "dispatch: daily quota: %d unrecorded launch(es) pending (dry run, not recorded)\n", pendingLaunches.n)
		return reasonDailyQuotaRecordPending
	}
	if err := q.RecordN(now, pendingLaunches.n); err != nil {
		logf(out, "dispatch: daily quota: %d unrecorded launch(es) pending: %s\n", pendingLaunches.n, err)
		return err.Error()
	}
	pendingLaunches.day, pendingLaunches.n = "", 0
	return ""
}

// resolveDailyQuota resolves Inputs.DailyQuota for one pass: it settles pending
// launches first and fails the pass closed when that does not clear them, and
// otherwise reads the count. With the quota off (nil counter) it returns the
// zero state and never touches the pending launches.
func resolveDailyQuota(q *capacity.DailyQuota, now time.Time, dryRun bool, out io.Writer) DailyQuotaState {
	if q == nil {
		return DailyQuotaState{}
	}
	if hold := settlePendingLaunches(q, now, dryRun, out); hold != "" {
		return DailyQuotaState{Unreadable: hold}
	}
	return readDailyQuotaFn(q, now, dryRun, out)
}

// replanContextToken (#828) is the escape-hatch token appended to the run
// Ticket argument for an autonomous re-plan dispatch ("<number> replan"), so
// the dispatched implement session's --replan-requested branch
// (flow/skills/implement/SKILL.md:116) skips Phase 1's human-confirmation
// gate for the stale-plan branch -- an unattended session that landed on that
// gate would hang as a Working ticket until the reconciler reaped it.
// Literal shared across projects (#357, watch/AGENTS.md): grep both ends
// before ever renaming it.
const replanContextToken = "replan"

// answerCommentContextToken (#910) is the prefix of the single token appended
// to a resume launch's Ticket argument (".plans/<file> answer-comment=<id>"),
// naming the one escalation-answer comment the dispatch probe found
// authorized. The dispatched implement session's plan-file mode recognizes it
// (flow/skills/implement/SKILL.md) and reads only that comment, after
// re-checking it itself -- the ID is a pointer, never authorization.
// Literal shared across projects (#357, watch/AGENTS.md): grep both ends
// before ever renaming it; resume_crosslane_test.go pins the SKILL.md side.
const answerCommentContextToken = "answer-comment="

// dispatchTicketArg builds the run.Opts.Ticket argument for d, per kind
// (#828): the ordinary kind launches the matched plan file exactly as
// before; a resume (#827) launches the same plan file and, when it carries an
// answering comment ID (#910), appends " answer-comment=<id>" (only for
// d.Resume && d.AnswerCommentID > 0 -- every other kind stays byte-identical);
// a planning-pickup dispatch (Plan == nil) launches the bare ticket number so
// the implement session's Phase 1 discovers there is no plan yet; an
// autonomous re-plan (Planning && Replan, Plan != nil) appends
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
	arg := filepath.Join(".plans", filepath.Base(d.Plan.Path))
	if d.Resume && d.AnswerCommentID > 0 {
		arg += " " + answerCommentContextToken + strconv.FormatInt(d.AnswerCommentID, 10)
	}
	return arg
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

	// AnswerCommentIDs maps planKey(repo, number) -> the answering comment's
	// ID for every ticket whose probe is AnswerProbeAnswered (#910), resolved
	// by resolveAnswerProbes next to Answers and threaded straight through to
	// Inputs.AnswerCommentIDs.
	AnswerCommentIDs map[string]int64

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

	// PlanInventories maps repo -> the resolved PlanInventory for that
	// repo's `.plans` directory read this pass (#884), populated by
	// readPlansForRepos alongside PlanProbes above. Threaded straight
	// through to Inputs.PlanInventories so Decide can hold every ticket in
	// a repo whose `.plans` directory could not be fully enumerated (Q5),
	// without doing any I/O itself.
	PlanInventories map[string]PlanInventory

	// OrphanedContainerSessions and ContainerSessionProbe (#1218) are the
	// once-per-pass orphan count and its trust status, resolved by
	// resolveContainerSessions before applyDispatch/Decide ever run --
	// threaded straight through to the same-named Inputs fields.
	OrphanedContainerSessions int
	ContainerSessionProbe     ContainerSessionProbe

	// RepoMemory (#1290) is the once-per-pass per-repo memory reading resolved
	// by resolveMemoryHeadroom before applyDispatch/Decide ever run --
	// threaded straight through to Inputs.RepoMemory.
	RepoMemory map[string]RepoMemory

	// OOMBackoffSince (#1291) is the persisted OOM-retry time per ticket,
	// keyed by planKey and loaded once per pass by RunOnce through
	// oomBackoffLoadFn -- threaded straight through to Inputs.OOMBackoffSince.
	OOMBackoffSince map[string]time.Time

	// PlanningTrust (#847) is the once-per-pass planning trust verdict per
	// candidate, keyed by planKey and resolved by resolvePlanningTrust before
	// applyDispatch/Decide ever run -- threaded straight through to
	// Inputs.PlanningTrust.
	PlanningTrust map[string]PlanningTrustProbe
}

// RunOnce gathers inputs (tickets, plans, snapshot, clock), runs the pure
// engine, logs every decision, then dispatches each ActionDispatch via run.Run
// (unless dryRun). The daily quota (#1566), when Config.DailyQuota is set, is
// read from and recorded to the persisted capacity store by applyDispatch. It
// returns the full decision table alongside the first collection error
// encountered (CollectTickets, then per-repo ReadPlans), if any -- all existing
// logging is unchanged, this only additionally surfaces the error to the caller
// instead of swallowing it.
func RunOnce(cfg Config, ctrl run.Controller, mut TicketMutator, dryRun bool, out io.Writer) ([]Decision, error) {
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

	// Repo-autonomy probe (#851, #877): reads each repo's committed
	// `planning.autonomy` at its own resolved AutonomyRef (from syncs above)
	// -- the object ID of the remote's main tip, confirmed against `git
	// ls-remote origin refs/heads/main` this pass (#903), never local HEAD,
	// FreshRef or a possibly stale refs/remotes/origin/main -- so a Refined
	// planning pickup or autonomous re-plan can no longer be authorized by
	// dispatch.planRefined alone.
	autonomies := probeRepoAutonomies(cfg.Repos, syncs, out)

	// Attended narrowing (#1086), immediately after the probe above and
	// before anything downstream ever reads the map: maps every
	// RepoAutonomyLean entry to RepoAutonomyAttended when the fleet
	// planning.attended switch is on. Runs unconditionally over the whole
	// map -- not scoped to repos with tickets this pass -- so the logged
	// narrowed count reflects every enrolled repo, matching
	// probeRepoAutonomies' own per-repo, per-pass scope. The probe's own log
	// line above already reported each repo's true committed verdict, so
	// this step's log line is purely additive discoverability, never a
	// replacement for it.
	narrowAutonomiesAttended(autonomies, cfg, out)

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

	plans, planProbes, planInventories, err := readPlansForRepos(cfg.Repos, syncs, out)
	if err != nil && collectErr == nil {
		collectErr = err
	}

	snap, _ := ReadSnapshot(watch.DefaultSocketPath()) // nil on error ⇒ Decide skips safely

	// Orphaned container sessions (#1218) count against the concurrency cap
	// alongside the daemon's Running count, probed at most once per pass and
	// only when the pass collected a ticket. A probe failure is logged and
	// fails closed at the capacity gate; it never feeds collectErr, matching
	// the answers probe below.
	orphanedSessions, sessionProbe := resolveContainerSessions(tickets, out)

	// Per-repo container memory (#1290) feeds the memory-headroom gate right
	// after the container-session gate. Same shape: probed at most once per
	// pass, only for repos that collected a ticket, a failure is logged and
	// fails that repo closed, and it never feeds collectErr.
	repoMemory := resolveMemoryHeadroom(cfg.Repos, tickets, out)

	// OOM backoff (#1291): the reconciler's persisted OOM-retry times feed
	// Decide's rule 6.5. Read-only, loaded at most once per pass, and only when
	// the gate can fire (a ticket exists and dispatch.oomBackoff > 0). An
	// unreadable state file is logged once and leaves the map nil, so no ticket
	// is skipped by this gate that pass; it never feeds collectErr.
	var oomBackoffSince map[string]time.Time
	if len(tickets) > 0 && cfg.OOMBackoff > 0 {
		var loadErr error
		oomBackoffSince, loadErr = oomBackoffLoadFn()
		if loadErr != nil {
			logf(out, "dispatch: loading OOM backoff state: %v\n", loadErr)
			oomBackoffSince = nil
		}
	}

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
	answers, answerCommentIDs := resolveAnswerProbes(tickets, indexPlans(plans), out)

	// Planning trust probe (#847): for every unattended planning pickup or
	// re-plan candidate in a lean repo (after the attended narrowing above),
	// read the comment thread and check the author's and every commenter's
	// current write permission. Same shape as the answers probe: read-only,
	// also run in a dry run, its own per-pass permission cache, and a failure
	// only logs and fails that candidate closed -- it never feeds collectErr.
	// Zero gh calls with planRefined off or no candidate.
	planningTrust := resolvePlanningTrust(Inputs{
		Tickets:         tickets,
		Plans:           plans,
		CurrentUser:     currentUser,
		Config:          cfg,
		RepoAutonomy:    autonomies,
		PlanProbes:      planProbes,
		PlanInventories: planInventories,
	}, out)

	// #927: a leftover top-level dispatch.session is read-only detection --
	// it never gates a pass -- so this logs at most once per pass and never
	// feeds collectErr/applyErr.
	logLegacyTopLevelSessionDeprecation(cfg, out)

	decisions, applyErr := applyDispatch(cfg, dispatchDeps{
		Tickets:                   tickets,
		Plans:                     plans,
		Snapshot:                  snap,
		Now:                       time.Now(),
		CurrentUser:               currentUser,
		Answers:                   answers,
		AnswerCommentIDs:          answerCommentIDs,
		RepoAutonomy:              autonomies,
		PlanProbes:                planProbes,
		PlanInventories:           planInventories,
		OrphanedContainerSessions: orphanedSessions,
		ContainerSessionProbe:     sessionProbe,
		RepoMemory:                repoMemory,
		OOMBackoffSince:           oomBackoffSince,
		PlanningTrust:             planningTrust,
	}, ctrl, mut, dryRun, out)

	// #927, #1464: the per-repo session sentinel(s) must outrank a same-pass
	// collectErr (a likely co-occurrence -- both can be driven by the same
	// underlying outage), mirroring RunReconcileOnce's documented
	// ErrReconcileStateUnreadable special-case (watch/docs/dispatch-
	// reconcile.md). errors.Join (rather than returning applyErr alone)
	// keeps collectErr's own diagnostic text reachable too, while
	// errors.Is(..., ErrSessionUnconfigured/ErrSessionMissing/
	// ErrSessionProbeFailed) still finds the sentinel through the join.
	if isSessionSentinel(applyErr) {
		return decisions, errors.Join(applyErr, collectErr)
	}
	return decisions, firstNonNil(collectErr, applyErr)
}

// narrowAutonomiesAttended applies the fleet-wide planning.attended
// narrowing step (#1086) to autonomies in place: when cfg.PlanningAttended
// is on, every RepoAutonomyLean entry becomes RepoAutonomyAttended -- a
// deny, never a grant, and never a mask of any other denial class -- so a
// fresh Refined planning pickup or autonomous re-plan is suppressed for
// lean repos while a human is at this machine's keyboard. Runs over every
// entry already in the map (whatever probeRepoAutonomies populated this
// pass, over every enrolled repo, not scoped to repos with tickets this
// pass), never adding or removing repos from it. No-ops entirely, with zero
// log output, when cfg.PlanningAttended is off -- the byte-identical-when-
// off AC. Both log lines are prefixed "dispatch: " at column 0 (neither
// contains the lazyboards-reserved " skip:"/" dispatch " substrings, per
// formatDecision's doc comment).
func narrowAutonomiesAttended(autonomies map[string]RepoAutonomy, cfg Config, out io.Writer) {
	if !cfg.PlanningAttended {
		return
	}
	narrowed := 0
	for repo, a := range autonomies {
		if a == RepoAutonomyLean {
			autonomies[repo] = RepoAutonomyAttended
			narrowed++
		}
	}
	if narrowed > 0 {
		logf(out, "dispatch: planning attended mode on (planning.attended); %d lean repo(s) narrowed\n", narrowed)
	}
	if cfg.PlanningAttendedUnparseable {
		logf(out, "dispatch: planning.attended could not be parsed as a bool, folding to attended (restrictive)\n")
	}
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
//
// The returned map[string]PlanInventory (#884) is populated for EVERY
// configured repo, every pass -- including a repo whose directory read
// failed: unlike the pre-#884 behavior (which `continue`d past a ReadPlans
// error, leaving that repo silently absent from every returned map and thus
// indistinguishable from verified absence), a failed read now records its
// PlanInventoryUnreadable/PlanInventoryPartial classification here so the
// repo-wide hold (Q5) actually reaches Decide via Inputs.PlanInventories.
func readPlansForRepos(repos []RepoConfig, syncs map[string]mainSyncResult, out io.Writer) ([]Plan, map[string]PlanProbe, map[string]PlanInventory, error) {
	var plans []Plan
	planProbes := map[string]PlanProbe{}
	planInventories := map[string]PlanInventory{}
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
		scan, err := ReadPlans(rc.Repo, rc.Dir, commitsBehind, out)
		planInventories[rc.Repo] = scan.Inventory
		if err != nil {
			logf(out, "dispatch: reading plans in %s: %v\n", rc.Dir, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		plans = append(plans, scan.Plans...)
		for k, v := range scan.Probes {
			planProbes[k] = v
		}
	}
	return plans, planProbes, planInventories, firstErr
}

// applyDispatch runs the pure engine over already-collected deps, logs, spawns
// each dispatch (unless dryRun), and synchronously claims each spawned ticket
// with the Working label so no later pass can re-pick it during the
// spawn→self-label window. A failed claim is logged and the pass continues —
// the spawn already happened, and the reconciler recovers label drift. It is
// the testable core of RunOnce.
//
// Every decision that would spawn is admitted by the shared capacity ledger
// (#1469) between Decide and the decision log: a refusal becomes a skip, a launch
// that fails before any window exists gives its reservation back, and one that
// may have created a window keeps it (capacity.go). A dry run admits nothing.
//
// With Config.DailyQuota set (#1566), the persisted count is read once before
// Decide (Used on a real pass, Peek on a dry run) and one launch is recorded
// after each successful spawn, whether or not the Working claim then succeeds.
// A failed spawn and a dry run record nothing, and a failed record is logged,
// not returned (RunLoop treats a returned error as fatal).
//
// A failed record (#1572) is kept in an in-process pending count tagged with
// its local day. Every real pass with the quota set records today's pending
// launches in one write before the read, and fails closed if that write fails:
// each candidate is skipped with the unreadable reason and nothing is spawned,
// until the store is writable again. A dry run never writes, so pending
// launches for today make it skip with a fixed reason. Pending launches from an
// earlier local day are discarded, since the gate only counts today. The count
// lives in memory only: a process exit (a daemon restart, a one-shot pass)
// drops it, which is the one residual undercount. With the quota off the store
// is never opened and the pending count is left alone.
func applyDispatch(cfg Config, deps dispatchDeps, ctrl run.Controller, mut TicketMutator, dryRun bool, out io.Writer) ([]Decision, error) {
	if out == nil {
		out = os.Stdout
	}

	dirByRepo := make(map[string]string, len(cfg.Repos))
	for _, rc := range cfg.Repos {
		dirByRepo[rc.Repo] = rc.Dir
	}
	sessionByRepo := buildSessionByRepo(cfg.Repos)

	// Daily quota (#1566): the seam is called, and the store touched, only when a
	// quota is set. The read happens here, before Decide, so the gate stays pure.
	var quota *capacity.DailyQuota
	if cfg.DailyQuota != nil {
		quota = dailyQuotaFn()
	}
	quotaState := resolveDailyQuota(quota, deps.Now, dryRun, out)

	// Per-repo session eligibility (#1464), resolved before Decide so the gate
	// sits ahead of the cap check and an ineligible repo never takes a slot. A
	// dry run probes nothing (configured sessions get the not-probed note
	// below) but still reports an unconfigured session from config alone.
	//
	// The socket is resolved once per real pass (#1431) and the same string
	// goes to the HasSession probe and to run.Opts.TmuxSocket below, so the gate
	// and the spawn can never name different servers. A dry run resolves none.
	var socket string
	var socketErr error
	if !dryRun {
		socket, socketErr = resolveLaunchSocket()
	}
	eligibility := resolveSessionEligibility(cfg.Repos, deps.Tickets, ctrl, socket, socketErr, dryRun, cfg.Path, out)

	decisions := Decide(Inputs{
		Tickets:                   deps.Tickets,
		Plans:                     deps.Plans,
		Snapshot:                  deps.Snapshot,
		Budgets:                   buildBudgetProvider(cfg, deps.Now),
		Now:                       deps.Now,
		DailyQuota:                quotaState,
		CurrentUser:               deps.CurrentUser,
		Config:                    cfg,
		Answers:                   deps.Answers,
		AnswerCommentIDs:          deps.AnswerCommentIDs,
		RepoAutonomy:              deps.RepoAutonomy,
		PlanProbes:                deps.PlanProbes,
		PlanInventories:           deps.PlanInventories,
		OrphanedContainerSessions: deps.OrphanedContainerSessions,
		ContainerSessionProbe:     deps.ContainerSessionProbe,
		RepoMemory:                deps.RepoMemory,
		OOMBackoffSince:           deps.OOMBackoffSince,
		SessionEligibility:        eligibility,
		PlanningTrust:             deps.PlanningTrust,
	})

	// Admission (#1469): each decision that would spawn reserves a slot in the
	// shared capacity ledger BEFORE the decision table is logged, so a refusal is
	// rewritten to a skip and every ticket still gets exactly one decision line. A
	// dry run admits nothing and writes nothing: it logs Decide's decisions as
	// they are, and the ledger is never created by it.
	var adm *admitter
	var permits map[int]*capacity.Permit
	if dryRun {
		for i := range decisions {
			if decisions[i].Action == ActionDispatch && sessionByRepo[decisions[i].Ticket.Repo] != "" {
				decisions[i].Note = joinNotes(decisions[i].Note, noteSessionNotProbed)
			}
		}
	} else {
		adm = newAdmitter(cfg, deps, socket, dirByRepo, out)
		// First, retry the settle of any reservation an earlier pass of this process
		// left reserved (#1578), so the slot it holds is free for this pass's Admit.
		settleOwnReservations(adm.ledger, out)
		permits = adm.admitDecisions(decisions, eligibility)
	}

	// Surface the pinned model in the same log stream as the decision table so
	// it's never silent: a dispatch pass with no Model set falls back to
	// agents.*.model (or, absent that, whatever ambient default the agent CLI
	// itself resolves), which is precisely the drift this field exists to make
	// visible and overridable.
	if cfg.Model != "" {
		logf(out, "dispatch: model override %q\n", cfg.Model)
	}

	for _, d := range decisions {
		logf(out, "%s\n", formatDecision(d, sessionByRepo))
	}

	// A dry run returns no session sentinel (#1464): it probed nothing, and its
	// exit code must not depend on tmux.
	if dryRun {
		return decisions, nil
	}

	// The session skips above are per-repo misconfigurations, surfaced as
	// sentinels the caller can match (RunOnce, isLoopContinuable, passError).
	sessionErr := sessionSkipSentinels(decisions)

	var firstErr error
	for i := range decisions {
		d := decisions[i]
		// A planning-pickup dispatch (#828) carries Plan == nil by design (no
		// plan file exists yet) -- admit it alongside the ordinary/resume/
		// re-plan kinds, all of which carry a non-nil Plan.
		if d.Action != ActionDispatch || (d.Plan == nil && !d.Planning) {
			continue
		}
		// Spawn only into a session this pass proved live (#1464): Decide's map
		// miss is permissive, so a dispatch whose repo has no explicit
		// SessionEligible verdict is logged and left untouched -- no clear, no
		// label edit, no spawn -- rather than launched unverified.
		if eligibility[d.Ticket.Repo] != SessionEligible {
			logf(out, "dispatch: #%d not spawned: no session verdict for %s this pass\n", d.Ticket.Number, d.Ticket.Repo)
			continue
		}

		// Clear the ticket's recorded launch (#1292) BEFORE the resume claim
		// below and before the spawn: the new session starts on the window-name
		// liveness rule until its own `working` claim records its launch, so a
		// dead launch recorded before a crash can never make it read dead. A
		// failed clear (an unreadable state file) skips this ticket entirely --
		// no spawn, no label edit -- so a stale launch is never left behind a new
		// session. It is logged, not returned: one ticket's broken state file must
		// not fail the whole pass (RunLoop treats a returned error as fatal). An
		// empty repo dir is never touched (pipeline would fall back to the
		// process cwd); the collector reads no launch for it either.
		if dir := dirByRepo[d.Ticket.Repo]; dir != "" {
			if err := clearLaunchFn(pipeline.ArtifactOpts{ID: strconv.Itoa(d.Ticket.Number), RepoRoot: dir}); err != nil {
				logf(out, "dispatch: #%d clearing recorded launch failed: %v\n", d.Ticket.Number, err)
				adm.release(permits[i], d.Ticket.Number)
				continue
			}
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
				adm.release(permits[i], d.Ticket.Number)
				continue
			}
		}

		// The controller runFn gets records whether a tmux window creation was
		// attempted (#1469): that is how the reservation tells a launch that
		// definitely failed before any window existed (released) from one that may
		// have one (confirmed to the deterministic window name).
		spawnCtrl := ctrl
		var tracker *spawnTracker
		if ctrl != nil {
			tracker = &spawnTracker{Controller: ctrl}
			spawnCtrl = tracker
		}
		err := runFn(run.Opts{
			Workflow:     "implement",
			Ticket:       dispatchTicketArg(d),
			WindowTicket: strconv.Itoa(d.Ticket.Number),
			Agent:        d.Agent,
			Model:        cfg.Model,
			// Session (#927): resolved from this decision's own repo's
			// trimmed repos[].session -- the same string
			// resolveSessionEligibility proved live this pass. The dispatch
			// spawn path never consults ctrl.CurrentSession(); config is the
			// only source.
			Session:    sessionByRepo[d.Ticket.Repo],
			TmuxSocket: socket,
			Dir:        dirByRepo[d.Ticket.Repo],
			// Unattended (#1087): every dispatched session pins
			// CENCI_ATTENDED=0 in its own window environment. #1086's fleet
			// gate already refuses to start a planning session while
			// planning.attended is on; this is the defense-in-depth layer for
			// a flag toggled mid-flight, since a detached tmux window has
			// nobody to answer an interactive question and would sit there
			// with the ticket stuck on Working.
			Unattended: true,
		}, spawnCtrl)
		// Settle the reservation: only a launch that never reached NewWindow is
		// given back. A spawn that succeeded, failed after the window was created
		// (exit 3, run.ErrWindowSpawned) or failed in NewWindow itself holds the
		// slot until a complete inventory proves no worker.
		if err != nil && !errors.Is(err, run.ErrWindowSpawned) && (tracker == nil || !tracker.newWindowCalled) {
			adm.release(permits[i], d.Ticket.Number)
		} else {
			adm.confirm(permits[i], d)
		}
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
		decisions[i].Spawned = true
		// One launch per successful spawn (#1566), before the claim below: a spawn
		// whose Working claim then fails still happened and still counts. Never on
		// a dry run (returned above) or a failed spawn (continued above). A failed
		// record is logged, not returned -- RunLoop treats a returned error as
		// fatal. A corrupt, unknown-schema or symlinked file fails closed on the
		// next pass's read. A failed record is kept as a pending launch (#1572): the
		// next real pass records it in one write before reading the count and fails
		// closed while that write fails, so the limit stays enforced. Only a process
		// exit before then loses it.
		if quota != nil {
			if err := quota.Record(deps.Now); err != nil {
				logf(out, "dispatch: #%d recording daily quota launch failed: %v\n", d.Ticket.Number, err)
				addPendingLaunch(deps.Now)
			}
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
	// #927: the session sentinel(s) must remain discoverable via errors.Is
	// (ErrSessionUnconfigured/ErrSessionMissing/ErrSessionProbeFailed) even
	// when a live-session repo in the same pass hit an unrelated spawn/claim
	// failure -- but that real per-ticket failure must never be silently
	// discarded just because a sentinel also fired for a different repo
	// (previously firstNonNil picked only one of the two, stranding a
	// ticket at Working with a dead spawn on a correctly-configured repo
	// while the operator only ever saw "session misconfigured"). errors.Join
	// tolerates either or both being nil and preserves errors.Is matching
	// for whichever inputs are non-nil.
	return decisions, errors.Join(sessionErr, firstErr)
}

// errConfigReload marks a dispatchTick whose config reload failed (#1385). The
// failure was already logged by reloadConfig and the pass was skipped, so
// RunLoop treats it as "try again next tick" rather than as fatal.
var errConfigReload = errors.New("loading dispatch config")

// RunLoop reloads Config from configPath and runs RunOnce immediately and then
// on every interval tick. The daily quota (#1566), when set, lives in the
// persisted capacity store, so it carries across ticks and restarts. A launch
// whose recording failed (#1572) carries across ticks in memory only: a restart
// loses it.
// modelOverride, when non-empty (the --model CLI flag), is re-applied to Config
// every tick, since each tick reloads Config fresh from disk and would
// otherwise drop it. It blocks until the process exits.
func RunLoop(configPath string, ctrl run.Controller, mut TicketMutator, interval time.Duration, out io.Writer, modelOverride string) error {
	// #927: a session sentinel is a per-repo misconfiguration, not a
	// loop-fatal condition -- the pass that produced it already logged the
	// affected repo(s) via resolveSessionEligibility, so RunLoop just keeps ticking
	// instead of terminating the whole fleet loop over one repo's bad
	// config. #1385: a failed config reload (a bad hand edit of a filter, say)
	// is the same kind of non-fatal condition -- the tick was skipped and the
	// error logged, and the corrected file dispatches again on the next tick.
	// The initial LoadConfig in runDispatch still exits 1, and every other
	// error stays fatal exactly as today.
	if err := dispatchTick(configPath, ctrl, mut, out, modelOverride); err != nil && !isLoopContinuable(err) {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if err := dispatchTick(configPath, ctrl, mut, out, modelOverride); err != nil && !isLoopContinuable(err) {
			return err
		}
	}
	return nil
}

// isLoopContinuable reports whether err leaves RunLoop running: a per-repo
// session sentinel (#927, #1464) or a skipped config reload (#1385).
func isLoopContinuable(err error) bool {
	return isSessionSentinel(err) || errors.Is(err, errConfigReload)
}

// dispatchTick is RunLoop's per-tick body: reload Config from configPath,
// re-apply modelOverride (if set) since the reload otherwise wins, then run
// one dispatch pass against the resulting config. On a reload error the tick
// is skipped entirely (no dispatch, no quota read or record) and errConfigReload
// is returned, so a bad edit between ticks cannot crash the loop or silently
// dispatch against a stale config (or against unrestricted defaults).
func dispatchTick(configPath string, ctrl run.Controller, mut TicketMutator, out io.Writer, modelOverride string) error {
	cfg, ok := reloadConfig(configPath, out)
	if !ok {
		return errConfigReload
	}
	if modelOverride != "" {
		cfg.Model = modelOverride
	}
	_, err := RunOnce(cfg, ctrl, mut, false, out)
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
// the resolved agent, plan file, and (#927) the repo's configured session
// (or "(unset)") so the table is self-explanatory. The ` skip:` / ` dispatch `
// substrings are load-bearing: downstream consumers (lazyboards) classify
// lines by matching on them. Skip lines are unchanged -- they never render a
// session. A Decision.Note (#1464) follows the reason after "; ".
func formatDecision(d Decision, sessionByRepo map[string]string) string {
	reason := joinNotes(d.Reason, d.Note)
	if d.Action == ActionDispatch && d.Plan != nil {
		return fmt.Sprintf("%s#%d dispatch (%s, %s, session %s): %s",
			d.Ticket.Repo, d.Ticket.Number, d.Agent, filepath.Base(d.Plan.Path), sessionLabel(sessionByRepo[d.Ticket.Repo]), reason)
	}
	// A planning-pickup dispatch (#828) carries Plan == nil by design -- no
	// plan file exists yet -- so it needs its own branch rather than falling
	// through to the skip: rendering below, which would silently break the
	// lazyboards " dispatch " substring contract for this dispatch kind.
	if d.Action == ActionDispatch && d.Planning {
		return fmt.Sprintf("%s#%d dispatch (%s, session %s): %s",
			d.Ticket.Repo, d.Ticket.Number, d.Agent, sessionLabel(sessionByRepo[d.Ticket.Repo]), reason)
	}
	return fmt.Sprintf("%s#%d skip: %s", d.Ticket.Repo, d.Ticket.Number, reason)
}

// noteSessionNotProbed is applyDispatch's dry-run Decision.Note (#1464) for a
// dispatch into a configured session: a dry run never calls HasSession, so the
// preview cannot say whether the session exists.
const noteSessionNotProbed = "session not probed (dry-run)"

// joinNotes appends note to s after "; ", or returns s alone for an empty note.
func joinNotes(s, note string) string {
	switch {
	case note == "":
		return s
	case s == "":
		return note
	default:
		return s + "; " + note
	}
}

// sessionLabel renders a repo's configured session for formatDecision: the
// literal "(unset)" for an empty/whitespace-only session (the exact token
// the #927 AC's dry-run test asserts), else the %q-quoted session name --
// matching session.go's resolveSessionEligibility log-line convention -- so a session name
// containing a newline or the literal " skip: "/" dispatch " substrings can
// never forge or break the parsed decision-line contract lazyboards depends
// on.
func sessionLabel(session string) string {
	if strings.TrimSpace(session) == "" {
		return "(unset)"
	}
	return fmt.Sprintf("%q", session)
}
