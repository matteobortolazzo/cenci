package dispatch

import (
	"os"
	"testing"
)

// TestMain installs a hermetic default for the container-session probe seam
// (#1218) before any test in this package runs. Without it, every RunOnce
// test (installFakeGHOnPath, the chain harness, ...) would run the real
// probe, which execs `docker`/`podman` found on the developer's PATH -- an
// ambient, machine-dependent dependency (watch/docs/test-isolation.md). The
// stub reports "no orphans, healthy probe". The tests that exercise the real
// probe restore defaultContainerSessionProbe themselves, over a PATH holding
// only fakes (useRealContainerSessionProbe in dispatch_run_test.go), and the
// tests that exercise the wiring swap in their own stub
// (stubContainerSessionProbe).
func TestMain(m *testing.M) {
	containerSessionProbeFn = func() (int, error) { return 0, nil }
	os.Exit(m.Run())
}
