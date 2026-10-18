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

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
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

// The State.Session keys that record the sandbox launch a ticket's `working`
// claim ran in (#1292): the launch ID, the container it lives in and the
// runtime that runs it. They are written by `pipeline label --transition
// working` (labels.go) and removed by ClearLaunch (the dispatcher, before it
// spawns a new session). The session map already round-trips and merges key by
// key, so these need no CurrentSchemaVersion bump; the existing keys (runId,
// reviewPath) do not collide. The values are agent-writable (.cenci/pipeline/
// lives in the bind-mounted repo), so every reader must re-check them with
// ValidLaunchTriple before use.
const (
	SessionKeyLaunchID  = "launchId"
	SessionKeyContainer = "container"
	SessionKeyRuntime   = "runtime"
)

// ValidLaunchTriple reports whether id, container and runtime together form a
// recordable launch: a well-formed launch ID, a sandbox container name within
// the runtime's name grammar, and a runtime in the closed set docker/podman.
// It is the one check shared by the write side (labels.go) and any consumer
// that reads the triple back before it reaches an exec argv.
func ValidLaunchTriple(id, container, runtime string) bool {
	return sandbox.ValidLaunchID(id) && sandbox.ValidSandboxContainerName(container) && sandbox.ValidLaunchRuntime(runtime)
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

// saveState writes s to path atomically: marshal, write to a fresh sibling
// temp file (exclusive create, random name), then os.Rename over the target so
// a reader never observes a partially-written file.
func saveState(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	// The state dir lives in the agent-writable bind-mounted repo, so the temp
	// file must be a fresh O_EXCL create under an unpredictable name: a fixed
	// "<path>.tmp" could be a planted symlink that os.WriteFile would follow.
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod temp state file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
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

// ClearLaunch removes the recorded launch triple (SessionKeyLaunchID,
// SessionKeyContainer, SessionKeyRuntime) from the ticket's persisted
// session, keeping every other session key, under the same per-ticket flock the
// other state writers take (#1292). The dispatcher calls it before spawning a
// session, so the new session starts on the window-name rule until its own
// `working` claim records the new launch.
//
// A missing state file is the normal "nothing recorded yet" case: it is a
// no-op that creates neither the file, its directory nor the lock file. A file
// that carries none of the three keys is left byte-for-byte as it is (no
// write). An unreadable or corrupt file is an error, so a caller never
// proceeds believing a stale launch was cleared.
//
// Because .cenci/ lives in the agent-writable bind-mounted repo, a symlinked
// .cenci or .cenci/pipeline directory would redirect the host-side lock, read,
// temp file and rename outside the repo. checkStateDirContained refuses both
// (and any state directory that resolves outside the repo root) before the
// lock is taken. This closes the static symlink case only: a swap that races
// the check (TOCTOU) is a known residual, tracked as a followup together with
// a repo-wide os.Root migration of the other state writers.
func ClearLaunch(o ArtifactOpts) error {
	path, err := resolveStatePath(Opts{ID: o.ID, RepoRoot: o.RepoRoot, StateDir: o.StateDir})
	if err != nil {
		return err
	}
	// Lstat, not Stat: the state file is agent-writable, so a planted symlink
	// (or directory, device) at the path is refused rather than read or
	// rewritten through by this host-side writer.
	info, statErr := os.Lstat(path)
	if os.IsNotExist(statErr) {
		return nil
	}
	if statErr != nil {
		return fmt.Errorf("stat state file %s: %w", path, statErr)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("state file %s is not a regular file (mode %s), refusing to clear its launch", path, info.Mode().Type())
	}
	if err := checkStateDirContained(path, o.StateDir == ""); err != nil {
		return err
	}
	return withLock(path+".lock", defaultRetryConfig(), func() error {
		s, lerr := loadState(path)
		if lerr != nil {
			return lerr
		}
		if !deleteLaunchKeys(&s) {
			return nil
		}
		s.UpdatedAt = time.Now().UTC()
		return saveState(path, s)
	})
}

// checkStateDirContained refuses a state directory that an agent-planted
// symlink could redirect outside the repo. It Lstat-checks the directory that
// holds the state file and, when the path is the canonical
// <repoRoot>/.cenci/pipeline/<id>.json (repoLayout), the .cenci directory too:
// each must be a real directory, not a symlink. It then compares the fully
// resolved state directory against the resolved repo root and refuses unless it
// is inside. An explicit StateDir override has no repo root, so only the
// state directory's own Lstat check applies. The errors name paths only, never
// recorded values.
func checkStateDirContained(stateFile string, repoLayout bool) error {
	stateDir := filepath.Dir(stateFile)
	dirs := []string{stateDir}
	repoRoot := ""
	if repoLayout {
		cenciDir := filepath.Dir(stateDir)
		repoRoot = filepath.Dir(cenciDir)
		dirs = []string{cenciDir, stateDir}
	}
	for _, d := range dirs {
		info, err := os.Lstat(d)
		if err != nil {
			return fmt.Errorf("stat state directory %s: %w", d, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("state directory %s is not a plain directory (mode %s), refusing to clear its launch", d, info.Mode().Type())
		}
	}
	if repoRoot == "" {
		return nil
	}
	resolvedRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return fmt.Errorf("resolve repo root %s: %w", repoRoot, err)
	}
	resolvedState, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		return fmt.Errorf("resolve state directory %s: %w", stateDir, err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedState)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("state directory %s resolves outside the repo root %s, refusing to clear its launch", stateDir, repoRoot)
	}
	return nil
}

// deleteLaunchKeys removes the three launch keys from s.Session and reports
// whether any was present. An emptied session is set back to nil so omitempty
// drops it from the file.
func deleteLaunchKeys(s *State) bool {
	removed := false
	for _, k := range []string{SessionKeyLaunchID, SessionKeyContainer, SessionKeyRuntime} {
		if _, ok := s.Session[k]; ok {
			delete(s.Session, k)
			removed = true
		}
	}
	if len(s.Session) == 0 {
		s.Session = nil
	}
	return removed
}
