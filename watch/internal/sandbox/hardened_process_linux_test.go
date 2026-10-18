package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

// Model the npm Codex launcher and its native, non-dumpable child. Both stay
// alive until the test closes stdin; no installed Codex or container is needed.
func TestHardenedProcessHelper(t *testing.T) {
	mode := os.Getenv("CENCI_TEST_HARDENED_PROCESS")
	if mode == "" {
		return
	}
	if mode == "child" {
		// A native child may remain alive after its wrapper receives TERM.
		// The cleanup must retain and escalate the child's own PID.
		signal.Ignore(syscall.SIGTERM)
		if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 4, 0, 0, 0, 0, 0); errno != 0 {
			panic(errno)
		}
		if path := os.Getenv("CENCI_TEST_CONVERSATION_LOCK"); path != "" {
			f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				panic(err)
			}
			if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				panic(err)
			}
			defer func() { _ = f.Close() }()
		}
		fmt.Println(os.Getpid())
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHardenedProcessHelper$")
	cmd.Env = append(os.Environ(), "CENCI_TEST_HARDENED_PROCESS=child")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestCloseHardenedLaunchReleasesConversationLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "conversation.lock")
	t.Setenv("CENCI_TEST_CONVERSATION_LOCK", lockPath)
	parent, child := spawnHardenedLaunch(t, launchIDOne)
	t.Setenv("CENCI_TEST_CONVERSATION_LOCK", "")
	otherParent, otherChild := spawnHardenedLaunch(t, launchIDTwo)
	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != syscall.EWOULDBLOCK {
		t.Fatalf("fixture must hold the conversation lock before cleanup: %v", err)
	}

	// Only the container transport is replaced: execute the real scan,
	// identity probes and signals against our real Linux processes. Guard
	// every signal so even a broken scan cannot affect unrelated processes.
	// The runtime must be a closed-set name (docker/podman), so the stub is a
	// fake "docker" found first on PATH rather than an absolute path.
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	const rt = "docker"
	exectest.WriteExecutable(t, filepath.Join(binDir, rt), fmt.Sprintf(`#!/bin/sh
[ "$1 $2 $3 $4" = "exec -u dev codex-cenci-test" ] || [ "$1 $2 $3 $4" = "exec -u root codex-cenci-test" ] || exit 90
shift 4
if [ "$1" = kill ]; then
  case "$3" in %d|%d) ;; *) exit 91;; esac
fi
exec "$@"
`, parent, child))
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = KillLaunch(ctx, rt, "codex-cenci-test", launchIDOne, 0, &stdout, &stderr)
	// Unrelated non-dumpable host processes can produce a partial-result
	// diagnostic; it must not prevent cleanup of the positively matched tree.
	if err != nil && !errors.Is(err, ErrUnreadableProcesses) {
		t.Fatalf("KillLaunch: %v; stderr: %s", err, stderr.String())
	}
	deadline := time.Now().Add(time.Second)
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		t.Fatalf("resume still blocked after cleanup: %v", err)
	}
	for _, pid := range []int{otherParent, otherChild} {
		if err := syscall.Kill(pid, 0); err != nil {
			t.Errorf("cleanup affected another live launch (pid %d): %v", pid, err)
		}
	}
}

func spawnHardenedLaunch(t *testing.T, id string) (parent, child int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHardenedProcessHelper$")
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX_PANE=") || strings.HasPrefix(kv, "CENCI_TMUX_SOCKET=") || strings.HasPrefix(kv, LaunchIDEnvName+"=") {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "CENCI_TEST_HARDENED_PROCESS=parent")
	if id != "" {
		cmd.Env = append(cmd.Env, "TMUX_PANE=%7", "CENCI_TMUX_SOCKET="+scanTestSocket)
		if id != "legacy" {
			cmd.Env = append(cmd.Env, LaunchIDEnvName+"="+id)
		}
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = in.Close()
		_ = cmd.Wait()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	child, err = strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(environPath(child)); !os.IsPermission(err) {
		t.Skipf("requires non-dumpable environ to be denied (no CAP_SYS_PTRACE): %v", err)
	}
	return cmd.Process.Pid, child
}

func TestHardenedChildIsFoundByBothCleanupScans(t *testing.T) {
	parent, child := spawnHardenedLaunch(t, launchIDOne)
	_, other := spawnHardenedLaunch(t, launchIDTwo)
	_, unowned := spawnHardenedLaunch(t, "")

	t.Run("orphan scan retains child identity and launcher ownership", func(t *testing.T) {
		rows := runScanScript(t)
		for _, pid := range []int{parent, child} {
			row := rows[strconv.Itoa(pid)]
			if row.unreadable || row.pane != "%7" || row.socket != scanTestSocket || row.launchID != launchIDOne || row.start != procStartTime(t, pid) {
				t.Errorf("pid %d: got %+v, want own start time and launcher's ownership", pid, row)
			}
		}
		if row := rows[strconv.Itoa(other)]; row.launchID != launchIDTwo {
			t.Errorf("other launch's child lost its ownership: %+v", row)
		}
		if !rows[strconv.Itoa(unowned)].unreadable {
			t.Error("child without a tagged ancestor must remain visibly unreadable")
		}
	})
	t.Run("explicit close finds child without matching another launch", func(t *testing.T) {
		rows, unreadable, _ := runLaunchProbeScriptWithUnreadable(t, launchIDOne)
		for _, pid := range []int{parent, child} {
			if rows[strconv.Itoa(pid)] != procStartTime(t, pid) || unreadable[strconv.Itoa(pid)] {
				t.Errorf("pid %d missing or unreadable: rows=%v unreadable=%v", pid, rows, unreadable)
			}
		}
		if _, ok := rows[strconv.Itoa(other)]; ok {
			t.Error("matched another launch's hardened child")
		}
		if !unreadable[strconv.Itoa(unowned)] {
			t.Error("child without a tagged ancestor must remain visibly unreadable")
		}
	})
}

func TestHardenedChildOfLegacyLauncherRetainsSocketAndPane(t *testing.T) {
	_, child := spawnHardenedLaunch(t, "legacy")
	row := runScanScript(t)[strconv.Itoa(child)]
	if row.unreadable || row.pane != "%7" || row.socket != scanTestSocket || row.launchID != "" || row.start != procStartTime(t, child) {
		t.Fatalf("legacy launch's child must retain socket/pane ownership and its own identity: %+v", row)
	}
}
