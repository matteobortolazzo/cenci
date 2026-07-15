package launcher

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/sandbox"
)

// reapScanScript is the minimal POSIX in-container scan, copied VERBATIM
// from sandbox/cenci-sand's REAP_SCAN_SCRIPT (it still runs inside the
// container via `exec -u root sh -c`): emits "<pid>\t<pane>\t<start>" for
// every process whose environ carries a TMUX_PANE key (pane may be empty;
// <start> is /proc/<pid>/stat field 22, read in the same loop pass; empty if
// the stat line couldn't be read/parsed), skipping processes lacking the
// TMUX_PANE key entirely. All classification — skip-empty-pane,
// skip-malformed-pane, skip-live-pane, reap-dead-pane, TERM→KILL escalation,
// PID-reuse detection, no-tmux handling — happens host-side in ReapOrphans,
// never inside this canned scan.
const reapScanScript = `for e in /proc/[0-9]*/environ; do [ -f "$e" ] || continue; p=${e#/proc/}; p=${p%/environ}; line=$(tr "\0" "\n" < "$e" 2>/dev/null | grep "^TMUX_PANE=") || continue; st=$(cat "/proc/$p/stat" 2>/dev/null); st=${st##*) }; set -- $st; if [ "$#" -ge 20 ]; then shift 19; start=$1; else start=""; fi; printf "%s\t%s\t%s\n" "$p" "${line#TMUX_PANE=}" "$start"; done`

// reapLivenessScript is the pre-SIGKILL liveness/identity probe, copied
// VERBATIM from sandbox/cenci-sand's REAP_LIVENESS_SCRIPT: invoked as
// `sh -c "<script>" _ <pid>` (pid passed as an argument, never interpolated).
// Prints the process's *current* start time if alive, the sentinel __GONE__
// if not, or __NOSTAT__ if alive but unverifiable. Always exits 0 if it ran
// at all, so a non-zero exit from the surrounding `exec` means the exec
// transport itself failed — see the escalation loop for the host-side
// classification.
const reapLivenessScript = `pid="$1"; st=$(cat "/proc/$pid/stat" 2>/dev/null); if [ -z "$st" ]; then printf "__GONE__\n"; exit 0; fi; st=${st##*) }; set -- $st; if [ "$#" -ge 20 ]; then shift 19; printf "%s\n" "$1"; else printf "__NOSTAT__\n"; fi; exit 0`

// panePattern is tmux's real pane-id format; anything else in a TMUX_PANE
// value is malformed and never signaled.
var panePattern = regexp.MustCompile(`^%[0-9]+$`)

// numericPattern validates start times before trusting a mismatch as genuine
// PID reuse (a forging process can corrupt its own stat field 22).
var numericPattern = regexp.MustCompile(`^[0-9]+$`)

// reapSleep is the SIGTERM→SIGKILL grace sleep, a package var so tests could
// stub it; CENCI_SANDBOX_REAP_GRACE_SECS already tunes the duration to 0 for
// fast test runs, matching the bash suite's usage.
var reapSleep = time.Sleep

// ReapOrphans kills container-side agent processes whose owning tmux pane no
// longer exists on the host. Ownership is recorded via the TMUX_PANE env var
// injected into every exec'd session, present in /proc/<pid>/environ of
// every container-side agent process and descendants. It is a global sweep
// (not scoped to the current repo) and scans every installed runtime, since
// orphans can exist under either docker or podman regardless of which one
// the launcher prefers.
//
// It prints its own "Error:"/"Note:" lines (errors to stderr, matching the
// bash launcher whose retargeted test suite asserts these exact strings);
// the returned error only signals a non-zero exit to the caller.
func ReapOrphans(stdout, stderr io.Writer) error {
	// CENCI_SANDBOX_REAP_GRACE_SECS lets callers tune the SIGTERM → SIGKILL
	// grace window (e.g. to 0 for fast test runs).
	grace := 5.0
	if v := os.Getenv("CENCI_SANDBOX_REAP_GRACE_SECS"); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			grace = parsed
		}
	}

	// ── Host tmux liveness ──────────────────────────────────────────
	livePanes := map[string]bool{}
	noTmuxServer := false
	var tmuxStdout, tmuxStderr bytes.Buffer
	tmuxCmd := exec.Command("tmux", "list-panes", "-a", "-F", "#{pane_id}")
	tmuxCmd.Stdout = &tmuxStdout
	tmuxCmd.Stderr = &tmuxStderr
	if err := tmuxCmd.Run(); err == nil {
		for _, pane := range splitLines(tmuxStdout.String()) {
			if pane != "" {
				livePanes[pane] = true
			}
		}
	} else if strings.Contains(strings.ToLower(tmuxStderr.String()), "no server running") {
		noTmuxServer = true
		_, _ = fmt.Fprintln(stdout, "Note: No tmux server detected; treating every TMUX_PANE-carrying process as orphaned.")
	} else {
		errText := strings.TrimRight(tmuxStderr.String(), "\n")
		_, _ = fmt.Fprintf(stderr, "Error: failed to query tmux panes: %s\n", errText)
		return fmt.Errorf("tmux list-panes: %w", err)
	}

	// ── Runtimes to scan (independent of the single preferred runtime) ──
	var runtimes []string
	for _, rt := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(rt); err == nil {
			runtimes = append(runtimes, rt)
		}
	}
	if len(runtimes) == 0 {
		_, _ = fmt.Fprintln(stderr, "Error: no container runtime (docker/podman) found on PATH.")
		return fmt.Errorf("no container runtime found")
	}

	reapedCount := 0
	for _, rt := range runtimes {
		out, err := exec.Command(rt, "ps", "--format", "{{.Names}}").Output()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: failed to list running containers for %s.\n", rt)
			return fmt.Errorf("%s ps: %w", rt, err)
		}

		for _, container := range splitLines(string(out)) {
			if container == "" || !sandbox.IsSandboxContainerName(container) {
				continue
			}

			// -u root: the container's persisted Config.User (from `run
			// --user root`) may not be root at exec time, and this environ
			// scan needs to read every process's /proc/<pid>/environ, not
			// just those owned by the default user.
			var scanOut, scanErr bytes.Buffer
			scan := exec.Command(rt, "exec", "-u", "root", container, "sh", "-c", reapScanScript)
			scan.Stdout = &scanOut
			scan.Stderr = &scanErr
			if err := scan.Run(); err != nil {
				_, _ = fmt.Fprintf(stderr, "Error: failed to scan processes in container %s: %s\n", container, strings.TrimRight(scanErr.String(), "\n"))
				return fmt.Errorf("%s exec scan: %w", rt, err)
			}

			var termPids, termStarts []string
			for _, line := range splitLines(scanOut.String()) {
				parts := strings.SplitN(line, "\t", 3)
				pid := parts[0]
				pane, start := "", ""
				if len(parts) > 1 {
					pane = parts[1]
				}
				if len(parts) > 2 {
					start = parts[2]
				}
				if pid == "" || pane == "" {
					continue
				}

				if !panePattern.MatchString(pane) {
					_, _ = fmt.Fprintf(stdout, "Note: process %s in container %s has a malformed TMUX_PANE value; skipping.\n", pid, container)
					continue
				}

				if !noTmuxServer && livePanes[pane] {
					continue
				}

				// -u root: see comment above the scan call.
				term := exec.Command(rt, "exec", "-u", "root", container, "kill", "-TERM", pid)
				term.Stdout = stdout
				term.Stderr = stderr
				if err := term.Run(); err != nil {
					_, _ = fmt.Fprintf(stderr, "Error: failed to send SIGTERM to pid %s in container %s.\n", pid, container)
					return fmt.Errorf("%s exec kill -TERM: %w", rt, err)
				}
				_, _ = fmt.Fprintf(stdout, "reaped\t%s\t%s\t%s\n", container, pid, pane)
				reapedCount++
				termPids = append(termPids, pid)
				termStarts = append(termStarts, start)
			}

			if len(termPids) == 0 {
				continue
			}
			reapSleep(time.Duration(grace * float64(time.Second)))
			for i, pid := range termPids {
				recordedStart := termStarts[i]

				// -u root: see comment above the scan call. Pid is passed as
				// an argument to the probe, never interpolated. A non-zero
				// exit here means the exec transport itself failed, not
				// "process gone" — a hard error, not swallowed.
				var probeOut, probeErr bytes.Buffer
				probe := exec.Command(rt, "exec", "-u", "root", container, "sh", "-c", reapLivenessScript, "_", pid)
				probe.Stdout = &probeOut
				probe.Stderr = &probeErr
				if err := probe.Run(); err != nil {
					_, _ = fmt.Fprintf(stderr, "Error: failed to probe liveness of pid %s in container %s: %s\n", pid, container, strings.TrimRight(probeErr.String(), "\n"))
					return fmt.Errorf("%s exec liveness probe: %w", rt, err)
				}
				probeStart := strings.TrimRight(probeOut.String(), "\n")

				// __GONE__: process already gone by probe time — no-op, not
				// an error.
				if probeStart == "__GONE__" {
					continue
				}

				// A recorded start and a probe start that are both strictly
				// numeric and differ means the pid was reused by an unrelated
				// process during the grace window — treat as already-gone,
				// skip SIGKILL. Both sides must be numeric before trusting a
				// mismatch as genuine PID reuse (a forging process can
				// corrupt its own stat field 22 or shift the scan's TSV
				// columns; both vectors are confined to the forging process's
				// own record, so this is unparseable-value handling, not a
				// security boundary). __NOSTAT__ and an empty recorded start
				// fall back to liveness-only silently; a non-empty but
				// non-numeric value on either side falls back the same way
				// but is noted since it's unexpected.
				recordedNumeric := numericPattern.MatchString(recordedStart)
				probeNumeric := numericPattern.MatchString(probeStart)
				if recordedNumeric && probeNumeric {
					if probeStart != recordedStart {
						continue
					}
				} else if (recordedStart != "" && !recordedNumeric) ||
					(probeStart != "__NOSTAT__" && !probeNumeric) {
					_, _ = fmt.Fprintf(stdout, "Note: process %s in container %s has an unparseable start-time value; proceeding best-effort.\n", pid, container)
				}

				kill := exec.Command(rt, "exec", "-u", "root", container, "kill", "-KILL", pid)
				kill.Stdout = stdout
				kill.Stderr = stderr
				if err := kill.Run(); err != nil {
					_, _ = fmt.Fprintf(stderr, "Error: failed to send SIGKILL to pid %s in container %s.\n", pid, container)
					return fmt.Errorf("%s exec kill -KILL: %w", rt, err)
				}
			}
		}
	}

	if reapedCount > 0 {
		_, _ = fmt.Fprintf(stdout, "Reaped %d orphaned process(es).\n", reapedCount)
	} else {
		_, _ = fmt.Fprintln(stdout, "No orphaned processes found.")
	}
	return nil
}
