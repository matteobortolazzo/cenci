package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The runtime shim invokes the production image helper against a test home
// and starts a stand-in client that reads its native config. All remaining
// operations go through the usual runtime fixture and real cenci open binary.
func TestOpenAppliesHostPreferencesBeforeStartingClient(t *testing.T) {
	python := os.Getenv("CENCI_TEST_PYTHON")
	if python == "" {
		python = "/usr/bin/python3"
	}
	if err := exec.Command(python, "-I", "-c", "import tomlkit, tomllib").Run(); err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatal("requires python3 with tomlkit and tomllib")
		}
		t.Skip("requires python3 with tomlkit and tomllib")
	}
	helper, err := filepath.Abs("../sandbox/lib/host-preferences.py")
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			f := newAddDirOpenFixture(t)
			sandboxHome := t.TempDir()
			mkdirAll(t, sandboxHome, "."+agent)
			name := "settings.json"
			if agent == "codex" {
				name = "config.toml"
			}
			source := filepath.Join(mkdirAll(t, f.home, "."+agent), name)
			policy := filepath.Join(mkdirAll(t, f.home, ".config", "cenci"), "config.json")
			write := func(path, data string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(policy, fmt.Sprintf(`{"sandboxHostProfiles":{%q:{"repositories":[%q],"preferences":{"source":%q}}}}`, agent, f.repo, source))
			for _, runtime := range []string{"docker", "podman"} {
				path := filepath.Join(f.fakeDir, runtime)
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				shim := `#!/bin/sh
case "$*" in
  *'/usr/local/bin/lib/host-preferences.py'*)
     exec "$TEST_PYTHON" -I "$TEST_HELPER" "$TEST_AGENT" "$TEST_VOLUME" apply ;;
  'exec -it '*)
     exec "$TEST_PYTHON" -I -c 'import json,sys,tomllib,pathlib; f=pathlib.Path(sys.argv[1]); d=json.loads(f.read_text()) if f.suffix==".json" else tomllib.loads(f.read_text());print("model read by client",d.get("model","absent"))' "$TEST_CONFIG" ;;
esac
exec "$0.original" "$@"
`
				if err := os.WriteFile(path, []byte(shim), 0700); err != nil {
					t.Fatal(err)
				}
			}
			container := strings.Replace(f.container, "claude-", agent+"-", 1)
			for _, phase := range []string{"fresh", "attach", "disabled"} {
				env := f.env
				if phase != "fresh" {
					env = f.runningEnvNamed(container)
				}
				env = append(env, "TEST_PYTHON="+python, "TEST_HELPER="+helper, "TEST_VOLUME="+sandboxHome, "TEST_AGENT="+agent,
					"FAKE_AGENT_MOUNTS=cenci-agent-cli-"+agent+"|/opt/cenci-agent|false\n",
					"TEST_CONFIG="+filepath.Join(sandboxHome, "."+agent, name))
				model := "model-" + phase
				if agent == "claude" {
					data, _ := json.Marshal(map[string]string{"model": model})
					write(source, string(data))
				} else {
					write(source, fmt.Sprintf("model=%q\n", model))
				}
				if phase == "disabled" {
					write(policy, "{}")
					model = "absent"
				}
				result := runCenci(t, env, f.repo, "open", "--agent", agent)
				if result.exitCode != 0 || !strings.Contains(result.stdout, "model read by client "+model) {
					t.Fatalf("%s: exit %d, stdout=%s stderr=%s", phase, result.exitCode, result.stdout, result.stderr)
				}
			}
		})
	}
}
