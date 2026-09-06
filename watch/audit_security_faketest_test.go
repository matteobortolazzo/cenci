package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

// -- ticket #627: fake runtime for `cenci audit`/`cenci security explain`
// black-box (subprocess) tests -----------------------------------------------
//
// audit_cmd_test.go and security_cmd_test.go drive the real built `cenci`
// binary as a subprocess (binaryPath), so they can't reach into
// internal/sandbox/launcher's own faketest_test.go writeFakeRuntime helper
// (unexported, different package). This is an independent, byte-parallel
// copy of that helper's observed-inspect surface — #493 keep-in-sync note:
// any change to the combined observed-inspect probe format (the
// `.HostConfig.NetworkMode` token, or the header/mount line shapes) must be
// applied to BOTH this file and
// internal/sandbox/launcher/faketest_test.go's writeFakeRuntime.

// writeAuditFakeRuntime writes a fake docker/podman to dir that appends
// each invocation's argv (space-joined) as a line to callLog and answers
// only the read-only verbs Audit's observed-mode dispatch
// (NewForAuditWithRuntime) ever issues:
//
//	FAKE_PS                    → `ps ...` stdout (default "": no running
//	                              container, so Audit stays basis:"planned")
//	FAKE_PS_EXIT               → `ps ...` exit code (default 0); nonzero
//	                              simulates a daemon-unreachable ps failure
//	FAKE_OBSERVED_POSTURE      → combined observed-inspect probe stdout,
//	                              told apart by the distinctive
//	                              `.HostConfig.NetworkMode` format-string
//	                              token (default: an unweakened bridge/
//	                              no-mounts/dind-off header so tests that
//	                              don't care about observed content still
//	                              parse cleanly)
//	FAKE_OBSERVED_POSTURE_EXIT → combined observed-inspect probe exit code
//	                              (default 0); nonzero simulates an inspect
//	                              failure on an otherwise-running container
//
// Every other verb (images/volume/info/logs/exec/run/rm/create/start/stop)
// is a silent no-op that still exits 0 so a stray call doesn't crash the
// harness — Audit's read-only contract means none of them should ever
// actually be invoked; audit_cmd_test.go's no-mutation test asserts that
// directly against callLog.
func writeAuditFakeRuntime(t *testing.T, dir, name, callLog string) {
	t.Helper()
	body := `#!/bin/sh
printf '%s\n' "$*" >> ` + exectest.ShellQuote(callLog) + `
case "$1" in
ps) printf '%s' "${FAKE_PS:-}"; exit "${FAKE_PS_EXIT:-0}" ;;
inspect)
  case "$*" in
  *'.HostConfig.NetworkMode'*) printf '%b' "${FAKE_OBSERVED_POSTURE:-cenci-sandbox:latest|bridge|runc||\n\n}"; exit "${FAKE_OBSERVED_POSTURE_EXIT:-0}" ;;
  esac
  ;;
esac
exit 0
`
	exectest.WriteExecutable(t, filepath.Join(dir, name), body)
}

// writeAuditFakeRuntimes writes the same fake as both docker and podman
// (podman-first resolution order — internal/sandbox.ContainerRuntime) and
// returns the shared call-log path.
func writeAuditFakeRuntimes(t *testing.T, dir string) string {
	t.Helper()
	callLog := filepath.Join(dir, "calls.txt")
	writeAuditFakeRuntime(t, dir, "docker", callLog)
	writeAuditFakeRuntime(t, dir, "podman", callLog)
	return callLog
}

// auditSecurityEnv builds the subprocess environment shared by every
// command-level black-box test in audit_cmd_test.go and security_cmd_test.go
// that doesn't go through auditFakeRuntimeCmd: a fresh scripted docker/podman
// fake (writeAuditFakeRuntimes) wired onto PATH, plus an isolated HOME/
// CENCI_SOCKET_DIR. Consolidated here (rather than duplicated as auditEnv/
// securityEnv, byte-identical function bodies in their respective files)
// during the Phase 5 refactor pass that already consolidated a similar
// 5x-duplicated helper into auditFakeRuntimeCmd above.
func auditSecurityEnv(t *testing.T, home, xdg string) []string {
	t.Helper()
	fakeDir := t.TempDir()
	writeAuditFakeRuntimes(t, fakeDir)
	// t.TempDir() returns a directory created 0755 (masked by the process
	// umask, not 0700 — see testing.common.TempDir), which is looser than
	// CENCI_SOCKET_DIR's tier-1 leaf hardening tolerates without warning
	// (#1142's verbatim override means xdg IS the leaf, not a not-yet-
	// existing "cenci" subdir under it). Harden it explicitly so these
	// exact-output/JSON assertions aren't polluted by a spurious loose-
	// permissions warning on stderr.
	if err := os.Chmod(xdg, 0700); err != nil {
		t.Fatalf("hardening CENCI_SOCKET_DIR %q: %v", xdg, err)
	}
	return append(os.Environ(),
		"PATH="+fakeDir+":"+os.Getenv("PATH"),
		"HOME="+home,
		"CENCI_SOCKET_DIR="+xdg,
	)
}

// auditFakeRuntimeCmd builds an *exec.Cmd for the built `cenci` binary,
// wired to a fresh scripted docker/podman fake (writeAuditFakeRuntimes) via
// PATH, plus an isolated HOME/CENCI_SOCKET_DIR — the shared setup every
// command-level observed-mode test in audit_cmd_test.go/security_cmd_test.go
// needs before scripting FAKE_PS/FAKE_OBSERVED_POSTURE/FAKE_PS_EXIT via
// t.Setenv and running the command. Callers that don't need it (most do
// not) can ignore the returned call-log path.
func auditFakeRuntimeCmd(t *testing.T, repo, home string, args ...string) (cmd *exec.Cmd, callLog string) {
	t.Helper()
	fakeDir := t.TempDir()
	callLog = writeAuditFakeRuntimes(t, fakeDir)
	// See auditSecurityEnv above: t.TempDir() alone is 0755, not 0700.
	socketDir := t.TempDir()
	if err := os.Chmod(socketDir, 0700); err != nil {
		t.Fatalf("hardening CENCI_SOCKET_DIR %q: %v", socketDir, err)
	}
	cmd = exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(),
		"PATH="+fakeDir+":"+os.Getenv("PATH"),
		"HOME="+home,
		"CENCI_SOCKET_DIR="+socketDir,
	)
	cmd.Dir = repo
	return cmd, callLog
}
