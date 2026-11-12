package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// requireTmux skips the test when the tmux binary isn't on PATH.
func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
}

// startScratchServer starts a detached tmux server on a scratch socket with
// one session per name in sessionNames (created in order), registers a
// kill-server cleanup, and returns the socket path, the server's pid, and a
// run helper for issuing further commands against this server. Each caller
// gets its own isolated server; server state is never shared between tests.
func startScratchServer(t *testing.T, sessionNames ...string) (socket, pid string, run func(args ...string) string) {
	t.Helper()
	if len(sessionNames) == 0 {
		t.Fatalf("startScratchServer: at least one session name required")
	}

	socket = filepath.Join(t.TempDir(), "s.sock")

	run = func(args ...string) string {
		t.Helper()
		cmd := exec.Command("tmux", append([]string{"-S", socket}, args...)...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("tmux %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return stdout.String()
	}

	for _, name := range sessionNames {
		run("new-session", "-d", "-s", name)
	}
	t.Cleanup(func() { run("kill-server") })

	pid = strings.TrimSpace(run("display-message", "-p", "#{pid}"))
	return socket, pid, run
}

// TestCurrentSessionErrorsWithoutTmuxPane asserts that CurrentSession refuses
// to guess a session when $TMUX_PANE is blank, even though $TMUX points at a
// perfectly reachable tmux server. This isolates the "no pane context"
// validation from server reachability: with a reachable server and no -t
// scoping, the unscoped `tmux display-message -p` call the old code makes
// would happily resolve to whatever session tmux picks server-wide and
// return a nil error, which is exactly the ambiguity issue #158 is about.
func TestCurrentSessionErrorsWithoutTmuxPane(t *testing.T) {
	requireTmux(t)

	socket, pid, _ := startScratchServer(t, "only")

	t.Setenv("TMUX", fmt.Sprintf("%s,%s,0", socket, pid))
	t.Setenv("TMUX_PANE", "")

	client := &ExecClient{}
	session, err := client.CurrentSession("")

	if err == nil {
		t.Fatalf("expected an error when TMUX_PANE is blank, got session %q", session)
	}
	if session != "" {
		t.Errorf("expected empty session on error, got %q", session)
	}
}

// TestCurrentSessionErrorsWithStaleTmuxPane asserts that CurrentSession
// errors when $TMUX_PANE names a pane that doesn't exist on the reachable
// server (e.g. the pane was closed after the env var was inherited). The
// unscoped call the old code makes ignores an invalid $TMUX_PANE entirely
// and falls back to resolving a real session with a nil error, silently
// masking the stale reference.
func TestCurrentSessionErrorsWithStaleTmuxPane(t *testing.T) {
	requireTmux(t)

	socket, pid, run := startScratchServer(t, "only")

	const stalePane = "%999"
	panes := run("list-panes", "-a", "-F", "#{pane_id}")
	if strings.Contains(panes, stalePane) {
		t.Fatalf("test setup invalid: %s is a real pane on the scratch server: %q", stalePane, panes)
	}

	t.Setenv("TMUX", fmt.Sprintf("%s,%s,0", socket, pid))
	t.Setenv("TMUX_PANE", stalePane)

	client := &ExecClient{}
	session, err := client.CurrentSession("")

	if err == nil {
		t.Fatalf("expected an error when TMUX_PANE points at a nonexistent pane, got session %q", session)
	}
	if session != "" {
		t.Errorf("expected empty session on error, got %q", session)
	}
}

// TestCurrentSessionTargetsTmuxPaneSession reproduces issue #158: with two
// tmux sessions open, CurrentSession must resolve the session that owns the
// pane recorded in $TMUX_PANE, not tmux's server-wide "most recently active"
// session.
//
// On tmux 3.4 (verified in this environment), the unscoped `tmux
// display-message -p` call the old code makes already happens to resolve
// via $TMUX_PANE in this exact scenario (no attached client outranks it), so
// this case alone does not distinguish fixed from unfixed code here — that
// distinction is covered by TestCurrentSessionErrorsWithoutTmuxPane and
// TestCurrentSessionErrorsWithStaleTmuxPane. This test remains valuable as a
// documented contract: it guards tmux versions/configurations where default
// (unscoped) resolution does NOT coincide with $TMUX_PANE, and it exercises
// the code path production uses end-to-end.
func TestCurrentSessionTargetsTmuxPaneSession(t *testing.T) {
	requireTmux(t)

	// Session A first, session B last: B becomes the server-wide "current"
	// session by creation/activity order, which is the ambiguity issue #158
	// describes.
	socket, pid, run := startScratchServer(t, "A", "B")

	paneID := strings.TrimSpace(run("display-message", "-t", "A", "-p", "#{pane_id}"))

	t.Setenv("TMUX", fmt.Sprintf("%s,%s,0", socket, pid))
	t.Setenv("TMUX_PANE", paneID)

	// Informational only (not asserted): logs what the unscoped resolution
	// the old code relies on returns in this scenario, for visibility into
	// whether this test is currently distinguishing fixed from unfixed
	// code on the tmux version running it.
	if unscoped, err := exec.Command("tmux", "display-message", "-p", "#{session_name}").Output(); err == nil {
		t.Logf("unscoped tmux display-message resolved to %q (informational only)", strings.TrimSpace(string(unscoped)))
	} else {
		t.Logf("unscoped tmux display-message errored: %v", err)
	}

	client := &ExecClient{}
	session, err := client.CurrentSession("")
	if err != nil {
		t.Fatalf("CurrentSession() returned error: %v", err)
	}
	if session != "A" {
		t.Errorf("CurrentSession() = %q, want %q (the pane's own session)", session, "A")
	}
}

// TestListPanesReportsTheServerPID verifies the assumption the Codex association
// check rests on: the #{pid} column of the pane listing is the tmux server pid,
// the same value as the second field of $TMUX, on every pane of the server.
// Skipped when tmux is absent.
func TestListPanesReportsTheServerPID(t *testing.T) {
	requireTmux(t)

	socket, pid, run := startScratchServer(t, "A", "B")
	if pid == "" {
		t.Fatal("scratch server reported no pid")
	}

	// A pane's own $TMUX is "<socket>,<server pid>,<session>": read the second
	// field from a real pane so the check does not lean on #{pid} alone.
	envFile := filepath.Join(t.TempDir(), "env")
	run("new-window", "-d", "-t", "A:", fmt.Sprintf("env > '%s.tmp' && mv '%s.tmp' '%s'", envFile, envFile, envFile))
	paneTMUX := readWindowEnv(t, envFile)["TMUX"]
	_, rest, _ := strings.Cut(paneTMUX, ",")
	if got, _, _ := strings.Cut(rest, ","); got != pid {
		t.Fatalf("a pane's $TMUX = %q, want the server pid %q as its second field", paneTMUX, pid)
	}

	panes, err := (&ExecClient{}).ListPanes(socket)
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	if len(panes) < 2 {
		t.Fatalf("ListPanes returned %d panes, want at least 2: %+v", len(panes), panes)
	}
	for _, p := range panes {
		if p.ServerPID != pid {
			t.Errorf("pane %s ServerPID = %q, want the tmux server pid %q", p.PaneID, p.ServerPID, pid)
		}
	}
}

// reaperListFormat is the `list-panes -F` format the orphan reaper queries a
// socket with (sandbox.socketListFormat, #1252): pane id, a literal TAB, the
// pane's recorded launch ID.
const reaperListFormat = "#{pane_id}\t#{@cenci-launch-id}"

// TestSetPaneOptionRoundTripsThroughTheReapersListFormat sets a pane user
// option on one of two panes of a real scratch server and reads it back with
// the reaper's -F format: the tagged pane prints "<pane>\t<id>", the untagged
// one prints "<pane>\t" (an unset @option expands to the empty string, it is
// not an error and not a missing column), and the tag did not leak to the other
// pane.
func TestSetPaneOptionRoundTripsThroughTheReapersListFormat(t *testing.T) {
	requireTmux(t)

	socket, _, run := startScratchServer(t, "A", "B")
	paneA := strings.TrimSpace(run("display-message", "-t", "A", "-p", "#{pane_id}"))
	paneB := strings.TrimSpace(run("display-message", "-t", "B", "-p", "#{pane_id}"))
	const launchID = "0123456789abcdef0123456789abcdef"

	if err := SetPaneOption(socket, paneA, "@cenci-launch-id", launchID); err != nil {
		t.Fatalf("SetPaneOption: %v", err)
	}

	got := map[string]string{}
	// TrimRight "\n" only: TrimSpace would also strip the trailing TAB of the
	// last line when its pane has no launch ID ("%1\t" -> "%1").
	for _, line := range strings.Split(strings.TrimRight(run("list-panes", "-a", "-F", reaperListFormat), "\n"), "\n") {
		pane, id, found := strings.Cut(line, "\t")
		if !found {
			t.Fatalf("list-panes line %q has no TAB; the format must always print the second column", line)
		}
		got[pane] = id
	}
	if got[paneA] != launchID {
		t.Errorf("pane %s launch id = %q, want %q", paneA, got[paneA], launchID)
	}
	if id, ok := got[paneB]; !ok || id != "" {
		t.Errorf("untagged pane %s = (%q, present=%v), want present with an empty id", paneB, id, ok)
	}
}

// TestSetPaneOptionFailsForAPaneThatDoesNotExist asserts a failed tmux call is
// surfaced as an error (the launcher withholds the launch ID on any failure).
func TestSetPaneOptionFailsForAPaneThatDoesNotExist(t *testing.T) {
	requireTmux(t)

	socket, _, _ := startScratchServer(t, "only")

	if err := SetPaneOption(socket, "%999", "@cenci-launch-id", "x"); err == nil {
		t.Fatal("SetPaneOption on a nonexistent pane returned nil, want an error")
	}
}

// newNamedWindow opens a detached window named name running command on a
// scratch server and records repoDir as its @cenci-repo-dir (as `cenci run`
// does), returning the window id and the pane id tmux gave it.
func newNamedWindow(t *testing.T, run func(args ...string) string, name, repoDir, command string) (windowID, paneID string) {
	t.Helper()
	ids := strings.Fields(run("new-window", "-d", "-P", "-F", "#{window_id} #{pane_id}", "-t", "main:", "-n", name, command))
	if len(ids) != 2 {
		t.Fatalf("new-window printed %q, want a window id and a pane id", ids)
	}
	run("set-option", "-w", "-t", ids[0], "@cenci-repo-dir", repoDir)
	return ids[0], ids[1]
}

// requirePaneDead polls until tmux reports the pane dead (remain-on-exit keeps
// it listed once its command has exited).
func requirePaneDead(t *testing.T, run func(args ...string) string, pane string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for strings.TrimSpace(run("display-message", "-p", "-t", pane, "#{pane_dead}")) != "1" {
		if time.Now().After(deadline) {
			t.Fatalf("pane %s never became dead", pane)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TestRetainedWindowInventoryAgainstARealServer runs the whole retained-window
// read path (#1458) on an isolated tmux server with remain-on-exit: a window
// whose command has exited stays listed with every pane dead, the inventory and
// the revalidation read agree on it, the real `pane_dead` prints exactly 0/1 so
// a live window is never reported dead, and killing its pane id closes it. The
// missing-pane wording is the one watch/docs/tmux.md calls unverified; this test
// pins it on a machine that has tmux. Skipped when tmux is absent.
func TestRetainedWindowInventoryAgainstARealServer(t *testing.T) {
	requireTmux(t)

	socket, _, run := startScratchServer(t, "main")
	run("set-option", "-g", "remain-on-exit", "on")
	retainedWin, retainedPane := newNamedWindow(t, run, "4242-implement", "/repo/retained", "true")
	liveWin, livePane := newNamedWindow(t, run, "4243-implement", "/repo/live", "sleep 300")
	requirePaneDead(t, run, retainedPane)

	inv, err := ListCleanupInventory(socket)
	if err != nil {
		t.Fatalf("ListCleanupInventory: %v", err)
	}
	if len(inv.Dropped) != 0 {
		t.Errorf("dropped = %v, want a clean inventory from a real server", inv.Dropped)
	}
	byID := map[string]CleanupWindow{}
	for _, w := range inv.Windows {
		byID[w.WindowID] = w
	}
	retained, ok := byID[retainedWin]
	if !ok {
		t.Fatalf("inventory %+v has no window %s", inv.Windows, retainedWin)
	}
	wantRetained := CleanupWindow{WindowID: retainedWin, Name: "4242-implement", RepoDir: "/repo/retained", Panes: []CleanupPane{{ID: retainedPane, Dead: true}}}
	if !reflect.DeepEqual(retained, wantRetained) {
		t.Errorf("retained window = %+v, want %+v (@cenci-repo-dir resolves through the window listing)", retained, wantRetained)
	}
	live, ok := byID[liveWin]
	if !ok {
		t.Fatalf("inventory %+v has no window %s", inv.Windows, liveWin)
	}
	if live.AllDead() || live.Panes[0].Dead {
		t.Errorf("live window = %+v, a running pane must never be reported dead", live)
	}

	// The revalidation read sees the same window as the inventory did.
	got, err := ReadCleanupWindow(socket, retainedPane)
	if err != nil {
		t.Fatalf("ReadCleanupWindow: %v", err)
	}
	if !reflect.DeepEqual(got, retained) {
		t.Errorf("revalidation = %+v, want the inventory's %+v", got, retained)
	}

	// The tmux-owned listing the process killer reads prints pane_dead as 0/1.
	procs, err := ListWindowPaneProcs(socket, retainedPane)
	if err != nil || len(procs) != 1 || procs[0].PaneDead != "1" {
		t.Errorf("ListWindowPaneProcs(retained) = %+v, %v; want one pane with pane_dead 1", procs, err)
	}
	procs, err = ListWindowPaneProcs(socket, livePane)
	if err != nil || len(procs) != 1 || procs[0].PaneDead != "0" {
		t.Errorf("ListWindowPaneProcs(live) = %+v, %v; want one pane with pane_dead 0", procs, err)
	}

	// A pane that does not exist is gone, in tmux's real words.
	_, err = ReadCleanupWindow(socket, "%9999")
	if !errors.Is(err, ErrWindowGone) {
		t.Errorf("ReadCleanupWindow(%%9999) err = %v, want errors.Is(err, ErrWindowGone)", err)
	}
	t.Logf("tmux's missing-pane error: %v", err)

	// Killing the retained window by its pane id closes exactly that window.
	if err := (&ExecClient{}).KillWindow(socket, retainedPane); err != nil {
		t.Fatalf("KillWindow(%s): %v", retainedPane, err)
	}
	if _, err := ReadCleanupWindow(socket, retainedPane); !errors.Is(err, ErrWindowGone) {
		t.Errorf("ReadCleanupWindow after the kill err = %v, want ErrWindowGone", err)
	}
	after, err := ListCleanupInventory(socket)
	if err != nil {
		t.Fatalf("ListCleanupInventory after the kill: %v", err)
	}
	for _, w := range after.Windows {
		if w.WindowID == retainedWin {
			t.Errorf("window %s is still listed after kill-window", retainedWin)
		}
	}
	if _, err := ReadCleanupWindow(socket, livePane); err != nil {
		t.Errorf("the live window did not survive the kill of the retained one: %v", err)
	}
}

// readWindowEnv polls for the file a window's `env > f.tmp && mv f.tmp f`
// command publishes (the rename makes it appear complete or not at all) and
// returns its variables, without TMUX_PANE (differs per pane by design) and `_`
// (the shell's own last-command variable).
func readWindowEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			env := make(map[string]string)
			for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
				name, value, ok := strings.Cut(line, "=")
				if !ok || name == "TMUX_PANE" || name == "_" {
					continue
				}
				env[name] = value
			}
			return env
		}
		if time.Now().After(deadline) {
			t.Fatalf("the window never wrote %s: %v", path, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// P32: a window created through a socket-carrying NewWindow (the minimal
// allowlisted client env) inherits exactly the environment of one created
// through today's bare, full-env `tmux new-window` on the same server. A new
// window's environment comes from the server, not the client, so a variable
// exported to the client only after the server started reaches neither.
func TestNewWindowMinimalEnvInheritsSameEnvironmentAsFullEnv(t *testing.T) {
	requireTmux(t)
	socket, pid, _ := startScratchServer(t, "only")

	const marker = "CENCI_P32_CLIENT_ONLY_MARKER"
	t.Setenv(marker, "leaked")
	out := t.TempDir()
	publish := func(name string) string {
		file := filepath.Join(out, name)
		return fmt.Sprintf("env > '%s.tmp' && mv '%s.tmp' '%s'", file, file, file)
	}

	client := &ExecClient{}
	if err := client.NewWindow(socket, "=only", "minimal", publish("minimal")); err != nil {
		t.Fatalf("NewWindow with a socket: %v", err)
	}
	// Today's call: no socket, so bare tmux with the full ambient environment,
	// reaching the same server through $TMUX.
	t.Setenv("TMUX", fmt.Sprintf("%s,%s,0", socket, pid))
	if err := client.NewWindow("", "=only", "full", publish("full")); err != nil {
		t.Fatalf("NewWindow without a socket: %v", err)
	}

	minimal, full := readWindowEnv(t, filepath.Join(out, "minimal")), readWindowEnv(t, filepath.Join(out, "full"))

	if len(minimal) == 0 || len(full) == 0 {
		t.Fatalf("empty environments: minimal=%d full=%d variables", len(minimal), len(full))
	}
	for name, env := range map[string]map[string]string{"minimal": minimal, "full": full} {
		if _, ok := env[marker]; ok {
			t.Errorf("the %s window inherited %s from the client", name, marker)
		}
	}
	for name, value := range full {
		if got, ok := minimal[name]; !ok || got != value {
			t.Errorf("%s: minimal window has (%q, %v), full-env window has %q", name, got, ok, value)
		}
	}
	for name := range minimal {
		if _, ok := full[name]; !ok {
			t.Errorf("%s is set in the minimal window only", name)
		}
	}
}
