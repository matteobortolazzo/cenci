package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -- RepoDindConfig ----------------------------------------------------

// TestRepoDindConfig_ReadsSandboxDindKey pins the .cenci/config.json
// `sandbox.dind` key (#585): a minimal stdlib-JSON read of the repo-scoped
// config file, mirroring internal/run/template.go's Load pattern.
func TestRepoDindConfig_ReadsSandboxDindKey(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{"sandbox":{"dind":true}}`)

	on, err := RepoDindConfig(repo)
	if err != nil {
		t.Fatalf("RepoDindConfig(repo): %v", err)
	}
	if !on {
		t.Error("RepoDindConfig(repo) = false, want true for sandbox.dind=true")
	}
}

// TestRepoDindConfig_ValueAndErrorClasses (#632) replaces the old
// silent-false test: RepoDindConfig now returns (bool, error). Only an
// absent file, an absent "sandbox"/"dind" key, and an explicit
// `"dind":false` resolve to a safe (false, nil) off state. Every other
// failure class — unreadable file, unparsable JSON, a non-object top level,
// and wrong-typed "sandbox"/"dind" fields — must resolve to (false, error)
// with a path-bearing, non-usage error (so it maps to exit 1, not exit 2,
// per launch.go's IsUsage/exit-code convention), and each failure class's
// error text must be distinguishable from every other class's (AGENTS.md
// watch rule #446) rather than collapsing to a single generic message.
func TestRepoDindConfig_ValueAndErrorClasses(t *testing.T) {
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

		wantOn  bool
		wantErr bool
		// wantErrContains lists substrings that must ALL appear in the
		// error's message — chosen so each failure class has content
		// distinguishable from every other class, per watch rule #446.
		wantErrContains []string
	}{
		{
			name:    "absent file resolves off",
			wantOn:  false,
			wantErr: false,
		},
		{
			name:    "absent sandbox key resolves off",
			content: `{}`,
			wantOn:  false,
			wantErr: false,
		},
		{
			name:    "absent dind key resolves off",
			content: `{"sandbox":{}}`,
			wantOn:  false,
			wantErr: false,
		},
		{
			name:    "explicit dind false resolves off",
			content: `{"sandbox":{"dind":false}}`,
			wantOn:  false,
			wantErr: false,
		},
		{
			name:    "explicit dind true resolves on",
			content: `{"sandbox":{"dind":true}}`,
			wantOn:  true,
			wantErr: false,
		},
		{
			name:            "unreadable file errors",
			asDir:           true,
			wantOn:          false,
			wantErr:         true,
			wantErrContains: []string{"reading", configRelPath},
		},
		{
			name:            "unparsable JSON errors",
			content:         `{not valid json`,
			wantOn:          false,
			wantErr:         true,
			wantErrContains: []string{"parsing", configRelPath},
		},
		{
			name:            "non-object top level (array) errors",
			content:         `[]`,
			wantOn:          false,
			wantErr:         true,
			wantErrContains: []string{"top level", configRelPath},
		},
		{
			name:            "non-object top level (scalar) errors",
			content:         `42`,
			wantOn:          false,
			wantErr:         true,
			wantErrContains: []string{"top level", configRelPath},
		},
		{
			name:            "sandbox wrong type (string) errors",
			content:         `{"sandbox":"nope"}`,
			wantOn:          false,
			wantErr:         true,
			wantErrContains: []string{"sandbox", "must be an object", configRelPath},
		},
		{
			name:            "dind wrong type (string) errors",
			content:         `{"sandbox":{"dind":"yes"}}`,
			wantOn:          false,
			wantErr:         true,
			wantErrContains: []string{"dind", "must be a boolean", configRelPath},
		},
		{
			name:            "dind wrong type (number) errors",
			content:         `{"sandbox":{"dind":1}}`,
			wantOn:          false,
			wantErr:         true,
			wantErrContains: []string{"dind", "must be a boolean", configRelPath},
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

			on, err := RepoDindConfig(repo)

			if on != tc.wantOn {
				t.Errorf("RepoDindConfig(repo) on = %v, want %v (err: %v)", on, tc.wantOn, err)
			}

			if !tc.wantErr {
				if err != nil {
					t.Fatalf("RepoDindConfig(repo) unexpected error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("RepoDindConfig(repo) = nil error, want an error for case %q", tc.name)
			}
			if IsUsage(err) {
				t.Errorf("RepoDindConfig(repo) error is a usage error (would exit 2), want a plain hard-fail error (exit 1): %v", err)
			}
			for _, want := range tc.wantErrContains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("RepoDindConfig(repo) error = %q, want it to contain %q", err.Error(), want)
				}
			}
		})
	}
}

// -- ResolveDind precedence ----------------------------------------------

// TestResolveDind_Precedence pins the full precedence table (#585):
// --dind + --no-dind together is a usage error; --no-dind always wins over
// config and never errors on scope; --dind wins over config; with neither
// flag, the repo config decides; and requesting dind-on (via --dind) in a
// non-repo/legacy scope is a usage error.
func TestResolveDind_Precedence(t *testing.T) {
	t.Run("both flags together is a usage error", func(t *testing.T) {
		scope := Scope{WorkspaceScope: "repo", RepoRoot: t.TempDir()}
		_, err := ResolveDind(Options{Dind: true, NoDind: true}, scope)
		if err == nil || !IsUsage(err) {
			t.Fatalf("ResolveDind with --dind --no-dind together = %v, want a usage error", err)
		}
	})

	t.Run("--no-dind turns dind off even over an on repo config", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{"sandbox":{"dind":true}}`)
		scope := Scope{WorkspaceScope: "repo", RepoRoot: repo}
		on, err := ResolveDind(Options{NoDind: true}, scope)
		if err != nil {
			t.Fatalf("ResolveDind: %v", err)
		}
		if on {
			t.Error("ResolveDind with --no-dind = true, want false even though repo config sets dind=true")
		}
	})

	t.Run("--no-dind never errors on legacy scope", func(t *testing.T) {
		scope := Scope{WorkspaceScope: "legacy"}
		on, err := ResolveDind(Options{NoDind: true}, scope)
		if err != nil {
			t.Fatalf("ResolveDind: %v", err)
		}
		if on {
			t.Error("ResolveDind with --no-dind in legacy scope = true, want false")
		}
	})

	t.Run("--dind turns dind on", func(t *testing.T) {
		scope := Scope{WorkspaceScope: "repo", RepoRoot: t.TempDir()}
		on, err := ResolveDind(Options{Dind: true}, scope)
		if err != nil {
			t.Fatalf("ResolveDind: %v", err)
		}
		if !on {
			t.Error("ResolveDind with --dind = false, want true")
		}
	})

	t.Run("neither flag falls back to an on repo config", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{"sandbox":{"dind":true}}`)
		scope := Scope{WorkspaceScope: "repo", RepoRoot: repo}
		on, err := ResolveDind(Options{}, scope)
		if err != nil {
			t.Fatalf("ResolveDind: %v", err)
		}
		if !on {
			t.Error("ResolveDind with neither flag and an on repo config = false, want true")
		}
	})

	t.Run("neither flag, no config, defaults off", func(t *testing.T) {
		scope := Scope{WorkspaceScope: "repo", RepoRoot: t.TempDir()}
		on, err := ResolveDind(Options{}, scope)
		if err != nil {
			t.Fatalf("ResolveDind: %v", err)
		}
		if on {
			t.Error("ResolveDind with neither flag and no config = true, want false")
		}
	})

	t.Run("--dind requested in legacy scope is a usage error", func(t *testing.T) {
		scope := Scope{WorkspaceScope: "legacy"}
		_, err := ResolveDind(Options{Dind: true}, scope)
		if err == nil || !IsUsage(err) {
			t.Fatalf("ResolveDind with --dind in legacy scope = %v, want a usage error", err)
		}
	})

	t.Run("malformed repo config is a hard-fail, non-usage error", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{not valid json`)
		scope := Scope{WorkspaceScope: "repo", RepoRoot: repo}
		on, err := ResolveDind(Options{}, scope)
		if err == nil {
			t.Fatal("ResolveDind with malformed repo config = nil error, want an error")
		}
		if IsUsage(err) {
			t.Errorf("ResolveDind with malformed repo config returned a usage error (would exit 2), want a plain hard-fail error (exit 1): %v", err)
		}
		if on {
			t.Error("ResolveDind with malformed repo config: on = true, want false alongside the error")
		}
	})

	t.Run("--no-dind still works with a malformed repo config", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{not valid json`)
		scope := Scope{WorkspaceScope: "repo", RepoRoot: repo}
		on, err := ResolveDind(Options{NoDind: true}, scope)
		if err != nil {
			t.Fatalf("ResolveDind with --no-dind and a malformed repo config: %v", err)
		}
		if on {
			t.Error("ResolveDind with --no-dind and a malformed repo config: on = true, want false")
		}
	})
}
