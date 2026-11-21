package dispatch

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/babysit"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/capacity"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
)

// testBinaryPath is this test binary, captured before any test swaps os.Args[0]
// for a recording shim (installFakeCenciProcess): the capacity helper hook
// re-execs it.
var testBinaryPath = os.Args[0]

// TestMain installs a hermetic default for the container-session probe seam
// (#1218), the memory-headroom probe seam (#1290), the OOM counter probe seam
// and the OOM backoff load seam (#1291) before any test in this package runs.
// Without it, every RunOnce/RunReconcileOnce test (installFakeGHOnPath, the
// chain harness, ...) would run the real probes, which exec `docker`/`podman`
// found on the developer's PATH, and would read the developer's real
// reconcile.json -- ambient, machine-dependent dependencies
// (watch/docs/test-isolation.md). The container-session stub reports "no
// orphans, healthy probe"; the memory stub reports an empty map (every repo
// takes the permissive zero value: no reading, no skip); the OOM probe stub
// reads no counter (every recovery takes the ordinary path); the backoff stub
// reports no OOM retry times. The tests that exercise the real probes restore
// defaultContainerSessionProbe / defaultMemoryProbe themselves, over a PATH
// holding only fakes (useRealContainerSessionProbe in dispatch_run_test.go,
// useRealMemoryProbe in memprobe_test.go), oomprobe_test.go calls
// defaultOOMProbe directly over fakes, and the tests that exercise the wiring
// swap in their own stub (stubContainerSessionProbe, stubMemoryProbe,
// stubOOMProbe).
//
// dailyQuotaFn (#1566) is pointed at a fresh temp directory, so a test whose
// config sets a daily quota can never read or write the developer's real
// $XDG_STATE_HOME/cenci/capacity; tests that assert on the store use
// useQuotaDir for a directory of their own. The directory is removed after the
// run.
//
// The admission ledger (#1469) is isolated the same way. The ledger seam points
// at a temp directory, the tmux inventory seam reports every socket as a
// complete, empty inventory (so an entry a previous test left behind is released
// by the next admission, like a worker that has exited), the limits seam reads
// the limits off the Config a test built when it has no file (a hand-built Config
// has no Path), and XDG_STATE_HOME points at a temp directory too, so the babysit
// code a chain test reaches (whose seams are unexported) writes there instead of
// into the developer's real $XDG_STATE_HOME/cenci/capacity. Tests that assert on
// the ledger use useLedger for a directory of their own.
//
// A re-exec child (capacityHelperEnv set) is the opposite: it is one of the
// processes of a cross-process test and must reach the ledger, the limits and the
// tmux inventory through the environment its parent set, exactly like a real
// process, so none of those seams is installed there.
func TestMain(m *testing.M) {
	containerSessionProbeFn = func() (int, error) { return 0, nil }
	memoryProbeFn = func([]RepoConfig, io.Writer) map[string]RepoMemory { return map[string]RepoMemory{} }
	oomProbeFn = func([]Ticket, []RepoConfig, io.Writer) oomProbeResult { return oomProbeResult{} }
	oomBackoffLoadFn = func() (map[string]time.Time, error) { return nil, nil }

	// A `cenci run <workflow> ...` the babysit helper's launch self-execs (os.Args[0]
	// is this binary) is a fake `cenci run`. `go test` itself never has "run" as its
	// first argument, so the argv alone cannot be mistaken for a test run.
	if len(os.Args) > 1 && os.Args[1] == "run" && os.Getenv(capacityRunLogEnv) != "" {
		os.Exit(fakeCenciRun(os.Args[1:]))
	}
	if mode := os.Getenv(capacityHelperEnv); mode != "" {
		// Not inherited by what the helper starts: the fake `gh` shim re-execs this
		// binary too, and must run its own test entry point, not the helper.
		if err := os.Unsetenv(capacityHelperEnv); err != nil {
			fmt.Fprintf(os.Stderr, "clearing %s: %v\n", capacityHelperEnv, err)
			os.Exit(1)
		}
		os.Exit(runCapacityHelper(mode))
	}

	stateDir, err := os.MkdirTemp("", "cenci-state-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating the hermetic state directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("XDG_STATE_HOME", stateDir); err != nil {
		fmt.Fprintf(os.Stderr, "setting XDG_STATE_HOME: %v\n", err)
		os.Exit(1)
	}
	dailyQuotaFn = func() *capacity.DailyQuota { return capacity.NewDailyQuota(filepath.Join(stateDir, "quota")) }
	capacityLedgerFn = func() capacityLedger {
		return capacity.NewLedger(filepath.Join(stateDir, "ledger"), capacity.Options{})
	}
	capacityInventoryFn = func(string) (tmux.CleanupInventory, error) { return tmux.CleanupInventory{}, tmux.ErrNoServer }
	capacityLimitsFn = limitsOfConfig

	code := m.Run()
	_ = os.RemoveAll(stateDir)
	os.Exit(code)
}

// limitsOfConfig is the test default for the limits seam: the file at cfg.Path
// when the Config came from LoadConfig, else the limits the test put in the Config
// itself, so Decide's pre-filter and Admit see one set of numbers.
func limitsOfConfig(cfg Config) (capacity.Limits, error) {
	if cfg.Path != "" {
		return capacity.LoadLimits(cfg.Path)
	}
	limits := capacity.Limits{Machine: cfg.ConcurrencyCap, Repos: map[string]int{}}
	for _, rc := range cfg.Repos {
		if rc.ConcurrencyCap != nil {
			limits.Repos[strings.ToLower(rc.Repo)] = *rc.ConcurrencyCap
		}
	}
	return limits, nil
}

// -- the cross-process helper hook ---------------------------------------------

// The helper hook turns this test binary into one process of a cross-process
// capacity test. A parent starts it through capacityHelperCmd with
// CENCI_CAPACITY_HELPER set, and the child runs one pass and exits:
//
//   - `dispatch` runs one real RunOnce pass over the config at
//     CENCI_CAPACITY_CONFIG, with a fake `gh` (the parent's PATH shim and world),
//     the daemon snapshot at CENCI_SOCKET_DIR, and a spawn stub standing in for
//     the run controller: every launch appends `launch <ticket>` to
//     CENCI_CAPACITY_LAUNCH_LOG. The decision lines go to stdout, one per ticket,
//     so a refused pass shows `skip: concurrency cap reached` there. Exit 0, or 1
//     with the reason on stderr.
//   - `babysit` runs babysit.Run(Once) for PR CENCI_CAPACITY_PR (default 103) with
//     state in CENCI_CAPACITY_STATE_DIR, from the git checkout CENCI_CAPACITY_CHDIR,
//     against the parent's fakes (chain gh, fake tmux, the daemon snapshot).
//     babysit self-execs os.Args[0] as `cenci run <workflow> ...`, which is this
//     binary again with a `run` argv: with CENCI_CAPACITY_RUN_LOG set, that
//     grandchild is a fake `cenci run`, which appends its argv to that file and
//     exits CENCI_CAPACITY_RUN_EXIT (default 0; 3 is "window spawned, setup
//     failed").
//
//   - `wait` records a babysit-style wait (a ci-repair of PR CENCI_CAPACITY_PR on
//     repo CENCI_CAPACITY_REPO, 1h poll interval) on the ledger at capacity.Dir(),
//     creates the file CENCI_CAPACITY_READY and then blocks until it is killed. The
//     parent SIGKILLs it, so the wait is left behind by a dead owner.
//
// A helper clears CENCI_CAPACITY_HELPER from its own environment, so the processes
// it starts (the fake `gh` shim re-execs this binary as its own test entry point)
// are not helpers themselves.
//
// Both read the ledger at capacity.Dir() (XDG_STATE_HOME) and the limits at the
// config the pass itself resolves, so two helpers contend on one real ledger.
// CENCI_CAPACITY_BARRIER, when set, is a file the child waits for (up to 30s)
// before it starts its pass, so a parent can release two children together.
const (
	capacityHelperEnv     = "CENCI_CAPACITY_HELPER"
	capacityConfigEnv     = "CENCI_CAPACITY_CONFIG"
	capacityLaunchLogEnv  = "CENCI_CAPACITY_LAUNCH_LOG"
	capacityRunLogEnv     = "CENCI_CAPACITY_RUN_LOG"
	capacityRunExitEnv    = "CENCI_CAPACITY_RUN_EXIT"
	capacityPREnv         = "CENCI_CAPACITY_PR"
	capacityStateDirEnv   = "CENCI_CAPACITY_STATE_DIR"
	capacityChdirEnv      = "CENCI_CAPACITY_CHDIR"
	capacityBarrierEnv    = "CENCI_CAPACITY_BARRIER"
	capacitySessionEnv    = "CENCI_CAPACITY_SESSION"
	capacityRepoEnv       = "CENCI_CAPACITY_REPO"
	capacityReadyEnv      = "CENCI_CAPACITY_READY"
	capacityBarrierBudget = 30 * time.Second
	// capacityWaitInterval is the poll interval the `wait` helper records: far
	// longer than any test, so only the owner check can drop its wait early.
	capacityWaitInterval = time.Hour
)

// capacityHelperCmd returns the command that re-execs this test binary as a helper
// of the given mode, with env appended to the current environment.
func capacityHelperCmd(mode string, env ...string) *exec.Cmd {
	cmd := exec.Command(testBinaryPath)
	cmd.Env = append(append(os.Environ(), capacityHelperEnv+"="+mode), env...)
	return cmd
}

func runCapacityHelper(mode string) int {
	switch mode {
	case "dispatch":
		return dispatchHelper()
	case "babysit":
		return babysitHelper()
	case "wait":
		return waitHelper()
	}
	fmt.Fprintf(os.Stderr, "unknown %s mode %q\n", capacityHelperEnv, mode)
	return 2
}

func waitForBarrier() {
	path := os.Getenv(capacityBarrierEnv)
	if path == "" {
		return
	}
	deadline := time.Now().Add(capacityBarrierBudget)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintln(f, line)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

func dispatchHelper() int {
	cfg, err := LoadConfig(os.Getenv(capacityConfigEnv))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	logPath := os.Getenv(capacityLaunchLogEnv)
	runFn = func(o run.Opts, _ run.Controller) error {
		return appendLine(logPath, "launch "+o.WindowTicket)
	}
	waitForBarrier()
	if _, err := RunOnce(cfg, fakeController{}, &GHMutator{}, false, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func babysitHelper() int {
	if dir := os.Getenv(capacityChdirEnv); dir != "" {
		if err := os.Chdir(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	pr := os.Getenv(capacityPREnv)
	if pr == "" {
		pr = "103"
	}
	session := os.Getenv(capacitySessionEnv)
	if session == "" {
		session = chainFakeTmuxSession
	}
	waitForBarrier()
	err := babysit.Run(babysit.Options{PR: pr, Agent: "claude", StateDir: os.Getenv(capacityStateDirEnv), Once: true, Session: session})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// waitHelper records one wait on the shared ledger, signals that it is on disk and
// blocks until the parent kills it.
func waitHelper() int {
	pr, err := strconv.Atoi(os.Getenv(capacityPREnv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", capacityPREnv, err)
		return 1
	}
	ledger := capacity.NewLedger(capacity.Dir(), capacity.Options{})
	err = ledger.Wait(capacity.WaitRequest{
		Kind: capacity.KindCIRepair, Repo: os.Getenv(capacityRepoEnv), Ticket: pr,
		Reason: capacity.ReasonMachineCap, Interval: capacityWaitInterval,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(os.Getenv(capacityReadyEnv), nil, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	time.Sleep(capacityWaitInterval)
	return 0
}

// fakeCenciRun is the `cenci run <workflow> ...` the babysit helper's launch
// self-execs: it records its argv and exits with the status the parent chose.
func fakeCenciRun(args []string) int {
	if path := os.Getenv(capacityRunLogEnv); path != "" {
		if err := appendLine(path, strings.Join(args, " ")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	code := 0
	if v := os.Getenv(capacityRunExitEnv); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &code); err != nil {
			return 1
		}
	}
	return code
}
