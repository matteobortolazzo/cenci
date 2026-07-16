package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/exectest"
)

// writeFakeRuntime writes a fake docker/podman to dir that appends each
// invocation's argv (space-joined) as a line to callLog and answers the
// read-only listing verbs from env vars, so tests script responses without a
// real container runtime:
//
//	FAKE_IMAGES   → `images ...` stdout
//	FAKE_PS       → `ps ...` stdout (any form)
//	FAKE_VOLUMES  → `volume ls ...` stdout
//
// Plain /bin/sh (not env) so it resolves under a minimal overridden PATH.
func writeFakeRuntime(t *testing.T, dir, name, callLog string) {
	t.Helper()
	body := `#!/bin/sh
printf '%s\n' "$*" >> ` + exectest.ShellQuote(callLog) + `
case "$1" in
images) printf '%s' "${FAKE_IMAGES:-}" ;;
ps) printf '%s' "${FAKE_PS:-}" ;;
volume) [ "$2" = ls ] && printf '%s' "${FAKE_VOLUMES:-}" ;;
esac
exit 0
`
	exectest.WriteExecutable(t, filepath.Join(dir, name), body)
}

// readCallLog returns the fake runtime's call log lines.
func readCallLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read call log: %v", err)
	}
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func containsPrefix(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// containsLineWithAll reports whether some line contains every one of subs.
func containsLineWithAll(lines []string, subs ...string) bool {
	for _, l := range lines {
		all := true
		for _, s := range subs {
			if !strings.Contains(l, s) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}
