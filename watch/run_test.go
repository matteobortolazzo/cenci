package main_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
)

// writeRunConfig writes a `cenci run --config` file and returns its path.
func writeRunConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runDryRun runs `cenci run <args> --dry-run` and returns stdout, stderr and
// the process exit code (0 on success). --dry-run lands before a `--`
// terminator in args, since everything after it is a positional.
func runDryRun(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	at := slices.Index(args, "--")
	if at < 0 {
		at = len(args)
	}
	cmd := exec.Command(binaryPath, append([]string{"run"}, slices.Insert(slices.Clone(args), at, "--dry-run")...)...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		exitCode = exitErr.ExitCode()
	default:
		t.Fatalf("running %s: %v", binaryPath, err)
	}
	return outBuf.String(), errBuf.String(), exitCode
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

// #1497 AC1: `--unattended` pins CENCI_ATTENDED=0 as a shell assignment scoped
// to the agent command, right before the launcher/agent argv, and only once.
func TestRunDryRunUnattendedPinsAttendedZero(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	stdout, stderr, code := runDryRun(t, "implement", "40", "--unattended", "--session", "demo", "--config", noCfg)
	if code != 0 {
		t.Fatalf("--unattended dry-run exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	const pin = "CENCI_ATTENDED=0 "
	line := exectest.DryRunCommand(t, stdout)
	if !strings.HasPrefix(line, pin+"cenci open ") {
		t.Errorf("command = %q, want the %q assignment right before `cenci open `", line, pin)
	}
	if n := strings.Count(line, pin); n != 1 {
		t.Errorf("command carries %d %q assignments, want exactly 1: %q", n, pin, line)
	}
}

// #1497 AC2 + edge: without `--unattended` (or with `--unattended=false`, the
// stdlib bool form, placed after the positionals) the interactive path stays
// unpinned: no CENCI_ATTENDED anywhere in the output.
func TestRunDryRunWithoutUnattendedLeavesAttendedUnset(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{"flag absent", nil},
		{"--unattended=false", []string{"--unattended=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noCfg := filepath.Join(t.TempDir(), "none.json")
			args := append([]string{"implement", "40"}, tc.flags...)
			args = append(args, "--session", "demo", "--config", noCfg)
			stdout, stderr, code := runDryRun(t, args...)
			if code != 0 {
				t.Fatalf("dry-run exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			if strings.Contains(stdout, "CENCI_ATTENDED") {
				t.Errorf("an unpinned dry-run must not mention CENCI_ATTENDED, got:\n%s", stdout)
			}
		})
	}
}

// #1497 edge: the assignment sits on the agent argv, not on the `cd` the
// `--dir` prefix adds. shellQuote leaves a plain path bare and quotes one with
// a space.
func TestRunDryRunUnattendedPinFollowsDir(t *testing.T) {
	for _, tc := range []struct {
		name, dir, wantPrefix string
	}{
		{"plain path stays unquoted", "/tmp/x", "cd /tmp/x && CENCI_ATTENDED=0 cenci open "},
		{"spaced path is quoted", "/tmp/my x", "cd '/tmp/my x' && CENCI_ATTENDED=0 "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noCfg := filepath.Join(t.TempDir(), "none.json")
			stdout, stderr, code := runDryRun(t, "implement", "40", "--unattended", "--dir", tc.dir,
				"--session", "demo", "--config", noCfg)
			if code != 0 {
				t.Fatalf("dry-run exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			if line := exectest.DryRunCommand(t, stdout); !strings.HasPrefix(line, tc.wantPrefix) {
				t.Errorf("command = %q, want prefix %q", line, tc.wantPrefix)
			}
		})
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

// TestRunImplementSandboxDryRunStillResolvesSandbox pins the ordinary
// sandbox-resolution path: `--sandbox` on a normal workflow resolves `cenci
// open`.
func TestRunImplementSandboxDryRunStillResolvesSandbox(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "implement", "42",
		"--session", "demo", "--config", noCfg, "--sandbox", "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("implement sandbox dry-run failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "cenci open") {
		t.Errorf("implement --sandbox must still resolve cenci open, got:\n%s", output)
	}
}

// TestRunDirDryRunShowsResolvedDirectory is ticket #975's AC 1: `cenci run
// --dir <path>` must be accepted, and --dry-run must show the resolved
// directory via the existing `command:` line's `cd '<dir>' && ` prefix (no
// new `dir:` output line — auto-adopted answer #1: the prefix already
// carries the resolved directory verbatim and shell-quoted). The path
// contains a space so shellQuote's own contract (single-quote only when a
// word isn't already shell-safe, matching internal/run's shellQuote/isShellSafe
// and TestRunPrependsDir's identical path choice) is what triggers quoting
// here, rather than hard-coding quotes production wouldn't actually emit for
// an all-safe-character path.
func TestRunDirDryRunShowsResolvedDirectory(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "implement", "40",
		"--slug", "cenci-run", "--session", "demo", "--config", noCfg, "--dir", "/repo/my project", "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--dir dry-run failed: %v\n%s", err, output)
	}
	s := string(output)
	if !strings.Contains(s, "cd '/repo/my project' && ") {
		t.Errorf("expected the command: line's cd prefix to carry the resolved directory, got:\n%s", s)
	}
}

func TestRunCodexUsesBuiltinWorkflowTemplate(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cmd := exec.Command(binaryPath, "run", "implement", "40",
		"--agent", "codex", "--session", "demo", "--config", noCfg, "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("codex dry-run failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "cenci open") || !strings.Contains(string(output), "$cenci:implement 40") {
		t.Errorf("expected built-in Codex skill invocation, got:\n%s", output)
	}
}

// #1214: dry run labels where the resolved template came from.
func TestRunDryRunLabelsTemplateProvenance(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	override := writeRunConfig(t, `{"agents":{"claude":{"workflows":{"implement":{"args":["--","/overridden {ticket}"]},"file-added":{"args":["--","/added {ticket}"]}}},"acme":{"command":"acme","workflows":{"refine":{"args":["--task","{ticket}"]}}}}}`)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"built-in", []string{"implement", "40", "--session", "demo", "--config", noCfg}, "template: built-in"},
		{"args-only override stays built-in", []string{"implement", "40", "--session", "demo", "--config", override}, "template: built-in"},
		{"file-added workflow", []string{"file-added", "40", "--session", "demo", "--config", override}, "template: user-defined (" + override + ")"},
		{"custom agent", []string{"refine", "40", "--agent", "acme", "--no-sandbox", "--session", "demo", "--config", override}, "template: user-defined (" + override + ")"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runDryRun(t, tc.args...)
			if code != 0 {
				t.Fatalf("dry-run exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			found := false
			for _, line := range strings.Split(stdout, "\n") {
				if strings.TrimSpace(line) == tc.want {
					found = true
				}
			}
			if !found {
				t.Errorf("dry-run output missing line %q:\n%s", tc.want, stdout)
			}
		})
	}
}

// #1214 AC 3: a sandbox request (default, flag, or config) the agent cannot
// satisfy fails the dry run too, with exit 1 and no resolved command.
func TestRunDryRunUnsatisfiedSandboxFailsClosed(t *testing.T) {
	acme := `"agents":{"acme":{"command":"acme","workflows":{"refine":{"args":["--task","{ticket}"]}}}}`
	cases := []struct {
		name   string
		config string
		flags  []string
	}{
		{"default", `{` + acme + `}`, nil},
		{"--sandbox flag", `{` + acme + `}`, []string{"--sandbox"}},
		{"config sandbox true", `{"sandbox":true,` + acme + `}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := writeRunConfig(t, tc.config)
			args := append([]string{"refine", "40", "--agent", "acme", "--session", "demo", "--config", cfg}, tc.flags...)
			stdout, stderr, code := runDryRun(t, args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			if strings.Contains(stdout, "command:") {
				t.Errorf("a failed dry run must not print a resolved command:\n%s", stdout)
			}
			for _, want := range []string{"acme", "sandboxCommand", "--no-sandbox", `"sandbox": false`} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr missing %q:\n%s", want, stderr)
				}
			}
		})
	}
}

// #1214 AC 2: a custom agent without the requested workflow fails the dry run
// naming agent, workflow and remedy.
func TestRunDryRunMissingTemplateFails(t *testing.T) {
	cfg := writeRunConfig(t, `{"agents":{"acme":{"command":"acme","workflows":{"refine":{"args":["--task","{ticket}"]}}}}}`)
	stdout, stderr, code := runDryRun(t, "implement", "40", "--agent", "acme", "--no-sandbox", "--session", "demo", "--config", cfg)
	if code != 1 {
		t.Errorf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.Contains(stdout, "command:") {
		t.Errorf("a failed dry run must not print a resolved command:\n%s", stdout)
	}
	for _, want := range []string{"acme", "implement", "agents.acme.workflows.implement"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}
}

// #1214 AC 5/7: OpenCode stays disabled at the binary boundary -- by flag, by
// config defaultAgent, and even when a config override supplies workflows and a
// sandboxCommand -- and never suggests a config override can bypass it.
func TestRunDryRunDisabledOpenCodeFails(t *testing.T) {
	override := `{"agents":{"opencode":{"command":"opencode","sandboxCommand":"cenci open","workflows":{"implement":{"args":["--auto","--prompt","/cenci:implement {ticket}"]}}}}}`
	noCfg := filepath.Join(t.TempDir(), "none.json")
	cases := []struct {
		name string
		args []string
	}{
		{"--agent opencode", []string{"--agent", "opencode", "--config", noCfg}},
		{"config defaultAgent", []string{"--config", writeRunConfig(t, `{"defaultAgent":"opencode"}`)}},
		{"config override", []string{"--agent", "opencode", "--config", writeRunConfig(t, override)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"implement", "40", "--session", "demo"}, tc.args...)
			stdout, stderr, code := runDryRun(t, args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			if strings.Contains(stdout, "command:") {
				t.Errorf("a failed dry run must not print a resolved command:\n%s", stdout)
			}
			if !strings.Contains(stderr, "temporarily disabled") {
				t.Errorf("stderr missing \"temporarily disabled\":\n%s", stderr)
			}
			for _, banned := range []string{"config.json", "add one"} {
				if strings.Contains(stderr, banned) {
					t.Errorf("stderr must not suggest a config override (%q):\n%s", banned, stderr)
				}
			}
		})
	}
}

// runSpawn runs a real (non-dry-run) `cenci run ci-repair` against the fake
// tmux of writeCIRepairFakeTmux, with ctlFile created to steer it, and returns
// stdout followed by stderr, the exit code and the recorded tmux calls.
func runSpawn(t *testing.T, ctlFile string) (string, int, [][]string) {
	t.Helper()
	return runSpawnWith(t, ctlFile, nil, "--session", "work")
}

// runSpawnWith is runSpawn with the launch flags (after --no-sandbox, before
// --dir) and extra environment entries chosen by the caller; an empty ctlFile
// steers the fake tmux with nothing. The recorded calls are raw: each leads with
// the `-S <socket>` pair `cenci run` passes (#1431).
func runSpawnWith(t *testing.T, ctlFile string, extraEnv []string, flags ...string) (string, int, [][]string) {
	t.Helper()
	root := t.TempDir()
	binDir, ctlDir, workDir := filepath.Join(root, "bin"), filepath.Join(root, "ctl"), filepath.Join(root, "work")
	for _, d := range []string{binDir, ctlDir, workDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tmuxLog := filepath.Join(root, "tmux.log")
	writeCIRepairFakeTmux(t, binDir, ctlDir, tmuxLog)
	if ctlFile != "" {
		if err := os.WriteFile(filepath.Join(ctlDir, ctlFile), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// openTestEnv supplies the isolated HOME/XDG_CONFIG_HOME and a live event
	// socket, so this launch never spawns a daemon. It also pins
	// TMUX=/tmp/tmux-1000/cenci,12345,0 (the invoking pane's server).
	env, _, _ := openTestEnv(t, binDir, writeAssetFixture(t))
	env = append(env, extraEnv...)
	args := append([]string{"run", "ci-repair", "42 PR #42 (b) has failing CI checks: lint.", "--agent", "claude", "--no-sandbox"}, flags...)
	stdout, stderr, code := runBabysitCLI(t, env, append(args, "--dir", workDir)...)
	return stdout + stderr, code, adapterLaunchCalls(t, tmuxLog)
}

// #1455: a failure after the window was spawned (automatic-rename) exits 3, a
// status distinct from the pre-spawn refusals' 1 and the usage errors' 2.
func TestRunPostSpawnFailureExitsThree(t *testing.T) {
	out, code, calls := runSpawn(t, "fail-automatic-rename")
	if code != 3 {
		t.Fatalf("cenci run exit = %d, want 3; output:\n%s", code, out)
	}
	if !strings.Contains(out, "automatic-rename") {
		t.Errorf("output %q does not name the failed post-spawn step", out)
	}
	if !slices.ContainsFunc(calls, func(c []string) bool { c = withoutTmuxSocket(c); return len(c) > 0 && c[0] == "new-window" }) {
		t.Errorf("tmux calls %q, want the window spawned before the failure", calls)
	}
}

// #1455/#1250: a @cenci-repo-dir write failure only warns; `cenci run` exits 0.
func TestRunRepoDirFailureWarnsAndExitsZero(t *testing.T) {
	out, code, _ := runSpawn(t, "fail-@cenci-repo-dir")
	if code != 0 {
		t.Fatalf("cenci run exit = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "warning: could not record the launch directory") {
		t.Errorf("output %q, want the launch-directory warning", out)
	}
}

// socketOf returns the socket a raw fake-tmux call was made on ("" when it
// carried no leading `-S <socket>`).
func socketOf(call []string) string {
	if len(call) >= 2 && call[0] == "-S" {
		return call[1]
	}
	return ""
}

// requireAllCallsOn asserts there is at least one recorded call and that every
// one carried -S want, with a new-window among them.
func requireAllCallsOn(t *testing.T, calls [][]string, want string) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("no tmux calls were recorded")
	}
	for _, c := range calls {
		if got := socketOf(c); got != want {
			t.Errorf("tmux call %q on socket %q, want %q", c, got, want)
		}
	}
	if !slices.ContainsFunc(calls, func(c []string) bool { c = withoutTmuxSocket(c); return len(c) > 0 && c[0] == "new-window" }) {
		t.Errorf("tmux calls %q, want a new-window", calls)
	}
}

// requireOneLineUsageError asserts a refused `cenci run`: exit 2, a single
// non-empty stderr line naming wantHint, and no tmux call at all.
func requireOneLineUsageError(t *testing.T, out string, code int, calls [][]string, wantHint string) {
	t.Helper()
	if code != 2 {
		t.Errorf("cenci run exit = %d, want 2; output:\n%s", code, out)
	}
	line := strings.TrimSuffix(out, "\n")
	if line == "" || strings.Contains(line, "\n") {
		t.Errorf("output = %q, want exactly one line", out)
	}
	if !strings.Contains(line, wantHint) {
		t.Errorf("output = %q, want a hint naming %q", line, wantHint)
	}
	if len(calls) != 0 {
		t.Errorf("tmux calls = %q, want none: a refused run creates no window", calls)
	}
}

// P11: without --tmux-socket, `cenci run` targets the first field of $TMUX (the
// invoking pane's server) on every tmux call, through the real binary.
func TestRunDefaultsToTheInvokingPanesSocket(t *testing.T) {
	out, code, calls := runSpawnWith(t, "", nil, "--session", "work")
	if code != 0 {
		t.Fatalf("cenci run exit = %d, want 0; output:\n%s", code, out)
	}
	requireAllCallsOn(t, calls, "/tmp/tmux-1000/cenci")
}

// `cenci run --tmux-socket <path>` creates the window on <path>, whatever $TMUX
// says.
func TestRunTmuxSocketFlagCreatesTheWindowOnThatSocket(t *testing.T) {
	const sock = "/tmp/tmux-1000/other"
	out, code, calls := runSpawnWith(t, "", nil, "--session", "work", "--tmux-socket", sock)
	if code != 0 {
		t.Fatalf("cenci run exit = %d, want 0; output:\n%s", code, out)
	}
	requireAllCallsOn(t, calls, sock)
}

// A --tmux-socket value that fails the shape check (relative, TAB or newline)
// is a usage error: exit 2, one stderr line, no window.
func TestRunTmuxSocketFlagMalformedExitsTwoWithNoWindow(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"relative path", "relative/tmux.sock"},
		{"contains a tab", "/tmp/tmux-1000/a\tb"},
		{"contains a newline", "/tmp/tmux-1000/a\nb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, code, calls := runSpawnWith(t, "", nil, "--session", "work", "--tmux-socket", tc.value)
			requireOneLineUsageError(t, out, code, calls, "--tmux-socket")
		})
	}
}

// An explicit socket that is not the invoking pane's server, with no --session,
// would resolve $TMUX_PANE (a %N that belongs to the other server) on the wrong
// server: a usage error pointing at --session. The control with the pane's own
// socket proves the refusal is about the mismatch, not the flag.
func TestRunTmuxSocketForeignWithoutSessionExitsTwo(t *testing.T) {
	out, code, calls := runSpawnWith(t, "", nil, "--tmux-socket", "/tmp/tmux-1000/other")
	requireOneLineUsageError(t, out, code, calls, "--session")

	out, code, calls = runSpawnWith(t, "", nil, "--tmux-socket", "/tmp/tmux-1000/cenci")
	if code != 0 {
		t.Fatalf("control: cenci run with the pane's own socket exit = %d, want 0; output:\n%s", code, out)
	}
	requireAllCallsOn(t, calls, "/tmp/tmux-1000/cenci")
}

// dryRunArgv resolves `cenci run <workflow> <flags> [target]` to the argv the
// shell would hand the launcher program: the dry-run `command:` line run
// through /bin/sh against a recording fake, so element boundaries are exact. A
// target with a leading hyphen rides behind the `--` terminator.
func dryRunArgv(t *testing.T, workflow, target string, flags ...string) []string {
	t.Helper()
	args := []string{workflow}
	var tail []string
	if strings.HasPrefix(target, "-") {
		tail = []string{"--", target}
	} else {
		args = append(args, target)
	}
	args = append(append(args, "--session", "demo"), flags...)
	stdout, stderr, code := runDryRun(t, append(args, tail...)...)
	if code != 0 {
		t.Fatalf("dry-run exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	line := exectest.DryRunCommand(t, stdout)
	program, _, _ := strings.Cut(line, " ")
	return exectest.ShellArgv(t, line, program)
}

// #1232 A1/A2: every built-in Codex workflow launched in the sandbox reaches
// `cenci open [--model m] --agent codex -- <prompt>`, the prompt exactly one
// verbatim argv element whatever the target holds.
func TestRunDryRunCodexSandboxRoutesPromptAfterAgentFlag(t *testing.T) {
	noCfg := filepath.Join(t.TempDir(), "none.json")
	targets := []string{"42", "two words\tand a tab", `it's`, `say "hi"`, "$HOME", "42 --force", "-x"}
	codexWorkflows := 0
	for _, c := range run.BuiltinCapabilities() {
		if c.Agent != "codex" {
			continue
		}
		codexWorkflows++
		for _, target := range targets {
			skill := c.Workflow
			if skill == "babysit-review" {
				// #1472: the key names the window; the skill stays address-review.
				skill = "address-review"
			}
			prompt := "$cenci:" + skill + " " + target
			for _, model := range []string{"", "m"} {
				flags := []string{"--agent", "codex", "--sandbox", "--config", noCfg}
				want := []string{"cenci", "open"}
				if model != "" {
					flags = append(flags, "--model", model)
					want = append(want, "--model", model)
				}
				want = append(want, "--agent", "codex", "--")
				if c.Workflow == "ci-repair" || c.Workflow == "babysit-review" {
					// #1455, #1472: the finite Codex templates run `codex exec`.
					want = append(want, "exec")
				}
				want = append(want, prompt)
				t.Run(c.Workflow+"/"+target+"/model="+model, func(t *testing.T) {
					if got := dryRunArgv(t, c.Workflow, target, flags...); !slices.Equal(got, want) {
						t.Errorf("argv = %q, want %q", got, want)
					}
				})
			}
		}
	}
	if codexWorkflows == 0 {
		t.Fatal("no built-in Codex workflows: the table would pass vacuously")
	}
}

// #1232 A5: user-owned Codex templates keep their documented behavior. A
// file-defined workflow without sandboxArgs gets no injected agent routing,
// and a partial override changes only the field it names (per-field merge).
func TestRunDryRunCodexUserTemplatesKeepDocumentedBehavior(t *testing.T) {
	const builtinPrompt = "$cenci:implement 42"
	builtinSandbox := []string{"cenci", "open", "--agent", "codex", "--", builtinPrompt}
	for _, tc := range []struct {
		name, config, workflow, mode string
		want                         []string
	}{
		{"file workflow without sandboxArgs", `{"args":["exec","do {ticket}"]}`, "custom", "--sandbox",
			[]string{"cenci", "open", "exec", "do 42"}},
		{"args-only override, host", `{"args":["exec","x {ticket}"]}`, "implement", "--no-sandbox",
			[]string{"codex", "exec", "x 42"}},
		{"args-only override, sandbox keeps built-in sandboxArgs", `{"args":["exec","x {ticket}"]}`, "implement", "--sandbox",
			builtinSandbox},
		{"sandboxArgs-only override, sandbox", `{"sandboxArgs":["--agent","codex","--","mine {ticket}"]}`, "implement", "--sandbox",
			[]string{"cenci", "open", "--agent", "codex", "--", "mine 42"}},
		{"sandboxArgs-only override, host keeps built-in args", `{"sandboxArgs":["--agent","codex","--","mine {ticket}"]}`, "implement", "--no-sandbox",
			[]string{"codex", builtinPrompt}},
		// #1234 Q10: `{codex_stage}` was removed, not kept as a no-op, so a user
		// template that still carries it passes the token through verbatim (no
		// substitution, no `/plan\n` prefix) while `{ticket}` still expands.
		{"removed {codex_stage} token passes through literally", `{"args":["exec","{codex_stage}do {ticket}"]}`, "implement", "--no-sandbox",
			[]string{"codex", "exec", "{codex_stage}do 42"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := writeRunConfig(t, `{"agents":{"codex":{"workflows":{"`+tc.workflow+`":`+tc.config+`}}}}`)
			if got := dryRunArgv(t, tc.workflow, "42", "--agent", "codex", tc.mode, "--config", cfg); !slices.Equal(got, tc.want) {
				t.Errorf("argv = %q, want %q", got, tc.want)
			}
		})
	}
}
