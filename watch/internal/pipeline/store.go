package pipeline

// Pipeline run state persistence (ticket #558): repo-root resolution
// (ticket #559: anchored on the MAIN checkout root via `git rev-parse
// --path-format=absolute --git-common-dir` + filepath.Dir, which diverges
// from internal/sandbox/launcher/scope.go's ResolveRepoRoot — see
// resolveRepoRoot's own doc comment below for why), the canonical
// <repo>/.cenci/pipeline/<id>.json path (with <id> validated against ^\d+$
// before it ever reaches a path), and atomic load/save (mirrors
// internal/babysit's State/load/save: .tmp + os.Rename, MarshalIndent,
// MkdirAll).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// CurrentSchemaVersion tags the on-disk state format (mirrors babysit's
// SchemaVersion), giving a future format change something to gate on.
//
// Bumped to 2 (ticket #559) now that the v2 fields below (PlanPath, Branch,
// WorktreePath, PRURL, PRNumber, Labels, Session) are wired up by the
// mechanics verbs (artifact/worktree/label).
const CurrentSchemaVersion = 2

// State is the persisted pipeline run record for one ticket.
//
// The fields below PlanPath are v2 additions (ticket #559: deterministic
// pipeline mechanics — artifact tracking, worktree lifecycle, label
// lifecycle). All are omitempty so a v1 state file (schemaVersion=1, none
// of these fields present) still round-trips through loadState with them
// defaulting to their zero values.
type State struct {
	SchemaVersion int       `json:"schemaVersion"`
	ID            string    `json:"id"`
	Stage         Stage     `json:"stage"`
	UpdatedAt     time.Time `json:"updatedAt"`

	PlanPath     string            `json:"planPath,omitempty"`
	Branch       string            `json:"branch,omitempty"`
	WorktreePath string            `json:"worktreePath,omitempty"`
	PRURL        string            `json:"prUrl,omitempty"`
	PRNumber     int               `json:"prNumber,omitempty"`
	Labels       []string          `json:"labels,omitempty"`
	Session      map[string]string `json:"session,omitempty"`

	// TicketUpdatedAt is the ticket's GitHub updatedAt (RFC3339, verbatim
	// from gh) as observed immediately after the pipeline's own most recent
	// `gh issue edit` label transition (#669). CheckPlan's freshness verdict
	// treats the ticket as user-edited only when its updatedAt is after
	// BOTH the plan's createdAt and this baseline — without it, the label
	// swap that follows every plan persist would mark the plan stale.
	// Stored as a string (not time.Time) so an absent baseline round-trips
	// as "" under omitempty.
	TicketUpdatedAt string `json:"ticketUpdatedAt,omitempty"`
}

// idPattern is the validation gate every <id> must pass before it is used to
// build a filesystem path, guarding against path traversal or command
// injection via a malformed ticket id.
var idPattern = regexp.MustCompile(`^\d+$`)

// statePath returns the canonical on-disk path for a ticket's pipeline state
// under repoRoot, after validating id against ^\d+$. It never constructs a
// path from an invalid id.
func statePath(repoRoot, id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("invalid ticket id %q: must match ^\\d+$", id)
	}
	return filepath.Join(repoRoot, ".cenci", "pipeline", id+".json"), nil
}

// resolveRepoRoot returns the absolute root of the MAIN checkout of the git
// repo containing cwd, or an error when cwd isn't inside a git repo.
//
// This resolves via `git rev-parse --path-format=absolute --git-common-dir`
// + filepath.Dir, NOT `--show-toplevel` (ticket #559): the common-dir path
// (<main-repo>/.git) is shared by the main checkout and every linked
// worktree it owns, so filepath.Dir of it always yields the main checkout's
// root — even when cwd is inside a linked worktree (e.g. review/finalize
// running inside .worktrees/<id>-<slug>). `--show-toplevel` would instead
// resolve to that linked worktree's own root, breaking cross-worktree state
// continuity, which is exactly the gap this ticket closes. See
// TestResolveRepoRoot_FromLinkedWorktree_ReturnsMainCheckoutRoot in
// store_test.go.
//
// This behavior now DIVERGES from internal/sandbox/launcher/scope.go's
// ResolveRepoRoot, which intentionally stays on `--show-toplevel` for
// launcher/sandbox scoping (worktree-local by design) — that is not a bug,
// it is a different, correct answer to a different question. Do not
// "fix" scope.go to match this function or vice versa.
func resolveRepoRoot(cwd string) (string, error) {
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", fmt.Errorf("resolve repo root from %s: %w", cwd, err)
	}
	commonDir := strings.TrimSpace(string(out))
	return filepath.Dir(commonDir), nil
}

// saveState writes s to path atomically: marshal, write to a sibling .tmp
// file, then os.Rename over the target so a reader never observes a
// partially-written file.
func saveState(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename temp state file: %w", err)
	}
	return nil
}

// loadState reads the state at path. A missing file is tolerated (mirrors
// babysit's load): it is the normal "no pipeline run yet" case, returned as
// StageNew with no error, since the very first `cenci pipeline prepare <id>`
// run for a ticket must not fail just because no state file exists yet.
func loadState(path string) (State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{Stage: StageNew}, nil
		}
		return State{}, fmt.Errorf("read state file: %w", err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("decode state file: %w", err)
	}
	return s, nil
}
