package main_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/internal/dispatch"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

var binaryPath string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "cenci-test-*")
	if err != nil {
		panic("failed to create temp dir: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	binaryPath = filepath.Join(tmp, "cenci")

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

// TestNotifyParsesAgentIDFromStdin covers ticket #277: the real runNotify
// stdin-parsing path must decode the "agent_id" JSON key into
// ipc.HookEvent.AgentID, since that's the field the daemon's subagent guard
// (internal/daemon/event.go) relies on.
func TestNotifyParsesAgentIDFromStdin(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "events.sock")
	receiver, err := ipc.NewEventReceiver(socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = receiver.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go receiver.Accept(ctx)

	input := `{"hook_event_name":"Stop","session_id":"s1","agent_id":"sub1"}`
	cmd := exec.Command(binaryPath, "notify", "-agent", "claude", "-event-socket", socket)
	cmd.Stdin = strings.NewReader(input)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("notify: %v: %s", err, output)
	}

	select {
	case event := <-receiver.Events():
		if event.AgentID != "sub1" {
			t.Fatalf("agent_id = %q, want %q", event.AgentID, "sub1")
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

	const localBinary = `"${PLUGIN_ROOT}/bin/cenci" notify`
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
	stub := filepath.Join(pluginRoot, "bin", "cenci")
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

	if !strings.Contains(string(output), `cenci: unknown subcommand "frobnicate"`) {
		t.Errorf("expected stderr to contain %q, got:\n%s",
			`cenci: unknown subcommand "frobnicate"`, output)
	}
}

// TestFlagRouting_DashV_NoLongerRoutesToDaemon locks in the BREAKING change:
// a bare top-level flag like -v used to fall through to running the daemon
// in the foreground. It no longer does — it's an unrecognized top-level
// argument like any other, so it exits 2 immediately (no daemon spawn, no
// need for a context timeout).
func TestFlagRouting_DashV_NoLongerRoutesToDaemon(t *testing.T) {
	cmd := exec.Command(binaryPath, "-v")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if strings.Contains(string(output), "cenci starting") {
		t.Errorf("-v must not route to the daemon anymore, got:\n%s", output)
	}
}

// TestBareInvocation_PrintsUsageAndExits2 locks in the BREAKING change: bare
// `cenci` used to run the daemon in the foreground. It now prints usage
// and exits 2 instead — the daemon only starts via the explicit `daemon`
// subcommand group.
func TestBareInvocation_PrintsUsageAndExits2(t *testing.T) {
	cmd := exec.Command(binaryPath)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "Usage:") {
		t.Errorf("expected usage text in output, got:\n%s", output)
	}
	if strings.Contains(string(output), "cenci starting") {
		t.Errorf("bare invocation must not route to the daemon anymore, got:\n%s", output)
	}
}

// TestHelpFlag_PrintsUsageAndExits0 covers the `help`/`-h`/`--help`
// convenience added alongside the bare-invocation BREAKING change above.
func TestHelpFlag_PrintsUsageAndExits0(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		cmd := exec.Command(binaryPath, arg)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", arg, err, output)
		}
		if !strings.Contains(string(output), "Usage:") {
			t.Errorf("%s: expected usage text, got:\n%s", arg, output)
		}
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
		"--slug", "cenci-run", "--session", "demo", "--config", noCfg, "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run failed: %v\n%s", err, output)
	}
	s := string(output)
	if !strings.Contains(s, "40-implement") {
		t.Errorf("expected window name 40-implement, got:\n%s", s)
	}
	// No flag and no config default → the sandbox launcher (#98).
	if !strings.Contains(s, "cenci open") || !strings.Contains(s, "/cenci:implement 40") {
		t.Errorf("expected a cenci open command with the cenci skill, got:\n%s", s)
	}
}

func TestRunDryRunNoSandboxUsesHostCommand(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "implement", "40",
		"--slug", "cenci-run", "--session", "demo", "--config", noCfg, "--no-sandbox", "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("no-sandbox dry-run failed: %v\n%s", err, output)
	}
	s := string(output)
	if strings.Contains(s, "cenci open") {
		t.Errorf("--no-sandbox must not use the sandbox launcher, got:\n%s", s)
	}
	if !strings.Contains(s, "claude") || !strings.Contains(s, "/cenci:implement 40") {
		t.Errorf("expected host claude command with the cenci skill, got:\n%s", s)
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
	if !strings.Contains(s, "/cenci:implement 99999999 focus on the API layer") {
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
	if !strings.Contains(string(output), "cenci open") {
		t.Errorf("expected a cenci open command, got:\n%s", output)
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

// TestWidgetJSONAndWaybarSubcommandsBothRoute covers the widget-json split
// (#daemon-lifecycle PR): "widget-json" (the renamed hidden plumbing
// subcommand) and its pre-existing hidden alias "waybar" must both route to
// the same Waybar-JSON status frontend that plain `status` used to. With no
// daemon on the socket they exit 1 with no output — never the "unknown
// subcommand" error, and never route to the new human-readable `status`.
func TestWidgetJSONAndWaybarSubcommandsBothRoute(t *testing.T) {
	for _, sub := range []string{"widget-json", "waybar"} {
		cmd := exec.Command(binaryPath, sub, "-socket", filepath.Join(t.TempDir(), "nope.sock"))
		output, err := cmd.CombinedOutput()

		if strings.Contains(string(output), "unknown subcommand") {
			t.Errorf("%s: expected routing to the widget-json frontend, got:\n%s", sub, output)
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

// TestWidgetJSONAndWaybarOutputByteIdentical drives a real broadcast snapshot
// through both "widget-json" and its "waybar" alias and asserts byte-for-byte
// identical output — the split (#daemon-lifecycle PR) must not change a
// single byte of what widget frontends parse as JSON.
func TestWidgetJSONAndWaybarOutputByteIdentical(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "aw.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	snap := ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Session: "sess-a", WindowIndex: "0", WindowName: "77-implement", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	}

	outputs := make(map[string]string, 2)
	for _, sub := range []string{"widget-json", "waybar"} {
		srv.Broadcast(snap)
		time.Sleep(20 * time.Millisecond)
		cmd := exec.Command(binaryPath, sub, "-socket", socket)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", sub, err, out)
		}
		outputs[sub] = string(out)
	}

	if outputs["widget-json"] != outputs["waybar"] {
		t.Errorf("widget-json and waybar output differ:\nwidget-json: %q\nwaybar:      %q", outputs["widget-json"], outputs["waybar"])
	}
	if !strings.Contains(outputs["widget-json"], `"text"`) || !strings.Contains(outputs["widget-json"], "77-implement") {
		t.Errorf("expected Waybar JSON with the session tooltip, got: %q", outputs["widget-json"])
	}
}

// TestStatusSubcommand_HumanReadable_DegradesGracefullyWithNoDaemon covers
// the new human-readable `cenci status` (distinct from `widget-json`):
// with nothing listening on either socket it must still print a report and
// exit 0 — never the "unknown subcommand" error, and never a non-zero exit
// (unlike `daemon status`, which exits 1 when not running).
func TestStatusSubcommand_HumanReadable_DegradesGracefullyWithNoDaemon(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(binaryPath, "status",
		"-socket", filepath.Join(dir, "nope.sock"),
		"-event-socket", filepath.Join(dir, "nope-events.sock"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status: expected exit 0 even with no daemon, got %v\n%s", err, output)
	}
	if strings.Contains(string(output), "unknown subcommand") {
		t.Errorf("expected routing to the human status overview, got:\n%s", output)
	}
	if !strings.Contains(string(output), "daemon: not running") {
		t.Errorf("expected 'daemon: not running', got:\n%s", output)
	}
	if !strings.Contains(string(output), "sessions: unavailable") {
		t.Errorf("expected sessions to be reported unavailable, got:\n%s", output)
	}
}

// TestStatusSubcommand_HumanReadable_ListsLiveSessions drives a real
// broadcast snapshot (same pattern as TestClose_DryRun_PrintsCloseAndSkipDecisions)
// through the human `status` overview and asserts the session line renders.
func TestStatusSubcommand_HumanReadable_ListsLiveSessions(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "aw.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	srv.Broadcast(ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Session: "sess-a", WindowIndex: "0", WindowName: "77-implement", TaskName: "implement thing", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	})
	time.Sleep(20 * time.Millisecond)

	cmd := exec.Command(binaryPath, "status",
		"-socket", socket,
		"-event-socket", filepath.Join(t.TempDir(), "nope-events.sock"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "sess-a:0 - implement thing (running)") {
		t.Errorf("expected session line, got:\n%s", output)
	}
	if !strings.Contains(string(output), "daemon: not running") {
		t.Errorf("expected daemon not running (only the broadcast socket was live), got:\n%s", output)
	}
}

func TestVersionSubcommandPrintsVersion(t *testing.T) {
	cmd := exec.Command(binaryPath, "version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("version: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "cenci dev") {
		t.Errorf("version output = %q, want to contain %q", output, "cenci dev")
	}
}

func TestVersionFlagsRouteToVersion(t *testing.T) {
	for _, flagArg := range []string{"--version", "-version"} {
		cmd := exec.Command(binaryPath, flagArg)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", flagArg, err, output)
		}
		if !strings.Contains(string(output), "cenci dev") {
			t.Errorf("%s output = %q, want to contain %q", flagArg, output, "cenci dev")
		}
		if strings.Contains(string(output), "unknown subcommand") {
			t.Errorf("%s output = %q, want NOT to contain %q", flagArg, output, "unknown subcommand")
		}
	}

	// Bare -v is now just an unrecognized top-level argument (see
	// TestFlagRouting_DashV_NoLongerRoutesToDaemon) — it must not print the
	// version banner either.
	cmd := exec.Command(binaryPath, "-v")
	output, _ := cmd.CombinedOutput()
	if strings.Contains(string(output), "cenci dev") {
		t.Errorf("-v output = %q, want NOT to contain %q", output, "cenci dev")
	}
}

func TestVersionStampedViaLdflags(t *testing.T) {
	stampedPath := filepath.Join(t.TempDir(), "cenci-stamped")
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
	if !strings.Contains(string(output), `cenci dispatch: unknown subcommand "statas"`) {
		t.Errorf("stderr = %q, want to contain %q", output, `cenci dispatch: unknown subcommand "statas"`)
	}
	if strings.Contains(string(output), "skip:") || strings.Contains(string(output), "dispatch (") {
		t.Errorf("output must not contain dispatch decision-table lines (a real dispatch pass must never run), got:\n%s", output)
	}
}

// TestSocketDirSubcommandPrintsResolvedDir covers the new `cenci
// socket-dir` CLI command (#217): it must print the resolved SocketDir() path
// to stdout and exit 0, so shell consumers (widget scripts, tests) don't
// reimplement the XDG-vs-fallback logic themselves.
func TestSocketDirSubcommandPrintsResolvedDir(t *testing.T) {
	xdgDir := t.TempDir()
	cmd := exec.Command(binaryPath, "socket-dir")
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+xdgDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("socket-dir: %v\n%s", err, output)
	}

	want := filepath.Join(xdgDir, "cenci")
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

// -- close subcommand (ticket #314) ------------------------------------------

// TestClose_UnreachableDaemon_ErrorsExit1_NoKill locks in the hard fail-safe:
// when the daemon socket cannot be reached, `close` exits 1 and never reports
// a closed window (i.e. it never reached a tmux call).
func TestClose_UnreachableDaemon_ErrorsExit1_NoKill(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "nope.sock")

	cmd := exec.Command(binaryPath, "close", "42", "--socket", socket)
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1\n%s", exitErr.ExitCode(), output)
	}
	if strings.Contains(string(output), "closed ") {
		t.Errorf("output must not report any closed window when the daemon is unreachable, got:\n%s", output)
	}
}

func TestClose_MissingArg_Exits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "close")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
}

func TestClose_TrailingUnexpectedArg_Exits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "close", "42", "unexpected")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
}

// TestClose_DryRun_PrintsCloseAndSkipDecisions drives `close --dry-run`
// against a real ipc.Server broadcasting a crafted snapshot with one
// non-busy and one busy window sharing the same ticket-number prefix, and
// asserts the decision lines without ever requiring a real tmux binary
// (--dry-run never calls the killer).
func TestClose_DryRun_PrintsCloseAndSkipDecisions(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "aw.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	srv.Broadcast(ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Session: "sess-a", WindowIndex: "0", WindowName: "77-implement", Status: "done"},
			{Session: "sess-a", WindowIndex: "1", WindowName: "77-review", Status: "running"},
		},
	})
	time.Sleep(20 * time.Millisecond)

	cmd := exec.Command(binaryPath, "close", "77", "--dry-run", "--socket", socket)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("close --dry-run: %v\n%s", err, output)
	}

	if !strings.Contains(string(output), "would close 77-implement") {
		t.Errorf("output missing would-close line for done window, got:\n%s", output)
	}
	if !strings.Contains(string(output), "skip 77-review") {
		t.Errorf("output missing skip line for running window, got:\n%s", output)
	}
	if strings.Contains(string(output), "closed 77-implement") {
		t.Errorf("--dry-run must never print an actual 'closed' line, got:\n%s", output)
	}
}

// -- daemon lifecycle subcommands (daemon start|stop|restart|status) --------
//
// Every test in this section sets its own isolated XDG_RUNTIME_DIR so the
// real "cenci daemon start" subprocess it spawns never touches a real
// daemon's sockets/PID file, and so parallel test runs never collide.

// bgDaemon wraps a backgrounded `daemon start` subprocess together with a
// channel that closes once it has actually been reaped.
type bgDaemon struct {
	cmd  *exec.Cmd
	done chan struct{} // closed once cmd.Wait() has returned
}

// startDaemonBackground spawns `cenci daemon start` in the background
// (not context-bound: these tests need it to keep running until explicitly
// stopped, unlike the bounded-context daemon smoke tests elsewhere in this
// file) and waits for its PID file to appear before returning. The caller's
// test must have already set XDG_RUNTIME_DIR via t.Setenv.
//
// The reaping goroutine below is started immediately (not lazily, on-demand
// later) so the moment the daemon process actually exits — however it dies,
// including from an external `cenci daemon stop`/SIGKILL — it gets
// reaped right away. Otherwise this test process (the daemon subprocess's
// OS-level parent) would leave it as a zombie until something got around to
// calling Wait(), and os.Process.Signal(0) reports a zombie as still
// "alive", making an external stop look like it hung.
func startDaemonBackground(t *testing.T) *bgDaemon {
	t.Helper()
	cmd := exec.Command(binaryPath, "daemon", "start")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start 'daemon start': %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	})
	waitForFile(t, ipc.DefaultPIDPath(), 3*time.Second)
	return &bgDaemon{cmd: cmd, done: done}
}

// waitForFile polls for path to exist, bounded by timeout.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to appear", path)
}

// TestDaemonStart_WritesPIDFile_RemovedOnCleanShutdown covers the PID-file
// half of `daemon start`: the file appears (containing the daemon's real
// PID) once the daemon is up, and is removed again on a clean SIGTERM
// shutdown.
func TestDaemonStart_WritesPIDFile_RemovedOnCleanShutdown(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	bg := startDaemonBackground(t)
	pidPath := ipc.DefaultPIDPath()

	pid, err := daemon.ReadPIDFile(pidPath)
	if err != nil {
		t.Fatalf("ReadPIDFile: %v", err)
	}
	if pid != bg.cmd.Process.Pid {
		t.Errorf("pid file contains %d, want %d", pid, bg.cmd.Process.Pid)
	}

	if err := bg.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	select {
	case <-bg.done:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not exit after SIGTERM")
	}

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected pid file to be removed after clean shutdown")
	}
}

// TestDaemonStatus_NotRunning covers `daemon status` against an empty
// runtime dir: it must report not-running and exit non-zero.
func TestDaemonStatus_NotRunning(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	cmd := exec.Command(binaryPath, "daemon", "status")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "not running") {
		t.Errorf("expected 'not running', got:\n%s", output)
	}
}

// TestDaemonStatus_Running covers `daemon status` against a real,
// live-spawned daemon: it must report running (with the PID) and exit 0.
func TestDaemonStatus_Running(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	startDaemonBackground(t)

	cmd := exec.Command(binaryPath, "daemon", "status")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon status: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "running") {
		t.Errorf("expected 'running', got:\n%s", output)
	}
	if strings.Contains(string(output), "not running") {
		t.Errorf("expected running, not 'not running', got:\n%s", output)
	}
}

// TestDaemonStop_RemovesPIDFileAndKillsProcess is the ticket's required
// black-box regression: `daemon stop` against a real, live-spawned daemon
// must remove the PID file and the process must actually be gone.
func TestDaemonStop_RemovesPIDFileAndKillsProcess(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	bg := startDaemonBackground(t)
	pidPath := ipc.DefaultPIDPath()
	pid := bg.cmd.Process.Pid

	cmd := exec.Command(binaryPath, "daemon", "stop")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon stop: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "stopped daemon") {
		t.Errorf("expected 'stopped daemon', got:\n%s", output)
	}

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected pid file to be removed after 'daemon stop'")
	}
	if daemon.ProcessAlive(pid) {
		t.Errorf("expected daemon process %d to be gone after 'daemon stop'", pid)
	}

	select {
	case <-bg.done:
	case <-time.After(2 * time.Second):
		t.Error("expected the daemon subprocess to have been reaped promptly")
	}
}

// TestDaemonStop_NothingRunningIsNoop covers the idempotent no-op path: with
// nothing running, `daemon stop` exits 0 and reports "daemon not running".
func TestDaemonStop_NothingRunningIsNoop(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	cmd := exec.Command(binaryPath, "daemon", "stop")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon stop: expected exit 0, got %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "not running") {
		t.Errorf("expected 'not running', got:\n%s", output)
	}
}

// TestDaemonRestart_StopsOldAndStartsNew covers `daemon restart`: it must
// tear down an existing daemon and bring up a fresh one reachable at the
// same socket, reporting the old PID and a "restarted" confirmation.
func TestDaemonRestart_StopsOldAndStartsNew(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	oldBg := startDaemonBackground(t)
	oldPID := oldBg.cmd.Process.Pid

	cmd := exec.Command(binaryPath, "daemon", "restart")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon restart: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "daemon restarted") {
		t.Errorf("expected 'daemon restarted', got:\n%s", output)
	}

	if daemon.ProcessAlive(oldPID) {
		t.Errorf("expected old daemon process %d to be gone after restart", oldPID)
	}
	if !daemon.Alive(ipc.DefaultEventSocketPath()) {
		t.Error("expected a new daemon to be reachable after restart")
	}

	// The freshly spawned daemon is detached (Setsid) and outlives this test
	// process; stop it directly so it doesn't leak.
	if _, err := daemon.Stop(ipc.DefaultEventSocketPath(), ipc.DefaultPIDPath()); err != nil {
		t.Logf("cleanup: daemon.Stop: %v", err)
	}
}

// TestDaemonGroup_UnknownSubcommand_Exits2 mirrors the top-level unknown
// subcommand guard for the `daemon` subcommand group.
func TestDaemonGroup_UnknownSubcommand_Exits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "daemon", "frobnicate")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), `unknown subcommand "frobnicate"`) {
		t.Errorf("expected unknown-subcommand message, got:\n%s", output)
	}
}
