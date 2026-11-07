package modelswitch_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/modelswitch"
)

const (
	sonnet = "claude-sonnet-5-5"
	opus   = "claude-opus-5-5"
)

// isolate points the store at a fresh temp state home and clears the ambient
// session id (both are set in cenci-launched sessions; watch/docs/test-isolation.md).
// It returns the store directory a writer would use.
func isolate(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	return modelswitch.Dir()
}

func mainSwitch(from, to string) modelswitch.Switch {
	return modelswitch.Switch{Source: "auto", From: from, To: to}
}

func mustRead(t *testing.T, dir, session string) modelswitch.Result {
	t.Helper()
	return modelswitch.Read(dir, session)
}

func TestDirResolvesFromXDGStateHome(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	want := filepath.Join(state, "cenci", "model-switch")
	if got := modelswitch.Dir(); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestDirFallsBackToHomeForRelativeStateHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "relative/state")
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	want := filepath.Join(home, ".local", "state", "cenci", "model-switch")
	if got := modelswitch.Dir(); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestRecordedSwitchesReadBackInOrder(t *testing.T) {
	dir := isolate(t)
	base := time.Unix(1_700_000_000, 0)
	modelswitch.Record(dir, "sess-1", modelswitch.Switch{Source: "auto", From: sonnet, To: opus}, base)
	modelswitch.Record(dir, "sess-1", modelswitch.Switch{Source: "sdk", From: opus, To: sonnet, Subagent: true, AgentType: "cenci:reviewer"}, base.Add(time.Minute))
	modelswitch.Record(dir, "sess-1", modelswitch.Switch{Source: "command", From: sonnet, To: opus}, base.Add(2*time.Minute))

	res := mustRead(t, dir, "sess-1")
	if res.State != modelswitch.StateOK {
		t.Fatalf("state = %v (%s), want ok", res.State, res.Reason)
	}
	want := []modelswitch.Switch{
		{At: base.Unix(), Source: "auto", From: sonnet, To: opus},
		{At: base.Add(time.Minute).Unix(), Source: "sdk", From: opus, To: sonnet, Subagent: true, AgentType: "cenci:reviewer"},
		{At: base.Add(2 * time.Minute).Unix(), Source: "command", From: sonnet, To: opus},
	}
	if len(res.Switches) != len(want) {
		t.Fatalf("got %d switches, want %d: %+v", len(res.Switches), len(want), res.Switches)
	}
	for i := range want {
		if res.Switches[i] != want[i] {
			t.Errorf("switch %d = %+v, want %+v", i, res.Switches[i], want[i])
		}
	}
	if res.More != 0 {
		t.Errorf("more = %d, want 0", res.More)
	}
}

func TestSwitchesAreKeptPerSession(t *testing.T) {
	dir := isolate(t)
	modelswitch.Record(dir, "sess-a", mainSwitch(sonnet, opus), time.Now())
	modelswitch.Record(dir, "sess-b", mainSwitch(opus, sonnet), time.Now())

	a, b := mustRead(t, dir, "sess-a"), mustRead(t, dir, "sess-b")
	if len(a.Switches) != 1 || a.Switches[0].To != opus {
		t.Errorf("sess-a = %+v", a)
	}
	if len(b.Switches) != 1 || b.Switches[0].To != sonnet {
		t.Errorf("sess-b = %+v", b)
	}
}

func TestPastTheCapSwitchesAreCountedNotDropped(t *testing.T) {
	dir := isolate(t)
	for i := 0; i < modelswitch.MaxSwitches+2; i++ {
		modelswitch.Record(dir, "sess-1", mainSwitch(fmt.Sprintf("model-%d", i), fmt.Sprintf("model-%d", i+1)), time.Now())
	}
	res := mustRead(t, dir, "sess-1")
	if res.State != modelswitch.StateOK {
		t.Fatalf("state = %v (%s)", res.State, res.Reason)
	}
	if len(res.Switches) != modelswitch.MaxSwitches {
		t.Fatalf("kept %d switches, want %d", len(res.Switches), modelswitch.MaxSwitches)
	}
	if res.More != 2 {
		t.Errorf("more = %d, want 2", res.More)
	}
	for i, sw := range res.Switches {
		if want := fmt.Sprintf("model-%d", i); sw.From != want {
			t.Errorf("switch %d from = %q, want %q (order must be preserved)", i, sw.From, want)
		}
	}
}

func TestInvalidModelStringsAreRejectedBeforeStorage(t *testing.T) {
	cases := map[string]string{
		"escape sequence":  "claude\x1b[31m-opus",
		"newline":          "claude\nopus",
		"carriage return":  "claude\ropus",
		"nul":              "claude\x00opus",
		"empty":            "",
		"over 128 chars":   strings.Repeat("a", 129),
		"space":            "claude opus",
		"slash":            "../claude",
		"non ascii":        "claudé",
		"pango markup":     "<b>opus</b>",
		"shell metachars":  "$(id)",
		"already the mark": modelswitch.InvalidModel,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			dir := isolate(t)
			if got := modelswitch.SanitizeModel(raw); got != modelswitch.InvalidModel {
				t.Errorf("SanitizeModel(%q) = %q, want the placeholder", raw, got)
			}
			modelswitch.Record(dir, "sess-1", modelswitch.Switch{Source: "auto", From: raw, To: opus}, time.Now())

			data, err := os.ReadFile(filepath.Join(dir, "sess-1"))
			if err != nil {
				t.Fatalf("read store: %v", err)
			}
			if raw != "" && raw != modelswitch.InvalidModel && strings.Contains(string(data), raw) {
				t.Errorf("raw invalid value reached the store: %q", data)
			}
			res := mustRead(t, dir, "sess-1")
			if res.State != modelswitch.StateOK || len(res.Switches) != 1 {
				t.Fatalf("an invalid model must still be recorded, got %+v", res)
			}
			if res.Switches[0].From != modelswitch.InvalidModel || res.Switches[0].To != opus {
				t.Errorf("switch = %+v, want placeholder -> %s", res.Switches[0], opus)
			}
			line := res.Switches[0].Line()
			if raw != "" && raw != modelswitch.InvalidModel && strings.Contains(line, raw) {
				t.Errorf("raw invalid value reached the rendered line: %q", line)
			}
			if !strings.Contains(line, modelswitch.InvalidModel) {
				t.Errorf("line %q does not show the placeholder", line)
			}
		})
	}
}

func TestValidModelStringsPassThrough(t *testing.T) {
	for _, m := range []string{sonnet, opus, "claude-opus-5-5[1m]", "us.anthropic.claude-opus-5:0", strings.Repeat("a", 128)} {
		if got := modelswitch.SanitizeModel(m); got != m {
			t.Errorf("SanitizeModel(%q) = %q, want unchanged", m, got)
		}
	}
}

func TestInvalidAgentTypeAndSourceBecomePlaceholders(t *testing.T) {
	for _, raw := range []string{"", "bad type", "x\x1b[0m", strings.Repeat("a", 65), "a\nb"} {
		if got := modelswitch.SanitizeAgentType(raw); got != modelswitch.UnknownAgentType {
			t.Errorf("SanitizeAgentType(%q) = %q, want %q", raw, got, modelswitch.UnknownAgentType)
		}
	}
	if got := modelswitch.SanitizeAgentType("cenci:reviewer"); got != "cenci:reviewer" {
		t.Errorf("a plugin-qualified agent type must pass, got %q", got)
	}
	for _, raw := range []string{"", "a b", "x\x1b", strings.Repeat("a", 33)} {
		if got := modelswitch.SanitizeSource(raw); got != modelswitch.UnknownSource {
			t.Errorf("SanitizeSource(%q) = %q, want %q", raw, got, modelswitch.UnknownSource)
		}
	}

	dir := isolate(t)
	modelswitch.Record(dir, "sess-1", modelswitch.Switch{Source: "x y\x1b", From: sonnet, To: opus, Subagent: true, AgentType: "evil\x1b[0m type"}, time.Now())
	res := mustRead(t, dir, "sess-1")
	if res.State != modelswitch.StateOK || len(res.Switches) != 1 {
		t.Fatalf("got %+v", res)
	}
	sw := res.Switches[0]
	if sw.AgentType != modelswitch.UnknownAgentType || sw.Source != modelswitch.UnknownSource {
		t.Errorf("switch = %+v, want placeholder agent type and source", sw)
	}
	if got := sw.Line(); got != "Model switch: "+sonnet+" → "+opus+" (subagent unknown)" {
		t.Errorf("line = %q", got)
	}
}

func TestFormatLineLabelsOnlyAutoSwitchesAsFallback(t *testing.T) {
	cases := []struct {
		name string
		sw   modelswitch.Switch
		want string
	}{
		{"auto main", modelswitch.Switch{Source: "auto", From: sonnet, To: opus}, "Model switch: claude-sonnet-5-5 → claude-opus-5-5 (main session) — fallback"},
		{"auto subagent", modelswitch.Switch{Source: "auto", From: sonnet, To: opus, Subagent: true, AgentType: "cenci:reviewer"}, "Model switch: claude-sonnet-5-5 → claude-opus-5-5 (subagent cenci:reviewer) — fallback"},
		{"sdk is neutral", modelswitch.Switch{Source: "sdk", From: sonnet, To: opus}, "Model switch: claude-sonnet-5-5 → claude-opus-5-5 (main session)"},
		{"command is neutral", modelswitch.Switch{Source: "command", From: sonnet, To: opus}, "Model switch: claude-sonnet-5-5 → claude-opus-5-5 (main session)"},
		{"missing source is neutral", modelswitch.Switch{From: sonnet, To: opus}, "Model switch: claude-sonnet-5-5 → claude-opus-5-5 (main session)"},
		{"AUTO case is not auto", modelswitch.Switch{Source: "AUTO", From: sonnet, To: opus}, "Model switch: claude-sonnet-5-5 → claude-opus-5-5 (main session)"},
	}
	dir := isolate(t)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sw.Line(); got != tc.want {
				t.Errorf("Line() = %q, want %q", got, tc.want)
			}
			// The label must survive the store round trip.
			session := fmt.Sprintf("sess-%d", i)
			modelswitch.Record(dir, session, tc.sw, time.Now())
			res := mustRead(t, dir, session)
			if len(res.Switches) != 1 {
				t.Fatalf("got %+v", res)
			}
			if got := res.Switches[0].Line(); got != tc.want {
				t.Errorf("round-tripped Line() = %q, want %q", got, tc.want)
			}
		})
	}
	if got := modelswitch.FormatMore(3); got != "Model switch: +3 more" {
		t.Errorf("FormatMore(3) = %q", got)
	}
}

func TestReadDistinguishesNoRecordFromUnknown(t *testing.T) {
	t.Run("store absent", func(t *testing.T) {
		dir := isolate(t)
		if res := mustRead(t, dir, "sess-1"); res.State != modelswitch.StateNone {
			t.Errorf("state = %v (%s), want none", res.State, res.Reason)
		}
	})
	t.Run("store present without the session", func(t *testing.T) {
		dir := isolate(t)
		modelswitch.Record(dir, "other", mainSwitch(sonnet, opus), time.Now())
		if res := mustRead(t, dir, "sess-1"); res.State != modelswitch.StateNone {
			t.Errorf("state = %v (%s), want none", res.State, res.Reason)
		}
	})
	t.Run("reading never creates the store", func(t *testing.T) {
		dir := isolate(t)
		mustRead(t, dir, "sess-1")
		if _, err := os.Lstat(filepath.Dir(dir)); !os.IsNotExist(err) {
			t.Errorf("reader created the state directory (err = %v)", err)
		}
	})
}

func TestReadReportsUnknownForStoreErrors(t *testing.T) {
	seed := func(t *testing.T) string {
		dir := isolate(t)
		modelswitch.Record(dir, "sess-1", mainSwitch(sonnet, opus), time.Now())
		return dir
	}
	cases := []struct {
		name  string
		setup func(t *testing.T) (dir, session string)
	}{
		{"unresolvable store directory", func(t *testing.T) (string, string) { return "", "sess-1" }},
		{"empty session id", func(t *testing.T) (string, string) { return seed(t), "" }},
		{"path traversal session id", func(t *testing.T) (string, string) { return seed(t), "../sess-1" }},
		{"session id with a separator", func(t *testing.T) (string, string) { return seed(t), "a/b" }},
		{"dot session id", func(t *testing.T) (string, string) { return seed(t), ".." }},
		{"session id colliding with the marker suffix", func(t *testing.T) (string, string) { return seed(t), "sess-1.incomplete" }},
		{"untrusted group/other-writable store directory", func(t *testing.T) (string, string) {
			dir := seed(t)
			if err := os.Chmod(dir, 0o777); err != nil {
				t.Fatal(err)
			}
			return dir, "sess-1"
		}},
		{"untrusted state directory", func(t *testing.T) (string, string) {
			dir := seed(t)
			if err := os.Chmod(filepath.Dir(dir), 0o777); err != nil {
				t.Fatal(err)
			}
			return dir, "sess-1"
		}},
		{"store directory is a symlink", func(t *testing.T) (string, string) {
			dir := seed(t)
			moved := dir + ".real"
			if err := os.Rename(dir, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, dir); err != nil {
				t.Fatal(err)
			}
			return dir, "sess-1"
		}},
		{"corrupt record", func(t *testing.T) (string, string) {
			dir := seed(t)
			writeFile(t, filepath.Join(dir, "sess-1"), "this is not a record\n")
			return dir, "sess-1"
		}},
		{"older format record without a source", func(t *testing.T) (string, string) {
			dir := seed(t)
			writeFile(t, filepath.Join(dir, "sess-1"), "1700000000 claude-sonnet-5-5 claude-opus-5-5 main\n")
			return dir, "sess-1"
		}},
		{"record with an injected control character", func(t *testing.T) (string, string) {
			dir := seed(t)
			writeFile(t, filepath.Join(dir, "sess-1"), "1700000000 auto claude-sonnet-5-5 claude\x1b[31m-opus main\n")
			return dir, "sess-1"
		}},
		{"empty record", func(t *testing.T) (string, string) {
			dir := seed(t)
			writeFile(t, filepath.Join(dir, "sess-1"), "")
			return dir, "sess-1"
		}},
		{"record with more lines than the cap", func(t *testing.T) (string, string) {
			dir := seed(t)
			writeFile(t, filepath.Join(dir, "sess-1"), strings.Repeat("1700000000 auto a b main\n", modelswitch.MaxSwitches+1))
			return dir, "sess-1"
		}},
		{"oversized record", func(t *testing.T) (string, string) {
			dir := seed(t)
			writeFile(t, filepath.Join(dir, "sess-1"), strings.Repeat("x", 1<<20))
			return dir, "sess-1"
		}},
		{"record is a symlink", func(t *testing.T) (string, string) {
			dir := seed(t)
			target := filepath.Join(t.TempDir(), "elsewhere")
			writeFile(t, target, "1700000000 auto a b main\n")
			if err := os.Remove(filepath.Join(dir, "sess-1")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, "sess-1")); err != nil {
				t.Fatal(err)
			}
			return dir, "sess-1"
		}},
		{"incomplete marker next to a valid record", func(t *testing.T) (string, string) {
			dir := seed(t)
			writeFile(t, filepath.Join(dir, "sess-1.incomplete"), "")
			return dir, "sess-1"
		}},
		{"incomplete marker without a record", func(t *testing.T) (string, string) {
			dir := isolate(t)
			modelswitch.Record(dir, "other", mainSwitch(sonnet, opus), time.Now())
			writeFile(t, filepath.Join(dir, "sess-1.incomplete"), "")
			return dir, "sess-1"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, session := tc.setup(t)
			res := modelswitch.Read(dir, session)
			if res.State != modelswitch.StateUnknown {
				t.Fatalf("state = %v, want unknown (switches %+v)", res.State, res.Switches)
			}
			if res.Reason == "" {
				t.Error("an unknown result must carry a reason")
			}
			if dir != "" && strings.Contains(res.Reason, filepath.Dir(dir)) {
				t.Errorf("reason leaks a path: %q", res.Reason)
			}
			if len(res.Switches) != 0 {
				t.Errorf("an unknown result must not carry switches: %+v", res.Switches)
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRecordLeavesIncompleteMarkerWhenTheWriteFails(t *testing.T) {
	dir := isolate(t)
	modelswitch.Record(dir, "other", mainSwitch(sonnet, opus), time.Now())
	// A directory where the record file belongs makes the append fail after
	// the session id and the store directory validated.
	if err := os.Mkdir(filepath.Join(dir, "sess-1"), 0o700); err != nil {
		t.Fatal(err)
	}

	modelswitch.Record(dir, "sess-1", mainSwitch(sonnet, opus), time.Now())

	if _, err := os.Lstat(filepath.Join(dir, "sess-1.incomplete")); err != nil {
		t.Fatalf("expected the .incomplete marker after a failed write: %v", err)
	}
	if res := mustRead(t, dir, "sess-1"); res.State != modelswitch.StateUnknown {
		t.Errorf("state = %v, want unknown after a failed write", res.State)
	}
	if res := mustRead(t, dir, "other"); res.State != modelswitch.StateOK {
		t.Errorf("an unrelated session must stay readable, got %v", res.State)
	}
}

func TestRecordOntoACorruptRecordMarksItIncompleteInsteadOfOverwriting(t *testing.T) {
	dir := isolate(t)
	modelswitch.Record(dir, "sess-1", mainSwitch(sonnet, opus), time.Now())
	writeFile(t, filepath.Join(dir, "sess-1"), "garbage\n")

	modelswitch.Record(dir, "sess-1", mainSwitch(opus, sonnet), time.Now())

	if res := mustRead(t, dir, "sess-1"); res.State != modelswitch.StateUnknown {
		t.Errorf("state = %v, want unknown: a corrupt record must never be silently replaced", res.State)
	}
}

func TestRecordRefusesAnUntrustedStore(t *testing.T) {
	dir := isolate(t)
	modelswitch.Record(dir, "sess-1", mainSwitch(sonnet, opus), time.Now())
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	modelswitch.Record(dir, "sess-2", mainSwitch(sonnet, opus), time.Now())

	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("a writer touched an untrusted store: %d entries before, %d after", len(before), len(after))
	}
	if _, err := os.Lstat(filepath.Join(dir, "sess-2.incomplete")); err == nil {
		t.Error("a marker was created inside an untrusted store")
	}
}

func TestRecordIgnoresInvalidSessionIds(t *testing.T) {
	dir := isolate(t)
	for _, session := range []string{"", ".", "..", "../escape", "a/b", ".hidden", ".lock", "x.incomplete", strings.Repeat("a", 129)} {
		modelswitch.Record(dir, session, mainSwitch(sonnet, opus), time.Now())
	}
	if _, err := os.Lstat(filepath.Dir(dir)); err == nil {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.Name() != ".lock" {
				t.Errorf("an invalid session id created %q", e.Name())
			}
		}
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(dir), "escape")); err == nil {
		t.Error("a path traversal session id escaped the store directory")
	}
}

func TestRecordPrunesSessionsOlderThanFourteenDays(t *testing.T) {
	dir := isolate(t)
	modelswitch.Record(dir, "old", mainSwitch(sonnet, opus), time.Now())
	modelswitch.Record(dir, "recent", mainSwitch(sonnet, opus), time.Now())
	writeFile(t, filepath.Join(dir, "old.incomplete"), "")
	setAge(t, filepath.Join(dir, "old"), 15*24*time.Hour)
	setAge(t, filepath.Join(dir, "old.incomplete"), 15*24*time.Hour)
	setAge(t, filepath.Join(dir, "recent"), 13*24*time.Hour)

	modelswitch.Record(dir, "trigger", mainSwitch(sonnet, opus), time.Now())

	for _, gone := range []string{"old", "old.incomplete"} {
		if _, err := os.Lstat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s older than 14 days must be pruned (err = %v)", gone, err)
		}
	}
	for _, kept := range []string{"recent", "trigger"} {
		if _, err := os.Lstat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("%s must be kept: %v", kept, err)
		}
	}
}

func TestRecordKeepsAtMost256SessionFiles(t *testing.T) {
	dir := isolate(t)
	modelswitch.Record(dir, "seed", mainSwitch(sonnet, opus), time.Now())
	now := time.Now()
	for i := 0; i < modelswitch.MaxSessions+4; i++ {
		name := fmt.Sprintf("bulk-%03d", i)
		writeFile(t, filepath.Join(dir, name), "1700000000 auto a b main\n")
		// bulk-000 is the oldest, all well inside the 14 day window.
		if err := os.Chtimes(filepath.Join(dir, name), now, now.Add(-time.Duration(modelswitch.MaxSessions+4-i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	modelswitch.Record(dir, "newest", mainSwitch(sonnet, opus), time.Now())

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var sessions []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") && !strings.HasSuffix(e.Name(), ".incomplete") {
			sessions = append(sessions, e.Name())
		}
	}
	if len(sessions) > modelswitch.MaxSessions {
		t.Errorf("%d session files remain, want at most %d", len(sessions), modelswitch.MaxSessions)
	}
	if _, err := os.Lstat(filepath.Join(dir, "newest")); err != nil {
		t.Errorf("the session just written must never be pruned: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "bulk-000")); !os.IsNotExist(err) {
		t.Errorf("the oldest session file must be pruned first (err = %v)", err)
	}
}

func setAge(t *testing.T, path string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentWritersLoseNoSwitch(t *testing.T) {
	dir := isolate(t)
	const writers = 24
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			modelswitch.Record(dir, "sess-1", mainSwitch(sonnet, opus), time.Now())
		}()
	}
	wg.Wait()

	res := mustRead(t, dir, "sess-1")
	if res.State != modelswitch.StateOK {
		t.Fatalf("state = %v (%s), want ok", res.State, res.Reason)
	}
	if got := len(res.Switches) + res.More; got != writers {
		t.Errorf("recorded %d switches (+%d more), want %d in total", len(res.Switches), res.More, writers)
	}
	if len(res.Switches) != modelswitch.MaxSwitches {
		t.Errorf("kept %d switches, want the cap %d", len(res.Switches), modelswitch.MaxSwitches)
	}
	if _, err := os.Lstat(filepath.Join(dir, "sess-1.incomplete")); err == nil {
		t.Error("concurrent writers must not leave an .incomplete marker")
	}
}

func TestRecordedFilesAreOwnerOnly(t *testing.T) {
	dir := isolate(t)
	modelswitch.Record(dir, "sess-1", mainSwitch(sonnet, opus), time.Now())
	info, err := os.Stat(filepath.Join(dir, "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("record mode = %o, want owner-only", perm)
	}
	dinfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dinfo.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("store directory mode = %o, want 0700", perm)
	}
}
