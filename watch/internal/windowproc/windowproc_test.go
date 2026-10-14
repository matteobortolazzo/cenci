package windowproc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/windowproc/windowproctest"
)

// Tests for the shared window-process killer (#1253). Sandbox panes run against
// the shared fake tmux/docker/podman (windowproctest) alone on PATH; host panes
// run either against real processes (the process-group contract) or, for the
// refusal rules that must never touch a real group, against the package's
// signal seams.

const (
	wpTarget    = "=main:0"
	wpLaunchID  = "0123456789abcdef0123456789abcdef"
	wpLaunchID2 = "fedcba9876543210fedcba9876543210"
	wpContainer = "claude-cenci-wp"
	// wpPanePID / wpPGID are the fake host pane pid and its (seam-reported)
	// process group; wpOwnPGID is the pretend caller's own group.
	wpPanePID = 4242
	wpPGID    = 7777
	wpOwnPGID = 5555
)

// row is one `list-panes` line.
func row(id, pid, launchID, container, runtime string) string {
	return strings.Join([]string{id, pid, launchID, container, runtime}, "\t")
}

// runKiller installs the fakes (grace window 0) and runs the killer once.
func runKiller(t *testing.T, spec windowproctest.Spec) (*windowproctest.Fakes, error) {
	t.Helper()
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	fakes := windowproctest.Install(t, spec)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return fakes, New(io.Discard).KillWindowProcesses(ctx, wpTarget)
}

func rawList(lines ...string) *string {
	out := strings.Join(lines, "\n") + "\n"
	return &out
}

func hasExec(calls []string) bool {
	return slices.ContainsFunc(calls, func(c string) bool { return strings.Contains(c, " exec ") })
}

// -- signal seam -------------------------------------------------------------

type sigRecord struct {
	pid int
	sig syscall.Signal
}

type fakeGroup struct {
	alive, ignoreTerm bool
}

// fakeSys replaces the process-signal seams: pane pids map to process groups,
// groups are alive until signaled, and every kill(2) call is recorded -- the
// liveness probe (signal 0) included, so a refusal test can prove not even a
// probe reached a group.
type fakeSys struct {
	pgids        map[int]int
	getpgidErrs  map[int]error
	groups       map[int]*fakeGroup
	calls        []sigRecord
	getpgidCalls []int
}

func installFakeSys(t *testing.T) *fakeSys {
	t.Helper()
	s := &fakeSys{pgids: map[int]int{}, getpgidErrs: map[int]error{}, groups: map[int]*fakeGroup{}}
	oldKill, oldGetpgid, oldGetpgrp := sysKill, sysGetpgid, sysGetpgrp
	sysKill = s.kill
	sysGetpgid = s.getpgid
	sysGetpgrp = func() int { return wpOwnPGID }
	t.Cleanup(func() { sysKill, sysGetpgid, sysGetpgrp = oldKill, oldGetpgid, oldGetpgrp })
	return s
}

func (s *fakeSys) getpgid(pid int) (int, error) {
	s.getpgidCalls = append(s.getpgidCalls, pid)
	if err, ok := s.getpgidErrs[pid]; ok {
		return 0, err
	}
	pgid, ok := s.pgids[pid]
	if !ok {
		return 0, syscall.ESRCH
	}
	return pgid, nil
}

func (s *fakeSys) kill(pid int, sig syscall.Signal) error {
	s.calls = append(s.calls, sigRecord{pid, sig})
	if pid >= 0 {
		return nil
	}
	g := s.groups[-pid]
	if g == nil || !g.alive {
		return syscall.ESRCH
	}
	switch sig {
	case syscall.SIGTERM:
		if !g.ignoreTerm {
			g.alive = false
		}
	case syscall.SIGKILL:
		g.alive = false
	}
	return nil
}

// signals returns the delivered (non-zero) signals as "TERM -7777"-style strings.
func (s *fakeSys) signals() []string {
	var out []string
	for _, c := range s.calls {
		switch c.sig {
		case 0:
		case syscall.SIGTERM:
			out = append(out, fmt.Sprintf("TERM %d", c.pid))
		case syscall.SIGKILL:
			out = append(out, fmt.Sprintf("KILL %d", c.pid))
		default:
			out = append(out, fmt.Sprintf("%d %d", int(c.sig), c.pid))
		}
	}
	return out
}

func hostPane() windowproctest.Pane {
	return windowproctest.Pane{ID: "%1", PanePID: fmt.Sprint(wpPanePID)}
}

// -- classification and validation ------------------------------------------

// Every pane value is validated before anything is signaled: a malformed value
// is an error naming the offending field, no host group is signaled, no
// in-container exec is issued, and pane_pid is rejected before Getpgid could
// resolve 0 to the caller's own group. Unit-level on purpose: the field matrix
// is far too large to drive through the full close flow one process at a time.
func TestKillWindowProcesses_MalformedPaneIsRejectedWithoutSignalling(t *testing.T) {
	const (
		paneIDField  = "pane_id"
		panePIDField = "pane_pid"
		launchField  = "@cenci-launch-id"
		contField    = "@cenci-container"
		runtimeField = "@cenci-runtime"
		fieldsText   = "fields"
	)
	tests := []struct {
		name string
		line string
		want []string // substrings the error must contain
	}{
		{"pane id without percent", row("5", "4242", "", "", ""), []string{paneIDField}},
		{"pane id not numeric", row("%x", "4242", "", "", ""), []string{paneIDField}},
		{"pane id empty", row("", "4242", "", "", ""), []string{paneIDField}},
		{"pane id with shell metacharacter", row("%1;id", "4242", "", "", ""), []string{paneIDField}},

		{"pane pid empty", row("%1", "", "", "", ""), []string{panePIDField}},
		{"pane pid not numeric", row("%1", "abc", "", "", ""), []string{panePIDField}},
		{"pane pid negative", row("%1", "-5", "", "", ""), []string{panePIDField}},
		{"pane pid with trailing text", row("%1", "12x", "", "", ""), []string{panePIDField}},
		{"pane pid with a leading space", row("%1", " 42", "", "", ""), []string{panePIDField}},
		{"pane pid zero", row("%1", "0", "", "", ""), []string{panePIDField}},
		{"pane pid zero padded zero", row("%1", "00", "", "", ""), []string{panePIDField}},
		{"pane pid one", row("%1", "1", "", "", ""), []string{panePIDField}},
		{"pane pid overflowing int", row("%1", "99999999999999999999", "", "", ""), []string{panePIDField}},
		{"pane pid one on a sandbox pane", row("%1", "1", wpLaunchID, wpContainer, "docker"), []string{panePIDField}},

		{"launch id too short", row("%1", "4242", "abc", wpContainer, "docker"), []string{launchField}},
		{"launch id uppercase", row("%1", "4242", strings.ToUpper(wpLaunchID), wpContainer, "docker"), []string{launchField}},
		{"launch id too long", row("%1", "4242", wpLaunchID+"0", wpContainer, "docker"), []string{launchField}},
		{"launch id not hex", row("%1", "4242", strings.Repeat("z", 32), wpContainer, "docker"), []string{launchField}},

		{"container is not a sandbox container", row("%1", "4242", wpLaunchID, "redis", "docker"), []string{contField}},
		{"container with a space", row("%1", "4242", wpLaunchID, "claude-cenci-x y", "docker"), []string{contField}},
		{"container with a shell metacharacter", row("%1", "4242", wpLaunchID, "claude-cenci-x;id", "docker"), []string{contField}},
		{"container with a slash", row("%1", "4242", wpLaunchID, "claude-cenci-x/y", "docker"), []string{contField}},

		{"runtime is a path", row("%1", "4242", wpLaunchID, wpContainer, "/usr/bin/docker"), []string{runtimeField}},
		{"runtime is unknown", row("%1", "4242", wpLaunchID, wpContainer, "nerdctl"), []string{runtimeField}},
		{"runtime wrong case", row("%1", "4242", wpLaunchID, wpContainer, "Docker"), []string{runtimeField}},
		{"runtime with trailing space", row("%1", "4242", wpLaunchID, wpContainer, "docker "), []string{runtimeField}},

		{"launch id without a container", row("%1", "4242", wpLaunchID, "", "docker"), []string{launchField, contField}},
		{"launch id without container or runtime", row("%1", "4242", wpLaunchID, "", ""), []string{launchField, contField}},

		{"a single field", "garbage", []string{fieldsText}},
		{"four fields", strings.Join([]string{"%1", "4242", "", ""}, "\t"), []string{fieldsText}},
		{"six fields", strings.Join([]string{"%1", "4242", "", "", "", "extra"}, "\t"), []string{fieldsText}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sys := installFakeSys(t)
			sys.pgids[wpPanePID] = wpPGID
			sys.groups[wpPGID] = &fakeGroup{alive: true}
			spec := windowproctest.Spec{ListOutput: rawList(tt.line)}

			fakes, err := runKiller(t, spec)

			if err == nil {
				t.Fatal("KillWindowProcesses = nil, want an error for a malformed pane")
			}
			for _, sub := range tt.want {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error = %q, want it to name %q", err, sub)
				}
			}
			if len(sys.calls) != 0 {
				t.Errorf("host signals/probes issued = %v, want none for a malformed pane", sys.calls)
			}
			if len(sys.getpgidCalls) != 0 {
				t.Errorf("Getpgid called for %v, want it never reached for a malformed pane", sys.getpgidCalls)
			}
			if calls := fakes.Calls(t); hasExec(calls) {
				t.Errorf("a runtime exec was issued for a malformed pane; calls:\n%s", strings.Join(calls, "\n"))
			}
		})
	}
}

// A newline or tab inside a user pane option forges extra well-formed five-field
// rows in the option listing, with an attacker-chosen pane_pid. Only a row that
// matches tmux's own (pane_id, pane_pid) is trusted, so the forged pid never
// reaches Getpgid or a signal -- not even as a probe -- while the genuine pane
// is still killed and the forged row is named in the error.
func TestKillWindowProcesses_ForgedOptionRowNeverSignalsItsPid(t *testing.T) {
	const forgedPID = 31337
	sys := installFakeSys(t)
	sys.pgids[wpPanePID] = wpPGID
	sys.groups[wpPGID] = &fakeGroup{alive: true}
	sys.pgids[forgedPID] = 9999
	sys.groups[9999] = &fakeGroup{alive: true}
	owned := "%1\t" + fmt.Sprint(wpPanePID) + "\n%2\t4243\n"
	spec := windowproctest.Spec{
		OwnedOutput: &owned,
		// %2's launch-id option is "x\n%9\t31337": a truncated row for %2, then a
		// forged %9 row pointing at the attacker's pid.
		ListOutput: rawList(
			row("%1", fmt.Sprint(wpPanePID), "", "", ""),
			"%2\t4243\tx",
			row("%9", fmt.Sprint(forgedPID), "", "", ""),
		),
	}

	_, err := runKiller(t, spec)

	if err == nil {
		t.Fatal("KillWindowProcesses = nil, want the forged row reported")
	}
	for _, sub := range []string{"%9", fmt.Sprint(forgedPID)} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error = %q, want it to name the forged row via %q", err, sub)
		}
	}
	if slices.Contains(sys.getpgidCalls, forgedPID) {
		t.Errorf("Getpgid called for the forged pid %d (calls %v)", forgedPID, sys.getpgidCalls)
	}
	for _, c := range sys.calls {
		if c.pid == forgedPID || c.pid == -9999 {
			t.Errorf("forged pid/group signaled: %+v (all calls %v)", c, sys.calls)
		}
	}
	if got, want := sys.signals(), []string{fmt.Sprintf("TERM -%d", wpPGID)}; !slices.Equal(got, want) {
		t.Errorf("signals = %v, want only the genuine pane's group %v", got, want)
	}
}

// One bad pane never protects or blocks its healthy sibling: the valid pane's
// launch is still killed, and the bad pane is named in the returned error.
func TestKillWindowProcesses_ValidSiblingIsStillKilledWhenAnotherPaneIsMalformed(t *testing.T) {
	// Arbitrary pane pids: the seams keep a buggy host fallback from ever
	// signaling a real process group.
	installFakeSys(t)
	spec := windowproctest.Spec{
		Launches: map[string][]windowproctest.Proc{
			wpLaunchID2: {{PID: "201", Start: "9", IgnoreTerm: true}},
		},
		ListOutput: rawList(
			row("%1", "4242", wpLaunchID, "redis", "docker"),
			row("%2", "4243", wpLaunchID2, wpContainer, "docker"),
		),
	}

	fakes, err := runKiller(t, spec)

	if err == nil {
		t.Fatal("KillWindowProcesses = nil, want the malformed pane reported")
	}
	for _, sub := range []string{"%1", "@cenci-container"} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error = %q, want it to name %q", err, sub)
		}
	}
	calls := fakes.Calls(t)
	for _, want := range []string{
		"docker exec -u root " + wpContainer + " kill -TERM 201",
		"docker exec -u root " + wpContainer + " kill -KILL 201",
	} {
		if !slices.Contains(calls, want) {
			t.Errorf("missing %q: the valid sibling pane was not killed; calls:\n%s", want, strings.Join(calls, "\n"))
		}
	}
}

// A sandbox pane is killed by launch ID through its own recorded runtime and is
// never also signaled on the host.
func TestKillWindowProcesses_SandboxPaneIsKilledInItsRuntimeNotOnTheHost(t *testing.T) {
	for _, rt := range []string{"docker", "podman"} {
		t.Run(rt, func(t *testing.T) {
			sys := installFakeSys(t)
			spec := windowproctest.Spec{
				Launches: map[string][]windowproctest.Proc{wpLaunchID: {{PID: "301", Start: "9", IgnoreTerm: true}}},
				Panes:    []windowproctest.Pane{{ID: "%1", PanePID: fmt.Sprint(wpPanePID), LaunchID: wpLaunchID, Container: wpContainer, Runtime: rt}},
			}

			fakes, err := runKiller(t, spec)

			if err != nil {
				t.Fatalf("KillWindowProcesses: %v", err)
			}
			want := windowproctest.SandboxCloseCalls(rt, wpContainer, wpLaunchID, wpTarget, "301")
			want = want[:len(want)-1] // the killer never closes the window itself
			if got := fakes.Calls(t); !slices.Equal(got, want) {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if len(sys.calls) != 0 || len(sys.getpgidCalls) != 0 {
				t.Errorf("host signals = %v, getpgid = %v, want none for a sandbox pane", sys.calls, sys.getpgidCalls)
			}
		})
	}
}

// A pane with no launch tag at all takes the host process-group path, and no
// runtime is ever exec'd for it.
func TestKillWindowProcesses_UntaggedPaneTakesTheHostPath(t *testing.T) {
	sys := installFakeSys(t)
	sys.pgids[wpPanePID] = wpPGID
	sys.groups[wpPGID] = &fakeGroup{alive: true}

	fakes, err := runKiller(t, windowproctest.Spec{Panes: []windowproctest.Pane{hostPane()}})

	if err != nil {
		t.Fatalf("KillWindowProcesses: %v", err)
	}
	if got, want := sys.signals(), []string{fmt.Sprintf("TERM -%d", wpPGID)}; !slices.Equal(got, want) {
		t.Errorf("host signals = %v, want %v", got, want)
	}
	if calls := fakes.Calls(t); hasExec(calls) {
		t.Errorf("a runtime exec was issued for a host pane; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// A pane with a container and runtime but no launch ID (the ID is written last,
// so this is a partial tag failure) still takes the host path -- but the
// container's own processes are out of reach of that path, so the error says so
// (the reaper's pane fallback is what catches them).
func TestKillWindowProcesses_PartiallyTaggedPaneTakesTheHostPathAndReportsTheContainerWasNotKilled(t *testing.T) {
	sys := installFakeSys(t)
	sys.pgids[wpPanePID] = wpPGID
	sys.groups[wpPGID] = &fakeGroup{alive: true}
	pane := windowproctest.Pane{ID: "%1", PanePID: fmt.Sprint(wpPanePID), Container: wpContainer, Runtime: "docker"}

	fakes, err := runKiller(t, windowproctest.Spec{Panes: []windowproctest.Pane{pane}})

	if err == nil {
		t.Fatal("KillWindowProcesses = nil, want the un-killed container processes reported")
	}
	for _, sub := range []string{"%1", wpContainer, "not killed", "reaper"} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error = %q, want it to contain %q", err, sub)
		}
	}
	if got, want := sys.signals(), []string{fmt.Sprintf("TERM -%d", wpPGID)}; !slices.Equal(got, want) {
		t.Errorf("host signals = %v, want the host group still terminated: %v", got, want)
	}
	if calls := fakes.Calls(t); hasExec(calls) {
		t.Errorf("a runtime exec was issued without a launch ID; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// -- host process groups (seams) ---------------------------------------------

// TERM goes to the negative pgid; a group that exited is not also KILLed.
func TestKillWindowProcesses_HostGroupThatExitsOnTermIsNotKilled(t *testing.T) {
	sys := installFakeSys(t)
	sys.pgids[wpPanePID] = wpPGID
	sys.groups[wpPGID] = &fakeGroup{alive: true}

	if _, err := runKiller(t, windowproctest.Spec{Panes: []windowproctest.Pane{hostPane()}}); err != nil {
		t.Fatalf("KillWindowProcesses: %v", err)
	}

	if got, want := sys.signals(), []string{fmt.Sprintf("TERM -%d", wpPGID)}; !slices.Equal(got, want) {
		t.Errorf("signals = %v, want %v", got, want)
	}
}

// A group that survives the (here zero) grace window is KILLed, after the TERM.
func TestKillWindowProcesses_HostGroupThatIgnoresTermIsKilled(t *testing.T) {
	sys := installFakeSys(t)
	sys.pgids[wpPanePID] = wpPGID
	sys.groups[wpPGID] = &fakeGroup{alive: true, ignoreTerm: true}

	if _, err := runKiller(t, windowproctest.Spec{Panes: []windowproctest.Pane{hostPane()}}); err != nil {
		t.Fatalf("KillWindowProcesses: %v", err)
	}

	want := []string{fmt.Sprintf("TERM -%d", wpPGID), fmt.Sprintf("KILL -%d", wpPGID)}
	if got := sys.signals(); !slices.Equal(got, want) {
		t.Errorf("signals = %v, want %v", got, want)
	}
}

// A pane process that is already gone (ESRCH on Getpgid, and no group left
// under its pid either) is closed, not failed, and nothing is signaled.
func TestKillWindowProcesses_HostPaneProcessAlreadyGoneIsNotAnError(t *testing.T) {
	sys := installFakeSys(t) // no pgid registered for the pane pid: Getpgid fails with ESRCH

	if _, err := runKiller(t, windowproctest.Spec{Panes: []windowproctest.Pane{hostPane()}}); err != nil {
		t.Fatalf("KillWindowProcesses = %v, want nil for an already-gone pane process", err)
	}
	if got := sys.signals(); len(got) != 0 {
		t.Errorf("signals = %v, want none for a gone process", got)
	}
	if want := []sigRecord{{-wpPanePID, 0}}; !slices.Equal(sys.calls, want) {
		t.Errorf("kill calls = %v, want exactly the group probe %v", sys.calls, want)
	}
}

// The pane's leader can exit while its group lives on (children of a shell that
// died): Getpgid then reports ESRCH, but the group is still there under the
// leader's pid. It is terminated and killed like any other group.
func TestKillWindowProcesses_LeaderGoneButGroupStillAliveIsStillKilled(t *testing.T) {
	sys := installFakeSys(t) // no pgid registered for the pane pid: Getpgid fails with ESRCH
	sys.groups[wpPanePID] = &fakeGroup{alive: true, ignoreTerm: true}

	if _, err := runKiller(t, windowproctest.Spec{Panes: []windowproctest.Pane{hostPane()}}); err != nil {
		t.Fatalf("KillWindowProcesses: %v", err)
	}

	want := []string{fmt.Sprintf("TERM -%d", wpPanePID), fmt.Sprintf("KILL -%d", wpPanePID)}
	if got := sys.signals(); !slices.Equal(got, want) {
		t.Errorf("signals = %v, want %v", got, want)
	}
}

// The group-under-the-leader's-pid fallback keeps the standing refusals: the
// caller's own group is never signaled even when it is reached this way.
func TestKillWindowProcesses_LeaderGoneFallbackStillRefusesTheOwnGroup(t *testing.T) {
	sys := installFakeSys(t)
	sys.groups[wpOwnPGID] = &fakeGroup{alive: true}
	pane := windowproctest.Pane{ID: "%1", PanePID: fmt.Sprint(wpOwnPGID)}

	_, err := runKiller(t, windowproctest.Spec{Panes: []windowproctest.Pane{pane}})

	if err == nil || !strings.Contains(err.Error(), "own process group") {
		t.Fatalf("error = %v, want the own-group refusal", err)
	}
	if got := sys.signals(); len(got) != 0 {
		t.Errorf("signals = %v, want none", got)
	}
}

// Any other Getpgid failure is an error with its cause kept, and signals nothing.
func TestKillWindowProcesses_GetpgidFailureIsReportedWithItsCause(t *testing.T) {
	sys := installFakeSys(t)
	sys.getpgidErrs[wpPanePID] = syscall.EPERM

	_, err := runKiller(t, windowproctest.Spec{Panes: []windowproctest.Pane{hostPane()}})

	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("error = %v, want one wrapping EPERM", err)
	}
	if len(sys.calls) != 0 {
		t.Errorf("signals = %v, want none when the group could not be resolved", sys.calls)
	}
}

// Process groups 0 and 1, and the caller's own group, are never signaled -- not
// even probed -- and each refusal is reported with its own reason.
func TestKillWindowProcesses_RefusesGroupsItMustNeverSignal(t *testing.T) {
	tests := []struct {
		name string
		pgid int
		want string
	}{
		{"process group 1", 1, "process group 1"},
		{"process group 0", 0, "process group 0"},
		{"the caller's own process group", wpOwnPGID, "own process group"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sys := installFakeSys(t)
			sys.pgids[wpPanePID] = tt.pgid
			sys.groups[tt.pgid] = &fakeGroup{alive: true}

			_, err := runKiller(t, windowproctest.Spec{Panes: []windowproctest.Pane{hostPane()}})

			if err == nil {
				t.Fatal("KillWindowProcesses = nil, want the refusal reported")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
			if len(sys.calls) != 0 {
				t.Errorf("signals/probes = %v, want none for a refused group", sys.calls)
			}
		})
	}
}

// -- window state ------------------------------------------------------------

// A window that is already gone has nothing to kill: not an error.
func TestKillWindowProcesses_GoneWindowIsNotAnError(t *testing.T) {
	sys := installFakeSys(t)

	fakes, err := runKiller(t, windowproctest.Spec{ListGone: true})

	if err != nil {
		t.Fatalf("KillWindowProcesses = %v, want nil for a window that is already gone", err)
	}
	if len(sys.calls) != 0 || hasExec(fakes.Calls(t)) {
		t.Errorf("signals = %v, calls = %v, want nothing signaled", sys.calls, fakes.Calls(t))
	}
}

// Any other list-panes failure is surfaced with tmux's message, never read as
// "nothing to kill".
func TestKillWindowProcesses_ListFailureIsReported(t *testing.T) {
	sys := installFakeSys(t)

	_, err := runKiller(t, windowproctest.Spec{ListFails: true})

	if err == nil || !strings.Contains(err.Error(), windowproctest.TmuxUnrelatedErrorText) {
		t.Fatalf("error = %v, want it to carry tmux's message %q", err, windowproctest.TmuxUnrelatedErrorText)
	}
	if len(sys.calls) != 0 {
		t.Errorf("signals = %v, want none", sys.calls)
	}
}

// A runtime probe failure (the container is gone) is reported with the
// runtime's own message.
func TestKillWindowProcesses_SandboxProbeFailureIsReported(t *testing.T) {
	installFakeSys(t)
	spec := windowproctest.Spec{
		Launches:   map[string][]windowproctest.Proc{wpLaunchID: {{PID: "301", Start: "9"}}},
		Panes:      []windowproctest.Pane{{ID: "%1", PanePID: "4242", LaunchID: wpLaunchID, Container: wpContainer, Runtime: "docker"}},
		ProbeFails: true,
	}

	_, err := runKiller(t, spec)

	if err == nil || !strings.Contains(err.Error(), windowproctest.TransportMarker) {
		t.Fatalf("error = %v, want it to carry the runtime's %q", err, windowproctest.TransportMarker)
	}
}

// The caller's context bounds every runtime exec: an expired one fails the
// kill with a deadline error instead of running the exec unbounded.
func TestKillWindowProcesses_ExpiredContextFailsTheRuntimeExecs(t *testing.T) {
	installFakeSys(t)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	fakes := windowproctest.Install(t, windowproctest.Spec{
		Launches: map[string][]windowproctest.Proc{wpLaunchID: {{PID: "301", Start: "9", IgnoreTerm: true}}},
		Panes:    []windowproctest.Pane{{ID: "%1", PanePID: "4242", LaunchID: wpLaunchID, Container: wpContainer, Runtime: "docker"}},
	})
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	err := New(io.Discard).KillWindowProcesses(ctx, wpTarget)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want one wrapping context.DeadlineExceeded", err)
	}
	if calls := fakes.Calls(t); slices.ContainsFunc(calls, func(c string) bool { return strings.Contains(c, "kill -") }) {
		t.Errorf("a signal was sent under an expired context; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// -- real host processes -----------------------------------------------------

// A host pane's process group gets SIGTERM, which ends a cooperative group
// promptly; a process started under Setsid (the babysit supervisor shape) is in
// another session and survives.
func TestKillWindowProcesses_RealGroupIsTerminatedAndSetsidProcessSurvives(t *testing.T) {
	leader, member, survivor := windowproctest.StartCooperativeGroup(t) // before the fakes replace PATH

	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "20")
	fakes := windowproctest.Install(t, windowproctest.Spec{Panes: []windowproctest.Pane{{ID: "%1", PanePID: fmt.Sprint(leader.PID)}}})

	start := time.Now()
	err := New(io.Discard).KillWindowProcesses(context.Background(), wpTarget)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("KillWindowProcesses: %v", err)
	}
	leader.RequireDeathBy(t, "group leader", syscall.SIGTERM)
	member.RequireDeathBy(t, "group member", syscall.SIGTERM)
	survivor.RequireAlive(t, "Setsid process")
	if elapsed > 10*time.Second {
		t.Errorf("took %s with a cooperative group and a 20s grace window, want an early stop", elapsed)
	}
	if calls := fakes.Calls(t); !slices.Equal(calls, windowproctest.ListCalls(wpTarget)) {
		t.Errorf("calls = %q, want exactly the two pane listings", calls)
	}
}

// A group whose leader ignores SIGTERM is SIGKILLed once the grace window is
// spent, together with the rest of the group.
func TestKillWindowProcesses_RealGroupIgnoringTermIsKilled(t *testing.T) {
	leader, member := windowproctest.StartTermIgnoringGroup(t) // before the fakes replace PATH

	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	windowproctest.Install(t, windowproctest.Spec{Panes: []windowproctest.Pane{{ID: "%1", PanePID: fmt.Sprint(leader.PID)}}})

	if err := New(io.Discard).KillWindowProcesses(context.Background(), wpTarget); err != nil {
		t.Fatalf("KillWindowProcesses: %v", err)
	}

	leader.RequireDeathBy(t, "TERM-ignoring group leader", syscall.SIGKILL)
	// With a zero grace window the KILL follows the TERM immediately, so a
	// cooperative member may be ended by either.
	member.RequireDeathBy(t, "group member", syscall.SIGTERM, syscall.SIGKILL)
}
