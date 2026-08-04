package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/dispatch"
	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
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
	if strings.Contains(string(output), "session") {
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

func TestDispatchUnenroll_RemovesRepo_AndNotEnrolledIsNoop(t *testing.T) {
	dir := initGitRemote(t, "git@github.com:owner/name.git")
	configPath := filepath.Join(t.TempDir(), "config.json")

	if _, err := dispatch.EnrollRepo(configPath, dispatch.RepoIdentity{Repo: "owner/name", Dir: dir}); err != nil {
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
	if _, err := dispatch.EnrollRepo(configPath, dispatch.RepoIdentity{Repo: "owner/name", Dir: "/some/configured/dir"}); err != nil {
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
	if _, err := dispatch.EnrollRepo(configPath, dispatch.RepoIdentity{Repo: "owner/name", Dir: dir}); err != nil {
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

	if _, err := dispatch.EnrollRepo(configPath, dispatch.RepoIdentity{Repo: "owner/name", Dir: dir}); err != nil {
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

// -- dispatch loop on|off|status sub-verb (ticket #219) ---------------------

// useTempSocketDir isolates a test from any ambient cenci daemon by
// redirecting watch.DefaultSocketPath() to an empty temp dir, so a test
// asserting daemon_running:false holds even on a machine/CI runner with a
// live daemon socket bound. See docs/test-isolation.md.
func useTempSocketDir(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
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
	if _, err := dispatch.EnrollRepo(configPath, dispatch.RepoIdentity{Repo: "owner/name", Dir: dir}); err != nil {
		t.Fatalf("EnrollRepo setup: %v", err)
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
	for _, key := range []string{"repo", "dir", "enrolled", "loop"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("status --json output %s missing key %q", output, key)
		}
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
