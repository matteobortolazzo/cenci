package dispatch

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
)

// ErrSessionUnconfigured is the sentinel a real pass returns for every repo
// whose ticket was skipped with `session unconfigured`: an empty or
// whitespace-only repos[].session (#927, #1464). Plain errors.New, no
// internal/errcode registration -- mirrors ErrReconcileStateUnreadable
// (state.go).
var ErrSessionUnconfigured = errors.New("dispatch: session unconfigured")

// ErrSessionMissing is the sentinel a real pass returns for every repo whose
// ticket was skipped with `session absent`: a non-empty repos[].session that
// HasSession reported absent (#927, #1464).
var ErrSessionMissing = errors.New("dispatch: session missing")

// ErrSessionProbeFailed is the sentinel a real pass returns for every repo
// whose ticket was skipped with `session probe failed`: HasSession returned an
// error, so the session's existence is unknown (#1464). Kept distinct from
// ErrSessionMissing so "absent" and "could not check" never merge.
var ErrSessionProbeFailed = errors.New("dispatch: session probe failed")

// buildSessionByRepo maps repo -> its configured (possibly empty) session,
// mirroring dirByRepo's exact shape (dispatch.go) and keyed the same way, off
// rc.Repo (last entry wins on a duplicated repo). The value is trimmed once
// here so the eligibility probe (resolveSessionEligibility) and the value
// spawned into run.Opts.Session are guaranteed to be the same string, rather
// than relying solely on run.Run's own downstream trim.
func buildSessionByRepo(repos []RepoConfig) map[string]string {
	sessionByRepo := make(map[string]string, len(repos))
	for _, rc := range repos {
		sessionByRepo[rc.Repo] = strings.TrimSpace(rc.Session)
	}
	return sessionByRepo
}

// sessionCandidateRepos is the set of repos holding at least one ticket that
// could become a dispatch this pass: one carrying Planned, Input Needed or
// Refined. Decide's board-state rule turns every other ticket away before any
// launch, so this is a superset of the repos with an ActionDispatch decision --
// a repo outside it never needs a probe.
func sessionCandidateRepos(tickets []Ticket) map[string]bool {
	repos := make(map[string]bool)
	for _, t := range tickets {
		if hasLabel(t.Labels, labelPlanned) || hasLabel(t.Labels, labelInputNeeded) || hasLabel(t.Labels, labelRefined) {
			repos[t.Repo] = true
		}
	}
	return repos
}

// resolveSessionEligibility is the #1464 per-repo session verdict, resolved
// once per pass before Decide so an ineligible repo's tickets are skipped ahead
// of the cap check and never take a slot (replacing #927's post-Decide
// gateSessions). For every candidate repo (sessionCandidateRepos), in config
// order:
//
//   - an empty/whitespace-only repos[].session is SessionUnconfigured;
//   - otherwise the trimmed name is probed with ctrl.HasSession on the default
//     socket -- at most once per distinct name per pass, so repos sharing a
//     session share one probe -- giving SessionProbeFailed on an error,
//     SessionAbsent on false and SessionEligible on true. ctrl.CurrentSession
//     is never consulted: config is the only source of the target.
//
// A dry run probes nothing: a configured session gets no entry (the dispatch
// line notes it as not probed instead), while SessionUnconfigured, known from
// config alone, is still reported. Each ineligible repo logs one line naming
// its remedy. A repo absent from the result is permissive in Decide (mirroring
// RepoMemory), but applyDispatch spawns only for an explicit SessionEligible.
func resolveSessionEligibility(repos []RepoConfig, tickets []Ticket, ctrl run.Controller, dryRun bool, configPath string, out io.Writer) map[string]SessionEligibility {
	candidates := sessionCandidateRepos(tickets)
	sessionByRepo := buildSessionByRepo(repos)
	type probeResult struct {
		live bool
		err  error
	}
	probes := make(map[string]probeResult)
	eligibility := make(map[string]SessionEligibility)

	for _, rc := range repos {
		if !candidates[rc.Repo] {
			continue
		}
		if _, done := eligibility[rc.Repo]; done {
			continue
		}
		session := sessionByRepo[rc.Repo]
		if session == "" {
			eligibility[rc.Repo] = SessionUnconfigured
			logf(out, "dispatch: no target tmux session for %s; set repos[].session in %s\n", rc.Repo, configPath)
			continue
		}
		if dryRun {
			continue
		}
		p, probed := probes[session]
		if !probed {
			live, err := ctrl.HasSession("", session)
			p = probeResult{live: live, err: err}
			probes[session] = p
		}
		switch {
		case p.err != nil:
			eligibility[rc.Repo] = SessionProbeFailed
			logf(out, "dispatch: probing configured session %q for %s failed: %v; check that tmux is running and reachable (`tmux has-session -t %q`)\n", session, rc.Repo, p.err, session)
		case !p.live:
			eligibility[rc.Repo] = SessionAbsent
			logf(out, "dispatch: configured session %q for %s not found in tmux; create it (`tmux new-session -d -s %q`)\n", session, rc.Repo, session)
		default:
			eligibility[rc.Repo] = SessionEligible
		}
	}
	return eligibility
}

// sessionSkipSentinels builds a real pass's session sentinels from its
// decisions: one wrapped sentinel per repo with a session-skip decision, in
// decision order, joined (nil when there is none). A dry run never calls it.
func sessionSkipSentinels(decisions []Decision) error {
	seen := make(map[string]bool)
	var errs []error
	for _, d := range decisions {
		if d.Action != ActionSkip || seen[d.Ticket.Repo] {
			continue
		}
		var sentinel error
		switch d.Reason {
		case reasonSessionUnconfigured:
			sentinel = ErrSessionUnconfigured
		case reasonSessionAbsent:
			sentinel = ErrSessionMissing
		case reasonSessionProbeFailed:
			sentinel = ErrSessionProbeFailed
		default:
			continue
		}
		seen[d.Ticket.Repo] = true
		errs = append(errs, fmt.Errorf("%s: %w", d.Ticket.Repo, sentinel))
	}
	return errors.Join(errs...)
}

// legacySessionRepos returns, in cfg.Repos order, the repo names whose
// configured session is empty -- the set the #927 legacy top-level
// dispatch.session deprecation line names when Config.LegacyTopLevelSession
// is true. Read-only: it never mutates or back-fills anything.
func legacySessionRepos(repos []RepoConfig) []string {
	var names []string
	for _, rc := range repos {
		if strings.TrimSpace(rc.Session) == "" {
			names = append(names, rc.Repo)
		}
	}
	return names
}

// logLegacyTopLevelSessionDeprecation logs the once-per-pass #927 deprecation
// line when cfg still carries a leftover top-level dispatch.session with a
// non-empty value AND >=1 enrolled repo has no session of its own -- purely
// informational, it never gates a pass (Config.LegacyTopLevelSession is
// read-only detection) and never reaches LastError.
func logLegacyTopLevelSessionDeprecation(cfg Config, out io.Writer) {
	if !cfg.LegacyTopLevelSession {
		return
	}
	names := legacySessionRepos(cfg.Repos)
	if len(names) == 0 {
		return
	}
	logf(out, "dispatch: config %s has a legacy top-level dispatch.session; it is no longer used -- set repos[].session for %s\n", cfg.Path, strings.Join(names, ", "))
}

// isSessionSentinel reports whether err is (or wraps) any session sentinel
// (#927, #1464) -- the check RunOnce, isLoopContinuable and passError use to
// treat a per-repo session skip distinctly from every other RunOnce error.
func isSessionSentinel(err error) bool {
	return errors.Is(err, ErrSessionUnconfigured) || errors.Is(err, ErrSessionMissing) || errors.Is(err, ErrSessionProbeFailed)
}
