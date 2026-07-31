// Package planfile provides the shared stdlib front-matter parser and git
// staleness helper for `.plans/<id>-*.md` files (ticket #560, extracted from
// internal/dispatch/collect.go per that ticket's Q&A #4): flat `key: value`
// YAML-ish front matter (no yaml dependency), a comma-separated path-list
// splitter, a defensive int parser, and a git commits-behind count scoped to
// an optional path list. internal/dispatch and internal/pipeline both
// consume this package rather than duplicating it.
package planfile

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// commitShaPattern constrains sha before it reaches `git rev-list --count
// <sha>..<ref>`: a value starting with `-` could otherwise be parsed as a
// git option instead of a revision. Mirrors pipeline/planfile.go's own
// planCommitShaPattern exactly (same field, same front-matter source,
// same semantics) -- kept as its own unexported constant here rather than
// imported cross-package, since pipeline already validates the field before
// calling CommitsBehind and this package must not import pipeline.
var commitShaPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// ParseFrontMatter reads the leading `---`-delimited block as flat key: value
// scalars (no yaml dependency). It returns false when no front matter is
// found (missing opening fence, or an opening fence that never closes).
func ParseFrontMatter(content string) (map[string]string, bool) {
	content = strings.TrimPrefix(content, "\ufeff") // drop a leading UTF-8 BOM
	// The opening fence must be a line of its own ("---\n" or "---\r\n"), not a
	// key line that merely starts with dashes.
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return nil, false
	}
	rest := strings.TrimPrefix(content[len("---"):], "\r")
	rest = strings.TrimPrefix(rest, "\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, false
	}

	m := make(map[string]string)
	for _, line := range strings.Split(rest[:end], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"'`)
		m[key] = val
	}
	return m, true
}

// AtoiSafe parses an int, returning 0 for empty, "null", or malformed values.
func AtoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// SplitPaths parses a comma-separated stalenessPaths value into cleaned
// repo-relative paths: entries are trimmed and empties dropped, so trailing
// commas and stray spaces are harmless.
func SplitPaths(s string) []string {
	var paths []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// gitTimeout bounds every git invocation CommitsBehindRef makes (#852),
// mirroring internal/dispatch's execGit rationale (mainsync.go): a hung git
// process must never stall a caller indefinitely. Deliberately a
// package-local constant, not shared with internal/dispatch's own
// gitTimeout/execGit -- see #852's D1 decision: internal/planfile cannot
// import internal/dispatch, and a shared internal/boundedexec package would
// drag execGit into an unwanted refactor.
const gitTimeout = 60 * time.Second

// gitWaitDelay bounds how long cmd.Wait can block after the git process
// itself has exited or been killed by gitTimeout's context, mirroring
// internal/dispatch's gitWaitDelay (mainsync.go).
const gitWaitDelay = 5 * time.Second

// IsValidCommitSha reports whether sha has the shape of a legitimate git
// commit sha: 7-40 hex characters, per commitShaPattern above -- rejecting,
// in particular, a sha that begins with `-` (which git would otherwise
// parse as an option once concatenated into "<sha>..<ref>") or one that is
// otherwise malformed. Deliberately does NOT special-case the empty
// string: an absent planCommitSha is a distinct, deliberately-permissive
// "legacy plan file" case (see CommitsBehindRef's own sha == "" short-circuit),
// not a malformed one, and callers are expected to check for emptiness
// themselves before ever consulting this function.
func IsValidCommitSha(sha string) bool {
	return commitShaPattern.MatchString(sha)
}

// CommitsBehind counts default-branch commits since sha, against local HEAD.
// With paths it counts only commits touching them (`rev-list -- <paths>`),
// so unrelated monorepo churn cannot mark a scoped plan stale; without paths
// it keeps the whole-repo count.
//
// A thin ref="HEAD" wrapper around CommitsBehindRef (#851) -- kept as its
// own function rather than inlined at every call site because
// pipeline/planfile.go:276 also calls it and must not have its signature
// changed by that ticket.
//
// A git failure (unreachable sha, shallow clone, corrupted worktree,
// transient error) is propagated rather than swallowed to 0 -- callers that
// gate a decision on this count (e.g. internal/pipeline's planIsStale) must
// not confuse "genuinely 0 commits behind" with "could not be determined".
// Callers that only display the count (e.g. internal/dispatch's ReadPlans)
// may choose to ignore the returned error and degrade gracefully.
//
// The git invocation itself is bounded by gitTimeout/gitWaitDelay (#852) so
// a hung git process can never stall a caller indefinitely.
//
// sha == "" short-circuits to (0, nil) before ever invoking git (#852 D2):
// an empty planCommitSha is deliberate absence-by-design for a legacy
// pre-planCommitSha plan file, not a staleness-calculation error, so it
// must never reach AC5's "staleness calculation errors" default-deny path.
// This makes today's "fresh" behavior for an empty sha explicit rather than
// an accidental consequence of git resolving "..HEAD" as "HEAD..HEAD" = 0.
//
// A non-empty sha is validated via IsValidCommitSha before ever shelling
// out (#852): a malformed sha -- in particular one beginning with `-`,
// which git would otherwise parse as an option once concatenated into
// "<sha>..HEAD" -- is rejected with an error rather than spliced into the
// git invocation (closing an argument-injection hole internal/pipeline
// already guards against, but ReadPlans historically did not).
func CommitsBehind(dir, sha string, paths []string) (int, error) {
	return CommitsBehindRef(dir, sha, "HEAD", paths)
}

// CommitsBehindRef counts default-branch commits since sha, scoped to an
// arbitrary ref rather than hardcoded to HEAD (#851): dispatch's dry-run pass
// needs to compute staleness against the fetched origin/main blob when a
// fast-forward is pending, without ever mutating local HEAD to do so. With
// paths it counts only commits touching them (`rev-list -- <paths>`), so
// unrelated monorepo churn cannot mark a scoped plan stale; without paths it
// keeps the whole-repo count.
//
// A git failure (unreachable sha/ref, shallow clone, corrupted worktree,
// transient error) is propagated rather than swallowed to 0 -- see
// CommitsBehind's own doc comment above for the full rationale.
//
// sha == "" short-circuits to (0, nil) before ever invoking git (#852 D2):
// an empty planCommitSha is deliberate absence-by-design for a legacy
// pre-planCommitSha plan file, not a staleness-calculation error, so it must
// never reach AC5's "staleness calculation errors" default-deny path. This
// makes the "fresh" verdict for an empty sha explicit rather than an
// accidental consequence of git resolving "..<ref>" as "<ref>..<ref>" = 0.
//
// A non-empty sha is validated via IsValidCommitSha before it ever reaches
// git argv construction: a value beginning with `-` (e.g. "--upload-pack=x")
// would otherwise be interpreted as a git option rather than a revision,
// since sha comes straight from a `.plans/<id>-*.md` file's untrusted front
// matter.
func CommitsBehindRef(dir, sha, ref string, paths []string) (int, error) {
	if sha == "" {
		return 0, nil
	}
	if !IsValidCommitSha(sha) {
		return 0, fmt.Errorf("invalid planCommitSha %q: must match %s", sha, commitShaPattern.String())
	}
	args := []string{"-C", dir, "rev-list", "--count", sha + ".." + ref}
	if len(paths) > 0 {
		args = append(append(args, "--"), paths...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = gitWaitDelay
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count %s..%s: %w", sha, ref, err)
	}
	return AtoiSafe(strings.TrimSpace(string(out))), nil
}
