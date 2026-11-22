package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func versionProfileFixture(t *testing.T, agent, script string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config")
	t.Setenv("XDG_CONFIG_HOME", configDir)
	if err := os.MkdirAll(filepath.Join(configDir, "cenci"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "bin", agent)
	if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if script != "" {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	profile := map[string]any{"sandboxHostProfiles": map[string]any{agent: map[string]any{"version": map[string]string{"mode": "follow-host", "executable": path}}}}
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "cenci", "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFollowHostAgentVolume(t *testing.T) {
	for _, tc := range []struct {
		name, version, pin string
		running, empty     bool
		updateExit         int
		update, warn, fail bool
	}{
		{name: "upgrade", version: "1.4.0", update: true},
		{name: "downgrade on attach", version: "0.9.0", running: true, update: true},
		{name: "match suppresses stale latest refresh", version: "1.2.3"},
		{name: "pin overrides", version: "1.4.0", pin: "1.2.3"},
		{name: "offline with usable release", version: "1.4.0", update: true, updateExit: 1, warn: true},
		{name: "unavailable artifact with empty store", version: "1.4.0", empty: true, update: true, updateExit: 1, fail: true},
		{name: "missing host with empty store", empty: true, fail: true},
		{name: "missing host with usable release", warn: true},
		{name: "unsupported output with usable release", version: "release branch", warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := agentVolumeStatusLines(tc.pin, time.Time{}, time.Time{})
			if tc.empty {
				status = strings.Replace(status, "populated=yes", "populated=no", 1)
			}
			e, log, warnings := volumeCheckEngineStatus(t, status, 0, tc.updateExit, 0)
			script := ""
			if tc.version != "" {
				script = "test \"$1\" = --version && printf '%s\\n' '" + tc.version + " (Claude Code)'"
			}
			versionProfileFixture(t, "claude", script)
			err := e.EnsureAgentVolume("claude", tc.running)
			if (err != nil) != tc.fail {
				t.Fatalf("error %v, want failure %t", err, tc.fail)
			}
			calls := readCallLog(t, log)
			if got := containsLineWithAll(calls, "agent-cli.sh update"); got != tc.update {
				t.Fatalf("unexpected updater disposition: %v", calls)
			}
			if tc.update && !containsLineWithAll(calls, "agent-cli.sh update claude "+tc.version+" --follow-host") {
				t.Fatalf("missing verified exact-version updater: %v", calls)
			}
			if tc.warn && !strings.Contains(warnings.String(), "1.2.3") {
				t.Fatalf("missing effective fallback version: %s", warnings)
			}
			if containsLineWithAll(calls, "volume rm") {
				t.Fatal("follow-host removed shared volume")
			}
		})
	}
}

func TestFollowHostSweepAndManualUpdate(t *testing.T) {
	for _, sweep := range []bool{true, false} {
		e, log, _ := volumeCheckEngine(t, 0, 0, 0)
		versionProfileFixture(t, "claude", "echo '4.3.2 (Claude Code)'")
		var err error
		if sweep {
			err = e.UpdateAgentSkipIfPinned("claude")
		} else {
			err = e.UpdateAgent("claude", "")
		}
		if err != nil {
			t.Fatal(err)
		}
		if calls := readCallLog(t, log); !containsLineWithAll(calls, "agent-cli.sh update claude 4.3.2 --follow-host") {
			t.Fatalf("sweep=%t calls=%v", sweep, calls)
		}
	}
}
