package pipeline

// Pipeline run state persistence (ticket #558): repo-root resolution
// (mirrors internal/sandbox/launcher/scope.go's ResolveRepoRoot), the
// canonical <repo>/.cenci/pipeline/<id>.json path (with <id> validated
// against ^\d+$ before it ever reaches a path), and atomic load/save
// (mirrors internal/babysit's State/load/save: .tmp + os.Rename,
// MarshalIndent, MkdirAll).

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
const CurrentSchemaVersion = 1

// State is the persisted pipeline run record for one ticket.
type State struct {
	SchemaVersion int       `json:"schemaVersion"`
	ID            string    `json:"id"`
	Stage         Stage     `json:"stage"`
	UpdatedAt     time.Time `json:"updatedAt"`
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

// resolveRepoRoot returns the absolute root of the git repo containing cwd,
// or an error when cwd isn't inside a git repo (mirrors
// internal/sandbox/launcher/scope.go's ResolveRepoRoot).
func resolveRepoRoot(cwd string) (string, error) {
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("resolve repo root from %s: %w", cwd, err)
	}
	return strings.TrimSpace(string(out)), nil
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
