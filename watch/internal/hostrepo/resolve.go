// Package hostrepo resolves the single host git checkout that a forwarded
// babysit-arm request's "owner/repo" belongs to, by inspecting every running
// sandbox container's `/workspace` bind-mount source under both container
// runtimes (docker and podman) and matching each source's normalized
// `origin` remote against the requested repo (#1095).
package hostrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
)

// ErrNoMatch is returned by Resolve when no running sandbox container's
// origin remote matches the requested repo -- fail closed, per the ticket's
// "### Decisions": zero matches never guesses.
var ErrNoMatch = errors.New("hostrepo: no running sandbox container's origin remote matches the requested repo")

// ErrAmbiguous is returned by Resolve when more than one distinct host
// checkout's origin remote matches the requested repo (e.g. two worktrees of
// the same repo, each running its own sandbox container) -- fail closed, per
// the ticket's "### Decisions": multiple distinct matches never guesses.
var ErrAmbiguous = errors.New("hostrepo: multiple distinct host checkouts' origin remotes match the requested repo")

// ErrMalformedInspect is the sentinel a combined per-runtime `inspect
// --format` probe's parser returns (wrapped with context via %w) for any
// response that doesn't parse into the expected per-container mount-record
// shape -- detectable via errors.Is, mirroring
// watch/internal/sandbox/launcher/audit_observed.go's
// ErrMalformedObservedInspect. Its whole purpose is to let Resolve
// distinguish "the inspect probe genuinely returned something this parser
// cannot trust" from "the probe parsed to a legitimate empty/no-match
// result" -- a malformed response must never be silently read as the
// permissive zero-value (no /workspace mount found), per
// watch/docs/error-handling.md's default-deny convention.
var ErrMalformedInspect = errors.New("hostrepo: malformed combined inspect output")

// inspectRecordSeparator delimits each requested container's mount record in
// the combined per-runtime `inspect --format` output (one Go template
// invocation per container name argument, applied in the same order the
// names were given on the command line -- docker/podman inspect's own
// documented per-object application order). Exactly one separator line
// terminates every record, including the last, so the parser can verify the
// record count matches the requested container count without a trailing
// partial record.
const inspectRecordSeparator = "---"

// workspaceMountDestination is the container-side mount point every
// sandboxed launch binds the host repo checkout to (or `~/Repos` in legacy
// scope) -- watch/internal/sandbox/launcher/scope.go's documented constant,
// re-declared here since internal/daemon must not import
// internal/sandbox/launcher (constraint: daemon -> hostrepo -> sandbox stays
// acyclic).
const workspaceMountDestination = "/workspace"

// combinedInspectFormat is the single per-runtime `inspect --format` Go
// template applied once per requested container name, mirroring
// watch/internal/sandbox/launcher/audit_observed.go's combined-probe
// pattern: for each container it emits at most one
// "<source>::/workspace::<rw>" line (only the /workspace mount, if any),
// terminated by inspectRecordSeparator -- docker/podman's own per-object
// template application appends a trailing newline after every object, which
// completes each record's own terminating separator line.
var combinedInspectFormat = `{{$found := false}}{{range .Mounts}}{{if eq .Destination "` + workspaceMountDestination + `"}}{{.Source}}::{{.Destination}}::{{.RW}}{{$found = true}}{{end}}{{end}}{{if $found}}{{"\n"}}{{end}}` + inspectRecordSeparator

// execWaitDelay bounds how long cmd.Wait can block after a subprocess this
// package starts has already exited or been killed by ctx's deadline,
// mirroring watch/internal/dispatch/mainsync.go's gitWaitDelay /
// watch/internal/babysit/tmux.go's tmuxWaitDelay.
const execWaitDelay = 5 * time.Second

// classifyExecTimeout re-joins context.DeadlineExceeded into runErr (a
// non-nil cmd.Run() error) when ctx's own deadline is what actually caused
// the failure -- mirrors watch/internal/babysit/gh.go's execGh /
// watch/internal/dispatch/gh.go's execGhBoundedCtx (#886): when a
// context.WithTimeout deadline fires while cmd.Run()/cmd.Wait() is already in
// flight, Go's os/exec kills the process and Wait returns a plain
// *exec.ExitError ("signal: killed") -- it does NOT wrap ctx.Err() on its
// own. ctx.Err() must be read here, before any caller's own deferred
// cancel() fires (a context.WithTimeout's cancel makes ctx.Err() report
// context.Canceled, not context.DeadlineExceeded, once called), so the real
// cause is still observable. A cmd.WaitDelay-triggered kill (a lingering
// grandchild holding the output pipes open) similarly returns bare
// exec.ErrWaitDelay, never joined with ctx's own cause -- operationally
// indistinguishable from a deadline kill, so it classifies the same way.
// Joining context.DeadlineExceeded directly (not a package-local sentinel)
// is what lets callers all the way up through hostrepo.Resolve into
// daemon.hostArmSpawn classify a real subprocess timeout via a plain
// errors.Is(err, context.DeadlineExceeded) check, no translation layer
// required.
func classifyExecTimeout(ctx context.Context, runErr error) error {
	if ctx.Err() == context.DeadlineExceeded {
		runErr = errors.Join(runErr, context.DeadlineExceeded)
	}
	if errors.Is(runErr, exec.ErrWaitDelay) {
		runErr = errors.Join(runErr, context.DeadlineExceeded)
	}
	return runErr
}

// execRuntime runs `<runtime> <args...>` bounded by ctx (the caller's own
// resolution budget), with cmd.WaitDelay bounding a lingering grandchild,
// separate bounded stdout/stderr buffers, and cmd.Stdin = nil -- mirrors
// mainsync.go's execGit / babysit/tmux.go's execTmux bounded-subprocess
// convention (watch/AGENTS.md rule #5).
func execRuntime(ctx context.Context, runtime string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, runtime, args...)
	cmd.WaitDelay = execWaitDelay
	cmd.Stdin = nil
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		err = classifyExecTimeout(ctx, err)
		return "", fmt.Errorf("%s %s: %w (%s)", runtime, strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return outBuf.String(), nil
}

// execGit runs `git -C dir <args...>` bounded by ctx exactly like
// execRuntime, so a per-source origin-remote read can never stall Resolve's
// own budget.
func execGit(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.WaitDelay = execWaitDelay
	cmd.Stdin = nil
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		err = classifyExecTimeout(ctx, err)
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimSpace(outBuf.String()), nil
}

// Resolve maps repo ("owner/repo") to the single host checkout directory
// whose `origin` remote matches it, by inspecting every running sandbox
// container's `/workspace` bind-mount source under every installed container
// runtime. It returns ErrNoMatch for zero matches, ErrAmbiguous for more
// than one distinct matching checkout (after symlink/Clean normalization and
// dedup), ErrMalformedInspect for an unparsable combined inspect response,
// and a probe-failure error (never one of the three sentinels above, and
// never silently guessed) for any other runtime-probe failure (a non-zero
// `ps`/`inspect` exit, a runtime enumeration failure, etc.) -- fail closed,
// per the ticket's "### Decisions".
//
// ctx bounds every subprocess call Resolve makes (the caller -- the daemon's
// hostArmSpawn seam -- supplies a budget-scoped context); Resolve makes no
// network calls (repo matching is origin-remote-only, per the ticket's
// "network-free" decision).
func Resolve(ctx context.Context, repo string) (string, error) {
	runtimes, err := sandbox.AvailableRuntimes()
	if err != nil {
		return "", fmt.Errorf("hostrepo: enumerating container runtimes: %w", err)
	}

	var candidates []string // /workspace bind-mount sources across every runtime

	for _, rt := range runtimes {
		psOut, err := execRuntime(ctx, rt, "ps", "--format", "{{.Names}}")
		if err != nil {
			return "", fmt.Errorf("hostrepo: %s ps: %w", rt, err)
		}
		var names []string
		for _, line := range strings.Split(psOut, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && sandbox.IsSandboxContainerName(line) {
				names = append(names, line)
			}
		}
		if len(names) == 0 {
			continue
		}

		inspectArgs := append([]string{"inspect", "--format", combinedInspectFormat}, names...)
		inspectOut, err := execRuntime(ctx, rt, inspectArgs...)
		if err != nil {
			return "", fmt.Errorf("hostrepo: %s inspect: %w", rt, err)
		}
		sources, err := parseCombinedInspect(inspectOut, names)
		if err != nil {
			return "", fmt.Errorf("hostrepo: %s inspect: %w", rt, err)
		}
		for _, n := range names {
			if src := sources[n]; src != "" {
				candidates = append(candidates, src)
			}
		}
	}

	matched := make(map[string]string) // canonical path -> first-seen raw source
	for _, source := range candidates {
		if !originMatches(ctx, source, repo) {
			continue
		}
		canon := canonicalize(source)
		if _, exists := matched[canon]; !exists {
			matched[canon] = source
		}
	}

	switch len(matched) {
	case 0:
		return "", ErrNoMatch
	case 1:
		for _, src := range matched {
			return src, nil
		}
	}
	return "", ErrAmbiguous
}

// parseCombinedInspect parses a combined per-runtime `inspect --format`
// response (combinedInspectFormat) into a map of container name -> the
// /workspace mount's source (empty string when the container has no
// /workspace mount -- a legitimate, non-malformed record). It fails closed
// (ErrMalformedInspect) when the response doesn't parse into exactly
// len(names) inspectRecordSeparator-terminated records, or when a record
// carries more than one content line, or a content line doesn't split into
// exactly 3 "::"-delimited fields naming the /workspace destination -- a
// garbled response must never be silently read as "no /workspace mount
// found" (watch/docs/error-handling.md's default-deny convention).
func parseCombinedInspect(out string, names []string) (map[string]string, error) {
	lines := strings.Split(out, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	result := make(map[string]string, len(names))
	idx := 0
	var current []string
	for _, line := range lines {
		if line == inspectRecordSeparator {
			if idx >= len(names) {
				return nil, fmt.Errorf("more inspect records than requested containers (%d): %w", len(names), ErrMalformedInspect)
			}
			source, err := parseInspectRecord(current)
			if err != nil {
				return nil, err
			}
			result[names[idx]] = source
			idx++
			current = nil
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 || idx != len(names) {
		return nil, fmt.Errorf("got %d complete inspect records, want %d: %w", idx, len(names), ErrMalformedInspect)
	}
	return result, nil
}

// parseInspectRecord parses one container's accumulated content lines (the
// lines between the previous separator and this record's own) into its
// /workspace mount source, or "" when the record legitimately carries no
// content line (no /workspace mount).
func parseInspectRecord(lines []string) (string, error) {
	switch len(lines) {
	case 0:
		return "", nil
	case 1:
		parts := strings.SplitN(lines[0], "::", 3)
		if len(parts) != 3 || parts[1] != workspaceMountDestination {
			return "", fmt.Errorf("malformed inspect record %q: %w", lines[0], ErrMalformedInspect)
		}
		return parts[0], nil
	default:
		return "", fmt.Errorf("inspect record carries %d content lines, want 0 or 1: %w", len(lines), ErrMalformedInspect)
	}
}

// originMatches reports whether source's git `origin` remote normalizes to
// repo ("owner/repo"). Any failure to read or parse the remote (source isn't
// a git checkout at all -- e.g. a legacy-scope `~/Repos` bind source, or has
// no `origin` remote configured) excludes it as a plain non-match, never an
// error: per the ticket's decisions, only the container-runtime probe
// (ps/inspect) fails closed with its own distinguishable error; a per-source
// git read is just one candidate among many.
func originMatches(ctx context.Context, source, repo string) bool {
	url, err := execGit(ctx, source, "remote", "get-url", "origin")
	if err != nil {
		return false
	}
	owner, name, err := parseOriginRemote(url)
	if err != nil {
		return false
	}
	return owner+"/"+name == repo
}

// canonicalize resolves source to its canonical form for dedup purposes
// (symlink resolution + Clean), falling back to a plain filepath.Clean when
// EvalSymlinks fails (e.g. a since-removed path) so a transient stat failure
// never crashes Resolve.
func canonicalize(source string) string {
	if real, err := filepath.EvalSymlinks(source); err == nil {
		return real
	}
	return filepath.Clean(source)
}

// parseOriginRemote extracts owner/name from a git origin remote URL,
// re-implemented here (rather than imported from
// internal/dispatch/enroll.go's parseRemoteURL) because internal/dispatch
// imports internal/daemon (a confirmed import cycle -- plan Q&A #2): ssh://,
// https://, http://, and scp-style ([user@]host:path) forms are all
// accepted, tolerating an optional trailing ".git" suffix and trailing
// slash. Pure, side-effect-free parsing logic (watch/docs/test-strategy.md's
// "unit tests for pure logic" exception).
func parseOriginRemote(url string) (owner, name string, err error) {
	var path string
	switch {
	case strings.HasPrefix(url, "ssh://"), strings.HasPrefix(url, "https://"), strings.HasPrefix(url, "http://"):
		_, rest, _ := strings.Cut(url, "://")
		_, p, ok := strings.Cut(rest, "/")
		if !ok {
			return "", "", fmt.Errorf("no path in remote url %q", url)
		}
		path = p
	default:
		// scp-style: [user@]host:path
		_, p, ok := strings.Cut(url, ":")
		if !ok {
			return "", "", fmt.Errorf("unrecognized remote url %q", url)
		}
		path = p
	}

	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if path == "" {
		return "", "", fmt.Errorf("empty path in remote url %q", url)
	}

	segments := strings.Split(path, "/")
	if len(segments) < 2 {
		return "", "", fmt.Errorf("remote url %q has fewer than two path segments", url)
	}
	owner = segments[len(segments)-2]
	name = segments[len(segments)-1]
	if owner == "" || name == "" {
		return "", "", fmt.Errorf("remote url %q has an empty owner or name segment", url)
	}
	return owner, name, nil
}
