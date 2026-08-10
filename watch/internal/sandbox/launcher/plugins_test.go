package launcher

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// -- RepoSandboxPlugins ---------------------------------------------------
//
// NOTE (red phase): RepoSandboxPlugins, ResolveSandboxPlugins,
// DefaultSandboxPlugins, sandboxPluginsEnvValue, and sandboxPluginsDiffer do
// not exist yet -- they land in plugins.go in the next, non-red phase
// (ticket #1002's Implementation Order step 2). Every reference to them
// below is therefore a compile error until that lands; that is the intended
// red-phase state (mirroring dind_test.go's own RepoDindConfig precedent),
// not a bug to fix by stubbing the implementation in this file.

// TestRepoSandboxPlugins_ValueAndErrorClasses pins RepoSandboxPlugins'
// .cenci/config.json "sandbox.plugins" contract (#1002), mirroring
// RepoDindConfig's exact two-stage decode/failure taxonomy (dind_test.go's
// TestRepoDindConfig_ValueAndErrorClasses) plus the plugins-specific closed-
// set/duplicate rules: an absent file, an absent "sandbox"/"plugins" key all
// resolve to the safe default pair (["cenci","cenci-watch"], nil error); a
// valid subset (including the empty array, which is a DIFFERENT valid value
// than the default -- resolves to an empty, not the default, list) resolves
// cleanly; every other failure class -- unreadable file, unparsable JSON, a
// non-object top level, wrong-typed "sandbox"/"plugins" fields, a non-string
// element, an unrecognized plugin name (including the "cenci-sandbox"
// near-miss), and a duplicate name -- resolves to (nil, error) with a
// path-bearing, non-usage error (#632: exit 1, never exit 2), and each
// failure class's error text must be distinguishable from every other
// class's (watch/docs/error-handling.md rule #446).
func TestRepoSandboxPlugins_ValueAndErrorClasses(t *testing.T) {
	const configRelPath = ".cenci/config.json"

	cases := []struct {
		name string
		// content is written as the config.json file content. If empty and
		// asDir is false, no file is written at all (the "absent file" case).
		content string
		// asDir, if true, creates a directory at the config.json path instead
		// of a file, forcing a portable (no chmod/root dependency) read
		// failure at that exact path.
		asDir bool

		wantPlugins []string
		wantErr     bool
		// wantErrContains lists substrings that must ALL appear in the
		// error's message -- chosen so each failure class has content
		// distinguishable from every other class, per watch rule #446.
		wantErrContains []string
	}{
		{
			name:        "absent file resolves the default pair",
			wantPlugins: []string{"cenci", "cenci-watch"},
		},
		{
			name:        "absent sandbox key resolves the default pair",
			content:     `{}`,
			wantPlugins: []string{"cenci", "cenci-watch"},
		},
		{
			name:        "absent plugins key resolves the default pair",
			content:     `{"sandbox":{}}`,
			wantPlugins: []string{"cenci", "cenci-watch"},
		},
		{
			name:        "valid single-element subset resolves that subset",
			content:     `{"sandbox":{"plugins":["cenci"]}}`,
			wantPlugins: []string{"cenci"},
		},
		{
			name:        "valid full subset in explicit order resolves verbatim, not re-sorted",
			content:     `{"sandbox":{"plugins":["cenci-watch","cenci"]}}`,
			wantPlugins: []string{"cenci-watch", "cenci"},
		},
		{
			name:        "empty array resolves an empty list, not the default",
			content:     `{"sandbox":{"plugins":[]}}`,
			wantPlugins: []string{},
		},
		{
			name:            "unreadable file errors",
			asDir:           true,
			wantErr:         true,
			wantErrContains: []string{"reading", configRelPath},
		},
		{
			name:            "unparsable JSON errors",
			content:         `{not valid json`,
			wantErr:         true,
			wantErrContains: []string{"parsing", configRelPath},
		},
		{
			name:            "non-object top level (array) errors",
			content:         `[]`,
			wantErr:         true,
			wantErrContains: []string{"top level", configRelPath},
		},
		{
			name:            "non-object top level (scalar) errors",
			content:         `42`,
			wantErr:         true,
			wantErrContains: []string{"top level", configRelPath},
		},
		{
			name:            "sandbox wrong type (string) errors",
			content:         `{"sandbox":"nope"}`,
			wantErr:         true,
			wantErrContains: []string{"sandbox", "must be an object", configRelPath},
		},
		{
			name:            "plugins wrong type (string) errors",
			content:         `{"sandbox":{"plugins":"cenci"}}`,
			wantErr:         true,
			wantErrContains: []string{"plugins", "must be an array", configRelPath},
		},
		{
			name:            "plugins wrong type (object) errors",
			content:         `{"sandbox":{"plugins":{"cenci":true}}}`,
			wantErr:         true,
			wantErrContains: []string{"plugins", "must be an array", configRelPath},
		},
		{
			name:            "non-string element errors",
			content:         `{"sandbox":{"plugins":["cenci",42]}}`,
			wantErr:         true,
			wantErrContains: []string{"plugins", configRelPath},
		},
		{
			name:            "unknown plugin name errors, naming the offending value and the allowed set",
			content:         `{"sandbox":{"plugins":["cenci","bogus-plugin"]}}`,
			wantErr:         true,
			wantErrContains: []string{"bogus-plugin", "cenci", "cenci-watch", configRelPath},
		},
		{
			name:            `"cenci-sandbox" near-miss is rejected, not silently accepted`,
			content:         `{"sandbox":{"plugins":["cenci-sandbox"]}}`,
			wantErr:         true,
			wantErrContains: []string{"cenci-sandbox", configRelPath},
		},
		{
			name:            "duplicate plugin name errors, not silently deduped",
			content:         `{"sandbox":{"plugins":["cenci","cenci"]}}`,
			wantErr:         true,
			wantErrContains: []string{"cenci", "duplicate", configRelPath},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			configPath := filepath.Join(repo, ".cenci", "config.json")
			switch {
			case tc.asDir:
				if err := os.MkdirAll(configPath, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", configPath, err)
				}
			case tc.content != "":
				writeFile(t, configPath, tc.content)
			}

			plugins, err := RepoSandboxPlugins(repo)

			if !tc.wantErr {
				if err != nil {
					t.Fatalf("RepoSandboxPlugins(repo) unexpected error: %v", err)
				}
				if !reflect.DeepEqual(plugins, tc.wantPlugins) {
					t.Errorf("RepoSandboxPlugins(repo) = %#v, want %#v", plugins, tc.wantPlugins)
				}
				if plugins == nil {
					t.Error("RepoSandboxPlugins(repo) returned a nil slice on success, want a fresh non-nil slice (watch/docs/go-gotchas.md nil-slice/JSON rule)")
				}
				return
			}

			if err == nil {
				t.Fatalf("RepoSandboxPlugins(repo) = nil error, want an error for case %q", tc.name)
			}
			if plugins != nil {
				t.Errorf("RepoSandboxPlugins(repo) plugins = %#v alongside an error, want nil", plugins)
			}
			if IsUsage(err) {
				t.Errorf("RepoSandboxPlugins(repo) error is a usage error (would exit 2), want a plain hard-fail error (exit 1): %v", err)
			}
			for _, want := range tc.wantErrContains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("RepoSandboxPlugins(repo) error = %q, want it to contain %q", err.Error(), want)
				}
			}
		})
	}
}

// TestRepoSandboxPlugins_EmptyArrayReturnsFreshNonNilSlice pins the plan's
// Assumption directly: `plugins: []` must resolve to a fresh, non-nil,
// zero-length slice -- distinguishable in principle from a nil slice a
// caller could accidentally mutate a shared package-level default through
// (watch/docs/go-gotchas.md nil-slice/JSON rule), even though both compare
// equal via reflect.DeepEqual to []string{}.
func TestRepoSandboxPlugins_EmptyArrayReturnsFreshNonNilSlice(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{"sandbox":{"plugins":[]}}`)

	plugins, err := RepoSandboxPlugins(repo)
	if err != nil {
		t.Fatalf("RepoSandboxPlugins(repo): %v", err)
	}
	if plugins == nil {
		t.Fatal("RepoSandboxPlugins(repo) with plugins:[] returned a nil slice, want a fresh non-nil empty slice")
	}
	if len(plugins) != 0 {
		t.Errorf("RepoSandboxPlugins(repo) with plugins:[] = %#v, want an empty (but non-nil) slice", plugins)
	}
}

// TestRepoSandboxPlugins_DefaultCallsDoNotShareBackingArray pins the same
// Assumption for the default-pair path: two independent calls that both
// resolve to the default must not return slices sharing a backing array, or
// one caller mutating its returned slice in place could corrupt every other
// caller's "default" value.
func TestRepoSandboxPlugins_DefaultCallsDoNotShareBackingArray(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()

	a, err := RepoSandboxPlugins(repoA)
	if err != nil {
		t.Fatalf("RepoSandboxPlugins(repoA): %v", err)
	}
	b, err := RepoSandboxPlugins(repoB)
	if err != nil {
		t.Fatalf("RepoSandboxPlugins(repoB): %v", err)
	}

	if len(a) == 0 || len(b) == 0 {
		t.Fatalf("expected both calls to resolve the non-empty default pair, got a=%#v b=%#v", a, b)
	}
	a[0] = "mutated"
	if b[0] == "mutated" {
		t.Error("mutating one call's returned default slice corrupted another call's returned default slice -- they share a backing array")
	}
}

// -- DefaultSandboxPlugins --------------------------------------------------

// TestDefaultSandboxPlugins_ReturnsTheClosedSetPair pins the default value
// itself: exactly {"cenci","cenci-watch"}, in that order (the order every
// golden create-arg/refresh-command assertion elsewhere depends on).
func TestDefaultSandboxPlugins_ReturnsTheClosedSetPair(t *testing.T) {
	got := DefaultSandboxPlugins()
	want := []string{"cenci", "cenci-watch"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultSandboxPlugins() = %#v, want %#v", got, want)
	}
}

// TestDefaultSandboxPlugins_ReturnsFreshSliceEachCall pins the non-nil,
// non-shared slice requirement for the default constructor itself: a caller
// that appends to or mutates one call's result must never affect another.
func TestDefaultSandboxPlugins_ReturnsFreshSliceEachCall(t *testing.T) {
	a := DefaultSandboxPlugins()
	b := DefaultSandboxPlugins()
	if len(a) == 0 {
		t.Fatal("DefaultSandboxPlugins() returned an empty slice, want the default pair")
	}
	a[0] = "mutated"
	if b[0] == "mutated" {
		t.Error("mutating one DefaultSandboxPlugins() call's result corrupted another call's result -- they share a backing array")
	}
}

// -- ResolveSandboxPlugins ---------------------------------------------------

// TestResolveSandboxPlugins_LegacyScopeDefaultsWithoutReadingConfig pins the
// ResolveDind precedent (dind.go:93-99, dind_test.go's
// "--no-dind still works with a malformed repo config" case): in legacy
// (non-repo) scope the default applies and RepoSandboxPlugins is never
// invoked at all -- proven here by pointing RepoRoot at a directory holding
// a malformed config.json that would hard-fail if it were ever read.
func TestResolveSandboxPlugins_LegacyScopeDefaultsWithoutReadingConfig(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{not valid json`)
	scope := Scope{WorkspaceScope: "legacy", RepoRoot: repo}

	plugins, err := ResolveSandboxPlugins(scope)
	if err != nil {
		t.Fatalf("ResolveSandboxPlugins(legacy scope) with malformed config present: %v", err)
	}
	want := []string{"cenci", "cenci-watch"}
	if !reflect.DeepEqual(plugins, want) {
		t.Errorf("ResolveSandboxPlugins(legacy scope) = %#v, want the default pair %#v", plugins, want)
	}
}

// TestResolveSandboxPlugins_RepoScopeReadsConfig pins the repo-scope half:
// ResolveSandboxPlugins in repo scope resolves exactly what RepoSandboxPlugins
// would for that repo root, including a non-default subset.
func TestResolveSandboxPlugins_RepoScopeReadsConfig(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{"sandbox":{"plugins":["cenci"]}}`)
	scope := Scope{WorkspaceScope: "repo", RepoRoot: repo}

	plugins, err := ResolveSandboxPlugins(scope)
	if err != nil {
		t.Fatalf("ResolveSandboxPlugins(repo scope): %v", err)
	}
	want := []string{"cenci"}
	if !reflect.DeepEqual(plugins, want) {
		t.Errorf("ResolveSandboxPlugins(repo scope) = %#v, want %#v", plugins, want)
	}
}

// TestResolveSandboxPlugins_RepoScopeMalformedConfigHardFails pins the
// #632-mirroring hard-fail: unlike legacy scope, repo scope propagates
// RepoSandboxPlugins' error unchanged -- a plain, non-usage error.
func TestResolveSandboxPlugins_RepoScopeMalformedConfigHardFails(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{not valid json`)
	scope := Scope{WorkspaceScope: "repo", RepoRoot: repo}

	plugins, err := ResolveSandboxPlugins(scope)
	if err == nil {
		t.Fatal("ResolveSandboxPlugins(repo scope) with malformed config = nil error, want an error")
	}
	if IsUsage(err) {
		t.Errorf("ResolveSandboxPlugins(repo scope) malformed-config error is a usage error (would exit 2), want a plain hard-fail error (exit 1): %v", err)
	}
	if plugins != nil {
		t.Errorf("ResolveSandboxPlugins(repo scope) plugins = %#v alongside an error, want nil", plugins)
	}
}

// -- sandboxPluginsEnvValue --------------------------------------------------

// TestSandboxPluginsEnvValue_SpaceJoinsInConfigOrder pins the CENCI_SANDBOX_
// PLUGINS env value's exact formatting (Q&A #5): space-separated, in the
// resolved list's own order -- never re-sorted -- and "" for an empty list
// (the env var is still SET for the empty-list case, per the ticket's
// Decisions -- sandboxPluginsEnvValue only formats the value, the caller is
// responsible for always emitting the "-e CENCI_SANDBOX_PLUGINS=" flag).
func TestSandboxPluginsEnvValue_SpaceJoinsInConfigOrder(t *testing.T) {
	cases := []struct {
		name    string
		plugins []string
		want    string
	}{
		{"default pair", []string{"cenci", "cenci-watch"}, "cenci cenci-watch"},
		{"single element", []string{"cenci"}, "cenci"},
		{"explicit non-default order is preserved, not re-sorted", []string{"cenci-watch", "cenci"}, "cenci-watch cenci"},
		{"empty list joins to the empty string", []string{}, ""},
		{"nil list joins to the empty string", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sandboxPluginsEnvValue(tc.plugins); got != tc.want {
				t.Errorf("sandboxPluginsEnvValue(%#v) = %q, want %q", tc.plugins, got, tc.want)
			}
		})
	}
}

// -- sandboxPluginsDiffer ----------------------------------------------------

// TestSandboxPluginsDiffer pins the attach-path drift comparison (Q&A #5):
// set-wise, so element order alone never produces a spurious drift warning;
// and an absent (runningFound=false) running value is the legacy-container
// signal, compared against DefaultSandboxPlugins() rather than against an
// empty running set.
func TestSandboxPluginsDiffer(t *testing.T) {
	cases := []struct {
		name         string
		resolved     []string
		running      string
		runningFound bool
		want         bool
	}{
		{
			name:         "identical order, found: no drift",
			resolved:     []string{"cenci", "cenci-watch"},
			running:      "cenci cenci-watch",
			runningFound: true,
			want:         false,
		},
		{
			name:         "same set, different order, found: no drift (order-insensitive)",
			resolved:     []string{"cenci-watch", "cenci"},
			running:      "cenci cenci-watch",
			runningFound: true,
			want:         false,
		},
		{
			name:         "narrowed resolved set vs a wider running set, found: drift",
			resolved:     []string{"cenci"},
			running:      "cenci cenci-watch",
			runningFound: true,
			want:         true,
		},
		{
			name:         "empty resolved vs non-empty running, found: drift",
			resolved:     []string{},
			running:      "cenci cenci-watch",
			runningFound: true,
			want:         true,
		},
		{
			name:         "not found (legacy container) compares against the default pair: resolved default -> no drift",
			resolved:     []string{"cenci", "cenci-watch"},
			running:      "",
			runningFound: false,
			want:         false,
		},
		{
			name:         "not found (legacy container) compares against the default pair: narrowed resolved -> drift",
			resolved:     []string{"cenci"},
			running:      "",
			runningFound: false,
			want:         true,
		},
		{
			name:         "both empty sets, found: no drift",
			resolved:     []string{},
			running:      "",
			runningFound: true,
			want:         false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sandboxPluginsDiffer(tc.resolved, tc.running, tc.runningFound); got != tc.want {
				t.Errorf("sandboxPluginsDiffer(%#v, %q, %t) = %t, want %t", tc.resolved, tc.running, tc.runningFound, got, tc.want)
			}
		})
	}
}
