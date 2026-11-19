package dispatch

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/capacity"
)

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
func TestMain(m *testing.M) {
	containerSessionProbeFn = func() (int, error) { return 0, nil }
	memoryProbeFn = func([]RepoConfig, io.Writer) map[string]RepoMemory { return map[string]RepoMemory{} }
	oomProbeFn = func([]Ticket, []RepoConfig, io.Writer) oomProbeResult { return oomProbeResult{} }
	oomBackoffLoadFn = func() (map[string]time.Time, error) { return nil, nil }
	quotaDir, err := os.MkdirTemp("", "cenci-quota-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating the hermetic quota directory: %v\n", err)
		os.Exit(1)
	}
	dailyQuotaFn = func() *capacity.DailyQuota { return capacity.NewDailyQuota(filepath.Join(quotaDir, "capacity")) }
	code := m.Run()
	_ = os.RemoveAll(quotaDir)
	os.Exit(code)
}
