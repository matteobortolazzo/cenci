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

	windows []winCall
	options []optCall
}

type winCall struct{ session, name, cmd string }
type optCall struct{ target, key, value string }

func (m *mockCtrl) CurrentSession() (string, error)       { return m.session, m.sessionErr }
func (m *mockCtrl) IsGroupedSession(string) (bool, error) { return m.grouped, m.groupedErr }
func (m *mockCtrl) NewWindow(session, name, cmd string) error {
	m.windows = append(m.windows, winCall{session, name, cmd})
	return m.newWindowErr
}
func (m *mockCtrl) SetWindowOption(target, key, value string) error {
	m.options = append(m.options, optCall{target, key, value})
	return nil
}

// noConfigOpts returns Opts pinned to a non-existent config (built-ins only),
// so tests are deterministic.
func noConfigOpts(t *testing.T) Opts {
	t.Helper()
	return Opts{
		ConfigPath: filepath.Join(t.TempDir(), "none.json"),
		// Stub the daemon hook: the real daemon.EnsureRunning spawns
		// os.Executable(), which inside `go test` is this test binary —
		// every spawned child re-runs the suite and spawns more children
		// (a fork bomb, masked in sandboxes where AGENT_SAND=1 short-
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
	if w.session != "work" {
		t.Errorf("session = %q, want work", w.session)
	}
	if w.name != "40-implement" {
		t.Errorf("name = %q, want 40-implement", w.name)
	}
	// With no flag and no config, the default is now the sandbox launcher (#98).
	if !strings.Contains(w.cmd, "agent-sand") || !strings.Contains(w.cmd, "/agentflow:implement 40") {
		t.Errorf("command = %q", w.cmd)
	}

	found := false
	for _, o := range m.options {
		if o.target == "work:40-implement" && o.key == "automatic-rename" && o.value == "off" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected automatic-rename off on work:40-implement, got %+v", m.options)
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
	if !strings.Contains(m.windows[0].cmd, "agent-sand") {
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
	if strings.Contains(cmd, "agent-sand") {
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
	if !strings.Contains(cmd, "/agentflow:implement 40") {
		t.Errorf("command dropped the workflow: %q", cmd)
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

// TestRunEnsuresDaemonBeforeSpawning guards #139: a window spawned before the
// daemon (and thus the event socket agent-sand mounts into the sandbox) is up
// never gets status wired in for its whole lifetime.
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
	if len(m.windows) != 1 || m.windows[0].session != "explicit" {
		t.Errorf("expected spawn into explicit session, got %+v", m.windows)
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
	for _, want := range []string{"work", "40-implement", "/agentflow:implement 40"} {
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
	// agentflow workflows — short and uniform.
	if got := windowName("230", "refine", ""); got != "230-refine" {
		t.Errorf("numeric refine = %q, want 230-refine", got)
	}
	if got := windowName("255", "implement", ""); got != "255-implement" {
		t.Errorf("numeric implement = %q, want 255-implement", got)
	}
	if got := windowName("412", "design", ""); got != "412-design" {
		t.Errorf("numeric design = %q, want 412-design", got)
	}
	// Trailing context after a numeric id is ignored: the name is skill-only.
	if got := windowName("42 focus on the API layer", "refine", ""); got != "42-refine" {
		t.Errorf("id+context = %q, want 42-refine", got)
	}
	// A leading '#' on the id is stripped.
	if got := windowName("#1 focus on API", "design", ""); got != "1-design" {
		t.Errorf("hash id = %q, want 1-design", got)
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
	if !strings.Contains(w.cmd, "/agentflow:implement 42 focus on the API layer") {
		t.Errorf("command dropped context: %q", w.cmd)
	}
	// The window name is the skill only — trailing context does not leak in.
	if w.name != "42-implement" {
		t.Errorf("window name = %q, want 42-implement", w.name)
	}
}
