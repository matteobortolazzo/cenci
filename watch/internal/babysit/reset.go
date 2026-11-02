package babysit

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrAgentRefused: the recorded agent may not be re-armed (a disabled agent,
// or one missing an arm-time workflow). The check runs before any write or
// signal, so nothing was changed.
var ErrAgentRefused = errors.New("recorded agent refused")

// resetExitWait bounds how long Reset waits for a SIGTERMed supervisor to
// exit, and resetPollInterval is how often it re-checks. Both are test seams.
var (
	resetExitWait     = 10 * time.Second
	resetPollInterval = 100 * time.Millisecond
)

// defaultResetInterval re-arms a state that recorded no interval.
const defaultResetInterval = 15 * time.Minute

// Reset zeroes the retry budget of the paused (or live) supervisor for pr and
// re-arms it with the settings recorded in its state (#1391). It is host-only:
// a reset lifts fixCap, the bound on automated repair launches, so it refuses
// inside a sandbox and no event-socket kind or daemon handler carries it.
//
// Only the five budget fields are zeroed (ciFixAttempts, ciRepairPending,
// lastRepairedCiHeadSha, conflictFixAttempts, conflictNotifiedHeadSha);
// feedback bookkeeping and conflictRepairHeadSha are kept. A live supervisor
// that processOwned verifies is sent SIGTERM and awaited for resetExitWait.
// The supervisor has no SIGTERM handler, so its deferred lock removal never
// runs and Reset removes the lock itself. refusal is the CLI's agent check
// (babysitAgentRefusal); it lives in package main, so it is passed in and
// consulted before anything is written or signalled.
//
// Arming goes through Run's arming branch, which a supervisor child skips, so
// Reset refuses when CENCI_BABYSIT_SUPERVISOR is set. Accepted race: a reset
// that lands between another arm's parent save and its child taking the lock
// (pid 0, no lock yet) can start a second child, but the child's O_EXCL lock
// open fails, so only one supervisor survives.
func Reset(pr, explicit string, refusal func(agent string) string) error {
	cleanPR := strings.TrimPrefix(pr, "#")
	if os.Getenv("CENCI_SANDBOX") == "1" {
		return fmt.Errorf("not reset: the babysit supervisor runs on the host and its retry budget can only be reset there; run `cenci babysit reset %s` from a host tmux pane", cleanPR)
	}
	if os.Getenv("CENCI_BABYSIT_SUPERVISOR") != "" {
		return errors.New("not reset: cannot re-arm from inside a babysit supervisor; run `cenci babysit reset` from a host tmux pane")
	}
	if n, err := strconv.Atoi(cleanPR); err != nil || n <= 0 {
		return errors.New("PR must be a positive integer")
	}
	dir, err := stateDir(explicit)
	if err != nil {
		return err
	}
	repo, err := repository()
	if err != nil {
		return err
	}
	path := statePath(dir, repo, cleanPR)
	lockPath := path + ".lock"

	s, err := readStateStrict(path)
	if errors.Is(err, os.ErrNotExist) {
		return &classifiedError{class: ErrNoSupervisor, cause: err, msg: fmt.Sprintf("no supervisor found for PR #%s", cleanPR)}
	}
	if err != nil {
		return err
	}
	if refusal != nil {
		if reason := refusal(s.Agent); reason != "" {
			return &classifiedError{class: ErrAgentRefused, msg: reason}
		}
	}

	if s.PID > 0 {
		if !processOwned(s.PID, cleanPR) {
			return &classifiedError{class: ErrSupervisorUnverified, msg: fmt.Sprintf("refusing to signal pid %d: it is not the recorded cenci babysit process", s.PID)}
		}
		if err := terminateAndWait(s.PID, cleanPR); err != nil {
			return err
		}
		if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		// Re-read only now: the supervisor may have saved while it was dying,
		// and zeroing the first read would overwrite that with stale data.
		if s, err = readStateStrict(path); err != nil {
			return err
		}
	} else if _, err := os.Stat(lockPath); err == nil {
		owner, _ := os.ReadFile(lockPath)
		return fmt.Errorf("supervisor already running for PR #%s (%s)", cleanPR, strings.TrimSpace(string(owner)))
	} else if !os.IsNotExist(err) {
		return err
	}

	s.FixAttempts = 0
	s.ConflictFixAttempts = 0
	s.RepairPending = false
	s.LastRepairedSHA = ""
	s.ConflictNotifiedSHA = ""
	s.PID = 0
	s.UpdatedAt = time.Now().UTC()
	if err := save(path, s); err != nil {
		return err
	}

	interval := time.Duration(s.IntervalSeconds) * time.Second
	if s.IntervalSeconds <= 0 {
		interval = defaultResetInterval
	}
	fmt.Printf("Reset the retry budget for PR #%s.\n", cleanPR)
	if err := Run(Options{PR: cleanPR, Agent: s.Agent, Interval: interval, StateDir: dir, Session: s.LaunchSession, Dir: s.LaunchDir}); err != nil {
		return fmt.Errorf("budget reset for PR #%s but not re-armed: %w; re-arm with cenci babysit %s --agent %s", cleanPR, err, cleanPR, s.Agent)
	}
	return nil
}

// terminateAndWait sends SIGTERM to the verified supervisor pid and polls
// processOwned until it is gone or resetExitWait elapses.
func terminateAndWait(pid int, pr string) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stop pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(resetExitWait)
	for processOwned(pid, pr) {
		if !time.Now().Before(deadline) {
			return fmt.Errorf("supervisor for PR #%s did not exit within %s of SIGTERM; nothing was reset", pr, resetExitWait)
		}
		time.Sleep(resetPollInterval)
	}
	return nil
}
