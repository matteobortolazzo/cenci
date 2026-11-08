package main_test

// Subprocess CLI tests for `cenci dispatch` flag handling (#1464): the
// continuous dry-run rejection (--dry-run with --interval, no --once or
// --reconcile), the neighbouring combinations that keep today's behaviour, the
// --help statement of the rule, and the `approve` verb routing. They run the
// built binary (binaryPath, TestMain in main_test.go) against fake gh, git,
// tmux, docker and podman on PATH that append every invocation to one call log,
// so "zero calls of any kind" is an assertion on a file, not on internals. The
// fake git logs and then execs the real git, because the preview pass needs a
// real repository to fetch into.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

const (
	dispatchCLIRepo    = "owner/name"
	dispatchCLISession = "cenci-work"
	dispatchCLITicket  = 7
	// dispatchCLIIssues is one open Refined ticket: the "eligible ticket" the
	// fake gh offers, so a pass that wrongly ran for real would have work to do.
	dispatchCLIIssues = `[{"number":7,"title":"Eligible ticket","body":"","labels":[{"name":"Refined"}],"assignees":[],"milestone":null}]`
	dispatchCLINoWork = `[]`
	// dispatchCLIRunTimeout bounds every run-to-completion invocation.
	dispatchCLIRunTimeout = 20 * time.Second
	dispatchCLIOpenPR = `{"data":{"repository":{"pullRequests":{"totalCount":0,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}`
	// dispatchCLIDryRunRule is the part of the rejection message and of the
	// --help text that names the unsupported combination.
	dispatchCLIDryRunRule = "--dry-run cannot be combined with --interval"
)

// dispatchCLIFixture is one hermetic `cenci dispatch` environment: a real git
// repository with a local origin, a config enrolling it, fake binaries on PATH
// and the call log they write.
type dispatchCLIFixture struct {
	t          *testing.T
	configPath string
	logPath    string
	env        []string
}

// newDispatchCLIFixture builds the environment. issues is what the fake
// `gh issue list` returns.
func newDispatchCLIFixture(t *testing.T, issues string) *dispatchCLIFixture {
	t.Helper()
	root := t.TempDir()

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locating real git: %v", err)
	}

	origin := filepath.Join(root, "origin.git")
	repoDir := filepath.Join(root, "repo")
	for _, dir := range []string{origin, repoDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	runGit(t, origin, "init", "--bare", "--initial-branch=main")
	runGit(t, repoDir, "init", "--initial-branch=main")
	runGit(t, repoDir, "remote", "add", "origin", origin)
	runGit(t, repoDir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false",
		"commit", "--allow-empty", "-m", "init")
	runGit(t, repoDir, "push", "origin", "main")
	runGit(t, repoDir, "fetch", "origin")

	fakeDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(fakeDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", fakeDir, err)
	}
	logPath := filepath.Join(root, "calls.log")
	issuesPath := filepath.Join(root, "issues.json")
	openPRPath := filepath.Join(root, "openpr.json")
	for path, body := range map[string]string{issuesPath: issues, openPRPath: dispatchCLIOpenPR} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	record := func(name string) string {
		return "printf '%s %s\\n' " + name + " \"$*\" >> " + exectest.ShellQuote(logPath) + "\n"
	}
	exectest.WriteExecutable(t, filepath.Join(fakeDir, "gh"), "#!/bin/sh\n"+record("gh")+
		"case \"$1 $2\" in\n"+
		"\"api user\") echo tester ;;\n"+
		"\"api graphql\") cat "+exectest.ShellQuote(openPRPath)+" ;;\n"+
		"\"issue list\") cat "+exectest.ShellQuote(issuesPath)+" ;;\n"+
		"esac\nexit 0\n")
	exectest.WriteExecutable(t, filepath.Join(fakeDir, "git"), "#!/bin/sh\n"+record("git")+
		"exec "+exectest.ShellQuote(realGit)+" \"$@\"\n")
	for _, name := range []string{"tmux", "docker", "podman"} {
		exectest.WriteExecutable(t, filepath.Join(fakeDir, name), "#!/bin/sh\n"+record(name)+"exit 1\n")
	}

	configPath := filepath.Join(root, "config.json")
	config := fmt.Sprintf(`{"dispatch":{"repos":[{"repo":%q,"dir":%q,"session":%q}]}}`, dispatchCLIRepo, repoDir, dispatchCLISession)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// An isolated, hardened socket dir keeps the pass from reaching an ambient
	// daemon (watch/docs/test-isolation.md).
	socketDir := filepath.Join(root, "sock")
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", socketDir, err)
	}
	env := pipelineEnvWith(fakeDir,
		"CENCI_SOCKET_DIR="+socketDir,
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "xdg-config"),
	)
	return &dispatchCLIFixture{t: t, configPath: configPath, logPath: logPath, env: env}
}

// run executes `cenci dispatch <args> --config <config>` to completion.
func (f *dispatchCLIFixture) run(args ...string) (stdout, stderr string, exitCode int) {
	f.t.Helper()
	return f.runWithConfig(f.configPath, args...)
}

func (f *dispatchCLIFixture) runWithConfig(configPath string, args ...string) (stdout, stderr string, exitCode int) {
	f.t.Helper()
	// A form that wrongly enters the continuous loop would block for the whole
	// interval; the deadline turns that into a failed assertion, not a hang.
	ctx, cancel := context.WithTimeout(context.Background(), dispatchCLIRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, append(append([]string{"dispatch"}, args...), "--config", configPath)...)
	cmd.Env = f.env
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	err := cmd.Run()
	if ctx.Err() != nil {
		f.t.Errorf("dispatch %v did not exit within %s (a continuous loop?)\nstdout:\n%s", args, dispatchCLIRunTimeout, outBuf.String())
		return outBuf.String(), errBuf.String(), -1
	}
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			f.t.Fatalf("dispatch %v: %v", args, err)
		}
		exitCode = exitErr.ExitCode()
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// calls returns every recorded fake-binary invocation, one "<binary> <args>"
// line each; nil when nothing ran.
func (f *dispatchCLIFixture) calls() []string {
	f.t.Helper()
	data, err := os.ReadFile(f.logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatalf("reading call log: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// mutations returns the recorded calls that change state: any label or issue
// edit or comment, any PR or run call, any tmux call (a dry run probes no
// session and launches nothing), and any git command that moves a ref, the
// index or the work tree. `git fetch` is the documented read-only preview
// fetch and is not a mutation.
func (f *dispatchCLIFixture) mutations() []string {
	f.t.Helper()
	var out []string
	for _, line := range f.calls() {
		name, args, _ := strings.Cut(line, " ")
		fields := strings.Fields(args)
		bad := false
		switch name {
		case "tmux":
			bad = true
		case "gh":
			bad = len(fields) > 1 && (fields[0] == "label" || fields[0] == "pr" || fields[0] == "run" ||
				(fields[0] == "issue" && fields[1] != "list" && fields[1] != "view"))
		case "git":
			for _, field := range fields {
				switch field {
				case "merge", "push", "checkout", "reset", "commit", "rebase":
					bad = true
				}
			}
		}
		if bad {
			out = append(out, line)
		}
	}
	return out
}

func (f *dispatchCLIFixture) ghIssueListCalls() int {
	n := 0
	for _, line := range f.calls() {
		if strings.HasPrefix(line, "gh issue list") {
			n++
		}
	}
	return n
}

// rejectedDryRunForms are the four spellings of a continuous dry-run the flag
// package accepts; each must be rejected the same way.
var rejectedDryRunForms = []struct {
	name string
	args []string
}{
	{"double-dash dry-run then separate interval", []string{"--dry-run", "--interval", "5m"}},
	{"single-dash dry-run then interval=value", []string{"-dry-run", "-interval=5m"}},
	{"interval=value then dry-run", []string{"--interval=5m", "--dry-run"}},
	{"dry-run=true then separate interval", []string{"--dry-run=true", "--interval", "5m"}},
}

// TestDispatchDryRunInterval_RejectedWithExit2AndNoCalls covers "Continuous
// dry-run rejected" and "Other flag forms rejected the same way": every form
// exits 2 with a stderr message naming the combination, and no fake binary is
// invoked at all -- no run, no label edit, no launch-state clear, no git
// fetch/merge -- even though the config and the fake gh offer an eligible
// ticket. Each form is its own case.
func TestDispatchDryRunInterval_RejectedWithExit2AndNoCalls(t *testing.T) {
	for _, form := range rejectedDryRunForms {
		t.Run(form.name, func(t *testing.T) {
			f := newDispatchCLIFixture(t, dispatchCLIIssues)

			stdout, stderr, code := f.run(form.args...)

			if code != 2 {
				t.Errorf("exit code = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			if !strings.Contains(stderr, dispatchCLIDryRunRule) {
				t.Errorf("stderr = %q, want it to name the combination %q", stderr, dispatchCLIDryRunRule)
			}
			if !strings.Contains(stderr, "--dry-run --once") {
				t.Errorf("stderr = %q, want it to suggest %q", stderr, "--dry-run --once")
			}
			if calls := f.calls(); len(calls) != 0 {
				t.Errorf("rejected form made %d fake-binary call(s), want 0:\n%s", len(calls), strings.Join(calls, "\n"))
			}
			if strings.TrimSpace(stdout) != "" {
				t.Errorf("stdout = %q, want no pass output for a rejected form", stdout)
			}
		})
	}
}

// TestDispatchDryRunInterval_RejectedBeforeLoadingConfig asserts the rejection
// happens before LoadConfig: with a config that would fail loudly if loaded
// (invalid JSON, which exits 1 naming the path), every form still exits 2 with
// the combination message and the config is never mentioned.
func TestDispatchDryRunInterval_RejectedBeforeLoadingConfig(t *testing.T) {
	for _, form := range rejectedDryRunForms {
		t.Run(form.name, func(t *testing.T) {
			f := newDispatchCLIFixture(t, dispatchCLIIssues)
			broken := filepath.Join(t.TempDir(), "broken-config.json")
			if err := os.WriteFile(broken, []byte("{not json"), 0o600); err != nil {
				t.Fatalf("write broken config: %v", err)
			}

			_, stderr, code := f.runWithConfig(broken, form.args...)

			if code != 2 {
				t.Errorf("exit code = %d, want 2 (a loaded broken config would exit 1)\nstderr:\n%s", code, stderr)
			}
			if !strings.Contains(stderr, dispatchCLIDryRunRule) {
				t.Errorf("stderr = %q, want it to name the combination %q", stderr, dispatchCLIDryRunRule)
			}
			if strings.Contains(stderr, broken) {
				t.Errorf("stderr = %q mentions the config path: the config was loaded before the rejection", stderr)
			}
		})
	}
}

// TestDispatchDryRunIntervalZero_IsSinglePreviewPass pins the edge case:
// --interval 0 is "no interval", so --dry-run with it is the ordinary single
// preview pass (exit 0), not a rejection.
func TestDispatchDryRunIntervalZero_IsSinglePreviewPass(t *testing.T) {
	f := newDispatchCLIFixture(t, dispatchCLIIssues)

	stdout, stderr, code := f.run("--dry-run", "--interval", "0")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.Contains(stderr, dispatchCLIDryRunRule) {
		t.Errorf("stderr = %q must not reject --interval 0", stderr)
	}
	if !strings.Contains(stdout, "main sync "+dispatchCLIRepo) {
		t.Errorf("stdout = %q, want one preview pass (a main sync line)", stdout)
	}
	if got := f.ghIssueListCalls(); got != 1 {
		t.Errorf("gh issue list calls = %d, want exactly one pass", got)
	}
	if muts := f.mutations(); len(muts) != 0 {
		t.Errorf("preview pass made mutation calls:\n%s", strings.Join(muts, "\n"))
	}
}

// TestDispatchDryRunOnceWithInterval_SinglePreviewPassNoMutation: --once wins
// over --interval, so the combination keeps today's behaviour -- one preview
// pass, exit 0, zero mutation calls, the eligible ticket reported.
func TestDispatchDryRunOnceWithInterval_SinglePreviewPassNoMutation(t *testing.T) {
	f := newDispatchCLIFixture(t, dispatchCLIIssues)

	stdout, stderr, code := f.run("--dry-run", "--interval", "5m", "--once")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := f.ghIssueListCalls(); got != 1 {
		t.Errorf("gh issue list calls = %d, want exactly one pass", got)
	}
	if !strings.Contains(stdout, fmt.Sprintf("#%d", dispatchCLITicket)) {
		t.Errorf("stdout = %q, want a decision line for #%d", stdout, dispatchCLITicket)
	}
	if muts := f.mutations(); len(muts) != 0 {
		t.Errorf("preview pass made mutation calls:\n%s", strings.Join(muts, "\n"))
	}
}

// TestDispatchReconcileDryRunWithInterval_SingleReconcilePass: --reconcile
// takes its branch before the rejection, so the three-flag combination runs one
// reconcile dry-run pass and exits 0 without mutating anything.
func TestDispatchReconcileDryRunWithInterval_SingleReconcilePass(t *testing.T) {
	f := newDispatchCLIFixture(t, dispatchCLIIssues)

	stdout, stderr, code := f.run("--reconcile", "--dry-run", "--interval", "5m")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.Contains(stderr, dispatchCLIDryRunRule) {
		t.Errorf("stderr = %q must not reject --reconcile", stderr)
	}
	if got := f.ghIssueListCalls(); got != 1 {
		t.Errorf("gh issue list calls = %d, want exactly one reconcile pass", got)
	}
	if muts := f.mutations(); len(muts) != 0 {
		t.Errorf("reconcile dry-run made mutation calls:\n%s", strings.Join(muts, "\n"))
	}
}

// TestDispatchModelFlag_StillLoggedOnDryRunOnceWithInterval keeps the --model
// override visible in the pass log when it rides along with the neighbouring
// --dry-run --interval --once combination.
func TestDispatchModelFlag_StillLoggedOnDryRunOnceWithInterval(t *testing.T) {
	f := newDispatchCLIFixture(t, dispatchCLIIssues)

	stdout, stderr, code := f.run("--model", "claude-sonnet-5", "--dry-run", "--interval", "5m", "--once")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `dispatch: model override "claude-sonnet-5"`) {
		t.Errorf("stdout = %q, want the model override logged", stdout)
	}
}

// TestDispatchDryRunFalseWithInterval_EntersLoop: --dry-run=false is not a
// dry run, so --interval 5m starts the real loop. The test never waits the
// production interval: it starts the built binary, waits for the first pass's
// main-sync log line, checks the process is still alive after a single pass
// would have exited, then kills it. The fake gh offers no tickets so the real
// pass has nothing to launch.
func TestDispatchDryRunFalseWithInterval_EntersLoop(t *testing.T) {
	f := newDispatchCLIFixture(t, dispatchCLINoWork)

	cmd := exec.Command(binaryPath, "dispatch", "--dry-run=false", "--interval", "5m", "--config", f.configPath)
	cmd.Env = f.env
	stdout := &syncBuffer{}
	var stderr syncBuffer
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting dispatch loop: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case waitErr := <-exited:
			_ = waitErr
		case <-time.After(10 * time.Second):
		}
	})

	marker := "main sync " + dispatchCLIRepo
	deadline := time.After(30 * time.Second)
	for !strings.Contains(stdout.String(), marker) {
		select {
		case err := <-exited:
			t.Fatalf("process exited (%v) before logging its first pass\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		case <-deadline:
			t.Fatalf("no first-pass log line within 30s\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
		case <-time.After(20 * time.Millisecond):
		}
	}

	// A single pass finishes within milliseconds and exits; the loop sleeps for
	// the 5m interval. Staying alive past a generous grace window proves RunLoop.
	select {
	case err := <-exited:
		t.Fatalf("process exited (%v) after one pass; --dry-run=false --interval 5m must keep looping\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	case <-time.After(2 * time.Second):
	}
	if strings.Contains(stderr.String(), dispatchCLIDryRunRule) {
		t.Errorf("stderr = %q must not reject --dry-run=false", stderr.String())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("killing loop: %v", err)
	}
}

// TestDispatchHelp_StatesDryRunCannotCombineWithInterval covers "Help and docs
// match" for the binary: `cenci dispatch --help` says the rule.
func TestDispatchHelp_StatesDryRunCannotCombineWithInterval(t *testing.T) {
	cmd := exec.Command(binaryPath, "dispatch", "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dispatch --help: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), dispatchCLIDryRunRule) {
		t.Errorf("help output = %q, want it to state %q", output, dispatchCLIDryRunRule)
	}
}

// TestDispatchApproveVerb_IsRouted proves the `approve` token reaches the
// approve verb rather than the unknown-subcommand branch: with no id it exits 2
// with the approve usage line. (The release behaviour itself is covered through
// dispatchApprove in dispatch_approve_cmd_test.go.)
func TestDispatchApproveVerb_IsRouted(t *testing.T) {
	cmd := exec.Command(binaryPath, "dispatch", "approve")
	cmd.Env = pipelineEnv("")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2\nstderr:\n%s", exitErr.ExitCode(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "cenci dispatch approve: usage:") {
		t.Errorf("stderr = %q, want the approve usage line", stderr.String())
	}
	if strings.Contains(stderr.String(), "unknown subcommand") {
		t.Errorf("stderr = %q: approve must be a routed verb, not an unknown subcommand", stderr.String())
	}
}
