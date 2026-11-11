package main_test

// Ticket #1455: black-box tests of the finite CI-repair launch. A built `cenci
// babysit --once` ticks against a fake `gh` that reports failing checks,
// self-execs `cenci run ci-repair`, and a fake `tmux` records the `new-window`
// command. The test then runs that captured command under `sh -c`, the way tmux
// would, against fake `claude`/`codex` clients across the host/sandbox ×
// Claude/Codex matrix, and asserts the client process exited.
//
// The fake clients block (like an interactive session waiting at its prompt)
// unless invoked in their finite mode -- Claude with `--print`, Codex as
// `codex exec` -- so "the process exited" is evidence of a one-shot template,
// not of a fake that always exits. In the sandbox cells `cenci` is the built
// binary on PATH and `cenci open` runs against the scripted runtime; a thin
// docker/podman wrapper turns the final `exec -it` attach into the fake client,
// which records the container-side argv.
//
// Isolation (watch/docs/test-isolation.md): openTestEnv supplies the isolated
// HOME/XDG_CONFIG_HOME, the asset fixture, the scripted runtime and a live event
// socket (so `cenci run`/`cenci open` never spawn a daemon); every GH_*
// credential variable is neutralized, and XDG_STATE_HOME/CODEX_HOME point at
// test-owned directories.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox/launcher"
)

const (
	ciRepairPR      = "42"
	ciRepairRepo    = "o/r"
	ciRepairSession = "work"
	ciRepairHead    = "abc"
	ciRepairOldHead = "old-sha"
	// ciRepairBranch carries every shell-hostile character the edge cases
	// name: a single quote, `$`, backticks, parentheses and spaces.
	ciRepairBranch = "fix/it's $HOME `id` (x) y"
	// ciRepairWorkerTimeout bounds one captured worker command. A finite
	// worker ends in well under a second; a blocked (interactive) one never does.
	ciRepairWorkerTimeout = 20 * time.Second
)

// ciRepairArg is the exact `cenci run ci-repair` argument babysit builds for
// the fixture PR: the PR number first, then today's prose byte-for-byte. The
// failing checks hold commas and digits (`lint, test (3.11), e2e #2`).
const ciRepairArg = ciRepairPR + " PR #" + ciRepairPR + " (" + ciRepairBranch + ") has failing CI checks: lint, test (3.11), e2e #2. Diagnose, fix, test, commit, and push without force-pushing."

// ciRepairCell is one matrix cell.
type ciRepairCell struct {
	agent   string
	sandbox bool
}

func (c ciRepairCell) name() string {
	if c.sandbox {
		return c.agent + "/sandbox"
	}
	return c.agent + "/host"
}

// wantClientArgv is the argv the fake client must receive: the host template
// verbatim, or, in the sandbox, the launcher's permission and model flags
// followed by the passthrough -- after `exec` for Codex.
func (c ciRepairCell) wantClientArgv() []string {
	model := launcher.DefaultModel(c.agent)
	switch {
	case c.agent == "claude" && !c.sandbox:
		return []string{"--print", "--", "/cenci:ci-repair " + ciRepairArg}
	case c.agent == "codex" && !c.sandbox:
		return []string{"exec", "$cenci:ci-repair " + ciRepairArg}
	case c.agent == "claude":
		return []string{"--dangerously-skip-permissions", "--model", model, "--print", "/cenci:ci-repair " + ciRepairArg}
	default:
		return []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust", "--model", model, "$cenci:ci-repair " + ciRepairArg}
	}
}

var ciRepairCells = []ciRepairCell{{"claude", false}, {"codex", false}, {"claude", true}, {"codex", true}}

// ciRepairWorld is one test's isolated fake host.
type ciRepairWorld struct {
	cell      ciRepairCell
	env       []string
	home      string
	binDir    string
	ctlDir    string
	fixtures  string
	workDir   string
	stateDir  string
	tmuxLog   string
	clientLog string
}

// writeCIRepairFakeTmux writes a fake `tmux` that records every call
// NUL-separated ("<argc>\0<arg>\0...") to logPath and is steered by files in
// ctlDir, never env vars (a socket-carrying launcher call runs with a minimal
// environment): `grouped` makes the `#{session_grouped}` probe answer 1;
// `panes` is the `list-panes` inventory (absent: the call fails, an unreadable
// inventory); `fail-<option>` fails `set-window-option` for that option.
// Everything else, including the launcher's `-S <socket> set-option` pane tags,
// exits 0.
func writeCIRepairFakeTmux(t *testing.T, binDir, ctlDir, logPath string) {
	t.Helper()
	ctl := func(name string) string { return exectest.ShellQuote(filepath.Join(ctlDir, name)) }
	exectest.WriteExecutable(t, filepath.Join(binDir, "tmux"), "#!/bin/sh\n"+
		`printf '%s\000' "$#" "$@" >> `+exectest.ShellQuote(logPath)+"\n"+
		// `cenci run` always leads with `-S <socket>` (#1431): drop it after logging.
		`if [ "$1" = -S ]; then shift 2; fi`+"\n"+
		`case "$1" in`+"\n"+
		`has-session) exit 0 ;;`+"\n"+
		`display-message) if [ -e `+ctl("grouped")+` ]; then echo 1; else echo 0; fi ;;`+"\n"+
		`list-panes) if [ -e `+ctl("panes")+` ]; then cat `+ctl("panes")+`; else echo 'fake tmux: lost server' >&2; exit 1; fi ;;`+"\n"+
		`set-window-option) if [ -e `+exectest.ShellQuote(ctlDir)+`/"fail-$4" ]; then echo "fake tmux: refused $4" >&2; exit 1; fi ;;`+"\n"+
		"esac\nexit 0\n")
}

// writeCIRepairFakeClients writes fake `claude` and `codex` clients. Each
// records its argv NUL-separated to logPath, then blocks -- an interactive
// session waiting at its prompt -- unless invoked in finite mode (Claude:
// `--print` anywhere; Codex: `exec` first). In finite mode it exits
// $FAKE_CLIENT_EXIT (default 0); FAKE_CLIENT_DENY=1 simulates a permission
// denial: it reports it on stderr and exits 1. Codex also answers the host
// adapter probe's `plugin list --json` from pluginList, without recording it.
func writeCIRepairFakeClients(t *testing.T, binDir, logPath, pluginList string) {
	t.Helper()
	finish := `if [ "${FAKE_CLIENT_DENY:-}" = 1 ]; then echo 'permission denied: Bash(git push) was not approved' >&2; exit 1; fi` + "\n" +
		`exit "${FAKE_CLIENT_EXIT:-0}"` + "\n"
	record := `printf '%s\000' "$#" "$@" >> ` + exectest.ShellQuote(logPath) + "\n"
	exectest.WriteExecutable(t, filepath.Join(binDir, "claude"), "#!/bin/sh\n"+record+
		"finite=\nfor a do [ \"$a\" = --print ] && finite=1; done\n"+
		"[ -n \"$finite\" ] || exec sleep 300\n"+finish)
	exectest.WriteExecutable(t, filepath.Join(binDir, "codex"), "#!/bin/sh\n"+
		`if [ "$1 $2 $3" = "plugin list --json" ]; then cat `+exectest.ShellQuote(pluginList)+"; exit 0; fi\n"+record+
		"[ \"$1\" = exec ] || exec sleep 300\n"+finish)
}

// writeCIRepairRuntimeWrapper puts a docker/podman wrapper in binDir over the
// scripted runtime in rtDir: every call goes to the scripted runtime except the
// final `exec -it` attach, which becomes the fake client named by the agent
// binary token, receiving exactly the container-side argv that follows it.
func writeCIRepairRuntimeWrapper(t *testing.T, binDir, rtDir string) {
	t.Helper()
	callLog := filepath.Join(rtDir, "calls.txt")
	for _, name := range []string{"docker", "podman"} {
		writeScriptedRuntime(t, rtDir, name, callLog)
		exectest.WriteExecutable(t, filepath.Join(binDir, name), "#!/bin/sh\n"+
			`if [ "$1" = exec ] && [ "$2" = -it ]; then`+"\n"+
			`  while [ "$#" -gt 0 ]; do`+"\n"+
			`    case "$1" in /opt/cenci-agent/current/node_modules/.bin/*) bin=${1##*/}; shift; exec `+exectest.ShellQuote(binDir)+`/"$bin" "$@" ;; esac`+"\n"+
			"    shift\n  done\n"+
			`  echo 'fake runtime: no agent binary in the attach argv' >&2; exit 97`+"\n"+
			"fi\n"+
			"exec "+exectest.ShellQuote(filepath.Join(rtDir, name))+` "$@"`+"\n")
	}
}

// ciRepairWorldOpts tunes one world: the Codex host plugin state and whether
// automerge is enabled (a refusal's reason is recorded only when it is).
type ciRepairWorldOpts struct {
	pluginDisabled   bool
	adapterMissing   bool
	automergeEnabled bool
}

func newCIRepairWorld(t *testing.T, cell ciRepairCell, opts ciRepairWorldOpts) *ciRepairWorld {
	t.Helper()
	root := t.TempDir()
	w := &ciRepairWorld{
		cell:      cell,
		binDir:    filepath.Join(root, "bin"),
		ctlDir:    filepath.Join(root, "ctl"),
		fixtures:  filepath.Join(root, "fixtures"),
		workDir:   filepath.Join(root, "work"),
		stateDir:  filepath.Join(root, "state", "babysit"),
		tmuxLog:   filepath.Join(root, "tmux.log"),
		clientLog: filepath.Join(root, "client.log"),
	}
	codexHome := filepath.Join(root, "codex-home")
	rtDir := filepath.Join(root, "runtime")
	for _, d := range []string{w.binDir, w.ctlDir, w.fixtures, w.workDir, rtDir, filepath.Join(root, "gh-config")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	writeCIRepairFakeTmux(t, w.binDir, w.ctlDir, w.tmuxLog)
	pluginList := filepath.Join(w.fixtures, "plugins.json")
	ciRepairWrite(t, pluginList, adapterLaunchPluginList(!opts.pluginDisabled))
	writeCIRepairFakeClients(t, w.binDir, w.clientLog, pluginList)
	if cell.sandbox {
		writeCIRepairRuntimeWrapper(t, w.binDir, rtDir)
		if err := os.Symlink(binaryPath, filepath.Join(w.binDir, "cenci")); err != nil {
			t.Fatal(err)
		}
	}

	// Fake gh: the fixture PR (setPR) with three failing checks, no comments or
	// reviews; anything else fails.
	w.setPR(t, ciRepairHead)
	ciRepairWrite(t, filepath.Join(w.fixtures, "checks.json"), `[{"bucket":"fail","name":"lint","state":"FAILURE"},{"bucket":"fail","name":"test (3.11)","state":"FAILURE"},{"bucket":"fail","name":"e2e #2","state":"FAILURE"},{"bucket":"pass","name":"py3.11-shard-2","state":"SUCCESS"}]`)
	// #1472: the PR conversation read (`issues/<pr>/comments`) is served from a
	// fixture, `[]` by default.
	ciRepairWrite(t, filepath.Join(w.fixtures, "discussion.json"), `[]`)
	exectest.WriteExecutable(t, filepath.Join(w.binDir, "gh"), "#!/bin/sh\n"+
		`case "$1 $2" in`+"\n"+
		`  "repo view") echo `+ciRepairRepo+` ;;`+"\n"+
		`  "pr view") cat `+exectest.ShellQuote(filepath.Join(w.fixtures, "pr.json"))+` ;;`+"\n"+
		`  "pr checks") cat `+exectest.ShellQuote(filepath.Join(w.fixtures, "checks.json"))+` ;;`+"\n"+
		`  api\ repos/*/pulls/*/comments*|api\ repos/*/pulls/*/reviews*) echo '[]' ;;`+"\n"+
		`  api\ repos/*/issues/*/comments*) cat `+exectest.ShellQuote(filepath.Join(w.fixtures, "discussion.json"))+` ;;`+"\n"+
		`  *) exit 1 ;;`+"\n"+
		"esac\n")

	// The Codex host launch probes the installed plugin root for the adapter.
	adapter := filepath.Join(codexHome, "plugins", "cache", "cenci", "cenci", "1.2.3", "skills", "ci-repair", "codex.md")
	if err := os.MkdirAll(filepath.Dir(adapter), 0o755); err != nil {
		t.Fatal(err)
	}
	if !opts.adapterMissing {
		ciRepairWrite(t, adapter, "# ci-repair Codex procedure\n")
	}

	env, home, _ := openTestEnv(t, w.binDir, writeAssetFixture(t))
	w.home = home
	if cell.agent == "codex" {
		stageCodexAuth(t, home)
	}
	config := map[string]any{"sandbox": cell.sandbox}
	if opts.automergeEnabled {
		config["automerge"] = map[string]any{"enabled": true}
	}
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	writeFleetConfigFixture(t, home, string(body))

	w.env = append(env,
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"CODEX_HOME="+codexHome,
		"GH_TOKEN=", "GITHUB_TOKEN=", "GH_ENTERPRISE_TOKEN=", "GITHUB_ENTERPRISE_TOKEN=", "GH_HOST=",
		"GH_CONFIG_DIR="+filepath.Join(root, "gh-config"),
	)
	return w
}

func ciRepairWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setPR rewrites the fixture PR at head, keeping the shell-hostile branch and a
// closing issue numbered 7 (which must never name the window).
func (w *ciRepairWorld) setPR(t *testing.T, head string) {
	t.Helper()
	pr, err := json.Marshal(map[string]any{
		"number": 42, "title": "Change", "state": "OPEN", "headRefName": ciRepairBranch, "headRefOid": head,
		"url": "https://example/pr/42", "closingIssuesReferences": []map[string]any{{"number": 7}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ciRepairWrite(t, filepath.Join(w.fixtures, "pr.json"), string(pr))
}

// ciRepairOtherRow is an unrelated window as one three-column inventory row
// (window_name, pane_dead, @cenci-repo-dir): no worker, no recorded repo-dir.
const ciRepairOtherRow = "1-implement\t0\t\n"

// setPanes sets the `list-panes` inventory the fake tmux reports.
func (w *ciRepairWorld) setPanes(t *testing.T, panes string) {
	t.Helper()
	ciRepairWrite(t, filepath.Join(w.ctlDir, "panes"), panes)
}

// workerRow is one inventory row for a window this repository owns: its
// @cenci-repo-dir is the recorded launch dir, so only the name and pane_dead
// decide whether it is a live worker.
func (w *ciRepairWorld) workerRow(name string, dead bool) string {
	state := "0"
	if dead {
		state = "1"
	}
	return name + "\t" + state + "\t" + w.workDir + "\n"
}

// touch creates one fake-tmux control file.
func (w *ciRepairWorld) touch(t *testing.T, name string) {
	t.Helper()
	ciRepairWrite(t, filepath.Join(w.ctlDir, name), "")
}

// seedState writes a supervisor with one attempt already spent at an older
// head, so every refusal's "unchanged" check compares non-zero values.
func (w *ciRepairWorld) seedState(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"schemaVersion": adapterLaunchSchema, "pr": ciRepairPR, "repo": ciRepairRepo, "agent": w.cell.agent,
		"intervalSeconds": 60, "currentDelaySeconds": 60, "status": "running", "updatedAt": "2026-01-01T00:00:00Z",
		"ciFixAttempts": 1, "ciRepairPending": true, "lastRepairedCiHeadSha": ciRepairOldHead,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(w.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(babysitStatePath(w.stateDir, ciRepairRepo, ciRepairPR), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// ciRepairFields are the state fields the AC pins, by JSON name.
type ciRepairFields struct {
	FixAttempts     int    `json:"ciFixAttempts"`
	RepairPending   bool   `json:"ciRepairPending"`
	LastRepairedSHA string `json:"lastRepairedCiHeadSha"`
	AutomergeReason string `json:"automergeReason"`
}

func (w *ciRepairWorld) fields(t *testing.T) ciRepairFields {
	t.Helper()
	data, err := os.ReadFile(babysitStatePath(w.stateDir, ciRepairRepo, ciRepairPR))
	if err != nil {
		t.Fatal(err)
	}
	var f ciRepairFields
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// repairFields is fields without AutomergeReason, for the dispatch paths,
// where the reason is automerge's own (disabled) hold and not under test.
func (w *ciRepairWorld) repairFields(t *testing.T) ciRepairFields {
	t.Helper()
	f := w.fields(t)
	f.AutomergeReason = ""
	return f
}

// babysitOnce runs `cenci babysit 42 --agent <agent> --once` in the recorded
// session (unless session is empty) and returns its combined output and exit
// code.
func (w *ciRepairWorld) babysitOnce(t *testing.T, session string, extraEnv ...string) (string, int) {
	t.Helper()
	args := []string{"babysit", ciRepairPR, "--agent", w.cell.agent, "--once", "--state-dir", w.stateDir, "--dir", w.workDir}
	if session != "" {
		args = append(args, "--session", session)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Dir = w.workDir
	cmd.Env = append(slices.Clone(w.env), extraEnv...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &exitErr):
		return out.String(), exitErr.ExitCode()
	default:
		t.Fatalf("running cenci babysit: %v", err)
		return "", -1
	}
}

// tmuxCalls returns the recorded fake-tmux calls whose first argument is verb.
func (w *ciRepairWorld) tmuxCalls(t *testing.T, verb string) [][]string {
	t.Helper()
	var out [][]string
	for _, c := range adapterLaunchCalls(t, w.tmuxLog) {
		if c = withoutTmuxSocket(c); len(c) > 0 && c[0] == verb {
			out = append(out, c)
		}
	}
	return out
}

// windowNames returns the `-n` name of every `new-window` issued so far, in
// order, after checking each targets the recorded session.
func (w *ciRepairWorld) windowNames(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, c := range w.tmuxCalls(t, "new-window") {
		if len(c) < 6 || !slices.Equal(c[:4], []string{"new-window", "-d", "-t", "=" + ciRepairSession + ":"}) || c[4] != "-n" {
			t.Fatalf("new-window argv = %q, want -d -t =%s: -n <name> followed by the command", c, ciRepairSession)
		}
		names = append(names, c[5])
	}
	return names
}

// lastWindowCommand returns the shell command of the most recent `new-window`.
func (w *ciRepairWorld) lastWindowCommand(t *testing.T) string {
	t.Helper()
	windows := w.tmuxCalls(t, "new-window")
	if len(windows) == 0 {
		t.Fatal("no tmux new-window was issued")
	}
	last := windows[len(windows)-1]
	return last[len(last)-1]
}

// onlyWindowCommand asserts exactly one `new-window` was issued, in the recorded
// session and named 42-ci-repair, and returns its shell command.
func (w *ciRepairWorld) onlyWindowCommand(t *testing.T) string {
	t.Helper()
	if got, want := w.windowNames(t), []string{ciRepairPR + "-ci-repair"}; !slices.Equal(got, want) {
		t.Fatalf("tmux new-window names = %q, want exactly %q", got, want)
	}
	return w.lastWindowCommand(t)
}

// assertWindowOptions asserts 42-ci-repair got automatic-rename off and the
// recorded launch dir as @cenci-repo-dir.
func (w *ciRepairWorld) assertWindowOptions(t *testing.T) {
	t.Helper()
	target := "=" + ciRepairSession + ":=" + ciRepairPR + "-ci-repair"
	got := w.tmuxCalls(t, "set-window-option")
	want := [][]string{
		{"set-window-option", "-t", target, "automatic-rename", "off"},
		{"set-window-option", "-t", target, "@cenci-repo-dir", w.workDir},
	}
	if len(got) != len(want) || !slices.Equal(got[0], want[0]) || !slices.Equal(got[1], want[1]) {
		t.Errorf("set-window-option calls = %q, want %q", got, want)
	}
}

// runWorker runs the captured window command under `sh -c`, as tmux would, in
// its own process group, killed at ciRepairWorkerTimeout. It fails the test if
// the worker is still running then (an interactive prompt left waiting), and
// returns its exit code and combined output.
func (w *ciRepairWorld) runWorker(t *testing.T, shellCommand string, extraEnv ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), ciRepairWorkerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", shellCommand)
	cmd.Env = append(slices.Clone(w.env), extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // reap any straggler in the group
	}
	if ctx.Err() != nil {
		t.Fatalf("worker still running after %s, so a prompt was left waiting; output:\n%s", ciRepairWorkerTimeout, out.String())
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String()
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), out.String()
	default:
		t.Fatalf("running the window command: %v", err)
		return -1, ""
	}
}

// clientArgv returns the single recorded fake-client invocation.
func (w *ciRepairWorld) clientArgv(t *testing.T) []string {
	t.Helper()
	calls := adapterLaunchCalls(t, w.clientLog)
	if len(calls) != 1 {
		t.Fatalf("fake client invocations = %q, want exactly one", calls)
	}
	return calls[0]
}

// assertWindowCommandPinsUnattended is #1497's end-to-end check that a
// babysit-launched window carries the unattended pin: the supervisor's
// `cenci run ci-repair ... --unattended` put `CENCI_ATTENDED=0 ` on the agent
// command, after the `cd <dir> &&` prefix (so the assignment scopes to the
// agent, not the `cd`), exactly once, and with no CENCI_ATTENDED_ORIGIN: a host
// launch carries only the bare `0`, which pipeline.Unattended() reads as
// unattended (covered by TestUnattended_Matrix in internal/pipeline).
func assertWindowCommandPinsUnattended(t *testing.T, shellCommand string) {
	t.Helper()
	const pin = "CENCI_ATTENDED=0 "
	if !strings.Contains(shellCommand, " && "+pin) {
		t.Errorf("window command %q lacks %q after the cd prefix", shellCommand, " && "+pin)
	}
	if strings.HasPrefix(shellCommand, "CENCI_ATTENDED") {
		t.Errorf("window command %q starts with the assignment: it must sit on the agent argv, not the cd", shellCommand)
	}
	if n := strings.Count(shellCommand, pin); n != 1 {
		t.Errorf("window command %q carries %d %q assignments, want exactly 1", shellCommand, n, pin)
	}
	if strings.Contains(shellCommand, "CENCI_ATTENDED_ORIGIN") {
		t.Errorf("window command %q must not carry CENCI_ATTENDED_ORIGIN on a host launch", shellCommand)
	}
}

// AC1/AC2/AC4/AC5/AC6: in every cell the supervisor launches `42-ci-repair`, the
// captured command runs the client in its finite mode with the argument intact,
// and the client process exits -- after success and after an explicit failure.
// A failed (blocked) outcome leaves RepairPending set, and the retained dead
// pane blocks nothing (#1457): the next tick at the same head retries, spending
// the next attempt.
func TestCIRepairMatrix_WorkerExits(t *testing.T) {
	for _, cell := range ciRepairCells {
		for _, exit := range []int{0, 1} {
			t.Run(cell.name()+"/exit="+strconv.Itoa(exit), func(t *testing.T) {
				w := newCIRepairWorld(t, cell, ciRepairWorldOpts{})
				w.seedState(t)
				w.setPanes(t, ciRepairOtherRow)

				if out, code := w.babysitOnce(t, ciRepairSession); code != 0 {
					t.Fatalf("cenci babysit --once exit = %d, want 0; output:\n%s", code, out)
				}
				shellCommand := w.onlyWindowCommand(t)
				assertWindowCommandPinsUnattended(t, shellCommand)
				w.assertWindowOptions(t)
				if got, want := w.repairFields(t), (ciRepairFields{FixAttempts: 2, RepairPending: true, LastRepairedSHA: ciRepairHead}); got != want {
					t.Errorf("state after dispatch = %+v, want %+v", got, want)
				}

				code, out := w.runWorker(t, shellCommand, "FAKE_CLIENT_EXIT="+strconv.Itoa(exit))
				if code != exit {
					t.Errorf("worker exit = %d, want the client's %d; output:\n%s", code, exit, out)
				}
				if got, want := w.clientArgv(t), cell.wantClientArgv(); !slices.Equal(got, want) {
					t.Errorf("client argv =\n %q\nwant\n %q", got, want)
				}
				if exit == 0 {
					return
				}

				w.setPanes(t, w.workerRow(ciRepairPR+"-ci-repair", true))
				if out, code := w.babysitOnce(t, ciRepairSession); code != 0 {
					t.Fatalf("tick at the same head: exit = %d; output:\n%s", code, out)
				}
				if got, want := w.windowNames(t), []string{ciRepairPR + "-ci-repair", ciRepairPR + "-ci-repair"}; !slices.Equal(got, want) {
					t.Errorf("new-window names after a same-head tick = %q, want %q: a dead 42-ci-repair pane must not block the retry", got, want)
				}
				if got, want := w.repairFields(t), (ciRepairFields{FixAttempts: 3, RepairPending: true, LastRepairedSHA: ciRepairHead}); got != want {
					t.Errorf("state after the same-head retry = %+v, want %+v", got, want)
				}
			})
		}
	}
}

// A host worker that hits a permission denial reports it and exits; nothing is
// left waiting at a prompt.
func TestCIRepairMatrix_HostPermissionDenialExits(t *testing.T) {
	for _, cell := range []ciRepairCell{{"claude", false}, {"codex", false}} {
		t.Run(cell.name(), func(t *testing.T) {
			w := newCIRepairWorld(t, cell, ciRepairWorldOpts{})
			w.seedState(t)
			w.setPanes(t, ciRepairOtherRow)
			if out, code := w.babysitOnce(t, ciRepairSession); code != 0 {
				t.Fatalf("cenci babysit --once exit = %d; output:\n%s", code, out)
			}
			code, out := w.runWorker(t, w.onlyWindowCommand(t), "FAKE_CLIENT_DENY=1")
			if code != 1 || !strings.Contains(out, "permission denied") {
				t.Errorf("worker exit = %d, output %q; want exit 1 with the reported denial", code, out)
			}
		})
	}
}

// A live 42-ci-repair worker suppresses another launch without consuming an
// attempt; a live 42-ci-repair-old or 142-ci-repair window does not.
func TestCIRepairLiveWorkerGuard(t *testing.T) {
	for _, tc := range []struct {
		name     string
		panes    func(w *ciRepairWorld) string
		dispatch bool
	}{
		{"live exact worker suppresses", func(w *ciRepairWorld) string {
			return ciRepairOtherRow + w.workerRow(ciRepairPR+"-ci-repair", false)
		}, false},
		{"siblings do not block", func(w *ciRepairWorld) string {
			return w.workerRow(ciRepairPR+"-ci-repair-old", false) + w.workerRow("1"+ciRepairPR+"-ci-repair", false)
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCIRepairWorld(t, ciRepairCell{"claude", false}, ciRepairWorldOpts{})
			w.seedState(t)
			w.setPanes(t, tc.panes(w))
			if out, code := w.babysitOnce(t, ciRepairSession); code != 0 {
				t.Fatalf("cenci babysit --once exit = %d; output:\n%s", code, out)
			}
			want := ciRepairFields{FixAttempts: 1, RepairPending: true, LastRepairedSHA: ciRepairOldHead}
			wantWindows := 0
			if tc.dispatch {
				want = ciRepairFields{FixAttempts: 2, RepairPending: true, LastRepairedSHA: ciRepairHead}
				wantWindows = 1
			}
			if n := len(w.tmuxCalls(t, "new-window")); n != wantWindows {
				t.Errorf("new-window calls = %d, want %d", n, wantWindows)
			}
			if got := w.repairFields(t); got != want {
				t.Errorf("state = %+v, want %+v", got, want)
			}
		})
	}
}

// AC7: every pre-spawn refusal opens no window, keeps the attempt budget and
// the dedup marker, and records reasonWorkflowLaunchFailed.
func TestCIRepairRefusalsConsumeNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cell    ciRepairCell
		opts    ciRepairWorldOpts
		session string
		setup   func(t *testing.T, w *ciRepairWorld)
		env     []string
	}{
		{name: "codex adapter missing", cell: ciRepairCell{"codex", false}, opts: ciRepairWorldOpts{adapterMissing: true}, session: ciRepairSession},
		{name: "codex plugin disabled", cell: ciRepairCell{"codex", false}, opts: ciRepairWorldOpts{pluginDisabled: true}, session: ciRepairSession},
		{name: "no recorded session", cell: ciRepairCell{"claude", false}, env: []string{"TMUX_PANE=", "TMUX="}},
		{name: "grouped session", cell: ciRepairCell{"claude", false}, session: ciRepairSession,
			setup: func(t *testing.T, w *ciRepairWorld) { w.touch(t, "grouped") }},
		{name: "unreadable pane inventory", cell: ciRepairCell{"claude", false}, session: ciRepairSession,
			setup: func(t *testing.T, w *ciRepairWorld) { _ = os.Remove(filepath.Join(w.ctlDir, "panes")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.automergeEnabled = true
			w := newCIRepairWorld(t, tc.cell, tc.opts)
			w.seedState(t)
			w.setPanes(t, ciRepairOtherRow)
			if tc.setup != nil {
				tc.setup(t, w)
			}

			out, code := w.babysitOnce(t, tc.session, tc.env...)
			if code != 1 {
				t.Errorf("cenci babysit --once exit = %d, want 1 (a failed launch); output:\n%s", code, out)
			}
			if windows := w.tmuxCalls(t, "new-window"); len(windows) != 0 {
				t.Errorf("a refused launch spawned windows: %q", windows)
			}
			want := ciRepairFields{FixAttempts: 1, RepairPending: true, LastRepairedSHA: ciRepairOldHead, AutomergeReason: "workflow launch failed"}
			if got := w.fields(t); got != want {
				t.Errorf("state = %+v, want %+v", got, want)
			}
		})
	}
}

// AC8: a post-spawn failure (`cenci run` exit 3) is a dispatched attempt, and
// while its window is live (#1457) the next tick at the same head opens no
// second one. A @cenci-repo-dir write failure only warns: the launch is
// dispatched too.
func TestCIRepairPostSpawnFailuresCountAsDispatched(t *testing.T) {
	for _, tc := range []struct{ name, marker, wantOutput string }{
		{"automatic-rename failure exits 3", "fail-automatic-rename", "warning"},
		{"repo-dir failure only warns", "fail-@cenci-repo-dir", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCIRepairWorld(t, ciRepairCell{"claude", false}, ciRepairWorldOpts{})
			w.seedState(t)
			w.setPanes(t, ciRepairOtherRow)
			w.touch(t, tc.marker)

			out, code := w.babysitOnce(t, ciRepairSession)
			if code != 0 {
				t.Fatalf("cenci babysit --once exit = %d, want 0 (dispatched); output:\n%s", code, out)
			}
			if !strings.Contains(out, tc.wantOutput) {
				t.Errorf("supervisor output %q, want it to contain %q", out, tc.wantOutput)
			}
			w.onlyWindowCommand(t)
			want := ciRepairFields{FixAttempts: 2, RepairPending: true, LastRepairedSHA: ciRepairHead}
			if got := w.repairFields(t); got != want {
				t.Errorf("state = %+v, want %+v", got, want)
			}

			w.setPanes(t, ciRepairOtherRow+w.workerRow(ciRepairPR+"-ci-repair", false))
			if out, code := w.babysitOnce(t, ciRepairSession); code != 0 {
				t.Fatalf("tick at the same head: exit = %d; output:\n%s", code, out)
			}
			if n := len(w.tmuxCalls(t, "new-window")); n != 1 {
				t.Errorf("new-window calls after a same-head tick = %d, want still 1", n)
			}
			if got := w.repairFields(t); got != want {
				t.Errorf("state after the live-worker tick = %+v, want %+v (no second attempt)", got, want)
			}
		})
	}
}

// #1457: black-box, finite fake worker. Each ended worker (it exits without
// pushing, leaving a dead pane) is retried at the same failing head with a new
// `42-ci-repair` window; a live one suppresses the relaunch. After the third
// window ends, exactly one babysit-attention window opens for the cap and no
// further window follows.
func TestCIRepairEndedWorkersRetryToCapAttention(t *testing.T) {
	w := newCIRepairWorld(t, ciRepairCell{"claude", false}, ciRepairWorldOpts{})
	if err := os.MkdirAll(w.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tick := func(step string) {
		t.Helper()
		if out, code := w.babysitOnce(t, ciRepairSession); code != 0 {
			t.Fatalf("%s: cenci babysit --once exit = %d; output:\n%s", step, code, out)
		}
	}
	ciWindow := ciRepairPR + "-ci-repair"
	wantWindows := []string{}

	for attempt := 1; attempt <= 3; attempt++ {
		w.setPanes(t, ciRepairOtherRow)
		tick("launch tick")
		wantWindows = append(wantWindows, ciWindow)
		if got := w.windowNames(t); !slices.Equal(got, wantWindows) {
			t.Fatalf("attempt %d: new-window names = %q, want %q", attempt, got, wantWindows)
		}
		if got := w.fields(t).FixAttempts; got != attempt {
			t.Fatalf("attempt %d: FixAttempts = %d", attempt, got)
		}

		w.setPanes(t, ciRepairOtherRow+w.workerRow(ciWindow, false))
		tick("live-worker tick")
		if got := w.windowNames(t); !slices.Equal(got, wantWindows) {
			t.Fatalf("attempt %d: a live worker still got a new window: %q, want %q", attempt, got, wantWindows)
		}
		if got := w.fields(t).FixAttempts; got != attempt {
			t.Fatalf("attempt %d: FixAttempts after the live tick = %d", attempt, got)
		}

		// The worker really runs to its end without pushing; its pane is retained dead.
		if code, out := w.runWorker(t, w.lastWindowCommand(t), "FAKE_CLIENT_EXIT=1"); code != 1 {
			t.Fatalf("attempt %d: worker exit = %d, want 1; output:\n%s", attempt, code, out)
		}
		w.setPanes(t, w.workerRow(ciWindow, true))
	}

	// The cap tick opens the window `cenci run babysit-attention 42 ...` names:
	// "<PR>-" plus the workflow (internal/run windowName). The next opens none.
	wantWindows = append(wantWindows, ciRepairPR+"-babysit-attention")
	for _, step := range []string{"cap tick", "tick after the cap"} {
		tick(step)
		if got := w.windowNames(t); !slices.Equal(got, wantWindows) {
			t.Fatalf("%s: new-window names = %q, want %q (exactly one attention window)", step, got, wantWindows)
		}
	}
	if got := w.fields(t).FixAttempts; got != 3 {
		t.Errorf("FixAttempts at the cap = %d, want 3", got)
	}
}
