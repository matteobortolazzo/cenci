package pipeline

// Pipeline run state persistence (ticket #558): repo-root resolution
// (ticket #559: anchored on the MAIN checkout root via `git rev-parse
// --path-format=absolute --git-common-dir` + filepath.Dir, which diverges
// from internal/sandbox/launcher/scope.go's ResolveRepoRoot — see
// resolveRepoRoot's own doc comment below for why), the canonical
// <repo>/.cenci/pipeline/<id>.json path (with <id> validated against ^\d+$
// before it ever reaches a path), and the State format. The atomic load/save
// of that format (temp file, rename, MarshalIndent) lives in stateroot.go,
// which scopes every file operation to an os.Root on the state directory.

import (
	"errors"
	"fmt"
	"io/fs"
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
// Because .cenci/ lives in the agent-writable bind-mounted repo, the state is
// reached only through a stateDir (stateroot.go): a symlinked .cenci,
// .cenci/pipeline or --state-dir, a symlinked, FIFO or oversized state file and
// a symlinked lock file are all refused, and a directory swapped after the walk
// cannot redirect the lock, read, temp file or rename outside the repo.
func ClearLaunch(o ArtifactOpts) error {
	loc, err := resolveStateLocation(Opts{ID: o.ID, RepoRoot: o.RepoRoot, StateDir: o.StateDir})
	if err != nil {
		return err
	}
	d, err := openStateDir(loc, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	// Checked before the lock so a missing file creates no lock file, and a
	// planted symlink (or directory, device, FIFO) is refused rather than read
	// or rewritten through by this host-side writer.
	if _, err := d.lstatState(); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("%w, refusing to clear its launch", err)
	}
	return withLock(d, defaultRetryConfig(), func() error {
		s, lerr := d.load()
		if lerr != nil {
			return lerr
		}
		if !deleteLaunchKeys(&s) {
			return nil
		}
		s.UpdatedAt = time.Now().UTC()
		return d.save(s)
	})
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
