package tmux

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	session, err := client.CurrentSession()

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
	session, err := client.CurrentSession()

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
	session, err := client.CurrentSession()
	if err != nil {
		t.Fatalf("CurrentSession() returned error: %v", err)
	}
	if session != "A" {
		t.Errorf("CurrentSession() = %q, want %q (the pane's own session)", session, "A")
	}
}
