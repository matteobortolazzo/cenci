package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The decoded shape below is
//
//	{"installed":[{"pluginId":"cenci@cenci","version":"1.2.3","installed":true,"enabled":true}]}
//
// UNVERIFIED against the real Codex CLI: no `codex` binary was available when
// this fixture was written. `.installed[]` + `.pluginId` + `.version` are the
// fields install.sh and sandbox/lib/migrate-settings.sh already parse; the
// per-entry `installed` and `enabled` booleans come from the #1237 plan and
// are not read anywhere else in the repo. The probe decodes both as *bool and
// refuses unless each is explicitly true, so a wrong guess about their names
// fails closed (every host Codex launch refuses) rather than open.
const (
	testPluginVersion = "1.2.3"
	testWorkflow      = "ci-repair"
	testAdapter       = "skills/ci-repair/codex.md"
)

// pluginEntry renders one `.installed[]` entry; enabled/installed are rendered
// only when non-empty so a test can leave the field absent.
func pluginEntry(id, version, installed, enabled string) string {
	fields := []string{fmt.Sprintf(`"pluginId":%q`, id), fmt.Sprintf(`"version":%q`, version)}
	if installed != "" {
		fields = append(fields, `"installed":`+installed)
	}
	if enabled != "" {
		fields = append(fields, `"enabled":`+enabled)
	}
	return "{" + strings.Join(fields, ",") + "}"
}

func pluginList(entries ...string) string {
	return `{"installed":[` + strings.Join(entries, ",") + `]}`
}

func goodEntry() string { return pluginEntry("cenci@cenci", testPluginVersion, "true", "true") }

// fakeCodex is a throwaway `codex` shell script that logs its argv, prints a
// canned stdout/stderr and exits with a canned code.
type fakeCodex struct{ command, callLog string }

func newFakeCodex(t *testing.T, stdout, stderr string, exit int) fakeCodex {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "stdout.json")
	if err := os.WriteFile(out, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	f := fakeCodex{command: filepath.Join(dir, "codex"), callLog: filepath.Join(dir, "calls.log")}
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> '%s'\ncat '%s'\nprintf '%%s' '%s' >&2\nexit %d\n", f.callLog, out, stderr, exit)
	if err := os.WriteFile(f.command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f fakeCodex) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.callLog)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// installAdapter lays out <codexHome>/plugins/cache/cenci/cenci/<version>/<adapter>.
func installAdapter(t *testing.T, codexHome, version, adapter, content string) {
	t.Helper()
	path := filepath.Join(codexHome, "plugins", "cache", "cenci", "cenci", version, filepath.FromSlash(adapter))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// probeEnv isolates CODEX_HOME for one test and returns it.
func probeEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	return home
}

func TestProbeCodexAdapterAcceptsInstalledEnabledPluginWithAdapter(t *testing.T) {
	home := probeEnv(t)
	installAdapter(t, home, testPluginVersion, testAdapter, "# codex procedure\n")
	// Other plugins in the list, including a half-described one, are ignored.
	f := newFakeCodex(t, pluginList(
		pluginEntry("cenci-watch@cenci", "9.9.9", "", ""),
		goodEntry(),
	), "", 0)

	if err := probeCodexAdapter(f.command, testWorkflow, testAdapter); err != nil {
		t.Fatalf("probeCodexAdapter = %v, want nil", err)
	}
	if calls := f.calls(t); len(calls) != 1 || calls[0] != "plugin list --json" {
		t.Errorf("fake codex calls = %q, want exactly [plugin list --json]", calls)
	}
}

func TestProbeCodexAdapterAcceptsSkillsPluginRootWhenAdapterIsSkillMd(t *testing.T) {
	home := probeEnv(t)
	installAdapter(t, home, testPluginVersion, "skills/babysit/SKILL.md", "# babysit\n")
	f := newFakeCodex(t, pluginList(goodEntry()), "", 0)
	if err := probeCodexAdapter(f.command, "babysit", "skills/babysit/SKILL.md"); err != nil {
		t.Fatalf("probeCodexAdapter = %v, want nil", err)
	}
}

func TestProbeCodexAdapterFallsBackToHomeDotCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", home)
	installAdapter(t, filepath.Join(home, ".codex"), testPluginVersion, testAdapter, "# codex procedure\n")
	f := newFakeCodex(t, pluginList(goodEntry()), "", 0)
	if err := probeCodexAdapter(f.command, testWorkflow, testAdapter); err != nil {
		t.Fatalf("probeCodexAdapter with CODEX_HOME unset = %v, want nil via $HOME/.codex", err)
	}
}

// TestProbeCodexAdapterRefusals drives every fail-closed class through the
// real probe with a fake codex. Each class asserts its own message marker
// (watch/docs/error-handling.md #446) so a regression that collapses two
// classes into one message fails, and every refusal must wrap
// ErrAdapterUnavailable and name the plugin, workflow and remedy.
func TestProbeCodexAdapterRefusals(t *testing.T) {
	cases := []struct {
		name   string
		list   string // stdout of `codex plugin list --json`
		stderr string
		exit   int
		// setup prepares CODEX_HOME contents (default: adapter installed at testPluginVersion).
		setup   func(t *testing.T, home string)
		command string // overrides the fake binary path
		marker  string // class-specific message text
		remedy  string // extra remedy text the class must carry
		// absent is text belonging to a sibling class that this refusal must not
		// carry, so two classes that share generic words stay distinguishable.
		absent []string
	}{
		{
			name:   "not installed (absent from the list)",
			list:   pluginList(pluginEntry("other@cenci", "1.0.0", "true", "true")),
			marker: "not installed",
		},
		{
			name:   "not installed (empty installed array)",
			list:   `{"installed":[]}`,
			marker: "cenci@cenci is not installed in Codex",
		},
		{
			name:   "unrecognised shape (no installed key)",
			list:   `{}`,
			marker: "unrecognised plugin list shape (no top-level `installed` array)",
		},
		{
			name:   "unrecognised shape (plugins key instead of installed)",
			list:   `{"plugins":[` + goodEntry() + `]}`,
			marker: "unrecognised plugin list shape (no top-level `installed` array)",
		},
		{
			name:   "unrecognised shape (installed null)",
			list:   `{"installed":null}`,
			marker: "unrecognised plugin list shape (no top-level `installed` array)",
		},
		{
			name:   "not installed (installed:false)",
			list:   pluginList(pluginEntry("cenci@cenci", testPluginVersion, "false", "true")),
			marker: "not installed",
		},
		{
			name:   "disabled",
			list:   pluginList(pluginEntry("cenci@cenci", testPluginVersion, "true", "false")),
			marker: "is disabled",
			remedy: `plugins."cenci@cenci".enabled = true`,
		},
		{
			name:   "enabled field absent",
			list:   pluginList(pluginEntry("cenci@cenci", testPluginVersion, "true", "")),
			marker: `lacks an explicit "enabled"`,
		},
		{
			name:   "installed field absent",
			list:   pluginList(pluginEntry("cenci@cenci", testPluginVersion, "", "true")),
			marker: `lacks an explicit "installed"`,
		},
		{
			name:   "duplicate entries",
			list:   pluginList(goodEntry(), goodEntry()),
			marker: "multiple",
		},
		{
			name: "adapter file missing",
			list: pluginList(goodEntry()),
			setup: func(t *testing.T, home string) {
				installAdapter(t, home, testPluginVersion, "skills/other/codex.md", "x")
			},
			marker: "adapter " + testAdapter + " is missing from the installed plugin root",
			absent: []string{"is not readable"},
		},
		{
			name:   "adapter file empty",
			list:   pluginList(goodEntry()),
			setup:  func(t *testing.T, home string) { installAdapter(t, home, testPluginVersion, testAdapter, " \n") },
			marker: "is empty (workflow",
		},
		{
			name: "adapter is a directory",
			list: pluginList(goodEntry()),
			setup: func(t *testing.T, home string) {
				dir := filepath.Join(home, "plugins", "cache", "cenci", "cenci", testPluginVersion, filepath.FromSlash(testAdapter))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			marker: "is not a regular file",
		},
		{
			name:   "plugin root missing for the reported version",
			list:   pluginList(pluginEntry("cenci@cenci", "7.7.7", "true", "true")),
			marker: "is not readable (stat ",
			absent: []string{"is missing from", "not a directory"},
		},
		{
			name: "plugin root is a regular file",
			list: pluginList(goodEntry()),
			setup: func(t *testing.T, home string) {
				root := filepath.Join(home, "plugins", "cache", "cenci", "cenci", testPluginVersion)
				if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(root, []byte("not a plugin tree"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			marker: "is not readable (not a directory)",
			absent: []string{"is missing from"},
		},
		{
			name:   "non-zero exit with otherwise valid installed and enabled output",
			list:   pluginList(goodEntry()),
			stderr: "failed-after-printing",
			exit:   7,
			marker: "exited with status 7",
		},
		{
			name:   "malformed JSON",
			list:   `{"installed": [`,
			marker: "could not parse",
		},
		{
			name:   "empty output",
			list:   "",
			marker: "could not parse",
		},
		{
			name:   "non-zero exit carries stderr",
			list:   "",
			stderr: "boom-from-codex",
			exit:   3,
			marker: "boom-from-codex",
		},
		{
			name:    "binary not found",
			command: "/nonexistent-dir/codex",
			marker:  "could not run",
		},
		{
			name:   "traversal version",
			list:   pluginList(pluginEntry("cenci@cenci", "../../evil", "true", "true")),
			marker: "unsafe version",
		},
		{
			name:   "slash in version",
			list:   pluginList(pluginEntry("cenci@cenci", "1/2", "true", "true")),
			marker: "unsafe version",
		},
		{
			name:   "empty version",
			list:   pluginList(pluginEntry("cenci@cenci", "", "true", "true")),
			marker: "unsafe version",
		},
		{
			name:   "dot version",
			list:   pluginList(pluginEntry("cenci@cenci", "..", "true", "true")),
			marker: "unsafe version",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := probeEnv(t)
			if tc.setup != nil {
				tc.setup(t, home)
			} else {
				installAdapter(t, home, testPluginVersion, testAdapter, "# codex procedure\n")
			}
			command := tc.command
			if command == "" {
				command = newFakeCodex(t, tc.list, tc.stderr, tc.exit).command
			}
			err := probeCodexAdapter(command, testWorkflow, testAdapter)
			assertAdapterRefusal(t, err, tc.marker, tc.remedy)
			for _, bad := range tc.absent {
				if err != nil && strings.Contains(err.Error(), bad) {
					t.Errorf("err = %q, want it not to contain %q (a sibling refusal class)", err, bad)
				}
			}
		})
	}
}

func TestProbeCodexAdapterRefusesRelativeCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "relative/codex-home")
	f := newFakeCodex(t, pluginList(goodEntry()), "", 0)
	assertAdapterRefusal(t, probeCodexAdapter(f.command, testWorkflow, testAdapter), "CODEX_HOME must be an absolute path", "")
}

func TestProbeCodexAdapterRefusesUnknownHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", "")
	f := newFakeCodex(t, pluginList(goodEntry()), "", 0)
	assertAdapterRefusal(t, probeCodexAdapter(f.command, testWorkflow, testAdapter), "cannot locate the Codex home", "")
}

// TestProbeCodexAdapterUnrecognisedShapeIsNotReportedAsNotInstalled: an absent
// top-level `installed` key means the Codex JSON shape may have changed, which
// must not read as the (different, actionable) "not installed" refusal.
func TestProbeCodexAdapterUnrecognisedShapeIsNotReportedAsNotInstalled(t *testing.T) {
	home := probeEnv(t)
	installAdapter(t, home, testPluginVersion, testAdapter, "# codex procedure\n")
	f := newFakeCodex(t, `{}`, "", 0)
	err := probeCodexAdapter(f.command, testWorkflow, testAdapter)
	assertAdapterRefusal(t, err, "may have changed", "")
	if strings.Contains(err.Error(), "not installed") {
		t.Errorf("err = %q, want the shape refusal not to claim the plugin is not installed", err)
	}
}

// TestProbeCodexAdapterStripsTerminalEscapesFromStderr: Codex stderr is
// untrusted text that ends up in a refusal printed to a terminal, so control
// bytes (ESC, BEL, other C0/C1) are replaced and never reach the message.
func TestProbeCodexAdapterStripsTerminalEscapesFromStderr(t *testing.T) {
	// "\x9b" is a raw (invalid UTF-8) C1 byte; "\u009b" is U+009B encoded as
	// UTF-8 (C2 9B), which a terminal may also treat as CSI.
	msg, detail := probeStderrDetail(t, "\x1b[31mred\x1b]0;title\x07 tail\x9b\x01\u009b")
	for _, bad := range []string{"\x1b", "\x07", "\x9b", "\x01", "\u009b"} {
		if strings.Contains(msg, bad) {
			t.Errorf("err = %q, want control byte %q replaced", msg, bad)
		}
	}
	if !strings.Contains(detail, "?[31mred?]0;title? tail") {
		t.Errorf("stderr detail = %q, want control runes replaced by ? with the text kept", detail)
	}
}

// TestPrintable pins the sanitizer directly: C0 (ESC, BEL), DEL, C1 (UTF-8
// encoded and raw), and the bidi override are replaced; printable text,
// including non-ASCII letters, is left unchanged.
func TestPrintable(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"C0 ESC", "a\x1b[31mb", "a?[31mb"},
		{"C0 BEL", "bel\x07", "bel?"},
		{"DEL", "del\x7f", "del?"},
		{"C1 range as UTF-8", "c1\u0080\u0085\u009b\u009f", "c1????"},
		{"raw invalid UTF-8 byte", "raw\x9b", "raw?"},
		{"bidi override", "bidi\u202eevil", "bidi?evil"},
		{"normal printable unchanged", "plain text: é 日本 ~!", "plain text: é 日本 ~!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := printable(tc.in); got != tc.want {
				t.Errorf("printable(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestProbeCodexAdapterWaitDelayIsNotReportedAsTimeout: a child that outlives
// codex while holding its output pipes trips WaitDelay, not the deadline; the
// refusal must say so accurately and still fail closed.
func TestProbeCodexAdapterWaitDelayIsNotReportedAsTimeout(t *testing.T) {
	home := probeEnv(t)
	installAdapter(t, home, testPluginVersion, testAdapter, "# codex procedure\n")
	old := adapterProbeWaitDelay
	adapterProbeWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { adapterProbeWaitDelay = old })

	dir := t.TempDir()
	command := filepath.Join(dir, "codex")
	// The backgrounded sleep inherits the stdout/stderr pipes and outlives the
	// shell, which exits 0.
	if err := os.WriteFile(command, []byte("#!/bin/sh\nsleep 3 &\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := probeCodexAdapter(command, testWorkflow, testAdapter)
	assertAdapterRefusal(t, err, "kept its output pipes open", "")
	if strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %q, want the WaitDelay refusal not to claim a probe timeout", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want no DeadlineExceeded for a WaitDelay refusal", err)
	}
}

// TestProbeCodexAdapterTimesOutClosed: a hung `codex plugin list` is bounded
// by the probe timeout and refuses (fail closed) with its own marker.
func TestProbeCodexAdapterTimesOutClosed(t *testing.T) {
	home := probeEnv(t)
	installAdapter(t, home, testPluginVersion, testAdapter, "# codex procedure\n")
	old := adapterProbeTimeout
	adapterProbeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { adapterProbeTimeout = old })

	dir := t.TempDir()
	command := filepath.Join(dir, "codex")
	// exec replaces the shell so the kill lands on sleep itself
	// (watch/docs/go-gotchas.md, fake subprocess shims).
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := probeCodexAdapter(command, testWorkflow, testAdapter)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("probe took %v, want it bounded by the timeout", elapsed)
	}
	assertAdapterRefusal(t, err, "timed out", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to wrap context.DeadlineExceeded (watch/docs/go-gotchas.md bounded-subprocess rule)", err)
	}
}

// probeStderrDetail runs the probe against a fake codex that exits non-zero
// with the given stderr and returns the refusal message plus the stderr text
// the probe copied into it (between "status <n>: " and " (workflow").
func probeStderrDetail(t *testing.T, stderr string) (msg, detail string) {
	t.Helper()
	home := probeEnv(t)
	installAdapter(t, home, testPluginVersion, testAdapter, "# codex procedure\n")
	f := newFakeCodex(t, "", stderr, 3)
	err := probeCodexAdapter(f.command, testWorkflow, testAdapter)
	assertAdapterRefusal(t, err, "exited with status 3", "")
	msg = err.Error()
	_, after, ok := strings.Cut(msg, "exited with status 3: ")
	if !ok {
		t.Fatalf("err = %q, want it to carry the stderr after the exit status", msg)
	}
	detail, _, ok = strings.Cut(after, " (workflow")
	if !ok {
		t.Fatalf("err = %q, want the stderr detail to be followed by the workflow", msg)
	}
	return msg, detail
}

// TestProbeCodexAdapterBoundsAndTrimsStderr: a non-zero exit copies only a
// trimmed, bounded, one-line slice of stderr into the refusal. The fake emits
// far more than maxProbeStderr bytes with a marker at the start and another
// well past the cut, surrounded by blank lines and padding.
func TestProbeCodexAdapterBoundsAndTrimsStderr(t *testing.T) {
	const (
		leading  = "LEADING-MARKER"
		trailing = "TRAILING-MARKER"
		suffix   = "...(truncated)"
	)
	stderr := "\n  \n   " + leading + "\n" + strings.Repeat("filler line\n", 5*maxProbeStderr) + trailing + " \n\n  "
	if len(stderr) < 10*maxProbeStderr {
		t.Fatalf("fixture stderr is %d bytes, want it far larger than the %d-byte bound", len(stderr), maxProbeStderr)
	}

	msg, detail := probeStderrDetail(t, stderr)

	if !strings.HasPrefix(detail, leading) {
		t.Errorf("stderr detail = %q, want it trimmed so it starts with %q", detail, leading)
	}
	if !strings.HasSuffix(detail, suffix) {
		t.Errorf("stderr detail = %q, want it to end with %q", detail, suffix)
	}
	if strings.Contains(msg, trailing) {
		t.Errorf("err = %q, want the stderr past the bound (%q) cut off", msg, trailing)
	}
	if limit := maxProbeStderr + len(suffix); len(detail) > limit {
		t.Errorf("stderr detail is %d bytes, want at most %d (bound plus suffix)", len(detail), limit)
	}
	if strings.ContainsAny(detail, "\n\r") {
		t.Errorf("stderr detail = %q, want it collapsed to one line", detail)
	}
}

// TestProbeCodexAdapterTrimsShortStderrWithoutTruncating: stderr within the
// bound is passed through trimmed, with no truncation suffix.
func TestProbeCodexAdapterTrimsShortStderrWithoutTruncating(t *testing.T) {
	msg, detail := probeStderrDetail(t, "\n  \n  short failure\n  detail  \n\n ")
	if detail != "short failure detail" {
		t.Errorf("stderr detail = %q, want %q", detail, "short failure detail")
	}
	if strings.Contains(msg, "(truncated)") {
		t.Errorf("err = %q, want no truncation marker for stderr within the bound", msg)
	}
}

// TestProbeCodexAdapterTruncatesStderrOnRuneBoundary: when the bound would
// land inside a multi-byte rune, the cut backs up so the message stays valid
// UTF-8. The leading ASCII byte shifts every 2-byte rune so the byte at the
// bound is a continuation byte.
func TestProbeCodexAdapterTruncatesStderrOnRuneBoundary(t *testing.T) {
	msg, detail := probeStderrDetail(t, "a"+strings.Repeat("é", 2*maxProbeStderr))
	if !utf8.ValidString(msg) {
		t.Errorf("err = %q, want valid UTF-8 after truncation", msg)
	}
	if !strings.HasSuffix(detail, "...(truncated)") {
		t.Errorf("stderr detail = %q, want a truncation marker", detail)
	}
}

// assertAdapterRefusal checks the sentinel plus the content every refusal
// must carry: the plugin, the workflow, the remedy, and the class marker.
func assertAdapterRefusal(t *testing.T, err error, marker, extraRemedy string) {
	t.Helper()
	if err == nil {
		t.Fatal("probe returned nil, want a refusal")
	}
	if !errors.Is(err, ErrAdapterUnavailable) {
		t.Errorf("err = %v, want errors.Is(_, ErrAdapterUnavailable)", err)
	}
	if errors.Is(err, ErrWindowSpawned) {
		t.Errorf("err = %v, must never wrap ErrWindowSpawned (nothing was spawned)", err)
	}
	msg := err.Error()
	// go vet cannot check refuseAdapter's format string, so a mangled verb or
	// argument count would show up here as a "%!" fragment.
	if strings.Contains(msg, "%!") {
		t.Errorf("err = %q, want no mangled format verb (%%!)", msg)
	}
	wants := []string{marker, "cenci@cenci", testWorkflow, "codex plugin add cenci@cenci"}
	if extraRemedy != "" {
		wants = append(wants, extraRemedy)
	}
	for _, want := range wants {
		if !strings.Contains(msg, want) {
			t.Errorf("err = %q, want it to contain %q", msg, want)
		}
	}
}
