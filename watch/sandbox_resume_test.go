package main_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

func TestOpenCodexCleansClosedPaneBeforeResume(t *testing.T) {
	for _, scanFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("scan failure=%t", scanFails), func(t *testing.T) {
			dir := t.TempDir()
			callLog := writeScriptedRuntimes(t, dir)
			env, _, socketDir := openTestEnv(t, dir, writeAssetFixture(t))
			released := filepath.Join(dir, "released")
			for _, name := range []string{"docker", "podman"} {
				original := filepath.Join(dir, name+"-original")
				if err := os.Rename(filepath.Join(dir, name), original); err != nil {
					t.Fatal(err)
				}
				exectest.WriteExecutable(t, filepath.Join(dir, name), fmt.Sprintf(`#!/bin/sh
case "$*" in
  *'exec -u dev codex-cenci-default sh -c'*'TMUX_PANE='*)
    if [ "%t" = true ]; then echo scan-unavailable >&2; exit 23; fi
    printf '4242\t%%%%9\t123\t/tmp/cenci-resume-test\taaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n'
    printf '4243\t%%%%8\t456\t/tmp/cenci-resume-test\tbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n'
    exit 0;;
  'exec -u root codex-cenci-default kill -TERM 4242') exit 0;;
  'exec -u root codex-cenci-default kill -KILL 4242') : > %s; exit 0;;
  'exec -u root codex-cenci-default sh -c'*'__GONE__'*) printf '123\n'; exit 0;;
  'exec -u root'*'kill '*) echo unexpected-signal >&2; exit 91;;
  'exec -it '*)
    if [ "%t" = false ] && [ ! -f %s ]; then echo conversation-still-locked >&2; exit 24; fi;;
esac
exec %s "$@"
`, scanFails, exectest.ShellQuote(released), scanFails, exectest.ShellQuote(released), exectest.ShellQuote(original)))
			}
			exectest.WriteExecutable(t, filepath.Join(dir, "tmux"), `#!/bin/sh
case "$*" in *list-panes*) printf '%%8\tbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n';; esac
`)
			cmd := exec.Command(binaryPath, "open", "xa", "--", "resume", "--last")
			cmd.Dir = t.TempDir()
			cmd.Env = append(env,
				"CENCI_SANDBOX_REAP_GRACE_SECS=0",
				"FAKE_PS=codex-cenci-default\n",
				"FAKE_AGENT_MOUNTS=cenci-agent-cli-codex|/opt/cenci-agent|false\n",
				"FAKE_INSPECT_LABEL=detached",
				"FAKE_INSPECT_MOUNTS=/workspace::/workspace\n/home/dev::/home/dev\n"+socketDir+"::/run/user/1000/cenci\n",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("open xa resume: %v\n%s", err, out)
			}
			if strings.Contains(string(out), "unexpected-signal") {
				t.Fatalf("cleanup signaled a live launch: %s", out)
			}
			if scanFails && (!strings.Contains(string(out), "Warning: could not clean up closed Codex sessions") || !strings.Contains(string(out), "scan-unavailable")) {
				t.Fatalf("cleanup failure must warn without blocking launch: %s", out)
			}
			if line := attachLine(t, callLogLines(t, callLog)); !strings.HasSuffix(line, "resume --last") {
				t.Fatalf("resume arguments lost: %s", line)
			}
		})
	}
}
