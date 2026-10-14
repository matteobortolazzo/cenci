package run

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
)

// mockCtrl records launcher calls for assertions.
type mockCtrl struct {
	session      string
	sessionErr   error
	grouped      bool
	groupedErr   error
	newWindowErr error
	// optErr, when set, is returned by every SetWindowOption call -- the
	// post-NewWindow failure ErrWindowSpawned wraps (#853).
	optErr error
	// optErrByKey fails SetWindowOption for one option key only (#1250); a key
	// present here wins over the optErr catch-all, and a nil value there makes
	// that key succeed.
	optErrByKey map[string]error
	// hasSession/hasSessionErr are HasSession's canned return (#927). Run
	// itself never calls HasSession (that's dispatch's per-repo gate, not the
	// interactive `cenci run` path), so no existing Run test depends on these;
	// they exist so mockCtrl keeps satisfying run.Controller.
	hasSession    bool
	hasSessionErr error

	windows         []winCall
	options         []optCall
	groupedSessions []string
}

type winCall struct{ session, name, cmd string }
type optCall struct{ target, key, value string }

func (m *mockCtrl) CurrentSession() (string, error) { return m.session, m.sessionErr }
func (m *mockCtrl) IsGroupedSession(session string) (bool, error) {
	m.groupedSessions = append(m.groupedSessions, session)
	return m.grouped, m.groupedErr
}
func (m *mockCtrl) NewWindow(session, name, cmd string) error {
	m.windows = append(m.windows, winCall{session, name, cmd})
	return m.newWindowErr
}
func (m *mockCtrl) SetWindowOption(target, key, value string) error {
	m.options = append(m.options, optCall{target, key, value})
	if err, ok := m.optErrByKey[key]; ok {
		return err
	}
	return m.optErr
}
func (m *mockCtrl) HasSession(string) (bool, error) { return m.hasSession, m.hasSessionErr }

// noConfigOpts returns Opts pinned to a non-existent config (built-ins only),
// so tests are deterministic.
func noConfigOpts(t *testing.T) Opts {
	t.Helper()
	return Opts{
		ConfigPath: filepath.Join(t.TempDir(), "none.json"),
		// Stub the daemon hook: the real daemon.EnsureRunning spawns
		// os.Executable(), which inside `go test` is this test binary —
		// every spawned child re-runs the suite and spawns more children
		// (a fork bomb, masked in sandboxes where CENCI_SANDBOX=1 short-
		// circuits EnsureRunning). Tests asserting the hook override this.
		EnsureDaemon: func() {},
	}
}

func TestRunSpawnsWindowAndPinsName(t *testing.T) {
	m := &mockCtrl{session: "work"}
	opts := noConfigOpts(t)
	// A numeric ticket names the window `<number>-<skill>`; --slug is ignored.
	opts.Workflow, opts.Ticket, opts.Slug = "implement", "40", "demo"

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(m.windows) != 1 {
		t.Fatalf("expected 1 NewWindow, got %d", len(m.windows))
	}
	w := m.windows[0]
	if len(m.groupedSessions) != 1 || m.groupedSessions[0] != "=work" {
		t.Errorf("grouped-session targets = %q, want [=work]", m.groupedSessions)
	}
	if w.session != "=work" {
		t.Errorf("session = %q, want =work", w.session)
	}
	if w.name != "40-implement" {
		t.Errorf("name = %q, want 40-implement", w.name)
	}
	// With no flag and no config, the default is now the sandbox launcher (#98).
	if !strings.Contains(w.cmd, "cenci open") || !strings.Contains(w.cmd, "/cenci:implement 40") {
		t.Errorf("command = %q", w.cmd)
	}

	found := false
	for _, o := range m.options {
		if o.target == "=work:=40-implement" && o.key == "automatic-rename" && o.value == "off" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected automatic-rename off on =work:=40-implement, got %+v", m.options)
	}
}

func TestRunTreatsLeadingEqualsAsPartOfRawSessionName(t *testing.T) {
	m := &mockCtrl{}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Session = "implement", "40", "=work"

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(m.groupedSessions) != 1 || m.groupedSessions[0] != "==work" {
		t.Errorf("grouped-session targets = %q, want [==work]", m.groupedSessions)
	}
	if len(m.windows) != 1 || m.windows[0].session != "==work" {
		t.Errorf("window calls = %+v, want the raw name =work encoded as exact target ==work", m.windows)
	}
	// automatic-rename off, then the repo-dir option (#1250): both must carry
	// the exact target.
	if len(m.options) != 2 {
		t.Fatalf("option calls = %+v, want automatic-rename and the repo-dir option", m.options)
	}
	for _, o := range m.options {
		if o.target != "==work:=40-implement" {
			t.Errorf("option call %+v, want exact target ==work:=40-implement", o)
		}
	}
}

// TestRunWindowTicketNamesWindowIndependentOfTicket guards the dispatch path:
// dispatch passes a plan-file path as Ticket (the implement skill's positional
// argument) but must still get the uniform `<number>-implement` join key that
// Lazyboards and dispatch's own matching join on. WindowTicket carries the
// numeric identity for naming only, without disturbing the command argument.
func TestRunWindowTicketNamesWindowIndependentOfTicket(t *testing.T) {
	m := &mockCtrl{session: "work"}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.WindowTicket = "implement", ".plans/42-x.md", "42"

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(m.windows) != 1 {
		t.Fatalf("expected 1 NewWindow, got %d", len(m.windows))
	}
	w := m.windows[0]
	// The join key comes from WindowTicket, not the (non-numeric) plan path.
	if w.name != "42-implement" {
		t.Errorf("name = %q, want 42-implement", w.name)
	}
	// The skill argument still comes from Ticket — the plan path must survive.
	if !strings.Contains(w.cmd, ".plans/42-x.md") {
		t.Errorf("command must carry the plan path from Ticket, got %q", w.cmd)
	}

	found := false
	for _, o := range m.options {
		if o.target == "=work:=42-implement" && o.key == "automatic-rename" && o.value == "off" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected automatic-rename off on =work:=42-implement, got %+v", m.options)
	}
}

func TestRunDefaultsToSandbox(t *testing.T) {
	m := &mockCtrl{session: "work"}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket = "implement", "40"

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(m.windows) != 1 {
		t.Fatalf("expected 1 NewWindow, got %d", len(m.windows))
	}
	// No flag and no config default → sandbox launcher (#98).
	if !strings.Contains(m.windows[0].cmd, "cenci open") {
		t.Errorf("command = %q, want sandbox launcher", m.windows[0].cmd)
	}
}

func TestRunNoSandboxForcesHost(t *testing.T) {
	m := &mockCtrl{session: "work"}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket = "implement", "40"
	// Mirrors `--no-sandbox`: flag is set, sandbox is false.
	opts.SandboxSet = true
	opts.Sandbox = false

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(m.windows) != 1 {
		t.Fatalf("expected 1 NewWindow, got %d", len(m.windows))
	}
	cmd := m.windows[0].cmd
	if strings.Contains(cmd, "cenci open") {
		t.Errorf("--no-sandbox must not use the sandbox launcher: %q", cmd)
	}
	if !strings.Contains(cmd, "claude") {
		t.Errorf("command = %q, want host claude launcher", cmd)
	}
}

func TestRunMergeRepairExplicitClaudeOpusOverridesDefaults(t *testing.T) {
	for _, defaultAgent := range []string{"claude", "codex", "opencode"} {
		for _, sandbox := range []bool{false, true} {
			runtime := "host"
			wantCommand := "claude --model opus --print -- '/cenci:merge-repair 42'"
			if sandbox {
				runtime = "sandbox"
				wantCommand = "cenci open --model opus -- --print '/cenci:merge-repair 42'"
			}
			t.Run(defaultAgent+"/"+runtime, func(t *testing.T) {
				opts := noConfigOpts(t)
				config := `{"defaultAgent":"` + defaultAgent + `","agents":{"claude":{"model":"sonnet"}}}`
				if err := os.WriteFile(opts.ConfigPath, []byte(config), 0o600); err != nil {
					t.Fatal(err)
				}
				opts.Workflow, opts.Ticket = "merge-repair", "42"
				opts.Agent, opts.Model = "claude", "opus"
				opts.SandboxSet, opts.Sandbox = true, sandbox
				ctrl := &mockCtrl{session: "work"}

				if err := Run(opts, ctrl); err != nil {
					t.Fatalf("Run: %v", err)
				}
				if len(ctrl.windows) != 1 {
					t.Fatalf("windows = %v, want one merge-repair window", ctrl.windows)
				}
				window := ctrl.windows[0]
				if window.name != "42-merge-repair" || window.session != "=work" {
					t.Errorf("window = %+v, want 42-merge-repair in =work", window)
				}
				if window.cmd != wantCommand {
					t.Errorf("command = %q, want %q", window.cmd, wantCommand)
				}
			})
		}
	}
}

func TestRunMergeRepairTemplateOverridesPreserveOtherRuntime(t *testing.T) {
	for _, tc := range []struct {
		name        string
		override    string
		wantHost    string
		wantSandbox string
	}{
		{
			name:        "host args only",
			override:    `{"args":["--print","--","/repair-native {ticket}"]}`,
			wantHost:    "claude --model opus --print -- '/repair-native 42'",
			wantSandbox: "cenci open --model opus -- --print '/cenci:merge-repair 42'",
		},
		{
			name:        "sandbox args only",
			override:    `{"sandboxArgs":["--","--print","/repair-sandbox {ticket}"]}`,
			wantHost:    "claude --model opus --print -- '/cenci:merge-repair 42'",
			wantSandbox: "cenci open --model opus -- --print '/repair-sandbox 42'",
		},
		{
			name:        "empty args preserve defaults",
			override:    `{"args":[],"sandboxArgs":[]}`,
			wantHost:    "claude --model opus --print -- '/cenci:merge-repair 42'",
			wantSandbox: "cenci open --model opus -- --print '/cenci:merge-repair 42'",
		},
		{
			name:        "sandbox model placeholder",
			override:    `{"sandboxArgs":["--model","{model}","--","--print","/repair-sandbox {ticket}"]}`,
			wantHost:    "claude --model opus --print -- '/cenci:merge-repair 42'",
			wantSandbox: "cenci open --model opus -- --print '/repair-sandbox 42'",
		},
	} {
		for _, sandbox := range []bool{false, true} {
			runtime, wantCommand := "host", tc.wantHost
			if sandbox {
				runtime, wantCommand = "sandbox", tc.wantSandbox
			}
			t.Run(tc.name+"/"+runtime, func(t *testing.T) {
				opts := noConfigOpts(t)
				config := `{"agents":{"claude":{"workflows":{"merge-repair":` + tc.override + `}}}}`
				if err := os.WriteFile(opts.ConfigPath, []byte(config), 0o600); err != nil {
					t.Fatal(err)
				}
				opts.Workflow, opts.Ticket = "merge-repair", "42"
				opts.Agent, opts.Model = "claude", "opus"
				opts.SandboxSet, opts.Sandbox = true, sandbox
				ctrl := &mockCtrl{session: "work"}

				if err := Run(opts, ctrl); err != nil {
					t.Fatalf("Run: %v", err)
				}
				if len(ctrl.windows) != 1 || ctrl.windows[0].cmd != wantCommand {
					t.Errorf("windows = %+v, want command %q", ctrl.windows, wantCommand)
				}
			})
		}
	}
}

func TestRunPrependsDir(t *testing.T) {
	m := &mockCtrl{session: "work"}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket = "implement", "40"
	opts.Dir = "/repos/my project"

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(m.windows) != 1 {
		t.Fatalf("expected 1 NewWindow, got %d", len(m.windows))
	}
	cmd := m.windows[0].cmd
	// The command must start by cd'ing into the (quoted) dir, then run the agent.
	if !strings.HasPrefix(cmd, "cd '/repos/my project' && ") {
		t.Errorf("command missing cd prefix: %q", cmd)
	}
	if !strings.Contains(cmd, "/cenci:implement 40") {
		t.Errorf("command dropped the workflow: %q", cmd)
	}
}

// TestRunPrependsDirInDryRun is ticket #975's AC 1 dry-run variant of
// TestRunPrependsDir above: the cd prefix opts.Dir adds must reach dry-run
// output too, since Dir is prepended into shellCommand unconditionally,
// before the dry-run print — proving --dry-run shows the resolved
// directory without spawning anything.
func TestRunPrependsDirInDryRun(t *testing.T) {
	m := &mockCtrl{session: "work"}
	var buf bytes.Buffer
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket = "implement", "40"
	opts.Dir = "/repos/my project"
	opts.DryRun = true
	opts.Out = &buf

	if err := Run(opts, m); err != nil {
		t.Fatal(err)
	}
	if len(m.windows) != 0 {
		t.Errorf("dry-run must not spawn, got %+v", m.windows)
	}
	out := buf.String()
	if !strings.Contains(out, "cd '/repos/my project' && ") {
		t.Errorf("dry-run output missing cd prefix: %q", out)
	}
}

// OpenCode remains disabled for both host/sandbox runs and config overrides.
func TestRunOpenCodeDisabledNeverSpawns(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		for _, host := range []bool{false, true} {
			for _, fromConfig := range []bool{false, true} {
				m := &mockCtrl{session: "work"}
				opts := noConfigOpts(t)
				opts.Agent, opts.Workflow, opts.Ticket = "opencode", "implement", "40"
				opts.DryRun = dryRun
				opts.SandboxSet, opts.Sandbox = host, !host
				if fromConfig {
					opts.Agent = ""
					if err := os.WriteFile(opts.ConfigPath, []byte(`{"defaultAgent":"opencode","agents":{"opencode":{"command":"custom-opencode"}}}`), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				var buf bytes.Buffer
				opts.Out = &buf
				err := Run(opts, m)
				if err == nil || !strings.Contains(err.Error(), "OpenCode is temporarily disabled") {
					t.Fatalf("expected pause refusal, got %v", err)
				}
				if len(m.windows) != 0 || buf.Len() != 0 {
					t.Fatalf("disabled run created a window or printed a launch command: %+v %s", m.windows, buf.String())
				}
			}
		}
	}
}

// TestRunQuotesPromptWithSpecialCharacters guards shell-safety: a ticket
// carrying spaces/quotes must reach the spawned command as a single,
// safely-quoted shell word (shellQuote handles this generically). It replaces
// the retired OpenCode-specific variant (#1214): the contract is the same for
// the supported clients.
func TestRunQuotesPromptWithSpecialCharacters(t *testing.T) {
	m := &mockCtrl{session: "work"}
	opts := noConfigOpts(t)
	opts.Agent, opts.Workflow = "claude", "implement"
	opts.Ticket = `42 fix the "quoting" bug`

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(m.windows) != 1 {
		t.Fatalf("expected 1 NewWindow, got %d", len(m.windows))
	}
	cmd := m.windows[0].cmd
	wantQuoted := `'/cenci:implement 42 fix the "quoting" bug'`
	if !strings.Contains(cmd, wantQuoted) {
		t.Errorf("command = %q, want prompt single-quoted as %q", cmd, wantQuoted)
	}
}

// --- #1214: pre-spawn capability, sandbox and disabled-agent safety ---

// runOutcome is everything a #1214 test observes about one Run call.
type runOutcome struct {
	err          error
	ctrl         *mockCtrl
	out          string
	configPath   string
	daemonCalled bool
}

// runCase runs Run against a config body ("" = built-ins only). set mutates
// the default Opts (workflow implement, ticket 40) before the call.
func runCase(t *testing.T, configBody string, dryRun bool, set func(*Opts)) runOutcome {
	t.Helper()
	var o runOutcome
	opts := noConfigOpts(t)
	if configBody != "" {
		opts.ConfigPath = writeConfigFile(t, configBody)
	}
	o.configPath = opts.ConfigPath
	opts.Workflow, opts.Ticket = "implement", "40"
	opts.DryRun = dryRun
	var buf bytes.Buffer
	opts.Out = &buf
	opts.EnsureDaemon = func() { o.daemonCalled = true }
	if set != nil {
		set(&opts)
	}
	o.ctrl = &mockCtrl{session: "work"}
	o.err = Run(opts, o.ctrl)
	o.out = buf.String()
	return o
}

// assertNothingSpawned pins the pre-spawn contract: no window, no grouped
// session probe, no option write, no daemon start, and no dry-run output.
func assertNothingSpawned(t *testing.T, o runOutcome) {
	t.Helper()
	if len(o.ctrl.windows) != 0 {
		t.Errorf("a window was created: %+v", o.ctrl.windows)
	}
	if len(o.ctrl.options) != 0 {
		t.Errorf("window options were set: %+v", o.ctrl.options)
	}
	if len(o.ctrl.groupedSessions) != 0 {
		t.Errorf("IsGroupedSession was probed: %v (the failure must precede session resolution)", o.ctrl.groupedSessions)
	}
	if o.daemonCalled {
		t.Error("EnsureDaemon was called for a launch that cannot succeed")
	}
	if o.out != "" {
		t.Errorf("dry-run printed output for a launch that cannot succeed:\n%s", o.out)
	}
}

// dryRunLine returns the dry-run output line starting with prefix, or "".
func dryRunLine(out, prefix string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func acmeConfigWith(top string) string {
	return `{` + top + `"agents":{"acme":{"command":"acme","workflows":{"refine":{"args":["--task","{ticket}"]}}}}}`
}

func runModes() []struct {
	name   string
	dryRun bool
} {
	return []struct {
		name   string
		dryRun bool
	}{{"dry-run", true}, {"real", false}}
}

// Missing template (AC 2): a custom unsupported agent, independent of the
// disabled-OpenCode guard.
func TestRunMissingTemplateFailsBeforeSpawn(t *testing.T) {
	cases := []struct {
		name  string
		agent string
		set   func(*Opts)
	}{
		{"custom agent opted out of sandbox", "acme", func(o *Opts) { o.SandboxSet, o.Sandbox = true, false }},
		// The missing-template check must win over the sandbox check: the
		// operator should hear about the more fundamental problem first.
		{"custom agent with default sandbox", "acme", nil},
		{"unknown agent", "ghost", func(o *Opts) { o.SandboxSet, o.Sandbox = true, false }},
	}
	for _, tc := range cases {
		for _, mode := range runModes() {
			t.Run(tc.name+"/"+mode.name, func(t *testing.T) {
				o := runCase(t, acmeConfig, mode.dryRun, func(opts *Opts) {
					opts.Agent = tc.agent
					if tc.set != nil {
						tc.set(opts)
					}
				})
				if o.err == nil {
					t.Fatal("expected an error")
				}
				if !errors.Is(o.err, ErrMissingTemplate) {
					t.Errorf("err = %v, want errors.Is(_, ErrMissingTemplate)", o.err)
				}
				if errors.Is(o.err, ErrWindowSpawned) {
					t.Errorf("err = %v, must not wrap ErrWindowSpawned", o.err)
				}
				for _, want := range []string{tc.agent, "implement", "agents." + tc.agent + ".workflows.implement", o.configPath} {
					if !strings.Contains(o.err.Error(), want) {
						t.Errorf("err = %q, want it to contain %q", o.err, want)
					}
				}
				assertNothingSpawned(t, o)
			})
		}
	}
}

// Unsatisfied sandbox (AC 3): requested via flag, config, or the #98 default,
// but the agent has no sandboxCommand. Fails before spawn, dry run included.
func TestRunUnsatisfiedSandboxFailsBeforeSpawn(t *testing.T) {
	sources := []struct {
		name        string
		config      string
		set         func(*Opts)
		wantInErr   []string
		absentInErr []string
	}{
		{
			name:        "flag",
			config:      acmeConfig,
			set:         func(o *Opts) { o.SandboxSet, o.Sandbox = true, true },
			wantInErr:   []string{"--sandbox"},
			absentInErr: []string{`"sandbox": true`},
		},
		{
			name:        "config",
			config:      acmeConfigWith(`"sandbox":true,`),
			wantInErr:   []string{`"sandbox": true`},
			absentInErr: []string{"--sandbox"},
		},
		{
			name:        "default",
			config:      acmeConfig,
			wantInErr:   []string{"default"},
			absentInErr: []string{`"sandbox": true`, "--sandbox"},
		},
	}
	for _, src := range sources {
		for _, mode := range runModes() {
			t.Run(src.name+"/"+mode.name, func(t *testing.T) {
				o := runCase(t, src.config, mode.dryRun, func(opts *Opts) {
					opts.Agent, opts.Workflow = "acme", "refine"
					if src.set != nil {
						src.set(opts)
					}
				})
				if o.err == nil {
					t.Fatal("expected an error: sandbox requested but acme has no sandboxCommand")
				}
				if !errors.Is(o.err, ErrSandboxUnavailable) {
					t.Errorf("err = %v, want errors.Is(_, ErrSandboxUnavailable)", o.err)
				}
				if errors.Is(o.err, ErrMissingTemplate) || errors.Is(o.err, ErrAgentDisabled) || errors.Is(o.err, ErrWindowSpawned) {
					t.Errorf("err = %v, must wrap only ErrSandboxUnavailable", o.err)
				}
				msg := o.err.Error()
				// Names the agent and BOTH remedies.
				for _, want := range append([]string{"acme", "sandboxCommand", "agents.acme.sandboxCommand", "--no-sandbox", `"sandbox": false`}, src.wantInErr...) {
					if !strings.Contains(strings.ToLower(msg), strings.ToLower(want)) {
						t.Errorf("err = %q, want it to contain %q", msg, want)
					}
				}
				// Each request source is named distinctly (#446).
				for _, banned := range src.absentInErr {
					if strings.Contains(strings.ToLower(msg), strings.ToLower(banned)) {
						t.Errorf("err = %q, must not name another source (%q)", msg, banned)
					}
				}
				if src.name == "config" && !strings.Contains(msg, o.configPath) {
					t.Errorf("err = %q, want the config path %s that requested the sandbox", msg, o.configPath)
				}
				assertNothingSpawned(t, o)
			})
		}
	}
}

// A whitespace-only sandboxCommand is unset, not a runnable launcher.
func TestRunWhitespaceSandboxCommandIsUnsatisfied(t *testing.T) {
	cfg := `{"agents":{"acme":{"command":"acme","sandboxCommand":"  \t ","workflows":{"refine":{"args":["--task","{ticket}"]}}}}}`
	o := runCase(t, cfg, false, func(opts *Opts) { opts.Agent, opts.Workflow = "acme", "refine" })
	if !errors.Is(o.err, ErrSandboxUnavailable) {
		t.Fatalf("err = %v, want errors.Is(_, ErrSandboxUnavailable)", o.err)
	}
	assertNothingSpawned(t, o)
}

// Positive control: a custom agent that DOES define sandboxCommand launches it
// when the sandbox is requested (the default).
func TestRunCustomAgentWithSandboxCommandLaunchesIt(t *testing.T) {
	cfg := `{"agents":{"acme":{"command":"acme","sandboxCommand":"acme-box run","workflows":{"refine":{"args":["--task","{ticket}"]}}}}}`
	o := runCase(t, cfg, false, func(opts *Opts) { opts.Agent, opts.Workflow = "acme", "refine" })
	if o.err != nil {
		t.Fatalf("Run: %v", o.err)
	}
	if len(o.ctrl.windows) != 1 || o.ctrl.windows[0].cmd != "acme-box run --task 40" {
		t.Errorf("windows = %+v, want one window running %q", o.ctrl.windows, "acme-box run --task 40")
	}
}

// Explicit host opt-out still works for a custom agent with a user-defined
// template (AC 4), via --no-sandbox and via config "sandbox": false.
func TestRunExplicitHostOptOutStillWorksForCustomAgent(t *testing.T) {
	cases := []struct {
		name   string
		config string
		set    func(*Opts)
	}{
		{"--no-sandbox flag", acmeConfig, func(o *Opts) { o.SandboxSet, o.Sandbox = true, false }},
		{`config "sandbox": false`, acmeConfigWith(`"sandbox":false,`), nil},
	}
	for _, tc := range cases {
		for _, mode := range runModes() {
			t.Run(tc.name+"/"+mode.name, func(t *testing.T) {
				o := runCase(t, tc.config, mode.dryRun, func(opts *Opts) {
					opts.Agent, opts.Workflow = "acme", "refine"
					if tc.set != nil {
						tc.set(opts)
					}
				})
				if o.err != nil {
					t.Fatalf("Run: %v", o.err)
				}
				const wantCommand = "acme --task 40"
				if mode.dryRun {
					if len(o.ctrl.windows) != 0 {
						t.Errorf("dry-run must not spawn, got %+v", o.ctrl.windows)
					}
					if got := dryRunLine(o.out, "command:"); got != "command: "+wantCommand {
						t.Errorf("command line = %q, want %q\n%s", got, "command: "+wantCommand, o.out)
					}
					return
				}
				if len(o.ctrl.windows) != 1 || o.ctrl.windows[0].cmd != wantCommand {
					t.Errorf("windows = %+v, want one window running %q", o.ctrl.windows, wantCommand)
				}
			})
		}
	}
}

// Provenance (AC 4): dry run labels where the resolved template came from.
func TestRunDryRunLabelsTemplateProvenance(t *testing.T) {
	claudeOverride := `{"agents":{"claude":{"workflows":{"implement":{"args":["--","/overridden {ticket}"]},"file-added":{"args":["--","/added {ticket}"]}}}}}`
	cases := []struct {
		name   string
		config string
		set    func(*Opts)
		want   func(path string) string
	}{
		{"built-in claude", "", nil, func(string) string { return "template: built-in" }},
		{"built-in codex", "", func(o *Opts) { o.Agent = "codex" }, func(string) string { return "template: built-in" }},
		{"args-only override stays built-in", claudeOverride, func(o *Opts) { o.SandboxSet, o.Sandbox = true, false },
			func(string) string { return "template: built-in" }},
		{"file-added workflow on built-in agent", claudeOverride, func(o *Opts) { o.Workflow = "file-added" },
			func(p string) string { return "template: user-defined (" + p + ")" }},
		{"custom agent", acmeConfig, func(o *Opts) { o.Agent, o.Workflow, o.SandboxSet, o.Sandbox = "acme", "refine", true, false },
			func(p string) string { return "template: user-defined (" + p + ")" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := runCase(t, tc.config, true, tc.set)
			if o.err != nil {
				t.Fatalf("Run: %v", o.err)
			}
			want := tc.want(o.configPath)
			if got := dryRunLine(o.out, "template:"); got != want {
				t.Errorf("template line = %q, want %q\n%s", got, want, o.out)
			}
			// The pre-existing resolution lines are still printed.
			for _, prefix := range []string{"session:", "window:", "command:"} {
				if dryRunLine(o.out, prefix) == "" {
					t.Errorf("dry-run output lost its %q line:\n%s", prefix, o.out)
				}
			}
		})
	}
}

// TestRunSandboxArgsRequireSandboxCommand replaces the former assertion of a
// silent host fallback (#1214 AC 3): sandboxArgs only apply with a
// sandboxCommand, so a sandbox request against an agent without one now fails
// closed, while the explicit opt-out runs the host args.
func TestRunSandboxArgsRequireSandboxCommand(t *testing.T) {
	config := `{"agents":{"custom":{"command":"claude","workflows":{"repair":{"args":["--print","--","/repair-host {ticket}"],"sandboxArgs":["--","--print","/repair-sandbox {ticket}"]}}}}}`
	set := func(sandbox bool) func(*Opts) {
		return func(o *Opts) {
			o.Workflow, o.Ticket = "repair", "42"
			o.Agent, o.Model = "custom", "opus"
			o.SandboxSet, o.Sandbox = true, sandbox
		}
	}

	t.Run("sandbox requested fails closed", func(t *testing.T) {
		o := runCase(t, config, false, set(true))
		if !errors.Is(o.err, ErrSandboxUnavailable) {
			t.Fatalf("err = %v, want errors.Is(_, ErrSandboxUnavailable)", o.err)
		}
		assertNothingSpawned(t, o)
	})

	t.Run("explicit opt-out runs host args", func(t *testing.T) {
		o := runCase(t, config, false, set(false))
		if o.err != nil {
			t.Fatalf("Run: %v", o.err)
		}
		wantCommand := "claude --model opus --print -- '/repair-host 42'"
		if len(o.ctrl.windows) != 1 || o.ctrl.windows[0].cmd != wantCommand {
			t.Errorf("windows = %+v, want host command %q", o.ctrl.windows, wantCommand)
		}
	})
}

// Disabled OpenCode (AC 5, AC 7): every route to the opencode agent -- the
// --agent flag, the config defaultAgent, and a config override that supplies
// opencode workflows plus a sandboxCommand -- reports the temporary disable,
// never suggests a config override, and spawns nothing.
func TestRunDisabledOpenCodeFailsBeforeSpawn(t *testing.T) {
	openCodeOverride := `{"agents":{"opencode":{"command":"opencode","sandboxCommand":"cenci open","workflows":{"implement":{"args":["--auto","--prompt","/cenci:implement {ticket}"]}}}}}`
	cases := []struct {
		name   string
		config string
		set    func(*Opts)
	}{
		{"--agent opencode, default sandbox", "", func(o *Opts) { o.Agent = "opencode" }},
		{"--agent opencode, opted out of sandbox", "", func(o *Opts) { o.Agent = "opencode"; o.SandboxSet, o.Sandbox = true, false }},
		{"config defaultAgent", `{"defaultAgent":"opencode"}`, nil},
		{"config override with workflows and sandboxCommand", openCodeOverride, func(o *Opts) { o.Agent = "opencode" }},
		{"config override, opted out of sandbox", openCodeOverride, func(o *Opts) { o.Agent = "opencode"; o.SandboxSet, o.Sandbox = true, false }},
		{"config defaultAgent plus override", `{"defaultAgent":"opencode","agents":{"opencode":{"command":"opencode","sandboxCommand":"cenci open","workflows":{"implement":{"args":["{ticket}"]}}}}}`, nil},
	}
	for _, tc := range cases {
		for _, mode := range runModes() {
			t.Run(tc.name+"/"+mode.name, func(t *testing.T) {
				o := runCase(t, tc.config, mode.dryRun, tc.set)
				if o.err == nil {
					t.Fatal("expected an error: opencode is temporarily disabled")
				}
				if !errors.Is(o.err, ErrAgentDisabled) {
					t.Errorf("err = %v, want errors.Is(_, ErrAgentDisabled)", o.err)
				}
				if errors.Is(o.err, ErrMissingTemplate) || errors.Is(o.err, ErrSandboxUnavailable) || errors.Is(o.err, ErrWindowSpawned) {
					t.Errorf("err = %v, must wrap only ErrAgentDisabled", o.err)
				}
				msg := o.err.Error()
				for _, want := range []string{"OpenCode", "temporarily disabled"} {
					if !strings.Contains(msg, want) {
						t.Errorf("err = %q, want it to contain %q", msg, want)
					}
				}
				for _, banned := range []string{"config.json", "add one"} {
					if strings.Contains(msg, banned) {
						t.Errorf("err = %q, must not suggest a config override (%q)", msg, banned)
					}
				}
				assertNothingSpawned(t, o)
			})
		}
	}
}

func TestRunRefusesGroupedSession(t *testing.T) {
	m := &mockCtrl{session: "work", grouped: true}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Slug = "implement", "40", "demo"

	if err := Run(opts, m); err == nil {
		t.Fatal("expected an error for a grouped session")
	}
	if len(m.windows) != 0 {
		t.Errorf("must not spawn into a grouped session, got %+v", m.windows)
	}
}

// -- ErrWindowSpawned (#853): a post-NewWindow SetWindowOption failure is
// confirmed-alive launch evidence -- the tmux window was demonstrably
// created even though this call failed -- so the caller (dispatch's resume
// rollback) can retain Working instead of rolling back to Input Needed. Every
// failure that happens before ctrl.NewWindow ever succeeds must NOT carry the
// sentinel, since no window exists at all in that case.

// TestRunSetWindowOptionFailureWrapsErrWindowSpawned covers the positive
// case: NewWindow succeeds (so the window was demonstrably created), then
// SetWindowOption fails -- the returned error must be detectable via
// errors.Is(_, ErrWindowSpawned).
func TestRunSetWindowOptionFailureWrapsErrWindowSpawned(t *testing.T) {
	m := &mockCtrl{session: "work", optErr: errors.New("tmux set-option failed")}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Slug = "implement", "40", "demo"

	err := Run(opts, m)
	if err == nil {
		t.Fatal("expected an error when SetWindowOption fails")
	}
	if !errors.Is(err, ErrWindowSpawned) {
		t.Errorf("error = %v, want errors.Is(_, ErrWindowSpawned) -- the window was already created before this failure", err)
	}
	if strings.Contains(err.Error(), "=work:40-implement") || !strings.Contains(err.Error(), "work:40-implement") {
		t.Errorf("error = %q, want the human-readable session target without tmux's exact-match marker", err)
	}
	if len(m.windows) != 1 {
		t.Errorf("expected the window to have been created before the failure, got %+v", m.windows)
	}
}

// TestRunPreNewWindowFailuresNeverWrapErrWindowSpawned covers every failure
// class that can occur before ctrl.NewWindow ever succeeds: none of them may
// carry ErrWindowSpawned, since no tmux window was ever demonstrably created.
func TestRunPreNewWindowFailuresNeverWrapErrWindowSpawned(t *testing.T) {
	cases := []struct {
		name   string
		ctrl   *mockCtrl
		config string // "" = built-ins only
		set    func(*Opts)
	}{
		{name: "grouped session refused", ctrl: &mockCtrl{session: "work", grouped: true}},
		{name: "no session and none passed", ctrl: &mockCtrl{session: "", sessionErr: errors.New("no server running")}},
		{name: "NewWindow itself fails", ctrl: &mockCtrl{session: "work", newWindowErr: errors.New("tmux new-window failed")}},
		// #1214: the new fail-closed classes are all pre-spawn too.
		{name: "disabled agent", ctrl: &mockCtrl{session: "work"}, set: func(o *Opts) { o.Agent = "opencode" }},
		{name: "missing template", ctrl: &mockCtrl{session: "work"}, config: acmeConfig, set: func(o *Opts) { o.Agent = "acme" }},
		{name: "unsatisfied sandbox", ctrl: &mockCtrl{session: "work"}, config: acmeConfig, set: func(o *Opts) { o.Agent, o.Workflow = "acme", "refine" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := noConfigOpts(t)
			if tc.config != "" {
				opts.ConfigPath = writeConfigFile(t, tc.config)
			}
			opts.Workflow, opts.Ticket, opts.Slug = "implement", "40", "demo"
			if tc.set != nil {
				tc.set(&opts)
			}

			err := Run(opts, tc.ctrl)
			if err == nil {
				t.Fatal("expected an error")
			}
			if errors.Is(err, ErrWindowSpawned) {
				t.Errorf("error = %v, must NOT wrap ErrWindowSpawned -- no window was ever demonstrably created", err)
			}
		})
	}
}

// TestRunEnsuresDaemonBeforeSpawning guards #139: a window spawned before the
// daemon (and thus the event socket `cenci open` mounts into the sandbox) is
// up never gets status wired in for its whole lifetime.
func TestRunEnsuresDaemonBeforeSpawning(t *testing.T) {
	m := &mockCtrl{session: "work"}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Slug = "implement", "40", "demo"

	called := false
	opts.EnsureDaemon = func() { called = true }

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !called {
		t.Error("expected EnsureDaemon to be called before spawning the window")
	}
	if len(m.windows) != 1 {
		t.Fatalf("expected 1 NewWindow, got %d", len(m.windows))
	}
}

func TestRunDryRunDoesNotEnsureDaemon(t *testing.T) {
	m := &mockCtrl{session: "work"}
	var buf bytes.Buffer
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Slug = "implement", "40", "demo"
	opts.DryRun = true
	opts.Out = &buf

	called := false
	opts.EnsureDaemon = func() { called = true }

	if err := Run(opts, m); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("dry-run must not start the daemon")
	}
}

func TestRunRefusedGroupedSessionDoesNotEnsureDaemon(t *testing.T) {
	m := &mockCtrl{session: "work", grouped: true}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Slug = "implement", "40", "demo"

	called := false
	opts.EnsureDaemon = func() { called = true }

	if err := Run(opts, m); err == nil {
		t.Fatal("expected an error for a grouped session")
	}
	if called {
		t.Error("a refused grouped session must not start the daemon")
	}
}

func TestRunErrorsWhenNotInTmuxWithoutSession(t *testing.T) {
	m := &mockCtrl{session: "", sessionErr: errors.New("no server running")}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Slug = "implement", "40", "demo"

	if err := Run(opts, m); err == nil {
		t.Fatal("expected an error when not in tmux and no --session")
	}
	if len(m.windows) != 0 {
		t.Errorf("must not spawn, got %+v", m.windows)
	}
}

func TestRunUsesExplicitSession(t *testing.T) {
	m := &mockCtrl{session: "", sessionErr: errors.New("no server running")}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Slug = "implement", "40", "demo"
	opts.Session = "explicit"

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run with explicit session: %v", err)
	}
	if len(m.windows) != 1 || m.windows[0].session != "=explicit" {
		t.Errorf("expected spawn into exact explicit session, got %+v", m.windows)
	}
}

func TestRunDryRunPrintsAndDoesNotSpawn(t *testing.T) {
	m := &mockCtrl{session: "work"}
	var buf bytes.Buffer
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Slug = "implement", "40", "demo"
	opts.DryRun = true
	opts.Out = &buf

	if err := Run(opts, m); err != nil {
		t.Fatal(err)
	}
	if len(m.windows) != 0 {
		t.Errorf("dry-run must not spawn, got %+v", m.windows)
	}
	out := buf.String()
	for _, want := range []string{"work", "40-implement", "/cenci:implement 40"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, out)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Hello World":     "hello-world",
		"foo_bar":         "foo-bar",
		"  a  b  ":        "a-b",
		"Add: New! Thing": "add-new-thing",
		"already-slug":    "already-slug",
		"--x--y--":        "x-y",
		"":                "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWindowName(t *testing.T) {
	// A numeric ticket names the window `<number>-<skill>`, one of the three
	// cenci workflows — short and uniform.
	if got := windowName("230", "refine", ""); got != "230-refine" {
		t.Errorf("numeric refine = %q, want 230-refine", got)
	}
	if got := windowName("255", "implement", ""); got != "255-implement" {
		t.Errorf("numeric implement = %q, want 255-implement", got)
	}
	if got := windowName("412", "babysit", ""); got != "412-babysit" {
		t.Errorf("numeric babysit = %q, want 412-babysit", got)
	}
	// Trailing context after a numeric id is ignored: the name is skill-only.
	if got := windowName("42 focus on the API layer", "refine", ""); got != "42-refine" {
		t.Errorf("id+context = %q, want 42-refine", got)
	}
	// A leading '#' on the id is stripped.
	if got := windowName("#1 focus on API", "babysit", ""); got != "1-babysit" {
		t.Errorf("hash id = %q, want 1-babysit", got)
	}
	// --slug is ignored for a numeric ticket — the name is always the skill.
	if got := windowName("42 raw context", "implement", "chosen"); got != "42-implement" {
		t.Errorf("slug ignored for numeric = %q, want 42-implement", got)
	}

	// Free-text task description (implement's ticketless mode) keeps its
	// descriptive slug — it has no ticket number and is never joined on.
	if got := windowName("feature-x", "implement", ""); got != "feature-x" {
		t.Errorf("non-numeric ticket = %q, want feature-x", got)
	}
	if got := windowName("add dark mode toggle", "implement", ""); got != "add-dark-mode-toggle" {
		t.Errorf("task description = %q, want add-dark-mode-toggle", got)
	}
	// An explicit --slug wins for free-text.
	if got := windowName("add dark mode toggle", "implement", "chosen"); got != "chosen" {
		t.Errorf("explicit slug should win = %q, want chosen", got)
	}

	// The cap still applies to a long free-text slug, keeping any prefix intact.
	long := windowName("free "+strings.Repeat("a", 100), "implement", "")
	if len([]rune(long)) > windowNameMaxLen {
		t.Errorf("windowName not capped: len=%d (%q)", len([]rune(long)), long)
	}
}

// TestRunImplementExplicitSandboxFlagResolvesSandbox pins that an explicit
// --sandbox flag (SandboxSet && Sandbox) resolves the sandbox launcher, the
// same as the default (#98).
func TestRunImplementExplicitSandboxFlagResolvesSandbox(t *testing.T) {
	m := &mockCtrl{session: "work"}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket = "implement", "40"
	opts.SandboxSet, opts.Sandbox = true, true

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(m.windows) != 1 {
		t.Fatalf("expected 1 NewWindow, got %d", len(m.windows))
	}
	if !strings.Contains(m.windows[0].cmd, "cenci open") {
		t.Errorf("implement --sandbox must still resolve sandbox: %q", m.windows[0].cmd)
	}
}

func TestRunForwardsFullTicketArgument(t *testing.T) {
	m := &mockCtrl{session: "work"}
	opts := noConfigOpts(t)
	opts.Workflow = "implement"
	opts.Ticket = "42 focus on the API layer"

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(m.windows) != 1 {
		t.Fatalf("expected 1 NewWindow, got %d", len(m.windows))
	}
	w := m.windows[0]
	// The whole argument reaches the skill, not just the first token.
	if !strings.Contains(w.cmd, "/cenci:implement 42 focus on the API layer") {
		t.Errorf("command dropped context: %q", w.cmd)
	}
	// The window name is the skill only — trailing context does not leak in.
	if w.name != "42-implement" {
		t.Errorf("window name = %q, want 42-implement", w.name)
	}
}

// --- #1250: recording the launch repo directory on every spawned window ---

// repoDirCalls returns the SetWindowOption calls that wrote the repo-dir option.
func repoDirCalls(m *mockCtrl) []optCall {
	var calls []optCall
	for _, o := range m.options {
		if o.key == frontend.RepoDirOption {
			calls = append(calls, o)
		}
	}
	return calls
}

// requireOneRepoDir asserts the window got exactly one repo-dir write, aimed
// at the window Run just named, and returns its value.
func requireOneRepoDir(t *testing.T, m *mockCtrl) string {
	t.Helper()
	calls := repoDirCalls(m)
	if len(calls) != 1 {
		t.Fatalf("repo-dir option writes = %+v, want exactly one (all options: %+v)", calls, m.options)
	}
	if calls[0].target != "=work:=40-implement" {
		t.Errorf("repo-dir target = %q, want =work:=40-implement", calls[0].target)
	}
	return calls[0].value
}

func TestRepoDirOptionName(t *testing.T) {
	if frontend.RepoDirOption != "@cenci-repo-dir" {
		t.Errorf("RepoDirOption = %q, want @cenci-repo-dir (the @cenci-* window-option naming)", frontend.RepoDirOption)
	}
}

func TestRunRecordsAbsoluteDirOnSpawnedWindow(t *testing.T) {
	dir := t.TempDir()
	m := &mockCtrl{session: "work"}
	var warn bytes.Buffer
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Dir, opts.Warn = "implement", "40", dir, &warn

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := requireOneRepoDir(t, m); got != dir {
		t.Errorf("repo-dir value = %q, want the absolute launch dir %q", got, dir)
	}
	// automatic-rename off is still written first, then the repo dir.
	if len(m.options) < 2 || m.options[0].key != "automatic-rename" || m.options[1].key != frontend.RepoDirOption {
		t.Errorf("options = %+v, want automatic-rename first and the repo-dir option right after it", m.options)
	}
	if warn.Len() != 0 {
		t.Errorf("a successful launch must not warn, got %q", warn.String())
	}
}

func TestRunRecordsTrimmedDir(t *testing.T) {
	dir := t.TempDir()
	m := &mockCtrl{session: "work"}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Dir = "implement", "40", "  "+dir+"\t\n"

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := requireOneRepoDir(t, m); got != dir {
		t.Errorf("repo-dir value = %q, want the trimmed dir %q", got, dir)
	}
}

func TestRunMakesRelativeDirAbsoluteAgainstCwd(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	m := &mockCtrl{session: "work"}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Dir = "implement", "40", filepath.Join("repos", "app")

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := filepath.Join(base, "repos", "app")
	if got := requireOneRepoDir(t, m); got != want {
		t.Errorf("repo-dir value = %q, want %q (relative Dir made absolute against the cwd)", got, want)
	}
}

func TestRunRecordsCwdWhenDirIsUnset(t *testing.T) {
	for _, dir := range []string{"", "   "} {
		t.Run("Dir="+strings.ReplaceAll(dir, " ", "_"), func(t *testing.T) {
			base := t.TempDir()
			t.Chdir(base)
			m := &mockCtrl{session: "work"}
			opts := noConfigOpts(t)
			opts.Workflow, opts.Ticket, opts.Dir = "implement", "40", dir

			if err := Run(opts, m); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := requireOneRepoDir(t, m); got != base {
				t.Errorf("repo-dir value = %q, want the working directory %q", got, base)
			}
		})
	}
}

// TestRunRepoDirFailureWarnsButLaunchSucceeds pins AC 1's soft-failure clause:
// the window exists and runs, so a failed repo-dir write only costs the
// window its auto-close and must never surface as an error (let alone
// ErrWindowSpawned, which dispatch treats as a launch failure).
func TestRunRepoDirFailureWarnsButLaunchSucceeds(t *testing.T) {
	setErr := errors.New("tmux set-option exploded")
	m := &mockCtrl{session: "work", optErrByKey: map[string]error{frontend.RepoDirOption: setErr}}
	var warn bytes.Buffer
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Dir, opts.Warn = "implement", "40", t.TempDir(), &warn

	err := Run(opts, m)
	if err != nil {
		t.Fatalf("Run = %v, want nil: the window was created and a repo-dir failure is only a warning", err)
	}
	if errors.Is(err, ErrWindowSpawned) {
		t.Error("a repo-dir failure must not wrap ErrWindowSpawned")
	}
	if len(m.windows) != 1 {
		t.Errorf("windows = %+v, want the window created", m.windows)
	}
	if len(repoDirCalls(m)) != 1 {
		t.Errorf("repo-dir writes = %+v, want exactly one attempt", repoDirCalls(m))
	}
	msg := warn.String()
	if lines := strings.Split(strings.TrimSpace(msg), "\n"); len(lines) != 1 || lines[0] == "" {
		t.Fatalf("warning = %q, want exactly one line", msg)
	}
	for _, want := range []string{"40-implement", setErr.Error(), "auto-closed"} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning = %q, want it to contain %q", msg, want)
		}
	}
}

// TestRunUnresolvableCwdWarnsButLaunchSucceeds covers the other half of the
// soft-failure clause: the cwd fallback cannot be determined at all.
func TestRunUnresolvableCwdWarnsButLaunchSucceeds(t *testing.T) {
	gone := t.TempDir()
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatalf("removing the working directory: %v", err)
	}
	if _, err := os.Getwd(); err == nil {
		t.Skip("this platform still resolves a removed working directory")
	}
	m := &mockCtrl{session: "work"}
	var warn bytes.Buffer
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Warn = "implement", "40", &warn

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run = %v, want nil: an unresolvable cwd only costs the window its auto-close", err)
	}
	if len(m.windows) != 1 {
		t.Errorf("windows = %+v, want the window created", m.windows)
	}
	if calls := repoDirCalls(m); len(calls) != 0 {
		t.Errorf("repo-dir writes = %+v, want none when the dir cannot be resolved", calls)
	}
	msg := warn.String()
	if lines := strings.Split(strings.TrimSpace(msg), "\n"); len(lines) != 1 || lines[0] == "" {
		t.Fatalf("warning = %q, want exactly one line", msg)
	}
	for _, want := range []string{"40-implement", "auto-closed"} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning = %q, want it to contain %q", msg, want)
		}
	}
}

// TestRunRepoDirWarningDefaultsToStderr pins "nil Warn means os.Stderr".
func TestRunRepoDirWarningDefaultsToStderr(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	restored := false
	restore := func() {
		if !restored {
			os.Stderr = orig
			restored = true
		}
	}
	defer restore()

	m := &mockCtrl{session: "work", optErrByKey: map[string]error{frontend.RepoDirOption: errors.New("boom-on-repo-dir")}}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Dir = "implement", "40", t.TempDir()
	runErr := Run(opts, m)
	restore()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var captured bytes.Buffer
	if _, err := captured.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if !strings.Contains(captured.String(), "boom-on-repo-dir") {
		t.Errorf("stderr = %q, want the warning when Opts.Warn is nil", captured.String())
	}
}

// TestRunDryRunRecordsNothing: --dry-run touches no tmux state, so no option
// (including the repo dir) is written and nothing is warned.
func TestRunDryRunRecordsNothing(t *testing.T) {
	m := &mockCtrl{session: "work"}
	var out, warn bytes.Buffer
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Dir = "implement", "40", t.TempDir()
	opts.DryRun, opts.Out, opts.Warn = true, &out, &warn

	if err := Run(opts, m); err != nil {
		t.Fatal(err)
	}
	if len(m.options) != 0 || len(m.windows) != 0 {
		t.Errorf("dry-run touched tmux: windows=%+v options=%+v", m.windows, m.options)
	}
	if warn.Len() != 0 {
		t.Errorf("dry-run warned: %q", warn.String())
	}
}

// TestRunAutomaticRenameFailureNeverRecordsRepoDir pins the plan assumption
// that the repo dir is written only after automatic-rename succeeds: a failed
// rename keeps today's ErrWindowSpawned error and writes nothing further.
func TestRunAutomaticRenameFailureNeverRecordsRepoDir(t *testing.T) {
	m := &mockCtrl{session: "work", optErrByKey: map[string]error{"automatic-rename": errors.New("rename refused")}}
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Dir = "implement", "40", t.TempDir()

	err := Run(opts, m)
	if !errors.Is(err, ErrWindowSpawned) {
		t.Fatalf("err = %v, want errors.Is(_, ErrWindowSpawned)", err)
	}
	if calls := repoDirCalls(m); len(calls) != 0 {
		t.Errorf("repo-dir writes = %+v, want none after an automatic-rename failure", calls)
	}
}
