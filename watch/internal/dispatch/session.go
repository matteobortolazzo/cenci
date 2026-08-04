package dispatch

import (
	"errors"
	"io"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/run"
)

// ErrSessionUnconfigured is the sentinel every repo with >=1 ActionDispatch
// decision this pass and an empty/whitespace-only repos[].session wraps
// (#927). Plain errors.New, no internal/errcode registration -- mirrors
// ErrReconcileStateUnreadable (state.go).
var ErrSessionUnconfigured = errors.New("dispatch: session unconfigured")

// ErrSessionMissing is the sentinel every repo with >=1 ActionDispatch
// decision this pass and a non-empty repos[].session that HasSession reports
// absent (or errors probing) wraps (#927).
var ErrSessionMissing = errors.New("dispatch: session missing")

// buildSessionByRepo maps repo -> its configured (possibly empty) session,
// mirroring dirByRepo's exact shape (dispatch.go) and keyed the same way, off
// rc.Repo. The value is trimmed once here (defense-in-depth) so the gate's
// classification (gateSessions, also trimming) and the actual value spawned
// into run.Opts.Session are guaranteed consistent, rather than relying solely
// on run.Run's own downstream trim.
func buildSessionByRepo(repos []RepoConfig) map[string]string {
	sessionByRepo := make(map[string]string, len(repos))
	for _, rc := range repos {
		sessionByRepo[rc.Repo] = strings.TrimSpace(rc.Session)
	}
	return sessionByRepo
}

// gateSessions is the #927 per-repo pre-mutation skip gate: for every repo
// that owns >=1 ActionDispatch decision this pass (the AC-literal predicate,
// not the spawn loop's extra defensive Plan==nil/!Planning filter), it
// classifies the repo's configured session as unconfigured (empty/
// whitespace) or missing (non-empty, but HasSession reports absent or
// errors), probing HasSession at most once per repo. It logs one line per
// affected repo (never per ticket), and returns the set of repo names to
// skip alongside a joined sentinel error (nil when nothing was gated).
//
// The gated-repo list is derived from the ordered decisions slice with a
// seen-set, never by ranging a map, so the log-line order stays deterministic
// across runs.
func gateSessions(decisions []Decision, sessionByRepo map[string]string, ctrl run.Controller, configPath string, out io.Writer) (map[string]bool, error) {
	seen := make(map[string]bool)
	var repos []string
	for _, d := range decisions {
		if d.Action != ActionDispatch {
			continue
		}
		if seen[d.Ticket.Repo] {
			continue
		}
		seen[d.Ticket.Repo] = true
		repos = append(repos, d.Ticket.Repo)
	}

	skip := make(map[string]bool)
	var errs []error
	for _, repo := range repos {
		session := strings.TrimSpace(sessionByRepo[repo])
		if session == "" {
			skip[repo] = true
			errs = append(errs, ErrSessionUnconfigured)
			logf(out, "dispatch: no target tmux session for %s; set repos[].session in %s\n", repo, configPath)
			continue
		}

		live, err := ctrl.HasSession(session)
		if err != nil {
			skip[repo] = true
			errs = append(errs, ErrSessionMissing)
			logf(out, "dispatch: configured session %q for %s not found in tmux: %v; create it (`tmux new-session -d -s %q`)\n", session, repo, err, session)
			continue
		}
		if !live {
			skip[repo] = true
			errs = append(errs, ErrSessionMissing)
			logf(out, "dispatch: configured session %q for %s not found in tmux; create it (`tmux new-session -d -s %q`)\n", session, repo, session)
		}
	}

	if len(errs) == 0 {
		return skip, nil
	}
	return skip, errors.Join(errs...)
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

// isSessionSentinel reports whether err is (or wraps) either #927 session
// sentinel -- the check RunOnce/RunLoop use to treat a session-gate skip
// distinctly from every other RunOnce error.
func isSessionSentinel(err error) bool {
	return errors.Is(err, ErrSessionUnconfigured) || errors.Is(err, ErrSessionMissing)
}
