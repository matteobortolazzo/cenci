package launcher

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
)

// reapScanScript is the minimal POSIX in-container scan (originally ported
// verbatim from sandbox/cenci-sand's REAP_SCAN_SCRIPT; it now runs inside the
// container via `exec -u dev sh -c`; see the call site's comment for why,
// #361 for the environ-unreadable diagnostics added below, #373 for the
// null-delimited match below, and #1007 for the 4th socket field): emits
// "<pid>\t<pane>\t<start>\t<socket>" for every process whose environ carries
// a TMUX_PANE key (pane may be empty; <start> is /proc/<pid>/stat field 22,
// read in the same loop pass, empty if the stat line couldn't be
// read/parsed; <socket> is that process's CENCI_TMUX_SOCKET value, empty for
// a legacy process that never carried the var), skipping processes lacking
// the TMUX_PANE key entirely. Both the TMUX_PANE and CENCI_TMUX_SOCKET lines
// are matched directly against the raw NUL-delimited environ file via `grep
// --null-data` (the long-form flag, not `-z`, since some grep-compatible
// tools repurpose `-z` for `--decompress`) instead of translating NUL to
// newline first — a process fully controls its own environ, so a
// NUL→newline translation would let an embedded literal newline inside some
// other variable's value forge what looks like a `TMUX_PANE=` or
// `CENCI_TMUX_SOCKET=` line. If a process's environ fails to read but the
// process still exists, the scan instead emits a raw I/O fact —
// `__UNREADABLE__\t<pid>` — rather than silently skipping it. All
// classification — skip-empty-pane, skip-malformed-pane, skip-init,
// skip-live-pane (now (socket, pane) pair matching, #1007), fail-open on a
// missing/malformed socket, reap-dead-pane, TERM→KILL escalation, PID-reuse
// detection, no-tmux handling, and counting/reporting __UNREADABLE__ markers
// (including PID-1 exclusion) — happens host-side in ReapOrphans, never
// inside this canned scan.
const reapScanScript = `for e in /proc/[0-9]*/environ; do [ -f "$e" ] || continue; p=${e#/proc/}; p=${p%/environ}; line=$(grep --null-data --text --only-matching --max-count=1 '^TMUX_PANE=.*' "$e" 2>/dev/null); rc=$?; if [ "$rc" -eq 2 ]; then [ -e "$e" ] && printf "__UNREADABLE__\t%s\n" "$p"; continue; fi; [ "$rc" -eq 0 ] || continue; sock=$(grep --null-data --text --only-matching --max-count=1 '^CENCI_TMUX_SOCKET=.*' "$e" 2>/dev/null); sockrc=$?; if [ "$sockrc" -eq 0 ]; then sock=${sock#CENCI_TMUX_SOCKET=}; else sock=""; fi; st=$(cat "/proc/$p/stat" 2>/dev/null); st=${st##*) }; set -- $st; if [ "$#" -ge 20 ]; then shift 19; start=$1; else start=""; fi; printf "%s\t%s\t%s\t%s\n" "$p" "${line#TMUX_PANE=}" "$start" "$sock"; done`

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

// socketPattern validates a CENCI_TMUX_SOCKET value before it is ever passed
// to `tmux -S` (#1007): an absolute path, containing no tab or newline
// (mirrors panePattern's shape-validation style), and length-bounded so a
// pathological value can't be used to overload the classification path
// (Go's regexp repeat-count ceiling caps this at 1000). A
// container process fully controls its own environ, so anything else is
// malformed and — exactly like a missing socket — fails open: never
// signaled, never passed to tmux (see legacyNoSocketNoteText's aggregated
// diagnostic in reap_test.go).
var socketPattern = regexp.MustCompile(`^/[^\t\n]{0,1000}$`)

// numericPattern validates start times before trusting a mismatch as genuine
// PID reuse (a forging process can corrupt its own stat field 22).
var numericPattern = regexp.MustCompile(`^[0-9]+$`)

// reapSleep is the SIGTERM→SIGKILL grace sleep, a package var so tests could
// stub it; CENCI_SANDBOX_REAP_GRACE_SECS already tunes the duration to 0 for
// fast test runs, matching the bash suite's usage.
var reapSleep = time.Sleep

// socketLiveness is the memoized per-socket resolution result: the set of
// live pane ids on that socket. Populated once per distinct socket per sweep
// by resolveSocketLiveness and cached in the caller-supplied map.
type socketLiveness struct {
	live map[string]bool
}

// parsedScanRow is one parsed data-carrying line from reapScanScript's
// output (an __UNREADABLE__ marker line is handled separately and never
// becomes a parsedScanRow).
type parsedScanRow struct {
	pid, pane, start, socket string
}

// tmuxEnvAllowlist is the minimal set of env vars a `tmux -S <path>` child
// process needs to run correctly. tmux forwards its full process environment
// to whatever server it connects to (the MSG_IDENTIFY_ENVIRON/
// update-environment handshake), and CENCI_TMUX_SOCKET is attacker-controlled
// — any container process can set it to point at a socket it controls. Rather
// than scrubbing a couple of ambient vars (TMUX/TMUX_PANE) out of the full
// os.Environ() and forwarding everything else, buildMinimalTmuxEnv builds an
// explicit allowlisted env from scratch, so no secret ambient var (API keys,
// tokens, ...) is ever available to hand to a hostile peer if a malformed
// socket path does get dialed. This does not restrict *which* socket path
// gets dialed (deferred to future #646 work per the plan's Risks section) —
// only what's in that connection's environment.
var tmuxEnvAllowlist = []string{"PATH", "HOME", "TERM", "LANG"}

// buildMinimalTmuxEnv builds the child env for a `tmux -S <path>` process:
// only the vars in tmuxEnvAllowlist, each included only if actually set in
// the current process env (never fabricated). watch/docs/tmux.md documents
// that unscoped tmux target resolution honors an ambient $TMUX_PANE; since
// ReapOrphans always passes an explicit -S, TMUX/TMUX_PANE are never in the
// allowlist and so are never forwarded — the reaper's own self-exec'd
// invocation (internal/reap.ExecReaper) routinely carries a $TMUX_PANE that
// doesn't exist on the socket being queried.
func buildMinimalTmuxEnv() []string {
	out := make([]string, 0, len(tmuxEnvAllowlist))
	for _, key := range tmuxEnvAllowlist {
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
		}
	}
	return out
}

// tmuxQueryTimeout bounds the `tmux -S <socket> list-panes` call
// resolveSocketLiveness makes: CENCI_TMUX_SOCKET is attacker-controlled, so a
// peer that accepts the connection and never completes tmux's handshake must
// never block the reaper indefinitely. Mirrors internal/dispatch/mainsync.go's
// gitTimeout/gitWaitDelay convention, scaled down for a local socket call
// rather than a network one.
const tmuxQueryTimeout = 5 * time.Second

// tmuxQueryWaitDelay bounds how long Cmd.Wait can block after tmuxQueryTimeout's
// context has already killed the process, in case a lingering grandchild keeps
// the stdout/stderr pipes open (same rationale as mainsync.go's gitWaitDelay).
const tmuxQueryWaitDelay = 2 * time.Second

// resolveSocketLiveness resolves and memoizes socket's live pane-id set via
// `tmux -S <socket> list-panes -a -F '#{pane_id}'`, run with a minimal
// allowlisted env (buildMinimalTmuxEnv) so no ambient secret is ever
// available to hand to a hostile peer, and bounded by tmuxQueryTimeout/
// tmuxQueryWaitDelay so a peer that never completes the handshake can't block
// the reaper indefinitely. A "no server running" stderr substring resolves to
// an empty live set (every pane on that socket is therefore an orphan) with a
// per-socket "Note:" retaining the existing "No tmux server detected"
// substring pinned by tests/reap-orphans.test.sh case 5; any other tmux
// failure is a hard "Error:" + non-zero return, never folded into the
// negative/empty-set case (watch/docs/error-handling.md's rule against
// folding a probe error into a benign negative condition). The socket path is
// attacker-controlled, so it's interpolated with %q (not %s) into both
// diagnostics below — an escaped rendering rather than raw control
// bytes/ANSI escapes reaching reaper stdout/stderr.
func resolveSocketLiveness(cache map[string]socketLiveness, socket string, stdout, stderr io.Writer) (socketLiveness, error) {
	if sl, ok := cache[socket]; ok {
		return sl, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), tmuxQueryTimeout)
	defer cancel()

	var tmuxStdout, tmuxStderr bytes.Buffer
	tmuxCmd := exec.CommandContext(ctx, "tmux", "-S", socket, "list-panes", "-a", "-F", "#{pane_id}")
	tmuxCmd.Stdout = &tmuxStdout
	tmuxCmd.Stderr = &tmuxStderr
	tmuxCmd.Env = buildMinimalTmuxEnv()
	tmuxCmd.WaitDelay = tmuxQueryWaitDelay

	sl := socketLiveness{live: map[string]bool{}}
	if err := tmuxCmd.Run(); err == nil {
		for _, pane := range splitLines(tmuxStdout.String()) {
			if pane != "" {
				sl.live[pane] = true
			}
		}
	} else if strings.Contains(strings.ToLower(tmuxStderr.String()), "no server running") {
		_, _ = fmt.Fprintf(stdout, "Note: No tmux server detected on %q; treating every TMUX_PANE-carrying process on that socket as orphaned.\n", socket)
	} else {
		errText := strings.TrimRight(tmuxStderr.String(), "\n")
		_, _ = fmt.Fprintf(stderr, "Error: failed to query tmux panes on %q: %q\n", socket, errText)
		return socketLiveness{}, fmt.Errorf("tmux -S %s list-panes: %w", socket, err)
	}

	cache[socket] = sl
	return sl, nil
}

// ReapOrphans kills container-side agent processes whose owning tmux pane no
// longer exists on the host. Ownership is recorded via the TMUX_PANE and
// CENCI_TMUX_SOCKET env vars injected into every exec'd session (#1007),
// present in /proc/<pid>/environ of every container-side agent process and
// descendants. A process is live iff its (socket, pane) pair is present in
// that socket's resolved live-pane set — never a union of pane ids across
// sockets, since both a personal default tmux server and the `cenci` server
// allocate pane ids from %0. Every distinct, shape-validated socket a
// container's scan output references is resolved at most once per sweep
// (memoized across containers/runtimes) before any signal is sent in that
// container. A row carrying no socket (pre-#1007 launcher) or a malformed
// one fails open: treated as live, never signaled — a false kill here is
// irreversible loss of in-flight agent work. It is a global sweep (not
// scoped to the current repo) and scans every installed runtime, since
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

	// Memoized per-socket live-pane resolution, shared across every
	// container/runtime in this sweep (#1007).
	socketCache := map[string]socketLiveness{}

	// ── Runtimes to scan (independent of the single preferred runtime) ──
	runtimes, err := sandbox.AvailableRuntimes()
	if err != nil {
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

			// -u dev: the scan runs as the fixed sandbox agent user so
			// same-uid /proc/<pid>/environ reads succeed without
			// CAP_SYS_PTRACE (root lacks it under docker's default
			// capability set, so a root-run scan cannot read dev-owned
			// agent processes — the actual reap target). Root-owned helper
			// processes are outside the target set. PID 1 is guarded
			// below as defense-in-depth, though under the dev-user scan
			// it typically won't be readable at all, so it never
			// surfaces in practice (#356).
			var scanOut, scanErr bytes.Buffer
			scan := exec.Command(rt, "exec", "-u", "dev", container, "sh", "-c", reapScanScript)
			scan.Stdout = &scanOut
			scan.Stderr = &scanErr
			if err := scan.Run(); err != nil {
				_, _ = fmt.Fprintf(stderr, "Error: failed to scan processes in container %s: %s\n", container, strings.TrimRight(scanErr.String(), "\n"))
				return fmt.Errorf("%s exec scan: %w", rt, err)
			}

			var rows []parsedScanRow
			unreadableCount := 0
			for _, line := range splitLines(scanOut.String()) {
				if strings.HasPrefix(line, "__UNREADABLE__\t") {
					pid := strings.TrimPrefix(line, "__UNREADABLE__\t")
					if pid != "1" {
						unreadableCount++
					}
					continue
				}

				parts := strings.SplitN(line, "\t", 4)
				pid := parts[0]
				pane, start, socket := "", "", ""
				if len(parts) > 1 {
					pane = parts[1]
				}
				if len(parts) > 2 {
					start = parts[2]
				}
				if len(parts) > 3 {
					socket = parts[3]
				}
				if pid == "" || pane == "" {
					continue
				}
				rows = append(rows, parsedScanRow{pid: pid, pane: pane, start: start, socket: socket})
			}

			// Pre-resolve every distinct, shape-validated socket this
			// container's scan output references *before* sending any
			// signal in this container (#1007) — preserves the existing "a
			// genuine tmux error hard-fails and reaps nothing before any
			// kill" ordering/contract (tests/reap-orphans.test.sh case 6).
			for _, r := range rows {
				if r.pid == "1" || !panePattern.MatchString(r.pane) {
					continue
				}
				if !socketPattern.MatchString(r.socket) {
					continue
				}
				if _, err := resolveSocketLiveness(socketCache, r.socket, stdout, stderr); err != nil {
					return err
				}
			}

			var termPids, termStarts []string
			noSocketCount := 0
			for _, r := range rows {
				pid, pane, start, socket := r.pid, r.pane, r.start, r.socket

				if !panePattern.MatchString(pane) {
					_, _ = fmt.Fprintf(stdout, "Note: process %s in container %s has a malformed TMUX_PANE value; skipping.\n", pid, container)
					continue
				}

				// PID 1 is the container's init: signaling it destroys the
				// whole shared container and every agent session exec'd into
				// it. It can carry a (stale) TMUX_PANE on containers created
				// by pre-#356 launchers, which baked the creating pane's id
				// into the container-lifetime env at `run` time. Never a
				// valid reap target, whatever its pane says.
				if pid == "1" {
					_, _ = fmt.Fprintf(stdout, "Note: process 1 in container %s is the container init; skipping.\n", container)
					continue
				}

				// No socket, or a malformed one (not an absolute path): fail
				// open — a container process fully controls its own
				// environ, and a false kill here is irreversible loss of
				// in-flight agent work. Never passed to tmux. Aggregated
				// into one Note per container (mirroring the unreadableCount
				// idiom below) rather than one line per process.
				if !socketPattern.MatchString(socket) {
					noSocketCount++
					continue
				}

				// A socket that passed socketPattern above is always resolved
				// in the pre-resolve loop before this loop runs (same
				// pid/pane/socket guards), so this cache-miss branch is
				// currently unreachable — defensive against future drift, so
				// a genuinely unresolved socket can never silently reap
				// (fails open exactly like the no-socket/malformed-socket
				// case above, via the same noSocketCount aggregation).
				sl, ok := socketCache[socket]
				if !ok {
					noSocketCount++
					continue
				}
				if sl.live[pane] {
					continue
				}

				// -u root: `docker run --user X` persists as the container's
				// Config.User for the container's lifetime, not just its
				// initial process, so every exec call site must carry its
				// own explicit -u rather than relying on the container's
				// default user (see sandbox project's CLAUDE.md). Signaling
				// stays root regardless of which user the scan runs as.
				term := exec.Command(rt, "exec", "-u", "root", container, "kill", "-TERM", pid)
				term.Stdout = stdout
				term.Stderr = stderr
				if err := term.Run(); err != nil {
					// A pid can legitimately exit between the scan and the
					// signal. One guaranteed source on containers created by
					// pre-#356 launchers: the scan's own in-container sh
					// inherits the container-lifetime TMUX_PANE (docker/podman
					// exec merges Config.Env), so once that baked pane goes
					// stale the scan reports itself as an orphan — and it has
					// always exited by kill time. Probe before classifying:
					// __GONE__ means the TERM raced an exit, not a failure.
					if start, perr := probeStartTime(rt, container, pid); perr == nil && start == "__GONE__" {
						_, _ = fmt.Fprintf(stdout, "Note: process %s in container %s exited before SIGTERM could be delivered; skipping.\n", pid, container)
						continue
					}
					_, _ = fmt.Fprintf(stderr, "Error: failed to send SIGTERM to pid %s in container %s.\n", pid, container)
					return fmt.Errorf("%s exec kill -TERM: %w", rt, err)
				}
				_, _ = fmt.Fprintf(stdout, "reaped\t%s\t%s\t%s\n", container, pid, pane)
				reapedCount++
				termPids = append(termPids, pid)
				termStarts = append(termStarts, start)
			}

			// (pre-#1007 launcher): the ticket reference stays in this
			// comment, not in the printed string below — a raw GitHub issue
			// number in a user-facing CLI diagnostic is opaque to an operator
			// without repo context.
			if noSocketCount > 0 {
				_, _ = fmt.Fprintf(stdout, "Note: %d process(es) in container %s carried no CENCI_TMUX_SOCKET; failing open and treating as live so no in-flight agent work is ever killed.\n", noSocketCount, container)
			}

			if unreadableCount > 0 {
				_, _ = fmt.Fprintf(stdout, "Note: %d process environ(s) in container %s were unreadable during the -u dev scan; if this persists it may mean the scan user's UID no longer matches the agent process UID, so orphans could go undetected.\n", unreadableCount, container)
			}

			if len(termPids) == 0 {
				continue
			}
			reapSleep(time.Duration(grace * float64(time.Second)))
			for i, pid := range termPids {
				recordedStart := termStarts[i]

				// -u root: `docker run --user X` persists as the container's
				// Config.User for the container's lifetime, so this exec
				// call site carries its own explicit -u (see sandbox
				// project's CLAUDE.md). A non-zero exit here means the exec
				// transport itself failed, not "process gone" — a hard
				// error, not swallowed.
				probeStart, err := probeStartTime(rt, container, pid)
				if err != nil {
					_, _ = fmt.Fprintf(stderr, "Error: failed to probe liveness of pid %s in container %s: %v\n", pid, container, err)
					return fmt.Errorf("%s exec liveness probe: %w", rt, err)
				}

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

// probeStartTime runs the in-container liveness/identity probe for pid and
// returns its output: the process's current /proc stat start time, __GONE__
// (process gone), or __NOSTAT__ (alive but unverifiable). Pid is passed as an
// argument to the probe, never interpolated. An error means the exec
// transport itself failed; the probe's stderr is folded into the error text.
func probeStartTime(rt, container, pid string) (string, error) {
	// -u root: `docker run --user X` persists as the container's Config.User
	// for the container's lifetime, so this exec call site carries its own
	// explicit -u (see sandbox project's CLAUDE.md). The probe stays root
	// regardless of which user the scan runs as.
	var probeOut, probeErr bytes.Buffer
	probe := exec.Command(rt, "exec", "-u", "root", container, "sh", "-c", reapLivenessScript, "_", pid)
	probe.Stdout = &probeOut
	probe.Stderr = &probeErr
	if err := probe.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimRight(probeErr.String(), "\n"))
	}
	return strings.TrimRight(probeOut.String(), "\n"), nil
}
