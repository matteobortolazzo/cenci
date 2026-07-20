package main

// Deterministic pipeline mechanics verbs (ticket #559): `cenci pipeline
// label|worktree|worktree-cleanup|artifact <id> [flags]`, dispatched from
// runPipeline (pipeline_cmd.go) alongside the existing five stage
// transitions. Each verb parses and validates its own flag set (usage
// errors exit 2 with a one-line stderr hint naming that verb specifically,
// per docs/cli-conventions.md), dispatches into internal/pipeline, and
// renders the same {state, next_actions, artifacts, warnings, errors}
// contract the stage commands do via renderMechanicsOutput.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/pipeline"
)

const (
	pipelineLabelUsage = "cenci pipeline label: usage: cenci pipeline label <id> --transition working|planned|in-review " +
		"[--trivial] [--parent N] [--repo-slug OWNER/REPO] [--state-dir DIR] [--repo PATH]"
	pipelineWorktreeUsage = "cenci pipeline worktree: usage: cenci pipeline worktree <id> --slug SLUG " +
		"[--state-dir DIR] [--repo PATH]"
	pipelineWorktreeCleanupUsage = "cenci pipeline worktree-cleanup: usage: cenci pipeline worktree-cleanup <id> --slug SLUG " +
		"[--state-dir DIR] [--repo PATH]"
	pipelineArtifactUsage = "cenci pipeline artifact: usage: cenci pipeline artifact <id> [--get] [--plan PATH] " +
		"[--branch BRANCH] [--pr URL] [--pr-number N] [--session KEY=VALUE ...] [--state-dir DIR] [--repo PATH]"
)

func pipelineLabelUsageExit() {
	fmt.Fprintln(os.Stderr, pipelineLabelUsage)
	os.Exit(2)
}

func pipelineWorktreeUsageExit() {
	fmt.Fprintln(os.Stderr, pipelineWorktreeUsage)
	os.Exit(2)
}

func pipelineWorktreeCleanupUsageExit() {
	fmt.Fprintln(os.Stderr, pipelineWorktreeCleanupUsage)
	os.Exit(2)
}

func pipelineArtifactUsageExit() {
	fmt.Fprintln(os.Stderr, pipelineArtifactUsage)
	os.Exit(2)
}

// sessionFlag implements flag.Value for a repeatable `--session KEY=VALUE`
// flag, accumulating into a map. A value without a non-empty KEY= prefix is
// malformed CLI input (rejected by Set, which flag.Parse then surfaces as a
// parse error), not a domain error.
type sessionFlag struct {
	m map[string]string
}

func (s *sessionFlag) String() string { return "" }

func (s *sessionFlag) Set(v string) error {
	idx := strings.Index(v, "=")
	if idx <= 0 {
		return fmt.Errorf("malformed --session value %q: want KEY=VALUE", v)
	}
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[v[:idx]] = v[idx+1:]
	return nil
}

// mechanicsID parses and validates the mandatory leading <id> positional
// for a mechanics verb, calling usageExit (without returning) on any
// malformed-CLI case: missing id, id starting with "-" (a flag mistaken for
// the id), or an id that fails ^\d+$.
func mechanicsID(rest []string, usageExit func()) string {
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		usageExit()
	}
	id := rest[0]
	if !pipelineIDPattern.MatchString(id) {
		usageExit()
	}
	return id
}

// renderMechanicsOutput renders one mechanics verb's {state, next_actions,
// artifacts, warnings, errors} contract to stdout and exits 1 on a domain
// error (err != nil), mirroring runPipeline's own render step. When err is
// non-nil and state carries no stage (the mechanics call bailed out before
// loading/returning any State), it best-effort re-reads the persisted state
// for a more informative `state` field, exactly as pipeline.Run's own
// lock-contention fallback does.
func renderMechanicsOutput(id, repoRoot, stateDir string, state pipeline.State, artifacts []string, err error) {
	if err != nil && state.Stage == "" {
		if fallback, gerr := pipeline.GetArtifacts(pipeline.ArtifactOpts{ID: id, RepoRoot: repoRoot, StateDir: stateDir}); gerr == nil {
			state = fallback
		}
	}

	out := pipeline.Output{
		State:       string(state.Stage),
		NextActions: pipeline.NextActionsFor(state.Stage),
		Artifacts:   artifacts,
		Warnings:    []string{},
		Errors:      []string{},
	}
	if err != nil {
		out.Errors = []string{err.Error()}
	}

	b, marshalErr := json.Marshal(out)
	if marshalErr != nil {
		fmt.Fprintf(os.Stderr, "cenci pipeline: %v\n", marshalErr)
		os.Exit(1)
	}
	fmt.Println(string(b))

	if err != nil {
		os.Exit(1)
	}
}

// runPipelineLabel implements `cenci pipeline label <id> --transition
// working|planned|in-review [--trivial] [--parent N] [--repo-slug O/R]
// [--state-dir DIR] [--repo PATH]`.
func runPipelineLabel(rest []string) {
	id := mechanicsID(rest, pipelineLabelUsageExit)

	fs := flag.NewFlagSet("pipeline label", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	transition := fs.String("transition", "", "label transition: working|planned|in-review")
	trivial := fs.Bool("trivial", false, "planned transition only: keep Working instead of removing it")
	parent := fs.String("parent", "", "in-review transition only: cascade the label to this parent ticket id")
	repoSlug := fs.String("repo-slug", "", "override the resolved owner/repo gh target (test hook)")
	stateDir := fs.String("state-dir", "", "override the pipeline state directory (test hook)")
	repo := fs.String("repo", "", "override the resolved repo root (test hook)")
	if err := fs.Parse(rest[1:]); err != nil {
		pipelineLabelUsageExit()
	}
	if len(fs.Args()) > 0 {
		pipelineLabelUsageExit()
	}
	switch *transition {
	case "working", "planned", "in-review":
	default:
		pipelineLabelUsageExit()
	}

	// --trivial is only meaningful for --transition planned, and --parent
	// only for --transition in-review; silently accepting either on a
	// mismatched transition would be the same "silent precedence"
	// anti-pattern docs/cli-conventions.md forbids for conflicting inputs.
	trivialSet, parentSet := false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "trivial":
			trivialSet = true
		case "parent":
			parentSet = true
		}
	})
	if trivialSet && *transition != "planned" {
		pipelineLabelUsageExit()
	}
	if parentSet && *transition != "in-review" {
		pipelineLabelUsageExit()
	}
	if *parent != "" && !pipelineIDPattern.MatchString(*parent) {
		pipelineLabelUsageExit()
	}

	s, err := pipeline.ApplyLabelTransition(pipeline.LabelOpts{
		ID:         id,
		RepoRoot:   *repo,
		StateDir:   *stateDir,
		RepoSlug:   *repoSlug,
		Transition: *transition,
		Trivial:    *trivial,
		ParentID:   *parent,
	})
	renderMechanicsOutput(id, *repo, *stateDir, s, []string{}, err)
}

// runPipelineWorktree implements `cenci pipeline worktree <id> --slug SLUG
// [--state-dir DIR] [--repo PATH]`.
func runPipelineWorktree(rest []string) {
	id := mechanicsID(rest, pipelineWorktreeUsageExit)

	fs := flag.NewFlagSet("pipeline worktree", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	slug := fs.String("slug", "", "worktree/branch slug (required)")
	stateDir := fs.String("state-dir", "", "override the pipeline state directory (test hook)")
	repo := fs.String("repo", "", "override the resolved repo root (test hook)")
	if err := fs.Parse(rest[1:]); err != nil {
		pipelineWorktreeUsageExit()
	}
	if len(fs.Args()) > 0 {
		pipelineWorktreeUsageExit()
	}
	if *slug == "" {
		pipelineWorktreeUsageExit()
	}

	s, err := pipeline.CreateWorktree(pipeline.WorktreeOpts{ID: id, Slug: *slug, RepoRoot: *repo, StateDir: *stateDir})
	artifacts := []string{}
	if s.WorktreePath != "" {
		artifacts = []string{s.WorktreePath}
	}
	renderMechanicsOutput(id, *repo, *stateDir, s, artifacts, err)
}

// runPipelineWorktreeCleanup implements `cenci pipeline worktree-cleanup
// <id> --slug SLUG [--state-dir DIR] [--repo PATH]`.
func runPipelineWorktreeCleanup(rest []string) {
	id := mechanicsID(rest, pipelineWorktreeCleanupUsageExit)

	fs := flag.NewFlagSet("pipeline worktree-cleanup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	slug := fs.String("slug", "", "worktree/branch slug (required)")
	stateDir := fs.String("state-dir", "", "override the pipeline state directory (test hook)")
	repo := fs.String("repo", "", "override the resolved repo root (test hook)")
	if err := fs.Parse(rest[1:]); err != nil {
		pipelineWorktreeCleanupUsageExit()
	}
	if len(fs.Args()) > 0 {
		pipelineWorktreeCleanupUsageExit()
	}
	if *slug == "" {
		pipelineWorktreeCleanupUsageExit()
	}

	err := pipeline.CleanupWorktree(pipeline.WorktreeOpts{ID: id, Slug: *slug, RepoRoot: *repo, StateDir: *stateDir})
	var s pipeline.State
	if err == nil {
		if got, gerr := pipeline.GetArtifacts(pipeline.ArtifactOpts{ID: id, RepoRoot: *repo, StateDir: *stateDir}); gerr == nil {
			s = got
		}
	}
	renderMechanicsOutput(id, *repo, *stateDir, s, []string{}, err)
}

// runPipelineArtifact implements `cenci pipeline artifact <id> [--get]
// [--plan PATH] [--branch BRANCH] [--pr URL] [--pr-number N] [--session
// KEY=VALUE ...] [--state-dir DIR] [--repo PATH]`. Without --get it sets the
// non-zero fields provided; with --get it is a read-only point-in-time
// fetch and every other flag is ignored.
func runPipelineArtifact(rest []string) {
	id := mechanicsID(rest, pipelineArtifactUsageExit)

	fs := flag.NewFlagSet("pipeline artifact", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	get := fs.Bool("get", false, "read the current artifacts instead of setting them")
	plan := fs.String("plan", "", "set PlanPath")
	branch := fs.String("branch", "", "set Branch")
	prURL := fs.String("pr", "", "set PRURL")
	prNumber := fs.Int("pr-number", 0, "set PRNumber")
	var sess sessionFlag
	fs.Var(&sess, "session", "set/merge session metadata KEY=VALUE (repeatable)")
	stateDir := fs.String("state-dir", "", "override the pipeline state directory (test hook)")
	repo := fs.String("repo", "", "override the resolved repo root (test hook)")
	if err := fs.Parse(rest[1:]); err != nil {
		pipelineArtifactUsageExit()
	}
	if len(fs.Args()) > 0 {
		pipelineArtifactUsageExit()
	}

	if *get {
		// --get is a read-only point-in-time fetch: combining it with any
		// mutation flag is a conflicting input, which
		// docs/cli-conventions.md requires to be rejected outright rather
		// than silently ignored.
		mutationFlagSet := false
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "plan", "branch", "pr", "pr-number", "session":
				mutationFlagSet = true
			}
		})
		if mutationFlagSet {
			pipelineArtifactUsageExit()
		}
		s, err := pipeline.GetArtifacts(pipeline.ArtifactOpts{ID: id, RepoRoot: *repo, StateDir: *stateDir})
		renderMechanicsOutput(id, *repo, *stateDir, s, []string{}, err)
		return
	}

	prNumberSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "pr-number" {
			prNumberSet = true
		}
	})

	s, err := pipeline.SetArtifacts(pipeline.ArtifactOpts{
		ID:          id,
		RepoRoot:    *repo,
		StateDir:    *stateDir,
		PlanPath:    *plan,
		Branch:      *branch,
		PRURL:       *prURL,
		PRNumber:    *prNumber,
		PRNumberSet: prNumberSet,
		Session:     sess.m,
	})
	renderMechanicsOutput(id, *repo, *stateDir, s, []string{}, err)
}
