package pipeline

// Artifact tracking (ticket #559): set/get the v2 State fields (PlanPath,
// Branch, WorktreePath, PRURL, PRNumber, Session) for one ticket, under the
// same per-ticket flock lock pipeline.Run uses (see lock.go's withLock),
// backing the `cenci pipeline artifact <id>` mechanics verb.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ArtifactOpts are the resolved inputs to one artifact get/set invocation.
// RepoRoot/StateDir mirror pipeline.Opts' own precedence (StateDir, when
// set, is used verbatim; otherwise RepoRoot, or the git repo root resolved
// from the working directory, anchors the canonical
// <repo>/.cenci/pipeline/<id>.json path).
//
// Every other field is "set if non-zero, leave unchanged otherwise" for
// SetArtifacts, EXCEPT Session, which always merges into the existing map
// rather than replacing it (an empty Session leaves the persisted map
// untouched; a non-empty Session merges key by key, never clobbering keys
// it doesn't mention). PRNumberSet distinguishes "set PRNumber to 0"
// (impossible for a real PR number, but kept explicit rather than
// overloading the zero value) from "PRNumber not provided this call".
type ArtifactOpts struct {
	ID       string
	RepoRoot string
	StateDir string

	PlanPath     string
	Branch       string
	WorktreePath string
	PRURL        string
	PRNumber     int
	PRNumberSet  bool
	Session      map[string]string
}

// SetArtifacts applies the non-zero fields in o to the persisted state for
// o.ID, acquiring the same per-ticket flock lock pipeline.Run uses, and
// returns the resulting State.
func SetArtifacts(o ArtifactOpts) (State, error) {
	path, err := resolveStatePath(Opts{ID: o.ID, RepoRoot: o.RepoRoot, StateDir: o.StateDir})
	if err != nil {
		return State{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return State{}, fmt.Errorf("create state dir: %w", err)
	}

	var result State
	lockErr := withLock(path+".lock", defaultRetryConfig(), func() error {
		s, lerr := loadState(path)
		if lerr != nil {
			return lerr
		}

		if o.PlanPath != "" {
			s.PlanPath = o.PlanPath
		}
		if o.Branch != "" {
			s.Branch = o.Branch
		}
		if o.WorktreePath != "" {
			s.WorktreePath = o.WorktreePath
		}
		if o.PRURL != "" {
			s.PRURL = o.PRURL
		}
		if o.PRNumberSet {
			s.PRNumber = o.PRNumber
		}
		if len(o.Session) > 0 {
			if s.Session == nil {
				s.Session = map[string]string{}
			}
			for k, v := range o.Session {
				s.Session[k] = v
			}
		}

		s.ID = o.ID
		s.SchemaVersion = CurrentSchemaVersion
		s.UpdatedAt = time.Now().UTC()
		if serr := saveState(path, s); serr != nil {
			return serr
		}
		result = s
		return nil
	})
	if lockErr != nil {
		return State{}, lockErr
	}
	return result, nil
}

// GetArtifacts loads and returns the current State for o.ID (RepoRoot/
// StateDir precedence only; the other ArtifactOpts fields are ignored). It
// does not acquire the per-ticket lock -- a point-in-time read.
func GetArtifacts(o ArtifactOpts) (State, error) {
	path, err := resolveStatePath(Opts{ID: o.ID, RepoRoot: o.RepoRoot, StateDir: o.StateDir})
	if err != nil {
		return State{}, err
	}
	return loadState(path)
}
