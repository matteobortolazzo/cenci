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

func (m *mockCtrl) CurrentSession() (string, error)          { return m.session, m.sessionErr }
func (m *mockCtrl) IsGroupedSession(string) (bool, error)    { return m.grouped, m.groupedErr }
func (m *mockCtrl) NewWindow(session, name, cmd string) error {
	m.windows = append(m.windows, winCall{session, name, cmd})
	return m.newWindowErr
}
func (m *mockCtrl) SetWindowOption(target, key, value string) error {
	m.options = append(m.options, optCall{target, key, value})
	return nil
}

// noConfigOpts returns Opts pinned to a non-existent config (built-ins only)
// with a stubbed gh lookup, so tests are deterministic.
func noConfigOpts(t *testing.T) Opts {
	t.Helper()
	return Opts{
		ConfigPath: filepath.Join(t.TempDir(), "none.json"),
		GHTitle:    func(string) string { return "" },
	}
}

func TestRunSpawnsWindowAndPinsName(t *testing.T) {
	m := &mockCtrl{session: "work"}
	opts := noConfigOpts(t)
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
	if w.name != "40-demo" {
		t.Errorf("name = %q, want 40-demo", w.name)
	}
	if !strings.Contains(w.cmd, "claude") || !strings.Contains(w.cmd, "/ccflow:implement 40") {
		t.Errorf("command = %q", w.cmd)
	}

	found := false
	for _, o := range m.options {
		if o.target == "work:40-demo" && o.key == "automatic-rename" && o.value == "off" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected automatic-rename off on work:40-demo, got %+v", m.options)
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
	for _, want := range []string{"work", "40-demo", "/ccflow:implement 40"} {
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
	if got := windowName("40", "demo", ""); got != "40-demo" {
		t.Errorf("numeric+slug = %q, want 40-demo", got)
	}
	if got := windowName("40", "", ""); got != "40" {
		t.Errorf("numeric bare = %q, want 40", got)
	}
	if got := windowName("40", "", "Add dark mode toggle"); got != "40-add-dark-mode-toggle" {
		t.Errorf("numeric+ghTitle = %q, want 40-add-dark-mode-toggle", got)
	}
	if got := windowName("feature-x", "", ""); got != "feature-x" {
		t.Errorf("non-numeric ticket = %q, want feature-x", got)
	}

	long := windowName("40", strings.Repeat("a", 100), "")
	if len([]rune(long)) > windowNameMaxLen {
		t.Errorf("windowName not capped: len=%d (%q)", len([]rune(long)), long)
	}
	if !strings.HasPrefix(long, "40-") {
		t.Errorf("cap dropped the numeric prefix: %q", long)
	}
}
