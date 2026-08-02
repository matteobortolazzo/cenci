package dispatch

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -- #851: probeRepoAutonomy / probeRepoAutonomies -------------------------
//
// Reuses this package's existing #822 git test infrastructure
// (mainSyncGitEnv, initOriginAndLocal, gitTest, commitFile -- mainsync_test.go
// / collect_test.go) rather than duplicating it: the autonomy probe reads
// real committed content via `git show <ref>:.cenci/config.json`, never the
// working tree, so its tests need the same real-temp-repo isolation as the
// main-sync suite.

// writeCommittedConfig commits content as .cenci/config.json in dir, so
// probeRepoAutonomy has real, git-tracked content to read via `git show`.
func writeCommittedConfig(t *testing.T, dir, content string) {
	t.Helper()
	commitFile(t, dir, ".cenci/config.json", content)
}

const (
	leanConfigJSON         = `{"planning":{"autonomy":"lean"}}`
	interactiveConfigJSON  = `{"planning":{"autonomy":"interactive"}}`
	missingBlockConfigJSON = `{"automerge":{"enabled":true}}`
	missingKeyConfigJSON   = `{"planning":{}}`
	wrongCaseConfigJSON    = `{"planning":{"autonomy":"Lean"}}`
	malformedConfigJSON    = `{not valid json`

	// wrongCaseKeyConfigJSON has the correct lowercase "lean" value but
	// wrong-case *keys* ("Planning"/"Autonomy" instead of the documented
	// lowercase "planning"/"autonomy"). encoding/json's struct-field decode
	// falls back to case-insensitive matching, so a naive struct-tagged
	// decode would resolve this identically to the correct lowercase schema
	// -- this must instead resolve to Interactive, since every other reader
	// of this schema only recognizes the literal lowercase keys.
	wrongCaseKeyConfigJSON = `{"Planning":{"Autonomy":"lean"}}`
)

// TestProbeRepoAutonomy_LeanConfig_ReturnsLean covers the sole authorizing
// case: the exact string "lean" resolves RepoAutonomyLean.
func TestProbeRepoAutonomy_LeanConfig_ReturnsLean(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, leanConfigJSON)

	if got := probeRepoAutonomy(local, "HEAD"); got != RepoAutonomyLean {
		t.Errorf("probeRepoAutonomy = %q, want RepoAutonomyLean", got)
	}
}

// TestProbeRepoAutonomy_InteractiveConfig_ReturnsInteractive covers the
// explicit "interactive" value.
func TestProbeRepoAutonomy_InteractiveConfig_ReturnsInteractive(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, interactiveConfigJSON)

	if got := probeRepoAutonomy(local, "HEAD"); got != RepoAutonomyInteractive {
		t.Errorf("probeRepoAutonomy = %q, want RepoAutonomyInteractive", got)
	}
}

// TestProbeRepoAutonomy_MissingPlanningBlock_ReturnsInteractive covers a
// config.json that exists (so the path IS present in ref) but has no
// "planning" block at all -- default-deny to Interactive, not Missing.
func TestProbeRepoAutonomy_MissingPlanningBlock_ReturnsInteractive(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, missingBlockConfigJSON)

	if got := probeRepoAutonomy(local, "HEAD"); got != RepoAutonomyInteractive {
		t.Errorf("probeRepoAutonomy = %q, want RepoAutonomyInteractive (missing planning block)", got)
	}
}

// TestProbeRepoAutonomy_MissingAutonomyKey_ReturnsInteractive covers a
// present "planning" block with no "autonomy" key.
func TestProbeRepoAutonomy_MissingAutonomyKey_ReturnsInteractive(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, missingKeyConfigJSON)

	if got := probeRepoAutonomy(local, "HEAD"); got != RepoAutonomyInteractive {
		t.Errorf("probeRepoAutonomy = %q, want RepoAutonomyInteractive (missing autonomy key)", got)
	}
}

// TestProbeRepoAutonomy_WrongCaseValue_ReturnsInteractive covers the exact-
// string-match contract: "Lean" (wrong case) must NOT authorize -- only the
// literal lowercase "lean" does.
func TestProbeRepoAutonomy_WrongCaseValue_ReturnsInteractive(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, wrongCaseConfigJSON)

	if got := probeRepoAutonomy(local, "HEAD"); got != RepoAutonomyInteractive {
		t.Errorf("probeRepoAutonomy = %q, want RepoAutonomyInteractive (wrong-case \"Lean\" must not authorize)", got)
	}
}

// TestProbeRepoAutonomy_WrongCaseKey_ReturnsInteractive is the security
// fix's regression test: encoding/json's case-insensitive struct-field
// fallback must never let wrong-case JSON keys (e.g. "Planning"/"Autonomy")
// decode as if they were the documented lowercase "planning"/"autonomy"
// schema, even when the value itself is the exact authorizing "lean"
// string. Only the literal lowercase keys may authorize.
func TestProbeRepoAutonomy_WrongCaseKey_ReturnsInteractive(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, wrongCaseKeyConfigJSON)

	if got := probeRepoAutonomy(local, "HEAD"); got != RepoAutonomyInteractive {
		t.Errorf("probeRepoAutonomy = %q, want RepoAutonomyInteractive (wrong-case keys must not authorize even with value \"lean\")", got)
	}
}

// TestProbeRepoAutonomy_PathAbsentInRef_ReturnsMissing covers a repo with no
// .cenci/config.json committed at all: the path is absent from the ref
// entirely, distinct from a present-but-empty/malformed file.
func TestProbeRepoAutonomy_PathAbsentInRef_ReturnsMissing(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	// initOriginAndLocal's base commit never writes .cenci/config.json.

	if got := probeRepoAutonomy(local, "HEAD"); got != RepoAutonomyMissing {
		t.Errorf("probeRepoAutonomy = %q, want RepoAutonomyMissing (no config.json committed at all)", got)
	}
}

// TestProbeRepoAutonomy_MalformedJSON_ReturnsMalformed covers a committed
// config.json that fails to decode.
func TestProbeRepoAutonomy_MalformedJSON_ReturnsMalformed(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, malformedConfigJSON)

	if got := probeRepoAutonomy(local, "HEAD"); got != RepoAutonomyMalformed {
		t.Errorf("probeRepoAutonomy = %q, want RepoAutonomyMalformed", got)
	}
}

// TestProbeRepoAutonomy_BadRef_ReturnsUnreadable covers a ref that does not
// resolve in an otherwise-real repo -- the probe itself failed to run, must
// not be confused with "path absent" (a resolvable ref with no such path).
func TestProbeRepoAutonomy_BadRef_ReturnsUnreadable(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, leanConfigJSON)

	if got := probeRepoAutonomy(local, "no-such-ref-at-all"); got != RepoAutonomyUnreadable {
		t.Errorf("probeRepoAutonomy = %q, want RepoAutonomyUnreadable (bad ref)", got)
	}
}

// TestProbeRepoAutonomy_NonRepoDir_ReturnsUnreadable covers a plain,
// existing, non-git directory.
func TestProbeRepoAutonomy_NonRepoDir_ReturnsUnreadable(t *testing.T) {
	dir := t.TempDir()

	if got := probeRepoAutonomy(dir, "HEAD"); got != RepoAutonomyUnreadable {
		t.Errorf("probeRepoAutonomy = %q, want RepoAutonomyUnreadable (not a git repo)", got)
	}
}

// TestProbeRepoAutonomy_EmptyDir_ReturnsMissingNeverProbesCwd mirrors
// probeStage/syncMain's existing "dir==\"\" never probes cwd" convention
// (collect_test.go's TestProbeStage_EmptyDir_NeverProbes_NoCwdRelativeRead,
// mainsync_test.go's TestSyncMain_EmptyDir_NeverProbesCwd): chdir into a real
// repo with a genuinely lean committed config, and confirm probeRepoAutonomy
// still reports Missing for dir=="" rather than leaking the cwd-relative
// lean verdict in.
func TestProbeRepoAutonomy_EmptyDir_ReturnsMissingNeverProbesCwd(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, leanConfigJSON)

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(local); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	if got := probeRepoAutonomy("", "HEAD"); got != RepoAutonomyMissing {
		t.Errorf("probeRepoAutonomy(\"\", ...) = %q, want RepoAutonomyMissing -- dir=\"\" must never probe cwd", got)
	}
}

// TestProbeRepoAutonomy_UncommittedWorkingTreeEditDoesNotChangeVerdict is
// the plan Q&A 3's critical committed-vs-working-tree distinction: the
// probe must read via `git show <ref>:...`, never the working tree, so an
// uncommitted local edit can never grant (or revoke) lean.
func TestProbeRepoAutonomy_UncommittedWorkingTreeEditDoesNotChangeVerdict(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, leanConfigJSON)

	// Uncommitted edit: overwrite the working-tree file with an interactive
	// (denying) config, WITHOUT committing it.
	if err := os.WriteFile(filepath.Join(local, ".cenci", "config.json"), []byte(interactiveConfigJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := probeRepoAutonomy(local, "HEAD"); got != RepoAutonomyLean {
		t.Errorf("probeRepoAutonomy = %q, want RepoAutonomyLean -- an uncommitted working-tree edit must never change the verdict", got)
	}
}

// TestProbeRepoAutonomy_SharedFreshRefParity_LeanConfigOnOriginMainVisible
// is the plan Q&A 3's shared-FreshRef parity case: a lean config committed
// only on origin/main (not yet merged into local HEAD) must be visible when
// probed with FreshRef=="origin/main" -- and, for contrast, must NOT be
// visible via local HEAD, proving the ref parameter actually selects which
// blob is read rather than always reading local HEAD regardless of ref.
func TestProbeRepoAutonomy_SharedFreshRefParity_LeanConfigOnOriginMainVisible(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	writeCommittedConfig(t, origin, leanConfigJSON) // committed only on origin, not yet fetched/merged
	gitTest(t, local, "fetch", "origin")

	if got := probeRepoAutonomy(local, "origin/main"); got != RepoAutonomyLean {
		t.Errorf("probeRepoAutonomy(local, \"origin/main\") = %q, want RepoAutonomyLean (the fetched blob)", got)
	}
	if got := probeRepoAutonomy(local, "HEAD"); got != RepoAutonomyMissing {
		t.Errorf("probeRepoAutonomy(local, \"HEAD\") = %q, want RepoAutonomyMissing (local HEAD has not merged the config commit yet)", got)
	}
}

// TestProbeRepoAutonomies_MapKeyedByRepoUsingEachSyncsFreshRef covers
// probeRepoAutonomies' basic per-repo wiring: it loops every repo, reads
// each one's committed config at its own resolved AutonomyRef (#877, from
// the supplied syncs map -- FreshRef is no longer read here), and returns a
// map keyed by repo -- mirroring syncMains' own per-repo map contract.
func TestProbeRepoAutonomies_MapKeyedByRepoUsingEachSyncsFreshRef(t *testing.T) {
	mainSyncGitEnv(t)
	localA, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, localA, leanConfigJSON)
	localB, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, localB, interactiveConfigJSON)

	repos := []RepoConfig{
		{Repo: "o/a", Dir: localA},
		{Repo: "o/b", Dir: localB},
	}
	syncs := map[string]mainSyncResult{
		"o/a": {Status: MainSyncSynced, FreshRef: "HEAD", AutonomyRef: "HEAD"},
		"o/b": {Status: MainSyncSynced, FreshRef: "HEAD", AutonomyRef: "HEAD"},
	}

	got := probeRepoAutonomies(repos, syncs, io.Discard)
	if got["o/a"] != RepoAutonomyLean {
		t.Errorf(`probeRepoAutonomies()["o/a"] = %q, want RepoAutonomyLean`, got["o/a"])
	}
	if got["o/b"] != RepoAutonomyInteractive {
		t.Errorf(`probeRepoAutonomies()["o/b"] = %q, want RepoAutonomyInteractive`, got["o/b"])
	}
}

// TestProbeRepoAutonomies_LogsAvoidLoadBearingSubstrings mirrors
// TestSyncMains_TwoRepos_MapCarriesBothAndLogsDistinctOutcomesSafely
// (mainsync_test.go): probeRepoAutonomies' per-repo log lines must never
// contain the lazyboards-reserved " skip:" / " dispatch " substrings.
func TestProbeRepoAutonomies_LogsAvoidLoadBearingSubstrings(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, interactiveConfigJSON)

	repos := []RepoConfig{{Repo: "o/r", Dir: local}}
	syncs := map[string]mainSyncResult{"o/r": {Status: MainSyncSynced, FreshRef: "HEAD", AutonomyRef: "HEAD"}}

	var buf bytes.Buffer
	probeRepoAutonomies(repos, syncs, &buf)

	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, " skip:") || strings.Contains(line, " dispatch ") {
			t.Errorf("repo-level autonomy-probe log line must not contain the lazyboards-reserved substrings, got %q", line)
		}
	}
}

// -- #877: AutonomyRef-driven remote authorization ---------------------------
//
// #851 read planning.autonomy against each repo's resolved FreshRef, which
// fell back to local HEAD on every path except the dry-run strictly-behind
// branch -- so a fetch outage silently fell back to local HEAD, and a
// local-ahead main's config could grant/revoke lean without ever having been
// pushed. #877 requires probeRepoAutonomies to consume the new AutonomyRef
// field instead (non-empty only after a successful `git fetch origin` this
// pass, and always the fully-qualified refs/remotes/origin/main), removing
// the "HEAD" map-miss fallback entirely.

// TestProbeRepoAutonomy_RemoteRefAuthoritative_DiffersFromLocalHEAD covers
// the headline requirement: probing at the fully-qualified remote-tracking
// ref must read the fetched remote object, not local HEAD, even when the two
// disagree (here, local HEAD hasn't even merged the commit that carries the
// config at all).
func TestProbeRepoAutonomy_RemoteRefAuthoritative_DiffersFromLocalHEAD(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	writeCommittedConfig(t, origin, leanConfigJSON) // committed only on origin, not yet fetched/merged
	gitTest(t, local, "fetch", "origin")

	if got := probeRepoAutonomy(local, remoteMainAuthRef); got != RepoAutonomyLean {
		t.Errorf("probeRepoAutonomy(local, remoteMainAuthRef) = %q, want RepoAutonomyLean (the fetched remote object)", got)
	}
	if got := probeRepoAutonomy(local, "HEAD"); got != RepoAutonomyMissing {
		t.Errorf("probeRepoAutonomy(local, \"HEAD\") = %q, want RepoAutonomyMissing (local HEAD never merged the fetched commit)", got)
	}
}

// TestProbeRepoAutonomy_RemoteRevokedToInteractive_LocalMainStillLean_DeniesAtRemoteRef
// covers the ticket's acceptance criterion "remote revocation to interactive
// is honored even when local main still contains lean after the last
// successful pass": simulate an earlier successful pass that merged a lean
// grant into local main, then a remote revocation to interactive -- probing
// at the remote-authoritative ref must deny even though local HEAD (the
// stale merge from the prior pass) still reads lean.
func TestProbeRepoAutonomy_RemoteRevokedToInteractive_LocalMainStillLean_DeniesAtRemoteRef(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	writeCommittedConfig(t, origin, leanConfigJSON)
	// Simulate an earlier successful pass: fetch + fast-forward merges the
	// lean grant into local main.
	gitTest(t, local, "fetch", "origin")
	gitTest(t, local, "merge", "--ff-only", "origin/main")
	if got := probeRepoAutonomy(local, "HEAD"); got != RepoAutonomyLean {
		t.Fatalf("setup invariant broken: local HEAD = %q after the merge, want RepoAutonomyLean", got)
	}

	// Remote is now revoked to interactive; re-fetch (local main is
	// deliberately left un-merged here -- #822's fast-forward-only merge is
	// a separate concern from this test).
	writeCommittedConfig(t, origin, interactiveConfigJSON)
	gitTest(t, local, "fetch", "origin")

	if got := probeRepoAutonomy(local, remoteMainAuthRef); got != RepoAutonomyInteractive {
		t.Errorf("probeRepoAutonomy(local, remoteMainAuthRef) = %q, want RepoAutonomyInteractive (remote revocation honored)", got)
	}
	if got := probeRepoAutonomy(local, "HEAD"); got != RepoAutonomyLean {
		t.Fatalf("setup invariant broken: local HEAD should still read Lean (the revocation was never merged locally), got %q -- otherwise this test isn't actually proving remote-ref authority over a stale local grant", got)
	}
}

// TestProbeRepoAutonomy_LocalAheadUnpushedLean_CannotGrant_RemoteRefDenies
// covers the ticket's "local-ahead main cannot supply the autonomy grant;
// remote config remains authoritative" acceptance criterion: an unpushed
// local commit granting lean (local ahead of origin) must never authorize --
// only the fetched, remote-confirmed object can.
func TestProbeRepoAutonomy_LocalAheadUnpushedLean_CannotGrant_RemoteRefDenies(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	writeCommittedConfig(t, origin, interactiveConfigJSON)
	gitTest(t, local, "fetch", "origin")
	gitTest(t, local, "merge", "--ff-only", "origin/main")
	// Local-only, unpushed lean grant -- local main is now ahead of origin.
	writeCommittedConfig(t, local, leanConfigJSON)

	if got := probeRepoAutonomy(local, "HEAD"); got != RepoAutonomyLean {
		t.Fatalf("setup invariant broken: local HEAD should read Lean (the unpushed local grant), got %q", got)
	}
	if got := probeRepoAutonomy(local, remoteMainAuthRef); got != RepoAutonomyInteractive {
		t.Errorf("probeRepoAutonomy(local, remoteMainAuthRef) = %q, want RepoAutonomyInteractive -- an unpushed local-ahead grant must never authorize", got)
	}
}

// TestProbeRepoAutonomy_MalformedRemoteConfig_ReturnsMalformed covers a
// malformed committed config fetched from origin, probed at the fully
// qualified remote-tracking ref -- the malformed/unreadable-remote-config
// half of the ticket's "missing/malformed/unreadable/non-lean remote config
// denies; no stale fallback" acceptance criterion.
func TestProbeRepoAutonomy_MalformedRemoteConfig_ReturnsMalformed(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	writeCommittedConfig(t, origin, malformedConfigJSON)
	gitTest(t, local, "fetch", "origin")

	if got := probeRepoAutonomy(local, remoteMainAuthRef); got != RepoAutonomyMalformed {
		t.Errorf("probeRepoAutonomy(local, remoteMainAuthRef) = %q, want RepoAutonomyMalformed", got)
	}
}

// TestProbeRepoAutonomy_UnreadableRemoteRef_BadFetchedRef_ReturnsUnreadable
// covers the unreadable-remote-config half of the same acceptance criterion,
// using the fully-qualified ref form: a ref that does not resolve at all
// (e.g. a repo that was never fetched, so refs/remotes/origin/main doesn't
// exist yet) must classify as Unreadable -- the probe itself failed to run,
// distinct from a resolvable ref with an absent/malformed config.
func TestProbeRepoAutonomy_UnreadableRemoteRef_BadFetchedRef_ReturnsUnreadable(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	// Deliberately never fetched: refs/remotes/origin/main does not exist in
	// this clone yet (initOriginAndLocal's `git clone` seeds it, so delete it
	// to force the unresolvable case).
	gitTest(t, local, "update-ref", "-d", "refs/remotes/origin/main")

	if got := probeRepoAutonomy(local, remoteMainAuthRef); got != RepoAutonomyUnreadable {
		t.Errorf("probeRepoAutonomy(local, remoteMainAuthRef) = %q, want RepoAutonomyUnreadable (ref does not resolve)", got)
	}
}

// TestProbeRepoAutonomy_Q1_LocalBranchNamedOriginMain_DoesNotShadowRemoteTrackingRef
// is the plan's Q1 regression test: git's rev-parse precedence resolves
// refs/heads/<name> before refs/remotes/<name>, so the short "origin/main"
// string used elsewhere in mainsync.go could, in principle, be shadowed by a
// local branch literally named "origin/main". The authorization probe must
// use the fully-qualified refs/remotes/origin/main specifically to close
// this off: a local branch named "origin/main" carrying a granting lean
// config must never leak into the authorization decision.
func TestProbeRepoAutonomy_Q1_LocalBranchNamedOriginMain_DoesNotShadowRemoteTrackingRef(t *testing.T) {
	mainSyncGitEnv(t)
	local, origin := initOriginAndLocal(t)
	writeCommittedConfig(t, origin, interactiveConfigJSON)
	gitTest(t, local, "fetch", "origin")

	// A local branch literally named "origin/main" (refs/heads/origin/main)
	// carrying a granting lean config.
	gitTest(t, local, "checkout", "-b", "origin/main")
	writeCommittedConfig(t, local, leanConfigJSON)
	gitTest(t, local, "checkout", "main")

	if got := probeRepoAutonomy(local, remoteMainAuthRef); got != RepoAutonomyInteractive {
		t.Errorf("probeRepoAutonomy(local, remoteMainAuthRef) = %q, want RepoAutonomyInteractive -- a local branch literally named %q must never shadow the remote-tracking ref", got, "origin/main")
	}
	// Regression proof: the short, unqualified "origin/main" string DOES
	// resolve to the shadowing local branch (git's own rev-parse
	// precedence), confirming this test actually exercises the shadowing
	// scenario rather than a vacuous setup.
	if got := probeRepoAutonomy(local, "origin/main"); got != RepoAutonomyLean {
		t.Fatalf("probeRepoAutonomy(local, \"origin/main\") = %q, want RepoAutonomyLean -- if this fails, the shadowing-branch setup itself is not exercising ambiguity, invalidating the regression proof above", got)
	}
}

// TestProbeRepoAutonomies_FetchUnconfirmed_RunsNoProbeAndDeniesDistinctly
// covers the ticket's "fetch failure gates freshness-dependent
// planning/replanning with a distinct retryable reason" decision, at the
// probe layer: a repo whose AutonomyRef is empty this pass (no successful
// `git fetch origin`) must deny as the new RepoAutonomyFetchUnconfirmed
// classification WITHOUT running any git command at all -- proven by
// pointing Dir at a path that would fail differently (RepoAutonomyUnreadable)
// if a probe actually ran against it.
func TestProbeRepoAutonomies_FetchUnconfirmed_RunsNoProbeAndDeniesDistinctly(t *testing.T) {
	bogusDir := filepath.Join(t.TempDir(), "never-created")
	repos := []RepoConfig{{Repo: "o/r", Dir: bogusDir}}
	syncs := map[string]mainSyncResult{"o/r": {Status: MainSyncFetchFailed, AutonomyRef: ""}}

	var buf bytes.Buffer
	got := probeRepoAutonomies(repos, syncs, &buf)

	if got["o/r"] != RepoAutonomyFetchUnconfirmed {
		t.Errorf(`probeRepoAutonomies()["o/r"] = %q, want RepoAutonomyFetchUnconfirmed`, got["o/r"])
	}
	log := buf.String()
	if strings.Contains(log, string(RepoAutonomyUnreadable)) {
		t.Errorf("expected no probe to run at all (no Unreadable outcome from touching the bogus, never-created dir), got log %q", log)
	}
	for _, line := range strings.Split(log, "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, " skip:") || strings.Contains(line, " dispatch ") {
			t.Errorf("repo-level autonomy-probe log line must not contain the lazyboards-reserved substrings, got %q", line)
		}
	}
}

// TestProbeRepoAutonomies_MissingSyncsMapEntry_DeniesFetchUnconfirmedNotHead
// covers the removed "HEAD" map-miss fallback directly: a repo absent from
// the syncs map entirely (not merely present with an empty AutonomyRef) must
// deny as RepoAutonomyFetchUnconfirmed, never silently fall back to probing
// local HEAD.
func TestProbeRepoAutonomies_MissingSyncsMapEntry_DeniesFetchUnconfirmedNotHead(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, leanConfigJSON) // local HEAD IS lean

	repos := []RepoConfig{{Repo: "o/r", Dir: local}}
	got := probeRepoAutonomies(repos, map[string]mainSyncResult{}, io.Discard)

	if got["o/r"] != RepoAutonomyFetchUnconfirmed {
		t.Errorf(`probeRepoAutonomies()["o/r"] = %q, want RepoAutonomyFetchUnconfirmed -- a syncs map miss must no longer fall back to probing local HEAD (which is lean here)`, got["o/r"])
	}
}

// TestProbeRepoAutonomies_MissingSyncsMapEntry_NoLongerFallsBackToLocalHEAD is
// TestProbeRepoAutonomies_MissingSyncsMapEntry_DeniesFetchUnconfirmedNotHead's
// twin, written using only symbols that already exist in today's production
// code (RepoAutonomyLean, no RepoAutonomyFetchUnconfirmed reference) so it
// compiles cleanly against the current, unmodified autonomy.go and fails on
// assertion rather than on a compile error: today's "HEAD" map-miss fallback
// (autonomy.go's `ref := "HEAD"` default) wrongly authorizes off local HEAD
// for a repo this pass never confirmed a fetch for.
func TestProbeRepoAutonomies_MissingSyncsMapEntry_NoLongerFallsBackToLocalHEAD(t *testing.T) {
	mainSyncGitEnv(t)
	local, _ := initOriginAndLocal(t)
	writeCommittedConfig(t, local, leanConfigJSON) // local HEAD IS lean

	repos := []RepoConfig{{Repo: "o/r", Dir: local}}
	got := probeRepoAutonomies(repos, map[string]mainSyncResult{}, io.Discard)

	if got["o/r"] == RepoAutonomyLean {
		t.Errorf(`probeRepoAutonomies()["o/r"] = RepoAutonomyLean via the "HEAD" map-miss fallback -- a repo this pass never confirmed a fetch for must never authorize off local HEAD`)
	}
}
