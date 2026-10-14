package sandbox

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
// "<pid>\t<pane>\t<start>\t<socket>\t<launchID>" success row or an
// "__UNREADABLE__\t<pid>" marker (unreadable == true, pane/start/socket/
// launchID unset). socket is the 4th field (#1007): CENCI_TMUX_SOCKET, empty
// for a legacy row that never carried the var. launchID is the 5th field
// (#1252): CENCI_LAUNCH_ID, empty when the process never carried the var, or
// the fixed malformedFieldMarker when the value was not hex. fields is the
// number of tab-separated fields on the row's line, so a test can prove a
// hostile value neither added nor dropped a column.
type scanRow struct {
	pane       string
	start      string
	socket     string
	launchID   string
	fields     int
	unreadable bool
}

// spawnChildWithEnv starts a long-lived child process (blocked reading
// stdin) whose /proc/<pid>/environ carries exactly the base process env plus
// envExtra, with any pre-existing TMUX_PANE/CENCI_TMUX_SOCKET/CENCI_LAUNCH_ID
// entries stripped first so a value set on the test runner itself (e.g.
// running inside a cenci sandbox, where CENCI_TMUX_SOCKET is now ambient per
// #1007 and CENCI_LAUNCH_ID per #1252) can never leak into — or race — the
// crafted entry under test. Returns the child's pid; the process is killed
// via t.Cleanup.
func spawnChildWithEnv(t *testing.T, envExtra []string) int {
	t.Helper()

	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX_PANE=") || strings.HasPrefix(kv, "CENCI_TMUX_SOCKET=") || strings.HasPrefix(kv, "CENCI_LAUNCH_ID=") {
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

// runScanScriptRaw runs the real reapScanScript constant via `sh -c` on the
// host and returns its non-empty stdout lines unparsed, so a test can prove a
// hostile environ value produced no extra line.
func runScanScriptRaw(t *testing.T) []string {
	t.Helper()

	var stdout, stderr bytes.Buffer
	cmd := exec.Command("sh", "-c", reapScanScript)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("reapScanScript: %v\nstderr: %s", err, stderr.String())
	}

	var lines []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// parseScanRows parses raw scan lines into a pid → scanRow map.
func parseScanRows(lines []string) map[string]scanRow {
	rows := map[string]scanRow{}
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if fields[0] == "__UNREADABLE__" {
			if len(fields) >= 2 {
				rows[fields[1]] = scanRow{unreadable: true}
			}
			continue
		}
		row := scanRow{fields: len(fields)}
		if len(fields) > 1 {
			row.pane = fields[1]
		}
		if len(fields) > 2 {
			row.start = fields[2]
		}
		if len(fields) > 3 {
			row.socket = fields[3]
		}
		if len(fields) > 4 {
			row.launchID = fields[4]
		}
		rows[fields[0]] = row
	}
	return rows
}

// runScanScript runs the real reapScanScript constant via `sh -c` on the
// host and parses its output into a pid → scanRow map.
func runScanScript(t *testing.T) map[string]scanRow {
	t.Helper()
	return parseScanRows(runScanScriptRaw(t))
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

// -- ticket #1252: the 5th scan field (CENCI_LAUNCH_ID) and the sanitization of
// every environ-derived field. A process fully controls its own environ, so
// each value is replaced in-script with malformedFieldMarker before printing
// when it could forge or shift a row; the host then fails open on the marker.

const (
	// scanTestSocket is a well-formed absolute socket path.
	scanTestSocket = "/tmp/tmux-1000/cenci"
	// forgedPID is a first field no genuine row can carry, so a line starting
	// with it can only have been forged through an environ value.
	forgedPID = "forgedpid"
)

// requireNoForgedRow fails the test when any raw scan line starts with the
// forged pid, i.e. a hostile environ value managed to inject its own row.
func requireNoForgedRow(t *testing.T, lines []string) {
	t.Helper()
	for _, line := range lines {
		if strings.HasPrefix(line, forgedPID+"\t") {
			t.Fatalf("a hostile environ value forged a scan row: %q (all lines: %q)", line, lines)
		}
	}
}

// requireScanRow returns the parsed row for pid or fails the test.
func requireScanRow(t *testing.T, lines []string, pid int) scanRow {
	t.Helper()
	row, ok := parseScanRows(lines)[strconv.Itoa(pid)]
	if !ok || row.unreadable {
		t.Fatalf("expected a scan row for pid %d, got none (lines: %q)", pid, lines)
	}
	return row
}

// A genuine CENCI_LAUNCH_ID reaches the 5th field exactly, and the fields
// before it stay aligned.
func TestReapScanScript_GenuineLaunchIDIsExtractedExactly(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reapScanScript reads /proc; Linux-only")
	}

	pid := spawnChildWithEnv(t, []string{"TMUX_PANE=%7", "CENCI_TMUX_SOCKET=" + scanTestSocket, "CENCI_LAUNCH_ID=" + launchIDOne})

	row := requireScanRow(t, runScanScriptRaw(t), pid)

	if row.launchID != launchIDOne {
		t.Errorf("launchID = %q, want %q exactly (no stray NUL, newline or suffix)", row.launchID, launchIDOne)
	}
	if row.fields != 5 {
		t.Errorf("row has %d tab-separated fields, want 5", row.fields)
	}
	if row.pane != "%7" || row.socket != scanTestSocket {
		t.Errorf("pane/socket = %q/%q, want %%7/%s (a 5th field must not shift the earlier ones)", row.pane, row.socket, scanTestSocket)
	}
}

// A process that never carried the variable still gets a (blank) 5th field, so
// the host can tell "no launch ID" (legacy rule) from a shifted row.
func TestReapScanScript_NoLaunchIDVariableLeavesFieldEmpty(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reapScanScript reads /proc; Linux-only")
	}

	pid := spawnChildWithEnv(t, []string{"TMUX_PANE=%7", "CENCI_TMUX_SOCKET=" + scanTestSocket})

	row := requireScanRow(t, runScanScriptRaw(t), pid)

	if row.launchID != "" {
		t.Errorf("launchID = %q, want empty for a process without CENCI_LAUNCH_ID", row.launchID)
	}
	if row.fields != 5 {
		t.Errorf("row has %d tab-separated fields, want 5 (a trailing empty launch-ID field)", row.fields)
	}
}

// AC6: a launch ID containing a newline (plus a forged row), a tab, an
// uppercase hex digit or any other non-hex byte is replaced by the marker; it
// never forges a row and never adds or drops a column.
func TestReapScanScript_MalformedLaunchIDBecomesMarker(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reapScanScript reads /proc; Linux-only")
	}

	cases := []struct {
		name  string
		value string
	}{
		{"newline followed by a forged row", "aaaa\n" + forgedPID + "\t%1\t1\t/x\t" + launchIDOne},
		{"embedded tab", "aaaa\tbbbb"},
		{"uppercase hex", strings.ToUpper(launchIDOne)},
		{"non-hex letters", strings.Repeat("z", 32)},
		{"hex with a trailing space", launchIDOne + " "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pid := spawnChildWithEnv(t, []string{"TMUX_PANE=%7", "CENCI_TMUX_SOCKET=" + scanTestSocket, "CENCI_LAUNCH_ID=" + tc.value})

			lines := runScanScriptRaw(t)
			requireNoForgedRow(t, lines)
			row := requireScanRow(t, lines, pid)

			if row.launchID != malformedFieldMarker {
				t.Errorf("launchID = %q, want the malformed marker %q", row.launchID, malformedFieldMarker)
			}
			if row.fields != 5 {
				t.Errorf("row has %d tab-separated fields, want 5 (the value must not add or drop a column)", row.fields)
			}
			if row.pane != "%7" || row.socket != scanTestSocket {
				t.Errorf("pane/socket = %q/%q, want %%7/%s", row.pane, row.socket, scanTestSocket)
			}
		})
	}
}

// Q2: the same defence for the TMUX_PANE field. A pane value with anything
// outside %0-9 (here an embedded newline carrying a forged row) becomes the
// marker, so it can neither forge a row nor shift the later fields.
func TestReapScanScript_PaneWithEmbeddedNewlineBecomesMarker(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reapScanScript reads /proc; Linux-only")
	}

	pid := spawnChildWithEnv(t, []string{
		"TMUX_PANE=%7\n" + forgedPID + "\t%1\t1\t/x\t" + launchIDOne,
		"CENCI_TMUX_SOCKET=" + scanTestSocket,
		"CENCI_LAUNCH_ID=" + launchIDOne,
	})

	lines := runScanScriptRaw(t)
	requireNoForgedRow(t, lines)
	row := requireScanRow(t, lines, pid)

	if row.pane != malformedFieldMarker {
		t.Errorf("pane = %q, want the malformed marker %q", row.pane, malformedFieldMarker)
	}
	if row.fields != 5 || row.socket != scanTestSocket || row.launchID != launchIDOne {
		t.Errorf("fields/socket/launchID = %d/%q/%q, want 5/%s/%s (the pane value must not shift later fields)", row.fields, row.socket, row.launchID, scanTestSocket, launchIDOne)
	}
}

// Q2: a socket value containing any control character (newline, tab, ESC) is
// replaced by the marker, so it can neither forge a row nor shift the launch-ID
// field; the host then treats the row as having no usable socket (fail open).
func TestReapScanScript_SocketWithControlCharacterBecomesMarker(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reapScanScript reads /proc; Linux-only")
	}

	cases := []struct {
		name  string
		value string
	}{
		{"newline followed by a forged row", scanTestSocket + "\n" + forgedPID + "\t%1\t1\t/x\t" + launchIDOne},
		{"embedded tab", scanTestSocket + "\textra"},
		{"escape sequence", scanTestSocket + "\x1b[31m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pid := spawnChildWithEnv(t, []string{"TMUX_PANE=%7", "CENCI_TMUX_SOCKET=" + tc.value, "CENCI_LAUNCH_ID=" + launchIDOne})

			lines := runScanScriptRaw(t)
			requireNoForgedRow(t, lines)
			row := requireScanRow(t, lines, pid)

			if row.socket != malformedFieldMarker {
				t.Errorf("socket = %q, want the malformed marker %q", row.socket, malformedFieldMarker)
			}
			if row.fields != 5 || row.pane != "%7" || row.launchID != launchIDOne {
				t.Errorf("fields/pane/launchID = %d/%q/%q, want 5/%%7/%s (the socket value must not shift later fields)", row.fields, row.pane, row.launchID, launchIDOne)
			}
		})
	}
}
