package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// reapScanScript is the minimal POSIX in-container scan (originally ported
// verbatim from sandbox/cenci-sand's REAP_SCAN_SCRIPT; it now runs inside the
// container via `exec -u dev sh -c`; see ScanContainer's comment for why,
// #361 for the environ-unreadable diagnostics added below, #373 for the
// null-delimited match below, #1007 for the 4th socket field, and #1252 for
// the 5th launch-ID field and the field sanitization): emits
// "<pid>\t<pane>\t<start>\t<socket>\t<launch id>" for every process whose
// environ carries a TMUX_PANE key (pane may be empty; <start> is
// /proc/<pid>/stat field 22, read in the same loop pass, empty if the stat
// line couldn't be read/parsed; <socket> is that process's CENCI_TMUX_SOCKET
// value, empty for a legacy process that never carried the var; <launch id>
// is its CENCI_LAUNCH_ID value, empty for a process from before launch IDs),
// skipping processes lacking the TMUX_PANE key entirely. The TMUX_PANE,
// CENCI_TMUX_SOCKET and CENCI_LAUNCH_ID lines are matched directly against the
// raw NUL-delimited environ file via `grep --null-data` (the long-form flag,
// not `-z`, since some grep-compatible tools repurpose `-z` for
// `--decompress`) instead of translating NUL to newline first — a process
// fully controls its own environ, so a NUL→newline translation would let an
// embedded literal newline inside some other variable's value forge what
// looks like a `TMUX_PANE=` or `CENCI_TMUX_SOCKET=` line. The matched values
// are still process-controlled and may themselves carry a newline or tab, so
// each is sanitized in-script before it is printed: a pane with anything
// outside `%0-9`, a socket containing a control character, or a launch ID
// with any byte outside lowercase hex (the digits are spelled out, never an
// `a-f` range, so locale collation cannot change matching) is replaced by
// malformedFieldMarker, which no host-side shape check accepts — so a hostile
// value can neither forge a row nor shift a column, and fails open host-side.
// If a process's environ fails to read but the process still exists, the scan
// instead emits a raw I/O fact — `__UNREADABLE__\t<pid>` — rather than
// silently skipping it. All classification — skip-empty-pane,
// skip-malformed-pane, skip-init, launch-ID and (socket, pane) liveness,
// fail-open on a missing/malformed socket or launch ID, reap-dead, and
// counting/reporting __UNREADABLE__ markers (including PID-1 exclusion) —
// happens host-side in OrphanScanner.ScanContainer, never inside this canned
// scan; the reaper's TERM→KILL escalation, PID-reuse detection, and no-tmux
// handling stay in launcher.ReapOrphans.
const reapScanScript = `for e in /proc/[0-9]*/environ; do [ -f "$e" ] || continue; p=${e#/proc/}; p=${p%/environ}; line=$(grep --null-data --text --only-matching --max-count=1 '^TMUX_PANE=.*' "$e" 2>/dev/null); rc=$?; if [ "$rc" -eq 2 ]; then [ -e "$e" ] && printf "__UNREADABLE__\t%s\n" "$p"; continue; fi; [ "$rc" -eq 0 ] || continue; pane=${line#TMUX_PANE=}; case "$pane" in *[!%0123456789]*) pane=` + malformedFieldMarker + `;; esac; sock=$(grep --null-data --text --only-matching --max-count=1 '^CENCI_TMUX_SOCKET=.*' "$e" 2>/dev/null); sockrc=$?; if [ "$sockrc" -eq 0 ]; then sock=${sock#CENCI_TMUX_SOCKET=}; else sock=""; fi; case "$sock" in *[[:cntrl:]]*) sock=` + malformedFieldMarker + `;; esac; lid=$(grep --null-data --text --only-matching --max-count=1 '^` + LaunchIDEnvName + `=.*' "$e" 2>/dev/null); lidrc=$?; if [ "$lidrc" -eq 0 ]; then lid=${lid#` + LaunchIDEnvName + `=}; else lid=""; fi; case "$lid" in *[!0123456789abcdef]*) lid=` + malformedFieldMarker + `;; esac; st=$(cat "/proc/$p/stat" 2>/dev/null); st=${st##*) }; set -- $st; if [ "$#" -ge 20 ]; then shift 19; start=$1; else start=""; fi; printf "%s\t%s\t%s\t%s\t%s\n" "$p" "$pane" "$start" "$sock" "$lid"; done`

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
// diagnostic in launcher/reap_test.go).
var socketPattern = regexp.MustCompile(`^/[^\t\n]{0,1000}$`)

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
// the scan always passes an explicit -S, TMUX/TMUX_PANE are never in the
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
// never block a scan indefinitely. Mirrors internal/dispatch/mainsync.go's
// gitTimeout/gitWaitDelay convention, scaled down for a local socket call
// rather than a network one.
const tmuxQueryTimeout = 5 * time.Second

// tmuxQueryWaitDelay bounds how long Cmd.Wait can block after tmuxQueryTimeout's
// context has already killed the process, in case a lingering grandchild keeps
// the stdout/stderr pipes open (same rationale as mainsync.go's gitWaitDelay).
const tmuxQueryWaitDelay = 2 * time.Second

// containerCmdTimeout bounds each container-runtime call the scan makes (`ps`
// and the in-container `exec` scan), so a hung runtime daemon or a stuck
// container can never stall a reap sweep or a dispatch pass indefinitely.
// Mirrors internal/dispatch/mainsync.go's gitTimeout convention; the caller's
// own ctx deadline still applies on top of it.
const containerCmdTimeout = 30 * time.Second

// containerCmdWaitDelay bounds how long Cmd.Wait can block after
// containerCmdTimeout's context has already killed the runtime process (same
// rationale as mainsync.go's gitWaitDelay).
const containerCmdWaitDelay = 5 * time.Second

// OrphanRowClass is the host-side classification of one scanned process row.
// The precedence ScanContainer applies is: malformed pane, container init,
// missing/malformed/unresolved socket, malformed launch ID, then liveness: a
// row carrying a launch ID is live iff that ID is recorded (@cenci-launch-id)
// on a live pane of its socket (strict: a live pane without that ID does not
// keep it alive); a row without one is live iff its (socket, pane) pair is a
// live pane (the pre-#1252 rule).
type OrphanRowClass int

const (
	// OrphanMalformedPane: the TMUX_PANE value is not a real tmux pane id
	// (rows with an empty pane are dropped before classification). Never a
	// target.
	OrphanMalformedPane OrphanRowClass = iota
	// OrphanContainerInit: PID 1, the container init. Never a target,
	// whatever its pane says.
	OrphanContainerInit
	// OrphanNoSocket: no CENCI_TMUX_SOCKET, a malformed one, or one that did
	// not resolve. Fails open: treated as live, never a target.
	OrphanNoSocket
	// OrphanLive: the row's launch ID is recorded on a live pane of its
	// socket, or (no launch ID) its (socket, pane) pair is a live pane.
	OrphanLive
	// OrphanDeadPane: dead pane or dead launch -- the launch ID is recorded on
	// no live pane of its socket, or (no launch ID) the (socket, pane) pair is
	// absent from the socket's live pane set (or the socket has no tmux
	// server): the only reap target.
	OrphanDeadPane
	// OrphanMalformedLaunchID: the CENCI_LAUNCH_ID value is not 32 lowercase
	// hex (including the scan's malformed marker). Fails open: treated as live,
	// never a target.
	OrphanMalformedLaunchID
)

// OrphanScanRow is one scanned process with its classification.
type OrphanScanRow struct {
	PID, Pane, Start, Socket string
	// LaunchID is the scan's 5th field: the process's CENCI_LAUNCH_ID, empty
	// for a process from before launch IDs, or the malformed marker for an
	// unsafe value (#1252).
	LaunchID string
	Class    OrphanRowClass
}

// ContainerOrphanScan is one container's classified scan: Rows in scan order,
// plus the aggregate counts the reaper reports as Notes.
type ContainerOrphanScan struct {
	Rows []OrphanScanRow
	// UnreadableCount counts __UNREADABLE__ markers, excluding PID 1.
	UnreadableCount int
	// NoSocketCount counts rows classified OrphanNoSocket.
	NoSocketCount int
	// MalformedLaunchIDCount counts rows classified OrphanMalformedLaunchID.
	MalformedLaunchIDCount int
}

// ScanError is returned by ScanContainer when the in-container scan command
// itself fails; Stderr carries the scan's trimmed stderr so the reaper can
// print its "failed to scan processes" diagnostic.
type ScanError struct {
	Runtime, Container, Stderr string
	Err                        error
}

func (e *ScanError) Error() string {
	msg := fmt.Sprintf("%s exec scan: %v", e.Runtime, e.Err)
	if stderr := strings.TrimSpace(e.Stderr); stderr != "" {
		msg += ": " + logSafeText(stderr)
	}
	return msg
}

func (e *ScanError) Unwrap() error { return e.Err }

// socketLiveness is the memoized per-socket resolution result: the set of
// live pane ids on that socket, and the set of well-formed launch IDs recorded
// (@cenci-launch-id) on those live panes. Populated once per distinct socket
// per sweep by resolveSocketLiveness and cached in the scanner's map.
type socketLiveness struct {
	live      map[string]bool
	launchIDs map[string]bool
}

// OrphanScanner runs the shared orphan scan: container listing, the
// in-container environ scan, and the per-row classification. It is shared by
// launcher.ReapOrphans (which then signals dead-pane/dead-launch rows) and
// CountOrphanedSessions (which never signals anything). Socket liveness is
// memoized across every container/runtime scanned by one scanner. Stdout and
// Stderr receive the "Note:" and "Error:" diagnostics a sweep prints.
type OrphanScanner struct {
	stdout, stderr io.Writer
	socketCache    map[string]socketLiveness
}

// NewOrphanScanner returns a scanner that writes diagnostics to stdout/stderr.
func NewOrphanScanner(stdout, stderr io.Writer) *OrphanScanner {
	return &OrphanScanner{stdout: stdout, stderr: stderr, socketCache: map[string]socketLiveness{}}
}

// maxErrorTextBytes caps how much of an untrusted string (a runtime/tmux
// stderr, a container-supplied socket path) logSafeText embeds in an error.
const maxErrorTextBytes = 200

// classifierDefusers neutralise the two substrings downstream consumers
// (lazyboards) classify dispatch log lines by -- see internal/dispatch's
// formatDecision. %q alone keeps spaces, so a quoted value could still forge
// ` dispatch `/` skip:`; escaping the leading space of each occurrence removes
// the match without disturbing the rest of the text.
var classifierDefusers = strings.NewReplacer(" dispatch", `\x20dispatch`, " skip:", `\x20skip:`)

// logSafeText renders an untrusted string for embedding in an error that
// reaches the dispatch log: truncated to maxErrorTextBytes, quoted (no raw
// ESC/CR/newline), and with the log-classifier substrings defused.
func logSafeText(s string) string {
	if len(s) > maxErrorTextBytes {
		s = s[:maxErrorTextBytes] + "..."
	}
	return classifierDefusers.Replace(strconv.Quote(s))
}

// stderrSuffix renders a command's trimmed stderr as a ": <quoted>" error
// suffix, or "" when it wrote nothing.
func stderrSuffix(stderr *bytes.Buffer) string {
	if text := strings.TrimSpace(stderr.String()); text != "" {
		return ": " + logSafeText(text)
	}
	return ""
}

// classifyExecTimeout joins context.DeadlineExceeded into runErr when ctx's
// deadline (or cmd.WaitDelay) killed the process: os/exec returns a plain
// *exec.ExitError ("signal: killed") or bare exec.ErrWaitDelay otherwise, so a
// caller's errors.Is(err, context.DeadlineExceeded) would miss a real timeout
// (watch/docs/go-gotchas.md). It must run before the caller's deferred
// cancel() changes ctx.Err() to Canceled.
func classifyExecTimeout(ctx context.Context, runErr error) error {
	if ctx.Err() == context.DeadlineExceeded || errors.Is(runErr, exec.ErrWaitDelay) {
		return errors.Join(runErr, context.DeadlineExceeded)
	}
	return runErr
}

// RunningContainers lists the running sandbox containers (claude-cenci-/
// codex-cenci-/opencode-cenci- names only) on rt via `<rt> ps --format
// {{.Names}}`, bounded by containerCmdTimeout. Foreign containers are never
// returned.
func (s *OrphanScanner) RunningContainers(ctx context.Context, rt string) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, containerCmdTimeout)
	defer cancel()

	var out, errBuf bytes.Buffer
	cmd := exec.CommandContext(cctx, rt, "ps", "--format", "{{.Names}}")
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	cmd.WaitDelay = containerCmdWaitDelay
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s ps: %w%s", rt, classifyExecTimeout(cctx, err), stderrSuffix(&errBuf))
	}

	return parseNames(out.String(), ""), nil
}

// ScanContainer runs the in-container environ scan on one container, resolves
// every distinct, shape-validated socket its output references, and returns
// the rows classified in scan order. A failing scan command is a *ScanError; a
// genuine tmux failure is returned as-is (its "Error:" line already written to
// the scanner's stderr). Nothing is signaled here.
func (s *OrphanScanner) ScanContainer(ctx context.Context, rt, container string) (ContainerOrphanScan, error) {
	// -u dev: the scan runs as the fixed sandbox agent user so
	// same-uid /proc/<pid>/environ reads succeed without
	// CAP_SYS_PTRACE (root lacks it under docker's default
	// capability set, so a root-run scan cannot read dev-owned
	// agent processes — the actual reap target). Root-owned helper
	// processes are outside the target set. PID 1 is guarded
	// below as defense-in-depth, though under the dev-user scan
	// it typically won't be readable at all, so it never
	// surfaces in practice (#356).
	cctx, cancel := context.WithTimeout(ctx, containerCmdTimeout)
	defer cancel()

	var scanOut, scanErr bytes.Buffer
	scan := exec.CommandContext(cctx, rt, "exec", "-u", "dev", container, "sh", "-c", reapScanScript)
	scan.Stdout = &scanOut
	scan.Stderr = &scanErr
	scan.WaitDelay = containerCmdWaitDelay
	if err := scan.Run(); err != nil {
		return ContainerOrphanScan{}, &ScanError{
			Runtime:   rt,
			Container: container,
			Stderr:    strings.TrimRight(scanErr.String(), "\n"),
			Err:       classifyExecTimeout(cctx, err),
		}
	}

	var scanned ContainerOrphanScan
	var rows []OrphanScanRow
	for _, line := range SplitLines(scanOut.String()) {
		if strings.HasPrefix(line, "__UNREADABLE__\t") {
			pid := strings.TrimPrefix(line, "__UNREADABLE__\t")
			if pid != "1" {
				scanned.UnreadableCount++
			}
			continue
		}

		parts := strings.SplitN(line, "\t", 5)
		row := OrphanScanRow{PID: parts[0]}
		if len(parts) > 1 {
			row.Pane = parts[1]
		}
		if len(parts) > 2 {
			row.Start = parts[2]
		}
		if len(parts) > 3 {
			row.Socket = parts[3]
		}
		if len(parts) > 4 {
			row.LaunchID = parts[4]
		}
		if row.PID == "" || row.Pane == "" {
			continue
		}
		rows = append(rows, row)
	}

	// Pre-resolve every distinct, shape-validated socket this
	// container's scan output references *before* classifying anything
	// (#1007) — preserves the existing "a genuine tmux error hard-fails and
	// reaps nothing before any kill" ordering/contract
	// (tests/reap-orphans.test.sh case 6).
	for _, r := range rows {
		if r.PID == "1" || !panePattern.MatchString(r.Pane) {
			continue
		}
		if !socketPattern.MatchString(r.Socket) {
			continue
		}
		if _, err := s.resolveSocketLiveness(ctx, r.Socket); err != nil {
			return ContainerOrphanScan{}, err
		}
	}

	for _, r := range rows {
		r.Class = s.classify(r)
		switch r.Class {
		case OrphanNoSocket:
			scanned.NoSocketCount++
		case OrphanMalformedLaunchID:
			scanned.MalformedLaunchIDCount++
		}
		scanned.Rows = append(scanned.Rows, r)
	}
	return scanned, nil
}

// classify applies the precedence in OrphanRowClass's comment to one row whose
// sockets were already pre-resolved.
func (s *OrphanScanner) classify(r OrphanScanRow) OrphanRowClass {
	if !panePattern.MatchString(r.Pane) {
		return OrphanMalformedPane
	}

	// PID 1 is the container's init: signaling it destroys the
	// whole shared container and every agent session exec'd into
	// it. It can carry a (stale) TMUX_PANE on containers created
	// by pre-#356 launchers, which baked the creating pane's id
	// into the container-lifetime env at `run` time. Never a
	// valid reap target, whatever its pane says.
	if r.PID == "1" {
		return OrphanContainerInit
	}

	// No socket, or a malformed one (not an absolute path): fail
	// open — a container process fully controls its own
	// environ, and a false kill here is irreversible loss of
	// in-flight agent work. Never passed to tmux.
	if !socketPattern.MatchString(r.Socket) {
		return OrphanNoSocket
	}

	// A socket that passed socketPattern above is always resolved
	// in ScanContainer's pre-resolve loop before classification runs (same
	// pid/pane/socket guards), so this cache-miss branch is
	// currently unreachable — defensive against future drift, so
	// a genuinely unresolved socket can never silently reap
	// (fails open exactly like the no-socket/malformed-socket
	// case above).
	sl, ok := s.socketCache[r.Socket]
	if !ok {
		return OrphanNoSocket
	}

	// A row with a launch ID is judged by that ID alone: live iff it is
	// recorded on a live pane of this socket. A live pane that records no ID
	// (a pane id reused after a tmux server restart) does not keep it alive.
	// An empty value is "no launch ID", not malformed.
	if r.LaunchID != "" {
		if !ValidLaunchID(r.LaunchID) {
			return OrphanMalformedLaunchID
		}
		if sl.launchIDs[r.LaunchID] {
			return OrphanLive
		}
		return OrphanDeadPane
	}

	if sl.live[r.Pane] {
		return OrphanLive
	}
	return OrphanDeadPane
}

// socketListFormat is the `list-panes -F` format the reaper queries each
// socket with: the pane id, a literal TAB, then the pane's recorded launch ID
// (@cenci-launch-id; tmux expands an unset user option to the empty string, so
// a pane without one prints a trailing TAB and nothing after it).
const socketListFormat = "#{pane_id}\t#{" + PaneOptionLaunchID + "}"

// resolveSocketLiveness resolves and memoizes socket's live pane-id set and
// the launch IDs recorded on those panes via
// `tmux -S <socket> list-panes -a -F '#{pane_id}<TAB>#{@cenci-launch-id}'`
// (socketListFormat; a line with no tab or an empty second column is a pane
// with no ID; a non-empty line whose pane part is not a pane id, or whose
// second column is non-empty but fails ValidLaunchID, is an unexpected tmux
// format and hard-fails like any other tmux resolution error), run with a minimal
// allowlisted env (buildMinimalTmuxEnv) so no ambient secret is ever
// available to hand to a hostile peer, and bounded by tmuxQueryTimeout/
// tmuxQueryWaitDelay (and the caller's ctx) so a peer that never completes the
// handshake can't block a scan indefinitely. A "no server running" stderr
// substring resolves to an empty live set (every pane on that socket is
// therefore an orphan) with a per-socket "Note:" retaining the existing "No
// tmux server detected" substring pinned by tests/reap-orphans.test.sh case 5;
// any other tmux failure is a hard "Error:" + non-zero return, never folded
// into the negative/empty-set case (watch/docs/error-handling.md's rule
// against folding a probe error into a benign negative condition). The
// socket path is attacker-controlled, so it's interpolated with %q (not %s)
// into both diagnostics below — an escaped rendering rather than raw control
// bytes/ANSI escapes reaching reaper stdout/stderr.
func (s *OrphanScanner) resolveSocketLiveness(ctx context.Context, socket string) (socketLiveness, error) {
	if sl, ok := s.socketCache[socket]; ok {
		return sl, nil
	}

	cctx, cancel := context.WithTimeout(ctx, tmuxQueryTimeout)
	defer cancel()

	var tmuxStdout, tmuxStderr bytes.Buffer
	tmuxCmd := exec.CommandContext(cctx, "tmux", "-S", socket, "list-panes", "-a", "-F", socketListFormat)
	tmuxCmd.Stdout = &tmuxStdout
	tmuxCmd.Stderr = &tmuxStderr
	tmuxCmd.Env = buildMinimalTmuxEnv()
	tmuxCmd.WaitDelay = tmuxQueryWaitDelay

	sl := socketLiveness{live: map[string]bool{}, launchIDs: map[string]bool{}}
	if err := tmuxCmd.Run(); err == nil {
		for _, line := range SplitLines(tmuxStdout.String()) {
			if line == "" {
				continue
			}
			pane, id, _ := strings.Cut(line, "\t")
			// A line whose pane part is not a pane id means tmux did not
			// honor the format (an escaped tab, an unexpected build): folding
			// it into the live set would silently drop that pane's launch ID
			// and reap its agent. Hard-fail like any other tmux resolution
			// error, before anything is signaled. The same holds for a valid
			// pane whose launch-ID column is non-empty but malformed: dropping
			// it would class the process carrying that pane's real ID as a
			// dead pane. Only an empty column means "no launch ID".
			if !ValidPaneID(pane) || (id != "" && !ValidLaunchID(id)) {
				_, _ = fmt.Fprintf(s.stderr, "Error: unexpected tmux list-panes output on %q: line %q\n", socket, line)
				return socketLiveness{}, fmt.Errorf("tmux -S %s list-panes: unexpected output line %s", logSafeText(socket), logSafeText(line))
			}
			sl.live[pane] = true
			if id != "" {
				sl.launchIDs[id] = true
			}
		}
	} else if strings.Contains(strings.ToLower(tmuxStderr.String()), "no server running") {
		_, _ = fmt.Fprintf(s.stdout, "Note: No tmux server detected on %q; treating every TMUX_PANE-carrying process on that socket as orphaned.\n", socket)
	} else {
		errText := strings.TrimRight(tmuxStderr.String(), "\n")
		_, _ = fmt.Fprintf(s.stderr, "Error: failed to query tmux panes on %q: %q\n", socket, errText)
		return socketLiveness{}, fmt.Errorf("tmux -S %s list-panes: %w%s", logSafeText(socket), classifyExecTimeout(cctx, err), stderrSuffix(&tmuxStderr))
	}

	s.socketCache[socket] = sl
	return sl, nil
}

// CountOrphanedSessions returns the number of distinct orphaned sessions that
// `cenci sandbox reap-orphans` would classify as dead reap targets across every
// sandbox container on runtimes (#1218). A session is identified by (tmux
// socket, launch ID) when its process carries a launch ID and by (tmux socket,
// pane) when it does not (#1252), so two dead launches that both used pane %0
// count as two and a live launch on %0 never hides a dead one. It shares the
// reaper's scan and classification (OrphanScanner) and is strictly read-only:
// it issues the -u dev environ scan and the tmux list-panes query only, and
// never signals any process. Any listing, scan, or tmux failure (including a
// ctx timeout) returns an error and no partial count, so a caller can fail
// closed. The container and runtime are not part of the identity: the same
// session seen from several containers counts once.
func CountOrphanedSessions(ctx context.Context, runtimes []string, stdout, stderr io.Writer) (int, error) {
	scanner := NewOrphanScanner(stdout, stderr)
	sessions := map[[3]string]struct{}{}
	for _, rt := range runtimes {
		containers, err := scanner.RunningContainers(ctx, rt)
		if err != nil {
			return 0, err
		}
		for _, container := range containers {
			scanned, err := scanner.ScanContainer(ctx, rt, container)
			if err != nil {
				return 0, err
			}
			for _, r := range scanned.Rows {
				if r.Class == OrphanDeadPane {
					sessions[orphanSessionKey(r)] = struct{}{}
				}
			}
		}
	}
	return len(sessions), nil
}

// orphanSessionKey is the identity CountOrphanedSessions counts a dead row
// under: {"launch", socket, launch ID} for a row carrying a launch ID, else
// {"pane", socket, pane}. The leading kind keeps the two identity spaces from
// colliding.
func orphanSessionKey(r OrphanScanRow) [3]string {
	if r.LaunchID != "" {
		return [3]string{"launch", r.Socket, r.LaunchID}
	}
	return [3]string{"pane", r.Socket, r.Pane}
}

// SplitLines splits command output into lines without the trailing empty
// element a final newline would produce. Byte-wise on '\n' only (not
// bufio.Scanner, which strips '\r' and fails on lines over 64KB) so socket
// values from a container's environ parse exactly as the reaper always
// parsed them. launcher's splitLines delegates here.
func SplitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
