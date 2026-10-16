package worktreeclean

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrUnmapped is returned by Inspect for an entry that does not map to a safe
// local worktree right now.
var ErrUnmapped = errors.New("worktreeclean: entry is unmapped")

// Facts are the read-only git and filesystem facts of one mapped worktree.
//
// Facts are trustworthy only when Inspect returned a nil error. On any non-nil
// error they are advisory: they hold whatever was gathered before the failure,
// and the unsafe-direction zeros (Dirty, Locked, Untracked and
// ReachableFromRemote all false) carry no meaning, because a fact that was never
// read looks identical to one that was read and found false. A caller deciding
// a removal must keep the worktree on any error and never read a field of the
// returned Facts as evidence. A worktree containing a submodule never yields
// Facts: Inspect reports it as an error by design (see Inspect).
type Facts struct {
	// Dirty is true when a tracked file is modified, staged, deleted, renamed or
	// unmerged. Untracked is true when a non-ignored untracked file exists.
	Dirty     bool
	Untracked bool
	// Locked is true when the worktree has an admin `locked` file.
	Locked bool
	// Branch is the checked-out branch ("" when detached); Detached marks a
	// detached HEAD. HeadSHA is "" for an unborn HEAD.
	Branch   string
	Detached bool
	HeadSHA  string
	// Upstream is the configured upstream ("" when none); UpstreamGone is true
	// when it is configured but its remote-tracking ref no longer exists.
	Upstream     string
	UpstreamGone bool
	// Ahead is the commit count ahead of the upstream, meaningful only when
	// AheadKnown (an upstream is configured and exists locally).
	Ahead      int
	AheadKnown bool
	// ReachableFromRemote is true when HEAD is contained in some refs/remotes/*
	// ref, as of the last fetch.
	ReachableFromRemote bool
	// LastActivity is the newest of the non-ignored files' mtimes, the admin
	// index and HEAD-log mtimes and the last commit date; zero when none exist.
	LastActivity time.Time
	// SizeBytes is the allocated size on disk of the worktree directory.
	SizeBytes int64
}

// branchPattern is a local copy of internal/pipeline's branch-name gate (this
// leaf package must not import it): a leading alphanumeric, then
// alphanumerics and `._/-`. A git ref name can hold shell metacharacters, and
// the value flows to later consumers, so anything else is refused.
var branchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// Inspect reports the read-only facts of one worktree. It re-derives the mapping
// from entry.AdminName (ignoring the entry's other fields, which may be stale or
// forged), which doubles as the time-of-check re-check a later removal reuses; an
// entry that does not map now yields ErrUnmapped with the reason and no facts. A
// Repo that is not a canonical main checkout (see validRepo) is refused too.
//
// Every fact is read with explicit --git-dir/--work-tree and without writing
// anything or running a repo-configured command: a clean or process filter in
// the repository's own configuration makes Inspect refuse, since `git status`
// would run it on any stat-dirty file.
//
// Worktrees containing submodules are reported as errors (kept) by design, a
// deliberate limit: any worktree whose index or HEAD tree holds a gitlink (mode
// 160000) yields an error and no Facts. Seeing inside a submodule requires git to
// start a child process in it, which loads the submodule's own agent-writable
// config and attributes (clean filters, fsmonitor), and the only mode that avoids
// the child (--ignore-submodules=all) would read a dirty submodule as clean.
// Enumerate is unaffected: it never runs status.
//
// On a failure Inspect returns the facts gathered so far together with a joined
// error (the partial-facts contract), but a non-nil error means the facts are
// advisory only: Dirty=false, Locked=false, Untracked=false and
// ReachableFromRemote=false then carry no meaning (the stage that sets them may
// not have run), so the facts must not be used to decide a removal.
func Inspect(ctx context.Context, repo Repo, entry Entry) (Facts, error) {
	if err := ctx.Err(); err != nil {
		return Facts{}, err
	}
	if err := validRepo(repo); err != nil {
		return Facts{}, err
	}
	m := mapEntry(repo, entry.AdminName)
	if m.Mapping == MappingUnmapped {
		return Facts{}, fmt.Errorf("%w: %s (admin entry %q)", ErrUnmapped, m.Reason, entry.AdminName)
	}
	if err := rejectConfiguredFilters(ctx, m); err != nil {
		return Facts{}, err
	}

	// A submodule is refused outright (see the Inspect doc). The index is checked
	// before status runs; HEAD's tree needs the SHA status reports, so it is
	// checked right after. Neither order matters for safety: --ignore-submodules=all
	// below means status never starts a process inside a gitlink directory.
	if err := rejectGitlinksInIndex(ctx, m); err != nil {
		return Facts{}, err
	}

	// --ignore-submodules=all is the only mode in which git never starts a child
	// process in a submodule directory; any mode that inspects submodule content
	// would load that submodule's own agent-writable config and attributes (and run
	// its clean filter or fsmonitor hook). It also overrides every way a submodule
	// can be configured, so the setting cannot vary with .gitmodules or repo config.
	stdout, stderr, err := execGitWT(ctx, m.AdminDir, m.LocalPath,
		"status", "--porcelain=v2", "-z", "--branch", "--untracked-files=normal", "--ignore-submodules=all")
	if err != nil {
		return Facts{}, fmt.Errorf("git status in %s: %w", m.LocalPath, withStderr(err, stderr))
	}
	facts, err := parseStatus(stdout)
	if err != nil {
		return facts, fmt.Errorf("parse git status of %s: %w", m.LocalPath, err)
	}

	// =all also hides a staged add or delete of a gitlink, so HEAD's tree is
	// checked as well; the index is re-read to catch a gitlink added since the
	// first check.
	if facts.HeadSHA != "" {
		if err := rejectGitlinksInHead(ctx, m, facts.HeadSHA); err != nil {
			return Facts{}, err
		}
	}
	if err := rejectGitlinksInIndex(ctx, m); err != nil {
		return Facts{}, err
	}

	var errs []error
	locked, err := isLocked(m.AdminDir)
	facts.Locked = locked
	errs = append(errs, err)

	var commitDate time.Time
	if facts.HeadSHA != "" {
		facts.ReachableFromRemote, err = reachableFromRemote(ctx, m, facts.HeadSHA)
		errs = append(errs, err)
		commitDate, err = lastCommitDate(ctx, m, facts.HeadSHA)
		errs = append(errs, err)
	}
	facts.LastActivity, err = lastActivity(ctx, m, commitDate)
	errs = append(errs, err)
	facts.SizeBytes, err = diskUsage(ctx, m.LocalPath)
	errs = append(errs, err)
	return facts, errors.Join(errs...)
}

// validRepo refuses a caller-supplied Repo that is not exactly what ResolveRepo
// returns: absolute, cleaned paths with CommonDir being MainRoot/.git. Inspect
// feeds both into --git-dir/--work-tree arguments and pointer comparisons, so a
// hand-built or corrupted value must not steer it.
func validRepo(repo Repo) error {
	for _, f := range []struct{ name, path string }{{"MainRoot", repo.MainRoot}, {"CommonDir", repo.CommonDir}} {
		if !filepath.IsAbs(f.path) || filepath.Clean(f.path) != f.path {
			return fmt.Errorf("worktreeclean: Repo.%s %q is not an absolute, cleaned path", f.name, f.path)
		}
	}
	if want := filepath.Join(repo.MainRoot, ".git"); repo.CommonDir != want {
		return fmt.Errorf("worktreeclean: Repo.CommonDir %q is not %q", repo.CommonDir, want)
	}
	return nil
}

// filterKeyPattern matches the config keys of the filters `git status` runs
// while reading a file whose stat differs from the index.
const filterKeyPattern = `^filter\..*\.(clean|process)$`

// rejectConfiguredFilters refuses a worktree whose repository (local or
// worktree scope) or command-line configuration defines a clean or process
// filter. Which files use a filter is decided by .gitattributes, which the same
// agent can write, so the presence of the configuration is enough to deny.
// System and global scopes are the host's own and are tolerated (git-lfs
// installs its filters there). Only a clean "no match" (exit 1, no output at
// all) passes; every other outcome of the probe is an error.
func rejectConfiguredFilters(ctx context.Context, m Entry) error {
	stdout, stderr, err := execGitWT(ctx, m.AdminDir, m.LocalPath,
		"config", "--show-scope", "--get-regexp", filterKeyPattern)
	if err != nil {
		// Compared by type, not errors.As: a timeout or truncation joined to the
		// exit error must not read as a clean no-match.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && stdout == "" && stderr == "" {
			return nil
		}
		return fmt.Errorf("check filter configuration of %s: %w", m.LocalPath, withStderr(err, stderr))
	}
	for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		if strings.HasPrefix(line, "system\t") || strings.HasPrefix(line, "global\t") {
			continue
		}
		return fmt.Errorf("worktree %s: a clean or process filter is configured (%s); git status would run it", m.LocalPath, collapse(line))
	}
	return nil
}

// gitlinkMode is the tree/index mode of a submodule commit pointer.
const gitlinkMode = "160000"

// modePattern is the shape of a git tree or index entry mode.
var modePattern = regexp.MustCompile(`^[0-7]{6}$`)

// rejectGitlinksInIndex refuses a worktree whose index holds a gitlink.
func rejectGitlinksInIndex(ctx context.Context, m Entry) error {
	// ls-files --stage entries: "<mode> <object> <stage>\t<path>".
	err := execGitWTStream(ctx, m.AdminDir, m.LocalPath, gitlinkScanner("index"), "ls-files", "--stage", "-z")
	if err != nil {
		return fmt.Errorf("scan the index of %s for submodules: %w", m.LocalPath, err)
	}
	return nil
}

// rejectGitlinksInHead refuses a worktree whose HEAD commit tree holds a gitlink.
// sha must be the hex-validated object name status reported.
func rejectGitlinksInHead(ctx context.Context, m Entry, sha string) error {
	if !isHexSHA(sha) {
		return fmt.Errorf("scan HEAD of %s for submodules: malformed HEAD %q", m.LocalPath, sha)
	}
	// ls-tree entries: "<mode> <type> <object>\t<path>".
	err := execGitWTStream(ctx, m.AdminDir, m.LocalPath, gitlinkScanner("HEAD tree"), "ls-tree", "-r", "-z", sha)
	if err != nil {
		return fmt.Errorf("scan HEAD of %s for submodules: %w", m.LocalPath, err)
	}
	return nil
}

// gitlinkScanner returns the stream callback that fails on a gitlink entry and,
// default-deny, on any entry it cannot parse.
func gitlinkScanner(where string) func(string) error {
	return func(entry string) error {
		header, _, ok := strings.Cut(entry, "\t")
		mode, _, hasFields := strings.Cut(header, " ")
		if !ok || !hasFields || !modePattern.MatchString(mode) {
			return fmt.Errorf("unrecognized %s entry %q", where, collapse(entry))
		}
		if mode == gitlinkMode {
			return fmt.Errorf("worktree holds a submodule (gitlink) in its %s: %q; submodules are not inspected, so the worktree is kept", where, collapse(entry))
		}
		return nil
	}
}

// parseStatus reads `git status --porcelain=v2 -z --branch` output. Unknown
// record types are an error (default deny); fields are validated before they
// reach the returned Facts.
func parseStatus(out string) (Facts, error) {
	var f Facts
	var haveOID, haveHead bool
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		rec := fields[i]
		switch {
		case rec == "":
		case strings.HasPrefix(rec, "# branch.oid "):
			oid := strings.TrimPrefix(rec, "# branch.oid ")
			switch {
			case oid == "(initial)":
			case isHexSHA(oid):
				f.HeadSHA = oid
			default:
				return f, fmt.Errorf("malformed branch.oid %q", oid)
			}
			haveOID = true
		case strings.HasPrefix(rec, "# branch.head "):
			head := strings.TrimPrefix(rec, "# branch.head ")
			switch {
			case head == "(detached)":
				f.Detached = true
			case branchPattern.MatchString(head):
				f.Branch = head
			default:
				return f, fmt.Errorf("branch name %q does not match %s", head, branchPattern)
			}
			haveHead = true
		case strings.HasPrefix(rec, "# branch.upstream "):
			upstream := strings.TrimPrefix(rec, "# branch.upstream ")
			if !branchPattern.MatchString(upstream) {
				return f, fmt.Errorf("upstream name %q does not match %s", upstream, branchPattern)
			}
			f.Upstream = upstream
		case strings.HasPrefix(rec, "# branch.ab "):
			ahead, err := parseAhead(strings.TrimPrefix(rec, "# branch.ab "))
			if err != nil {
				return f, err
			}
			f.Ahead, f.AheadKnown = ahead, true
		case strings.HasPrefix(rec, "# "):
			// Other headers carry nothing this package reports.
		case strings.HasPrefix(rec, "1 "), strings.HasPrefix(rec, "u "):
			f.Dirty = true
		case strings.HasPrefix(rec, "2 "):
			f.Dirty = true
			i++ // the renamed-from path is its own NUL-separated field
		case strings.HasPrefix(rec, "? "):
			f.Untracked = true
		default:
			return f, fmt.Errorf("unrecognized status record %q", collapse(rec))
		}
	}
	if !haveOID || !haveHead {
		return f, errors.New("status output lacks branch.oid or branch.head")
	}
	f.UpstreamGone = f.Upstream != "" && !f.AheadKnown
	return f, nil
}

// parseAhead reads the "+<ahead> -<behind>" value of branch.ab.
func parseAhead(v string) (int, error) {
	parts := strings.Fields(v)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "+") || !strings.HasPrefix(parts[1], "-") {
		return 0, fmt.Errorf("malformed branch.ab %q", v)
	}
	ahead, aheadErr := strconv.Atoi(parts[0][1:])
	behind, behindErr := strconv.Atoi(parts[1][1:])
	if aheadErr != nil || behindErr != nil || ahead < 0 || behind < 0 {
		return 0, fmt.Errorf("malformed branch.ab %q", v)
	}
	return ahead, nil
}

// isHexSHA reports whether s is a full lowercase hex object name (SHA-1 or
// SHA-256); it gates every SHA before it enters an argv.
func isHexSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// isLocked reports whether the admin dir holds a `locked` file. Only a missing
// file means false; any other failure is an error, never a collapsed false.
func isLocked(adminDir string) (bool, error) {
	_, err := os.Lstat(filepath.Join(adminDir, "locked"))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, fmt.Errorf("check lock of %s: %w", adminDir, err)
}

// reachableFromRemote reports whether sha is contained in any remote-tracking
// ref, using only the local refs/remotes as of the last fetch.
func reachableFromRemote(ctx context.Context, m Entry, sha string) (bool, error) {
	stdout, stderr, err := execGitWT(ctx, m.AdminDir, m.LocalPath,
		"for-each-ref", "--count=1", "--format=%(refname)", "--contains", sha, "refs/remotes")
	if err != nil {
		return false, fmt.Errorf("git for-each-ref --contains in %s: %w", m.LocalPath, withStderr(err, stderr))
	}
	return strings.TrimSpace(stdout) != "", nil
}

// lastCommitDate is the committer date of sha.
func lastCommitDate(ctx context.Context, m Entry, sha string) (time.Time, error) {
	stdout, stderr, err := execGitWT(ctx, m.AdminDir, m.LocalPath, "log", "-1", "--no-show-signature", "--format=%ct", sha, "--")
	if err != nil {
		return time.Time{}, fmt.Errorf("git log in %s: %w", m.LocalPath, withStderr(err, stderr))
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(stdout), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("malformed commit date %q: %w", collapse(stdout), err)
	}
	return time.Unix(secs, 0), nil
}

// lastActivity is the newest of the commit date, the admin index and HEAD-log
// mtimes and the mtime of every tracked or untracked non-ignored file (ignored
// trees are never walked). A future-dated mtime is not clamped: it makes the
// worktree look recently active, which keeps it. A missing file is skipped; any
// other failure, and an unfinished listing, is an error.
func lastActivity(ctx context.Context, m Entry, commitDate time.Time) (time.Time, error) {
	newest := commitDate
	consider := func(path string) error {
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
			return nil
		case err != nil:
			return err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	}
	for _, p := range []string{
		filepath.Join(m.AdminDir, "index"),
		filepath.Join(m.AdminDir, "logs", "HEAD"),
	} {
		if err := consider(p); err != nil {
			return newest, fmt.Errorf("stat %s: %w", p, err)
		}
	}
	err := execGitWTStream(ctx, m.AdminDir, m.LocalPath, func(rel string) error {
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("git listed a path outside the worktree: %q", rel)
		}
		return consider(filepath.Join(m.LocalPath, rel))
	}, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return newest, fmt.Errorf("list files of %s: %w", m.LocalPath, err)
	}
	return newest, nil
}

// diskUsage is the du-like allocated size (blocks * 512) of root, counting each
// multiply-linked inode once and never following symlinks. Without a
// syscall.Stat_t it falls back to the file size. The walk is bounded by
// sizeTimeout and maxSizeEntries: expiry or the cap is an error, never a smaller
// total. A root that cannot be read is an error; below the root a vanished entry
// is skipped and any other failure is collected while the walk goes on.
func diskUsage(ctx context.Context, root string) (int64, error) {
	if _, err := os.Lstat(root); err != nil {
		return 0, fmt.Errorf("measure size of %s: %w", root, err)
	}
	ctx, cancel := context.WithTimeout(ctx, sizeTimeout)
	defer cancel()

	type inode struct{ dev, ino uint64 }
	var total int64
	var errs []error
	entries := 0
	seen := make(map[inode]struct{})
	skip := func(path string, err error) error {
		if path == root || !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		return nil
	}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entries++; entries > maxSizeEntries {
			return fmt.Errorf("%w: more than %d entries", errOutputTruncated, maxSizeEntries)
		}
		if err != nil {
			return skip(path, err)
		}
		info, err := d.Info()
		if err != nil {
			return skip(path, err)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			total += info.Size()
			return nil
		}
		if st.Nlink > 1 && !d.IsDir() {
			key := inode{dev: uint64(st.Dev), ino: uint64(st.Ino)}
			if _, dup := seen[key]; dup {
				return nil
			}
			seen[key] = struct{}{}
		}
		total += st.Blocks * 512
		return nil
	})
	if walkErr != nil {
		if errors.Is(walkErr, context.DeadlineExceeded) {
			walkErr = errors.Join(walkErr, errTimeout)
		}
		errs = append(errs, walkErr)
	}
	if err := errors.Join(errs...); err != nil {
		return total, fmt.Errorf("measure size of %s: %w", root, err)
	}
	return total, nil
}
