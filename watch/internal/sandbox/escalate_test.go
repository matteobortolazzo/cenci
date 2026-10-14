package sandbox

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/windowproc/windowproctest"
)

// Tests for the TERM -> poll -> KILL escalation shared by the orphan reaper and
// the close-path kill (#1253). Everything runs against the shared fake docker
// (windowproctest) alone on PATH, with a fake clock standing in for the poll
// sleeps so the grace window costs no wall time.

const (
	escContainer = "claude-cenci-esc"
	escLaunchID  = "0123456789abcdef0123456789abcdef"
	escPID       = "1000"
	escStart     = "5000"
	// escReusedStart is the start time of an unrelated process that was handed
	// escPID after the original exited.
	escReusedStart = "9000"
	escGrace       = 5 * time.Second
	escMaxPoll     = time.Second
)

// fakeClock advances only when the escalation sleeps.
type fakeClock struct {
	t     time.Time
	slept time.Duration
}

func installFakeClock(t *testing.T) *fakeClock {
	t.Helper()
	c := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	oldSleep, oldNow := sleep, now
	sleep = func(d time.Duration) {
		c.slept += d
		c.t = c.t.Add(d)
	}
	now = func() time.Time { return c.t }
	t.Cleanup(func() { sleep, now = oldSleep, oldNow })
	return c
}

func escalationFixture(t *testing.T, p windowproctest.Proc, spec windowproctest.Spec) (*windowproctest.Fakes, *fakeClock, []ProcTarget) {
	t.Helper()
	p.PID, p.Start = escPID, escStart
	spec.Launches = map[string][]windowproctest.Proc{escLaunchID: {p}}
	fakes := windowproctest.Install(t, spec)
	return fakes, installFakeClock(t), []ProcTarget{{PID: escPID, Start: escStart}}
}

func livenessCall(rt, pid string) string {
	return rt + " exec -u root " + escContainer + " sh -c " + windowproctest.LivenessToken + " _ " + pid
}

func killCall(rt, pid string) string {
	return rt + " exec -u root " + escContainer + " kill -KILL " + pid
}

func requireNoKill(t *testing.T, calls []string) {
	t.Helper()
	for _, c := range calls {
		if strings.Contains(c, "kill -KILL") {
			t.Errorf("a process that should not be killed was sent SIGKILL; calls:\n%s", strings.Join(calls, "\n"))
			return
		}
	}
}

// The wait after SIGTERM ends as soon as every target reports gone: it does
// not sleep out the whole grace window, and the KILL pass then has nothing to do.
func TestAwaitExit_StopsEarlyOnceTargetsAreGone(t *testing.T) {
	fakes, clock, targets := escalationFixture(t, windowproctest.Proc{Dead: true}, windowproctest.Spec{})

	AwaitExit(context.Background(), "docker", escContainer, targets, escGrace)

	if clock.slept >= escGrace {
		t.Errorf("slept %s of a %s grace window for an already-gone process, want an early stop", clock.slept, escGrace)
	}
	calls := fakes.Calls(t)
	if len(calls) == 0 || calls[0] != livenessCall("docker", escPID) {
		t.Fatalf("calls = %q, want the wait to start with a liveness probe of pid %s", calls, escPID)
	}

	var stdout, stderr bytes.Buffer
	if err := KillSurvivors(context.Background(), "docker", escContainer, targets, &stdout, &stderr); err != nil {
		t.Fatalf("KillSurvivors: %v\nstderr: %s", err, stderr.String())
	}
	requireNoKill(t, fakes.Calls(t))
}

// A process that survives SIGTERM is polled until the grace window is spent
// (the cap), then SIGKILLed after a final liveness check.
func TestAwaitExit_PollsUntilGraceCapThenKillSurvivorsKills(t *testing.T) {
	const grace = 2 * time.Second
	fakes, clock, targets := escalationFixture(t, windowproctest.Proc{IgnoreTerm: true}, windowproctest.Spec{})

	AwaitExit(context.Background(), "docker", escContainer, targets, grace)

	if clock.slept < grace {
		t.Errorf("slept %s, want the full %s grace window for a process that never exits", clock.slept, grace)
	}
	if clock.slept > grace+escMaxPoll {
		t.Errorf("slept %s, want the wait capped at the %s grace window", clock.slept, grace)
	}
	polled := fakes.Calls(t)
	if len(polled) == 0 {
		t.Fatal("no liveness probe was issued while waiting out the grace window")
	}
	for _, c := range polled {
		if c != livenessCall("docker", escPID) {
			t.Errorf("wait issued %q, want only liveness probes of pid %s", c, escPID)
		}
	}

	var stdout, stderr bytes.Buffer
	if err := KillSurvivors(context.Background(), "docker", escContainer, targets, &stdout, &stderr); err != nil {
		t.Fatalf("KillSurvivors: %v\nstderr: %s", err, stderr.String())
	}
	calls := fakes.Calls(t)
	if len(calls) < len(polled)+2 {
		t.Fatalf("calls = %q, want a liveness probe and a KILL after the polling", calls)
	}
	tail := calls[len(polled):]
	want := []string{livenessCall("docker", escPID), killCall("docker", escPID)}
	if strings.Join(tail, "\n") != strings.Join(want, "\n") {
		t.Errorf("KILL pass = %q, want %q (liveness check, then SIGKILL)", tail, want)
	}
}

// Grace <= 0 means the caller wants no wait at all: no probe, no sleep.
func TestAwaitExit_NonPositiveGraceIssuesNoProbes(t *testing.T) {
	for name, grace := range map[string]time.Duration{"zero": 0, "negative": -time.Second} {
		t.Run(name, func(t *testing.T) {
			fakes, clock, targets := escalationFixture(t, windowproctest.Proc{IgnoreTerm: true}, windowproctest.Spec{})

			AwaitExit(context.Background(), "docker", escContainer, targets, grace)

			if calls := fakes.Calls(t); len(calls) != 0 {
				t.Errorf("calls = %q, want no runtime call for grace %s", calls, grace)
			}
			if clock.slept != 0 {
				t.Errorf("slept %s, want no sleep for grace %s", clock.slept, grace)
			}

			// The KILL pass must then go straight to its own single liveness
			// check and the SIGKILL: no probe came from the wait before it.
			var stdout, stderr bytes.Buffer
			if err := KillSurvivors(context.Background(), "docker", escContainer, targets, &stdout, &stderr); err != nil {
				t.Fatalf("KillSurvivors: %v\nstderr: %s", err, stderr.String())
			}
			want := []string{livenessCall("docker", escPID), killCall("docker", escPID)}
			if got := fakes.Calls(t); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("calls = %q, want %q", got, want)
			}
		})
	}
}

// A pid whose start time changed was reused by an unrelated process: the wait
// treats it as gone, and the KILL pass must not signal it.
func TestKillSurvivors_ReusedPIDIsNeverKilled(t *testing.T) {
	fakes, clock, targets := escalationFixture(t, windowproctest.Proc{ReusedStart: escReusedStart}, windowproctest.Spec{})

	AwaitExit(context.Background(), "docker", escContainer, targets, escGrace)
	if clock.slept >= escGrace {
		t.Errorf("slept %s, want an early stop once the pid's start time changed", clock.slept)
	}

	var stdout, stderr bytes.Buffer
	if err := KillSurvivors(context.Background(), "docker", escContainer, targets, &stdout, &stderr); err != nil {
		t.Fatalf("KillSurvivors: %v\nstderr: %s", err, stderr.String())
	}
	calls := fakes.Calls(t)
	if len(calls) == 0 {
		t.Fatal("no liveness probe was issued, so the PID-reuse check was never exercised")
	}
	requireNoKill(t, calls)
}

// A liveness probe whose exec transport fails ends the wait, is never read as
// "gone", and comes back as an error from the KILL pass with the cause in it.
func TestKillSurvivors_ProbeTransportErrorIsReturnedAndNothingKilled(t *testing.T) {
	fakes, clock, targets := escalationFixture(t, windowproctest.Proc{IgnoreTerm: true}, windowproctest.Spec{LivenessFails: true})

	AwaitExit(context.Background(), "docker", escContainer, targets, escGrace)
	if clock.slept >= escGrace {
		t.Errorf("slept %s, want the failing probe to end the wait early", clock.slept)
	}

	var stdout, stderr bytes.Buffer
	err := KillSurvivors(context.Background(), "docker", escContainer, targets, &stdout, &stderr)
	if err == nil {
		t.Fatal("KillSurvivors = nil, want the liveness probe's transport error")
	}
	if !strings.Contains(err.Error(), windowproctest.TransportMarker) {
		t.Errorf("error = %q, want the probe's stderr (%q) in it", err, windowproctest.TransportMarker)
	}
	wantLine := "failed to probe liveness of pid " + escPID + " in container " + escContainer
	if !strings.Contains(stderr.String(), wantLine) {
		t.Errorf("stderr = %q, want it to report %q", stderr.String(), wantLine)
	}
	requireNoKill(t, fakes.Calls(t))
}

// GraceFromEnv parses CENCI_SANDBOX_REAP_GRACE_SECS. This is a unit test on
// purpose: the edge-input matrix (unset, fractional, negative, garbage) is
// cheaper to pin here than to drive through a runtime fake, and a negative value
// used to reach time.Sleep unclamped.
func TestGraceFromEnv(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"unset defaults to 5s", "", 5 * time.Second},
		{"zero", "0", 0},
		{"whole seconds", "2", 2 * time.Second},
		{"fractional seconds", "1.5", 1500 * time.Millisecond},
		{"negative clamps to zero", "-3", 0},
		{"unparseable falls back to the default", "soon", 5 * time.Second},
		// ParseFloat accepts these: without the explicit guards a huge value would
		// overflow time.Duration and NaN/Inf would reach the arithmetic.
		{"huge value clamps to the cap", "1e12", time.Hour},
		{"NaN falls back to the default", "NaN", 5 * time.Second},
		{"positive infinity falls back to the default", "Inf", 5 * time.Second},
		{"negative infinity falls back to the default", "-Inf", 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", tt.value)
			if got := GraceFromEnv(); got != tt.want {
				t.Errorf("GraceFromEnv() = %s, want %s", got, tt.want)
			}
		})
	}
}

// killSurvivorsFixture installs the fakes for several processes of one launch
// (all ignoring SIGTERM unless a field says otherwise) and returns them as the
// KILL pass's targets, in order.
func killSurvivorsFixture(t *testing.T, procs ...windowproctest.Proc) (*windowproctest.Fakes, []ProcTarget) {
	t.Helper()
	targets := make([]ProcTarget, len(procs))
	for i := range procs {
		procs[i].IgnoreTerm = true
		targets[i] = ProcTarget{PID: procs[i].PID, Start: procs[i].Start}
	}
	fakes := windowproctest.Install(t, windowproctest.Spec{Launches: map[string][]windowproctest.Proc{escLaunchID: procs}})
	return fakes, targets
}

// A SIGKILL that fails because the process exited in the window between the
// liveness check and the signal is benign -- the same verdict SignalTerm
// reaches for a failed SIGTERM. The re-probe says gone, so nothing is reported
// and the remaining targets are still killed.
func TestKillSurvivors_KillFailureOfAnAlreadyExitedProcessIsNotAnErrorAndTheRestStillDie(t *testing.T) {
	fakes, targets := killSurvivorsFixture(t,
		windowproctest.Proc{PID: "1000", Start: "5000", ExitsBeforeKill: true},
		windowproctest.Proc{PID: "1001", Start: "5001"},
	)

	var stdout, stderr bytes.Buffer
	err := KillSurvivors(context.Background(), "docker", escContainer, targets, &stdout, &stderr)

	if err != nil {
		t.Fatalf("KillSurvivors = %v, want nil for a process that exited before SIGKILL landed", err)
	}
	// The runtime's own stderr from the failed kill streams through, as it does
	// for a failed SIGTERM; what must be absent is the reaper's "Error:" line.
	if strings.Contains(stderr.String(), "Error:") {
		t.Errorf("stderr = %q, want no Error: line for a process that exited on its own", stderr.String())
	}
	want := []string{
		livenessCall("docker", "1000"), killCall("docker", "1000"), livenessCall("docker", "1000"),
		livenessCall("docker", "1001"), killCall("docker", "1001"),
	}
	if got := fakes.Calls(t); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A SIGKILL that fails for a process that is still alive stops the pass (the
// reaper's contract) with the bash-pinned stderr line, and the returned error
// says how many later targets were therefore never signaled.
func TestKillSurvivors_KillFailureOfALiveProcessStopsThePassAndCountsTheUnsignaledTargets(t *testing.T) {
	fakes, targets := killSurvivorsFixture(t,
		windowproctest.Proc{PID: "1000", Start: "5000", KillFails: true},
		windowproctest.Proc{PID: "1001", Start: "5001"},
		windowproctest.Proc{PID: "1002", Start: "5002"},
	)

	var stdout, stderr bytes.Buffer
	err := KillSurvivors(context.Background(), "docker", escContainer, targets, &stdout, &stderr)

	if err == nil {
		t.Fatal("KillSurvivors = nil, want the failed SIGKILL")
	}
	for _, sub := range []string{"kill -KILL", "2 further target(s) not signaled"} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error = %q, want it to contain %q", err, sub)
		}
	}
	// The runtime's own stderr streams through first; the pinned line follows it.
	if want := "Error: failed to send SIGKILL to pid 1000 in container " + escContainer + ".\n"; !strings.HasSuffix(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to end with exactly %q", stderr.String(), want)
	}
	if n := strings.Count(stderr.String(), "Error: failed to send SIGKILL"); n != 1 {
		t.Errorf("stderr = %q, want the Error: line exactly once, got %d", stderr.String(), n)
	}
	calls := fakes.Calls(t)
	for _, c := range calls {
		if strings.Contains(c, "1001") || strings.Contains(c, "1002") {
			t.Errorf("a later target was touched after the pass stopped: %q (calls:\n%s)", c, strings.Join(calls, "\n"))
		}
	}
}

// The last target failing leaves nothing unsignaled, so the error carries no
// "further targets" claim.
func TestKillSurvivors_KillFailureOfTheLastTargetReportsNoUnsignaledTargets(t *testing.T) {
	_, targets := killSurvivorsFixture(t, windowproctest.Proc{PID: "1000", Start: "5000", KillFails: true})

	var stdout, stderr bytes.Buffer
	err := KillSurvivors(context.Background(), "docker", escContainer, targets, &stdout, &stderr)

	if err == nil {
		t.Fatal("KillSurvivors = nil, want the failed SIGKILL")
	}
	if strings.Contains(err.Error(), "further") {
		t.Errorf("error = %q, want no count of further targets when none remain", err)
	}
}
