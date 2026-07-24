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
//	FAKE_IMAGES        → `images ...` stdout, optionally filtered by a
//	                     trailing positional repository argument (e.g.
//	                     `images --format {{.Repository}}:{{.Tag}}
//	                     cenci-sandbox-base` returns only the FAKE_IMAGES
//	                     lines whose repository is "cenci-sandbox-base");
//	                     with no positional, every FAKE_IMAGES line is
//	                     returned unfiltered.
//	FAKE_PS            → `ps ...` stdout (any form)
//	FAKE_VOLUMES       → `volume ls ...` stdout
//	FAKE_INFO_RUNTIMES → `info --format ...` stdout (the sysbox-runc
//	                     registration probe, sandbox.SysboxRegistered);
//	                     defaults to "{}" (no runtimes registered), mirroring
//	                     sandbox_open_test.go's writeScriptedRuntime.
//	FAKE_INSPECT_MOUNTS → `inspect --format ...` stdout for the ".RW" mounts
//	                     format containerHasSharedAgentMount uses (ticket
//	                     #620's read-only running-container disposition
//	                     probe); defaults to "" (no mounts -> incompatible).
//	                     Named to mirror sandbox_open_test.go's
//	                     writeScriptedRuntime FAKE_INSPECT_MOUNTS var (#493
//	                     keep-in-sync note), though that fake's var answers a
//	                     different inspect format (the plain ".Destination"
//	                     shape warnIfUnwired uses) since this package's DryRun
//	                     only ever needs the ".RW" shared-agent-mount check.
//
// Plain /bin/sh (not env) so it resolves under a minimal overridden PATH.
func writeFakeRuntime(t *testing.T, dir, name, callLog string) {
	t.Helper()
	// PATH is overridden to only the fake-runtime dir for these tests, so
	// the images filter below must stay shell-builtin-only (case/for, no
	// grep/awk) or it silently fails with "command not found" once the
	// real PATH is gone.
	body := `#!/bin/sh
printf '%s\n' "$*" >> ` + exectest.ShellQuote(callLog) + `
case "$1" in
images)
  if [ -n "$4" ]; then
    result=""
    IFS='
'
    for line in ${FAKE_IMAGES:-}; do
      case "$line" in
        "$4":*) result="${result}${line}
" ;;
      esac
    done
    printf '%s' "$result"
  else
    printf '%s' "${FAKE_IMAGES:-}"
  fi
  ;;
ps) printf '%s' "${FAKE_PS:-}" ;;
volume) [ "$2" = ls ] && printf '%s' "${FAKE_VOLUMES:-}" ;;
info) printf '%s' "${FAKE_INFO_RUNTIMES:-{\}}" ;;
inspect)
  case "$*" in
  *'.RW'*) printf '%b' "${FAKE_INSPECT_MOUNTS:-}" ;;
  esac
  ;;
esac
exit 0
`
	exectest.WriteExecutable(t, filepath.Join(dir, name), body)
}

// setFakeDockerNotRunning puts a fake "docker" (FAKE_PS left unset, so
// containerRunning reports nothing running) first on PATH, so tests that
// don't care about container disposition can call DryRun (which now always
// performs the read-only containerRunning probe via planArgvs, ticket #620)
// without depending on a real container runtime being reachable on the host
// running the test suite.
func setFakeDockerNotRunning(t *testing.T) {
	t.Helper()
	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
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
