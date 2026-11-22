package launcher

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFollowHostAuthorityIsIndependentOfEnvironment(t *testing.T) {
	_, _, _ = volumeCheckEngine(t, 0, 0, 0)
	path := versionProfileFixture(t, "claude", "1.2.3 (Claude Code)")
	fake := t.TempDir()
	t.Setenv("HOME", fake)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(fake, ".config"))
	p, err := globalHostVersionPolicy("claude")
	if err != nil || p.Executable != path {
		t.Fatalf("changed HOME disabled account's policy: %+v %v", p, err)
	}
	writeFile(t, filepath.Join(fake, ".config", "cenci", "config.json"), `{"sandboxHostProfiles":{"claude":{"version":{"mode":"follow-host","executable":"/tmp/claude"}}}}`)
	if _, err := globalHostVersionPolicy("claude"); err == nil || !strings.Contains(err.Error(), "OS account") {
		t.Fatalf("untrusted XDG policy disposition: %v", err)
	}
}

func TestFollowHostCodexNPMPayload(t *testing.T) {
	for _, layout := range []string{"flat", "nested"} {
		t.Run(layout, func(t *testing.T) {
			e, _, _ := volumeCheckEngine(t, 0, 0, 0)
			entry := versionProfileFixture(t, "codex", "codex-cli 1.2.3")
			modules := filepath.Join(os.Getenv("HOME"), "lib", "node_modules")
			wrapper := filepath.Join(modules, "@openai", "codex", "bin", "codex.js")
			writeFile(t, wrapper, "#!/usr/bin/env node\nthrow new Error('wrapper must not execute')")
			if layout == "nested" {
				modules = filepath.Join(modules, "@openai", "codex", "node_modules")
			}
			arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
			triple := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH] + map[string]string{"linux": "-unknown-linux-musl", "darwin": "-apple-darwin"}[runtime.GOOS]
			native := filepath.Join(modules, "@openai", "codex-"+runtime.GOOS+"-"+arch, "vendor", triple, "bin", "codex")
			if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
				t.Fatal(err)
			}
			for _, ext := range []string{"", ".output"} {
				if err := os.Rename(entry+ext, native+ext); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(wrapper, entry); err != nil {
				t.Fatal(err)
			}
			p, err := globalHostVersionPolicy("codex")
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.probe("codex", e)
			if got != "1.2.3" || err != nil {
				t.Fatalf("%q %v", got, err)
			}
		})
	}
}

func TestAuditReadsBoundedVersionFactWithoutWorkloadExecution(t *testing.T) {
	for _, tc := range []struct{ value, want string }{{"1.2.3\n", "1.2.3"}, {"SECRET=hidden", ""}, {strings.Repeat("0", 40<<10), ""}} {
		e, _, _ := volumeCheckEngine(t, 0, 0, 0)
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		if err := tw.WriteHeader(&tar.Header{Name: "VERSION", Mode: 0644, Size: int64(len(tc.value))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(tc.value)); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		archive := filepath.Join(t.TempDir(), "fact.tar")
		if err := os.WriteFile(archive, buf.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		e.Runtime = filepath.Join(t.TempDir(), "runtime")
		if err := os.WriteFile(e.Runtime, []byte("#!/bin/sh\n[ \"$1\" = cp ] && [ \"$2\" = example:/opt/cenci-agent/current/VERSION ] && [ \"$3\" = - ] && /bin/cat \""+archive+"\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if got := e.readVersionFact("example", "current/VERSION"); got != tc.want {
			t.Fatalf("version %q, want %q", got, tc.want)
		}
	}
}

func TestHostVersionProbeEnvironmentAndLimits(t *testing.T) {
	for _, tc := range []struct{ name, output, want string }{
		{"valid prerelease", "codex-cli 1.2.3-beta.1", "1.2.3-beta.1"},
		{"nonexact", "codex-cli 1.02.3", ""},
		{"wrong cli", "1.2.3 (Claude Code)", ""},
		{"multiline", "codex-cli 1.2.3\n1.2.4", ""},
		{"excessive output", strings.Repeat(" ", 2048) + "codex-cli 1.2.3", ""},
		{"deadline", "stall", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, _ := volumeCheckEngine(t, 0, 0, 0)
			versionProfileFixture(t, "codex", tc.output)
			p, err := globalHostVersionPolicy("codex")
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			got, err := p.probe("codex", e)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("got %q, %v", got, err)
			}
			if time.Since(start) > 7*time.Second {
				t.Fatalf("unbounded host probe: %s", time.Since(start))
			}
		})
	}
}

func TestHostVersionRefusesUntrustedSecondaryRuntime(t *testing.T) {
	e, _, _ := volumeCheckEngine(t, 0, 0, 0)
	versionProfileFixture(t, "claude", "2.1.3 (Claude Code)")
	marker := filepath.Join(t.TempDir(), "ran")
	fake := filepath.Join(os.Getenv("PATH"), "podman")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n: > \""+marker+"\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p, err := globalHostVersionPolicy("claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.probe("claude", e); err == nil {
		t.Fatal("unsafe peer runtime accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("untrusted peer ran: %v", err)
	}
}

func TestHostVersionProbeRejectsImpostors(t *testing.T) {
	for _, mode := range []string{"checkout", "symlink into checkout", "hardlink", "unsafe wrapper", "ambient PATH", "writable"} {
		t.Run(mode, func(t *testing.T) {
			e, _, _ := volumeCheckEngine(t, 0, 0, 0)
			path := versionProfileFixture(t, "claude", "2.1.3 (Claude Code)")
			repo := filepath.Join(os.Getenv("HOME"), "repo")
			if err := os.MkdirAll(filepath.Join(repo, ".git"), 0700); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(repo, "claude")
			switch mode {
			case "checkout":
				if err := os.Mkdir(filepath.Join(filepath.Dir(path), ".git"), 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink into checkout":
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, other); err != nil {
					t.Fatal(err)
				}
			case "unsafe wrapper":
				if err := os.WriteFile(path, []byte("#!/bin/sh\necho '7.7.7 (Claude Code)'\n"), 0700); err != nil {
					t.Fatal(err)
				}
			case "ambient PATH":
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", repo+":"+os.Getenv("PATH"))
			case "writable":
				if err := os.Chmod(path, 0777); err != nil {
					t.Fatal(err)
				}
			}
			p, err := globalHostVersionPolicy("claude")
			if err != nil {
				t.Fatal(err)
			}
			if got, err := p.probe("claude", e); err == nil {
				t.Fatalf("unsafe probe gave version %q", got)
			}
		})
	}
}

func TestHostVersionReportIncludesPolicyHostInstalledPin(t *testing.T) {
	e, _, _ := volumeCheckEngineStatus(t, agentVolumeStatusLines("1.2.3", time.Now(), time.Time{}), 0, 0, 0)
	versionProfileFixture(t, "claude", "2.1.3 (Claude Code)")
	p, err := globalHostVersionPolicy("claude")
	if err != nil {
		t.Fatal(err)
	}
	report := e.describeHostVersion("claude", "", p, true)
	if report.Requested != "2.1.3" || report.Effective != "1.2.3" || report.Pin != "1.2.3" || report.Policy != "follow-host" || !strings.Contains(report.Note, "pin") {
		t.Fatalf("%+v", report)
	}
}
