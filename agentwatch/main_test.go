package main_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/dispatch"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/ipc"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/pkg/watch"
)

var binaryPath string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "agentwatch-test-*")
	if err != nil {
		panic("failed to create temp dir: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	binaryPath = filepath.Join(tmp, "agentwatch")

	// Build the binary from the current module root.
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("failed to build binary: " + err.Error())
	}

	os.Exit(m.Run())
}

func TestNotifyCodexPromptSendsOnlyCompactTaskName(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "events.sock")
	receiver, err := ipc.NewEventReceiver(socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = receiver.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go receiver.Accept(ctx)

	rawPrompt := "\n  improve\t codex tmux names\x00  \nprivate second line"
	input := `{"hook_event_name":"UserPromptSubmit","session_id":"codex-sess","prompt":` + string(mustJSON(t, rawPrompt)) + `}`
	cmd := exec.Command(binaryPath, "notify", "-agent", "codex", "-event-socket", socket)
	cmd.Stdin = strings.NewReader(input)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("notify: %v: %s", err, output)
	}

	select {
	case event := <-receiver.Events():
		if event.TaskName != "improve codex tmux names" {
			t.Fatalf("task_name = %q", event.TaskName)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "private second line") || strings.Contains(string(encoded), "prompt") {
			t.Fatalf("raw prompt leaked into IPC: %s", encoded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notify event")
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestCodexHooksJSONHasNoUnknownKeys guards against unsupported keys leaking
// into the Codex hooks file. In particular, Codex currently does not support
// asynchronous hooks, and emits a startup warning for each such hook.
func TestCodexHooksJSONHasNoUnknownKeys(t *testing.T) {
	// main_test runs with cwd = package dir (repo root), so this resolves.
	data, err := os.ReadFile(filepath.Join("plugin", "codex", "hooks.json"))
	if err != nil {
		t.Fatalf("read codex hooks.json: %v", err)
	}

	var root struct {
		Hooks map[string][]map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse codex hooks.json: %v", err)
	}
	if len(root.Hooks) == 0 {
		t.Fatal("expected at least one event group in codex hooks.json")
	}

	allowedGroupKeys := map[string]bool{"matcher": true, "hooks": true}
	allowedHookKeys := map[string]bool{"type": true, "command": true, "timeout": true}

	for event, groups := range root.Hooks {
		for i, group := range groups {
			for key := range group {
				if !allowedGroupKeys[key] {
					t.Errorf("%s[%d]: unexpected group key %q (allowed: matcher, hooks)", event, i, key)
				}
			}
			raw, ok := group["hooks"]
			if !ok {
				t.Errorf("%s[%d]: missing required \"hooks\" key", event, i)
				continue
			}
			var hooks []map[string]json.RawMessage
			if err := json.Unmarshal(raw, &hooks); err != nil {
				t.Errorf("%s[%d]: parse hooks array: %v", event, i, err)
				continue
			}
			for j, hook := range hooks {
				for key := range hook {
					if !allowedHookKeys[key] {
						t.Errorf("%s[%d].hooks[%d]: unexpected hook key %q (allowed: type, command, timeout)", event, i, j, key)
					}
				}
			}
		}
	}
}

func TestCodexHooksUsePluginLocalBinary(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("plugin", "codex", "hooks.json"))
	if err != nil {
		t.Fatalf("read codex hooks.json: %v", err)
	}

	var root struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse codex hooks.json: %v", err)
	}

	const localBinary = `"${PLUGIN_ROOT}/bin/agentwatch" notify`
	for event, groups := range root.Hooks {
		for i, group := range groups {
			for j, hook := range group.Hooks {
				if !strings.Contains(hook.Command, localBinary) {
					t.Errorf("%s[%d].hooks[%d] does not use plugin-local binary: %q", event, i, j, hook.Command)
				}
			}
		}
	}
}

func TestCodexStopHookEmitsJSON(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("plugin", "codex", "hooks.json"))
	if err != nil {
		t.Fatalf("read codex hooks.json: %v", err)
	}

	var root struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse codex hooks.json: %v", err)
	}
	stopGroups := root.Hooks["Stop"]
	if len(stopGroups) != 1 || len(stopGroups[0].Hooks) != 1 {
		t.Fatalf("expected exactly one Codex Stop hook, got %#v", stopGroups)
	}

	pluginRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pluginRoot, "bin"), 0o755); err != nil {
		t.Fatalf("create plugin bin dir: %v", err)
	}
	stub := filepath.Join(pluginRoot, "bin", "agentwatch")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatalf("write hook binary stub: %v", err)
	}

	cmd := exec.Command("sh", "-c", stopGroups[0].Hooks[0].Command)
	cmd.Env = append(os.Environ(), "PLUGIN_ROOT="+pluginRoot)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"Stop","session_id":"test"}`)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run Codex Stop hook: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Codex Stop hook output must be JSON, got %q: %v", output, err)
	}
}

func TestUnknownSubcommand_ExitCode2(t *testing.T) {
	cmd := exec.Command(binaryPath, "garbage")
	output, err := cmd.CombinedOutput()

	// Expect a non-zero exit.
	if err == nil {
		t.Fatal("expected non-zero exit code for unknown subcommand, got 0")
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}

	if exitErr.ExitCode() != 2 {
		t.Errorf("expected exit code 2, got %d", exitErr.ExitCode())
	}

	if !strings.Contains(string(output), `unknown subcommand "garbage"`) {
		t.Errorf("expected stderr to contain %q, got:\n%s",
			`unknown subcommand "garbage"`, output)
	}
}

func TestUnknownSubcommand_ErrorMessageFormat(t *testing.T) {
	cmd := exec.Command(binaryPath, "frobnicate")
	output, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected non-zero exit for unknown subcommand, got 0")
	}

	if !strings.Contains(string(output), `agentwatch: unknown subcommand "frobnicate"`) {
		t.Errorf("expected stderr to contain %q, got:\n%s",
			`agentwatch: unknown subcommand "frobnicate"`, output)
	}
}

func TestFlagRouting_DashV_NotUnknownSubcommand(t *testing.T) {
	// -v should route to daemon, not be treated as unknown subcommand.
	// The daemon will likely fail (no tmux), but should NOT print
	// "unknown subcommand". Use a timeout because the daemon blocks if it starts.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, "-v")
	output, _ := cmd.CombinedOutput()

	if strings.Contains(string(output), "unknown subcommand") {
		t.Errorf("expected -v flag NOT to trigger unknown subcommand error, got:\n%s", output)
	}
}

func TestNoArgs_NotUnknownSubcommand(t *testing.T) {
	// No arguments should route to daemon, not unknown subcommand.
	// Use a timeout because the daemon blocks if it starts.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath)
	output, _ := cmd.CombinedOutput()

	if strings.Contains(string(output), "unknown subcommand") {
		t.Errorf("expected no-args NOT to trigger unknown subcommand error, got:\n%s", output)
	}
}

func TestRunSubcommandRoutes(t *testing.T) {
	// `run` with no workflow is a usage error (exit 2) — but must never be
	// mistaken for an unknown subcommand.
	cmd := exec.Command(binaryPath, "run")
	output, _ := cmd.CombinedOutput()
	if strings.Contains(string(output), "unknown subcommand") {
		t.Errorf("run must route to the launcher, got:\n%s", output)
	}
}

func TestRunDryRunPrintsCommandAndWindowName(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	// --slug is ignored for a numeric ticket: the window is `<number>-<skill>`.
	cmd := exec.Command(binaryPath, "run", "implement", "40",
		"--slug", "agentwatch-run", "--session", "demo", "--config", noCfg, "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run failed: %v\n%s", err, output)
	}
	s := string(output)
	if !strings.Contains(s, "40-implement") {
		t.Errorf("expected window name 40-implement, got:\n%s", s)
	}
	// No flag and no config default → the sandbox launcher (#98).
	if !strings.Contains(s, "agent-sand") || !strings.Contains(s, "/agentflow:implement 40") {
		t.Errorf("expected agent-sand command with the agentflow skill, got:\n%s", s)
	}
}

func TestRunDryRunNoSandboxUsesHostCommand(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "implement", "40",
		"--slug", "agentwatch-run", "--session", "demo", "--config", noCfg, "--no-sandbox", "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("no-sandbox dry-run failed: %v\n%s", err, output)
	}
	s := string(output)
	if strings.Contains(s, "agent-sand") {
		t.Errorf("--no-sandbox must not use the sandbox launcher, got:\n%s", s)
	}
	if !strings.Contains(s, "claude") || !strings.Contains(s, "/agentflow:implement 40") {
		t.Errorf("expected host claude command with the agentflow skill, got:\n%s", s)
	}
}

func TestRunForwardsUnquotedCustomText(t *testing.T) {
	// Unquoted multi-word context (id + additional context) must all reach the
	// skill argument, not just the first token — even though the numeric window
	// name is skill-only and the context never appears in it.
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "implement", "99999999", "focus", "on", "the", "API", "layer",
		"--session", "demo", "--config", noCfg, "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("custom-text dry-run failed: %v\n%s", err, output)
	}
	s := string(output)
	if !strings.Contains(s, "/agentflow:implement 99999999 focus on the API layer") {
		t.Errorf("expected full argument forwarded, got:\n%s", s)
	}
	if !strings.Contains(s, "99999999-implement") {
		t.Errorf("expected window name 99999999-implement, got:\n%s", s)
	}
}

func TestRunDryRunSandboxUsesSandboxCommand(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "refine", "40",
		"--slug", "demo", "--session", "demo", "--config", noCfg, "--sandbox", "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox dry-run failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "agent-sand") {
		t.Errorf("expected agent-sand command, got:\n%s", output)
	}
}

func TestRunCodexWithoutTemplateErrors(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "implement", "40",
		"--agent", "codex", "--session", "demo", "--config", noCfg, "--dry-run")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit when no codex template exists")
	}
	if !strings.Contains(strings.ToLower(string(output)), "codex") {
		t.Errorf("expected an error mentioning codex, got:\n%s", output)
	}
}

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
	if !strings.Contains(string(output), "agentwatch dispatch enroll: ") {
		t.Errorf("stderr = %q, want to contain %q", output, "agentwatch dispatch enroll: ")
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

// useTempSocketDir isolates a test from any ambient agentwatch daemon by
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

// TestDispatchLoopNoArgs_Exits2NeverMutatesConfig locks in that `agentwatch
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
	if !strings.Contains(string(output), "agentwatch dispatch loop: expected a subcommand: on, off, or status") {
		t.Errorf("stderr = %q, want to contain %q", output, "agentwatch dispatch loop: expected a subcommand: on, off, or status")
	}
	if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
		t.Errorf("cfgPath = %s must not exist after a no-args `dispatch loop` (no config mutation), stat err = %v", cfgPath, statErr)
	}
	if strings.Contains(string(output), "Dispatch loop:") {
		t.Errorf("output must not contain a rendered dispatch-state line (ResolveDispatchState must never run), got:\n%s", output)
	}
}

// TestDispatchLoopUnknownVerb_Exits2NeverMutatesConfig locks in that
// `agentwatch dispatch loop garbage` hits the `default` case of the verb
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
	if !strings.Contains(string(output), `agentwatch dispatch loop: unknown subcommand "garbage"`) {
		t.Errorf("stderr = %q, want to contain %q", output, `agentwatch dispatch loop: unknown subcommand "garbage"`)
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
	if err := os.WriteFile(configPath, []byte(`{"dispatch":{"repos":[{"repo":"o/r","dir":"/definitely/missing-agentwatch-repo"}]}}`), 0o600); err != nil {
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

func TestStatusAndWaybarSubcommandsBothRoute(t *testing.T) {
	// Both "status" and its hidden alias "waybar" must route to the status
	// frontend. With no daemon on the socket they exit 1 with no output —
	// never the "unknown subcommand" error.
	for _, sub := range []string{"status", "waybar"} {
		cmd := exec.Command(binaryPath, sub, "-socket", filepath.Join(t.TempDir(), "nope.sock"))
		output, err := cmd.CombinedOutput()

		if strings.Contains(string(output), "unknown subcommand") {
			t.Errorf("%s: expected routing to status frontend, got:\n%s", sub, output)
		}
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s: expected exit error (daemon not running), got %T: %v", sub, err, err)
		}
		if exitErr.ExitCode() != 1 {
			t.Errorf("%s: expected exit code 1 when daemon not running, got %d", sub, exitErr.ExitCode())
		}
	}
}

func TestVersionSubcommandPrintsVersion(t *testing.T) {
	cmd := exec.Command(binaryPath, "version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("version: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "agentwatch dev") {
		t.Errorf("version output = %q, want to contain %q", output, "agentwatch dev")
	}
}

func TestVersionFlagsRouteToVersion(t *testing.T) {
	for _, flagArg := range []string{"--version", "-version"} {
		cmd := exec.Command(binaryPath, flagArg)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", flagArg, err, output)
		}
		if !strings.Contains(string(output), "agentwatch dev") {
			t.Errorf("%s output = %q, want to contain %q", flagArg, output, "agentwatch dev")
		}
		if strings.Contains(string(output), "unknown subcommand") {
			t.Errorf("%s output = %q, want NOT to contain %q", flagArg, output, "unknown subcommand")
		}
	}

	// Bare -v must still route to the daemon (existing behavior), not print
	// the version banner. Use a timeout because the daemon blocks if it starts.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, "-v")
	output, _ := cmd.CombinedOutput()
	if strings.Contains(string(output), "agentwatch dev") {
		t.Errorf("-v output = %q, want NOT to contain %q (must route to daemon, not version)", output, "agentwatch dev")
	}
}

func TestVersionStampedViaLdflags(t *testing.T) {
	stampedPath := filepath.Join(t.TempDir(), "agentwatch-stamped")
	build := exec.Command("go", "build", "-ldflags", "-X main.version=9.9.9-test", "-o", stampedPath, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build stamped binary: %v", err)
	}

	cmd := exec.Command(stampedPath, "version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("version: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "9.9.9-test") {
		t.Errorf("version output = %q, want to contain %q (catches a typo'd -X path)", output, "9.9.9-test")
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
	if !strings.Contains(string(output), `agentwatch dispatch: unknown subcommand "statas"`) {
		t.Errorf("stderr = %q, want to contain %q", output, `agentwatch dispatch: unknown subcommand "statas"`)
	}
	if strings.Contains(string(output), "skip:") || strings.Contains(string(output), "dispatch (") {
		t.Errorf("output must not contain dispatch decision-table lines (a real dispatch pass must never run), got:\n%s", output)
	}
}

// TestSocketDirSubcommandPrintsResolvedDir covers the new `agentwatch
// socket-dir` CLI command (#217): it must print the resolved SocketDir() path
// to stdout and exit 0, so shell consumers (dev-sandbox's agent-sand) don't
// reimplement the XDG-vs-fallback logic themselves.
func TestSocketDirSubcommandPrintsResolvedDir(t *testing.T) {
	xdgDir := t.TempDir()
	cmd := exec.Command(binaryPath, "socket-dir")
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+xdgDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("socket-dir: %v\n%s", err, output)
	}

	want := filepath.Join(xdgDir, "agentwatch")
	got := strings.TrimSpace(string(output))
	if got != want {
		t.Errorf("socket-dir output = %q, want %q", got, want)
	}
}

// TestSocketDirSubcommandRoutes locks in that "socket-dir" is recognized as a
// real subcommand (never falls through to the "unknown subcommand" error
// path), independent of what directory it resolves to.
func TestSocketDirSubcommandRoutes(t *testing.T) {
	cmd := exec.Command(binaryPath, "socket-dir")
	output, _ := cmd.CombinedOutput()
	if strings.Contains(string(output), "unknown subcommand") {
		t.Errorf("socket-dir must route to its own handler, got:\n%s", output)
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
