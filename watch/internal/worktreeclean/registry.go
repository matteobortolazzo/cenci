package worktreeclean

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
)

// maxRegistryFileBytes caps how much of the registry file and of the user config is
// read. Real ones are a few hundred bytes; the cap only bounds a hostile or runaway
// file.
const maxRegistryFileBytes = 64 << 10

// registrySchemaVersion is the only registry format this code reads and writes.
const registrySchemaVersion = 1

// RegistrySource says where a registry problem came from, so a caller can decide
// how loudly to report it.
type RegistrySource string

const (
	// RegistrySourceFile: the registry file itself (unreadable, corrupt, not
	// writable); Dir is the file's path.
	RegistrySourceFile RegistrySource = "registry"
	// RegistrySourceDispatch: a dispatch.repos[].dir value that failed validation.
	RegistrySourceDispatch RegistrySource = "dispatch"
	// RegistrySourceWindow: a window's @cenci-repo-dir that failed validation.
	RegistrySourceWindow RegistrySource = "window"
	// RegistrySourceEntry: an entry read back from the registry that could not be
	// examined this pass (it stays registered).
	RegistrySourceEntry RegistrySource = "entry"
)

// RegistryProblem is one loggable thing UpdateRegistry could not do. Dir is the
// offending directory. Err may quote a remote URL with its credentials redacted
// (a failed origin parse), so a caller's logs must not include it for a candidate
// source.
type RegistryProblem struct {
	Source RegistrySource
	Dir    string
	Err    error
}

// RegistryUpdate is the input of UpdateRegistry.
type RegistryUpdate struct {
	// Path is the registry file.
	Path string
	// DispatchDirs and WindowDirs are the candidate directories to register.
	DispatchDirs, WindowDirs []string
	// Resolve validates a candidate and returns the main-checkout root to register
	// for it; nil uses the production resolver (ticketstate.Resolver, then
	// ResolveRepo).
	Resolve func(ctx context.Context, dir string) (mainRoot string, err error)
}

// DefaultRegistryPath is $XDG_STATE_HOME/cenci/worktree-repos.json, falling back
// to ~/.local/state (also when $XDG_STATE_HOME is relative, which the XDG spec says
// to ignore); empty when no home directory can be found.
func DefaultRegistryPath() string {
	return xdgPath("XDG_STATE_HOME", filepath.Join(".local", "state"), "worktree-repos.json")
}

// DefaultUserConfigPath is $XDG_CONFIG_HOME/cenci/config.json, falling back to
// ~/.config (also when $XDG_CONFIG_HOME is relative); empty when no home directory
// can be found.
func DefaultUserConfigPath() string {
	return xdgPath("XDG_CONFIG_HOME", ".config", "config.json")
}

// xdgPath is <$env or ~/<homeRel>>/cenci/<file>. A relative $env is ignored.
func xdgPath(env, homeRel, file string) string {
	dir := os.Getenv(env)
	if !filepath.IsAbs(dir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, homeRel)
	}
	return filepath.Join(dir, "cenci", file)
}

// LoadRegistry reads the registered repo directories. A missing file is an empty
// registry; entries that are not absolute, clean paths free of NUL and newline
// are dropped. A file that is unreadable, too large, not a regular file, not JSON
// or of another schema version is an error.
func LoadRegistry(path string) ([]string, error) {
	data, err := readBoundedRegularFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	var version int
	if err := json.Unmarshal(top["schemaVersion"], &version); err != nil || version != registrySchemaVersion {
		return nil, fmt.Errorf("parsing %s: schemaVersion must be %d", path, registrySchemaVersion)
	}
	var repos []string
	if err := json.Unmarshal(top["repos"], &repos); err != nil {
		return nil, fmt.Errorf("parsing %s: repos must be a list of paths: %w", path, err)
	}
	return slices.DeleteFunc(repos, func(dir string) bool { return !validRegistryDir(dir) }), nil
}

// validRegistryDir reports whether dir is an absolute, clean path free of NUL and
// newline.
func validRegistryDir(dir string) bool {
	return filepath.IsAbs(dir) && filepath.Clean(dir) == dir && !strings.ContainsAny(dir, "\x00\n")
}

// errNoRegistryPath is the fixed reason for a registry path that is empty (no home
// directory) or relative: persisting it would write into the working directory.
var errNoRegistryPath = errors.New("the registry path is empty or not absolute, so the registry is not persisted")

// SaveRegistry writes dirs, sorted and de-duplicated, atomically: a temp file in
// the registry's directory is renamed over the file, so a reader sees the old or
// the new content, never a partial one.
func SaveRegistry(path string, dirs []string) error {
	if !filepath.IsAbs(path) {
		return errNoRegistryPath
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating registry dir %s: %w", dir, err)
	}
	repos := slices.Clone(dirs)
	slices.Sort(repos)
	repos = slices.Compact(repos)
	if repos == nil {
		repos = []string{}
	}
	data, err := json.MarshalIndent(struct {
		SchemaVersion int      `json:"schemaVersion"`
		Repos         []string `json:"repos"`
	}{registrySchemaVersion, repos}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling registry: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp registry file in %s: %w", dir, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("writing temp registry file %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("closing temp registry file %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

// DispatchRepoDirs reads dispatch.repos[].dir from the user config at configPath.
// A missing file contributes nothing, and so does a repo without a dir. Values are
// returned as written; validating them is UpdateRegistry's job. Keys match
// exactly: a struct-tag decode would also accept a differently cased key
// (go-gotchas rule 15).
func DispatchRepoDirs(configPath string) ([]string, error) {
	data, err := readBoundedRegularFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", configPath, err)
	}
	var top, dispatch map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", configPath, err)
	}
	rawDispatch, ok := top["dispatch"]
	if !ok {
		return nil, nil
	}
	if err := json.Unmarshal(rawDispatch, &dispatch); err != nil {
		return nil, fmt.Errorf("parsing %s: dispatch must be an object: %w", configPath, err)
	}
	rawRepos, ok := dispatch["repos"]
	if !ok {
		return nil, nil
	}
	var repos []map[string]json.RawMessage
	if err := json.Unmarshal(rawRepos, &repos); err != nil {
		return nil, fmt.Errorf("parsing %s: dispatch.repos must be a list of objects: %w", configPath, err)
	}
	var dirs []string
	for _, repo := range repos {
		rawDir, ok := repo["dir"]
		if !ok {
			continue
		}
		var dir string
		if err := json.Unmarshal(rawDir, &dir); err != nil {
			return nil, fmt.Errorf("parsing %s: dispatch.repos[].dir must be a string: %w", configPath, err)
		}
		if dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return dirs, nil
}

// UpdateRegistry merges the validated candidates into the persisted registry and
// returns the entries to process this pass, plus what went wrong.
//
// A candidate is registered as the main checkout root its validation resolves, so
// a window launched inside a worktree registers the repo, not the worktree that is
// about to be reclaimed. An entry is dropped only when its directory is gone
// (ErrNotExist); one that cannot be examined for another reason stays registered
// and is only left out of this pass. A corrupt file is reported and rebuilt from
// this call's candidates: the registry only widens which repos a pass looks at, so
// losing an entry delays cleanup and can never cause a removal. The file is
// rewritten only when its content changes.
func UpdateRegistry(ctx context.Context, u RegistryUpdate) ([]string, []RegistryProblem) {
	resolve := u.Resolve
	if resolve == nil {
		resolve = productionResolve()
	}
	var problems []RegistryProblem

	// Without an absolute registry path nothing is read or written (a relative
	// one would resolve against the working directory); the pass still gets the
	// union of this call's candidates.
	persistable := filepath.IsAbs(u.Path)
	var existing []string
	corrupt := false
	if persistable {
		var err error
		existing, err = LoadRegistry(u.Path)
		corrupt = err != nil
		if corrupt {
			problems = append(problems, RegistryProblem{RegistrySourceFile, u.Path, err})
		}
	} else {
		problems = append(problems, RegistryProblem{RegistrySourceFile, u.Path, errNoRegistryPath})
	}

	registered := slices.Clone(existing)
	for _, src := range []struct {
		source RegistrySource
		dirs   []string
	}{{RegistrySourceDispatch, u.DispatchDirs}, {RegistrySourceWindow, u.WindowDirs}} {
		tried := make(map[string]bool, len(src.dirs))
		for _, dir := range src.dirs {
			if tried[dir] {
				continue
			}
			tried[dir] = true
			root, err := resolve(ctx, dir)
			if err != nil {
				problems = append(problems, RegistryProblem{src.source, dir, err})
				continue
			}
			registered = append(registered, root)
		}
	}
	slices.Sort(registered)
	registered = slices.Compact(registered)

	var persist, process []string
	for _, dir := range registered {
		if _, err := os.Lstat(dir); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			problems = append(problems, RegistryProblem{RegistrySourceEntry, dir, err})
			persist = append(persist, dir)
			continue
		}
		persist = append(persist, dir)
		process = append(process, dir)
	}

	if persistable && (corrupt || !slices.Equal(persist, existing)) {
		if err := SaveRegistry(u.Path, persist); err != nil {
			problems = append(problems, RegistryProblem{RegistrySourceFile, u.Path, err})
		}
	}
	return process, problems
}

// productionResolve validates a candidate with a ticketstate.Resolver (an
// absolute existing directory in a git repo whose origin parses) and then
// ResolveRepo, whose containment check rejects a forged .git file; it returns the
// main checkout root.
func productionResolve() func(ctx context.Context, dir string) (string, error) {
	resolver := &ticketstate.Resolver{}
	return func(ctx context.Context, dir string) (string, error) {
		if _, err := resolver.Resolve(ctx, dir); err != nil {
			return "", err
		}
		repo, err := ResolveRepo(ctx, dir)
		if err != nil {
			return "", err
		}
		return repo.MainRoot, nil
	}
}

// readBoundedRegularFile reads path only when it is a regular file of at most
// maxRegistryFileBytes. The open is non-blocking (a FIFO would otherwise block
// until a writer appears), the type is checked on the open handle rather than the
// path (no stat/open race), and the read is capped.
func readBoundedRegularFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only handle: a close error cannot lose data

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file (mode %s)", info.Mode().Type())
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRegistryFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRegistryFileBytes {
		return nil, fmt.Errorf("larger than the %d-byte limit", maxRegistryFileBytes)
	}
	return data, nil
}
