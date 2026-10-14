package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
)

// launchProbeScript lists the in-container processes carrying a launch ID:
// invoked as `sh -c '<script>' _ <launch-id>` (the ID is argv, never
// interpolated; it is saved before `set --` reuses the positional parameters).
// Each process's NUL-delimited environ is matched with `grep --null-data` (the
// long-form flag, not `-z`: some grep-compatible tools repurpose `-z`) as a
// whole entry -- --line-regexp --fixed-strings against
// CENCI_LAUNCH_ID=<id> -- so an ID that is merely a prefix or suffix of a value,
// sits inside another variable's value, or is forged through an embedded newline
// never matches. PID 1 (the container init) and the probe's own shell are
// skipped. Only "<pid>\t<start>" rows are printed (start is /proc/<pid>/stat
// field 22, empty if it could not be read), never anything process-controlled.
//
// A grep that exits above 1 (2: the environ could not be read; 127: grep could
// not run) is not a "no match": folding it into one would let a blind probe
// report a clean kill. Mirroring reapScanScript, a process that still exists
// then prints "__UNREADABLE__\t<pid>" -- unless /proc/<pid>/status shows another
// real user, whose environ an unprivileged probe is never meant to read (a root
// helper in the container would otherwise make every close look blind). The
// uid check fails toward reporting: an unreadable status or uid reports.
const launchProbeScript = `id="$1"; me=$$; uid=$(id -u 2>/dev/null); set --; for e in /proc/[0-9]*/environ; do p=${e#/proc/}; p=${p%/environ}; [ "$p" = 1 ] && continue; [ "$p" = "$me" ] && continue; grep --null-data --text --quiet --line-regexp --fixed-strings -e "` + LaunchIDEnvName + `=$id" "$e" 2>/dev/null; rc=$?; if [ "$rc" -gt 1 ]; then if [ -e "$e" ]; then u=$(sed -n 's/^Uid:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "/proc/$p/status" 2>/dev/null); if [ -z "$u" ] || [ -z "$uid" ] || [ "$u" = "$uid" ]; then printf "__UNREADABLE__\t%s\n" "$p"; fi; fi; continue; fi; [ "$rc" -eq 0 ] || continue; st=$(cat "/proc/$p/stat" 2>/dev/null); st=${st##*) }; set -- $st; if [ "$#" -ge 20 ]; then shift 19; start=$1; else start=""; fi; printf "%s\t%s\n" "$p" "$start"; done`

// launchRowPattern is the only shape a probe output line may have: a positive
// PID without leading zeros (so "0" and "01" can never name the caller's group
// or the init) and an optional numeric start time.
var launchRowPattern = regexp.MustCompile(`^[1-9][0-9]*\t[0-9]*$`)

// unreadableRowPattern is the only shape of an unreadable-environ marker row:
// the marker, a TAB and a positive PID without leading zeros. A row naming PID 1
// matches the pattern but is rejected like a launch row for it: the script never
// prints it.
var unreadableRowPattern = regexp.MustCompile(`^__UNREADABLE__\t[1-9][0-9]*$`)

// ErrUnreadableProcesses marks a listing that is complete only for the processes
// it could read: ListLaunchProcesses returns the readable matches together with
// an error wrapping it, because processes whose environ could not be read may
// belong to the launch and cannot be signaled. Unlike every other listing
// error it is non-fatal -- the matches returned alongside are still valid.
var ErrUnreadableProcesses = errors.New("environ not readable")

// containerNameCharset is the container-runtime name grammar, applied on top of
// the sandbox prefix so a name can never carry a space, slash, newline or shell
// metacharacter into an exec argv.
var containerNameCharset = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// ValidSandboxContainerName reports whether name is a sandbox container name
// (claude-cenci-/codex-cenci-/opencode-cenci- prefix) within the runtime's name
// grammar -- the check every host-side caller must pass before a recorded
// container name reaches an exec argv.
func ValidSandboxContainerName(name string) bool {
	return IsSandboxContainerName(name) && containerNameCharset.MatchString(name)
}

// ListLaunchProcesses returns the processes in container whose environ carries
// CENCI_LAUNCH_ID=id, via launchProbeScript run as the unprivileged agent user
// (-u dev: same-uid /proc/<pid>/environ reads need no CAP_SYS_PTRACE). It fails
// closed -- an error, nothing returned, so nothing is ever signaled -- on an
// invalid id or container name and on any probe output line that is not a
// well-formed row or that names the container init. The one partial result is
// ErrUnreadableProcesses: the probe could not read some processes' environ, so
// the readable matches come back alongside an error wrapping it and the caller
// decides (KillLaunch signals the matches and reports the rest).
func ListLaunchProcesses(ctx context.Context, rt, container, id string) ([]ProcTarget, error) {
	if !ValidLaunchID(id) {
		return nil, fmt.Errorf("invalid launch id %q", id)
	}
	if !ValidSandboxContainerName(container) {
		return nil, fmt.Errorf("invalid container name %s", logSafeText(container))
	}

	var out, errBuf bytes.Buffer
	if err := runRuntime(ctx, rt, &out, &errBuf, "exec", "-u", "dev", container, "sh", "-c", launchProbeScript, "_", id); err != nil {
		return nil, fmt.Errorf("%s exec launch probe in %s: %w%s", rt, container, err, stderrSuffix(&errBuf))
	}

	text := strings.TrimSuffix(out.String(), "\n")
	if text == "" {
		return nil, nil
	}
	var targets []ProcTarget
	unreadable := 0
	for _, line := range strings.Split(text, "\n") {
		if unreadableRowPattern.MatchString(line) {
			if line == "__UNREADABLE__\t1" {
				return nil, fmt.Errorf("%s exec launch probe in %s: row names the container init", rt, container)
			}
			unreadable++
			continue
		}
		if !launchRowPattern.MatchString(line) {
			return nil, fmt.Errorf("%s exec launch probe in %s: malformed row %s", rt, container, logSafeText(line))
		}
		pid, start, _ := strings.Cut(line, "\t")
		if pid == "1" {
			return nil, fmt.Errorf("%s exec launch probe in %s: row names the container init", rt, container)
		}
		targets = append(targets, ProcTarget{PID: pid, Start: start})
	}
	if unreadable > 0 {
		return targets, fmt.Errorf("%d process(es) of launch %s unreadable in %s (their environ could not be read, so they could not be matched or signaled): %w", unreadable, id, container, ErrUnreadableProcesses)
	}
	return targets, nil
}

// KillLaunch signals every process of a launch in container: list, SIGTERM each
// (a pid that exited first is skipped; any other failure is collected and the
// rest still proceed), a bounded wait for them to exit, SIGKILL for the
// survivors, then one bounded re-list that SIGKILLs a process forked after the
// first listing and reports any first-listing process that is still there. All
// failures are returned together (errors.Join); the listing failing is the only
// early return, since nothing is known to signal. Processes the probe could not
// read (ErrUnreadableProcesses) are reported, once, without stopping the readable
// ones from being signaled. grace is the TERM -> KILL window (see GraceFromEnv).
func KillLaunch(ctx context.Context, rt, container, id string, grace time.Duration, stdout, stderr io.Writer) error {
	targets, err := ListLaunchProcesses(ctx, rt, container, id)
	var errs []error
	unreadableReported := false
	if errors.Is(err, ErrUnreadableProcesses) {
		errs = append(errs, err)
		unreadableReported = true
	} else if err != nil {
		return err
	}

	live := make([]ProcTarget, 0, len(targets))
	for _, t := range targets {
		gone, err := SignalTerm(ctx, rt, container, t.PID, stdout, stderr)
		if gone {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("pid %s: %w", t.PID, err))
		}
		// A SIGTERM that failed for a live process still gets its SIGKILL pass:
		// the failure says nothing about whether SIGKILL will land.
		live = append(live, t)
	}

	AwaitExit(ctx, rt, container, live, grace)
	if err := KillSurvivors(ctx, rt, container, live, stdout, stderr); err != nil {
		errs = append(errs, err)
	}

	// One re-list, not a loop: a fork that raced the first listing is caught
	// here, and the reaper remains the backstop for anything later.
	again, err := ListLaunchProcesses(ctx, rt, container, id)
	switch {
	case errors.Is(err, ErrUnreadableProcesses):
		// Already reported for the first listing; the re-list only reports it
		// when the first one did not see them.
		if !unreadableReported {
			errs = append(errs, err)
		}
	case err != nil:
		return errors.Join(append(errs, fmt.Errorf("re-listing launch %s after SIGKILL: %w", id, err))...)
	}
	var survivors []string
	for _, t := range again {
		if slices.Contains(targets, t) {
			survivors = append(survivors, t.PID)
		}
	}
	if len(survivors) > 0 {
		errs = append(errs, fmt.Errorf("process(es) %s of launch %s in %s survived SIGKILL", strings.Join(survivors, ","), id, container))
	}
	if err := KillSurvivors(ctx, rt, container, again, stdout, stderr); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
