package sandbox

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Tests for launchProbeScript (#1253), run for real on the host /proc under
// `sh -c`, following orphans_scan_script_test.go: a process fully controls its
// own environ, so the script must match the launch ID exactly (a whole
// NUL-delimited entry) and print nothing but "<pid>\t<start>" rows.

// probeRowPattern is the only shape the script may print for a match.
var probeRowPattern = regexp.MustCompile(`^[0-9]+\t[0-9]*$`)

// scriptUnreadableRowPattern is the shape of the marker row for a process whose
// environ could not be read.
var scriptUnreadableRowPattern = regexp.MustCompile(`^__UNREADABLE__\t([0-9]+)$`)

// runLaunchProbeScript runs the real launchProbeScript for id and returns its
// rows keyed by pid. extraEnv is appended to the probe shell's own environment
// (ambient launch-ID variables are stripped first). Every output line must have
// the "<pid>\t<start>" shape.
func runLaunchProbeScript(t *testing.T, id string, extraEnv ...string) (rows map[string]string, probePID int) {
	t.Helper()
	rows, unreadable, probePID := runLaunchProbeScriptWithUnreadable(t, id, extraEnv...)
	if len(unreadable) != 0 {
		t.Fatalf("script reported unreadable pids %v, want none", unreadable)
	}
	return rows, probePID
}

// runLaunchProbeScriptWithUnreadable is runLaunchProbeScript for tests that
// expect "__UNREADABLE__<TAB>pid" marker rows, returned as the set of pids.
func runLaunchProbeScriptWithUnreadable(t *testing.T, id string, extraEnv ...string) (rows map[string]string, unreadable map[string]bool, probePID int) {
	t.Helper()

	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, LaunchIDEnvName+"=") {
			continue
		}
		env = append(env, kv)
	}
	cmd := exec.Command("sh", "-c", launchProbeScript, "_", id)
	cmd.Env = append(env, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("launchProbeScript: %v\nstderr: %s", err, stderr.String())
	}

	rows = map[string]string{}
	unreadable = map[string]bool{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line == "" {
			continue
		}
		if m := scriptUnreadableRowPattern.FindStringSubmatch(line); m != nil {
			unreadable[m[1]] = true
			continue
		}
		if !probeRowPattern.MatchString(line) {
			t.Fatalf("script printed %q, want only <pid>\\t<start> or __UNREADABLE__\\t<pid> rows (all output: %q)", line, stdout.String())
		}
		pid, start, _ := strings.Cut(line, "\t")
		rows[pid] = start
	}
	return rows, unreadable, cmd.Process.Pid
}

// procStartTime reads /proc/<pid>/stat field 22 (starttime), the same value the
// escalation's PID-reuse check compares.
func procStartTime(t *testing.T, pid int) string {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatalf("read stat of pid %d: %v", pid, err)
	}
	s := string(data)
	idx := strings.LastIndex(s, ") ")
	if idx < 0 {
		t.Fatalf("unparseable stat for pid %d: %q", pid, s)
	}
	// After the "(comm) " prefix the fields start at field 3, so field 22 is
	// index 19.
	fields := strings.Fields(s[idx+2:])
	if len(fields) < 20 {
		t.Fatalf("stat for pid %d has %d fields after comm, want >= 20", pid, len(fields))
	}
	return fields[19]
}

func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("launchProbeScript reads /proc; Linux-only")
	}
}

// A process whose environ has exactly CENCI_LAUNCH_ID=<id> is printed with its
// pid and its real /proc start time; a process carrying another ID is not.
func TestLaunchProbeScript_ExactMatchPrintsPidAndStartTime(t *testing.T) {
	requireLinux(t)

	match := spawnChildWithEnv(t, []string{LaunchIDEnvName + "=" + launchIDOne})
	other := spawnChildWithEnv(t, []string{LaunchIDEnvName + "=" + launchIDTwo})

	rows, _ := runLaunchProbeScript(t, launchIDOne)

	start, ok := rows[strconv.Itoa(match)]
	if !ok {
		t.Fatalf("no row for pid %d carrying the exact launch ID (rows: %v)", match, rows)
	}
	if want := procStartTime(t, match); start != want {
		t.Errorf("start = %q, want %q (/proc/%d/stat field 22)", start, want, match)
	}
	if _, ok := rows[strconv.Itoa(other)]; ok {
		t.Errorf("pid %d carries a different launch ID but was listed (rows: %v)", other, rows)
	}
}

// A value that merely contains the ID, or an entry forged through an embedded
// newline, is not a match. Each case runs next to a genuine control process so
// an empty result cannot pass vacuously.
func TestLaunchProbeScript_NonExactValuesAreNotMatched(t *testing.T) {
	requireLinux(t)

	cases := []struct {
		name string
		env  string
	}{
		{"id is a prefix of the value", LaunchIDEnvName + "=" + launchIDOne + "ff"},
		{"id is a suffix of the value", LaunchIDEnvName + "=ff" + launchIDOne},
		{"id embedded in another variable's value", "OTHER_KEY=" + LaunchIDEnvName + "=" + launchIDOne},
		{"id under a longer variable name", "X" + LaunchIDEnvName + "=" + launchIDOne},
		{"newline-forged entry in another variable", "OTHER_KEY=x\n" + LaunchIDEnvName + "=" + launchIDOne},
		{"newline-extended value", LaunchIDEnvName + "=" + launchIDOne + "\nOTHER_KEY=x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			control := spawnChildWithEnv(t, []string{LaunchIDEnvName + "=" + launchIDOne})
			hostile := spawnChildWithEnv(t, []string{tc.env})

			rows, _ := runLaunchProbeScript(t, launchIDOne)

			if _, ok := rows[strconv.Itoa(control)]; !ok {
				t.Fatalf("control pid %d with the exact ID was not listed, so the case proves nothing (rows: %v)", control, rows)
			}
			if _, ok := rows[strconv.Itoa(hostile)]; ok {
				t.Errorf("pid %d with environ entry %q was matched as launch %s (rows: %v)", hostile, tc.env, launchIDOne, rows)
			}
		})
	}
}

// The probe shell itself carries the launch ID here (it inherits it), yet it
// must never list itself: the caller would otherwise signal its own probe.
func TestLaunchProbeScript_NeverPrintsItsOwnPid(t *testing.T) {
	requireLinux(t)

	control := spawnChildWithEnv(t, []string{LaunchIDEnvName + "=" + launchIDOne})

	rows, probePID := runLaunchProbeScript(t, launchIDOne, LaunchIDEnvName+"="+launchIDOne)

	if _, ok := rows[strconv.Itoa(control)]; !ok {
		t.Fatalf("control pid %d was not listed, so the case proves nothing (rows: %v)", control, rows)
	}
	if _, ok := rows[strconv.Itoa(probePID)]; ok {
		t.Errorf("the probe listed its own pid %d (rows: %v)", probePID, rows)
	}
}
