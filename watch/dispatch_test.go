package main_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/capacity"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/dispatch"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// -- dispatch enroll/unenroll/status sub-verbs (ticket #121) ---------------

// runGit runs `git -C dir <args>`, failing the test on error. Mirrors
// internal/dispatch/enroll_test.go's helper of the same name.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// initGitRemote creates a fresh git repo in a temp dir with the given origin
// remote URL and returns the repo directory (an absolute path since
// t.TempDir() is absolute). Mirrors internal/dispatch/enroll_test.go's
// helper of the same name.
func initGitRemote(t *testing.T, remoteURL string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", remoteURL)
	return dir
}

func TestDispatchEnroll_WritesConfigAndIsIdempotent(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := filepath.Join(t.TempDir(), "config.json")

	cmd := exec.Command(binaryPath, "dispatch", "enroll", "--dir", dir, "--config", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("first enroll: %v\n%s", err, output)
	}
	want := "Enrolled owner/name (" + dir + ")"
	if !strings.Contains(string(output), want) {
		t.Errorf("first enroll output = %q, want to contain %q", output, want)
	}

	got, qerr := dispatch.QueryEnrollment(configPath, "owner/name")
	if qerr != nil {
		t.Fatalf("QueryEnrollment: %v", qerr)
	}
	if !got.Enrolled || got.Dir != dir {
		t.Errorf("QueryEnrollment after enroll = %+v, want Enrolled=true Dir=%q", got, dir)
	}

	// Second run is a no-op.
	cmd = exec.Command(binaryPath, "dispatch", "enroll", "--dir", dir, "--config", configPath)
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("second enroll: %v\n%s", err, output)
	}
	want = "Already enrolled owner/name (" + dir + ")"
	if !strings.Contains(string(output), want) {
		t.Errorf("second enroll output = %q, want to contain %q", output, want)
	}
}

// -- #927: the post-enroll session hint --------------------------------------
//
// `repos[].session` is now required before a repo's dispatches can spawn
// anywhere; `cenci dispatch enroll` prints a one-line hint naming the
// resolved config path and that session must be set, on BOTH the
// fresh-enrollment and the idempotent "already enrolled" paths (Q&A #1), but
// never when the repo already has a session configured.

// TestDispatchEnroll_PrintsSessionHintOnFreshEnrollment covers the
// fresh-enrollment half of Q&A #1.
func TestDispatchEnroll_PrintsSessionHintOnFreshEnrollment(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := filepath.Join(t.TempDir(), "config.json")

	cmd := exec.Command(binaryPath, "dispatch", "enroll", "--dir", dir, "--config", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("enroll: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), configPath) {
		t.Errorf("output = %q, want the hint naming the resolved config path %q", output, configPath)
	}
	if !strings.Contains(string(output), "session") {
		t.Errorf("output = %q, want a one-line hint that session must be set before the repo will dispatch", output)
	}
}

// TestDispatchEnroll_PrintsSessionHintOnIdempotentAlreadyEnrolledPath covers
// Q&A #1's idempotent-path half: the hint also prints on the "Already
// enrolled" branch, not only on a changed==true fresh enrollment.
func TestDispatchEnroll_PrintsSessionHintOnIdempotentAlreadyEnrolledPath(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := filepath.Join(t.TempDir(), "config.json")

	cmd := exec.Command(binaryPath, "dispatch", "enroll", "--dir", dir, "--config", configPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("first enroll: %v\n%s", err, output)
	}

	cmd = exec.Command(binaryPath, "dispatch", "enroll", "--dir", dir, "--config", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("second enroll: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Already enrolled") {
		t.Fatalf("second enroll output = %q, want the idempotent path", output)
	}
	if !strings.Contains(string(output), configPath) || !strings.Contains(string(output), "session") {
		t.Errorf("second enroll output = %q, want the session hint to print on the idempotent path too (Q&A #1)", output)
	}
}

// TestDispatchEnroll_NoHintWhenSessionAlreadySet covers the negative case:
// once a repo's session is already configured, re-enrolling it must not
// print the hint.
func TestDispatchEnroll_NoHintWhenSessionAlreadySet(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfgJSON := fmt.Sprintf(`{"dispatch": {"repos": [{"repo": "owner/name", "dir": %q, "session": "work"}]}}`, dir)
	if err := os.WriteFile(configPath, []byte(cfgJSON), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cmd := exec.Command(binaryPath, "dispatch", "enroll", "--dir", dir, "--config", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("enroll: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Already enrolled") {
		t.Fatalf("enroll output = %q, want the idempotent path", output)
	}
	// Narrowed from a bare "session" substring (#933): the success line now
	// legitimately contains "session" (e.g. "→ session work"), so only the
	// warning's exact wording is a true negative here.
	if strings.Contains(string(output), "no tmux session set") {
		t.Errorf("enroll output = %q, want no session hint when the repo already has a session configured", output)
	}
}

func TestDispatchEnroll_OutsideGitRepo_Exits1(t *testing.T) {
	dir := t.TempDir() // no .git
	configPath := filepath.Join(t.TempDir(), "config.json")

	cmd := exec.Command(binaryPath, "dispatch", "enroll", "--dir", dir, "--config", configPath)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "cenci dispatch enroll: ") {
		t.Errorf("stderr = %q, want to contain %q", output, "cenci dispatch enroll: ")
	}
}

// -- #933: `dispatch enroll --session` ---------------------------------------

// TestDispatchEnroll_WithSessionFlag_SetsSessionAndPrintsArrow covers
// enrolling a fresh repo with an explicit --session: the effective session
// is persisted and surfaced in the success line.
func TestDispatchEnroll_WithSessionFlag_SetsSessionAndPrintsArrow(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := filepath.Join(t.TempDir(), "config.json")

	cmd := exec.Command(binaryPath, "dispatch", "enroll", "--dir", dir, "--config", configPath, "--session", "a-work")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("enroll --session: %v\n%s", err, output)
	}
	want := "Enrolled owner/name (" + dir + ") → session a-work"
	if !strings.Contains(string(output), want) {
		t.Errorf("enroll --session output = %q, want to contain %q", output, want)
	}

	got, qerr := dispatch.QueryEnrollment(configPath, "owner/name")
	if qerr != nil {
		t.Fatalf("QueryEnrollment: %v", qerr)
	}
	if got.Session != "a-work" {
		t.Errorf("QueryEnrollment.Session = %q, want %q", got.Session, "a-work")
	}
}

// TestDispatchEnroll_NoFlagOnAlreadySessioned_PreservesSessionAndBytes covers
// re-enrolling a repo that already has a session, with no --session flag:
// the session is left untouched and the config file is never rewritten.
func TestDispatchEnroll_NoFlagOnAlreadySessioned_PreservesSessionAndBytes(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfgJSON := fmt.Sprintf(`{"dispatch": {"repos": [{"repo": "owner/name", "dir": %q, "session": "work"}]}}`, dir)
	if err := os.WriteFile(configPath, []byte(cfgJSON), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	bytesBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}

	cmd := exec.Command(binaryPath, "dispatch", "enroll", "--dir", dir, "--config", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("enroll: %v\n%s", err, output)
	}
	want := "Already enrolled owner/name (" + dir + ") → session work"
	if !strings.Contains(string(output), want) {
		t.Errorf("enroll output = %q, want to contain %q", output, want)
	}

	got, qerr := dispatch.QueryEnrollment(configPath, "owner/name")
	if qerr != nil {
		t.Fatalf("QueryEnrollment: %v", qerr)
	}
	if got.Session != "work" {
		t.Errorf("QueryEnrollment.Session = %q, want unchanged %q", got.Session, "work")
	}

	bytesAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if !bytes.Equal(bytesBefore, bytesAfter) {
		t.Errorf("config file bytes changed on a no-flag re-enroll of an already-sessioned repo")
	}
}

// TestDispatchEnroll_NoFlagOnFreshRepo_WarnsNoTmuxSessionSet covers enrolling
// a fresh repo with no --session flag: the warning names the exact fix
// command and the resolved config path.
func TestDispatchEnroll_NoFlagOnFreshRepo_WarnsNoTmuxSessionSet(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := filepath.Join(t.TempDir(), "config.json")

	cmd := exec.Command(binaryPath, "dispatch", "enroll", "--dir", dir, "--config", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("enroll: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "no tmux session set") {
		t.Errorf("enroll output = %q, want to contain %q", output, "no tmux session set")
	}
	if !strings.Contains(string(output), "cenci dispatch enroll --session") {
		t.Errorf("enroll output = %q, want the literal fix command %q", output, "cenci dispatch enroll --session")
	}
	if !strings.Contains(string(output), configPath) {
		t.Errorf("enroll output = %q, want the resolved config path %q", output, configPath)
	}
}

// TestDispatchEnroll_SecondNoFlagEnroll_AlreadyEnrolledAndWarns_NeverWrites
// covers re-enrolling a still-session-less repo with no --session flag: both
// the idempotent "Already enrolled" line and the warning print, and the
// warning is output-only -- it never triggers a write.
func TestDispatchEnroll_SecondNoFlagEnroll_AlreadyEnrolledAndWarns_NeverWrites(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := filepath.Join(t.TempDir(), "config.json")

	cmd := exec.Command(binaryPath, "dispatch", "enroll", "--dir", dir, "--config", configPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("first enroll: %v\n%s", err, output)
	}

	bytesBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	statBefore, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}

	cmd = exec.Command(binaryPath, "dispatch", "enroll", "--dir", dir, "--config", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("second enroll: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Already enrolled") {
		t.Errorf("second enroll output = %q, want the idempotent path", output)
	}
	if !strings.Contains(string(output), "no tmux session set") {
		t.Errorf("second enroll output = %q, want the warning to still print", output)
	}

	bytesAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	statAfter, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if !bytes.Equal(bytesBefore, bytesAfter) {
		t.Errorf("config file bytes changed on the second no-flag enroll (warning must be output-only, never a write)")
	}
	if !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Errorf("config mtime changed on the second no-flag enroll: before=%v after=%v", statBefore.ModTime(), statAfter.ModTime())
	}
}

// TestDispatchEnroll_BlankSessionFlag_Exits2NeverWrites covers the empty and
// whitespace-only --session cases: both must exit 2 with a one-line stderr,
// and must never create or modify the config file (no silent clear).
func TestDispatchEnroll_BlankSessionFlag_Exits2NeverWrites(t *testing.T) {
	for _, tt := range []struct {
		name    string
		session string
	}{
		{"empty", ""},
		{"whitespace-only", "   "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := initGitRemote(t, "git@github.com:owner/name.git")
			configPath := filepath.Join(t.TempDir(), "config.json")

			cmd := exec.Command(binaryPath, "dispatch", "enroll", "--dir", dir, "--config", configPath, "--session", tt.session)
			output, err := cmd.CombinedOutput()

			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
			}
			if exitErr.ExitCode() != 2 {
				t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
			}
			if strings.Count(strings.TrimRight(string(output), "\n"), "\n") != 0 {
				t.Errorf("stderr = %q, want exactly one line", output)
			}
			if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
				t.Errorf("config file must not be created for a blank --session, but %s exists", configPath)
			}
		})
	}
}

func TestDispatchUnenroll_RemovesRepo_AndNotEnrolledIsNoop(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := filepath.Join(t.TempDir(), "config.json")

	if _, _, err := dispatch.EnrollRepo(configPath, dispatch.RepoIdentity{Repo: "owner/name", Dir: dir}, ""); err != nil {
		t.Fatalf("EnrollRepo setup: %v", err)
	}

	cmd := exec.Command(binaryPath, "dispatch", "unenroll", "--dir", dir, "--config", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unenroll: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Unenrolled owner/name") {
		t.Errorf("unenroll output = %q, want to contain %q", output, "Unenrolled owner/name")
	}

	got, qerr := dispatch.QueryEnrollment(configPath, "owner/name")
	if qerr != nil {
		t.Fatalf("QueryEnrollment: %v", qerr)
	}
	if got.Enrolled {
		t.Errorf("QueryEnrollment after unenroll = %+v, want Enrolled=false", got)
	}

	// Unenrolling again (already not enrolled) is exit 0, "Not enrolled".
	cmd = exec.Command(binaryPath, "dispatch", "unenroll", "--dir", dir, "--config", configPath)
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("second unenroll: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Not enrolled: owner/name") {
		t.Errorf("second unenroll output = %q, want to contain %q", output, "Not enrolled: owner/name")
	}
}

func TestDispatchUnenroll_ViaRepoFlag_NoGitRequired(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if _, _, err := dispatch.EnrollRepo(configPath, dispatch.RepoIdentity{Repo: "owner/name", Dir: "/some/configured/dir"}, ""); err != nil {
		t.Fatalf("EnrollRepo setup: %v", err)
	}

	// cwd is a plain, non-git directory: --repo must bypass git detection
	// entirely.
	noGitDir := t.TempDir()

	cmd := exec.Command(binaryPath, "dispatch", "unenroll", "--repo", "owner/name", "--config", configPath)
	cmd.Dir = noGitDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unenroll --repo: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Unenrolled owner/name") {
		t.Errorf("unenroll --repo output = %q, want to contain %q", output, "Unenrolled owner/name")
	}

	got, qerr := dispatch.QueryEnrollment(configPath, "owner/name")
	if qerr != nil {
		t.Fatalf("QueryEnrollment: %v", qerr)
	}
	if got.Enrolled {
		t.Errorf("QueryEnrollment after unenroll --repo = %+v, want Enrolled=false", got)
	}
}

func TestDispatchUnenroll_RepoAndExplicitDir_Exits2(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	explicitDir := t.TempDir()

	cmd := exec.Command(binaryPath, "dispatch", "unenroll",
		"--repo", "owner/name", "--dir", explicitDir, "--config", configPath)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError for --repo + explicit --dir, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
}

// TestDispatchStatusJSON_Enrolled asserts the pre-#219 enrollment fields
// individually rather than via an exact-byte comparison against
// dispatch.RepoEnrollment's marshaled shape: #219 adds an always-present
// "loop" key (see TestDispatchStatusJSON_IncludesLoopKey), which an
// RepoEnrollment-shaped exact match can no longer represent.
func TestDispatchStatusJSON_Enrolled(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := filepath.Join(t.TempDir(), "config.json")
	if _, _, err := dispatch.EnrollRepo(configPath, dispatch.RepoIdentity{Repo: "owner/name", Dir: dir}, ""); err != nil {
		t.Fatalf("EnrollRepo setup: %v", err)
	}

	cmd := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath, "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, output)
	}

	var got dispatch.RepoEnrollment
	if uerr := json.Unmarshal(output, &got); uerr != nil {
		t.Fatalf("unmarshal status --json output %q: %v", output, uerr)
	}
	want := dispatch.RepoEnrollment{Repo: "owner/name", Dir: dir, Enrolled: true}
	if got != want {
		t.Errorf("status --json output = %+v, want %+v", got, want)
	}
}

func TestDispatchStatusJSON_NotEnrolled_DetectedDir(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	// Config file deliberately does not exist (not even its parent dir).
	configPath := filepath.Join(t.TempDir(), "nested", "does-not-exist", "config.json")

	cmd := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath, "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status --json (not enrolled): %v\n%s", err, output)
	}

	var got dispatch.RepoEnrollment
	if uerr := json.Unmarshal(output, &got); uerr != nil {
		t.Fatalf("unmarshal status --json output %q: %v", output, uerr)
	}
	if got.Enrolled {
		t.Errorf("got.Enrolled = true, want false")
	}
	if got.Repo != "owner/name" {
		t.Errorf("got.Repo = %q, want %q", got.Repo, "owner/name")
	}
	if got.Dir != dir {
		t.Errorf("got.Dir = %q, want detected dir %q (non-empty even though config is missing)", got.Dir, dir)
	}

	if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
		t.Errorf("status must not create a config file, but %s exists", configPath)
	}
}

// TestDispatchStatusJSON_NotEnrolled_IncludesEmptySessionKey covers (#933)
// that `dispatch status --json` on a not-enrolled repo still advertises a
// "session" key (schema-agnostic decode), and it is empty.
func TestDispatchStatusJSON_NotEnrolled_IncludesEmptySessionKey(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := filepath.Join(t.TempDir(), "config.json")

	cmd := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath, "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status --json (not enrolled): %v\n%s", err, output)
	}

	var got map[string]any
	if uerr := json.Unmarshal(output, &got); uerr != nil {
		t.Fatalf("unmarshal status --json output %q: %v", output, uerr)
	}
	session, ok := got["session"]
	if !ok {
		t.Fatalf("status --json output %s missing key %q", output, "session")
	}
	if session != "" {
		t.Errorf("session = %v, want %q", session, "")
	}
}

func TestDispatchStatus_HumanOutput(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := filepath.Join(t.TempDir(), "config.json")

	// Not enrolled yet.
	cmd := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status (not enrolled): %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Not enrolled: owner/name") {
		t.Errorf("status (not enrolled) output = %q, want to contain %q", output, "Not enrolled: owner/name")
	}

	if _, _, err := dispatch.EnrollRepo(configPath, dispatch.RepoIdentity{Repo: "owner/name", Dir: dir}, ""); err != nil {
		t.Fatalf("EnrollRepo setup: %v", err)
	}

	cmd = exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath)
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status (enrolled): %v\n%s", err, output)
	}
	want := "Enrolled owner/name (" + dir + ")"
	if !strings.Contains(string(output), want) {
		t.Errorf("status (enrolled) output = %q, want to contain %q", output, want)
	}
}

// TestDispatchStatus_HumanOutput_ShowsSessionOrNoSessionHint covers (#933)
// the two enrolled-human-output shapes: an arrow naming the session when
// set, and an inline "no tmux session set" hint when it is not.
func TestDispatchStatus_HumanOutput_ShowsSessionOrNoSessionHint(t *testing.T) {
	t.Run("session set", func(t *testing.T) {
		dir := initGitRemote(t, "git@github.com:owner/name.git")
		configPath := filepath.Join(t.TempDir(), "config.json")
		cfgJSON := fmt.Sprintf(`{"dispatch": {"repos": [{"repo": "owner/name", "dir": %q, "session": "a-work"}]}}`, dir)
		if err := os.WriteFile(configPath, []byte(cfgJSON), 0o600); err != nil {
			t.Fatalf("writing config: %v", err)
		}

		cmd := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("status: %v\n%s", err, output)
		}
		want := "Enrolled owner/name (" + dir + ") → session a-work"
		if !strings.Contains(string(output), want) {
			t.Errorf("status output = %q, want to contain %q", output, want)
		}
	})

	t.Run("session unset", func(t *testing.T) {
		dir := initGitRemote(t, "git@github.com:owner/name.git")
		configPath := filepath.Join(t.TempDir(), "config.json")
		if _, _, err := dispatch.EnrollRepo(configPath, dispatch.RepoIdentity{Repo: "owner/name", Dir: dir}, ""); err != nil {
			t.Fatalf("EnrollRepo setup: %v", err)
		}

		cmd := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("status: %v\n%s", err, output)
		}
		want := "Enrolled owner/name (" + dir + "); no tmux session set"
		if !strings.Contains(string(output), want) {
			t.Errorf("status output = %q, want to contain %q", output, want)
		}
	})
}

// -- dispatch loop on|off|status sub-verb (ticket #219) ---------------------

// useTempSocketDir isolates a test from any ambient cenci daemon by
// redirecting watch.DefaultSocketPath() to an empty temp dir, so a test
// asserting daemon_running:false holds even on a machine/CI runner with a
// live daemon socket bound. See docs/test-isolation.md.
func useTempSocketDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	// t.TempDir() alone is created 0755 (masked by the process umask, not
	// 0700 — see testing.common.TempDir), which is looser than
	// CENCI_SOCKET_DIR's tier-1 leaf hardening tolerates without a warning
	// (#1142's verbatim override means dir IS the leaf); chmod it explicitly
	// so exact-output/JSON assertions aren't polluted by a spurious
	// loose-permissions warning on stderr.
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatalf("hardening CENCI_SOCKET_DIR %q: %v", dir, err)
	}
	t.Setenv("CENCI_SOCKET_DIR", dir)
}

// TestDispatchLoopStatusJSON_NoDaemon locks in that `dispatch loop status
// --json`, with no daemon reachable, prints a raw watch.DispatchState JSON
// object with daemon_running:false and enabled resolved from config.json.
func TestDispatchLoopStatusJSON_NoDaemon(t *testing.T) {
	useTempSocketDir(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"dispatch": {"loopEnabled": true, "daemonInterval": "5m"}}`), 0o600); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	cmd := exec.Command(binaryPath, "dispatch", "loop", "status", "--config", configPath, "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dispatch loop status --json: %v\n%s", err, output)
	}

	var got watch.DispatchState
	if uerr := json.Unmarshal(output, &got); uerr != nil {
		t.Fatalf("unmarshal loop status --json output %q: %v", output, uerr)
	}
	if got.DaemonRunning {
		t.Errorf("DaemonRunning = true, want false (no daemon reachable)")
	}
	if !got.Enabled {
		t.Errorf("Enabled = %v, want true (from config.json dispatch.loopEnabled)", got.Enabled)
	}
	if got.Interval != "5m" {
		t.Errorf("Interval = %q, want %q (from config.json dispatch.daemonInterval)", got.Interval, "5m")
	}
}

// TestDispatchLoopOnOff_WritesConfigAndRendersSameAsStatus locks in that
// `dispatch loop on`/`off`:
//  1. persist the toggle to config.json (verified via dispatch.LoadConfig,
//     not just by re-invoking the CLI), and
//  2. print the resulting state using the exact same rendering `dispatch loop
//     status` would print immediately after — not merely an echo of the
//     write (e.g. "Enrolled ..."-style text divorced from actual state).
func TestDispatchLoopOnOff_WritesConfigAndRendersSameAsStatus(t *testing.T) {
	useTempSocketDir(t)

	configPath := filepath.Join(t.TempDir(), "config.json")

	onCmd := exec.Command(binaryPath, "dispatch", "loop", "on", "--config", configPath)
	onOutput, err := onCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dispatch loop on: %v\n%s", err, onOutput)
	}

	cfg, err := dispatch.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig after loop on: %v", err)
	}
	if !cfg.LoopEnabled {
		t.Errorf("cfg.LoopEnabled after `dispatch loop on` = %v, want true", cfg.LoopEnabled)
	}

	statusAfterOnCmd := exec.Command(binaryPath, "dispatch", "loop", "status", "--config", configPath)
	statusAfterOn, err := statusAfterOnCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dispatch loop status (after on): %v\n%s", err, statusAfterOn)
	}
	if string(onOutput) != string(statusAfterOn) {
		t.Errorf("`dispatch loop on` output = %q, want identical rendering to a subsequent `dispatch loop status` = %q", onOutput, statusAfterOn)
	}

	offCmd := exec.Command(binaryPath, "dispatch", "loop", "off", "--config", configPath)
	offOutput, err := offCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dispatch loop off: %v\n%s", err, offOutput)
	}

	cfg, err = dispatch.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig after loop off: %v", err)
	}
	if cfg.LoopEnabled {
		t.Errorf("cfg.LoopEnabled after `dispatch loop off` = %v, want false", cfg.LoopEnabled)
	}

	statusAfterOffCmd := exec.Command(binaryPath, "dispatch", "loop", "status", "--config", configPath)
	statusAfterOff, err := statusAfterOffCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dispatch loop status (after off): %v\n%s", err, statusAfterOff)
	}
	if string(offOutput) != string(statusAfterOff) {
		t.Errorf("`dispatch loop off` output = %q, want identical rendering to a subsequent `dispatch loop status` = %q", offOutput, statusAfterOff)
	}

	// on/off must actually toggle a distinct state, not print identical text
	// regardless of the mutation.
	if string(statusAfterOn) == string(statusAfterOff) {
		t.Errorf("status rendering was identical before and after toggling the loop off: %q", statusAfterOn)
	}
}

// TestDispatchLoopNoArgs_Exits2NeverMutatesConfig locks in that `cenci
// dispatch loop` with no verb (here, a bare flag that starts with "-") hits
// the "expected a subcommand" branch before any flag parsing, config read, or
// socket dial: it must exit 2, print the exact usage error to stderr, leave
// --config's path untouched (no file created), and never print a rendered
// dispatch-state line (which would mean ResolveDispatchState ran).
func TestDispatchLoopNoArgs_Exits2NeverMutatesConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")

	cmd := exec.Command(binaryPath, "dispatch", "loop", "--config", cfgPath)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "cenci dispatch loop: expected a subcommand: on, off, or status") {
		t.Errorf("stderr = %q, want to contain %q", output, "cenci dispatch loop: expected a subcommand: on, off, or status")
	}
	if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
		t.Errorf("cfgPath = %s must not exist after a no-args `dispatch loop` (no config mutation), stat err = %v", cfgPath, statErr)
	}
	if strings.Contains(string(output), "Dispatch loop:") {
		t.Errorf("output must not contain a rendered dispatch-state line (ResolveDispatchState must never run), got:\n%s", output)
	}
}

// TestDispatchLoopUnknownVerb_Exits2NeverMutatesConfig locks in that
// `cenci dispatch loop garbage` hits the `default` case of the verb
// switch before SetLoopEnabled/ResolveDispatchState run: it must exit 2,
// print the exact unknown-subcommand error to stderr, and leave --config's
// path untouched (no file created).
func TestDispatchLoopUnknownVerb_Exits2NeverMutatesConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")

	cmd := exec.Command(binaryPath, "dispatch", "loop", "garbage", "--config", cfgPath)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), `cenci dispatch loop: unknown subcommand "garbage"`) {
		t.Errorf("stderr = %q, want to contain %q", output, `cenci dispatch loop: unknown subcommand "garbage"`)
	}
	if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
		t.Errorf("cfgPath = %s must not exist after `dispatch loop garbage` (no config mutation), stat err = %v", cfgPath, statErr)
	}
}

// TestDispatchStatusJSON_IncludesLoopKey locks in that `dispatch status
// --json` gains a top-level "loop" key (a watch.DispatchState) while every
// pre-existing key (repo, dir, enrolled) is still present and correct. Every
// existing key is asserted explicitly so a future edit can't silently drop
// one.
func TestDispatchStatusJSON_IncludesLoopKey(t *testing.T) {
	useTempSocketDir(t)

	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := filepath.Join(t.TempDir(), "config.json")
	// Seeded directly (rather than via EnrollRepo) so this test controls the
	// exact session value it asserts against below.
	cfgJSON := fmt.Sprintf(`{"dispatch": {"repos": [{"repo": "owner/name", "dir": %q, "session": "a-work"}]}}`, dir)
	if err := os.WriteFile(configPath, []byte(cfgJSON), 0o600); err != nil {
		t.Fatalf("seeding config: %v", err)
	}
	if err := dispatch.SetLoopEnabled(configPath, true); err != nil {
		t.Fatalf("SetLoopEnabled setup: %v", err)
	}

	cmd := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath, "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dispatch status --json: %v\n%s", err, output)
	}

	var got struct {
		Repo     string              `json:"repo"`
		Dir      string              `json:"dir"`
		Enrolled bool                `json:"enrolled"`
		Session  string              `json:"session"`
		Loop     watch.DispatchState `json:"loop"`
	}
	if uerr := json.Unmarshal(output, &got); uerr != nil {
		t.Fatalf("unmarshal status --json output %q: %v", output, uerr)
	}

	if got.Repo != "owner/name" {
		t.Errorf("repo = %q, want %q", got.Repo, "owner/name")
	}
	if got.Dir != dir {
		t.Errorf("dir = %q, want %q", got.Dir, dir)
	}
	if !got.Enrolled {
		t.Errorf("enrolled = %v, want true", got.Enrolled)
	}
	if got.Session != "a-work" {
		t.Errorf("session = %q, want %q", got.Session, "a-work")
	}
	if got.Loop.DaemonRunning {
		t.Errorf("loop.daemon_running = true, want false (no daemon reachable)")
	}
	if !got.Loop.Enabled {
		t.Errorf("loop.enabled = %v, want true (config was set via SetLoopEnabled)", got.Loop.Enabled)
	}

	// Belt-and-braces: every key from the pre-#219 shape must round-trip
	// through a schema-agnostic decode too, so nothing was dropped or
	// renamed underneath the typed struct above.
	var raw map[string]json.RawMessage
	if uerr := json.Unmarshal(output, &raw); uerr != nil {
		t.Fatalf("unmarshal status --json output %q as raw map: %v", output, uerr)
	}
	for _, key := range []string{"repo", "dir", "enrolled", "session", "loop"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("status --json output %s missing key %q", output, key)
		}
	}
}

// -- dispatch.memoryHeadroom in `dispatch status` (#1290) -------------------

// writeStatusConfig seeds a config.json whose dispatch block enrolls dir and
// carries extra (a raw `"key": value` fragment, or empty) alongside it.
func writeStatusConfig(t *testing.T, dir, extra string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	fragment := ""
	if extra != "" {
		fragment = ", " + extra
	}
	cfgJSON := fmt.Sprintf(`{"dispatch": {"repos": [{"repo": "owner/name", "dir": %q, "session": "a-work"}]%s}}`, dir, fragment)
	if err := os.WriteFile(configPath, []byte(cfgJSON), 0o600); err != nil {
		t.Fatalf("seeding config: %v", err)
	}
	return configPath
}

// TestDispatchStatusJSON_MemoryHeadroom: `dispatch status --json` gains a
// top-level integer `memoryHeadroom` -- 25 when the key is unset, the
// configured value (including 0) otherwise -- for enrolled and not-enrolled
// repos alike, since it is a fleet-wide value.
func TestDispatchStatusJSON_MemoryHeadroom(t *testing.T) {
	cases := []struct {
		name  string
		extra string // raw dispatch-block fragment
		want  float64
	}{
		{"unset defaults to 25", "", 25},
		{"configured 40", `"memoryHeadroom": 40`, 40},
		{"configured 0 (disabled)", `"memoryHeadroom": 0`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempSocketDir(t)
			dir := initGitRemote(t, "git@github.com:owner/name.git")
			configPath := writeStatusConfig(t, dir, tc.extra)

			output, err := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath, "--json").CombinedOutput()
			if err != nil {
				t.Fatalf("dispatch status --json: %v\n%s", err, output)
			}
			var raw map[string]any
			if uerr := json.Unmarshal(output, &raw); uerr != nil {
				t.Fatalf("unmarshal status --json output %q: %v", output, uerr)
			}
			got, ok := raw["memoryHeadroom"]
			if !ok {
				t.Fatalf("status --json output %s missing top-level key %q", output, "memoryHeadroom")
			}
			// encoding/json decodes every JSON number to float64; the value must
			// be an integer, not a fraction or a string.
			if got != tc.want {
				t.Errorf("memoryHeadroom = %v (%T), want the integer %v", got, got, tc.want)
			}
			// Additive key: every pre-existing key is still present.
			for _, key := range []string{"repo", "dir", "enrolled", "session", "loop"} {
				if _, present := raw[key]; !present {
					t.Errorf("status --json output %s lost pre-existing key %q", output, key)
				}
			}
		})
	}

	t.Run("not enrolled repo still reports the fleet value", func(t *testing.T) {
		useTempSocketDir(t)
		dir := initGitRemote(t, "git@github.com:owner/name.git")
		configPath := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(configPath, []byte(`{"dispatch": {"memoryHeadroom": 40}}`), 0o600); err != nil {
			t.Fatalf("seeding config: %v", err)
		}

		output, err := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath, "--json").CombinedOutput()
		if err != nil {
			t.Fatalf("dispatch status --json (not enrolled): %v\n%s", err, output)
		}
		var raw map[string]any
		if uerr := json.Unmarshal(output, &raw); uerr != nil {
			t.Fatalf("unmarshal status --json output %q: %v", output, uerr)
		}
		if raw["memoryHeadroom"] != float64(40) {
			t.Errorf("memoryHeadroom = %v, want 40 (fleet-wide, independent of enrollment)", raw["memoryHeadroom"])
		}
		if raw["enrolled"] != false {
			t.Errorf("enrolled = %v, want false", raw["enrolled"])
		}
	})
}

// TestDispatchStatus_HumanOutput_MemoryHeadroom: the human output gains a
// `Memory headroom: <N>%` line (`disabled` for 0), on both the enrolled and the
// not-enrolled shapes, without disturbing the existing enrollment line.
func TestDispatchStatus_HumanOutput_MemoryHeadroom(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  string
	}{
		{"unset defaults to 25%", "", "Memory headroom: 25%"},
		{"configured 40%", `"memoryHeadroom": 40`, "Memory headroom: 40%"},
		{"zero is disabled", `"memoryHeadroom": 0`, "Memory headroom: disabled"},
	}
	for _, tc := range cases {
		t.Run("enrolled/"+tc.name, func(t *testing.T) {
			dir := initGitRemote(t, "git@github.com:owner/name.git")
			configPath := writeStatusConfig(t, dir, tc.extra)

			output, err := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath).CombinedOutput()
			if err != nil {
				t.Fatalf("dispatch status: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), tc.want) {
				t.Errorf("status output = %q, want it to contain %q", output, tc.want)
			}
			if want := "Enrolled owner/name (" + dir + ") → session a-work"; !strings.Contains(string(output), want) {
				t.Errorf("status output = %q, want the enrollment line %q kept", output, want)
			}
		})
	}

	t.Run("not enrolled shows the fleet value too", func(t *testing.T) {
		dir := initGitRemote(t, "git@github.com:owner/name.git")
		configPath := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(configPath, []byte(`{"dispatch": {"memoryHeadroom": 0}}`), 0o600); err != nil {
			t.Fatalf("seeding config: %v", err)
		}

		output, err := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath).CombinedOutput()
		if err != nil {
			t.Fatalf("dispatch status (not enrolled): %v\n%s", err, output)
		}
		for _, want := range []string{"Not enrolled: owner/name", "Memory headroom: disabled"} {
			if !strings.Contains(string(output), want) {
				t.Errorf("status output = %q, want it to contain %q", output, want)
			}
		}
	})

	t.Run("no config file at all shows the default", func(t *testing.T) {
		dir := initGitRemote(t, "git@github.com:owner/name.git")
		configPath := filepath.Join(t.TempDir(), "absent.json")

		output, err := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath).CombinedOutput()
		if err != nil {
			t.Fatalf("dispatch status (no config): %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "Memory headroom: 25%") {
			t.Errorf("status output = %q, want it to contain %q", output, "Memory headroom: 25%")
		}
	})
}

// TestDispatchStatus_InvalidMemoryHeadroomExits1: an out-of-range or mistyped
// memoryHeadroom makes `dispatch status` print `cenci dispatch status:
// <error>` naming the key and exit 1 -- in human and --json modes -- rather
// than rendering a confident status for a config the dispatch loop will refuse.
func TestDispatchStatus_InvalidMemoryHeadroomExits1(t *testing.T) {
	for _, jsonOut := range []bool{false, true} {
		name := "human"
		if jsonOut {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			useTempSocketDir(t)
			dir := initGitRemote(t, "git@github.com:owner/name.git")
			configPath := writeStatusConfig(t, dir, `"memoryHeadroom": 101`)

			args := []string{"dispatch", "status", "--dir", dir, "--config", configPath}
			if jsonOut {
				args = append(args, "--json")
			}
			output, err := exec.Command(binaryPath, args...).CombinedOutput()

			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
			}
			if exitErr.ExitCode() != 1 {
				t.Errorf("exit code = %d, want 1\n%s", exitErr.ExitCode(), output)
			}
			if !strings.Contains(string(output), "cenci dispatch status: ") {
				t.Errorf("output = %q, want the %q prefix", output, "cenci dispatch status: ")
			}
			if !strings.Contains(string(output), "memoryHeadroom") {
				t.Errorf("output = %q, want the error to name memoryHeadroom", output)
			}
			if strings.Contains(string(output), "Memory headroom:") || strings.Contains(string(output), `"memoryHeadroom":`) {
				t.Errorf("output = %q must not render a status for an invalid config", output)
			}
		})
	}
}

// -- dispatch status: the dailyQuota line (#1566) ------------------------------

// quotaStatusRun runs `cenci dispatch status` for a repo whose config carries
// the raw dailyQuota fragment (empty for none), with XDG_STATE_HOME pointed at
// stateHome so the binary reads only the capacity store the test owns.
func quotaStatusRun(t *testing.T, stateHome, extra string, extraArgs ...string) (string, error) {
	t.Helper()
	useTempSocketDir(t)
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := writeStatusConfig(t, dir, extra)
	args := append([]string{"dispatch", "status", "--dir", dir, "--config", configPath}, extraArgs...)
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+stateHome)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// seedQuotaFile writes content as the persisted quota file under stateHome and
// returns its path. The directories are 0700, as the store requires.
func seedQuotaFile(t *testing.T, stateHome, content string) string {
	t.Helper()
	capDir := filepath.Join(stateHome, "cenci", "capacity")
	if err := os.MkdirAll(capDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{stateHome, filepath.Join(stateHome, "cenci"), capDir} {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(capDir, "daily-quota.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// `dailyQuota: off` for an absent key and for the explicit "off", with the store
// never created: an opted-out host's status read touches nothing.
func TestDispatchStatus_HumanOutput_DailyQuotaOff(t *testing.T) {
	for name, extra := range map[string]string{"absent": "", `"off"`: `"dailyQuota": "off"`} {
		t.Run(name, func(t *testing.T) {
			stateHome := t.TempDir()
			out, err := quotaStatusRun(t, stateHome, extra)
			if err != nil {
				t.Fatalf("dispatch status: %v\n%s", err, out)
			}
			if !strings.Contains(out, "dailyQuota: off\n") {
				t.Errorf("status output = %q, want the line %q", out, "dailyQuota: off")
			}
			if _, statErr := os.Lstat(filepath.Join(stateHome, "cenci")); !os.IsNotExist(statErr) {
				t.Errorf("a status read with the quota off created the state tree (Lstat err = %v)", statErr)
			}
		})
	}
}

// `dailyQuota: N per local day (used X)` from the persisted count: 0 before any
// launch (and nothing created by the read), the seeded count for today, and 0
// for a stored date that is not today.
func TestDispatchStatus_HumanOutput_DailyQuotaUsed(t *testing.T) {
	t.Run("no store yet reads 0 and creates nothing", func(t *testing.T) {
		stateHome := t.TempDir()
		out, err := quotaStatusRun(t, stateHome, `"dailyQuota": 3`)
		if err != nil {
			t.Fatalf("dispatch status: %v\n%s", err, out)
		}
		if want := "dailyQuota: 3 per local day (used 0)\n"; !strings.Contains(out, want) {
			t.Errorf("status output = %q, want it to contain %q", out, want)
		}
		if _, statErr := os.Lstat(filepath.Join(stateHome, "cenci")); !os.IsNotExist(statErr) {
			t.Errorf("a status read created the state tree (Lstat err = %v)", statErr)
		}
	})

	t.Run("today's count", func(t *testing.T) {
		const want = "dailyQuota: 3 per local day (used 2)\n"
		// The only way a run can miss is a local midnight between the seed and
		// the read; one retry reseeds against the new day.
		var out string
		var err error
		for range 2 {
			stateHome := t.TempDir()
			seedQuotaFile(t, stateHome, fmt.Sprintf(`{"schema":1,"date":%q,"used":2}`, time.Now().Format("2006-01-02")))
			out, err = quotaStatusRun(t, stateHome, `"dailyQuota": 3`)
			if err == nil && strings.Contains(out, want) {
				return
			}
		}
		t.Errorf("status output = %q (err %v), want it to contain %q", out, err, want)
	})

	t.Run("another day's count reads 0", func(t *testing.T) {
		stateHome := t.TempDir()
		seedQuotaFile(t, stateHome, `{"schema":1,"date":"2000-01-01","used":9}`)
		out, err := quotaStatusRun(t, stateHome, `"dailyQuota": 3`)
		if err != nil {
			t.Fatalf("dispatch status: %v\n%s", err, out)
		}
		if want := "dailyQuota: 3 per local day (used 0)\n"; !strings.Contains(out, want) {
			t.Errorf("status output = %q, want it to contain %q", out, want)
		}
	})
}

// An unreadable state still exits 0 and names the reason in place of the count,
// without rewriting the file.
func TestDispatchStatus_HumanOutput_DailyQuotaUnreadable(t *testing.T) {
	stateHome := t.TempDir()
	path := seedQuotaFile(t, stateHome, "{definitely not a quota file")
	out, err := quotaStatusRun(t, stateHome, `"dailyQuota": 3`)
	if err != nil {
		t.Fatalf("dispatch status exited non-zero for an unreadable quota state: %v\n%s", err, out)
	}
	if want := "dailyQuota: 3 per local day (daily quota state unreadable: " + capacity.ErrCorrupt.Error() + ")\n"; !strings.Contains(out, want) {
		t.Errorf("status output = %q, want it to contain %q", out, want)
	}
	if got, _ := os.ReadFile(path); string(got) != "{definitely not a quota file" {
		t.Errorf("a status read rewrote the quota file: %q", got)
	}
}

// The line sits right after the memory-headroom line, and --json is unchanged:
// no dailyQuota key appears.
func TestDispatchStatus_DailyQuotaLinePlacementAndJSONUnchanged(t *testing.T) {
	out, err := quotaStatusRun(t, t.TempDir(), `"dailyQuota": 3`)
	if err != nil {
		t.Fatalf("dispatch status: %v\n%s", err, out)
	}
	headroom := strings.Index(out, "Memory headroom: ")
	quota := strings.Index(out, "dailyQuota: ")
	filters := strings.Index(out, "Filters: ")
	if headroom < 0 || quota < headroom || filters < quota {
		t.Errorf("status output = %q, want the dailyQuota line after the memory-headroom line and before the filters", out)
	}

	jsonOut, err := quotaStatusRun(t, t.TempDir(), `"dailyQuota": 3`, "--json")
	if err != nil {
		t.Fatalf("dispatch status --json: %v\n%s", err, jsonOut)
	}
	var raw map[string]any
	if uerr := json.Unmarshal([]byte(jsonOut), &raw); uerr != nil {
		t.Fatalf("unmarshal status --json output %q: %v", jsonOut, uerr)
	}
	if _, present := raw["dailyQuota"]; present {
		t.Errorf("status --json output %s gained a dailyQuota key; the JSON shape is unchanged", jsonOut)
	}
}

func TestDispatchFlagRouting_DryRunUnaffectedBySubVerbPeel(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")

	cmd := exec.Command(binaryPath, "dispatch", "--dry-run", "--config", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dispatch --dry-run --config: %v\n%s", err, output)
	}
	for _, verbOutput := range []string{"Enrolled", "Unenrolled", "Not enrolled"} {
		if strings.Contains(string(output), verbOutput) {
			t.Errorf("dispatch --dry-run output must not contain enroll-verb output %q, got:\n%s", verbOutput, output)
		}
	}
}

func TestDispatchPassFailuresExitNonzero(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"dispatch":{"repos":[{"repo":"o/r","dir":"/definitely/missing-cenci-repo"}]}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	for _, args := range [][]string{
		{"dispatch", "--once", "--config", configPath},
		{"dispatch", "--dry-run", "--config", configPath},
		{"dispatch", "--reconcile", "--config", configPath},
		{"dispatch", "--interval", "1s", "--config", configPath},
	} {
		cmd := exec.Command(binaryPath, args...)
		output, err := cmd.CombinedOutput()
		exitErr, ok := err.(*exec.ExitError)
		if !ok || exitErr.ExitCode() != 1 {
			t.Errorf("%v exit = %v, want code 1; output:\n%s", args, err, output)
		}
	}
}

// TestDispatchModelFlag_OverridesPersistedConfig locks in that --model
// survives the enroll/unenroll/status sub-verb peel in runDispatch, reaches
// dispatch.LoadConfig's cfg.Model, and wins over a persisted config.json
// "dispatch.model" value — the fix for a dispatch pass silently inheriting
// whatever ambient default model was active at spawn time.
func TestDispatchModelFlag_OverridesPersistedConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"dispatch": {"model": "fable"}}`), 0o600); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	cmd := exec.Command(binaryPath, "dispatch", "--model", "claude-sonnet-5", "--dry-run", "--config", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dispatch --model --dry-run --config: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), `model override "claude-sonnet-5"`) {
		t.Errorf("expected the --model override to be logged, got:\n%s", output)
	}
	if strings.Contains(string(output), "fable") {
		t.Errorf("expected --model to win over the persisted config model, got:\n%s", output)
	}
}

// TestDispatchNoModelFlag_UsesPersistedConfig locks in that omitting --model
// falls back to config.json's persisted "dispatch.model" (not an empty
// override wiping it out).
func TestDispatchNoModelFlag_UsesPersistedConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"dispatch": {"model": "claude-sonnet-5"}}`), 0o600); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	cmd := exec.Command(binaryPath, "dispatch", "--dry-run", "--config", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dispatch --dry-run --config: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), `model override "claude-sonnet-5"`) {
		t.Errorf("expected the persisted config model to be logged when --model is omitted, got:\n%s", output)
	}
}

func TestDispatchUnknownVerb_Exits2NeverDispatches(t *testing.T) {
	cmd := exec.Command(binaryPath, "dispatch", "statas", "--json", "--dir", "X")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), `cenci dispatch: unknown subcommand "statas"`) {
		t.Errorf("stderr = %q, want to contain %q", output, `cenci dispatch: unknown subcommand "statas"`)
	}
	if strings.Contains(string(output), "skip:") || strings.Contains(string(output), "dispatch (") {
		t.Errorf("output must not contain dispatch decision-table lines (a real dispatch pass must never run), got:\n%s", output)
	}
}

func TestDispatchTrailingUnexpectedArg_Exits2(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "does-not-exist", "config.json")
	cmd := exec.Command(binaryPath, "dispatch", "--dry-run", "--config", configPath, "extra")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), `unexpected argument "extra"`) {
		t.Errorf("stderr = %q, want to contain %q", output, `unexpected argument "extra"`)
	}
}

// -- machine-local pickup filters in `dispatch status` and the one-off pass (#1385) --

// writeFilteredStatusConfig seeds a config.json enrolling owner/name at dir with
// the given raw JSON "filters" value ("" omits the key).
func writeFilteredStatusConfig(t *testing.T, dir, filters string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	field := ""
	if filters != "" {
		field = `, "filters": ` + filters
	}
	cfgJSON := fmt.Sprintf(`{"dispatch": {"repos": [{"repo": "owner/name", "dir": %q, "session": "a-work"%s}]}}`, dir, field)
	if err := os.WriteFile(configPath, []byte(cfgJSON), 0o600); err != nil {
		t.Fatalf("seeding config: %v", err)
	}
	return configPath
}

// filterStatusJSON is the filter-related part of `dispatch status --json`, with
// each `filters` member kept as raw JSON so a `null` can never pass for `[]`.
type filterStatusJSON struct {
	ConfigPath string                     `json:"configPath"`
	Filters    map[string]json.RawMessage `json:"filters"`
}

func statusFilterJSON(t *testing.T, dir, configPath string) (filterStatusJSON, map[string]string) {
	t.Helper()
	useTempSocketDir(t)
	output, err := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath, "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("dispatch status --json: %v\n%s", err, output)
	}
	var got filterStatusJSON
	if uerr := json.Unmarshal(output, &got); uerr != nil {
		t.Fatalf("unmarshal status --json output %q: %v", output, uerr)
	}
	lists := map[string]string{}
	for key, raw := range got.Filters {
		lists[key] = string(raw)
	}
	return got, lists
}

func TestDispatchStatusFilters_JSONRestrictedCollapsesDuplicates(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := writeFilteredStatusConfig(t, dir,
		`{"milestones": [3, 7, 3], "labelsAll": ["Area:Watch", "  area:watch "], "labelsAny": ["bug", "enhancement", "BUG"], "labelsNone": ["manual-only"]}`)

	got, lists := statusFilterJSON(t, dir, configPath)

	if got.ConfigPath != configPath {
		t.Errorf("configPath = %q, want the selected --config %q", got.ConfigPath, configPath)
	}
	want := map[string]string{
		"restricted": `true`,
		"milestones": `[3,7]`,
		"labelsAll":  `["Area:Watch"]`,
		"labelsAny":  `["bug","enhancement"]`,
		"labelsNone": `["manual-only"]`,
	}
	for key, w := range want {
		if lists[key] != w {
			t.Errorf("filters.%s = %s, want %s", key, lists[key], w)
		}
	}
}

func TestDispatchStatusFilters_JSONUnrestrictedShapesReportEmptyArrays(t *testing.T) {
	cases := []struct{ name, filters string }{
		{"omitted", ""},
		{"empty object", `{}`},
		{"empty lists", `{"milestones": [], "labelsAll": [], "labelsAny": [], "labelsNone": []}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := initGitRemote(t, "git@github.com:owner/name.git")
			configPath := writeFilteredStatusConfig(t, dir, tc.filters)

			got, lists := statusFilterJSON(t, dir, configPath)

			if got.ConfigPath != configPath {
				t.Errorf("configPath = %q, want %q", got.ConfigPath, configPath)
			}
			for key, w := range map[string]string{"restricted": `false`, "milestones": `[]`, "labelsAll": `[]`, "labelsAny": `[]`, "labelsNone": `[]`} {
				if lists[key] != w {
					t.Errorf("filters.%s = %s, want %s", key, lists[key], w)
				}
			}
		})
	}

	t.Run("repo that is not enrolled", func(t *testing.T) {
		dir := initGitRemote(t, "git@github.com:owner/name.git")
		configPath := filepath.Join(t.TempDir(), "config.json")
		other := `{"dispatch": {"repos": [{"repo": "other/repo", "dir": "/x", "filters": {"milestones": [3]}}]}}`
		if err := os.WriteFile(configPath, []byte(other), 0o600); err != nil {
			t.Fatal(err)
		}

		got, lists := statusFilterJSON(t, dir, configPath)

		if got.ConfigPath != configPath {
			t.Errorf("configPath = %q, want %q", got.ConfigPath, configPath)
		}
		for key, w := range map[string]string{"restricted": `false`, "milestones": `[]`, "labelsAll": `[]`, "labelsAny": `[]`, "labelsNone": `[]`} {
			if lists[key] != w {
				t.Errorf("filters.%s = %s, want %s (another repo's filters must not leak)", key, lists[key], w)
			}
		}
	})
}

func TestDispatchStatusFilters_HumanOutput(t *testing.T) {
	statusOutput := func(t *testing.T, dir, configPath string) string {
		t.Helper()
		output, err := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath).CombinedOutput()
		if err != nil {
			t.Fatalf("dispatch status: %v\n%s", err, output)
		}
		return string(output)
	}

	t.Run("restricted lists each configured dimension in fixed order, then the config path", func(t *testing.T) {
		dir := initGitRemote(t, "git@github.com:owner/name.git")
		configPath := writeFilteredStatusConfig(t, dir,
			`{"labelsNone": ["manual-only"], "milestones": [3, 7, 7], "labelsAny": ["bug", "enhancement"], "labelsAll": ["Area:Watch"]}`)

		out := statusOutput(t, dir, configPath)

		wantLines := []string{
			"Memory headroom: 25%",
			"Filters: milestones [3 7]",
			`Filters: labelsAll ["Area:Watch"]`,
			`Filters: labelsAny ["bug" "enhancement"]`,
			`Filters: labelsNone ["manual-only"]`,
			"Config: " + configPath,
		}
		last := -1
		for _, line := range wantLines {
			idx := strings.Index(out, line+"\n")
			if idx < 0 {
				t.Fatalf("status output lacks the line %q:\n%s", line, out)
			}
			if idx < last {
				t.Errorf("line %q is out of order in:\n%s", line, out)
			}
			last = idx
		}
		if strings.Contains(out, "Filters: unrestricted") {
			t.Errorf("a restricted repo must not print the unrestricted line:\n%s", out)
		}
	})

	t.Run("only configured dimensions print", func(t *testing.T) {
		dir := initGitRemote(t, "git@github.com:owner/name.git")
		configPath := writeFilteredStatusConfig(t, dir, `{"milestones": [3], "labelsAll": []}`)

		out := statusOutput(t, dir, configPath)

		if got := strings.Count(out, "Filters: "); got != 1 || !strings.Contains(out, "Filters: milestones [3]\n") {
			t.Errorf("want exactly the milestones line, got:\n%s", out)
		}
	})

	t.Run("unrestricted enrolled repo", func(t *testing.T) {
		dir := initGitRemote(t, "git@github.com:owner/name.git")
		configPath := writeFilteredStatusConfig(t, dir, `{}`)

		out := statusOutput(t, dir, configPath)

		for _, want := range []string{"Filters: unrestricted\n", "Config: " + configPath + "\n"} {
			if !strings.Contains(out, want) {
				t.Errorf("status output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("repo that is not enrolled", func(t *testing.T) {
		dir := initGitRemote(t, "git@github.com:owner/name.git")
		configPath := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(configPath, []byte(`{"dispatch": {}}`), 0o600); err != nil {
			t.Fatal(err)
		}

		out := statusOutput(t, dir, configPath)

		for _, want := range []string{"Not enrolled: owner/name\n", "Filters: unrestricted\n", "Config: " + configPath + "\n"} {
			if !strings.Contains(out, want) {
				t.Errorf("status output lacks %q:\n%s", want, out)
			}
		}
	})
}

// Filters come only from the selected user config: a committed
// .cenci/config.json filter block has no effect on status.
func TestDispatchStatusFilters_CommittedRepoConfigIsIgnored(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	committed := filepath.Join(dir, ".cenci", "config.json")
	if err := os.MkdirAll(filepath.Dir(committed), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"dispatch": {"repos": [{"repo": "owner/name", "filters": {"milestones": [9], "labelsNone": ["x"]}}]}}`
	if err := os.WriteFile(committed, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := writeFilteredStatusConfig(t, dir, "")

	_, lists := statusFilterJSON(t, dir, configPath)
	if lists["restricted"] != "false" || lists["milestones"] != "[]" || lists["labelsNone"] != "[]" {
		t.Errorf("committed filters leaked into status: %v", lists)
	}

	output, err := exec.Command(binaryPath, "dispatch", "status", "--dir", dir, "--config", configPath).CombinedOutput()
	if err != nil {
		t.Fatalf("dispatch status: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Filters: unrestricted\n") {
		t.Errorf("status output = %q, want Filters: unrestricted", output)
	}
}

// An invalid filters block makes status and the one-off pass exit 1 naming the
// config path, repo and field path, and the pass never reaches gh: nothing
// dispatches and no unrestricted default is used.
func TestDispatchStatusFilters_InvalidFiltersExit1WithoutDispatching(t *testing.T) {
	ghDir := t.TempDir()
	marker := filepath.Join(ghDir, "gh-was-called")
	script := "#!/bin/sh\ntouch " + marker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(ghDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "PATH="+ghDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := writeFilteredStatusConfig(t, dir, `{"milestones": [3, 0]}`)
	wantField := "dispatch.repos[0] (owner/name).filters.milestones[1]"

	cases := []struct {
		name string
		args []string
	}{
		{"status", []string{"dispatch", "status", "--dir", dir, "--config", configPath}},
		{"status --json", []string{"dispatch", "status", "--dir", dir, "--config", configPath, "--json"}},
		{"one-off pass", []string{"dispatch", "--once", "--config", configPath}},
		{"dry run", []string{"dispatch", "--dry-run", "--config", configPath}},
		{"interval loop", []string{"dispatch", "--interval", "1s", "--config", configPath}},
		{"reconcile", []string{"dispatch", "--reconcile", "--config", configPath}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempSocketDir(t)
			cmd := exec.Command(binaryPath, tc.args...)
			cmd.Env = env
			output, err := cmd.CombinedOutput()

			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 1 {
				t.Fatalf("exit = %v, want code 1\n%s", err, output)
			}
			for _, want := range []string{configPath, wantField} {
				if !strings.Contains(string(output), want) {
					t.Errorf("output lacks %q:\n%s", want, output)
				}
			}
			for _, bad := range []string{"skip:", "dispatch (", "Filters:", `"filters"`} {
				if strings.Contains(string(output), bad) {
					t.Errorf("output must not render a decision or status (%q):\n%s", bad, output)
				}
			}
			if _, err := os.Stat(marker); err == nil {
				t.Errorf("gh was invoked: a failed filter load must stop the pass before any collection")
			}
		})
	}
}

// -- dispatch status: the capacity lines (#1469) --------------------------------

// serveDaemonSnapshot starts a daemon double on CENCI_SOCKET_DIR that serves snap
// to every reader, so the status binary the test runs reads a real snapshot.
func serveDaemonSnapshot(t *testing.T, snap watch.StateSnapshot) {
	t.Helper()
	socketDir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	serveSnapshotAt(t, socketDir, snap)
}

// noContainerRuntimesPath is PATH with a docker and a podman that list nothing in
// front, so the orphan count the status line reads never reaches a developer's
// real container runtime.
func noContainerRuntimesPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"docker", "podman"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

// capacityStatusRun runs `cenci dispatch status` for owner/name with the machine
// cap and the repo's own cap (nil for none) in the config, XDG_STATE_HOME at
// stateHome, and either the daemon double serving snap or no daemon at all.
func capacityStatusRun(t *testing.T, stateHome string, machineCap int, repoCap *int, snap *watch.StateSnapshot, extraArgs ...string) (string, error) {
	t.Helper()
	if snap != nil {
		serveDaemonSnapshot(t, *snap)
	} else {
		useTempSocketDir(t)
	}
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	repoEntry := fmt.Sprintf(`{"repo":"owner/name","dir":%q,"session":"a-work"`, dir)
	if repoCap != nil {
		repoEntry += fmt.Sprintf(`,"concurrencyCap":%d`, *repoCap)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	body := fmt.Sprintf(`{"dispatch":{"concurrencyCap":%d,"repos":[%s}]}}`, machineCap, repoEntry)
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"dispatch", "status", "--dir", dir, "--config", configPath}, extraArgs...)
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+stateHome, "PATH="+noContainerRuntimesPath(t))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func intPtr(n int) *int { return &n }

// ledgerEntryJSON is one confirmed ledger entry as the ledger writes it.
func ledgerEntryJSON(repo string, ticket int, window string) string {
	return fmt.Sprintf(`{"holderId":"h%d","kind":"implement","repo":%q,"ticket":%d,"branch":"","launcherPid":1,"launcherStart":"","socket":"","window":%q,"dir":"","launchId":"","state":"confirmed","createdAt":"2026-07-10T12:00:00Z","updatedAt":"2026-07-10T12:00:00Z"}`, ticket, repo, ticket, window)
}

// seedLedgerFile writes content as the admission ledger under stateHome (0700
// directories, as the store requires) and returns its path.
func seedLedgerFile(t *testing.T, stateHome, content string) string {
	t.Helper()
	capDir := filepath.Join(stateHome, "cenci", "capacity")
	if err := os.MkdirAll(capDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{stateHome, filepath.Join(stateHome, "cenci"), capDir} {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(capDir, "ledger.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runningWindow(name string) watch.WindowState {
	return watch.WindowState{WindowName: name, Status: "running"}
}

// (edge) `dispatch status` with no ledger file prints the capacity line and leaves
// no ledger behind. With no daemon the used count is unknown, and the line says
// why; the repo's own slots come from the ledger alone and stay known.
func TestDispatchStatus_CapacityLines_NoLedgerNoDaemon(t *testing.T) {
	stateHome := t.TempDir()

	out, err := capacityStatusRun(t, stateHome, 3, intPtr(1), nil)

	if err != nil {
		t.Fatalf("dispatch status exited non-zero: %v\n%s", err, out)
	}
	for _, want := range []string{
		"capacity: ?/3 slots (capacity observation unavailable: daemon snapshot)\n",
		"capacity owner/name: 0/1 slots\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output lacks %q:\n%s", want, out)
		}
	}
	if _, statErr := os.Lstat(filepath.Join(stateHome, "cenci")); !os.IsNotExist(statErr) {
		t.Errorf("a status read created the state tree (Lstat err = %v)", statErr)
	}
}

// (P5) The used slots count each worker once under the shared rule: a ledger-held
// worker that is also a running daemon session, plus a running manual session, is
// 2/3; the capped repo's own line counts its ledger entries only.
func TestDispatchStatus_CapacityLines_CountEachWorkerOnce(t *testing.T) {
	stateHome := t.TempDir()
	ledger := seedLedgerFile(t, stateHome, `{"schema":1,"entries":[`+ledgerEntryJSON("owner/name", 42, "42-implement")+`],"waits":[]}`)
	before, _ := os.ReadFile(ledger)
	snap := watch.StateSnapshot{
		Windows: []watch.WindowState{runningWindow("42-implement"), runningWindow("manual-work"), {WindowName: "9-old", Status: "done"}},
		Summary: watch.StatusSummary{Running: 2},
	}

	out, err := capacityStatusRun(t, stateHome, 3, intPtr(1), &snap)

	if err != nil {
		t.Fatalf("dispatch status exited non-zero: %v\n%s", err, out)
	}
	for _, want := range []string{"capacity: 2/3 slots\n", "capacity owner/name: 1/1 slots\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output lacks %q:\n%s", want, out)
		}
	}
	if after, _ := os.ReadFile(ledger); string(after) != string(before) {
		t.Errorf("a status read rewrote the ledger:\n%s", after)
	}
}

// A repo with no repo cap gets no per-repo line, and the lines sit after the
// dailyQuota line and before the filters.
func TestDispatchStatus_CapacityLines_OnlyCappedReposAndPlacement(t *testing.T) {
	snap := watch.StateSnapshot{}

	out, err := capacityStatusRun(t, t.TempDir(), 3, nil, &snap)

	if err != nil {
		t.Fatalf("dispatch status exited non-zero: %v\n%s", err, out)
	}
	if !strings.Contains(out, "capacity: 0/3 slots\n") {
		t.Errorf("status output lacks the capacity line:\n%s", out)
	}
	if strings.Contains(out, "capacity owner/name") {
		t.Errorf("an uncapped repo got its own capacity line:\n%s", out)
	}
	quota, capacityLine, filters := strings.Index(out, "dailyQuota: "), strings.Index(out, "capacity: "), strings.Index(out, "Filters: ")
	if quota < 0 || capacityLine < quota || filters < capacityLine {
		t.Errorf("status output = %q, want the capacity line after the dailyQuota line and before the filters", out)
	}
}

// An unreadable ledger still exits 0 and names the reason in place of the count,
// without rewriting the file.
func TestDispatchStatus_CapacityLine_UnreadableLedger(t *testing.T) {
	stateHome := t.TempDir()
	const corrupt = "{definitely not a ledger"
	path := seedLedgerFile(t, stateHome, corrupt)

	out, err := capacityStatusRun(t, stateHome, 3, nil, &watch.StateSnapshot{})

	if err != nil {
		t.Fatalf("dispatch status exited non-zero for an unreadable ledger: %v\n%s", err, out)
	}
	if want := "capacity: ?/3 slots (capacity ledger unreadable: " + path + ": " + capacity.ErrCorrupt.Error() + ")\n"; !strings.Contains(out, want) {
		t.Errorf("status output lacks %q:\n%s", want, out)
	}
	if got, _ := os.ReadFile(path); string(got) != corrupt {
		t.Errorf("a status read rewrote the ledger: %q", got)
	}
}

// `--json` is unchanged: it gains no capacity key.
func TestDispatchStatus_CapacityJSONUnchanged(t *testing.T) {
	out, err := capacityStatusRun(t, t.TempDir(), 3, intPtr(1), &watch.StateSnapshot{}, "--json")
	if err != nil {
		t.Fatalf("dispatch status --json: %v\n%s", err, out)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("status --json output %q is not one JSON object: %v", out, err)
	}
	for key := range raw {
		if strings.Contains(strings.ToLower(key), "capacity") {
			t.Errorf("status --json gained a %q key; the JSON shape is unchanged", key)
		}
	}
}
