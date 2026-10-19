package dispatch

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// -- #1291: the real OOM counter probe over fake docker/podman ----------------
//
// Every test here runs the REAL defaultOOMProbe (and its default inner seams)
// over a PATH holding only fake runtimes (memprobe_test.go's memRuntime /
// restrictPATHToFakes), so a passing test can never have reached a real
// container runtime. Container names are spelled out literally from the
// directory basename the test chooses.

const (
	oomClaudeLocal = "docker/" + memClaudeLocal
	oomCodexLocal  = "docker/" + memCodexLocal
)

// memEvents renders a cgroup v2 memory.events file whose oom_kill counter is n.
func memEvents(n int64) memContainer {
	return memContainer{stdout: fmt.Sprintf("low 0\nhigh 0\nmax 4\noom 2\noom_kill %d\noom_group_kill 0\n", n)}
}

// oomRepo creates an enrolled repo whose directory basename is memLocal, so its
// workload containers are claude-cenci-local and codex-cenci-local.
func oomRepo(t *testing.T) RepoConfig {
	t.Helper()
	dir := filepath.Join(t.TempDir(), memLocal)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return RepoConfig{Repo: "o/r", Dir: dir}
}

// workingTicket is a non-terminal Working ticket of o/r.
func workingTicket(number int, launch RecordedLaunch, extra ...string) Ticket {
	return Ticket{Repo: "o/r", Number: number, Labels: append([]string{labelWorking, labelPlanned}, extra...), Launch: launch}
}

// installOOMFakes installs a fake gh (restrictPATHToFakes carries it over) and
// then the given fake runtimes as the only other PATH entries.
func installOOMFakes(t *testing.T, runtimes map[string]memRuntime) *memFakes {
	t.Helper()
	installFakeGHOnPath(t, "exit 1\n")
	return installMemFakes(t, runtimes)
}

// unavailableLines returns the "OOM classification unavailable" log lines,
// each checked to start with "reconcile: " at column 0 and to carry neither
// lazyboards-reserved substring.
func unavailableLines(t *testing.T, out string) []string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "OOM classification unavailable") {
			continue
		}
		if !strings.HasPrefix(line, "reconcile: ") {
			t.Errorf("log line %q must start with \"reconcile: \" at column 0", line)
		}
		for _, forbidden := range []string{" skip:", " dispatch "} {
			if strings.Contains(line, forbidden) {
				t.Errorf("log line %q must not carry %q", line, forbidden)
			}
		}
		lines = append(lines, line)
	}
	return lines
}

// AC12: every running workload container of a repo with Working tickets is read
// exactly once per pass, on every runtime, however many tickets share it; a
// foreign container is never read; a healthy read logs nothing.
func TestDefaultOOMProbe_ReadsEachRunningWorkloadContainerOnce(t *testing.T) {
	fakes := installOOMFakes(t, map[string]memRuntime{
		"docker": {
			ps: []string{memClaudeLocal, memCodexLocal, "claude-cenci-other"},
			containers: map[string]memContainer{
				memClaudeLocal: memEvents(3), memCodexLocal: memEvents(1), "claude-cenci-other": memEvents(9),
			},
		},
		"podman": {ps: []string{memClaudeLocal}, containers: map[string]memContainer{memClaudeLocal: memEvents(5)}},
	})
	repo := oomRepo(t)
	var out bytes.Buffer

	got := defaultOOMProbe([]Ticket{workingTicket(42, RecordedLaunch{}), workingTicket(43, RecordedLaunch{})}, []RepoConfig{repo}, &out)

	want := map[string]int64{oomClaudeLocal: 3, oomCodexLocal: 1, "podman/" + memClaudeLocal: 5}
	if !mapsEqual(got.Counters, want) {
		t.Errorf("Counters = %v, want %v", got.Counters, want)
	}
	wantCandidates := []string{oomClaudeLocal, oomCodexLocal, "podman/" + memClaudeLocal}
	for _, key := range []string{"o/r#42", "o/r#43"} {
		if c := slices.Sorted(slices.Values(got.Candidates[key])); !slices.Equal(c, wantCandidates) {
			t.Errorf("Candidates[%s] = %v, want %v", key, c, wantCandidates)
		}
	}
	for _, c := range []struct{ rt, name string }{{"docker", memClaudeLocal}, {"docker", memCodexLocal}, {"podman", memClaudeLocal}} {
		if n := fakes.execCount(t, c.rt, c.name); n != 1 {
			t.Errorf("%s %s read %d times, want exactly once per pass", c.rt, c.name, n)
		}
	}
	if n := fakes.execCount(t, "docker", "claude-cenci-other"); n != 0 {
		t.Errorf("another repo's container was read %d times, want never", n)
	}
	if out.Len() != 0 {
		t.Errorf("a healthy probe logs nothing, got:\n%s", out.String())
	}
}

// AC12: a pass with no non-terminal Working ticket makes no runtime call at all.
func TestDefaultOOMProbe_NoWorkingTicketMakesNoRuntimeCall(t *testing.T) {
	cases := []struct {
		name    string
		tickets []Ticket
	}{
		{"no tickets", nil},
		{"Planned only", []Ticket{{Repo: "o/r", Number: 42, Labels: []string{labelPlanned}}}},
		{"Working with a terminal label", []Ticket{workingTicket(42, RecordedLaunch{}, labelDispatchFailed)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakes := installOOMFakes(t, map[string]memRuntime{"docker": {
				ps: []string{memClaudeLocal}, containers: map[string]memContainer{memClaudeLocal: memEvents(1)},
			}})
			var out bytes.Buffer

			got := defaultOOMProbe(tc.tickets, []RepoConfig{oomRepo(t)}, &out)

			if calls := fakes.calls(t); calls != nil {
				t.Errorf("runtime calls = %q, want none", calls)
			}
			if len(got.Counters) != 0 || len(got.Candidates) != 0 {
				t.Errorf("result = %+v, want empty", got)
			}
			if out.Len() != 0 {
				t.Errorf("want no log, got:\n%s", out.String())
			}
		})
	}
}

// Edge: a host with neither docker nor podman logs one unavailable line for the
// pass, whatever the number of Working tickets, and execs nothing.
func TestDefaultOOMProbe_NoRuntimeOnPathLogsOncePerPass(t *testing.T) {
	installFakeGHOnPath(t, "exit 1\n")
	restrictPATHToFakes(t, nil)
	var out bytes.Buffer

	got := defaultOOMProbe([]Ticket{workingTicket(42, RecordedLaunch{}), workingTicket(43, testLaunch)}, []RepoConfig{oomRepo(t)}, &out)

	if lines := unavailableLines(t, out.String()); len(lines) != 1 {
		t.Errorf("unavailable lines = %q, want exactly one for the pass", lines)
	}
	if len(got.Counters) != 0 {
		t.Errorf("Counters = %v, want empty", got.Counters)
	}
}

// Edge: a valid recorded launch makes its container the ticket's only
// candidate, so the sibling agent's container is neither a candidate nor read.
func TestDefaultOOMProbe_RecordedLaunchContainerWins(t *testing.T) {
	fakes := installOOMFakes(t, map[string]memRuntime{"docker": {
		ps:         []string{memClaudeLocal, memCodexLocal},
		containers: map[string]memContainer{memClaudeLocal: memEvents(2), memCodexLocal: memEvents(7)},
	}})
	launch := RecordedLaunch{ID: testLaunchID, Container: memClaudeLocal, Runtime: "docker"}
	var out bytes.Buffer

	got := defaultOOMProbe([]Ticket{workingTicket(42, launch)}, []RepoConfig{oomRepo(t)}, &out)

	if c := got.Candidates["o/r#42"]; !slices.Equal(c, []string{oomClaudeLocal}) {
		t.Errorf("Candidates = %v, want only the recorded launch container", c)
	}
	if !mapsEqual(got.Counters, map[string]int64{oomClaudeLocal: 2}) {
		t.Errorf("Counters = %v, want only the recorded container's reading", got.Counters)
	}
	if n := fakes.execCount(t, "docker", memCodexLocal); n != 0 {
		t.Errorf("the sibling container was read %d times, want never", n)
	}
}

// Edge: a fallback candidate that is not running is skipped without a log line
// or an exec.
func TestDefaultOOMProbe_FallbackNotRunningIsSkippedSilently(t *testing.T) {
	fakes := installOOMFakes(t, map[string]memRuntime{"docker": {ps: []string{"claude-cenci-other"}}})
	var out bytes.Buffer

	got := defaultOOMProbe([]Ticket{workingTicket(42, RecordedLaunch{})}, []RepoConfig{oomRepo(t)}, &out)

	if fakes.anyExec(t) {
		t.Errorf("want no exec, calls:\n%s", strings.Join(fakes.calls(t), "\n"))
	}
	if len(got.Candidates["o/r#42"]) != 0 || len(got.Counters) != 0 {
		t.Errorf("result = %+v, want no candidate and no counter", got)
	}
	if out.Len() != 0 {
		t.Errorf("want no log, got:\n%s", out.String())
	}
}

// AC6/AC12: every way a container's counter can be unreadable leaves it out of
// Counters and logs exactly one unavailable line naming it, even when two
// tickets share the container; it is exec'd at most once.
func TestDefaultOOMProbe_UnreadableContainerLogsOncePerContainer(t *testing.T) {
	recorded := RecordedLaunch{ID: testLaunchID, Container: memClaudeLocal, Runtime: "docker"}
	cases := []struct {
		name   string
		rt     memRuntime
		launch RecordedLaunch
	}{
		{"non-zero exit", memRuntime{ps: []string{memClaudeLocal}, containers: map[string]memContainer{
			memClaudeLocal: {stderr: "cat: /sys/fs/cgroup/memory.events: No such file or directory", exit: 1}}}, RecordedLaunch{}},
		{"malformed memory.events", memRuntime{ps: []string{memClaudeLocal}, containers: map[string]memContainer{
			memClaudeLocal: {stdout: "oom 1\noom_kill many\n"}}}, RecordedLaunch{}},
		{"missing oom_kill line", memRuntime{ps: []string{memClaudeLocal}, containers: map[string]memContainer{
			memClaudeLocal: {stdout: "low 0\noom 3\n"}}}, RecordedLaunch{}},
		{"output over the byte cap", memRuntime{ps: []string{memClaudeLocal}, containers: map[string]memContainer{
			memClaudeLocal: {stdout: strings.Repeat("x", 5000) + "\noom_kill 1\n"}}}, RecordedLaunch{}},
		{"recorded container not running", memRuntime{ps: []string{memCodexLocal}, containers: map[string]memContainer{
			memCodexLocal: memEvents(1)}}, recorded},
		{"failed ps on the recorded container's runtime", memRuntime{psStderr: "Cannot connect to the Docker daemon"}, recorded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakes := installOOMFakes(t, map[string]memRuntime{"docker": tc.rt})
			var out bytes.Buffer

			got := defaultOOMProbe([]Ticket{workingTicket(42, tc.launch), workingTicket(43, tc.launch)}, []RepoConfig{oomRepo(t)}, &out)

			lines := unavailableLines(t, out.String())
			if len(lines) != 1 {
				t.Fatalf("unavailable lines = %q, want exactly one per unreadable container", lines)
			}
			if !strings.Contains(lines[0], memClaudeLocal) {
				t.Errorf("line %q does not name the container", lines[0])
			}
			if _, ok := got.Counters[oomClaudeLocal]; ok {
				t.Errorf("Counters = %v, an unreadable container must have no reading", got.Counters)
			}
			if n := fakes.execCount(t, "docker", memClaudeLocal); n > 1 {
				t.Errorf("container exec'd %d times, want at most once per pass", n)
			}
		})
	}
}

// AC12: the whole probe runs under oomProbePassTimeout. A hung exec is cut off
// at the deadline, and a container reached after it is not exec'd; both count
// as unreadable, one line each.
func TestDefaultOOMProbe_DeadlineCountsUnfinishedContainersUnreadable(t *testing.T) {
	restore := oomProbePassTimeout
	oomProbePassTimeout = 300 * time.Millisecond
	t.Cleanup(func() { oomProbePassTimeout = restore })
	fakes := installOOMFakes(t, map[string]memRuntime{"docker": {
		ps:         []string{memClaudeLocal, memCodexLocal},
		containers: map[string]memContainer{memClaudeLocal: {hang: true}, memCodexLocal: memEvents(1)},
	}})
	var out bytes.Buffer
	start := time.Now()

	got := defaultOOMProbe([]Ticket{workingTicket(42, RecordedLaunch{})}, []RepoConfig{oomRepo(t)}, &out)

	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Errorf("probe took %v, want it bounded by the pass deadline", elapsed)
	}
	if len(got.Counters) != 0 {
		t.Errorf("Counters = %v, want none read before the deadline", got.Counters)
	}
	if lines := unavailableLines(t, out.String()); len(lines) != 2 {
		t.Errorf("unavailable lines = %q, want one per unfinished container", lines)
	}
	if n := fakes.execCount(t, "docker", memCodexLocal); n != 0 {
		t.Errorf("a container reached after the deadline was exec'd %d times, want never", n)
	}
}

func mapsEqual[K comparable, V comparable](a, b map[K]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
