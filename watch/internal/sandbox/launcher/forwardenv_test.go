package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// -- ticket #1016: user-configurable host env-var forwarding ----------------
//
// `sandboxEnv.forward` in the user fleet config ($XDG_CONFIG_HOME/cenci/
// config.json) is a name-only allowlist of extra host env vars forwarded per
// exec. These tests pin the reader's failure taxonomy (forwardenv.go) and the
// merge into the secretEnvSet seam (secretenv.go): the tables are unit-level on
// purpose, because driving every malformed-config class through a launch would
// repeat the same exit-1 path dozens of times; the end-to-end exit codes and
// empty runtime call logs are pinned in watch/open_test.go and
// watch/audit_cmd_test.go.

// TestMain pins XDG_CONFIG_HOME to an empty temp dir for the whole package, so
// a developer's real fleet config can never change a golden (a real
// sandboxEnv.forward would add exec tokens and weakenings) or fail a suite (a
// malformed one is a hard error now). Tests that need a config call
// writeFleetConfig, whose t.Setenv overrides this for the one test
// (watch/docs/test-isolation.md, "Ambient config-dir isolation").
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cenci-launcher-xdg-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "launcher tests: creating the pinned XDG_CONFIG_HOME: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		fmt.Fprintf(os.Stderr, "launcher tests: pinning XDG_CONFIG_HOME: %v\n", err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// builtinForwardNames is the built-in forward order per agent, the baseline
// every "no config" case must equal.
func builtinForwardNames(agent string) []string {
	return builtinSecretEnvSet().forwardNames(agent)
}

// -- resolution: the cases that resolve to the built-in set -----------------

func TestLoadSecretEnvSet_ResolvesToBuiltinSetWithoutError(t *testing.T) {
	cases := []struct {
		name string
		body string // "" writes no file at all
	}{
		{"no fleet config file", ""},
		{"empty object", `{}`},
		{"other blocks but no sandboxEnv", `{"dispatch":{},"automerge":{},"planning":{"attended":true},"sandbox":false}`},
		{"sandboxEnv null", `{"sandboxEnv":null}`},
		{"sandboxEnv without forward", `{"sandboxEnv":{}}`},
		{"forward null", `{"sandboxEnv":{"forward":null}}`},
		{"forward empty array", `{"sandboxEnv":{"forward":[]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeFleetConfig(t, tc.body)

			set, err := loadSecretEnvSet()
			if err != nil {
				t.Fatalf("loadSecretEnvSet() error = %v, want the built-in set with no error", err)
			}
			if got := set.configuredNames(); len(got) != 0 {
				t.Errorf("configuredNames() = %v, want none", got)
			}
			for _, agent := range []string{"claude", "codex", "opencode"} {
				if got, want := set.forwardNames(agent), builtinForwardNames(agent); !slices.Equal(got, want) {
					t.Errorf("forwardNames(%q) = %v, want the built-in %v", agent, got, want)
				}
			}
		})
	}
}

func TestLoadSecretEnvSet_UndeterminableConfigPath_ResolvesToBuiltinSet(t *testing.T) {
	t.Run("seam returns empty", func(t *testing.T) {
		orig := sandboxEnvConfigPath
		sandboxEnvConfigPath = func() string { return "" }
		t.Cleanup(func() { sandboxEnvConfigPath = orig })

		set, err := loadSecretEnvSet()
		if err != nil {
			t.Fatalf("loadSecretEnvSet() error = %v, want the built-in set", err)
		}
		if got := set.forwardNames("claude"); !slices.Equal(got, builtinForwardNames("claude")) {
			t.Errorf("forwardNames(claude) = %v, want the built-in set", got)
		}
	})

	t.Run("no XDG_CONFIG_HOME and no home directory", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "")

		set, err := loadSecretEnvSet()
		if err != nil {
			t.Fatalf("loadSecretEnvSet() error = %v, want the built-in set (no path to read is not an error)", err)
		}
		if got := set.configuredNames(); len(got) != 0 {
			t.Errorf("configuredNames() = %v, want none", got)
		}
	})
}

func TestLoadSecretEnvSet_ReadsNamesFromTheFleetConfigInOrder(t *testing.T) {
	writeFleetConfig(t, `{"sandboxEnv":{"forward":["TAVILY_API_KEY","MY_THING","_underscore1"]}}`)

	set, err := loadSecretEnvSet()
	if err != nil {
		t.Fatalf("loadSecretEnvSet() error = %v", err)
	}
	want := []string{"TAVILY_API_KEY", "MY_THING", "_underscore1"}
	if got := set.configuredNames(); !slices.Equal(got, want) {
		t.Errorf("configuredNames() = %v, want %v (config order)", got, want)
	}
	if !set.Has("MY_THING") {
		t.Errorf("Has(MY_THING) = false, want true: a configured name is a secret whatever its suffix")
	}
}

// -- failure taxonomy: plain, path-bearing, non-usage errors ----------------

func TestLoadSecretEnvSet_MalformedConfig_HardErrorNamingPathAndKey(t *testing.T) {
	cases := []struct {
		name string
		body string
		// wantKey is the config key the message must name (empty: file-level
		// failure, only the path is required).
		wantKey string
		// wantText is a further substring the message must carry.
		wantText string
	}{
		{"unparsable JSON", `{not json`, "", "parsing"},
		{"top level array", `[]`, "", "top level must be an object"},
		{"top level null", `null`, "", "top level must be an object"},
		{"sandboxEnv array", `{"sandboxEnv":[]}`, "sandboxEnv", "must be an object"},
		{"sandboxEnv string", `{"sandboxEnv":"forward"}`, "sandboxEnv", "must be an object"},
		{"sandboxEnv number", `{"sandboxEnv":5}`, "sandboxEnv", "must be an object"},
		{"forward string", `{"sandboxEnv":{"forward":"NPM_TOKEN"}}`, "sandboxEnv.forward", "must be an array"},
		{"forward object", `{"sandboxEnv":{"forward":{}}}`, "sandboxEnv.forward", "must be an array"},
		{"forward number", `{"sandboxEnv":{"forward":7}}`, "sandboxEnv.forward", "must be an array"},
		{"element number", `{"sandboxEnv":{"forward":["OK_ONE",1]}}`, "sandboxEnv.forward", "[1] must be a string"},
		{"element null", `{"sandboxEnv":{"forward":[null]}}`, "sandboxEnv.forward", "[0] must be a string"},
		{"element array", `{"sandboxEnv":{"forward":[["A"]]}}`, "sandboxEnv.forward", "[0] must be a string"},
		{"element object", `{"sandboxEnv":{"forward":[{}]}}`, "sandboxEnv.forward", "[0] must be a string"},
		{"element bool", `{"sandboxEnv":{"forward":[true]}}`, "sandboxEnv.forward", "[0] must be a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeFleetConfig(t, tc.body)
			_ = requireConfigError(t, tc.wantKey, tc.wantText)
		})
	}
}

func TestLoadSecretEnvSet_UnreadableFile_HardErrorNamingPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// A directory where the file should be is an unreadable config that is not
	// "absent": ReadFile fails with something other than ErrNotExist.
	if err := os.MkdirAll(filepath.Join(dir, "cenci", "config.json"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_ = requireConfigError(t, "", "reading")
}

func TestLoadSecretEnvSet_InvalidNames_HardErrorNamingPathKeyAndEntry(t *testing.T) {
	cases := []struct {
		name  string
		entry string
		want  string // substring beyond the entry, the reason
	}{
		{"empty string", "", "not a valid environment variable name"},
		{"looks like a flag", "-e", "not a valid environment variable name"},
		{"argv injection", "--privileged", "not a valid environment variable name"},
		{"space", "MY VAR", "not a valid environment variable name"},
		{"leading digit", "1ABC", "not a valid environment variable name"},
		{"dash inside", "A-B", "not a valid environment variable name"},
		{"dot inside", "A.B", "not a valid environment variable name"},
		{"trailing newline", "FOO\n", "not a valid environment variable name"},
		{"TMUX_PANE", "TMUX_PANE", "reserved for the launcher"},
		{"CENCI_ATTENDED", "CENCI_ATTENDED", "reserved for the launcher"},
		{"CENCI_LAUNCH_ID", "CENCI_LAUNCH_ID", "reserved for the launcher"},
		{"CENCI_SANDBOX", "CENCI_SANDBOX", "reserved for the launcher"},
		{"CENCI_SANDBOX_AGENT", "CENCI_SANDBOX_AGENT", "reserved for the launcher"},
		{"any CENCI_ prefix", "CENCI_ANYTHING_ELSE", "reserved for the launcher"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeFleetConfig(t, fmt.Sprintf(`{"sandboxEnv":{"forward":["FINE_NAME",%q]}}`, tc.entry))
			err := requireConfigError(t, "sandboxEnv.forward", tc.want)
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", tc.entry)) {
				t.Errorf("error %q does not echo the offending entry %q (quoted)", err, tc.entry)
			}
		})
	}
}

func TestLoadSecretEnvSet_ReservedPrefixIsExactlyCENCIUnderscore(t *testing.T) {
	// Only the launcher-owned names are reserved: a name that merely starts with
	// the same letters, or differs in case, is an ordinary user variable.
	writeFleetConfig(t, `{"sandboxEnv":{"forward":["CENCIFOO","cenci_lower","TMUX_PANE_X","XCENCI_Y"]}}`)

	set, err := loadSecretEnvSet()
	if err != nil {
		t.Fatalf("loadSecretEnvSet() error = %v, want the names accepted", err)
	}
	if got, want := set.configuredNames(), []string{"CENCIFOO", "cenci_lower", "TMUX_PANE_X", "XCENCI_Y"}; !slices.Equal(got, want) {
		t.Errorf("configuredNames() = %v, want %v", got, want)
	}
}

func TestLoadSecretEnvSet_DuplicateName_HardErrorNamingTheDuplicate(t *testing.T) {
	writeFleetConfig(t, `{"sandboxEnv":{"forward":["NPM_TOKEN","OTHER_ONE","NPM_TOKEN"]}}`)

	err := requireConfigError(t, "sandboxEnv.forward", "duplicate")
	if !strings.Contains(err.Error(), `"NPM_TOKEN"`) {
		t.Errorf("error %q does not name the duplicated name NPM_TOKEN", err)
	}
}

// TestLoadSecretEnvSet_EntryWithEquals_RedactsEverythingAfterTheFirstEquals
// pins the name-only invariant for the error path (Q&A #1): a user who pastes
// NAME=value gets the entry reported as NAME=<redacted>; the text after the
// first "=" never reaches the error, so the error cannot leak a pasted secret.
func TestLoadSecretEnvSet_EntryWithEquals_RedactsEverythingAfterTheFirstEquals(t *testing.T) {
	const sentinel = "s3cr3t-sentinel-value"
	cases := []struct {
		name  string
		entry string
		want  string // the redacted form the message must carry, quoted
	}{
		{"FOO=bar", "FOO=bar", `"FOO=<redacted>"`},
		{"token style", "NPM_TOKEN=" + sentinel, `"NPM_TOKEN=<redacted>"`},
		{"second equals", "A=" + sentinel + "=" + sentinel + "2", `"A=<redacted>"`},
		{"empty value", "FOO=", `"FOO=<redacted>"`},
		{"empty name", "=" + sentinel, `"=<redacted>"`},
		{"invalid name and value", "MY VAR=" + sentinel, `"MY VAR=<redacted>"`},
		{"reserved name and value", "CENCI_ATTENDED=" + sentinel, `"CENCI_ATTENDED=<redacted>"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeFleetConfig(t, fmt.Sprintf(`{"sandboxEnv":{"forward":[%q]}}`, tc.entry))
			err := requireConfigError(t, "sandboxEnv.forward", "")

			// Strip the path before the leak check: it is host-derived and
			// unrelated to the entry.
			msg := strings.ReplaceAll(err.Error(), sandboxEnvConfigPath(), "<path>")
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error %q does not carry the redacted entry %s", msg, tc.want)
			}
			if !strings.Contains(msg, "<redacted>") {
				t.Errorf("error %q does not say the value was redacted", msg)
			}
			if after := tc.entry[strings.IndexByte(tc.entry, '=')+1:]; after != "" && strings.Contains(msg, after) {
				t.Errorf("error %q leaks the text after the first '=': %q", msg, after)
			}
		})
	}

	t.Run("FOO=bar names FOO and never bar", func(t *testing.T) {
		writeFleetConfig(t, `{"sandboxEnv":{"forward":["FOO=bar"]}}`)
		err := requireConfigError(t, "sandboxEnv.forward", "")
		msg := strings.ReplaceAll(err.Error(), sandboxEnvConfigPath(), "<path>")
		if !strings.Contains(msg, "FOO") || !strings.Contains(msg, "<redacted>") {
			t.Errorf("error %q must contain FOO and <redacted>", msg)
		}
		if strings.Contains(msg, "bar") {
			t.Errorf("error %q must not contain the value \"bar\"", msg)
		}
	})
}

// requireConfigError loads the set and requires the plain, path-bearing,
// non-usage hard error the taxonomy specifies (IsUsage false means exit 1, not
// exit 2), carrying wantKey and wantText when non-empty, plus the zero set.
func requireConfigError(t *testing.T, wantKey, wantText string) error {
	t.Helper()
	path := sandboxEnvConfigPath()
	set, err := loadSecretEnvSet()
	if err == nil {
		t.Fatalf("loadSecretEnvSet() = %+v, nil error; want a hard config error", set)
	}
	if IsUsage(err) {
		t.Errorf("error %q is a usage error (would exit 2), want a plain error (exit 1)", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the config file %q", err, path)
	}
	if wantKey != "" && !strings.Contains(err.Error(), wantKey) {
		t.Errorf("error %q does not name the key %q", err, wantKey)
	}
	if wantText != "" && !strings.Contains(err.Error(), wantText) {
		t.Errorf("error %q does not contain %q", err, wantText)
	}
	if len(set.configuredNames()) != 0 || set.Has("FINE_NAME") {
		t.Errorf("a failed load returned a non-zero set %+v; want the zero value (never a partial allowlist)", set)
	}
	return err
}

// -- merge: order and widening ----------------------------------------------

func TestResolveSecretEnvSet_MergeOrderAndWidening(t *testing.T) {
	cases := []struct {
		name       string
		configured []string
		want       map[string][]string // agent -> forwardNames
	}{
		{
			name:       "no configured names is the built-in set",
			configured: nil,
			want: map[string][]string{
				"claude":   {"CONTEXT7_API_KEY"},
				"codex":    {"CONTEXT7_API_KEY", "OPENAI_API_KEY"},
				"opencode": {"CONTEXT7_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
			},
		},
		{
			name:       "new names follow the built-ins in config order for every agent",
			configured: []string{"TAVILY_API_KEY", "MY_THING", "AAA_FIRST_ALPHABETICALLY"},
			want: map[string][]string{
				"claude":   {"CONTEXT7_API_KEY", "TAVILY_API_KEY", "MY_THING", "AAA_FIRST_ALPHABETICALLY"},
				"codex":    {"CONTEXT7_API_KEY", "OPENAI_API_KEY", "TAVILY_API_KEY", "MY_THING", "AAA_FIRST_ALPHABETICALLY"},
				"opencode": {"CONTEXT7_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "TAVILY_API_KEY", "MY_THING", "AAA_FIRST_ALPHABETICALLY"},
			},
		},
		{
			name:       "a built-in duplicate keeps its position and widens to every agent",
			configured: []string{"NEW_ONE", "OPENAI_API_KEY"},
			want: map[string][]string{
				"claude":   {"CONTEXT7_API_KEY", "OPENAI_API_KEY", "NEW_ONE"},
				"codex":    {"CONTEXT7_API_KEY", "OPENAI_API_KEY", "NEW_ONE"},
				"opencode": {"CONTEXT7_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "NEW_ONE"},
			},
		},
		{
			name:       "widening an opencode-only built-in reaches claude and codex",
			configured: []string{"ANTHROPIC_API_KEY"},
			want: map[string][]string{
				"claude":   {"CONTEXT7_API_KEY", "ANTHROPIC_API_KEY"},
				"codex":    {"CONTEXT7_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
				"opencode": {"CONTEXT7_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
			},
		},
		{
			name:       "an already-global built-in is listed once",
			configured: []string{"CONTEXT7_API_KEY", "MY_THING"},
			want: map[string][]string{
				"claude":   {"CONTEXT7_API_KEY", "MY_THING"},
				"codex":    {"CONTEXT7_API_KEY", "OPENAI_API_KEY", "MY_THING"},
				"opencode": {"CONTEXT7_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "MY_THING"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set, err := resolveSecretEnvSet(tc.configured)
			if err != nil {
				t.Fatalf("resolveSecretEnvSet(%v) error = %v", tc.configured, err)
			}
			for agent, want := range tc.want {
				if got := set.forwardNames(agent); !slices.Equal(got, want) {
					t.Errorf("forwardNames(%q) = %v, want %v", agent, got, want)
				}
			}
			for _, name := range tc.configured {
				if !set.Has(name) {
					t.Errorf("Has(%q) = false, want true for a configured name", name)
				}
			}
			if got := set.configuredNames(); !slices.Equal(got, tc.configured) && len(got)+len(tc.configured) != 0 {
				t.Errorf("configuredNames() = %v, want %v", got, tc.configured)
			}
		})
	}
}

func TestResolveSecretEnvSet_WideningNeverMutatesTheBuiltinSpecs(t *testing.T) {
	if _, err := resolveSecretEnvSet([]string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"}); err != nil {
		t.Fatalf("resolveSecretEnvSet: %v", err)
	}
	// A widened set must not leak into the shared built-in literal, or one
	// launch's config would widen every later set in the process.
	if got, want := builtinForwardNames("claude"), []string{"CONTEXT7_API_KEY"}; !slices.Equal(got, want) {
		t.Errorf("builtin forwardNames(claude) = %v after a widening resolve, want %v", got, want)
	}
	if got, want := builtinForwardNames("codex"), []string{"CONTEXT7_API_KEY", "OPENAI_API_KEY"}; !slices.Equal(got, want) {
		t.Errorf("builtin forwardNames(codex) = %v after a widening resolve, want %v", got, want)
	}
}

func TestResolveSecretEnvSet_ConfiguredNamesAreACopy(t *testing.T) {
	configured := []string{"MY_THING"}
	set, err := resolveSecretEnvSet(configured)
	if err != nil {
		t.Fatalf("resolveSecretEnvSet: %v", err)
	}
	configured[0] = "MUTATED"
	got := set.configuredNames()
	got[0] = "ALSO_MUTATED"
	if again := set.configuredNames(); !slices.Equal(again, []string{"MY_THING"}) {
		t.Errorf("configuredNames() = %v after the caller mutated its slices, want [MY_THING]", again)
	}
}

// TestResolveSecretEnvSet_RevalidatesWhatTheReaderValidates is the defense in
// depth behind the reader: the merge is the seam every caller reaches, so a
// name that bypassed the reader (a future second source) is still refused
// rather than reaching a runtime argv.
func TestResolveSecretEnvSet_RevalidatesWhatTheReaderValidates(t *testing.T) {
	cases := []struct {
		name       string
		configured []string
	}{
		{"flag-like", []string{"--privileged"}},
		{"empty", []string{""}},
		{"has equals", []string{"FOO=bar"}},
		{"reserved", []string{"TMUX_PANE"}},
		{"reserved prefix", []string{"CENCI_LAUNCH_ID"}},
		{"duplicate", []string{"MY_THING", "MY_THING"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set, err := resolveSecretEnvSet(tc.configured)
			if err == nil {
				t.Fatalf("resolveSecretEnvSet(%v) = %+v, nil error; want a validation error", tc.configured, set)
			}
			if IsUsage(err) {
				t.Errorf("error %q is a usage error, want a plain error", err)
			}
			if strings.Contains(err.Error(), "bar") {
				t.Errorf("error %q leaks the text after '='", err)
			}
		})
	}
}
