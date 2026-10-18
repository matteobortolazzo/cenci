package dispatch

import (
	"io"
	"os"
	"testing"
)

// TestMain installs a hermetic default for the container-session probe seam
// (#1218) and the memory-headroom probe seam (#1290) before any test in this
// package runs. Without it, every RunOnce test (installFakeGHOnPath, the chain
// harness, ...) would run the real probes, which exec `docker`/`podman` found
// on the developer's PATH -- an ambient, machine-dependent dependency
// (watch/docs/test-isolation.md). The container-session stub reports "no
// orphans, healthy probe"; the memory stub reports an empty map (every repo
// takes the permissive zero value: no reading, no skip). The tests that
// exercise the real probes restore defaultContainerSessionProbe /
// defaultMemoryProbe themselves, over a PATH holding only fakes
// (useRealContainerSessionProbe in dispatch_run_test.go, useRealMemoryProbe in
// memprobe_test.go), and the tests that exercise the wiring swap in their own
// stub (stubContainerSessionProbe, stubMemoryProbe).
func TestMain(m *testing.M) {
	containerSessionProbeFn = func() (int, error) { return 0, nil }
	memoryProbeFn = func([]RepoConfig, io.Writer) map[string]RepoMemory { return map[string]RepoMemory{} }
	os.Exit(m.Run())
}
