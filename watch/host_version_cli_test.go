package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise all public automatic update entry points against one host profile.
// Keep the caller's fake runtime and real host version probe independent.
func TestHostVersionOpenSweepAndUnpin(t *testing.T) {
	account := filepath.Join(filepath.Dir(binaryPath), "account")
	config := filepath.Join(account, ".config", "cenci", "config.json")
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(config) })
	dir := t.TempDir()
	program := filepath.Join(dir, "client.go")
	if err := os.WriteFile(program, []byte(`package main
import ("fmt"; "os")
func main(){if len(os.Args)!=2 || os.Args[1]!="--version"{os.Exit(2)};fmt.Println("3.2.1 (Claude Code)")}`), 0600); err != nil {
		t.Fatal(err)
	}
	client := filepath.Join(dir, "claude")
	if out, err := exec.Command("go", "build", "-o", client, program).CombinedOutput(); err != nil {
		t.Fatalf("build native fixture: %v\n%s", err, out)
	}
	data, err := json.Marshal(map[string]any{"sandboxHostProfiles": map[string]any{"claude": map[string]any{"version": map[string]string{"mode": "follow-host", "executable": client}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"open", "cs"}, {"sandbox", "update-agent", "--all"}, {"sandbox", "update-agent", "--unpin"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			fake := t.TempDir()
			log := writeDockerOnlyRuntime(t, fake)
			assets := writeAssetFixture(t)
			// No ambient runtime binaries. The scripted runtime only calls date, the
			// launcher uses the test tmux, and this fixture intentionally has no git.
			if err := os.Symlink("/bin/date", filepath.Join(fake, "date")); err != nil {
				t.Fatal(err)
			}
			env, home, _ := openTestEnv(t, fake, assets)
			env = append(env, "PATH="+fake, "HOME="+home, "FAKE_VOLUMES=cenci-agent-cli-claude\n", "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
			cmd := exec.Command(binaryPath, args...)
			cmd.Env = env
			cmd.Dir = t.TempDir()
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v: %v\n%s", args, err, out)
			}
			lines := callLogLines(t, log)
			sync, clear, attach := -1, -1, -1
			for i, line := range lines {
				if strings.HasSuffix(line, "agent-cli.sh update claude 3.2.1 --follow-host") {
					sync = i
				}
				if strings.HasSuffix(line, "agent-cli.sh unpin claude") {
					clear = i
				}
				if strings.Contains(line, "/opt/cenci-agent/current/node_modules/.bin/claude --dangerously-") {
					attach = i
				}
			}
			if sync < 0 || args[0] == "open" && attach <= sync || args[len(args)-1] == "--unpin" && (clear < 0 || clear >= sync) {
				t.Fatalf("missing ordered host match, calls:\n%s", strings.Join(lines, "\n"))
			}
		})
	}
}
