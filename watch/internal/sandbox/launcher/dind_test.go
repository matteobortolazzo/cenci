package launcher

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/errcode"
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
// error text must be distinguishable from every other class's
// (watch/docs/error-handling.md rule #446) rather than collapsing to a
// single generic message.
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

// -- host-platform gate (#962) -------------------------------------------

// setDindHostOS pins the host OS the dind platform gate evaluates against
// for the duration of one test, so a macOS host can be exercised from any
// runner (and a linux host from a macOS runner) without build tags.
func setDindHostOS(t *testing.T, hostOS string) {
	t.Helper()
	prev := dindHostOS
	dindHostOS = hostOS
	t.Cleanup(func() { dindHostOS = prev })
}

// TestResolveDindForHost_PlatformGate pins the macOS degrade (#962): sysbox
// is a Linux-only OCI runtime installed on the machine running dockerd, and
// on macOS dockerd lives inside Docker Desktop's unmodifiable LinuxKit VM,
// so sysbox-runc can never be registered there. Rather than hard-failing
// every launch in a dind repo (which left macOS unable to open a sandbox at
// all), a dind-on resolution degrades to dind-off with a degraded=true
// signal for the caller to warn on. Linux resolution is unchanged, and
// every error class ResolveDind raises still propagates on macOS — the gate
// only ever downgrades an otherwise-successful dind-ON decision.
func TestResolveDindForHost_PlatformGate(t *testing.T) {
	// configRepo builds a repo scope whose .cenci/config.json carries content.
	configRepo := func(t *testing.T, content string) Scope {
		t.Helper()
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, ".cenci", "config.json"), content)
		return Scope{WorkspaceScope: "repo", RepoRoot: repo}
	}

	t.Run("darwin degrades an on repo config", func(t *testing.T) {
		setDindHostOS(t, "darwin")
		on, degraded, err := resolveDindForHost(Options{}, configRepo(t, `{"sandbox":{"dind":true}}`))
		if err != nil {
			t.Fatalf("resolveDindForHost on darwin: %v", err)
		}
		if on {
			t.Error("on = true on darwin, want false — sysbox-runc can never be registered there")
		}
		if !degraded {
			t.Error("degraded = false, want true so the caller warns that dind was requested but dropped")
		}
	})

	t.Run("darwin degrades an explicit --dind", func(t *testing.T) {
		setDindHostOS(t, "darwin")
		scope := Scope{WorkspaceScope: "repo", RepoRoot: t.TempDir()}
		on, degraded, err := resolveDindForHost(Options{Dind: true}, scope)
		if err != nil {
			t.Fatalf("resolveDindForHost with --dind on darwin: %v", err)
		}
		if on || !degraded {
			t.Errorf("--dind on darwin = (on=%v, degraded=%v), want (false, true) — an explicit flag degrades and warns, it never hard-fails", on, degraded)
		}
	})

	t.Run("darwin with dind off is not reported as degraded", func(t *testing.T) {
		setDindHostOS(t, "darwin")
		scope := Scope{WorkspaceScope: "repo", RepoRoot: t.TempDir()}
		on, degraded, err := resolveDindForHost(Options{}, scope)
		if err != nil {
			t.Fatalf("resolveDindForHost: %v", err)
		}
		if on || degraded {
			t.Errorf("no dind requested on darwin = (on=%v, degraded=%v), want (false, false) — nothing was dropped, so there is nothing to warn about", on, degraded)
		}
	})

	t.Run("darwin with --no-dind is not reported as degraded", func(t *testing.T) {
		setDindHostOS(t, "darwin")
		on, degraded, err := resolveDindForHost(Options{NoDind: true}, configRepo(t, `{"sandbox":{"dind":true}}`))
		if err != nil {
			t.Fatalf("resolveDindForHost with --no-dind on darwin: %v", err)
		}
		if on || degraded {
			t.Errorf("--no-dind on darwin = (on=%v, degraded=%v), want (false, false) — the user already opted out, so no warning is owed", on, degraded)
		}
	})

	t.Run("darwin still rejects --dind --no-dind as a usage error", func(t *testing.T) {
		setDindHostOS(t, "darwin")
		scope := Scope{WorkspaceScope: "repo", RepoRoot: t.TempDir()}
		_, degraded, err := resolveDindForHost(Options{Dind: true, NoDind: true}, scope)
		if err == nil || !IsUsage(err) {
			t.Fatalf("resolveDindForHost with --dind --no-dind on darwin = %v, want a usage error (exit 2) — the platform gate must not swallow input validation", err)
		}
		if degraded {
			t.Error("degraded = true for a usage error, want false — nothing was degraded, the input was rejected")
		}
	})

	t.Run("darwin still rejects --dind outside repo scope as a usage error", func(t *testing.T) {
		setDindHostOS(t, "darwin")
		_, _, err := resolveDindForHost(Options{Dind: true}, Scope{WorkspaceScope: "legacy"})
		if err == nil || !IsUsage(err) {
			t.Fatalf("resolveDindForHost with --dind in legacy scope on darwin = %v, want a usage error (exit 2)", err)
		}
	})

	t.Run("darwin still hard-fails a malformed repo config", func(t *testing.T) {
		setDindHostOS(t, "darwin")
		on, degraded, err := resolveDindForHost(Options{}, configRepo(t, `{not valid json`))
		if err == nil {
			t.Fatal("resolveDindForHost with a malformed repo config on darwin = nil error, want a hard error — #632's contract must survive the platform gate")
		}
		if IsUsage(err) {
			t.Errorf("malformed-config error on darwin classified as a usage error (exit 2), want a plain hard-fail error (exit 1): %v", err)
		}
		if on || degraded {
			t.Errorf("malformed config on darwin = (on=%v, degraded=%v), want (false, false) alongside the error", on, degraded)
		}
	})

	t.Run("linux keeps an on repo config on", func(t *testing.T) {
		setDindHostOS(t, "linux")
		on, degraded, err := resolveDindForHost(Options{}, configRepo(t, `{"sandbox":{"dind":true}}`))
		if err != nil {
			t.Fatalf("resolveDindForHost on linux: %v", err)
		}
		if !on || degraded {
			t.Errorf("on repo config on linux = (on=%v, degraded=%v), want (true, false) — the platform gate must never touch a Linux host", on, degraded)
		}
	})

	t.Run("linux keeps an explicit --dind on", func(t *testing.T) {
		setDindHostOS(t, "linux")
		scope := Scope{WorkspaceScope: "repo", RepoRoot: t.TempDir()}
		on, degraded, err := resolveDindForHost(Options{Dind: true}, scope)
		if err != nil {
			t.Fatalf("resolveDindForHost with --dind on linux: %v", err)
		}
		if !on || degraded {
			t.Errorf("--dind on linux = (on=%v, degraded=%v), want (true, false)", on, degraded)
		}
	})
}

// TestResolveLaunchContext_DarwinDegradesDindAndWarns pins the launch-side
// half of #962: on macOS a dind repo launches (rather than dying in
// dindPreflight), the resolved context carries DindOn=false, the container
// runtime is resolved through the normal podman-first ContainerRuntime()
// path (NOT dind's docker-first ContainerRuntimePreferDocker(), which would
// pick a Docker the degraded launch has no reason to prefer), and a single
// warning naming the platform, the errcode, and the --no-dind escape hatch
// reaches Stderr.
func TestResolveLaunchContext_DarwinDegradesDindAndWarns(t *testing.T) {
	setDindHostOS(t, "darwin")
	repo := newAuditTestRepo(t)
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{"sandbox":{"dind":true}}`)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	// Both runtimes on PATH, so the resolved runtime actually discriminates
	// between ContainerRuntime() (podman first) and
	// ContainerRuntimePreferDocker() (docker first) — with only one present
	// both helpers would agree and the assertion would prove nothing.
	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	writeFakeRuntime(t, fakeDir, "podman", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	stderr := &bytes.Buffer{}
	eng := &Engine{Stderr: stderr}
	ctx, err := eng.resolveLaunchContext(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("resolveLaunchContext on darwin with a dind repo config = %v, want a successful degraded launch context", err)
	}
	if ctx.DindOn {
		t.Error("ctx.DindOn = true on darwin, want false")
	}
	if eng.Runtime != "podman" {
		t.Errorf("eng.Runtime = %q, want \"podman\" — a degraded launch resolves the runtime podman-first like any non-dind launch", eng.Runtime)
	}

	warning := stderr.String()
	for _, want := range []string{
		"nested Docker",
		"macOS",
		string(errcode.SandboxDindPlatformUnsupported),
		"--no-dind",
	} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning = %q, want it to contain %q", warning, want)
		}
	}
	if n := strings.Count(warning, string(errcode.SandboxDindPlatformUnsupported)); n != 1 {
		t.Errorf("warning mentions %s %d times, want exactly 1", errcode.SandboxDindPlatformUnsupported, n)
	}
}

// TestResolveLaunchContext_LinuxMissingSysboxStillHardFails is the
// regression guard for #962's blast radius: the degrade is macOS-only. On
// Linux an unregistered sysbox-runc is a fixable host-setup problem, so
// dindPreflight must still hard-fail with its install pointer — and the
// runtime must still have been resolved docker-first before the preflight
// ran.
func TestResolveLaunchContext_LinuxMissingSysboxStillHardFails(t *testing.T) {
	setDindHostOS(t, "linux")
	repo := newAuditTestRepo(t)
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{"sandbox":{"dind":true}}`)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, "docker", callLog)
	writeFakeRuntime(t, fakeDir, "podman", callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_INFO_RUNTIMES", `{"runc":{}}`)

	stderr := &bytes.Buffer{}
	eng := &Engine{Stderr: stderr}
	_, err := eng.resolveLaunchContext(Options{Agent: "claude"})
	if err == nil {
		t.Fatal("resolveLaunchContext on linux without sysbox = nil error, want the dindPreflight hard failure")
	}
	if IsUsage(err) {
		t.Errorf("missing-sysbox error on linux classified as a usage error (exit 2), want the exit-1 preflight-failure class: %v", err)
	}
	if !strings.Contains(err.Error(), "sysbox-runc") {
		t.Errorf("error = %q, want it to name the sysbox-runc runtime", err.Error())
	}
	if !strings.Contains(err.Error(), "sysbox-ce") {
		t.Errorf("error = %q, want it to keep the host install pointer", err.Error())
	}
	if eng.Runtime != "docker" {
		t.Errorf("eng.Runtime = %q, want \"docker\" — a dind launch still resolves the runtime docker-first on linux", eng.Runtime)
	}
	if strings.Contains(stderr.String(), string(errcode.SandboxDindPlatformUnsupported)) {
		t.Errorf("stderr = %q, want no platform-degrade warning on linux", stderr.String())
	}
}
