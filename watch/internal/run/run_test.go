package run

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
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
	if len(m.options) != 1 || m.options[0].target != "==work:=40-implement" {
		t.Errorf("option calls = %+v, want exact target ==work:=40-implement", m.options)
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

// TestRunOpenCodeHostCommandNoSandboxCommandConfigured is the Run()-level
// analog of TestOpenCodeSandboxWiringOutOfScope: dispatching through the
// public Run() entry point with --agent opencode (and no --no-sandbox) must
// still spawn the bare "opencode" host command, since no sandboxCommand is
// configured for opencode yet (#490 owns that wiring).
func TestRunOpenCodeHostCommandNoSandboxCommandConfigured(t *testing.T) {
	m := &mockCtrl{session: "work"}
	opts := noConfigOpts(t)
	opts.Agent, opts.Workflow, opts.Ticket = "opencode", "implement", "40"

	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(m.windows) != 1 {
		t.Fatalf("expected 1 NewWindow, got %d", len(m.windows))
	}
	cmd := m.windows[0].cmd
	if strings.Contains(cmd, "cenci open") {
		t.Errorf("opencode has no sandboxCommand configured yet (#490): %q", cmd)
	}
	if !strings.Contains(cmd, "opencode") || !strings.Contains(cmd, "--auto") || !strings.Contains(cmd, "/cenci:implement 40") {
		t.Errorf("command = %q, want opencode launcher with --auto and the implement prompt", cmd)
	}
}

// TestRunOpenCodeDryRunPrintsResolvedCommand mirrors
// TestRunDryRunPrintsAndDoesNotSpawn for the opencode agent: dry-run must be
// deterministic and never touch tmux state.
func TestRunOpenCodeDryRunPrintsResolvedCommand(t *testing.T) {
	m := &mockCtrl{session: "work"}
	var buf bytes.Buffer
	opts := noConfigOpts(t)
	opts.Agent, opts.Workflow, opts.Ticket = "opencode", "implement", "40"
	opts.DryRun = true
	opts.Out = &buf

	if err := Run(opts, m); err != nil {
		t.Fatal(err)
	}
	if len(m.windows) != 0 {
		t.Errorf("dry-run must not spawn, got %+v", m.windows)
	}
	out := buf.String()
	for _, want := range []string{"work", "40-implement", "/cenci:implement 40", "--auto"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, out)
		}
	}
}

// TestRunOpenCodeQuotesPromptWithSpecialCharacters guards shell-safety: a
// ticket carrying spaces/quotes must reach the spawned command as a single,
// safely-quoted shell word (shellQuote already handles this generically; this
// pins the behavior for opencode's --prompt argument specifically).
func TestRunOpenCodeQuotesPromptWithSpecialCharacters(t *testing.T) {
	m := &mockCtrl{session: "work"}
	opts := noConfigOpts(t)
	opts.Agent, opts.Workflow = "opencode", "implement"
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
		name string
		ctrl *mockCtrl
	}{
		{"grouped session refused", &mockCtrl{session: "work", grouped: true}},
		{"no session and none passed", &mockCtrl{session: "", sessionErr: errors.New("no server running")}},
		{"NewWindow itself fails", &mockCtrl{session: "work", newWindowErr: errors.New("tmux new-window failed")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := noConfigOpts(t)
			opts.Workflow, opts.Ticket, opts.Slug = "implement", "40", "demo"

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
