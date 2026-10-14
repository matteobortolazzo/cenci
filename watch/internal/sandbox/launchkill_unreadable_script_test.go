package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Tests for launchProbeScript's handling of an environ it cannot read (#1253),
// run for real on the host /proc. "grep could not read this" is made
// deterministic with a fake grep rather than file permissions, which would never
// bite when the tests run as root.

// fakeGrep returns a directory holding a grep that exits rc for any call whose
// last argument is one of environPaths and otherwise delegates to the real grep.
// The caller prepends the directory to the probe's PATH.
func fakeGrep(t *testing.T, rc int, environPaths ...string) string {
	t.Helper()
	realGrep, err := exec.LookPath("grep")
	if err != nil {
		t.Skipf("no grep to delegate to: %v", err)
	}
	var cases strings.Builder
	for _, path := range environPaths {
		fmt.Fprintf(&cases, "  %s) exit %d ;;\n", path, rc)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nfor a; do last=$a; done\ncase \"$last\" in\n" + cases.String() + "esac\nexec " + realGrep + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "grep"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake grep: %v", err)
	}
	return dir
}

func environPath(pid int) string { return fmt.Sprintf("/proc/%d/environ", pid) }

func pathWith(dir string) string {
	return "PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

// A process whose environ the probe could not read cannot be matched to any
// launch, so the script reports it as an "__UNREADABLE__<TAB>pid" marker row
// instead of silently treating it as a non-match -- a blind probe must not look
// like a clean one. Readable matches are still printed.
func TestLaunchProbeScript_UnreadableEnvironIsReportedAndReadableMatchesStillListed(t *testing.T) {
	requireLinux(t)

	match := spawnChildWithEnv(t, []string{LaunchIDEnvName + "=" + launchIDOne})
	blind := spawnChildWithEnv(t, []string{LaunchIDEnvName + "=" + launchIDOne})
	dir := fakeGrep(t, 2, environPath(blind))

	rows, unreadable, _ := runLaunchProbeScriptWithUnreadable(t, launchIDOne, pathWith(dir))

	if !unreadable[strconv.Itoa(blind)] {
		t.Errorf("pid %d with an unreadable environ was not reported (unreadable: %v)", blind, unreadable)
	}
	if _, ok := rows[strconv.Itoa(blind)]; ok {
		t.Errorf("pid %d was listed as a match although its environ was unreadable", blind)
	}
	if _, ok := rows[strconv.Itoa(match)]; !ok {
		t.Errorf("readable match pid %d was not listed (rows: %v)", match, rows)
	}
}

// grep failing for any reason other than "no match" (here: it cannot run at
// all) blinds the probe; the process must not pass as a clean non-match.
func TestLaunchProbeScript_GrepFailingIsReportedNotTreatedAsNoMatch(t *testing.T) {
	requireLinux(t)

	child := spawnChildWithEnv(t, []string{LaunchIDEnvName + "=" + launchIDOne})
	dir := fakeGrep(t, 127, environPath(child))

	rows, unreadable, _ := runLaunchProbeScriptWithUnreadable(t, launchIDOne, pathWith(dir))

	if !unreadable[strconv.Itoa(child)] {
		t.Errorf("pid %d was silently treated as a non-match when grep failed (unreadable: %v)", child, unreadable)
	}
	if _, ok := rows[strconv.Itoa(child)]; ok {
		t.Errorf("pid %d was listed as a match although grep failed", child)
	}
}

// Only processes of the probe's own user can be the launch's: another user's
// process (a root helper in the container) is unreadable to the unprivileged
// probe by design, and reporting it would make every close look blind.
func TestLaunchProbeScript_OtherUsersUnreadableProcessIsNotReported(t *testing.T) {
	requireLinux(t)

	other := otherUserPID(t)
	control := spawnChildWithEnv(t, []string{LaunchIDEnvName + "=" + launchIDOne})
	dir := fakeGrep(t, 2, environPath(other), environPath(control))

	_, unreadable, _ := runLaunchProbeScriptWithUnreadable(t, launchIDOne, pathWith(dir))

	if !unreadable[strconv.Itoa(control)] {
		t.Fatalf("control pid %d (same user) was not reported, so the case proves nothing (unreadable: %v)", control, unreadable)
	}
	if unreadable[strconv.Itoa(other)] {
		t.Errorf("pid %d belongs to another user but was reported as unreadable", other)
	}
}

// otherUserPID returns the pid of a live process (not PID 1) owned by a
// different real user than the test, or skips when the host has none.
func otherUserPID(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skipf("cannot read /proc: %v", err)
	}
	mine := strconv.Itoa(os.Getuid())
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
				if f := strings.Fields(rest); len(f) > 0 && f[0] != mine {
					return pid
				}
				break
			}
		}
	}
	t.Skip("no process owned by another user on this host")
	return 0
}
