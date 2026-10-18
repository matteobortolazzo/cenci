package sandbox

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/windowproc/windowproctest"
)

// Integration tests for ListLaunchProcesses and KillLaunch (#1253) against the
// shared fake docker/podman (windowproctest) alone on PATH. The probe script
// itself is covered against the real /proc in launchkill_script_test.go; here
// the runtime answers canned rows, so these cover the host side: argv shape,
// input validation, fail-closed parsing and the signal sequence.

const (
	lkContainer = "claude-cenci-kill"
	lkLaunchID  = "0123456789abcdef0123456789abcdef"
	// lkMalformedLaunchID is a second, valid launch ID whose probe output a test
	// replaces with garbage.
	lkMalformedLaunchID = "fedcba9876543210fedcba9876543210"
)

func lkProbeCall(rt, id string) string {
	return rt + " exec -u dev " + lkContainer + " sh -c " + windowproctest.LaunchProbeToken + " _ " + id
}

func lkTermCall(rt, pid string) string {
	return rt + " exec -u root " + lkContainer + " kill -TERM " + pid
}

func lkKillCall(rt, pid string) string {
	return rt + " exec -u root " + lkContainer + " kill -KILL " + pid
}

func anyCallContains(calls []string, sub string) bool {
	return slices.ContainsFunc(calls, func(c string) bool { return strings.Contains(c, sub) })
}

func lkSpec(procs ...windowproctest.Proc) windowproctest.Spec {
	return windowproctest.Spec{Launches: map[string][]windowproctest.Proc{lkLaunchID: procs}}
}

func killLaunch(t *testing.T, rt string) error {
	t.Helper()
	var stdout, stderr bytes.Buffer
	return KillLaunch(context.Background(), rt, lkContainer, lkLaunchID, 0, &stdout, &stderr)
}

// The probe is exec'd as `-u dev ... sh -c <script> _ <id>` on the requested
// runtime and its rows come back as pid/start targets, in order.
func TestListLaunchProcesses_ReturnsRowsFromTheRequestedRuntime(t *testing.T) {
	for _, rt := range []string{"docker", "podman"} {
		t.Run(rt, func(t *testing.T) {
			fakes := windowproctest.Install(t, lkSpec(
				windowproctest.Proc{PID: "101", Start: "5001"},
				windowproctest.Proc{PID: "102", Start: ""},
			))

			got, err := ListLaunchProcesses(context.Background(), rt, lkContainer, lkLaunchID)
			if err != nil {
				t.Fatalf("ListLaunchProcesses: %v", err)
			}

			want := []ProcTarget{{PID: "101", Start: "5001"}, {PID: "102", Start: ""}}
			if !slices.Equal(got, want) {
				t.Errorf("targets = %+v, want %+v", got, want)
			}
			calls := fakes.Calls(t)
			if len(calls) != 1 || calls[0] != lkProbeCall(rt, lkLaunchID) {
				t.Errorf("calls = %q, want exactly [%q]", calls, lkProbeCall(rt, lkLaunchID))
			}
		})
	}
}

// A malformed launch ID or container name never reaches the runtime: the values
// are validated before any exec, so nothing hostile can be interpolated.
func TestListLaunchProcesses_InvalidIdentityFailsBeforeAnyExec(t *testing.T) {
	cases := []struct {
		name      string
		container string
		id        string
	}{
		{"empty id", lkContainer, ""},
		{"non-hex id", lkContainer, strings.Repeat("z", 32)},
		{"uppercase id", lkContainer, strings.ToUpper(lkLaunchID)},
		{"short id", lkContainer, lkLaunchID[:31]},
		{"id with trailing newline", lkContainer, lkLaunchID + "\n"},
		{"empty container", "", lkLaunchID},
		{"foreign container", "redis", lkLaunchID},
		{"container with a space", "claude-cenci-x y", lkLaunchID},
		{"container with a shell metacharacter", "claude-cenci-x;id", lkLaunchID},
		{"container with a slash", "claude-cenci-x/y", lkLaunchID},
		{"container with a newline", "claude-cenci-x\n", lkLaunchID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakes := windowproctest.Install(t, lkSpec(windowproctest.Proc{PID: "101", Start: "5001"}))
			// Control: a valid identity does reach the runtime, so the "no call"
			// check below cannot pass just because nothing is wired up.
			if _, err := ListLaunchProcesses(context.Background(), "docker", lkContainer, lkLaunchID); err != nil {
				t.Fatalf("control ListLaunchProcesses with a valid identity: %v", err)
			}
			before := len(fakes.Calls(t))

			var stdout, stderr bytes.Buffer
			targets, err := ListLaunchProcesses(context.Background(), "docker", tc.container, tc.id)
			if err == nil {
				t.Errorf("ListLaunchProcesses = (%+v, nil), want a validation error", targets)
			}
			killErr := KillLaunch(context.Background(), "docker", tc.container, tc.id, 0, &stdout, &stderr)
			if killErr == nil {
				t.Error("KillLaunch = nil, want a validation error")
			}
			if calls := fakes.Calls(t); len(calls) != before {
				t.Errorf("calls = %q, want no runtime call for an invalid identity", calls[before:])
			}
		})
	}
}

// The runtime is validated inside each exported exec entry point, so no caller
// can skip the check: an out-of-set runtime is rejected before anything is
// executed (the fake docker/podman call log stays empty and the error names
// the runtime, rather than a lookup failure).
func TestRuntimeEntryPoints_InvalidRuntimeIsRejectedBeforeAnyExec(t *testing.T) {
	for _, rt := range []string{"sh", "../x", "", "DOCKER", "docker --privileged"} {
		t.Run("runtime "+rt, func(t *testing.T) {
			fakes := windowproctest.Install(t, lkSpec(windowproctest.Proc{PID: "101", Start: "5001"}))

			targets, listErr := ListLaunchProcesses(context.Background(), rt, lkContainer, lkLaunchID)
			names, psErr := RunningContainerNames(context.Background(), rt)

			if listErr == nil || !strings.Contains(listErr.Error(), "invalid runtime") {
				t.Errorf("ListLaunchProcesses = (%+v, %v), want an invalid-runtime error", targets, listErr)
			}
			if psErr == nil || !strings.Contains(psErr.Error(), "invalid runtime") {
				t.Errorf("RunningContainerNames = (%v, %v), want an invalid-runtime error", names, psErr)
			}
			if calls := fakes.Calls(t); len(calls) != 0 {
				t.Errorf("calls = %q, want no runtime call for an invalid runtime", calls)
			}
		})
	}
}

// Probe output is untrusted (it comes from inside the container): any row that
// is not "<digits>\t<digits-or-empty>", or that names PID 1, fails the whole
// listing closed and no signal is sent to anything.
func TestListLaunchProcesses_MalformedOutputFailsClosedAndSignalsNothing(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{"container init", "1\t100\n"},
		{"non-numeric pid", "abc\t100\n"},
		{"no start column", "101\n"},
		{"extra column", "101\t100\textra\n"},
		{"non-numeric start", "101\t10x\n"},
		{"leading space", " 101\t100\n"},
		{"a valid row followed by the init row", "101\t100\n1\t5\n"},
		{"a valid row followed by garbage", "101\t100\nnot a row\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := lkSpec(windowproctest.Proc{PID: "101", Start: "100", IgnoreTerm: true})
			spec.ProbeOutputs = map[string]string{lkMalformedLaunchID: tc.output}
			fakes := windowproctest.Install(t, spec)
			// Control: well-formed output for the other launch ID is accepted.
			if _, err := ListLaunchProcesses(context.Background(), "docker", lkContainer, lkLaunchID); err != nil {
				t.Fatalf("control ListLaunchProcesses with well-formed output: %v", err)
			}

			var stdout, stderr bytes.Buffer
			targets, err := ListLaunchProcesses(context.Background(), "docker", lkContainer, lkMalformedLaunchID)
			if err == nil {
				t.Errorf("ListLaunchProcesses = (%+v, nil), want an error", targets)
			}
			if err := KillLaunch(context.Background(), "docker", lkContainer, lkMalformedLaunchID, 0, &stdout, &stderr); err == nil {
				t.Error("KillLaunch = nil, want an error")
			}
			if calls := fakes.Calls(t); anyCallContains(calls, "kill -") {
				t.Errorf("a signal was sent despite malformed probe output; calls:\n%s", strings.Join(calls, "\n"))
			}
		})
	}
}

// A row with an empty start time is valid (the process had no readable stat):
// it is signaled, with liveness-only fallback.
func TestListLaunchProcesses_EmptyStartIsAccepted(t *testing.T) {
	spec := lkSpec()
	spec.ProbeOutputs = map[string]string{lkLaunchID: "101\t\n"}
	windowproctest.Install(t, spec)

	got, err := ListLaunchProcesses(context.Background(), "docker", lkContainer, lkLaunchID)
	if err != nil {
		t.Fatalf("ListLaunchProcesses: %v", err)
	}
	if want := []ProcTarget{{PID: "101", Start: ""}}; !slices.Equal(got, want) {
		t.Errorf("targets = %+v, want %+v", got, want)
	}
}

// A probe exec failure (the container is gone) is an error carrying the
// runtime's stderr, never "no processes".
func TestKillLaunch_ProbeTransportErrorIsReturnedAndNothingSignaled(t *testing.T) {
	spec := lkSpec(windowproctest.Proc{PID: "101", Start: "100", IgnoreTerm: true})
	spec.ProbeFails = true
	fakes := windowproctest.Install(t, spec)

	err := killLaunch(t, "docker")

	if err == nil {
		t.Fatal("KillLaunch = nil, want the probe's transport error")
	}
	if !strings.Contains(err.Error(), windowproctest.TransportMarker) {
		t.Errorf("error = %q, want the runtime stderr %q in it", err, windowproctest.TransportMarker)
	}
	if calls := fakes.Calls(t); anyCallContains(calls, "kill -") {
		t.Errorf("a signal was sent after the probe failed; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// A process that exits on SIGTERM is never SIGKILLed: the liveness check ahead
// of the KILL sees it gone.
func TestKillLaunch_ProcessThatExitsOnTermIsNotKilled(t *testing.T) {
	fakes := windowproctest.Install(t, lkSpec(windowproctest.Proc{PID: "101", Start: "100"}))

	if err := killLaunch(t, "docker"); err != nil {
		t.Fatalf("KillLaunch: %v", err)
	}

	calls := fakes.Calls(t)
	if !slices.Contains(calls, lkTermCall("docker", "101")) {
		t.Errorf("no SIGTERM was sent; calls:\n%s", strings.Join(calls, "\n"))
	}
	if anyCallContains(calls, "kill -KILL") {
		t.Errorf("SIGKILL was sent to a process that had already exited; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// A process that exited between the listing and the SIGTERM (kill fails, the
// liveness probe says gone) is skipped, not reported as a failure.
func TestKillLaunch_TermFailureOfAnAlreadyExitedProcessIsNotAnError(t *testing.T) {
	fakes := windowproctest.Install(t, lkSpec(windowproctest.Proc{PID: "101", Start: "100", ExitsBeforeTerm: true}))

	if err := killLaunch(t, "docker"); err != nil {
		t.Fatalf("KillLaunch = %v, want nil for a process that exited before SIGTERM landed", err)
	}
	if calls := fakes.Calls(t); anyCallContains(calls, "kill -KILL") {
		t.Errorf("SIGKILL was sent to an exited process; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// A SIGTERM that fails for a process that is still alive is an error naming
// the signal -- and it must not stop the launch's other processes from being
// terminated and killed.
func TestKillLaunch_TermFailureOfALiveProcessIsReportedAndOthersStillDie(t *testing.T) {
	fakes := windowproctest.Install(t, lkSpec(
		windowproctest.Proc{PID: "101", Start: "100", TermFails: true},
		windowproctest.Proc{PID: "102", Start: "200", IgnoreTerm: true},
	))

	err := killLaunch(t, "docker")

	if err == nil {
		t.Fatal("KillLaunch = nil, want the failed SIGTERM reported")
	}
	if !strings.Contains(err.Error(), "TERM") {
		t.Errorf("error = %q, want it to name the failed SIGTERM", err)
	}
	calls := fakes.Calls(t)
	for _, want := range []string{lkTermCall("docker", "102"), lkKillCall("docker", "102")} {
		if !slices.Contains(calls, want) {
			t.Errorf("missing %q: the healthy process was not escalated after the other one's TERM failed; calls:\n%s", want, strings.Join(calls, "\n"))
		}
	}
}

// A process forked after the first listing is invisible to TERM and KILL; the
// bounded re-list after the KILL pass finds it and kills it.
func TestKillLaunch_RelistKillsAProcessForkedAfterTheFirstListing(t *testing.T) {
	fakes := windowproctest.Install(t, lkSpec(
		windowproctest.Proc{PID: "101", Start: "100", IgnoreTerm: true},
		windowproctest.Proc{PID: "102", Start: "200", IgnoreTerm: true, LateFork: true},
	))

	if err := killLaunch(t, "docker"); err != nil {
		t.Fatalf("KillLaunch = %v, want nil: the late fork was found and killed by the re-list", err)
	}

	calls := fakes.Calls(t)
	lastProbe := -1
	for i, c := range calls {
		if c == lkProbeCall("docker", lkLaunchID) {
			lastProbe = i
		}
	}
	killLate := slices.Index(calls, lkKillCall("docker", "102"))
	if killLate < 0 {
		t.Fatalf("the late-forked process was never killed; calls:\n%s", strings.Join(calls, "\n"))
	}
	if probes := countCalls(calls, lkProbeCall("docker", lkLaunchID)); probes < 2 {
		t.Fatalf("launch probed %d time(s), want a re-list after the KILL pass; calls:\n%s", probes, strings.Join(calls, "\n"))
	}
	if killLate < lastProbe {
		t.Errorf("late-forked process was killed before the re-list that discovered it; calls:\n%s", strings.Join(calls, "\n"))
	}
	if first := slices.Index(calls, lkKillCall("docker", "101")); first < 0 || first > lastProbe {
		t.Errorf("the original process was not killed before the re-list; calls:\n%s", strings.Join(calls, "\n"))
	}
}

func countCalls(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want {
			n++
		}
	}
	return n
}

// A process from the first listing that is still there at the re-list survived
// SIGKILL: the close is reported as incomplete, naming the pid and the launch.
func TestKillLaunch_RelistFindingAFirstListingProcessReportsSurvivedSIGKILL(t *testing.T) {
	fakes := windowproctest.Install(t, lkSpec(
		windowproctest.Proc{PID: "101", Start: "100", IgnoreTerm: true, SurvivesKill: true},
		windowproctest.Proc{PID: "102", Start: "200", IgnoreTerm: true},
	))

	err := killLaunch(t, "docker")

	if err == nil {
		t.Fatal("KillLaunch = nil, want the process that survived SIGKILL reported")
	}
	for _, sub := range []string{"101", "survived SIGKILL", lkLaunchID} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error = %q, want it to contain %q", err, sub)
		}
	}
	if strings.Contains(err.Error(), "102") {
		t.Errorf("error = %q, must not blame pid 102, which died", err)
	}
	if calls := fakes.Calls(t); countCalls(calls, lkProbeCall("docker", lkLaunchID)) != 2 {
		t.Errorf("launch probed %d time(s), want the first listing and one re-list; calls:\n%s", countCalls(calls, lkProbeCall("docker", lkLaunchID)), strings.Join(calls, "\n"))
	}
}

// The re-list failing is reported with its cause and must not discard what the
// earlier steps already collected.
func TestKillLaunch_RelistProbeFailureIsReportedAndKeepsEarlierErrors(t *testing.T) {
	spec := lkSpec(windowproctest.Proc{PID: "101", Start: "100", IgnoreTerm: true, TermFails: true})
	spec.RelistFails = true
	fakes := windowproctest.Install(t, spec)

	err := killLaunch(t, "docker")

	if err == nil {
		t.Fatal("KillLaunch = nil, want the failures reported")
	}
	for _, sub := range []string{
		"re-listing launch " + lkLaunchID + " after SIGKILL", // the re-list failure itself
		windowproctest.TransportMarker,                       // with the runtime's cause
		"kill -TERM",                                         // the earlier SIGTERM failure, kept
	} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error = %q, want it to contain %q", err, sub)
		}
	}
	if calls := fakes.Calls(t); !slices.Contains(calls, lkKillCall("docker", "101")) {
		t.Errorf("the process was never killed before the re-list; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// Processes whose environ the probe could not read cannot be matched to a
// launch, so a clean-looking kill would be a blind one: they are reported, once,
// while the readable matches are still terminated and killed.
func TestKillLaunch_UnreadableProcessesAreReportedOnceAndReadableOnesStillDie(t *testing.T) {
	spec := lkSpec(windowproctest.Proc{PID: "101", Start: "100", IgnoreTerm: true})
	spec.Unreadable = map[string][]string{lkLaunchID: {"777", "778"}}
	fakes := windowproctest.Install(t, spec)

	err := killLaunch(t, "docker")

	if err == nil {
		t.Fatal("KillLaunch = nil, want the unreadable processes reported")
	}
	if !errors.Is(err, ErrUnreadableProcesses) {
		t.Errorf("error = %v, want one matching ErrUnreadableProcesses", err)
	}
	if want := "2 process(es) of launch " + lkLaunchID; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err, want)
	}
	if n := strings.Count(err.Error(), "unreadable"); n < 1 || strings.Count(err.Error(), "process(es) of launch") != 1 {
		t.Errorf("error = %q, want the unreadable processes reported exactly once (the re-list sees them again)", err)
	}
	calls := fakes.Calls(t)
	for _, want := range []string{lkTermCall("docker", "101"), lkKillCall("docker", "101")} {
		if !slices.Contains(calls, want) {
			t.Errorf("missing %q: the readable process was not signaled; calls:\n%s", want, strings.Join(calls, "\n"))
		}
	}
	for _, c := range calls {
		if strings.Contains(c, "777") || strings.Contains(c, "778") {
			t.Errorf("an unreadable pid was signaled: %q", c)
		}
	}
}

// ListLaunchProcesses hands back the readable matches together with a
// detectable ErrUnreadableProcesses, so a caller can use the partial result.
func TestListLaunchProcesses_UnreadableRowsComeBackAsErrUnreadableProcessesWithThePartialResult(t *testing.T) {
	spec := lkSpec(windowproctest.Proc{PID: "101", Start: "100"})
	spec.Unreadable = map[string][]string{lkLaunchID: {"777"}}
	windowproctest.Install(t, spec)

	got, err := ListLaunchProcesses(context.Background(), "docker", lkContainer, lkLaunchID)

	if !errors.Is(err, ErrUnreadableProcesses) {
		t.Fatalf("error = %v, want one matching ErrUnreadableProcesses", err)
	}
	if want := []ProcTarget{{PID: "101", Start: "100"}}; !slices.Equal(got, want) {
		t.Errorf("targets = %+v, want the readable match %+v", got, want)
	}
}

// A malformed unreadable marker is untrusted output like any other bad row: the
// listing fails closed (a hard error, not ErrUnreadableProcesses) and nothing is
// signaled.
func TestListLaunchProcesses_MalformedUnreadableRowFailsClosed(t *testing.T) {
	for name, output := range map[string]string{
		"non-numeric pid": "__UNREADABLE__\tabc\n",
		"container init":  "__UNREADABLE__\t1\n",
		"no pid":          "__UNREADABLE__\t\n",
		"extra column":    "__UNREADABLE__\t7\tx\n",
	} {
		t.Run(name, func(t *testing.T) {
			spec := lkSpec(windowproctest.Proc{PID: "101", Start: "100", IgnoreTerm: true})
			spec.ProbeOutputs = map[string]string{lkMalformedLaunchID: "101\t100\n" + output}
			fakes := windowproctest.Install(t, spec)

			targets, err := ListLaunchProcesses(context.Background(), "docker", lkContainer, lkMalformedLaunchID)

			if err == nil || errors.Is(err, ErrUnreadableProcesses) {
				t.Fatalf("ListLaunchProcesses = (%+v, %v), want a hard error", targets, err)
			}
			var stdout, stderr bytes.Buffer
			if err := KillLaunch(context.Background(), "docker", lkContainer, lkMalformedLaunchID, 0, &stdout, &stderr); err == nil {
				t.Error("KillLaunch = nil, want an error")
			}
			if calls := fakes.Calls(t); anyCallContains(calls, "kill -") {
				t.Errorf("a signal was sent despite a malformed marker; calls:\n%s", strings.Join(calls, "\n"))
			}
		})
	}
}
