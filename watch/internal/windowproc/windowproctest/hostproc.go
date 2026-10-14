package windowproctest

import (
	"bufio"
	"os/exec"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// HostProc is a real child process of the test, reaped by a background Wait so
// its death is observable (an unreaped zombie would still answer kill(pid, 0)).
// The real-process tests use it for the host process-group contract.
type HostProc struct {
	Cmd    *exec.Cmd
	PID    int
	done   chan struct{}
	status syscall.WaitStatus
}

// SleepBinary resolves the real sleep binary. Call it, and start the host
// processes, before Install replaces PATH.
func SleepBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("the real-process tests need a sleep binary: %v", err)
	}
	return path
}

// StartHostProc starts name with attr and reaps it in the background. It is
// killed when the test ends.
func StartHostProc(t *testing.T, attr *syscall.SysProcAttr, name string, args ...string) *HostProc {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = attr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", cmd.Args, err)
	}
	p := &HostProc{Cmd: cmd, PID: cmd.Process.Pid}
	p.watch(t)
	return p
}

func (p *HostProc) watch(t *testing.T) {
	t.Helper()
	p.done = make(chan struct{})
	go func() {
		_ = p.Cmd.Wait()
		if st, ok := p.Cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
			p.status = st
		}
		close(p.done)
	}()
	t.Cleanup(func() {
		_ = p.Cmd.Process.Kill()
		<-p.done
	})
}

// HarmlessPanePID starts a lone sleeper in its own process group and returns its
// pid. Tests whose sandbox panes need *some* pane_pid use it, so that a
// regression that wrongly falls back to the host process-group path can only
// ever signal this sleeper, never an unrelated real process. Call it before
// Install replaces PATH.
func HarmlessPanePID(t *testing.T) string {
	t.Helper()
	p := StartHostProc(t, &syscall.SysProcAttr{Setpgid: true}, SleepBinary(t), "300")
	return strconv.Itoa(p.PID)
}

// StartCooperativeGroup starts a process group of two sleepers (they die on
// SIGTERM) plus a third sleeper under Setsid -- the babysit supervisor shape,
// in another session and group, which a process-group kill must leave alone.
func StartCooperativeGroup(t *testing.T) (leader, member, survivor *HostProc) {
	t.Helper()
	sleep := SleepBinary(t)
	leader = StartHostProc(t, &syscall.SysProcAttr{Setpgid: true}, sleep, "300")
	member = StartHostProc(t, &syscall.SysProcAttr{Setpgid: true, Pgid: leader.PID}, sleep, "300")
	survivor = StartHostProc(t, &syscall.SysProcAttr{Setsid: true}, sleep, "300")
	return leader, member, survivor
}

// StartTermIgnoringGroup starts a process group whose leader is a shell that
// ignores SIGTERM (only SIGKILL ends it) plus a cooperative sleeper member. It
// returns once the shell has installed its trap.
func StartTermIgnoringGroup(t *testing.T) (leader, member *HostProc) {
	t.Helper()
	sleep := SleepBinary(t)
	cmd := exec.Command("/bin/sh", "-c", `trap "" TERM; echo ready; read x`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start TERM-ignoring leader: %v", err)
	}
	t.Cleanup(func() { _ = stdin.Close() })
	// The trap must be installed before the killer runs, or the TERM would
	// simply kill the shell and prove nothing.
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("TERM-ignoring leader readiness = %q, %v", line, err)
	}
	leader = &HostProc{Cmd: cmd, PID: cmd.Process.Pid}
	leader.watch(t)
	member = StartHostProc(t, &syscall.SysProcAttr{Setpgid: true, Pgid: leader.PID}, sleep, "300")
	return leader, member
}

// RequireDeathBy waits for the process to die and checks that one of the
// accepted signals ended it.
func (p *HostProc) RequireDeathBy(t *testing.T, name string, want ...syscall.Signal) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s (pid %d) is still alive", name, p.PID)
	}
	if !p.status.Signaled() || !slices.Contains(want, p.status.Signal()) {
		t.Errorf("%s (pid %d) ended with %v, want death by one of %v", name, p.PID, p.status, want)
	}
}

// RequireAlive checks that the process has not died.
func (p *HostProc) RequireAlive(t *testing.T, name string) {
	t.Helper()
	select {
	case <-p.done:
		t.Errorf("%s (pid %d) died (%v), want it to survive", name, p.PID, p.status)
	default:
	}
	if err := syscall.Kill(p.PID, 0); err != nil {
		t.Errorf("%s (pid %d) is not alive: %v", name, p.PID, err)
	}
}
