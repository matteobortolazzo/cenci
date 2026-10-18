package sandbox

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
)

// launchOwnershipEnv are the variables cenci_parent_environ accepts as
// launch/orphan ownership on an ancestor's /proc/<pid>/environ.
var launchOwnershipEnv = []string{"CENCI_LAUNCH_ID=", "TMUX_PANE=", "CENCI_TMUX_SOCKET="}

// launchEnvScrubbedMarker is set by the scrubbing re-exec. Helper processes
// that re-run this binary (TestHardenedProcessHelper's fake launcher) inherit
// it, so the launch ID a test deliberately gave them is kept.
const launchEnvScrubbedMarker = "CENCI_TEST_LAUNCH_ENV_SCRUBBED=1"

// TestMain re-execs the test binary once without the launch-ownership
// variables. The probe-script tests spawn children whose real parent is this
// process, and the scripts walk to /proc/<parent>/environ -- which keeps the
// environment the process was exec'd with, so neither t.Setenv nor scrubbing
// the child's env hides an ambient CENCI_LAUNCH_ID. Run inside a cenci launch,
// that ID made every child look owned by a foreign launch, so the
// unreadable-environ tests failed locally while passing in CI
// (watch/docs/test-isolation.md).
func TestMain(m *testing.M) {
	var clean []string
	scrubbed := false
	for _, kv := range os.Environ() {
		if kv == launchEnvScrubbedMarker {
			os.Exit(m.Run())
		}
		if hasAnyPrefix(kv, launchOwnershipEnv) {
			scrubbed = true
			continue
		}
		clean = append(clean, kv)
	}
	if scrubbed {
		clean = append(clean, launchEnvScrubbedMarker)
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "TestMain: resolve test binary to drop launch env: %v\n", err)
			os.Exit(2)
		}
		err = syscall.Exec(exe, os.Args, clean)
		fmt.Fprintf(os.Stderr, "TestMain: re-exec without launch env: %v\n", err)
		os.Exit(2)
	}
	// Nothing to scrub: still mark the environment, or a helper started with a
	// deliberate launch ID would scrub it in its own TestMain.
	name, value, _ := strings.Cut(launchEnvScrubbedMarker, "=")
	if err := os.Setenv(name, value); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: mark launch env scrubbed: %v\n", err)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
