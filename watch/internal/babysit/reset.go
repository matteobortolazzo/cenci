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
// Only eight fields are cleared: the five budget fields (ciFixAttempts,
// ciRepairPending, lastRepairedCiHeadSha, conflictFixAttempts,
// conflictNotifiedHeadSha), the two CI-cap episode markers (ciRepairPaused,
// ciCapNotified, #1456) and the ci-repair tracking marker (ciRepairTracked,
// #1457), so a legacy same-head hold ends too; feedback bookkeeping and
// conflictRepairHeadSha are kept. A CI-paused supervisor keeps its pid while paused, so a recorded pid
// has three outcomes (#1490):
//   - owned and live: processOwned verifies it, it is sent SIGTERM and awaited
//     for resetExitWait.
//   - gone: processOwned is false and processGone proves the pid no longer
//     exists (ESRCH), e.g. after a reboot. Nothing is signalled; the stale lock
//     is cleared (unless it names a live supervisor for the PR or cannot be
//     parsed, which refuses with "supervisor already running"), a stderr note
//     says so, and the reset carries on as if no pid were recorded.
//   - live but not ours: any other probe result is refused ("refusing to
//     signal pid") with state and lock untouched.
//
// The pid sequence is quiesceSupervisor, shared with Resolve (#1472). A state
// `cenci babysit stop` left (status stopped) has no agent and no budget: Reset
// refuses it with the re-arm hint.
//
// The supervisor has no SIGTERM handler, so its deferred lock removal never
// runs and Reset removes the lock itself. refusal is the CLI's agent check
// (babysitAgentRefusal); it lives in package main, so it is passed in and
// consulted before anything is written or signalled.
//
// Arming goes through Run's arming branch, which a supervisor child skips, so
// Reset refuses when CENCI_BABYSIT_SUPERVISOR is set. Accepted race: a reset
// that lands between another arm's parent save and its child taking the lock
// (pid 0, no lock yet) can start a second child, but the child's O_EXCL lock
// open fails, so only one supervisor survives. The gone path accepts the same
// race: a child that takes the lock after clearStaleLock found none loses its
// O_EXCL open to whichever supervisor Reset starts, or the reverse. Likewise
// clearStaleLock reads the lock and then removes it by path, so a lock created
// by a concurrent arm between the read and the remove can be deleted too; the
// losing child's O_EXCL open is the backstop.
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

	s, err := quiesceSupervisor(path, cleanPR, func(s State) error {
		if stoppedFeedbackOnly(s) {
			return fmt.Errorf("PR #%s is stopped: it has no agent or retry budget to reset; re-arm it with cenci babysit %s --agent <claude|codex>", cleanPR, cleanPR)
		}
		if refusal != nil {
			if reason := refusal(s.Agent); reason != "" {
				return &classifiedError{class: ErrAgentRefused, msg: reason}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	s.FixAttempts = 0
	s.ConflictFixAttempts = 0
	s.RepairPending = false
	s.LastRepairedSHA = ""
	s.ConflictNotifiedSHA = ""
	s.CIRepairPaused = false
	s.CICapNotified = false
	s.CIRepairTracked = false
	s.PID = 0
	s.UpdatedAt = time.Now().UTC()
	if err := save(path, s); err != nil {
		return fmt.Errorf("any supervisor for PR #%s was stopped, but the retry budget was not reset and nothing was re-armed: %w; fix the cause, then re-arm with cenci babysit %s --agent %s", cleanPR, err, cleanPR, s.Agent)
	}

	fmt.Printf("Reset the retry budget for PR #%s.\n", cleanPR)
	if err := reArm(dir, cleanPR, s); err != nil {
		return fmt.Errorf("budget reset for PR #%s but not re-armed: %w; re-arm with cenci babysit %s --agent %s", cleanPR, err, cleanPR, s.Agent)
	}
	return nil
}

// stoppedFeedbackOnly reports whether s is what `cenci babysit stop` leaves
// behind (#1472): feedback bookkeeping with no agent, budget or supervisor. Reset
// has nothing to zero or re-arm in it, and Resolve writes it without re-arming.
func stoppedFeedbackOnly(s State) bool { return s.Status == "stopped" }

// reArm starts a supervisor for s's PR with the settings s recorded: its agent,
// polling interval (defaultResetInterval when none was recorded) and launch
// target.
func reArm(dir, pr string, s State) error {
	interval := time.Duration(s.IntervalSeconds) * time.Second
	if s.IntervalSeconds <= 0 {
		interval = defaultResetInterval
	}
	return Run(Options{PR: pr, Agent: s.Agent, Interval: interval, StateDir: dir, Session: s.LaunchSession, TmuxSocket: s.LaunchSocket, Dir: s.LaunchDir})
}

// quiesceSupervisor is the verify/terminate/lock/re-read sequence Reset and
// Resolve share (#1472): it reads the state at path strictly, lets check refuse
// before anything is signalled or written, and leaves no supervisor running and
// no lock behind, returning the state the caller may now rewrite. Its states:
//   - no state file: ErrNoSupervisor. An unreadable one: an error wrapping
//     ErrStateUnreadable, text and path as readStateStrict gives them.
//   - a recorded pid that processOwned verifies is sent SIGTERM and awaited, its
//     lock removed, and the state re-read: the supervisor may have saved while it
//     was dying, and rewriting the first read would overwrite that save.
//   - a pid that is provably gone: nothing is signalled, the stale lock is
//     cleared (a live or unparseable one refuses) and the first read stays
//     current; a stderr note says so.
//   - a live pid that is not ours: refused, state and lock untouched.
//   - no pid: a lock file still refuses ("supervisor already running").
func quiesceSupervisor(path, pr string, check func(State) error) (State, error) {
	lockPath := path + ".lock"
	s, err := readStateStrict(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, &classifiedError{class: ErrNoSupervisor, cause: err, msg: fmt.Sprintf("no supervisor found for PR #%s", pr)}
	}
	if err != nil {
		return State{}, &classifiedError{class: ErrStateUnreadable, cause: err, msg: err.Error()}
	}
	if check != nil {
		if err := check(s); err != nil {
			return State{}, err
		}
	}

	if s.PID > 0 {
		if !processOwned(s.PID, pr) {
			if !processGone(s.PID) {
				return State{}, &classifiedError{class: ErrSupervisorUnverified, msg: fmt.Sprintf("refusing to signal pid %d: it is not the recorded cenci babysit process", s.PID)}
			}
			if err := clearStaleLock(lockPath, pr); err != nil {
				return State{}, err
			}
			fmt.Fprintln(os.Stderr, staleStateNote(s.PID, pr))
			return s, nil
		}
		if err := terminateAndWait(s.PID, pr); err != nil {
			return State{}, err
		}
		if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
			return State{}, err
		}
		fresh, err := readStateStrict(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return State{}, err
		case err != nil:
			return State{}, &classifiedError{class: ErrStateUnreadable, cause: err, msg: err.Error()}
		}
		return fresh, nil
	}
	if _, err := os.Stat(lockPath); err == nil {
		owner, _ := os.ReadFile(lockPath)
		return State{}, fmt.Errorf("supervisor already running for PR #%s (%s)", pr, strings.TrimSpace(string(owner)))
	} else if !os.IsNotExist(err) {
		return State{}, err
	}
	return s, nil
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
			return fmt.Errorf("supervisor for PR #%s did not exit within %s of SIGTERM; its state was not changed", pr, resetExitWait)
		}
		time.Sleep(resetPollInterval)
	}
	return nil
}
