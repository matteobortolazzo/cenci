package launcher

import (
	"path/filepath"
	"testing"
)

// -- RepoDindConfig ----------------------------------------------------

// TestRepoDindConfig_ReadsSandboxDindKey pins the new .cenci/config.json
// `sandbox.dind` key (#585): a minimal stdlib-JSON read of the repo-scoped
// config file, mirroring internal/run/template.go's Load pattern.
func TestRepoDindConfig_ReadsSandboxDindKey(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{"sandbox":{"dind":true}}`)

	if !RepoDindConfig(repo) {
		t.Error("RepoDindConfig(repo) = false, want true for sandbox.dind=true")
	}
}

// TestRepoDindConfig_DefaultsToFalse pins every "not explicitly true" input
// (absent file, explicit false, wrong types, unparsable JSON, missing keys)
// to a silent false — RepoDindConfig never errors, since a config-parsing
// hiccup must not block every future launch in the repo.
func TestRepoDindConfig_DefaultsToFalse(t *testing.T) {
	cases := map[string]string{
		"absent file":           "",
		"dind explicit false":   `{"sandbox":{"dind":false}}`,
		"sandbox not an object": `{"sandbox":"nope"}`,
		"dind wrong type":       `{"sandbox":{"dind":"yes"}}`,
		"unparsable json":       `{not valid json`,
		"no sandbox key":        `{}`,
		"no dind key":           `{"sandbox":{}}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			if content != "" {
				writeFile(t, filepath.Join(repo, ".cenci", "config.json"), content)
			}
			if RepoDindConfig(repo) {
				t.Errorf("RepoDindConfig(repo) = true, want false for case %q (content %q)", name, content)
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
}
