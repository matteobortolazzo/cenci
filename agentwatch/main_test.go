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

	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/dispatch"
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
	cmd := exec.Command(binaryPath, "run", "implement", "40",
		"--slug", "agentwatch-run", "--session", "demo", "--config", noCfg, "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run failed: %v\n%s", err, output)
	}
	s := string(output)
	if !strings.Contains(s, "40-agentwatch-run") {
		t.Errorf("expected window name 40-agentwatch-run, got:\n%s", s)
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
	// skill argument, not just the first token.
	// Use a non-existent issue number so the gh title lookup yields nothing and
	// the trailing context deterministically drives the slug.
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
	if !strings.Contains(s, "99999999-focus-on-the-api-layer") {
		t.Errorf("expected window name 99999999-focus-on-the-api-layer, got:\n%s", s)
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

	wantBytes, merr := json.Marshal(dispatch.RepoEnrollment{Repo: "owner/name", Dir: dir, Enrolled: true})
	if merr != nil {
		t.Fatalf("json.Marshal want: %v", merr)
	}
	got := strings.TrimSpace(string(output))
	if got != string(wantBytes) {
		t.Errorf("status --json output = %q, want exactly %q", got, wantBytes)
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
