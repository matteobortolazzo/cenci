package dispatch

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// repoConfigPath is the repo-relative path the autonomy probe reads,
// committed content only (#851 plan Q&A 3) -- never the working tree, so an
// uncommitted local edit can never grant (or revoke) lean.
const repoConfigPath = ".cenci/config.json"

// repoAutonomyProbe is probeRepoAutonomyDetailed's full result: Autonomy is
// the resolved verdict, and Detail carries the underlying git failure text
// for the RepoAutonomyUnreadable case only (bad ref, non-repo dir, timeout,
// etc.), so probeRepoAutonomies can log which failure mode produced the deny
// -- mirroring mainSyncResult.Detail's convention (mainsync.go). Detail must
// never carry config.json content: only the git error/stderr text, never the
// parsed/raw config (security constraint, #851 plan).
type repoAutonomyProbe struct {
	Autonomy RepoAutonomy
	Detail   string
}

// probeRepoAutonomy resolves dir's per-repository `planning.autonomy`
// authorization at ref (#851, #877): the repo's committed
// `.cenci/config.json`, read via `git show <ref>:.cenci/config.json` (never
// the working tree), is the authoritative grant -- fleet configuration
// (dispatch.planRefined) can only disable, never independently authorize,
// lean planning. Callers of this package's production entry point,
// probeRepoAutonomies, must always pass a remote-confirmed ref (the fully
// qualified remoteMainAuthRef, threaded through mainSyncResult.AutonomyRef)
// -- never a bare local ref that could be stale or unpushed -- so the
// authorization decision can only ever be traced to an object `git fetch
// origin` actually confirmed this pass, with no fallback to local HEAD.
//
// dir == "" is never probed (mirrors probeStage/syncMain's own dir == ""
// convention): resolving a repo root from the daemon's own cwd would risk
// reading an unrelated repo's config.
//
// `git ls-tree --full-tree --name-only <ref> -- .cenci/config.json`
// distinguishes "the path is absent from ref" (empty stdout, exit 0 --
// RepoAutonomyMissing) from "the probe itself failed to run" (a non-zero
// exit -- an unresolvable ref or a non-git directory -- RepoAutonomyUnreadable),
// since `git show` alone would report both as the same class of failure.
// --full-tree pins the pathspec to the repo root, matching the unconditional
// repo-root scope of the subsequent `git show <ref>:.cenci/config.json` read
// -- without it, a Dir that isn't the repo's exact toplevel could make the
// two disagree about which config.json is being checked/read.
//
// The autonomy match is an exact, case-sensitive string comparison against
// "lean": a missing `planning` block, a missing `autonomy` key, any other
// value, or a wrong-case match ("Lean") all resolve to the single
// RepoAutonomyInteractive default-deny, per the plan's Assumptions section.
//
// This is a thin wrapper around probeRepoAutonomyDetailed for callers that
// only need the verdict, not the failure detail.
func probeRepoAutonomy(dir, ref string) RepoAutonomy {
	return probeRepoAutonomyDetailed(dir, ref).Autonomy
}

// probeRepoAutonomyDetailed does the actual probe work; see probeRepoAutonomy
// for the full contract. Only probeRepoAutonomies calls this directly, so it
// can thread the git failure detail into its per-repo log line.
//
// The JSON decode deliberately does NOT decode straight into a Go struct
// tagged `json:"planning"`/`json:"autonomy"`: encoding/json falls back to
// case-insensitive struct-field matching, which would let a wrong-case key
// (e.g. `{"Planning":{"AUTONOMY":"lean"}}`) decode identically to the
// documented lowercase schema and silently authorize unattended planning --
// every other reader of this schema (flow's configure skill,
// phase-1-plan.md) only recognizes the literal lowercase keys. Decoding into
// map[string]json.RawMessage and looking up the literal lowercase key at
// each level keeps the key match exact, matching the value comparison
// ("lean") which was already exact and case-sensitive.
func probeRepoAutonomyDetailed(dir, ref string) repoAutonomyProbe {
	if dir == "" {
		return repoAutonomyProbe{Autonomy: RepoAutonomyMissing}
	}

	lsOut, lsErrOut, lsErr := execGitSeparate(dir, "ls-tree", "--full-tree", "--name-only", ref, "--", repoConfigPath)
	if lsErr != nil {
		return repoAutonomyProbe{
			Autonomy: RepoAutonomyUnreadable,
			Detail:   gitFailureDetail("git ls-tree failed", lsErr, lsErrOut),
		}
	}
	if lsOut == "" {
		return repoAutonomyProbe{Autonomy: RepoAutonomyMissing}
	}

	content, showErrOut, showErr := execGitSeparate(dir, "show", ref+":"+repoConfigPath)
	if showErr != nil {
		return repoAutonomyProbe{
			Autonomy: RepoAutonomyUnreadable,
			Detail:   gitFailureDetail("git show failed", showErr, showErrOut),
		}
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &top); err != nil {
		return repoAutonomyProbe{Autonomy: RepoAutonomyMalformed}
	}
	planningRaw, ok := top["planning"]
	if !ok {
		return repoAutonomyProbe{Autonomy: RepoAutonomyInteractive}
	}
	var planning map[string]json.RawMessage
	if err := json.Unmarshal(planningRaw, &planning); err != nil {
		return repoAutonomyProbe{Autonomy: RepoAutonomyMalformed}
	}
	autonomyRaw, ok := planning["autonomy"]
	if !ok {
		return repoAutonomyProbe{Autonomy: RepoAutonomyInteractive}
	}
	var autonomy string
	if err := json.Unmarshal(autonomyRaw, &autonomy); err != nil {
		// Non-string "autonomy" value: default-deny to Interactive rather than
		// a crash or a Malformed classification -- only the top-level/planning
		// structure decode failures are treated as Malformed.
		return repoAutonomyProbe{Autonomy: RepoAutonomyInteractive}
	}
	if autonomy == "lean" {
		return repoAutonomyProbe{Autonomy: RepoAutonomyLean}
	}
	return repoAutonomyProbe{Autonomy: RepoAutonomyInteractive}
}

// gitFailureDetail formats a git subprocess failure for logging: the Go exec
// error plus, when present, the collapsed (newline-safe, single-line) stderr
// text -- mirroring mainSyncResult.Detail's convention (mainsync.go). Never
// includes config.json content.
func gitFailureDetail(prefix string, err error, stderr string) string {
	detail := fmt.Sprintf("%s: %v", prefix, err)
	if stderr != "" {
		detail += " (" + strings.ReplaceAll(stderr, "\n", "; ") + ")"
	}
	return detail
}

// probeRepoAutonomies runs probeRepoAutonomy once per repo in repos, reading
// each one's own resolved AutonomyRef from syncs (#877), and unconditionally
// logs one line per repo. Mirrors syncMains' per-repo map/logging contract.
//
// Unlike the pre-#877 FreshRef convention, a repo with no confirmed
// AutonomyRef this pass -- either an explicit fetch-failure result
// (AutonomyRef == "") or a syncs map lookup miss for a configured repo (no
// entry at all) -- is classified RepoAutonomyFetchUnconfirmed WITHOUT
// running any git command at all: there is deliberately no "HEAD" fallback
// here (removed by #877), since falling back to local HEAD would let a
// fetch outage silently authorize off a stale or unpushed local grant. Only
// when AutonomyRef is non-empty does this call probeRepoAutonomyDetailed, at
// that exact ref.
//
// A RepoAutonomyUnreadable result additionally logs the underlying git
// failure detail (probeRepoAutonomyDetailed's Detail), so an operator can
// distinguish a timeout from a bad ref from a missing binary -- never the
// config.json content itself.
//
// Log lines intentionally avoid the " skip:" / " dispatch " substrings --
// lazyboards classifies decision lines by matching them (formatDecision's
// doc comment, dispatch.go).
func probeRepoAutonomies(repos []RepoConfig, syncs map[string]mainSyncResult, out io.Writer) map[string]RepoAutonomy {
	if out == nil {
		out = os.Stdout
	}
	result := make(map[string]RepoAutonomy, len(repos))
	for _, rc := range repos {
		s, ok := syncs[rc.Repo]
		if !ok || s.AutonomyRef == "" {
			// No remote-confirmed object this pass (fetch failed, or the repo
			// has no syncs entry at all) -- never probe.
			result[rc.Repo] = RepoAutonomyFetchUnconfirmed
			logf(out, "dispatch: repo autonomy %s: %s\n", rc.Repo, RepoAutonomyFetchUnconfirmed)
			continue
		}
		ref := s.AutonomyRef
		p := probeRepoAutonomyDetailed(rc.Dir, ref)
		result[rc.Repo] = p.Autonomy
		if p.Autonomy == RepoAutonomyUnreadable && p.Detail != "" {
			logf(out, "dispatch: repo autonomy %s: %s (ref %s; %s)\n", rc.Repo, p.Autonomy, ref, p.Detail)
		} else {
			logf(out, "dispatch: repo autonomy %s: %s (ref %s)\n", rc.Repo, p.Autonomy, ref)
		}
	}
	return result
}
