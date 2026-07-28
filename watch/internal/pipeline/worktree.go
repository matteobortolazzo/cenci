package pipeline

// Deterministic worktree create + cleanup (ticket #559): `.worktrees/<id>-
// <slug>` dir, `feature/<id>-<slug>` branch, created via `git -C <main-root>
// worktree add <dir> -b <branch>`, with <main-root> resolved the same way
// as resolveRepoRoot (store.go) so this always anchors on the MAIN
// checkout, never a linked worktree's own root. CreateWorktree records the
// resulting Branch + WorktreePath into the persisted pipeline State (via
// the artifacts API) and rolls back any partial state on failure.
// CleanupWorktree removes both the worktree directory (`git worktree
// remove`) and the branch (`git branch -D`) -- used only on the
// creation-failure rollback path, never on a "baseline gate failed" path
// (that path deliberately keeps the worktree/branch around for retry).
//
// AttachWorktree (ticket #688, closing #718 item 2) is the reuse-mode
// counterpart: `cenci pipeline worktree <id> --attach PATH` validates an
// existing worktree/branch against `git worktree list --porcelain` and
// records it as a pipeline artifact -- it creates nothing (no dir, no
// branch), so unlike CreateWorktree there is no rollback path: a
// SetArtifacts failure returns the error with no git side effects to undo.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrWorktreeExists is returned when the target worktree directory or
// branch for <id>-<slug> already exists (errors.Is-detectable, rule #412).
var ErrWorktreeExists = errors.New("pipeline: worktree or branch already exists")

// ErrWorktreeNotFound is returned by AttachWorktree when --attach PATH does
// not resolve to a path registered in `git worktree list --porcelain` for
// this repo: covers "doesn't exist", "plain directory", and "worktree of a
// different repo" alike -- a membership check, not a path-shape heuristic
// (errors.Is-detectable, content-distinct from ErrWorktreeNotAttachable and
// ErrWorktreeExists per rule #446).
var ErrWorktreeNotFound = errors.New("pipeline: not a registered worktree")

// ErrWorktreeNotAttachable is returned by AttachWorktree when --attach PATH
// IS a registered worktree but is not a valid attach target: either it
// resolves to the main checkout (never attach the main checkout -- a
// repo-wide critical rule) or it is checked out at a detached HEAD (no
// `branch refs/heads/...` line to record).
var ErrWorktreeNotAttachable = errors.New("pipeline: worktree not attachable")

// slugPattern is the validation gate every --slug must pass before it is
// used to build a worktree dir/branch name, guarding against path traversal
// (e.g. a slug of "../99-foo") via a malformed slug. CreateWorktree is only
// incidentally protected against this today -- the resulting branch ref
// (`feature/<id>-<slug>`) happens to be rejected by git's own ref-format
// check -- but CleanupWorktree builds no ref at all and runs `git worktree
// remove --force` + `git branch -D` straight off the joined path, so a
// traversal slug there would silently act on a DIFFERENT ticket's
// worktree/branch. Mirrors idPattern's (store.go) role for <id>.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// validateSlug rejects any slug that doesn't match slugPattern (empty,
// containing "/" or ".", leading "-", etc.) before it is ever used to build
// a filesystem path or git ref.
func validateSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("invalid worktree slug %q: must match ^[a-z0-9]+(?:-[a-z0-9]+)*$", slug)
	}
	return nil
}

// branchPattern is the validation gate AttachWorktree runs the parsed
// `branch refs/heads/<name>` porcelain value through before ever recording
// or returning it. Unlike CreateWorktree's own branch (a name THIS package
// builds from an already-validated slug), AttachWorktree's branch comes
// straight from `git worktree list --porcelain` output for a worktree the
// CALLER pointed --attach at -- and flow/skills/implement/phases/
// phase-9-pr.md later interpolates it, unquoted, into `git -C <path> push
// -u origin <branch>`. A git ref name can contain shell metacharacters (or
// start with "-", which a shell/CLI could mistake for a flag), so this
// mirrors slugPattern's role for <slug>: reject before the value is ever
// trusted downstream. Requires a leading alphanumeric, then any run of
// alphanumerics/`._/-`.
var branchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// validateBranchName rejects any branch name that doesn't match
// branchPattern.
func validateBranchName(branch string) error {
	if !branchPattern.MatchString(branch) {
		return fmt.Errorf("invalid branch name %q: must match ^[A-Za-z0-9][A-Za-z0-9._/-]*$", branch)
	}
	return nil
}

// WorktreeOpts are the resolved inputs to CreateWorktree/CleanupWorktree/
// AttachWorktree. RepoRoot/StateDir mirror pipeline.Opts' own precedence for
// locating the persisted state file; RepoRoot additionally anchors where
// `git worktree add`/`remove`/`branch -D`/`list --porcelain` run (resolved
// to the main checkout root the same way as resolveRepoRoot when RepoRoot
// is empty). AttachPath is AttachWorktree's own input (ticket #688):
// mutually exclusive with Slug at the CLI layer, never used by
// CreateWorktree/CleanupWorktree.
type WorktreeOpts struct {
	ID       string
	Slug     string
	RepoRoot string
	StateDir string

	AttachPath string
}

// resolveMainRoot mirrors resolveStatePath's own RepoRoot precedence: use
// repoRoot verbatim when set (a test/override hook), otherwise resolve the
// MAIN checkout root from the current working directory via
// resolveRepoRoot.
func resolveMainRoot(repoRoot string) (string, error) {
	if repoRoot != "" {
		return repoRoot, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return resolveRepoRoot(cwd)
}

// worktreeNames computes the deterministic dir + branch pair for id+slug:
// `.worktrees/<id>-<slug>` under mainRoot, and branch `feature/<id>-<slug>`.
func worktreeNames(mainRoot, id, slug string) (dir, branch string) {
	name := id + "-" + slug
	return filepath.Join(mainRoot, ".worktrees", name), "feature/" + name
}

// gitBranchExists reports whether branch exists in the repo at root.
func gitBranchExists(root, branch string) bool {
	out, err := command("git", "-C", root, "branch", "--list", branch)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// CreateWorktree creates `.worktrees/<id>-<slug>` and branch
// `feature/<id>-<slug>` via `git worktree add`, anchored at the main
// checkout root, records Branch + WorktreePath into the persisted pipeline
// state, and rolls back any partial state (dir and/or branch) on failure.
// Returns ErrWorktreeExists (errors.Is-detectable) when the target
// dir/branch already exists.
func CreateWorktree(o WorktreeOpts) (State, error) {
	if err := validateSlug(o.Slug); err != nil {
		return State{}, err
	}

	mainRoot, err := resolveMainRoot(o.RepoRoot)
	if err != nil {
		return State{}, fmt.Errorf("resolve main checkout root: %w", err)
	}

	dir, branch := worktreeNames(mainRoot, o.ID, o.Slug)

	if _, statErr := os.Stat(dir); statErr == nil {
		return State{}, fmt.Errorf("worktree dir %s already exists: %w", dir, ErrWorktreeExists)
	}
	if gitBranchExists(mainRoot, branch) {
		return State{}, fmt.Errorf("branch %s already exists: %w", branch, ErrWorktreeExists)
	}

	if out, cmdErr := command("git", "-C", mainRoot, "worktree", "add", dir, "-b", branch); cmdErr != nil {
		gitErr := fmt.Errorf("git worktree add %s -b %s: %s: %w", dir, branch, strings.TrimSpace(string(out)), cmdErr)
		if rbErr := cleanupWorktreeAndBranch(mainRoot, dir, branch); rbErr != nil {
			return State{}, fmt.Errorf("%w (rollback also failed: %v)", gitErr, rbErr)
		}
		return State{}, gitErr
	}

	result, artErr := SetArtifacts(ArtifactOpts{
		ID:           o.ID,
		RepoRoot:     o.RepoRoot,
		StateDir:     o.StateDir,
		Branch:       branch,
		WorktreePath: dir,
	})
	if artErr != nil {
		recordErr := fmt.Errorf("record worktree artifacts: %w", artErr)
		if rbErr := cleanupWorktreeAndBranch(mainRoot, dir, branch); rbErr != nil {
			return State{}, fmt.Errorf("%w (rollback also failed: %v)", recordErr, rbErr)
		}
		return State{}, recordErr
	}
	return result, nil
}

// CleanupWorktree removes both the worktree directory (`git worktree
// remove`) and the branch (`git branch -D`) for o.ID + o.Slug. Used only on
// the creation-failure rollback path -- never on a "baseline gate failed"
// path, which keeps the worktree/branch for retry. "Branch not found" (the
// branch was never created because creation failed before the branch
// existed) is treated as a successful no-op, not an error.
func CleanupWorktree(o WorktreeOpts) error {
	if err := validateSlug(o.Slug); err != nil {
		return err
	}

	mainRoot, err := resolveMainRoot(o.RepoRoot)
	if err != nil {
		return fmt.Errorf("resolve main checkout root: %w", err)
	}
	dir, branch := worktreeNames(mainRoot, o.ID, o.Slug)
	return cleanupWorktreeAndBranch(mainRoot, dir, branch)
}

// cleanupWorktreeAndBranch removes the worktree at dir (if it exists) and
// deletes branch (if it exists), tolerating "branch not found" as a
// successful no-op.
func cleanupWorktreeAndBranch(mainRoot, dir, branch string) error {
	if _, statErr := os.Stat(dir); statErr == nil {
		if out, cmdErr := command("git", "-C", mainRoot, "worktree", "remove", dir, "--force"); cmdErr != nil {
			return fmt.Errorf("git worktree remove %s: %s: %w", dir, strings.TrimSpace(string(out)), cmdErr)
		}
	}

	out, cmdErr := command("git", "-C", mainRoot, "branch", "-D", branch)
	if cmdErr != nil {
		text := strings.ToLower(strings.TrimSpace(string(out)))
		if strings.Contains(text, "not found") {
			return nil
		}
		return fmt.Errorf("git branch -D %s: %s: %w", branch, strings.TrimSpace(string(out)), cmdErr)
	}
	return nil
}

// worktreeRecord is one `git worktree list --porcelain` record: the
// worktree's path, its checked-out branch's short name (empty when
// detached), and whether it is detached.
type worktreeRecord struct {
	Path     string
	Branch   string
	Detached bool
}

// listWorktrees parses `git -C mainRoot worktree list --porcelain` into its
// blank-line-separated records. Each record starts with a `worktree <path>`
// line; an optional `branch refs/heads/<name>` line follows for a
// non-detached checkout, or a bare `detached` line for a detached HEAD. The
// first record is always the main worktree itself. All parse ambiguities
// (a record with no recognized branch/detached line) fail closed as
// "detached" -- AttachWorktree then rejects it as ErrWorktreeNotAttachable
// rather than silently attaching to an unknown checkout state.
func listWorktrees(mainRoot string) ([]worktreeRecord, error) {
	out, err := command("git", "-C", mainRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list --porcelain: %s: %w", strings.TrimSpace(string(out)), err)
	}

	var records []worktreeRecord
	var cur *worktreeRecord
	flush := func() {
		if cur != nil {
			records = append(records, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			continue
		}
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			flush()
			cur = &worktreeRecord{Path: path}
			continue
		}
		if cur == nil {
			// A line before any "worktree " header is not a valid record;
			// ignore it rather than panicking on cur.
			continue
		}
		if branch, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			cur.Branch = branch
			continue
		}
		if line == "detached" {
			cur.Detached = true
		}
	}
	flush()
	return records, nil
}

// resolveAttachPath resolves attachPath (relative paths join against
// mainRoot, per the plan's "PATH resolution" rule) to an absolute,
// symlink-resolved, cleaned path. A resolution failure (the path does not
// exist, or a component along it does not) is reported so the caller can
// fold it into ErrWorktreeNotFound -- default-deny: an unresolvable path
// can never be proven to be a registered worktree.
func resolveAttachPath(mainRoot, attachPath string) (string, error) {
	path := attachPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(mainRoot, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve attach path %s: %w", attachPath, err)
	}
	return filepath.Clean(resolved), nil
}

// AttachWorktree validates o.AttachPath against `git worktree list
// --porcelain` and records its resolved path + derived branch as pipeline
// artifacts (SetArtifacts{Branch, WorktreePath}, identical shape to
// CreateWorktree's own recording so artifact --get/reset/Phase 9 all see it
// as a tracked artifact) -- it creates nothing. See this file's header
// comment and the ticket #688 plan's "CLI surface -- item 2 (exact)"
// section for the full validation/sentinel contract:
//
//   - not a registered worktree of this repo (doesn't exist, a plain
//     directory, or a worktree of a DIFFERENT repo) -> ErrWorktreeNotFound.
//   - resolves to the main checkout -> ErrWorktreeNotAttachable (never
//     attach the main checkout).
//   - registered but detached HEAD -> ErrWorktreeNotAttachable.
//
// The "never attach the main checkout" comparison trusts git's own
// porcelain answer -- records[0].Path from `git worktree list --porcelain`
// is always the main worktree itself -- NOT resolveMainRoot/o.RepoRoot: the
// --repo test hook returns o.RepoRoot verbatim, so a caller could otherwise
// point --repo at a linked worktree to disguise the true main checkout and
// slip it past this guard. Deriving the identity from records[0].Path
// closes that gap because it is git's repo-wide answer, unaffected by
// which worktree (or which --repo override) the command was invoked
// against.
//
// A worktree path containing a newline is not attachable: `git worktree
// list --porcelain` is line-oriented, so such a path can never
// string-equal any single "worktree <path>" record and falls through to
// ErrWorktreeNotFound -- this is a documented consequence of the porcelain
// format, not specially handled.
//
// warnings holds at most one entry: either "replaced tracked worktree <old>
// with <new>" when a DIFFERENT worktree/branch was already recorded for
// o.ID (re-attaching the same path twice is a silent, idempotent success
// with no warning), or "could not read prior pipeline state for <id>: ...;
// proceeding" when reading that prior state failed with a genuine error
// (e.g. a corrupted state file) -- in which case the replacement comparison
// above is skipped rather than trusting an unreadable prior state's zero
// value. There is no rollback path: attach creates nothing, so a
// SetArtifacts failure is returned as-is, with no git side effects to undo
// (unlike CreateWorktree).
func AttachWorktree(o WorktreeOpts) (State, []string, error) {
	mainRoot, err := resolveMainRoot(o.RepoRoot)
	if err != nil {
		return State{}, nil, fmt.Errorf("resolve main checkout root: %w", err)
	}

	resolvedAttach, err := resolveAttachPath(mainRoot, o.AttachPath)
	if err != nil {
		return State{}, nil, fmt.Errorf("%s: %w", err, ErrWorktreeNotFound)
	}

	records, err := listWorktrees(mainRoot)
	if err != nil {
		return State{}, nil, err
	}
	if len(records) == 0 {
		// Defensive: `git worktree list --porcelain` never returns zero
		// records for a valid repo (the main worktree is always its first
		// record) -- an empty list means something is deeply wrong, and we
		// must not fall through with no trustworthy main-checkout identity
		// to compare against.
		return State{}, nil, fmt.Errorf(
			"git worktree list --porcelain returned no records for %s: %w",
			mainRoot, ErrWorktreeNotFound,
		)
	}

	// The main-checkout identity is derived from git's own porcelain answer
	// (records[0].Path -- "the first record is always the main worktree
	// itself", per listWorktrees' doc comment), never from the caller-
	// supplied --repo/o.RepoRoot value: resolveMainRoot returns o.RepoRoot
	// VERBATIM when the --repo test hook is set, so trusting it here would
	// let a caller point --repo at a linked worktree to disguise the real
	// main checkout and bypass the "never attach the main checkout" guard
	// below. git's repo-wide porcelain answer cannot be spoofed this way.
	resolvedMainCheckout, err := filepath.EvalSymlinks(records[0].Path)
	if err != nil {
		return State{}, nil, fmt.Errorf("resolve main checkout root %s: %w", records[0].Path, err)
	}
	resolvedMainCheckout = filepath.Clean(resolvedMainCheckout)

	var match *worktreeRecord
	for i := range records {
		recPath, evalErr := filepath.EvalSymlinks(records[i].Path)
		if evalErr != nil {
			// A stale/removed registration can never be proven to match:
			// default-deny, fall through to ErrWorktreeNotFound.
			continue
		}
		if filepath.Clean(recPath) == resolvedAttach {
			match = &records[i]
			break
		}
	}
	if match == nil {
		return State{}, nil, fmt.Errorf(
			"%s is not a registered worktree of %s; use --slug to create a new one: %w",
			resolvedAttach, mainRoot, ErrWorktreeNotFound,
		)
	}

	if resolvedAttach == resolvedMainCheckout {
		return State{}, nil, fmt.Errorf(
			"refusing to attach the main checkout %s; attach a linked worktree instead: %w",
			resolvedAttach, ErrWorktreeNotAttachable,
		)
	}

	if match.Branch == "" {
		return State{}, nil, fmt.Errorf(
			"worktree %s is in detached HEAD; check out a branch first: %w",
			resolvedAttach, ErrWorktreeNotAttachable,
		)
	}

	if err := validateBranchName(match.Branch); err != nil {
		return State{}, nil, fmt.Errorf(
			"worktree %s's branch %q is not a safe name to record: %w",
			resolvedAttach, match.Branch, ErrWorktreeNotAttachable,
		)
	}

	// Best-effort read of any previously recorded worktree, purely to
	// decide whether the "replaced tracked worktree" warning applies -- a
	// read failure here is not fatal to the attach itself (SetArtifacts
	// below still proceeds and succeeds), but it is NOT ignorable: loadState
	// returns (State{Stage: StageNew}, nil) -- no error -- for the normal
	// "no state file yet" case, so a non-nil error here means something
	// genuinely went wrong (e.g. a corrupted/unreadable state file). Silently
	// treating that the same as "nothing was there" would hide evidence of a
	// real state-file problem and could also emit an incorrect "replaced
	// tracked worktree" warning off of existing's untrustworthy zero value --
	// so on error we surface an advisory warning instead and skip the
	// replacement comparison entirely.
	existing, existingErr := GetArtifacts(ArtifactOpts{ID: o.ID, RepoRoot: o.RepoRoot, StateDir: o.StateDir})
	var warnings []string
	if existingErr != nil {
		warnings = append(warnings, fmt.Sprintf("could not read prior pipeline state for %s: %v; proceeding", o.ID, existingErr))
	} else if existing.WorktreePath != "" && existing.WorktreePath != resolvedAttach {
		warnings = append(warnings, fmt.Sprintf("replaced tracked worktree %s with %s", existing.WorktreePath, resolvedAttach))
	}

	result, artErr := SetArtifacts(ArtifactOpts{
		ID:           o.ID,
		RepoRoot:     o.RepoRoot,
		StateDir:     o.StateDir,
		Branch:       match.Branch,
		WorktreePath: resolvedAttach,
	})
	if artErr != nil {
		return State{}, nil, fmt.Errorf("record attached worktree artifacts: %w", artErr)
	}
	return result, warnings, nil
}
