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
// each one's committed config at its own resolved FreshRef (from the
// supplied syncs map), and returns a map keyed by repo -- mirroring
// syncMains' own per-repo map contract.
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
		"o/a": {Status: MainSyncSynced, FreshRef: "HEAD"},
		"o/b": {Status: MainSyncSynced, FreshRef: "HEAD"},
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
	syncs := map[string]mainSyncResult{"o/r": {Status: MainSyncSynced, FreshRef: "HEAD"}}

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
