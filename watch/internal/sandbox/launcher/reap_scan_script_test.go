package launcher

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// scanRow is one parsed line of reapScanScript's output: either a
// "<pid>\t<pane>\t<start>\t<socket>" success row or an
// "__UNREADABLE__\t<pid>" marker (unreadable == true, pane/start/socket
// unset). socket is the 4th field (#1007): CENCI_TMUX_SOCKET, empty for a
// legacy row that never carried the var.
type scanRow struct {
	pane       string
	start      string
	socket     string
	unreadable bool
}

// spawnChildWithEnv starts a long-lived child process (blocked reading
// stdin) whose /proc/<pid>/environ carries exactly the base process env plus
// envExtra, with any pre-existing TMUX_PANE/CENCI_TMUX_SOCKET entries
// stripped first so a value set on the test runner itself (e.g. running
// inside a cenci sandbox, where CENCI_TMUX_SOCKET is now ambient per #1007)
// can never leak into — or race — the crafted entry under test. Returns the
// child's pid; the process is killed via t.Cleanup.
func spawnChildWithEnv(t *testing.T, envExtra []string) int {
	t.Helper()

	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX_PANE=") || strings.HasPrefix(kv, "CENCI_TMUX_SOCKET=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, envExtra...)

	cmd := exec.Command("cat")
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// runScanScript runs the real reapScanScript constant via `sh -c` on the
// host and parses its output into a pid → scanRow map.
func runScanScript(t *testing.T) map[string]scanRow {
	t.Helper()

	var stdout, stderr bytes.Buffer
	cmd := exec.Command("sh", "-c", reapScanScript)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("reapScanScript: %v\nstderr: %s", err, stderr.String())
	}

	rows := map[string]scanRow{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if fields[0] == "__UNREADABLE__" {
			if len(fields) >= 2 {
				rows[fields[1]] = scanRow{unreadable: true}
			}
			continue
		}
		row := scanRow{}
		if len(fields) > 1 {
			row.pane = fields[1]
		}
		if len(fields) > 2 {
			row.start = fields[2]
		}
		if len(fields) > 3 {
			row.socket = fields[3]
		}
		rows[fields[0]] = row
	}
	return rows
}

// TestReapScanScript_EmbeddedNewlineDoesNotForgeTmuxPane is the regression
// case for #373: a process controls its own environ, so before the
// null-delimited-match fix, a single env entry whose value embedded a
// literal newline followed by "TMUX_PANE=<forged>" could fool the old
// tr("\0","\n")-then-grep extraction into reporting the forged pane as if it
// were a real environ entry.
func TestReapScanScript_EmbeddedNewlineDoesNotForgeTmuxPane(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reapScanScript reads /proc; Linux-only")
	}

	pid := spawnChildWithEnv(t, []string{"OTHER_KEY=x\nTMUX_PANE=%99"})

	rows := runScanScript(t)

	if row, ok := rows[strconv.Itoa(pid)]; ok && !row.unreadable && row.pane == "%99" {
		t.Fatalf("scan reported forged pane %%99 for pid %d via an embedded newline in OTHER_KEY (row: %+v)", pid, row)
	}
}

// TestReapScanScript_GenuineTmuxPaneIsExtracted proves the rewritten
// null-delimited-match extraction still works for the ordinary case: a
// process with one genuine TMUX_PANE entry (no embedded newline).
func TestReapScanScript_GenuineTmuxPaneIsExtracted(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reapScanScript reads /proc; Linux-only")
	}

	pid := spawnChildWithEnv(t, []string{"TMUX_PANE=%7"})

	rows := runScanScript(t)

	row, ok := rows[strconv.Itoa(pid)]
	if !ok || row.unreadable {
		t.Fatalf("expected a scan row for pid %d with pane %%7, got none (rows: %+v)", pid, rows)
	}
	if row.pane != "%7" {
		t.Errorf("pane = %q, want %%7", row.pane)
	}
	if row.start == "" {
		t.Errorf("start time missing for pid %d in row %+v", pid, row)
	}
}

// TestReapScanScript_GenuineSocketIsExtracted proves the scan's 4th field
// (#1007) is populated from a genuine CENCI_TMUX_SOCKET entry (no embedded
// newline), the same extraction pattern as TMUX_PANE.
func TestReapScanScript_GenuineSocketIsExtracted(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reapScanScript reads /proc; Linux-only")
	}

	pid := spawnChildWithEnv(t, []string{"TMUX_PANE=%7", "CENCI_TMUX_SOCKET=/tmp/tmux-1000/cenci"})

	rows := runScanScript(t)

	row, ok := rows[strconv.Itoa(pid)]
	if !ok || row.unreadable {
		t.Fatalf("expected a scan row for pid %d, got none (rows: %+v)", pid, rows)
	}
	if row.socket != "/tmp/tmux-1000/cenci" {
		t.Errorf("socket = %q, want /tmp/tmux-1000/cenci", row.socket)
	}
}

// TestReapScanScript_EmbeddedNewlineDoesNotForgeSocket is the #1007
// counterpart to TestReapScanScript_EmbeddedNewlineDoesNotForgeTmuxPane: a
// process controls its own environ, so an env entry whose value embeds a
// literal newline followed by a forged "CENCI_TMUX_SOCKET=<value>" line must
// not be extracted as if it were a genuine environ entry.
func TestReapScanScript_EmbeddedNewlineDoesNotForgeSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reapScanScript reads /proc; Linux-only")
	}

	pid := spawnChildWithEnv(t, []string{"TMUX_PANE=%7", "OTHER_KEY=x\nCENCI_TMUX_SOCKET=/forged/socket"})

	rows := runScanScript(t)

	if row, ok := rows[strconv.Itoa(pid)]; ok && !row.unreadable && row.socket == "/forged/socket" {
		t.Fatalf("scan reported forged socket %q for pid %d via an embedded newline in OTHER_KEY (row: %+v)", "/forged/socket", pid, row)
	}
}

// TestReapScanScript_PaneWithoutSocketLeavesSocketEmpty covers the legacy
// (pre-#1007) shape: a process carrying TMUX_PANE but no CENCI_TMUX_SOCKET
// must still be scanned (pane extracted normally), with the socket field
// left empty rather than erroring or omitting the row.
func TestReapScanScript_PaneWithoutSocketLeavesSocketEmpty(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reapScanScript reads /proc; Linux-only")
	}

	pid := spawnChildWithEnv(t, []string{"TMUX_PANE=%9"})

	rows := runScanScript(t)

	row, ok := rows[strconv.Itoa(pid)]
	if !ok || row.unreadable {
		t.Fatalf("expected a scan row for pid %d with pane %%9, got none (rows: %+v)", pid, rows)
	}
	if row.pane != "%9" {
		t.Errorf("pane = %q, want %%9", row.pane)
	}
	if row.socket != "" {
		t.Errorf("socket = %q, want empty (legacy no-socket case)", row.socket)
	}
}
