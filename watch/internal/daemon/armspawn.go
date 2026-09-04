package daemon

// Ticket #1095: the real armSpawn seam -- resolves the forwarded pane's
// tmux session and the request's host repo directory, then starts a
// detached host `cenci babysit` with cmd.Dir and --session/--dir set to the
// resolved values, gated by a mutex-guarded token-bucket spawn-rate bound.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/hostrepo"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// armBucketBurst and armBucketRefill are hostArmSpawn's token-bucket
// tunables (plan Assumptions): a normal implement run arms once, so a
// burst of 5 is invisible to legitimate use, while capping a looping
// container to roughly 2 forever-living host supervisors per minute
// (armBucketRefill's one-token-per-30s refill rate).
const (
	armBucketBurst  = 5
	armBucketRefill = 30 * time.Second
)

// armSpawnBudget bounds the pane- and repo-resolution work hostArmSpawn does
// before ever starting a process, comfortably inside ipc.armResponseDeadline
// (5s) so a truthful-but-late nack is never read by the client as "arm
// status unknown".
const armSpawnBudget = 4 * time.Second

// armExecWaitDelay bounds how long cmd.Wait can block after a subprocess
// this file starts has already exited or been killed by armSpawnBudget's
// deadline, mirroring watch/internal/dispatch/mainsync.go's gitWaitDelay.
const armExecWaitDelay = 5 * time.Second

// armNow is hostArmSpawn's injectable clock seam for the token bucket, so
// tests can advance time deterministically without a real sleep --
// mirrors the package's existing closeGuard/killer/reaper/armSpawn seam
// style.
var armNow = time.Now

// armTokenBucket is a minimal mutex-guarded token bucket gating the whole
// hostArmSpawn path (#1095 Decisions: "gate the whole armSpawn path, not
// just the fork"). Its zero value is a full bucket on first use (last.IsZero()
// below), so neither newDaemon nor any test constructor needs to initialize
// it. It is reached only from the ipc.EventReceiver connection goroutine
// (see hostArmSpawn) and must never touch d.sessions/d.pending/d.attention
// or any d.loop-owned field.
type armTokenBucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// take consumes one token if available, refilling at armBucketRefill's rate
// (one token per interval) since the last call, capped at armBucketBurst.
// Reports whether a token was available.
func (b *armTokenBucket) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := armNow()
	if b.last.IsZero() {
		b.tokens = armBucketBurst
	} else if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += elapsed.Seconds() / armBucketRefill.Seconds()
		if b.tokens > armBucketBurst {
			b.tokens = armBucketBurst
		}
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// armResolveHostRepo is the injectable seam over hostrepo.Resolve (#1095),
// mirroring the package's existing armSpawn/closeGuard/killer/reaper seam
// style -- tests substitute a scripted fake so the daemon suite never
// shells out to a real container runtime or git.
var armResolveHostRepo = hostrepo.Resolve

// armSessionForPane is the injectable seam over the bounded
// `tmux display-message -t <pane> -p '#{session_name}'` pane->session
// resolution (watch/docs/tmux.md: empty output on an invalid/gone pane is
// not itself a command error and must be checked explicitly). Distinct
// from internal/babysit's currentTmuxSession, which resolves the *current*
// process's own $TMUX_PANE -- this seam resolves an arbitrary
// caller-supplied pane ID crossing the container->host trust boundary.
var armSessionForPane = defaultArmSessionForPane

// armStartBabysit is the injectable seam over the detached `cenci babysit`
// child's process start, mirroring internal/babysit's startSupervisor seam
// shape exactly: it receives the fully-built *exec.Cmd (argv, Dir, Env,
// SysProcAttr all already set by hostArmSpawn) so tests can capture and
// assert on it without a real process ever starting.
var armStartBabysit = defaultArmStartBabysit

// defaultArmSessionForPane is armSessionForPane's production
// implementation: a bounded `tmux display-message -t <pane> -p
// '#{session_name}'`, treating empty stdout as its own failure per
// watch/docs/tmux.md ("tmux display-message -t <nonexistent-pane> -p
// exits 0 with empty stdout instead of erroring").
func defaultArmSessionForPane(ctx context.Context, pane string) (string, error) {
	stdout, stderr, err := execArmTmux(ctx, "display-message", "-t", pane, "-p", "#{session_name}")
	if err != nil {
		return "", fmt.Errorf("tmux display-message: %s: %w", stderr, err)
	}
	if stdout == "" {
		return "", fmt.Errorf("tmux could not resolve a session for pane %q", pane)
	}
	return stdout, nil
}

// execArmTmux runs `tmux <args...>` bounded by ctx, with cmd.WaitDelay
// bounding a lingering grandchild, separate bounded stdout/stderr buffers,
// and cmd.Stdin = nil -- mirrors internal/babysit/tmux.go's execTmux
// bounded-subprocess convention (watch/AGENTS.md rule #5).
//
// ctx.Err() is read here, before hostArmSpawn's own deferred cancel() fires,
// and joined into runErr when it explains the failure -- mirrors
// internal/babysit/gh.go's execGh / internal/dispatch/gh.go's
// execGhBoundedCtx (#886). A context.WithTimeout deadline firing mid-Run()
// kills the process but Cmd.Wait does NOT itself wrap ctx.Err() into the
// returned error (Go only does that automatically when the deadline had
// already expired before Start()), and a cmd.WaitDelay-triggered kill (a
// lingering grandchild holding the pipes open) similarly returns bare
// exec.ErrWaitDelay -- both are, operationally, "ctx's deadline is why this
// failed", so both get context.DeadlineExceeded joined in directly. This is
// what lets hostArmSpawn's existing errors.Is(err, context.DeadlineExceeded)
// classification branches actually fire on a real subprocess timeout,
// instead of always seeing the step-specific reason (#1095 review fix).
func execArmTmux(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, "tmux", args...)
	cmd.WaitDelay = armExecWaitDelay
	cmd.Stdin = nil
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			runErr = errors.Join(runErr, context.DeadlineExceeded)
		}
		if errors.Is(runErr, exec.ErrWaitDelay) {
			runErr = errors.Join(runErr, context.DeadlineExceeded)
		}
	}
	return strings.TrimSpace(outBuf.String()), strings.TrimSpace(errBuf.String()), runErr
}

// defaultArmStartBabysit is armStartBabysit's production implementation: a
// plain detached cmd.Start() -- the caller (hostArmSpawn) has already set
// argv/Dir/Env/SysProcAttr.
func defaultArmStartBabysit(cmd *exec.Cmd) error {
	return cmd.Start()
}

// armSpawnEnvStripKeys are the environment variables hostArmSpawn's spawned
// child must never inherit (#1095 auto-adopted answer #8): CENCI_SANDBOX
// would make babysit.Run re-forward to the daemon again (an arm->spawn->arm
// loop), and CENCI_BABYSIT_SUPERVISOR would make the child run its tick loop
// in the foreground instead of detaching.
var armSpawnEnvStripKeys = []string{"CENCI_SANDBOX", "CENCI_BABYSIT_SUPERVISOR"}

// scrubArmSpawnEnv returns env (an os.Environ()-shaped slice) with every key
// in armSpawnEnvStripKeys removed.
func scrubArmSpawnEnv(env []string) []string {
	scrubbed := make([]string, 0, len(env))
	for _, kv := range env {
		strip := false
		for _, key := range armSpawnEnvStripKeys {
			if strings.HasPrefix(kv, key+"=") {
				strip = true
				break
			}
		}
		if !strip {
			scrubbed = append(scrubbed, kv)
		}
	}
	return scrubbed
}

// hostArmSpawn is the real armSpawn seam Ticket #1095 supplies, installed
// by daemon.Run as d.armSpawn = d.hostArmSpawn. It executes on the
// ipc.EventReceiver's connection goroutine (see handleArmRequest), not
// d.loop -- the only daemon state it touches is d.armLimiter (mutex-guarded)
// and d.cfg (immutable after construction).
//
// Sequencing (#1095 Decisions): the token-bucket take is the FIRST action,
// before any pane/repo resolution, so an over-rate request never burns a
// cross-runtime inspect, a git remote resolution, a tmux lookup, or a
// fork/exec. Pane resolution runs before repo resolution, both before the
// spawn. A context.DeadlineExceeded-wrapped resolution failure classifies as
// ReasonArmResolutionTimedOut ahead of the step-specific reason.
func (d *Daemon) hostArmSpawn(req ipc.ArmRequest) ipc.ArmResponse {
	if !d.armLimiter.take() {
		d.logArmSpawnReject(req, ReasonArmRateLimited, nil)
		return ipc.ArmResponse{OK: false, Reason: ReasonArmRateLimited}
	}

	ctx, cancel := context.WithTimeout(context.Background(), armSpawnBudget)
	defer cancel()

	session, err := armSessionForPane(ctx, req.TmuxPane)
	if err != nil {
		reason := ReasonArmPaneUnresolvable
		if errors.Is(err, context.DeadlineExceeded) {
			reason = ReasonArmResolutionTimedOut
		}
		d.logArmSpawnReject(req, reason, err)
		return ipc.ArmResponse{OK: false, Reason: reason}
	}

	dir, err := armResolveHostRepo(ctx, req.Repo)
	if err != nil {
		reason := ReasonArmHostRepoProbeFailed
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			reason = ReasonArmResolutionTimedOut
		case errors.Is(err, hostrepo.ErrNoMatch):
			reason = ReasonArmHostRepoNotFound
		case errors.Is(err, hostrepo.ErrAmbiguous):
			reason = ReasonArmHostRepoAmbiguous
		}
		d.logArmSpawnReject(req, reason, err)
		return ipc.ArmResponse{OK: false, Reason: reason}
	}

	exePath, err := os.Executable()
	if err != nil {
		d.logArmSpawnReject(req, ReasonArmSpawnFailed, err)
		return ipc.ArmResponse{OK: false, Reason: ReasonArmSpawnFailed}
	}

	args := []string{"babysit", req.PR, "--agent", req.Agent, "--interval", req.Interval.String(), "--session", session, "--dir", dir}
	cmd := exec.Command(exePath, args...)
	cmd.Dir = dir
	cmd.Env = scrubArmSpawnEnv(os.Environ())
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := armStartBabysit(cmd); err != nil {
		d.logArmSpawnReject(req, ReasonArmSpawnFailed, err)
		return ipc.ArmResponse{OK: false, Reason: ReasonArmSpawnFailed}
	}

	// Never block the ack on the child: a bounded reaper goroutine reaps it
	// and logs a non-zero exit under cfg.Verbose (#1095 Q&A #1).
	go d.reapArmSpawn(cmd)

	return ipc.ArmResponse{OK: true}
}

// reapArmSpawn waits for a successfully-started spawned babysit supervisor
// and logs a non-zero exit under cfg.Verbose -- it never blocks
// hostArmSpawn's own ack, and it touches no daemon state besides d.cfg.
func (d *Daemon) reapArmSpawn(cmd *exec.Cmd) {
	if err := cmd.Wait(); err != nil && d.cfg.Verbose {
		log.Printf("arm: spawned babysit supervisor (%v) exited: %v", cmd.Args, err)
	}
}

// logArmSpawnReject logs a hostArmSpawn reject branch under cfg.Verbose
// (watch/docs/hook-events.md rule 2). The nack reason returned to the
// client carries no host detail (arm.go's Reason* constants); err, when
// non-nil, is only ever written to this host-side log.
func (d *Daemon) logArmSpawnReject(req ipc.ArmRequest, reason string, err error) {
	if !d.cfg.Verbose {
		return
	}
	if err != nil {
		log.Printf("arm: hostArmSpawn rejected pr=%q repo=%q: %s: %v", req.PR, req.Repo, reason, err)
		return
	}
	log.Printf("arm: hostArmSpawn rejected pr=%q repo=%q: %s", req.PR, req.Repo, reason)
}
