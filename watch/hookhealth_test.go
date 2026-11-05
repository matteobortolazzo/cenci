package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/hookhealth"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// Hook-health diagnostics (#1239), end to end. The Go-writer categories
// (malformed input, unavailable socket, delivery failure) are driven through
// the real hooks.json command of both clients — `sh -c '<wrapper> 2>/dev/null
// || true'` — with the shim pattern of runCodexManifestHook, and read back
// through the real `cenci daemon hook-health` reporter.

var hookHealthRecordRE = regexp.MustCompile(`^[0-9]+ (claude|codex)\n$`)

type hookHealthClient struct {
	name    string
	dir     string // plugin/<dir>/ holds hooks.json and notify.sh
	rootEnv string // env var the manifest command expands to the plugin root
}

var hookHealthClients = []hookHealthClient{
	{"claude", "hooks", "CLAUDE_PLUGIN_ROOT"},
	{"codex", "codex", "PLUGIN_ROOT"},
}

// hookHealthCases are the client/event pairs driven: UserPromptSubmit has the
// 5 s timeout of most hooks, Codex Interrupt the short 3 s one.
var hookHealthCases = []struct {
	client hookHealthClient
	event  string
}{
	{hookHealthClients[0], "UserPromptSubmit"},
	{hookHealthClients[1], "UserPromptSubmit"},
	{hookHealthClients[1], "Interrupt"},
}

const hookHealthValidPayload = `{"hook_event_name":"UserPromptSubmit","session_id":"hh-session","prompt":"hello"}`

// hookHealthStdin is a hook's stdin: literal text, or (dir set) an open
// directory, which makes io.ReadAll fail with EISDIR.
type hookHealthStdin struct {
	text string
	dir  string
}

type hookHealthRun struct {
	stdout, stderr string
	err            error
	elapsed        time.Duration
}

type hookHealthHook struct {
	command string
	timeout time.Duration
	env     []string
}

// newHookHealthHook builds a temp plugin root holding the real notify.sh and a
// shim that points the test binary at socket, and returns the manifest's real
// command for event with an isolated environment (store, socket dir).
func newHookHealthHook(t *testing.T, c hookHealthClient, event, socket, stateHome string) *hookHealthHook {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("plugin", c.dir, "hooks.json"))
	if err != nil {
		t.Fatalf("read %s hooks.json: %v", c.name, err)
	}
	var manifest struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse %s hooks.json: %v", c.name, err)
	}
	groups := manifest.Hooks[event]
	if len(groups) != 1 || len(groups[0].Hooks) != 1 {
		t.Fatalf("expected exactly one %s %s hook, got %+v", c.name, event, groups)
	}
	hook := groups[0].Hooks[0]

	root := t.TempDir()
	wrapper, err := os.ReadFile(filepath.Join("plugin", c.dir, "notify.sh"))
	if err != nil {
		t.Fatalf("read %s notify.sh: %v", c.name, err)
	}
	if err := os.MkdirAll(filepath.Join(root, c.dir), 0o755); err != nil {
		t.Fatalf("create wrapper dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, c.dir, "notify.sh"), wrapper, 0o755); err != nil {
		t.Fatalf("write notify.sh copy: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatalf("create plugin bin dir: %v", err)
	}
	shim := "#!/bin/sh\nexec \"$CENCI_TEST_BIN\" \"$@\" -event-socket \"$CENCI_TEST_SOCKET\"\n"
	if err := os.WriteFile(filepath.Join(root, "bin", "cenci"), []byte(shim), 0o755); err != nil {
		t.Fatalf("write cenci shim: %v", err)
	}
	socketDir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatalf("create socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })

	return &hookHealthHook{
		command: hook.Command,
		timeout: time.Duration(hook.Timeout) * time.Second,
		env: []string{
			c.rootEnv + "=" + root,
			"CENCI_TEST_BIN=" + binaryPath,
			"CENCI_TEST_SOCKET=" + socket,
			"CENCI_SANDBOX=",
			"TMUX_PANE=",
			"XDG_STATE_HOME=" + stateHome,
			// A wrapper fallback to an installed cenci must never reach the real daemon.
			"CENCI_SOCKET_DIR=" + socketDir,
		},
	}
}

// run executes the manifest command. It never calls t.Fatal, so it is safe
// from goroutines.
func (h *hookHealthHook) run(in hookHealthStdin) hookHealthRun {
	cmd := exec.Command("sh", "-c", h.command)
	cmd.Env = append(os.Environ(), h.env...)
	if in.dir != "" {
		f, err := os.Open(in.dir)
		if err != nil {
			return hookHealthRun{err: err}
		}
		defer func() { _ = f.Close() }()
		cmd.Stdin = f
	} else {
		cmd.Stdin = strings.NewReader(in.text)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	return hookHealthRun{stdout: stdout.String(), stderr: stderr.String(), err: err, elapsed: time.Since(start)}
}

// requireSilentExitZero asserts the hooks' contract under failure: exit 0, no
// stdout or stderr, and back within the manifest timeout.
func (h *hookHealthHook) requireSilentExitZero(t *testing.T, r hookHealthRun) {
	t.Helper()
	if r.err != nil {
		t.Errorf("hook exited non-zero: %v", r.err)
	}
	if r.stdout != "" || r.stderr != "" {
		t.Errorf("hook must stay silent, got stdout=%q stderr=%q", r.stdout, r.stderr)
	}
	if r.elapsed >= h.timeout {
		t.Errorf("hook took %v, want less than its %v timeout", r.elapsed, h.timeout)
	}
}

// hookHealthStore reads every entry of the hook-health store under stateHome
// (dotfiles included), failing on anything that is not a 0600 regular file. A
// missing store is an empty map.
func hookHealthStore(t *testing.T, stateHome string) map[string]string {
	t.Helper()
	dir := filepath.Join(stateHome, "cenci", "hook-health")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}
	}
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	store := map[string]string{}
	for _, entry := range entries {
		info, err := os.Lstat(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("stat %s: %v", entry.Name(), err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode = %v, want a regular 0600 file", entry.Name(), info.Mode())
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		store[entry.Name()] = string(data)
	}
	return store
}

func requireOnlyRecords(t *testing.T, store map[string]string, client string, want ...hookhealth.Category) {
	t.Helper()
	if len(store) != len(want) {
		t.Fatalf("store = %v, want exactly %v", store, want)
	}
	for _, category := range want {
		content, ok := store[string(category)]
		if !ok {
			t.Fatalf("store = %v, missing %s", store, category)
		}
		if !hookHealthRecordRE.MatchString(content) || !strings.HasSuffix(content, " "+client+"\n") {
			t.Errorf("%s = %q, want '<epoch> %s'", category, content, client)
		}
	}
}

// hookHealthSocketNoListener is a socket path nothing listens on.
func hookHealthSocketNoListener(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatalf("create socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "none.sock")
}

// hookHealthSocketLive returns a socket with a running receiver.
func hookHealthSocketLive(t *testing.T) (string, *ipc.EventReceiver) {
	t.Helper()
	socket := hookHealthSocketNoListener(t)
	receiver, err := ipc.NewEventReceiver(socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go receiver.Accept(ctx)
	return socket, receiver
}

// hookHealthSocketDenied returns a live socket whose file has mode 000, so a
// dial fails with EACCES: a delivery failure, not an unavailable socket. It
// skips when this process can dial it anyway (root).
func hookHealthSocketDenied(t *testing.T) string {
	t.Helper()
	socket, _ := hookHealthSocketLive(t)
	if err := os.Chmod(socket, 0); err != nil {
		t.Fatalf("chmod socket: %v", err)
	}
	conn, err := net.Dial("unix", socket)
	if err == nil {
		_ = conn.Close()
		t.Skip("socket file mode 000 is still dialable here (running as root?)")
	}
	if !errors.Is(err, syscall.EACCES) {
		t.Skipf("mode-000 socket dial failed with %v, not EACCES", err)
	}
	return socket
}

// hookHealthCLI runs `cenci daemon hook-health args...` against stateHome.
func hookHealthCLI(t *testing.T, stateHome string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	cmd := exec.Command(binaryPath, append([]string{"daemon", "hook-health"}, args...)...)
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+stateHome)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		exit = exitErr.ExitCode()
	default:
		t.Fatalf("run hook-health: %v", err)
	}
	return out.String(), errBuf.String(), exit
}

func hookHealthFirstLine(out string) string {
	line, _, _ := strings.Cut(out, "\n")
	return line
}

// seedHookHealth writes a valid record aged by age into stateHome's store.
func seedHookHealth(t *testing.T, stateHome string, category hookhealth.Category, client string, age time.Duration) {
	t.Helper()
	root := filepath.Join(stateHome, "cenci")
	dir := filepath.Join(root, "hook-health")
	for _, d := range []string{root, dir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("create %s: %v", d, err)
		}
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatalf("chmod %s: %v", d, err)
		}
	}
	content := strconv.FormatInt(time.Now().Add(-age).Unix(), 10) + " " + client + "\n"
	if err := os.WriteFile(filepath.Join(dir, string(category)), []byte(content), 0o600); err != nil {
		t.Fatalf("seed %s: %v", category, err)
	}
}

// TestHookHealthGoWritersRecordThroughRealHookCommand covers AC1 and AC2 for
// the Go-writer categories: after the real outer `2>/dev/null || true` hook
// command, exactly the expected record exists, the hook exited 0, printed
// nothing and returned inside its timeout.
func TestHookHealthGoWritersRecordThroughRealHookCommand(t *testing.T) {
	scenarios := []struct {
		name   string
		stdin  func(t *testing.T) hookHealthStdin
		socket func(t *testing.T) string
		want   hookhealth.Category
	}{
		{"malformed JSON", func(*testing.T) hookHealthStdin { return hookHealthStdin{text: `{"hook_event_name": nope`} },
			hookHealthSocketNoListener, hookhealth.MalformedInput},
		{"stdin is a directory", func(t *testing.T) hookHealthStdin { return hookHealthStdin{dir: t.TempDir()} },
			hookHealthSocketNoListener, hookhealth.MalformedInput},
		{"socket without a listener", func(*testing.T) hookHealthStdin { return hookHealthStdin{text: hookHealthValidPayload} },
			hookHealthSocketNoListener, hookhealth.SocketUnavailable},
		{"socket file denies access", func(*testing.T) hookHealthStdin { return hookHealthStdin{text: hookHealthValidPayload} },
			hookHealthSocketDenied, hookhealth.DeliveryFailure},
	}
	for _, tc := range scenarios {
		for _, hc := range hookHealthCases {
			t.Run(tc.name+"/"+hc.client.name+"/"+hc.event, func(t *testing.T) {
				stateHome := t.TempDir()
				stdin := tc.stdin(t)
				hook := newHookHealthHook(t, hc.client, hc.event, tc.socket(t), stateHome)

				r := hook.run(stdin)

				hook.requireSilentExitZero(t, r)
				requireOnlyRecords(t, hookHealthStore(t, stateHome), hc.client.name, tc.want)
			})
		}
	}
}

// TestHookHealthRecordsHoldNoPayloadOrEnvironmentSecrets covers AC3: prompt,
// tool-argument and transcript-path sentinels in the payload and API-key and
// token sentinels in the environment never reach the store, and every record
// is the strict closed-set shape.
func TestHookHealthRecordsHoldNoPayloadOrEnvironmentSecrets(t *testing.T) {
	sentinels := []string{
		"SENTINEL-PROMPT-7f3a",
		"SENTINEL-TOOL-ARG-9b1c",
		"/SENTINEL/transcript-4d2e.jsonl",
		"sk-ant-SENTINEL-KEY-31",
		"ghp_SENTINELTOKEN55",
	}
	body := `"hook_event_name":"UserPromptSubmit","session_id":"hh-secret",` +
		`"prompt":"` + sentinels[0] + `","tool_input":{"command":"` + sentinels[1] + `"},` +
		`"transcript_path":"` + sentinels[2] + `"`
	payloads := map[string]string{
		"malformed payload":      `{` + body, // missing closing brace
		"unreachable-socket run": `{` + body + `}`,
	}
	for name, payload := range payloads {
		for _, hc := range hookHealthCases {
			t.Run(name+"/"+hc.client.name+"/"+hc.event, func(t *testing.T) {
				stateHome := t.TempDir()
				hook := newHookHealthHook(t, hc.client, hc.event, hookHealthSocketNoListener(t), stateHome)
				hook.env = append(hook.env, "ANTHROPIC_API_KEY="+sentinels[3], "GH_TOKEN="+sentinels[4])

				hook.requireSilentExitZero(t, hook.run(hookHealthStdin{text: payload}))

				allowed := map[string]bool{}
				for _, c := range hookhealth.Categories() {
					allowed[filepath.Join("cenci", "hook-health", string(c))] = true
				}
				files := 0
				err := filepath.WalkDir(stateHome, func(path string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					rel, _ := filepath.Rel(stateHome, path)
					for _, s := range sentinels {
						if strings.Contains(rel, s) {
							t.Errorf("sentinel %q in store path %s", s, rel)
						}
					}
					if d.IsDir() {
						return nil
					}
					files++
					if !allowed[rel] {
						t.Errorf("unexpected store entry %s", rel)
					}
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					if !hookHealthRecordRE.Match(data) {
						t.Errorf("%s = %q, want the strict '<epoch> <client>' record", rel, data)
					}
					for _, s := range sentinels {
						if bytes.Contains(data, []byte(s)) {
							t.Errorf("sentinel %q leaked into %s", s, rel)
						}
					}
					return nil
				})
				if err != nil {
					t.Fatalf("walk store: %v", err)
				}
				if files == 0 {
					t.Fatal("no record was written, the sentinel check proves nothing")
				}
			})
		}
	}
}

// TestHookHealthRepeatedFailuresKeepStoreBounded covers AC4: many repeated
// failures leave one record per category with unchanged content and size.
func TestHookHealthRepeatedFailuresKeepStoreBounded(t *testing.T) {
	for _, hc := range hookHealthCases {
		t.Run(hc.client.name+"/"+hc.event, func(t *testing.T) {
			stateHome := t.TempDir()
			hook := newHookHealthHook(t, hc.client, hc.event, hookHealthSocketNoListener(t), stateHome)
			malformed := hookHealthStdin{text: "{broken"}
			unavailable := hookHealthStdin{text: hookHealthValidPayload}

			hook.requireSilentExitZero(t, hook.run(malformed))
			hook.requireSilentExitZero(t, hook.run(unavailable))
			first := hookHealthStore(t, stateHome)
			requireOnlyRecords(t, first, hc.client.name, hookhealth.MalformedInput, hookhealth.SocketUnavailable)

			for i := 0; i < 15; i++ {
				hook.requireSilentExitZero(t, hook.run(malformed))
				hook.requireSilentExitZero(t, hook.run(unavailable))
			}

			after := hookHealthStore(t, stateHome)
			if len(after) != len(first) {
				t.Fatalf("store grew from %v to %v", first, after)
			}
			for name, content := range first {
				if after[name] != content {
					t.Errorf("%s changed from %q to %q within the 60 s rate limit", name, content, after[name])
				}
			}
		})
	}
}

// TestHookHealthConcurrentRunsLeaveParseableRecords covers AC4's parallel
// writers: concurrent hook processes leave only valid records and no stray
// temp files.
func TestHookHealthConcurrentRunsLeaveParseableRecords(t *testing.T) {
	for _, hc := range hookHealthCases {
		t.Run(hc.client.name+"/"+hc.event, func(t *testing.T) {
			stateHome := t.TempDir()
			hook := newHookHealthHook(t, hc.client, hc.event, hookHealthSocketNoListener(t), stateHome)
			inputs := []hookHealthStdin{{text: "{broken"}, {text: hookHealthValidPayload}}

			const runs = 20
			results := make([]hookHealthRun, runs)
			var wg sync.WaitGroup
			for i := 0; i < runs; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					results[i] = hook.run(inputs[i%len(inputs)])
				}()
			}
			wg.Wait()

			for _, r := range results {
				hook.requireSilentExitZero(t, r)
			}
			requireOnlyRecords(t, hookHealthStore(t, stateHome), hc.client.name, hookhealth.MalformedInput, hookhealth.SocketUnavailable)
		})
	}
}

// TestHookHealthRecoveryAfterDelivery covers AC5 end to end: a failure through
// the real hook, then a delivery to a live receiver, makes the reporter print
// recovered.
func TestHookHealthRecoveryAfterDelivery(t *testing.T) {
	for _, hc := range hookHealthCases {
		t.Run(hc.client.name+"/"+hc.event, func(t *testing.T) {
			stateHome := t.TempDir()
			failing := newHookHealthHook(t, hc.client, hc.event, hookHealthSocketNoListener(t), stateHome)
			failing.requireSilentExitZero(t, failing.run(hookHealthStdin{text: hookHealthValidPayload}))

			out, _, exit := hookHealthCLI(t, stateHome)
			if exit != 0 || hookHealthFirstLine(out) != "hook health: current-failure" {
				t.Fatalf("after the failure: exit %d, output %q, want current-failure", exit, out)
			}

			// last-ok must be strictly newer than the failure's epoch second.
			time.Sleep(1100 * time.Millisecond)
			socket, receiver := hookHealthSocketLive(t)
			delivering := newHookHealthHook(t, hc.client, hc.event, socket, stateHome)
			delivering.requireSilentExitZero(t, delivering.run(hookHealthStdin{text: hookHealthValidPayload}))
			select {
			case <-receiver.Events():
			case <-time.After(2 * time.Second):
				t.Fatal("the delivery never reached the receiver")
			}

			out, _, exit = hookHealthCLI(t, stateHome)
			if exit != 0 || hookHealthFirstLine(out) != "hook health: recovered" {
				t.Fatalf("after the delivery: exit %d, output %q, want recovered", exit, out)
			}
		})
	}
}

// TestHookHealthSameSecondTieStaysCurrentFailureUntilNextSuccess covers AC5's
// tie rule: a failure and a last-ok sharing a second never read as health, and
// the next successful delivery resolves it.
func TestHookHealthSameSecondTieStaysCurrentFailureUntilNextSuccess(t *testing.T) {
	stateHome := t.TempDir()
	seedHookHealth(t, stateHome, hookhealth.DeliveryFailure, "claude", 5*time.Second)
	seedHookHealth(t, stateHome, hookhealth.LastOK, "claude", 5*time.Second)

	out, _, exit := hookHealthCLI(t, stateHome)
	if exit != 0 || hookHealthFirstLine(out) != "hook health: current-failure" {
		t.Fatalf("tied records: exit %d, output %q, want current-failure", exit, out)
	}

	socket, receiver := hookHealthSocketLive(t)
	hook := newHookHealthHook(t, hookHealthClients[0], "UserPromptSubmit", socket, stateHome)
	hook.requireSilentExitZero(t, hook.run(hookHealthStdin{text: hookHealthValidPayload}))
	select {
	case <-receiver.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("the delivery never reached the receiver")
	}

	out, _, exit = hookHealthCLI(t, stateHome)
	if exit != 0 || hookHealthFirstLine(out) != "hook health: recovered" {
		t.Fatalf("after the next success: exit %d, output %q, want recovered", exit, out)
	}
}

// TestHookHealthReporterStates covers AC6's CLI: each seeded store prints its
// first-line token, exits 0, and the reporter creates nothing.
func TestHookHealthReporterStates(t *testing.T) {
	hint := hookhealth.SocketUnavailable.Hint()
	cases := []struct {
		name     string
		seed     func(t *testing.T, stateHome string)
		wantLine string
		contains []string
	}{
		{"no store", func(*testing.T, string) {}, "hook health: no-evidence", []string{"not proof of health"}},
		{"only stale failure", func(t *testing.T, s string) {
			seedHookHealth(t, s, hookhealth.DeliveryFailure, "codex", 25*time.Hour)
		}, "hook health: no-evidence", []string{"not proof of health"}},
		{"only last-ok", func(t *testing.T, s string) {
			seedHookHealth(t, s, hookhealth.LastOK, "claude", 30*time.Second)
		}, "hook health: healthy", nil},
		{"failure then newer last-ok", func(t *testing.T, s string) {
			seedHookHealth(t, s, hookhealth.DeliveryFailure, "claude", 2*time.Minute)
			seedHookHealth(t, s, hookhealth.LastOK, "claude", 30*time.Second)
		}, "hook health: recovered", nil},
		{"current failure shows category and hint", func(t *testing.T, s string) {
			seedHookHealth(t, s, hookhealth.SocketUnavailable, "codex", 30*time.Second)
		}, "hook health: current-failure", []string{"socket-unavailable", "codex", hint}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stateHome := t.TempDir()
			tc.seed(t, stateHome)
			before := dirSnapshot(t, stateHome)

			out, stderr, exit := hookHealthCLI(t, stateHome)

			if exit != 0 {
				t.Errorf("exit = %d, want 0 (stderr %q)", exit, stderr)
			}
			if got := hookHealthFirstLine(out); got != tc.wantLine {
				t.Errorf("first line = %q, want %q (output %q)", got, tc.wantLine, out)
			}
			for _, want := range tc.contains {
				if !strings.Contains(out, want) {
					t.Errorf("output %q does not contain %q", out, want)
				}
			}
			if after := dirSnapshot(t, stateHome); after != before {
				t.Errorf("the reporter changed the store:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

func dirSnapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %v %d %d\n", path, info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return b.String()
}

// TestHookHealthReporterRejectsExtraArguments pins the usage-error contract.
func TestHookHealthReporterRejectsExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"extra"}, {"-x"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, stderr, exit := hookHealthCLI(t, t.TempDir(), args...)
			if exit != 2 {
				t.Errorf("exit = %d, want 2 (stderr %q)", exit, stderr)
			}
			if stderr == "" {
				t.Error("a usage error must explain itself on stderr")
			}
		})
	}
}

// TestHookHealthOpenCodeNeverRecords pins that agents outside claude and codex
// (OpenCode stays disabled, #1219) leave no record, whatever fails.
func TestHookHealthOpenCodeNeverRecords(t *testing.T) {
	for name, stdin := range map[string]string{
		"malformed input":    "{broken",
		"unavailable socket": hookHealthValidPayload,
	} {
		t.Run(name, func(t *testing.T) {
			stateHome := t.TempDir()
			cmd := exec.Command(binaryPath, "notify", "-agent", "opencode", "-event-socket", hookHealthSocketNoListener(t))
			cmd.Stdin = strings.NewReader(stdin)
			cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+stateHome, "CENCI_SANDBOX=", "TMUX_PANE=")
			if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
				t.Fatalf("notify: err %v, output %q, want exit 0 and silence", err, out)
			}
			// Only the hook-health store is checked: resolving the default
			// socket dir may create $XDG_STATE_HOME/cenci/run when
			// CENCI_SOCKET_DIR is unset, which is not a diagnostic record.
			store := filepath.Join(stateHome, "cenci", "hook-health")
			if _, err := os.Lstat(store); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("an opencode run created the hook-health store %s (stat err = %v)", store, err)
			}
		})
	}
}
