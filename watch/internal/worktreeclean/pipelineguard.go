package worktreeclean

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// maxPipelineStateBytes bounds the read of a pipeline state file: a real one is
// well under a kilobyte, and the file sits in a tree a sandboxed agent can write.
// A package var so tests can shrink it.
var maxPipelineStateBytes = 1 << 20

// stageFinalized is a local copy of internal/pipeline's final stage name (this
// leaf package must not import it); a test pins it to pipeline.StageFinalized.
// Any other stage string, known or not, reads as "not finalized", which keeps
// the worktree, so no list of stages has to stay in sync with the pipeline.
const stageFinalized = "finalized"

// pipelineMidStage reports whether the ticket's pipeline run is mid-stage: the
// state file <MainRoot>/.cenci/pipeline/<id>.json exists, its stage is not
// "finalized" and its updatedAt is no more than staleAfter old. A future-dated
// updatedAt counts as mid-stage (it is never "old"). A missing file or
// directory means nothing is running. Anything that cannot be trusted (a
// symlink, FIFO or oversized file, malformed JSON, a missing, null, empty or
// zero stage or updatedAt) is an error, so the caller keeps the worktree.
//
// Only stage and updatedAt are decoded, through a map so keys match exactly
// (encoding/json would match "Stage" to a "stage" tag, go-gotchas #851).
func pipelineMidStage(repo Repo, id string, staleAfter time.Duration, now time.Time) (bool, error) {
	if !validComponent(id) {
		return false, fmt.Errorf("worktreeclean: invalid pipeline id %q", id)
	}
	dir := repo.MainRoot
	for _, part := range []string{".cenci", "pipeline"} {
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return false, nil
		case err != nil:
			return false, fmt.Errorf("stat %s: %w", dir, err)
		case !info.IsDir():
			return false, fmt.Errorf("%s is not a plain directory (mode %s)", dir, info.Mode().Type())
		}
	}
	path := filepath.Join(dir, id+".json")
	data, err := readPipelineState(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return false, fmt.Errorf("decode %s: %w", path, err)
	}
	var stage string
	if err := json.Unmarshal(fields["stage"], &stage); err != nil || stage == "" {
		return false, fmt.Errorf("%s has no usable stage", path)
	}
	var updatedAt time.Time
	if err := json.Unmarshal(fields["updatedAt"], &updatedAt); err != nil || updatedAt.IsZero() {
		return false, fmt.Errorf("%s has no usable updatedAt", path)
	}
	if stage == stageFinalized {
		return false, nil
	}
	return now.Sub(updatedAt) <= staleAfter, nil
}

// readPipelineState reads a state file the way readPointerFile reads a pointer
// file: a symlink is refused by the open itself (O_NOFOLLOW), the open is
// non-blocking (a FIFO would otherwise block until a writer appears), the type
// is checked on the open handle and the read is capped (go-gotchas #1250). A
// missing file is returned as fs.ErrNotExist.
func readPipelineState(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("%s is a symlink: %w", path, err)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only handle: a close error cannot lose data

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (mode %s)", path, st.Mode().Type())
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(maxPipelineStateBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPipelineStateBytes {
		return nil, fmt.Errorf("%s is larger than the %d-byte limit", path, maxPipelineStateBytes)
	}
	return data, nil
}
