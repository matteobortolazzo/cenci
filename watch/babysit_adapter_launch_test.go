package main_test

// Ticket #1237 AC 3: black-box tests of the supervisor -> `cenci run` ->
// installed-capability path. A built `cenci babysit --once` ticks against a fake
// `gh` that reports a failing check, self-execs `cenci run ci-repair --agent
// codex`, whose host-launch pre-spawn probe asks a fake `codex` what is installed
// and reads the adapter out of a fixture plugin root. A fake `tmux` records every
// argv, so "a window was spawned" is observable.
//
// Isolation (watch/docs/test-isolation.md): the child env is built from scratch,
// fake tmux/gh/codex are first on PATH, TMUX/TMUX_PANE and every GH_* credential
// variable are neutralized, CENCI_SANDBOX is left unset (the host-launch path is
// under test), XDG_*/CODEX_HOME/CENCI_SOCKET_DIR point at test-owned
// directories, and the test owns the daemon's event socket so `cenci run`'s
// EnsureDaemon sees a live daemon and spawns none.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

const (
	adapterLaunchPR      = "42"
	adapterLaunchRepo    = "o/r"
	adapterLaunchSession = "work"
	adapterLaunchVersion = "1.2.3"
	// adapterLaunchAdapter is Codex ci-repair's declared adapter
	// (run.BuiltinAdapter("codex", "ci-repair")).
	adapterLaunchAdapter = "skills/ci-repair/codex.md"
	// adapterLaunchSchema mirrors babysit's stateSchemaVersion: a lower value
	// makes load() migrate PendingKeys into LaunchedKeys and would blur the
	// five-field comparison.
	adapterLaunchSchema = 2
	adapterLaunchStale  = "old-sha"
)

// adapterLaunchWorld is one test's isolated fake host.
type adapterLaunchWorld struct {
	env       []string
	workDir   string
	stateDir  string
	tmuxLog   string
	ghLog     string
	codexLog  string
	codexHome string
}

// adapterLaunchPluginList renders `codex plugin list --json` for one cenci@cenci
// entry. The shape is the one the probe decodes (adapter_probe_test.go).
func adapterLaunchPluginList(enabled bool) string {
	return `{"installed":[{"pluginId":"cenci@cenci","version":"` + adapterLaunchVersion +
		`","installed":true,"enabled":` + strconv.FormatBool(enabled) + `}]}`
}

// newAdapterLaunchWorld builds the fake host. installAdapter controls whether the
// fixture plugin root holds the declared adapter file.
func newAdapterLaunchWorld(t *testing.T, pluginList string, installAdapter bool) *adapterLaunchWorld {
	t.Helper()
	root := t.TempDir()
	w := &adapterLaunchWorld{
		workDir:   filepath.Join(root, "work"),
		stateDir:  filepath.Join(root, "state", "babysit"),
		tmuxLog:   filepath.Join(root, "tmux.log"),
		ghLog:     filepath.Join(root, "gh.log"),
		codexLog:  filepath.Join(root, "codex.log"),
		codexHome: filepath.Join(root, "codex-home"),
	}
	binDir := filepath.Join(root, "bin")
	home := filepath.Join(root, "home")
	xdgConfig := filepath.Join(home, "config")
	xdgState := filepath.Join(root, "state")
	ghConfig := filepath.Join(root, "gh-config")
	fixtures := filepath.Join(root, "fixtures")
	for _, d := range []string{binDir, w.workDir, xdgConfig, filepath.Join(xdgConfig, "cenci"), xdgState, ghConfig, fixtures} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// sandbox:false makes `cenci run` take the host launch path the probe gates.
	write(filepath.Join(xdgConfig, "cenci", "config.json"), `{"sandbox": false}`)

	// The plugin root always exists; only the adapter file inside it is optional.
	pluginRoot := filepath.Join(w.codexHome, "plugins", "cache", "cenci", "cenci", adapterLaunchVersion)
	adapterPath := filepath.Join(pluginRoot, filepath.FromSlash(adapterLaunchAdapter))
	if err := os.MkdirAll(filepath.Dir(adapterPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if installAdapter {
		write(adapterPath, "# ci-repair Codex procedure\n")
	}

	// Fake tmux: one NUL-separated record per call, "<argc>\0<arg>\0...", so
	// argument boundaries survive spaces and `$`. display-message prints 0, the
	// answer to the launcher's `#{session_grouped}` probe; everything else exits 0.
	exectest.WriteExecutable(t, filepath.Join(binDir, "tmux"), "#!/bin/sh\n"+
		`printf '%s\000' "$#" "$@" >> `+exectest.ShellQuote(w.tmuxLog)+"\n"+
		`case "$1" in display-message) echo 0 ;; esac`+"\nexit 0\n")

	// Fake gh: a PR with a failing check at head "abc", no comments or reviews;
	// anything else fails (and is logged, so an unexpected call is visible).
	write(filepath.Join(fixtures, "pr.json"), `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","url":"https://example/pr/42","closingIssuesReferences":[]}`)
	write(filepath.Join(fixtures, "checks.json"), `[{"bucket":"fail","name":"test","state":"FAILURE"}]`)
	exectest.WriteExecutable(t, filepath.Join(binDir, "gh"), "#!/bin/sh\n"+
		`printf '%s\000' "$#" "$@" >> `+exectest.ShellQuote(w.ghLog)+"\n"+
		`case "$1 $2" in`+"\n"+
		`  "repo view") echo `+adapterLaunchRepo+` ;;`+"\n"+
		`  "pr view") cat `+exectest.ShellQuote(filepath.Join(fixtures, "pr.json"))+` ;;`+"\n"+
		`  "pr checks") cat `+exectest.ShellQuote(filepath.Join(fixtures, "checks.json"))+` ;;`+"\n"+
		`  api\ repos/*/pulls/*/comments*|api\ repos/*/pulls/*/reviews*) echo '[]' ;;`+"\n"+
		`  *) exit 1 ;;`+"\n"+
		"esac\n")

	// Fake codex: logs its args and answers only `plugin list --json`.
	write(filepath.Join(fixtures, "plugins.json"), pluginList)
	exectest.WriteExecutable(t, filepath.Join(binDir, "codex"), "#!/bin/sh\n"+
		`printf '%s\n' "$*" >> `+exectest.ShellQuote(w.codexLog)+"\n"+
		`if [ "$1 $2 $3" = "plugin list --json" ]; then cat `+exectest.ShellQuote(filepath.Join(fixtures, "plugins.json"))+`; exit 0; fi`+"\nexit 1\n")

	// The test owns the daemon's event socket under an isolated short dir, so
	// daemon.Alive is true and `cenci run` never spawns a daemon. The dir is
	// outside t.TempDir() to stay under the unix sun_path limit.
	socketDir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	if err := os.Chmod(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(socketDir, watch.EventSocketBasename))
	if err != nil {
		t.Fatalf("listen on the event socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	w.env = []string{
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + xdgConfig,
		"XDG_STATE_HOME=" + xdgState,
		"CENCI_SOCKET_DIR=" + socketDir,
		"CODEX_HOME=" + w.codexHome,
		"TMUX=",
		"TMUX_PANE=",
		"GH_TOKEN=",
		"GITHUB_TOKEN=",
		"GH_ENTERPRISE_TOKEN=",
		"GITHUB_ENTERPRISE_TOKEN=",
		"GH_HOST=",
		"GH_CONFIG_DIR=" + ghConfig,
	}
	return w
}

// statePath mirrors babysit's statePath (sha256(repo)[:6] hex + "-<pr>.json").
func (w *adapterLaunchWorld) statePath() string {
	sum := sha256.Sum256([]byte(adapterLaunchRepo))
	return filepath.Join(w.stateDir, hex.EncodeToString(sum[:6])+"-"+adapterLaunchPR+".json")
}

// seedState writes a supervisor state with non-trivial values in the five AC 2
// fields and returns the bytes written.
func (w *adapterLaunchWorld) seedState(t *testing.T, withFeedbackKeys bool) []byte {
	t.Helper()
	state := map[string]any{
		"schemaVersion":         adapterLaunchSchema,
		"pr":                    adapterLaunchPR,
		"repo":                  adapterLaunchRepo,
		"agent":                 "codex",
		"intervalSeconds":       60,
		"currentDelaySeconds":   60,
		"status":                "running",
		"updatedAt":             "2026-01-01T00:00:00Z",
		"ciFixAttempts":         1,
		"ciRepairPending":       true,
		"lastRepairedCiHeadSha": adapterLaunchStale,
	}
	if withFeedbackKeys {
		state["pendingCommentKeys"] = []string{"comment:5", "comment:6"}
		state["launchedFeedbackKeys"] = []string{"comment:5"}
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(w.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.statePath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

// babysitOnce runs `cenci babysit 42 --agent <agent> --once` and returns its
// stderr and exit code.
func (w *adapterLaunchWorld) babysitOnce(t *testing.T, agent string) (stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(binaryPath, "babysit", adapterLaunchPR, "--agent", agent, "--once",
		"--state-dir", w.stateDir, "--session", adapterLaunchSession, "--dir", w.workDir)
	cmd.Dir = w.workDir
	cmd.Env = w.env
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return errBuf.String(), 0
	case errors.As(err, &exitErr):
		return errBuf.String(), exitErr.ExitCode()
	default:
		t.Fatalf("running cenci babysit: %v", err)
		return "", -1
	}
}

// adapterLaunchCalls decodes a fake's "<argc>\0<arg>\0..." call log; a missing
// log is no calls.
func adapterLaunchCalls(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	tokens := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	var calls [][]string
	for i := 0; i < len(tokens); {
		n, err := strconv.Atoi(tokens[i])
		if err != nil || i+1+n > len(tokens) {
			t.Fatalf("malformed call log %s at token %d: %q", path, i, tokens[i:])
		}
		calls = append(calls, tokens[i+1:i+1+n])
		i += 1 + n
	}
	return calls
}

// newWindowCalls returns the recorded tmux `new-window` calls.
func (w *adapterLaunchWorld) newWindowCalls(t *testing.T) [][]string {
	t.Helper()
	var out [][]string
	for _, c := range adapterLaunchCalls(t, w.tmuxLog) {
		if len(c) > 0 && c[0] == "new-window" {
			out = append(out, c)
		}
	}
	return out
}

// adapterLaunchFields are the five State fields AC 2 pins, by JSON name.
type adapterLaunchFields struct {
	FixAttempts     int      `json:"ciFixAttempts"`
	RepairPending   bool     `json:"ciRepairPending"`
	LastRepairedSHA string   `json:"lastRepairedCiHeadSha"`
	PendingKeys     []string `json:"pendingCommentKeys"`
	LaunchedKeys    []string `json:"launchedFeedbackKeys"`
}

// readFields decodes the five fields from the state file and asserts the state
// dir holds exactly that one *.json, so a drift in statePath's naming convention
// (which would leave the seeded file untouched and the run writing elsewhere)
// fails instead of comparing a stale file.
func (w *adapterLaunchWorld) readFields(t *testing.T) adapterLaunchFields {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(w.stateDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != w.statePath() {
		t.Fatalf("state dir holds %v, want exactly [%s]", files, w.statePath())
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var f adapterLaunchFields
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("decode %s: %v", files[0], err)
	}
	return f
}

// The installed, enabled plugin carries the adapter: the supervisor launches
// ci-repair through `cenci run`, the window argv invokes $cenci:ci-repair, the
// probe asked fake codex for the plugin list, and the counters advance.
func TestBabysitAdapterLaunch_InstalledPluginSpawnsCiRepairWindow(t *testing.T) {
	w := newAdapterLaunchWorld(t, adapterLaunchPluginList(true), true)
	w.seedState(t, false)

	stderr, code := w.babysitOnce(t, "codex")
	if code != 0 {
		t.Fatalf("cenci babysit --once exit = %d, want 0; stderr: %s", code, stderr)
	}

	windows := w.newWindowCalls(t)
	if len(windows) != 1 {
		t.Fatalf("tmux new-window calls = %v, want exactly one", windows)
	}
	newWindow := windows[0]
	// tmux new-window -d -t =<session>: -n <name> <shell command>
	if !reflect.DeepEqual(newWindow[:3], []string{"new-window", "-d", "-t"}) || newWindow[3] != "="+adapterLaunchSession+":" {
		t.Errorf("new-window argv = %q, want it to target the recorded session %q", newWindow, adapterLaunchSession)
	}
	shellCommand := newWindow[len(newWindow)-1]
	if !strings.HasPrefix(shellCommand, "cd ") || !strings.Contains(shellCommand, w.workDir) {
		t.Errorf("window command %q does not start in the recorded --dir %q", shellCommand, w.workDir)
	}
	argv := exectest.ShellArgv(t, shellCommand, "codex")
	if len(argv) != 2 || argv[0] != "codex" || !strings.HasPrefix(argv[1], "$cenci:ci-repair ") {
		t.Errorf("window argv = %q, want [codex, \"$cenci:ci-repair ...\"]", argv)
	}

	// Fake codex logs one "$*" line per call.
	raw, err := os.ReadFile(w.codexLog)
	if err != nil {
		t.Fatalf("fake codex was never invoked, so the installed plugin was not probed: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "plugin list --json" {
		t.Errorf("fake codex calls = %q, want exactly %q", got, "plugin list --json")
	}

	got := w.readFields(t)
	want := adapterLaunchFields{FixAttempts: 2, RepairPending: true, LastRepairedSHA: "abc"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("state after a successful launch = %+v, want %+v", got, want)
	}
}

// A refusal before spawn leaves the five repair fields as seeded, spawns no
// window, and its text reaches the supervisor's output. Whole-file equality is
// not asserted: a failed --once tick still re-saves status, updatedAt and the
// backoff delay (internal/babysit Run), which the ticket's Decisions keep.
func TestBabysitAdapterLaunch_RefusalSpawnsNoWindowAndKeepsRepairState(t *testing.T) {
	for _, tc := range []struct {
		name           string
		pluginList     string
		installAdapter bool
		wantMarker     string
	}{
		{
			name:           "adapter missing from the installed plugin",
			pluginList:     adapterLaunchPluginList(true),
			installAdapter: false,
			wantMarker:     "is missing from the installed plugin root",
		},
		{
			name:           "plugin disabled",
			pluginList:     adapterLaunchPluginList(false),
			installAdapter: true,
			wantMarker:     "is disabled in Codex",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newAdapterLaunchWorld(t, tc.pluginList, tc.installAdapter)
			w.seedState(t, true)
			seeded := w.readFields(t)
			wantSeeded := adapterLaunchFields{
				FixAttempts: 1, RepairPending: true, LastRepairedSHA: adapterLaunchStale,
				PendingKeys: []string{"comment:5", "comment:6"}, LaunchedKeys: []string{"comment:5"},
			}
			if !reflect.DeepEqual(seeded, wantSeeded) {
				t.Fatalf("seeded fields = %+v, want %+v (the unchanged check would be vacuous)", seeded, wantSeeded)
			}

			stderr, code := w.babysitOnce(t, "codex")

			if code != 1 {
				t.Errorf("cenci babysit --once exit = %d, want 1 (a failed launch); stderr: %s", code, stderr)
			}
			for _, want := range []string{"codex adapter unavailable", tc.wantMarker, "cenci@cenci", `"ci-repair"`, "codex plugin add cenci@cenci"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("supervisor stderr missing %q: %s", want, stderr)
				}
			}
			if windows := w.newWindowCalls(t); len(windows) != 0 {
				t.Errorf("a refused launch spawned tmux windows: %v", windows)
			}
			raw, err := os.ReadFile(w.codexLog)
			if err != nil {
				t.Fatalf("fake codex was never invoked, so the refusal did not come from the probe: %v", err)
			}
			if got := strings.TrimSpace(string(raw)); got != "plugin list --json" {
				t.Errorf("fake codex calls = %q, want exactly %q", got, "plugin list --json")
			}
			if after := w.readFields(t); !reflect.DeepEqual(after, seeded) {
				t.Errorf("a refused launch changed repair state:\n got %+v\nwant %+v", after, seeded)
			}
		})
	}
}

// A disabled agent is a usage error raised before any state write: exit 2, no
// window, no gh call, and the pre-seeded state file is byte-identical.
func TestBabysitAdapterLaunch_DisabledAgentLeavesStateByteIdentical(t *testing.T) {
	w := newAdapterLaunchWorld(t, adapterLaunchPluginList(true), true)
	seeded := w.seedState(t, true)

	stderr, code := w.babysitOnce(t, "opencode")

	if code != 2 {
		t.Errorf("cenci babysit --agent opencode exit = %d, want 2; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "OpenCode is temporarily disabled") {
		t.Errorf("stderr missing the disabled-agent refusal: %q", stderr)
	}
	if windows := w.newWindowCalls(t); len(windows) != 0 {
		t.Errorf("a disabled agent spawned tmux windows: %v", windows)
	}
	if calls := adapterLaunchCalls(t, w.ghLog); len(calls) != 0 {
		t.Errorf("a disabled agent reached babysit.Run; gh was invoked: %v", calls)
	}
	after, err := os.ReadFile(w.statePath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, seeded) {
		t.Errorf("a disabled agent rewrote the state file:\n got %s\nwant %s", after, seeded)
	}
	_ = w.readFields(t) // exactly one *.json: the seeded path is the real one
}
