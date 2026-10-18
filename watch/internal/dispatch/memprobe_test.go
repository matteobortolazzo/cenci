package dispatch

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// -- #1290: the memory-headroom gate, end to end through RunOnce --------------
//
// The probe tests run the REAL defaultMemoryProbe over a PATH holding only fake
// docker/podman binaries (restrictPATHToFakes), so the whole chain -- runtime
// listing, container-name derivation from each repo's directory, the bounded
// cgroup read, the most-loaded reduction, the log lines, Decide -- is
// exercised, and a passing test can never have reached a real container
// runtime. Container names are spelled out literally from the directory
// basenames the tests choose (never derived with production code).

const (
	// memLocal is the basename of every clone setupGatePass creates, so a
	// single-repo test's workload container is claude-cenci-local.
	memLocal = "local"

	memClaudeLocal = "claude-cenci-local"
	memCodexLocal  = "codex-cenci-local"

	// memFakeMax is the cgroup limit every canned reading uses; with it
	// current == 10 * percent.
	memFakeMax = int64(1000)
)

// memContainer scripts what a fake runtime does for `exec <name> /bin/cat ...`.
type memContainer struct {
	stdout string // printed on the cat's stdout
	stderr string // printed on stderr
	exit   int    // exit status
	hang   bool   // sleep far past any deadline instead of answering
}

// memRuntime scripts one fake docker/podman binary.
type memRuntime struct {
	ps         []string // running container names `ps` prints
	psStderr   string   // non-empty makes `ps` fail with this on stderr
	containers map[string]memContainer
}

// memReading renders `cat memory.current memory.max`.
func memReading(current, max int64) string { return fmt.Sprintf("%d\n%d\n", current, max) }

// usedPercent is a healthy container capped at memFakeMax using pct percent.
func usedPercent(pct int64) memContainer {
	return memContainer{stdout: memReading(pct*memFakeMax/100, memFakeMax)}
}

// unlimitedContainer is a container whose memory.max reads `max`.
func unlimitedContainer() memContainer { return memContainer{stdout: "123456\nmax\n"} }

// script renders the fake's shell body (no shebang). Every invocation is
// appended, prefixed with the runtime name, to callLog. Only shell builtins
// are used: the test PATH holds nothing else.
func (r memRuntime) script(rtName, callLog, sleepBin string) string {
	q := exectest.ShellQuote
	var b strings.Builder
	b.WriteString(`printf '%s %s\n' ` + q(rtName) + ` "$*" >> ` + q(callLog) + "\n")
	b.WriteString("case \"$1\" in\nps)\n")
	if r.psStderr != "" {
		b.WriteString("  printf '%s\\n' " + q(r.psStderr) + " >&2\n  exit 1\n  ;;\n")
	} else {
		b.WriteString("  printf '%s' " + q(strings.Join(r.ps, "\n")+"\n") + "\n  ;;\n")
	}
	b.WriteString("exec)\n  case \"$2\" in\n")
	names := make([]string, 0, len(r.containers))
	for name := range r.containers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := r.containers[name]
		b.WriteString("  " + q(name) + ")\n")
		if c.hang {
			// exec so the deadline kill reaches the sleeper itself, not a shell
			// parent holding the pipes open (watch/docs/go-gotchas.md).
			b.WriteString("    exec " + q(sleepBin) + " 30\n    ;;\n")
			continue
		}
		if c.stdout != "" {
			b.WriteString("    printf '%s' " + q(c.stdout) + "\n")
		}
		if c.stderr != "" {
			b.WriteString("    printf '%s\\n' " + q(c.stderr) + " >&2\n")
		}
		fmt.Fprintf(&b, "    exit %d\n    ;;\n", c.exit)
	}
	b.WriteString("  *) echo \"Error: no such container: $2\" >&2; exit 1 ;;\n  esac\n  ;;\nesac\nexit 0\n")
	return b.String()
}

// memFakes is the installed set of fake runtimes plus their shared call log.
type memFakes struct {
	callLog  string
	dir      string
	sleepBin string
}

// installMemFakes restricts PATH to the given fake runtimes (name -> script).
// It must run after setupGatePass, which installs the fake gh that
// restrictPATHToFakes carries over.
func installMemFakes(t *testing.T, runtimes map[string]memRuntime) *memFakes {
	t.Helper()
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("the hung-exec fixture needs a sleep binary: %v", err)
	}
	f := &memFakes{callLog: filepath.Join(t.TempDir(), "calls.txt"), sleepBin: sleepBin}
	bodies := make(map[string]string, len(runtimes))
	for name, rt := range runtimes {
		bodies[name] = rt.script(name, f.callLog, sleepBin)
	}
	restrictPATHToFakes(t, bodies)
	for name := range runtimes {
		p, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("LookPath(%s): %v", name, err)
		}
		f.dir = filepath.Dir(p)
		break
	}
	return f
}

// set rewrites one installed fake runtime between passes.
func (f *memFakes) set(t *testing.T, name string, rt memRuntime) {
	t.Helper()
	exectest.WriteExecutable(t, filepath.Join(f.dir, name), "#!/bin/sh\n"+rt.script(name, f.callLog, f.sleepBin))
}

// calls returns every recorded runtime invocation as "<runtime> <argv...>"; a
// runtime that was never invoked leaves no log at all.
func (f *memFakes) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.callLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading call log: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// execCount counts the cgroup reads made against container on runtime rt.
func (f *memFakes) execCount(t *testing.T, rt, container string) int {
	t.Helper()
	prefix := rt + " exec " + container + " "
	n := 0
	for _, c := range f.calls(t) {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// anyExec reports whether any `exec` was made at all, on any runtime.
func (f *memFakes) anyExec(t *testing.T) bool {
	t.Helper()
	for _, c := range f.calls(t) {
		if strings.Contains(c, " exec ") {
			return true
		}
	}
	return false
}

// useRealMemoryProbe restores the production probe over main_test.go's
// hermetic default stub; callers must also restrict PATH to fakes first.
func useRealMemoryProbe(t *testing.T) {
	t.Helper()
	restore := memoryProbeFn
	memoryProbeFn = defaultMemoryProbe
	t.Cleanup(func() { memoryProbeFn = restore })
}

// stubMemoryProbe installs a memory seam returning result and records the
// repos each call received.
func stubMemoryProbe(t *testing.T, result map[string]RepoMemory) *[][]RepoConfig {
	t.Helper()
	var calls [][]RepoConfig
	restore := memoryProbeFn
	memoryProbeFn = func(repos []RepoConfig, _ io.Writer) map[string]RepoMemory {
		calls = append(calls, repos)
		return result
	}
	t.Cleanup(func() { memoryProbeFn = restore })
	return &calls
}

// renameRepoDir moves enrolled repo i's clone to a new basename so containers
// of different repos get different names (setupGatePass names every clone
// "local").
func renameRepoDir(t *testing.T, cfg *Config, i int, base string) {
	t.Helper()
	dst := filepath.Join(filepath.Dir(cfg.Repos[i].Dir), base)
	if err := os.Rename(cfg.Repos[i].Dir, dst); err != nil {
		t.Fatalf("renaming %s: %v", cfg.Repos[i].Dir, err)
	}
	cfg.Repos[i].Dir = dst
}

// runMemoryPass runs one RunOnce pass and fails the test if it returns an
// error: a memory probe failure must hold candidates, never fail the pass.
func runMemoryPass(t *testing.T, cfg Config, dryRun bool) ([]Decision, string) {
	t.Helper()
	var buf bytes.Buffer
	decisions, err := RunOnce(cfg, fakeController{}, &fakeMutator{}, dryRun, &buf, nil)
	if err != nil {
		t.Fatalf("RunOnce returned %v; a memory probe failure must hold the pass, not fail it", err)
	}
	return decisions, buf.String()
}

// logLinesWith returns the log lines containing every one of subs.
func logLinesWith(out string, subs ...string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		match := true
		for _, s := range subs {
			if !strings.Contains(line, s) {
				match = false
				break
			}
		}
		if match {
			lines = append(lines, line)
		}
	}
	return lines
}

// assertProbeLogLine checks the log-hygiene contract every probe line shares
// (formatDecision's doc comment): it starts with "dispatch: " at column 0 and
// carries neither of the lazyboards-reserved " skip:" / " dispatch "
// substrings.
func assertProbeLogLine(t *testing.T, line string) {
	t.Helper()
	if !strings.HasPrefix(line, "dispatch: ") {
		t.Errorf("probe log line %q must start with \"dispatch: \" at column 0", line)
	}
	for _, forbidden := range []string{" skip:", " dispatch "} {
		if strings.Contains(line, forbidden) {
			t.Errorf("probe log line %q must not carry the lazyboards-reserved substring %q", line, forbidden)
		}
	}
}

// failureLines returns the "dispatch: memory probe failed" log lines, each
// already checked for log hygiene.
func failureLines(t *testing.T, out string) []string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "memory probe failed") && strings.HasPrefix(line, "dispatch: ") {
			assertProbeLogLine(t, line)
			lines = append(lines, line)
		}
	}
	return lines
}

// unlimitedLines returns the log lines naming container as unlimited, each
// already checked for log hygiene.
func unlimitedLines(t *testing.T, out, container string) []string {
	t.Helper()
	lines := logLinesWith(out, container, "unlimited")
	for _, line := range lines {
		assertProbeLogLine(t, line)
	}
	return lines
}

const (
	reasonMemFailedWant = "memory probe failed"
	dispatchWant        = "dispatch"
)

// -- AC1 end to end: the default threshold ------------------------------------

// AC1: with dispatch.memoryHeadroom unset (config loaded through LoadConfig
// from a file without the key), a container with 24% free skips the candidate
// with the exact reason and 26% free dispatches it -- through the real probe
// against a fake docker.
func TestRunOnce_MemoryGate_UnsetDefaultSkipsAt76AndPassesAt74(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"dispatch": {"concurrencyCap": 3}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	loaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	t.Run("76 percent used is skipped", func(t *testing.T) {
		cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "owner/name", tickets: []int{42}})
		cfg.MemoryHeadroom = loaded.MemoryHeadroom
		installMemFakes(t, map[string]memRuntime{"docker": {
			ps:         []string{memClaudeLocal},
			containers: map[string]memContainer{memClaudeLocal: usedPercent(76)},
		}})
		useRealMemoryProbe(t)
		failOnSpawn(t)

		decisions, _ := runMemoryPass(t, cfg, false)
		assertDecisions(t, decisions, []wantDecision{{42, ActionSkip, "memory headroom below 25% (owner/name container at 76%)", ""}})
	})

	t.Run("74 percent used dispatches and the healthy reading logs nothing", func(t *testing.T) {
		cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "owner/name", tickets: []int{42}})
		cfg.MemoryHeadroom = loaded.MemoryHeadroom
		installMemFakes(t, map[string]memRuntime{"docker": {
			ps:         []string{memClaudeLocal},
			containers: map[string]memContainer{memClaudeLocal: usedPercent(74)},
		}})
		useRealMemoryProbe(t)
		spawned := recordSpawns(t)

		decisions, out := runMemoryPass(t, cfg, false)
		assertDecisions(t, decisions, []wantDecision{{42, ActionDispatch, dispatchWant, ""}})
		if len(*spawned) != 1 || (*spawned)[0] != "42" {
			t.Errorf("spawned %v, want ticket 42", *spawned)
		}
		if strings.Contains(out, "memory") {
			t.Errorf("a healthy reading must leave the log untouched (the golden chain logs depend on it), got:\n%s", out)
		}
	})
}

// -- AC2: 0 disables the gate, but the probe still runs and logs --------------

func TestRunOnce_MemoryGate_ZeroDisablesButProbeStillRunsAndLogs(t *testing.T) {
	t.Run("an unlimited container still logs", func(t *testing.T) {
		cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "o/a", tickets: []int{42}})
		cfg.MemoryHeadroom = 0
		fakes := installMemFakes(t, map[string]memRuntime{"docker": {
			ps:         []string{memClaudeLocal},
			containers: map[string]memContainer{memClaudeLocal: unlimitedContainer()},
		}})
		useRealMemoryProbe(t)
		recordSpawns(t)

		decisions, out := runMemoryPass(t, cfg, false)
		assertDecisions(t, decisions, []wantDecision{{42, ActionDispatch, dispatchWant, ""}})
		if n := fakes.execCount(t, "docker", memClaudeLocal); n != 1 {
			t.Errorf("cgroup reads of %s = %d, want 1 (the probe still runs with the gate off)", memClaudeLocal, n)
		}
		if lines := unlimitedLines(t, out, memClaudeLocal); len(lines) != 1 {
			t.Errorf("unlimited log lines = %q, want exactly one", lines)
		}
	})

	t.Run("a failing read still logs but never skips", func(t *testing.T) {
		cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "o/a", tickets: []int{42}})
		cfg.MemoryHeadroom = 0
		fakes := installMemFakes(t, map[string]memRuntime{"docker": {
			ps:         []string{memClaudeLocal},
			containers: map[string]memContainer{memClaudeLocal: {stderr: "cgroup unreadable", exit: 1}},
		}})
		useRealMemoryProbe(t)
		recordSpawns(t)

		decisions, out := runMemoryPass(t, cfg, false)
		assertDecisions(t, decisions, []wantDecision{{42, ActionDispatch, dispatchWant, ""}})
		if n := fakes.execCount(t, "docker", memClaudeLocal); n != 1 {
			t.Errorf("cgroup reads of %s = %d, want 1", memClaudeLocal, n)
		}
		if lines := failureLines(t, out); len(lines) == 0 {
			t.Errorf("expected a \"dispatch: memory probe failed\" log line with the gate off, got:\n%s", out)
		}
	})

	t.Run("a container at 99 percent never skips", func(t *testing.T) {
		cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "o/a", tickets: []int{42}})
		cfg.MemoryHeadroom = 0
		fakes := installMemFakes(t, map[string]memRuntime{"docker": {
			ps:         []string{memClaudeLocal},
			containers: map[string]memContainer{memClaudeLocal: usedPercent(99)},
		}})
		useRealMemoryProbe(t)
		recordSpawns(t)

		decisions, _ := runMemoryPass(t, cfg, false)
		assertDecisions(t, decisions, []wantDecision{{42, ActionDispatch, dispatchWant, ""}})
		if n := fakes.execCount(t, "docker", memClaudeLocal); n != 1 {
			t.Errorf("cgroup reads of %s = %d, want 1", memClaudeLocal, n)
		}
	})
}

// -- AC4: unlimited containers ------------------------------------------------

// AC4: a container whose memory.max is `max` never skips, and the pass log has
// exactly one `dispatch:` line naming it as unlimited -- once per container per
// pass, however many candidates the repo has.
func TestRunOnce_MemoryGate_UnlimitedContainerLogsOnceAndPasses(t *testing.T) {
	cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "o/a", tickets: []int{42, 43}})
	cfg.MemoryHeadroom = 25
	installMemFakes(t, map[string]memRuntime{"docker": {
		ps:         []string{memClaudeLocal},
		containers: map[string]memContainer{memClaudeLocal: unlimitedContainer()},
	}})
	useRealMemoryProbe(t)
	recordSpawns(t)

	decisions, out := runMemoryPass(t, cfg, false)
	assertDecisions(t, decisions, []wantDecision{
		{42, ActionDispatch, dispatchWant, ""},
		{43, ActionDispatch, dispatchWant, ""},
	})
	if lines := unlimitedLines(t, out, memClaudeLocal); len(lines) != 1 {
		t.Errorf("unlimited log lines naming %s = %q, want exactly one", memClaudeLocal, lines)
	}
	if lines := failureLines(t, out); len(lines) != 0 {
		t.Errorf("an unlimited container is not a probe failure, got failure lines %q", lines)
	}
}

// -- AC5: no running container ------------------------------------------------

// AC5: a repo with no running workload container passes the gate, and the fake
// runtime records no exec at all -- even though another repo's container is
// running and listed.
func TestRunOnce_MemoryGate_NoRunningContainerPassesWithoutExec(t *testing.T) {
	cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "o/a", tickets: []int{42}})
	cfg.MemoryHeadroom = 25
	fakes := installMemFakes(t, map[string]memRuntime{"docker": {
		ps:         []string{"claude-cenci-someone-else"},
		containers: map[string]memContainer{"claude-cenci-someone-else": usedPercent(99)},
	}})
	useRealMemoryProbe(t)
	recordSpawns(t)

	decisions, out := runMemoryPass(t, cfg, false)
	assertDecisions(t, decisions, []wantDecision{{42, ActionDispatch, dispatchWant, ""}})
	if fakes.anyExec(t) {
		t.Errorf("no exec may run for a repo with no running container, calls:\n%s", strings.Join(fakes.calls(t), "\n"))
	}
	if lines := failureLines(t, out); len(lines) != 0 {
		t.Errorf("no running container is not a probe failure, got %q", lines)
	}
}

// Edge: no docker or podman on PATH means every repo passes as "no running
// container": no exec, no failure.
func TestRunOnce_MemoryGate_NoRuntimeOnPathPasses(t *testing.T) {
	cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "o/a", tickets: []int{42}})
	cfg.MemoryHeadroom = 25
	restrictPATHToFakes(t, nil)
	useRealMemoryProbe(t)
	recordSpawns(t)

	decisions, out := runMemoryPass(t, cfg, false)
	assertDecisions(t, decisions, []wantDecision{{42, ActionDispatch, dispatchWant, ""}})
	if strings.Contains(out, "memory") {
		t.Errorf("no runtime on PATH is a silent pass, got log:\n%s", out)
	}
}

// -- AC6: per-container and listing failures fail closed ----------------------

// AC6: any failed read of one repo's container -- non-zero exit, malformed
// numbers, output over the byte cap, a hang past the per-exec timeout -- holds
// every candidate of that repo with "memory probe failed", while another repo's
// healthy reading is decided normally, and RunOnce returns no error.
func TestRunOnce_MemoryGate_PerContainerFailureIsolatesRepo(t *testing.T) {
	const containerA = "claude-cenci-repo-a"
	const containerB = "claude-cenci-repo-b"

	cases := []struct {
		name string
		fail memContainer
	}{
		{"non-zero exit (cgroup v1 or a stopped container)", memContainer{stderr: "cat: /sys/fs/cgroup/memory.max: No such file or directory", exit: 1}},
		{"malformed numbers", memContainer{stdout: "abc\n1000\n"}},
		{"zero max would divide by zero", memContainer{stdout: "100\n0\n"}},
		{"output over the byte cap", memContainer{stdout: strings.Repeat("1", 5000) + "\n1000\n"}},
		// The per-exec timeout is 3s, so this subtest takes about that long.
		{"hang past the per-exec timeout", memContainer{hang: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := setupGatePass(t, watch.StateSnapshot{},
				gatePassRepo{repo: "o/a", tickets: []int{42}},
				gatePassRepo{repo: "o/b", tickets: []int{7}},
			)
			cfg.MemoryHeadroom = 25
			renameRepoDir(t, &cfg, 0, "repo-a")
			renameRepoDir(t, &cfg, 1, "repo-b")
			installMemFakes(t, map[string]memRuntime{"docker": {
				ps: []string{containerA, containerB},
				containers: map[string]memContainer{
					containerA: tc.fail,
					containerB: usedPercent(10),
				},
			}})
			useRealMemoryProbe(t)
			spawned := recordSpawns(t)

			decisions, out := runMemoryPass(t, cfg, false)
			assertDecisions(t, decisions, []wantDecision{
				{7, ActionDispatch, dispatchWant, ""},
				{42, ActionSkip, reasonMemFailedWant, ""},
			})
			if len(*spawned) != 1 || (*spawned)[0] != "7" {
				t.Errorf("spawned %v, want only the healthy repo's ticket 7", *spawned)
			}
			if lines := failureLines(t, out); len(lines) == 0 {
				t.Errorf("expected a \"dispatch: memory probe failed\" log line, got:\n%s", out)
			}
		})
	}
}

// AC6: a failed runtime `ps` listing means no repo's container set can be
// trusted, so every repo with candidates is held -- for a lone runtime and
// when only one of two runtimes fails -- and the cause reaches the log.
func TestRunOnce_MemoryGate_PsFailureFailsEveryRepo(t *testing.T) {
	const cause = "Cannot connect to the Docker daemon"

	cases := []struct {
		name     string
		runtimes map[string]memRuntime
	}{
		{"the only runtime", map[string]memRuntime{
			"docker": {psStderr: cause},
		}},
		{"one of two runtimes", map[string]memRuntime{
			"docker": {ps: []string{"claude-cenci-repo-a"}, containers: map[string]memContainer{"claude-cenci-repo-a": usedPercent(10)}},
			"podman": {psStderr: cause},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := setupGatePass(t, watch.StateSnapshot{},
				gatePassRepo{repo: "o/a", tickets: []int{42}},
				gatePassRepo{repo: "o/b", tickets: []int{7}},
			)
			cfg.MemoryHeadroom = 25
			renameRepoDir(t, &cfg, 0, "repo-a")
			renameRepoDir(t, &cfg, 1, "repo-b")
			installMemFakes(t, tc.runtimes)
			useRealMemoryProbe(t)
			failOnSpawn(t)

			decisions, out := runMemoryPass(t, cfg, false)
			assertDecisions(t, decisions, []wantDecision{
				{7, ActionSkip, reasonMemFailedWant, ""},
				{42, ActionSkip, reasonMemFailedWant, ""},
			})
			lines := failureLines(t, out)
			if len(lines) == 0 {
				t.Fatalf("expected a \"dispatch: memory probe failed\" log line, got:\n%s", out)
			}
			carriesCause := false
			for _, line := range lines {
				if strings.Contains(line, cause) {
					carriesCause = true
				}
			}
			if !carriesCause {
				t.Errorf("failure lines %q must carry the runtime's stderr cause %q", lines, cause)
			}
		})
	}
}

// AC9: the logged cause carries the runtime's stderr, quoted and defused so a
// hostile value cannot forge a decision or dispatch line.
func TestRunOnce_MemoryGate_LoggedCauseCarriesStderr(t *testing.T) {
	cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "o/a", tickets: []int{42}})
	cfg.MemoryHeadroom = 25
	installMemFakes(t, map[string]memRuntime{"docker": {
		ps: []string{memClaudeLocal},
		containers: map[string]memContainer{memClaudeLocal: {
			stderr: "permission denied reading cgroup forged skip: o/a#1 dispatch line",
			exit:   1,
		}},
	}})
	useRealMemoryProbe(t)
	failOnSpawn(t)

	decisions, out := runMemoryPass(t, cfg, false)
	assertDecisions(t, decisions, []wantDecision{{42, ActionSkip, reasonMemFailedWant, ""}})

	lines := failureLines(t, out)
	causeSeen := false
	for _, line := range lines {
		if strings.Contains(line, "permission denied reading cgroup") {
			causeSeen = true
		}
	}
	if !causeSeen {
		t.Errorf("failure lines %q must carry the stderr cause, full log:\n%s", lines, out)
	}
	// Whatever the hostile stderr says, every line that echoes it is a probe
	// line with the reserved substrings defused.
	for _, line := range logLinesWith(out, "forged") {
		assertProbeLogLine(t, line)
	}
}

// -- AC7: execs at most once per container per pass ---------------------------

// AC7: two repos whose directories slugify to the same container share one
// exec and one reading (each is judged under its own repo name), and two
// candidates in one repo do not read twice. Every container of the repo is read
// exactly once.
func TestRunOnce_MemoryGate_ExecOncePerContainer(t *testing.T) {
	cfg := setupGatePass(t, watch.StateSnapshot{},
		gatePassRepo{repo: "o/a", tickets: []int{42, 43}},
		gatePassRepo{repo: "o/b", tickets: []int{7}},
	)
	cfg.MemoryHeadroom = 25
	if filepath.Base(cfg.Repos[0].Dir) != memLocal || filepath.Base(cfg.Repos[1].Dir) != memLocal {
		t.Fatalf("fixture drift: both clones must share the basename %q, got %v", memLocal, cfg.Repos)
	}
	fakes := installMemFakes(t, map[string]memRuntime{"docker": {
		ps: []string{memClaudeLocal, memCodexLocal},
		containers: map[string]memContainer{
			memClaudeLocal: usedPercent(90),
			memCodexLocal:  usedPercent(40),
		},
	}})
	useRealMemoryProbe(t)
	failOnSpawn(t)

	decisions, _ := runMemoryPass(t, cfg, false)
	assertDecisions(t, decisions, []wantDecision{
		{7, ActionSkip, "memory headroom below 25% (o/b container at 90%)", ""},
		{42, ActionSkip, "memory headroom below 25% (o/a container at 90%)", ""},
		{43, ActionSkip, "memory headroom below 25% (o/a container at 90%)", ""},
	})
	for _, container := range []string{memClaudeLocal, memCodexLocal} {
		if n := fakes.execCount(t, "docker", container); n != 1 {
			t.Errorf("cgroup reads of %s = %d, want exactly 1 per pass (3 candidates, 2 repos)", container, n)
		}
	}
}

// AC7: a pass that collected no ticket makes no runtime call at all -- neither
// `ps` nor `exec` -- even with running containers on the host.
func TestRunOnce_MemoryGate_NoRuntimeCallsWithZeroTickets(t *testing.T) {
	cfg := setupGatePass(t, watch.StateSnapshot{},
		gatePassRepo{repo: "o/a"},
		gatePassRepo{repo: "o/b"},
	)
	cfg.MemoryHeadroom = 25
	fakes := installMemFakes(t, map[string]memRuntime{"docker": {
		ps:         []string{memClaudeLocal},
		containers: map[string]memContainer{memClaudeLocal: usedPercent(99)},
	}})
	useRealMemoryProbe(t)
	failOnSpawn(t)

	decisions, _ := runMemoryPass(t, cfg, false)
	if len(decisions) != 0 {
		t.Fatalf("decisions = %+v, want none", decisions)
	}
	if calls := fakes.calls(t); len(calls) != 0 {
		t.Errorf("runtime calls in a pass with zero tickets = %q, want none", calls)
	}
}

// AC7: an enrolled repo with zero collected tickets is never exec'd into, even
// when its container is running and another repo's candidates trigger the
// probe.
func TestRunOnce_MemoryGate_IdleEnrolledRepoNeverExeced(t *testing.T) {
	const containerA = "claude-cenci-repo-a"
	const containerIdle = "claude-cenci-repo-idle"

	cfg := setupGatePass(t, watch.StateSnapshot{},
		gatePassRepo{repo: "o/a", tickets: []int{42}},
		gatePassRepo{repo: "o/idle"},
	)
	cfg.MemoryHeadroom = 25
	renameRepoDir(t, &cfg, 0, "repo-a")
	renameRepoDir(t, &cfg, 1, "repo-idle")
	fakes := installMemFakes(t, map[string]memRuntime{"docker": {
		ps: []string{containerA, containerIdle},
		containers: map[string]memContainer{
			containerA:    usedPercent(10),
			containerIdle: usedPercent(99),
		},
	}})
	useRealMemoryProbe(t)
	recordSpawns(t)

	decisions, _ := runMemoryPass(t, cfg, false)
	assertDecisions(t, decisions, []wantDecision{{42, ActionDispatch, dispatchWant, ""}})
	if n := fakes.execCount(t, "docker", containerIdle); n != 0 {
		t.Errorf("cgroup reads of the idle repo's %s = %d, want 0", containerIdle, n)
	}
	if n := fakes.execCount(t, "docker", containerA); n != 1 {
		t.Errorf("cgroup reads of %s = %d, want 1", containerA, n)
	}
}

// AC7 (seam): the memory probe is handed only the repos that collected at least
// one ticket this pass, and is not called at all for a pass with none.
func TestResolveMemoryHeadroom_SeamScopedToReposWithTickets(t *testing.T) {
	repos := []RepoConfig{
		{Repo: "o/a", Dir: "/x/a"},
		{Repo: "o/idle", Dir: "/x/idle"},
		{Repo: "o/b", Dir: "/x/b"},
	}
	canned := map[string]RepoMemory{"o/a": limitedMemory(1, 2), "o/b": {Probe: MemoryProbeFailed}}

	t.Run("filters to repos with tickets and returns the probe result", func(t *testing.T) {
		calls := stubMemoryProbe(t, canned)
		tickets := []Ticket{{Repo: "o/a", Number: 1}, {Repo: "o/a", Number: 2}, {Repo: "o/b", Number: 3}}

		got := resolveMemoryHeadroom(repos, tickets, io.Discard)

		if len(*calls) != 1 {
			t.Fatalf("the seam ran %d times in one pass, want exactly 1", len(*calls))
		}
		var seen []string
		for _, r := range (*calls)[0] {
			seen = append(seen, r.Repo)
		}
		sort.Strings(seen)
		if want := []string{"o/a", "o/b"}; !reflect.DeepEqual(seen, want) {
			t.Errorf("seam received repos %v, want only the repos with tickets %v", seen, want)
		}
		if !reflect.DeepEqual(got, canned) {
			t.Errorf("resolveMemoryHeadroom = %+v, want the probe result %+v", got, canned)
		}
	})

	t.Run("zero tickets never calls the seam", func(t *testing.T) {
		calls := stubMemoryProbe(t, canned)
		got := resolveMemoryHeadroom(repos, nil, io.Discard)
		if len(*calls) != 0 {
			t.Errorf("the seam ran %d times for a pass with zero tickets, want 0", len(*calls))
		}
		if len(got) != 0 {
			t.Errorf("resolveMemoryHeadroom = %+v, want no readings", got)
		}
	})
}

// -- AC9: overall deadline ----------------------------------------------------

// AC9: the whole probe runs under one overall deadline. When it expires, only
// the repos whose read had not finished fail closed: a repo read before the
// deadline keeps its result, and a repo with no running container needed no
// read and still passes.
func TestRunOnce_MemoryGate_DeadlineFailsOnlyUnfinishedRepos(t *testing.T) {
	const (
		containerA = "claude-cenci-repo-a"
		containerB = "claude-cenci-repo-b"
	)
	cfg := setupGatePass(t, watch.StateSnapshot{},
		gatePassRepo{repo: "o/a", tickets: []int{1}},
		gatePassRepo{repo: "o/b", tickets: []int{2}},
		gatePassRepo{repo: "o/c", tickets: []int{3}},
	)
	cfg.MemoryHeadroom = 25
	renameRepoDir(t, &cfg, 0, "repo-a")
	renameRepoDir(t, &cfg, 1, "repo-b")
	renameRepoDir(t, &cfg, 2, "repo-c")
	installMemFakes(t, map[string]memRuntime{"docker": {
		ps: []string{containerA, containerB},
		containers: map[string]memContainer{
			containerA: usedPercent(10),
			containerB: {hang: true},
		},
	}})
	useRealMemoryProbe(t)
	spawned := recordSpawns(t)

	// Wide enough that repo A's fast read finishes on a loaded runner, yet
	// shorter than the 3s per-exec bound, so the overall deadline fires first.
	const deadline = 2 * time.Second
	const promptly = 15 * time.Second
	restore := memoryProbeTimeout
	memoryProbeTimeout = deadline
	t.Cleanup(func() { memoryProbeTimeout = restore })

	start := time.Now()
	decisions, out := runMemoryPass(t, cfg, false)
	elapsed := time.Since(start)

	assertDecisions(t, decisions, []wantDecision{
		{1, ActionDispatch, dispatchWant, ""},
		{2, ActionSkip, reasonMemFailedWant, ""},
		{3, ActionDispatch, dispatchWant, ""},
	})
	if len(*spawned) != 2 {
		t.Errorf("spawned %v, want tickets 1 and 3", *spawned)
	}
	if elapsed >= promptly {
		t.Errorf("pass took %s, want it bounded near the %s probe deadline (the exec sleeps 30s)", elapsed, deadline)
	}
	if lines := failureLines(t, out); len(lines) == 0 {
		t.Errorf("expected a \"dispatch: memory probe failed\" log line, got:\n%s", out)
	}
}

// -- AC10: dry run ------------------------------------------------------------

// AC10: `cenci dispatch --dry-run` prints the memory skip reason on the
// candidate's decision line, exactly as formatDecision renders every other skip.
func TestRunOnce_MemoryGate_DryRunPrintsReasonOnDecisionLine(t *testing.T) {
	cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "o/a", tickets: []int{42}})
	cfg.MemoryHeadroom = 25
	calls := stubMemoryProbe(t, map[string]RepoMemory{"o/a": limitedMemory(80, 100)})
	failOnSpawn(t)

	decisions, out := runMemoryPass(t, cfg, true)

	assertDecisions(t, decisions, []wantDecision{{42, ActionSkip, "memory headroom below 25% (o/a container at 80%)", ""}})
	const decisionLine = "o/a#42 skip: memory headroom below 25% (o/a container at 80%)"
	if !strings.Contains(out, decisionLine) {
		t.Errorf("dry-run output must carry the decision line %q, got:\n%s", decisionLine, out)
	}
	if len(*calls) != 1 {
		t.Errorf("the dry run ran the memory probe %d times, want 1 (it is read-only, like the orphan probe)", len(*calls))
	}
}

// -- AC12 and multi-container edges -------------------------------------------

// AC12: with two running containers for one repo, the most-loaded one gates and
// the reason reports its percent -- whichever agent it belongs to.
func TestRunOnce_MemoryGate_MostLoadedContainerGates(t *testing.T) {
	cases := []struct {
		name          string
		claude, codex int64
	}{
		{"codex is the more loaded", 50, 80},
		{"claude is the more loaded", 80, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "o/a", tickets: []int{42}})
			cfg.MemoryHeadroom = 25
			fakes := installMemFakes(t, map[string]memRuntime{"docker": {
				ps: []string{memClaudeLocal, memCodexLocal},
				containers: map[string]memContainer{
					memClaudeLocal: usedPercent(tc.claude),
					memCodexLocal:  usedPercent(tc.codex),
				},
			}})
			useRealMemoryProbe(t)
			failOnSpawn(t)

			decisions, _ := runMemoryPass(t, cfg, false)
			assertDecisions(t, decisions, []wantDecision{{42, ActionSkip, "memory headroom below 25% (o/a container at 80%)", ""}})
			for _, container := range []string{memClaudeLocal, memCodexLocal} {
				if n := fakes.execCount(t, "docker", container); n != 1 {
					t.Errorf("cgroup reads of %s = %d, want 1", container, n)
				}
			}
		})
	}
}

// Edge: a same-name container running under both docker and podman is read
// under each runtime, and the more loaded reading gates.
func TestRunOnce_MemoryGate_SameNameOnBothRuntimesMoreLoadedGates(t *testing.T) {
	cases := []struct {
		name           string
		docker, podman int64
	}{
		{"podman is the more loaded", 50, 90},
		{"docker is the more loaded", 90, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "o/a", tickets: []int{42}})
			cfg.MemoryHeadroom = 25
			fakes := installMemFakes(t, map[string]memRuntime{
				"docker": {ps: []string{memClaudeLocal}, containers: map[string]memContainer{memClaudeLocal: usedPercent(tc.docker)}},
				"podman": {ps: []string{memClaudeLocal}, containers: map[string]memContainer{memClaudeLocal: usedPercent(tc.podman)}},
			})
			useRealMemoryProbe(t)
			failOnSpawn(t)

			decisions, _ := runMemoryPass(t, cfg, false)
			assertDecisions(t, decisions, []wantDecision{{42, ActionSkip, "memory headroom below 25% (o/a container at 90%)", ""}})
			for _, rt := range []string{"docker", "podman"} {
				if n := fakes.execCount(t, rt, memClaudeLocal); n != 1 {
					t.Errorf("cgroup reads of %s under %s = %d, want 1", memClaudeLocal, rt, n)
				}
			}
		})
	}
}

// Edge: one unlimited container plus one limited one -- the limited one gates,
// and the unlimited one produces its single unlimited log line.
func TestRunOnce_MemoryGate_UnlimitedPlusLimited(t *testing.T) {
	cases := []struct {
		name    string
		codex   int64
		wantDec wantDecision
	}{
		{"limited container is over the threshold", 80, wantDecision{42, ActionSkip, "memory headroom below 25% (o/a container at 80%)", ""}},
		{"limited container is healthy", 10, wantDecision{42, ActionDispatch, dispatchWant, ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "o/a", tickets: []int{42}})
			cfg.MemoryHeadroom = 25
			installMemFakes(t, map[string]memRuntime{"docker": {
				ps: []string{memClaudeLocal, memCodexLocal},
				containers: map[string]memContainer{
					memClaudeLocal: unlimitedContainer(),
					memCodexLocal:  usedPercent(tc.codex),
				},
			}})
			useRealMemoryProbe(t)
			recordSpawns(t)

			decisions, out := runMemoryPass(t, cfg, false)
			assertDecisions(t, decisions, []wantDecision{tc.wantDec})
			if lines := unlimitedLines(t, out, memClaudeLocal); len(lines) != 1 {
				t.Errorf("unlimited log lines naming %s = %q, want exactly one", memClaudeLocal, lines)
			}
			if lines := unlimitedLines(t, out, memCodexLocal); len(lines) != 0 {
				t.Errorf("the limited container %s must not be logged as unlimited, got %q", memCodexLocal, lines)
			}
		})
	}
}

// Edge: on a cgroup v1 host (no /sys/fs/cgroup/memory.max) `cat` exits
// non-zero. That is a probe failure, never an unlimited container.
func TestRunOnce_MemoryGate_CgroupV1IsProbeFailureNotUnlimited(t *testing.T) {
	cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "o/a", tickets: []int{42}})
	cfg.MemoryHeadroom = 25
	installMemFakes(t, map[string]memRuntime{"docker": {
		ps: []string{memClaudeLocal},
		containers: map[string]memContainer{memClaudeLocal: {
			stdout: "512\n",
			stderr: "cat: /sys/fs/cgroup/memory.max: No such file or directory",
			exit:   1,
		}},
	}})
	useRealMemoryProbe(t)
	failOnSpawn(t)

	decisions, out := runMemoryPass(t, cfg, false)
	assertDecisions(t, decisions, []wantDecision{{42, ActionSkip, reasonMemFailedWant, ""}})
	if lines := unlimitedLines(t, out, memClaudeLocal); len(lines) != 0 {
		t.Errorf("a failed cat must never be logged as an unlimited container, got %q", lines)
	}
	if lines := failureLines(t, out); len(lines) == 0 {
		t.Errorf("expected a \"dispatch: memory probe failed\" log line, got:\n%s", out)
	}
}

// Edge: a container that restarted between passes (new cgroup, counters reset)
// is read fresh each pass. Nothing is cached across passes: each pass's
// decision uses its own reading, in both directions.
func TestRunOnce_MemoryGate_ReadsFreshEachPass(t *testing.T) {
	cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "o/a", tickets: []int{42}})
	cfg.MemoryHeadroom = 25
	fakes := installMemFakes(t, map[string]memRuntime{"docker": {
		ps:         []string{memClaudeLocal},
		containers: map[string]memContainer{memClaudeLocal: usedPercent(90)},
	}})
	useRealMemoryProbe(t)
	spawned := recordSpawns(t)

	decisions, _ := runMemoryPass(t, cfg, false)
	assertDecisions(t, decisions, []wantDecision{{42, ActionSkip, "memory headroom below 25% (o/a container at 90%)", ""}})

	// The container "restarts": a fresh cgroup, nearly empty.
	fakes.set(t, "docker", memRuntime{
		ps:         []string{memClaudeLocal},
		containers: map[string]memContainer{memClaudeLocal: usedPercent(10)},
	})
	decisions, _ = runMemoryPass(t, cfg, false)
	assertDecisions(t, decisions, []wantDecision{{42, ActionDispatch, dispatchWant, ""}})
	if len(*spawned) != 1 {
		t.Errorf("spawned %v after the second pass, want ticket 42 once", *spawned)
	}

	// And it fills up again.
	fakes.set(t, "docker", memRuntime{
		ps:         []string{memClaudeLocal},
		containers: map[string]memContainer{memClaudeLocal: usedPercent(85)},
	})
	decisions, _ = runMemoryPass(t, cfg, false)
	assertDecisions(t, decisions, []wantDecision{{42, ActionSkip, "memory headroom below 25% (o/a container at 85%)", ""}})
}

// The container name is derived from the slug of the basename of the repo's
// symlink-resolved directory -- the same name a session started there runs in
// -- not from the configured path's own basename.
func TestRunOnce_MemoryGate_ContainerNameUsesSlugOfResolvedBasename(t *testing.T) {
	cfg := setupGatePass(t, watch.StateSnapshot{}, gatePassRepo{repo: "o/a", tickets: []int{42}})
	cfg.MemoryHeadroom = 25
	renameRepoDir(t, &cfg, 0, "Real Repo_X")
	link := filepath.Join(filepath.Dir(cfg.Repos[0].Dir), "alias-link")
	if err := os.Symlink(cfg.Repos[0].Dir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	cfg.Repos[0].Dir = link

	const container = "claude-cenci-real-repo_x"
	fakes := installMemFakes(t, map[string]memRuntime{"docker": {
		ps:         []string{container, "claude-cenci-alias-link"},
		containers: map[string]memContainer{container: usedPercent(90), "claude-cenci-alias-link": usedPercent(10)},
	}})
	useRealMemoryProbe(t)
	failOnSpawn(t)

	decisions, _ := runMemoryPass(t, cfg, false)
	assertDecisions(t, decisions, []wantDecision{{42, ActionSkip, "memory headroom below 25% (o/a container at 90%)", ""}})
	if n := fakes.execCount(t, "docker", "claude-cenci-alias-link"); n != 0 {
		t.Errorf("the symlink's own basename must not name a container, but it was read %d times", n)
	}
}
