package main_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The compiled CLI can reach only these fakes: no real agents, container
// runtimes, tmux, credentials, or network are available on its PATH.
func remoteCommand(t *testing.T, alias bool, args ...string) (*exec.Cmd, string) {
	t.Helper()
	dir := t.TempDir()
	for _, agent := range []string{"claude", "codex"} {
		script := `#!/bin/sh
printf '%s\000' "$@" > "$HOME/argv"
pwd > "$HOME/cwd"
printf '%s' "$$" > "$HOME/pid"
IFS= read -r input
printf 'native output: %s\n' "$input"
printf 'native diagnostic\n' >&2
exit "${REMOTE_TEST_EXIT:-0}"
`
		if err := os.WriteFile(filepath.Join(dir, agent), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := binaryPath
	if alias {
		bin = buildArgv0Alias(t, dir, "cn")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, bin, append([]string{"remote"}, args...)...)
	cmd.Env = []string{"PATH=" + dir, "HOME=" + dir}
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("from terminal\n")
	return cmd, dir
}

func TestRemoteLaunch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		alias      bool
		args, want []string
	}{
		{"claude", false, []string{"claude"}, []string{"remote-control"}},
		{"named claude alias", true, []string{"claude", "--name", "My project; $(touch injected)"}, []string{"remote-control", "--name", "My project; $(touch injected)"}},
		{"name equals", false, []string{"claude", "--name=My project"}, []string{"remote-control", "--name", "My project"}},
		{"claude passthrough", false, []string{"claude", "--", "--spawn", "worktree", "--capacity", "3", "--", ""}, []string{"remote-control", "--spawn", "worktree", "--capacity", "3", "--", ""}},
		{"codex", false, []string{"codex"}, []string{"remote-control"}},
		{"codex start", true, []string{"codex", "start"}, []string{"remote-control", "start"}},
		{"codex pair", true, []string{"codex", "pair", "--", "--json"}, []string{"remote-control", "pair", "--json"}},
		{"codex stop", false, []string{"codex", "stop"}, []string{"remote-control", "stop"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, dir := remoteCommand(t, tc.alias, tc.args...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			data, err := os.ReadFile(filepath.Join(dir, "argv"))
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("argv = %q, want %q", got, tc.want)
			}
			pid, err := os.ReadFile(filepath.Join(dir, "pid"))
			if err != nil {
				t.Fatal(err)
			}
			if string(pid) != strconv.Itoa(cmd.Process.Pid) {
				t.Fatalf("agent did not replace CLI: pid %s != %d", pid, cmd.Process.Pid)
			}
		})
	}
}

func TestRemoteDirectoryAndTerminalIO(t *testing.T) {
	cmd, dir := remoteCommand(t, false, "claude", "--dir", "project with spaces")
	project := filepath.Join(dir, "project with spaces")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %s", err, &stderr)
	}
	if stdout.String() != "native output: from terminal\n" || stderr.String() != "native diagnostic\n" {
		t.Fatalf("terminal streams: stdout=%q stderr=%q", &stdout, &stderr)
	}
	cwd, err := os.ReadFile(filepath.Join(dir, "cwd"))
	if err != nil {
		t.Fatal(err)
	}
	// Resolve macOS's /var -> /private/var temporary-directory symlink.
	want, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(cwd)) != want {
		t.Fatalf("cwd=%q, want %q", cwd, want)
	}
}

func TestRemoteInvalidInputDoesNotLaunch(t *testing.T) {
	for _, args := range [][]string{
		{}, {"unknown"}, {"claude", "pair"}, {"codex", "restart"},
		{"claude", "--typo"}, {"claude", "extra"}, {"codex", "pair", "extra"},
		{"claude", "--name"}, {"claude", "--name="}, {"claude", "--name", "  "},
		{"codex", "--name", "My machine"}, {"codex", "pair", "--name=x"},
		{"claude", "--dir="}, {"claude", "--sandbox"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd, dir := remoteCommand(t, false, args...)
			out, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
				t.Fatalf("want exit 2, got %v: %s", err, out)
			}
			if len(out) == 0 {
				t.Fatal("missing usage diagnostic")
			}
			if _, err := os.Stat(filepath.Join(dir, "argv")); !os.IsNotExist(err) {
				t.Fatalf("agent launched: %v", err)
			}
		})
	}
}

func TestRemoteDryRunAndHelpDoNotLaunch(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--help"}, "Usage:"},
		{[]string{"claude", "--help"}, "--name"},
		{[]string{"codex", "pair", "--help"}, "pair"},
		{[]string{"claude", "--name=My project", "--dry-run"}, `"remote-control" "--name" "My project"`},
		{[]string{"codex", "pair", "--dry-run", "--", "--json"}, `"remote-control" "pair" "--json"`},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			cmd, dir := remoteCommand(t, true, tc.args...)
			// Dry-run and help must also work before an agent is installed.
			cmd.Env = []string{"PATH=/nonexistent", "HOME=" + dir}
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("%v: %s; want %s", err, out, tc.want)
			}
			if _, err := os.Stat(filepath.Join(dir, "argv")); !os.IsNotExist(err) {
				t.Fatalf("agent launched: %v", err)
			}
		})
	}
}

func TestRemoteNativeFailure(t *testing.T) {
	cmd, _ := remoteCommand(t, false, "codex", "pair")
	cmd.Env = append(cmd.Env, "REMOTE_TEST_EXIT=7")
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 7 {
		t.Fatalf("want native exit 7, got %v: %s", err, out)
	}
	if !strings.Contains(string(out), "native diagnostic") {
		t.Fatalf("missing native error: %s", out)
	}
}

func TestRemoteSignalReachesNativeProcess(t *testing.T) {
	cmd, dir := remoteCommand(t, false, "claude")
	cmd.Stdin = nil
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	// The fake writes its PID before blocking on terminal input. Wait until
	// native code is running, rather than accidentally signaling the wrapper.
	deadline := time.Now().Add(5 * time.Second)
	for {
		pid, err := os.ReadFile(filepath.Join(dir, "pid"))
		if err == nil && string(pid) == strconv.Itoa(cmd.Process.Pid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native process did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	exit, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected signal exit, got %v", err)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("native SIGTERM was not preserved: %v", exit)
	}
}

func TestRemoteMissingExecutableAndDirectory(t *testing.T) {
	for _, failure := range []string{"executable", "missing directory", "file as directory", "exec format"} {
		cmd, dir := remoteCommand(t, false, "claude")
		want := "claude"
		switch failure {
		case "missing directory":
			cmd.Args = append(cmd.Args, "--dir", filepath.Join(dir, "missing"))
			want = "directory"
		case "file as directory":
			cmd.Args = append(cmd.Args, "--dir", filepath.Join(dir, "claude"))
			want = "not a directory"
		case "exec format":
			if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("invalid executable"), 0o755); err != nil {
				t.Fatal(err)
			}
			want = "launch claude"
		default:
			cmd.Env = []string{"PATH=/nonexistent", "HOME=" + dir}
		}
		out, err := cmd.CombinedOutput()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			t.Fatalf("want exit 1, got %v: %s", err, out)
		}
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing %q diagnostic: %s", want, out)
		}
		if _, err := os.Stat(filepath.Join(dir, "argv")); !os.IsNotExist(err) {
			t.Fatalf("agent launched: %v", err)
		}
	}
}

func TestRemoteDryRunUsesSelectedDirectory(t *testing.T) {
	cmd, dir := remoteCommand(t, false, "codex", "start", "--dir=project", "--dry-run")
	project := filepath.Join(dir, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "directory: "+strconv.Quote(project)) {
		t.Fatalf("dry-run directory: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "argv")); !os.IsNotExist(err) {
		t.Fatalf("dry-run launched agent: %v", err)
	}
}

func TestCnRemoteAfterSeparatorStillLaunchesSandbox(t *testing.T) {
	cnPath := buildArgv0Alias(t, t.TempDir(), "cn")
	fakeDir := t.TempDir()
	callLog := writeScriptedRuntimes(t, fakeDir)
	assets := writeAssetFixture(t)
	env, _, _ := openTestEnv(t, fakeDir, assets)
	cmd := exec.Command(cnPath, "--", "remote")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cn -- remote: %v: %s", err, out)
	}
	line := attachLine(t, callLogLines(t, callLog))
	if !strings.HasSuffix(line, " remote") {
		t.Fatalf("sandbox must receive remote argument: %s", line)
	}
}
