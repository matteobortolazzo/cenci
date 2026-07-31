// Package pipeline implements the `cenci pipeline <stage> <id>` engine
// (ticket #558): the state machine, structured output contract, per-repo
// state persistence, flock-based concurrency guard, and the deterministic
// retry policy backing prepare's `gh issue view` call. pipeline_cmd.go
// (package main) owns flag parsing, dispatch, and rendering; this package
// owns everything below that boundary.
package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Opts are the resolved inputs to one `cenci pipeline <stage> <id>`
// invocation. RepoRoot and StateDir are test/override hooks mirroring the
// CLI's --repo/--state-dir flags: StateDir, when set, is used verbatim as
// the directory holding <id>.json (bypassing repo-root resolution
// entirely); otherwise RepoRoot (or, if empty, the git repo root resolved
// from the working directory) anchors the canonical
// <repo>/.cenci/pipeline/<id>.json path.
type Opts struct {
	Stage    string
	ID       string
	Approve  bool
	RepoRoot string
	StateDir string
}

// Output is the structured {state, next_actions, artifacts, warnings,
// errors} contract every stage command emits on stdout. All four arrays are
// always non-nil (empty when there is nothing to report).
//
// Decision and Plan (ticket #560) are omitempty additions carried only by
// `cenci pipeline plan-check`'s output -- every existing stage/mechanics
// command leaves them absent, preserving the frozen #558 contract.
// DraftFreshness (#853) is the same shape: omitempty, populated only
// alongside plan-check's "awaiting-input" decision.
type Output struct {
	State          string    `json:"state"`
	NextActions    []string  `json:"next_actions"`
	Artifacts      []string  `json:"artifacts"`
	Warnings       []string  `json:"warnings"`
	Errors         []string  `json:"errors"`
	Decision       string    `json:"decision,omitempty"`
	Plan           *PlanMeta `json:"plan,omitempty"`
	DraftFreshness string    `json:"draft_freshness,omitempty"`
}

// Run executes one pipeline stage command: it acquires the per-ticket state
// lock, loads the current stage, (for prepare, on the first run) confirms
// the ticket exists via a retried `gh issue view`, (for `plan --approve`
// only, ticket #688) grants plan-file-triggered stage adoption when
// adoptPlanFileStage's default-deny gate is satisfied, applies the
// state-machine transition, persists the result, and returns the structured
// contract. A non-nil error always pairs with a fully-populated Output
// (state, next_actions, and errors[] all set) so callers can render the
// contract regardless of success or domain-level failure.
func Run(o Opts) (Output, error) {
	path, err := resolveStatePath(o)
	if err != nil {
		return errOutput(StageNew, "", err), err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		mkErr := fmt.Errorf("create state dir: %w", err)
		return errOutput(StageNew, path, mkErr), mkErr
	}
	lockPath := path + ".lock"

	var out Output
	lockErr := withLock(lockPath, defaultRetryConfig(), func() error {
		s, lerr := loadState(path)
		if lerr != nil {
			out = errOutput(StageNew, path, lerr)
			return lerr
		}

		if o.Stage == "prepare" && s.Stage == StageNew {
			if _, ghErr := ghIssueView(o.ID, defaultRetryConfig()); ghErr != nil {
				out = errOutput(s.Stage, path, ghErr)
				return ghErr
			}
		}

		// Plan-file-triggered stage adoption (ticket #688, closing #718
		// item 1): mutates s.Stage/s.PlanPath in memory, inside this same
		// lock acquisition, before the untouched transition() runs. See
		// adopt.go's doc comment for the full default-deny gate.
		var adoptedPath string
		var adopted bool
		oldStage := s.Stage
		if adoptedPath, adopted = adoptPlanFileStage(o, s); adopted {
			s.Stage = StageWaitingForPlanApproval
			if s.PlanPath == "" {
				s.PlanPath = adoptedPath
			}
		}

		next, noop, tErr := transition(s.Stage, o.Stage, o.Approve)
		if tErr != nil {
			out = errOutput(s.Stage, path, tErr)
			return tErr
		}

		warnings := []string{}
		if adopted {
			warnings = append(warnings, planAdoptionWarning(adoptedPath, oldStage))
		}
		if noop {
			warnings = append(warnings, noopWarning(next, o.Stage, o.Approve))
		}

		s.Stage = next
		s.ID = o.ID
		s.SchemaVersion = CurrentSchemaVersion
		s.UpdatedAt = time.Now().UTC()
		if serr := saveState(path, s); serr != nil {
			out = errOutput(next, path, serr)
			return serr
		}

		out = successOutput(next, path, warnings)
		return nil
	})

	if lockErr != nil {
		if out.State == "" {
			// The lock was never acquired (ErrLockContention): fn never ran,
			// so out was never populated. Best-effort peek at the current
			// on-disk stage (outside the lock, for reporting purposes only)
			// so the contract's state field isn't empty.
			cur, _ := loadState(path)
			out = errOutput(cur.Stage, path, lockErr)
		}
		return out, lockErr
	}
	return out, nil
}

// resolveStatePath validates o.ID and computes the state file path for this
// invocation, per Opts' StateDir/RepoRoot precedence.
func resolveStatePath(o Opts) (string, error) {
	if !idPattern.MatchString(o.ID) {
		return "", fmt.Errorf("invalid ticket id %q: must match ^\\d+$", o.ID)
	}
	if o.StateDir != "" {
		return filepath.Join(o.StateDir, o.ID+".json"), nil
	}
	repoRoot := o.RepoRoot
	if repoRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		repoRoot, err = resolveRepoRoot(cwd)
		if err != nil {
			return "", fmt.Errorf("resolve repo root: %w", err)
		}
	}
	return statePath(repoRoot, o.ID)
}

func successOutput(stage Stage, path string, warnings []string) Output {
	return Output{
		State:       string(stage),
		NextActions: nextActionsFor(stage),
		Artifacts:   []string{path},
		Warnings:    warnings,
		Errors:      []string{},
	}
}

func errOutput(stage Stage, path string, err error) Output {
	artifacts := []string{}
	if path != "" {
		artifacts = []string{path}
	}
	return Output{
		State:       string(stage),
		NextActions: nextActionsFor(stage),
		Artifacts:   artifacts,
		Warnings:    []string{},
		Errors:      []string{err.Error()},
	}
}
