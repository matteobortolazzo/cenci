package planfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitTest runs a git command in dir with a pinned identity, failing the test
// on error and returning trimmed stdout. Mirrors internal/dispatch's own
// collect_test.go helper of the same name/shape (a different package, so no
// collision), needed here for #851's new CommitsBehindRef tests.
func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitFile makes one commit touching a single file under dir. Mirrors
// internal/dispatch/collect_test.go's helper of the same name.
func commitFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", rel)
	gitTest(t, dir, "commit", "-m", content)
}

func TestParseFrontMatter(t *testing.T) {
	content := `---
version: 1
mode: ticket
ticketId: 42
ticketTitle: "Add dark mode support"
slug: add-dark-mode
isChild: false
isLastChild: false
parentId: null
status: planned
planCommitSha: abc123def
---

## Implementation Plan
do the thing
`
	m, ok := ParseFrontMatter(content)
	if !ok {
		t.Fatal("expected front matter to parse")
	}
	cases := map[string]string{
		"ticketId":      "42",
		"ticketTitle":   "Add dark mode support", // quotes stripped
		"status":        "planned",
		"isChild":       "false",
		"parentId":      "null",
		"planCommitSha": "abc123def",
	}
	for k, want := range cases {
		if got := m[k]; got != want {
			t.Errorf("front matter[%q] = %q, want %q", k, got, want)
		}
	}
	// The body must not leak into the map.
	if _, ok := m["## Implementation Plan"]; ok {
		t.Error("body heading leaked into front matter")
	}
}

func TestParseFrontMatterNoFrontMatter(t *testing.T) {
	if _, ok := ParseFrontMatter("just a plain file\nno front matter\n"); ok {
		t.Error("expected false for a file without front matter")
	}
	if _, ok := ParseFrontMatter("---\nnever closed\n"); ok {
		t.Error("expected false for an unterminated front-matter block")
	}
	// The opening fence must stand on its own line, not run into a key.
	if _, ok := ParseFrontMatter("---status: planned\n---\n"); ok {
		t.Error("expected false when --- is not a line of its own")
	}
}

func TestParseFrontMatterBOM(t *testing.T) {
	m, ok := ParseFrontMatter("\ufeff---\nstatus: planned\n---\n")
	if !ok {
		t.Fatal("expected front matter to parse past a BOM")
	}
	if m["status"] != "planned" {
		t.Errorf("status = %q, want planned", m["status"])
	}
}

func TestAtoiSafe(t *testing.T) {
	cases := map[string]int{"42": 42, "": 0, "null": 0, " 7 ": 7, "abc": 0}
	for in, want := range cases {
		if got := AtoiSafe(in); got != want {
			t.Errorf("AtoiSafe(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestSplitPaths(t *testing.T) {
	cases := map[string][]string{
		"watch, flow":  {"watch", "flow"},
		"watch,,flow,": {"watch", "flow"},
		"":             nil,
		"  ":           nil,
	}
	for in, want := range cases {
		got := SplitPaths(in)
		if len(got) != len(want) {
			t.Errorf("SplitPaths(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("SplitPaths(%q) = %v, want %v", in, got, want)
				break
			}
		}
	}
}

// -- #851: CommitsBehindRef, the ref-parameterized sibling of CommitsBehind -

// TestCommitsBehindRef_CountsAgainstArbitraryRefNotJustHEAD covers the new
// ref parameter: two divergent branches from the same base sha report
// different counts, proving the count is scoped to the supplied ref rather
// than hardcoded to HEAD.
func TestCommitsBehindRef_CountsAgainstArbitraryRefNotJustHEAD(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init", "-b", "main")
	commitFile(t, dir, "base.txt", "base")
	sha := gitTest(t, dir, "rev-parse", "HEAD")

	gitTest(t, dir, "checkout", "-b", "other")
	commitFile(t, dir, "other1.txt", "o1")
	commitFile(t, dir, "other2.txt", "o2")
	gitTest(t, dir, "checkout", "main")
	commitFile(t, dir, "main1.txt", "m1")

	if got, err := CommitsBehindRef(dir, sha, "main", nil); err != nil || got != 1 {
		t.Errorf("CommitsBehindRef(main) = %d, err = %v, want 1, nil", got, err)
	}
	if got, err := CommitsBehindRef(dir, sha, "other", nil); err != nil || got != 2 {
		t.Errorf("CommitsBehindRef(other) = %d, err = %v, want 2, nil", got, err)
	}
}

// TestCommitsBehindRef_PathScoping mirrors CommitsBehind's existing
// path-scoping contract (internal/dispatch's TestGitCommitsBehindPathAware)
// for the new ref-parameterized sibling.
func TestCommitsBehindRef_PathScoping(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init", "-b", "main")
	commitFile(t, dir, "base.txt", "base")
	sha := gitTest(t, dir, "rev-parse", "HEAD")
	commitFile(t, dir, "watch/main.go", "watch change")
	commitFile(t, dir, "flow/skill.md", "flow change")

	if got, err := CommitsBehindRef(dir, sha, "HEAD", []string{"watch"}); err != nil || got != 1 {
		t.Errorf("watch-scoped count = %d, err = %v, want 1, nil", got, err)
	}
	if got, err := CommitsBehindRef(dir, sha, "HEAD", nil); err != nil || got != 2 {
		t.Errorf("whole-repo count = %d, err = %v, want 2, nil", got, err)
	}
}

// TestCommitsBehind_DelegatesToCommitsBehindRefWithHEAD is the
// behavior-equivalence regression case: CommitsBehind(dir, sha, paths) must
// produce the exact same result as CommitsBehindRef(dir, sha, "HEAD",
// paths) for identical inputs, proving CommitsBehind is now a thin
// ref="HEAD" wrapper rather than an independent implementation that could
// drift from it.
func TestCommitsBehind_DelegatesToCommitsBehindRefWithHEAD(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init", "-b", "main")
	commitFile(t, dir, "base.txt", "base")
	sha := gitTest(t, dir, "rev-parse", "HEAD")
	commitFile(t, dir, "a.txt", "a")
	commitFile(t, dir, "b.txt", "b")

	want, err := CommitsBehindRef(dir, sha, "HEAD", nil)
	if err != nil {
		t.Fatalf("CommitsBehindRef returned unexpected error: %v", err)
	}
	got, err := CommitsBehind(dir, sha, nil)
	if err != nil {
		t.Fatalf("CommitsBehind returned unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("CommitsBehind = %d, want it to equal CommitsBehindRef(..., \"HEAD\", ...) = %d", got, want)
	}
}

// TestCommitsBehindRef_GitFailurePropagatesError mirrors CommitsBehind's
// existing propagate-don't-swallow contract (a git failure must surface to
// the caller, not be swallowed to 0) for a nonexistent ref.
func TestCommitsBehindRef_GitFailurePropagatesError(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init", "-b", "main")
	commitFile(t, dir, "base.txt", "base")
	sha := gitTest(t, dir, "rev-parse", "HEAD")

	if _, err := CommitsBehindRef(dir, sha, "no-such-ref", nil); err == nil {
		t.Fatal("expected an error for a nonexistent ref, got nil")
	}
}

// TestCommitsBehindRef_MalformedShaRejectedBeforeGit pins the hardening fix
// that validates a front-matter-sourced sha against commitShaPattern before
// it ever reaches git rev-list argv: a value git would otherwise parse as an
// option (leading "-") must be rejected outright, and a nonexistent dir
// proves git is never actually invoked (a real invocation would fail with a
// different, dir-related error, not the validation error asserted below).
func TestCommitsBehindRef_MalformedShaRejectedBeforeGit(t *testing.T) {
	const noSuchDir = "/nonexistent/path/that/must/never/be/passed/to/git"
	for _, sha := range []string{"--upload-pack=x", "not-a-sha"} {
		_, err := CommitsBehindRef(noSuchDir, sha, "HEAD", nil)
		if err == nil || !strings.Contains(err.Error(), "invalid planCommitSha") {
			t.Errorf("CommitsBehindRef(%q) err = %v, want the pre-git validation error", sha, err)
		}
	}
}
