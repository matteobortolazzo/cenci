package daemon

// Ticket #1095: the real armSpawn seam -- reads the forwarded pane's tmux
// session and the request's host repo directory, then starts a detached host
// `cenci babysit` with cmd.Dir and --session/--dir set to the resolved values,
// gated by a mutex-guarded token-bucket spawn-rate bound.
//
// Ticket #1135: the request is bound to its sandbox launch. The pane's
// host-written launch options (@cenci-launch-id, @cenci-container,
// @cenci-runtime) are read in the same tmux call as its session, the pane's
// launch ID must equal the request's (constant-time), and the repo is resolved
// only from the container recorded on that pane (hostrepo.ResolveContainer),
// never by scanning every container. Live supervisors are then capped per repo
// and per host, counting the babysit state directory plus this daemon's own
// in-flight arms (armReservations).

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/babysit"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/hostrepo"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/selfexe"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
)

// armBucketBurst and armBucketRefill are hostArmSpawn's token-bucket
// tunables (plan Assumptions): a normal implement run arms once, so a
// burst of 5 is invisible to legitimate use, while capping a looping
// container to roughly 2 forever-living host supervisors per minute
// (armBucketRefill's one-token-per-30s refill rate).
//
// armRepoSupervisorCap and armHostSupervisorCap bound how many supervisors
// stay alive (#1135), which the rate alone does not: a new (repo, PR) is
// refused once its repo has 8 live supervisors or the host has 24. The
// dispatch default ConcurrencyCap is 3, so 8 per repo leaves more than twice
// that for dispatched and hand-armed PRs. The per-repo key is the repo the
// daemon verified against the bound container's `origin`; the spawned child
// resolves its own repo with `gh repo view` in --dir (remotes, `gh repo
// set-default`) and records that in State.Repo, so a container that adds an
// `upstream` remote or rewrites its `origin` (the accepted gap in
// watch/docs/tmux.md) can still fill slots under another repo's name and
// reach the shared host-wide cap. The caps bound resource use; they do not
// isolate one repo or container from another.
const (
	armBucketBurst       = 5
	armBucketRefill      = 30 * time.Second
	armRepoSupervisorCap = 8
	armHostSupervisorCap = 24
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

// armNow is hostArmSpawn's injectable clock seam for the token bucket and the
// arm reservations' expiry, so tests can advance time deterministically
// without a real sleep -- mirrors the package's existing
// closeGuard/killer/reaper/armSpawn seam style.
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

// armReservationKey is one (repo, PR) an admitted arm is spawning, with the
// repo lower-cased like babysit.LiveCounts' keys.
type armReservationKey struct{ repo, pr string }

// armReservations holds the arms this daemon admitted whose supervisor is not
// yet visible in the babysit state directory (#1135): the direct child
// detaches the real supervisor and exits, so between the spawn and the child's
// first "arming" write nothing on disk counts it. Each reservation counts
// toward the caps until the state shows that (repo, PR) live or
// babysit.ArmingLivenessGrace passes -- the same bound after which an orphaned
// "arming" state stops counting. Its zero value holds no reservation. Like
// armTokenBucket it is mutex-guarded, reached only from the ipc.EventReceiver
// connection goroutine (hostArmSpawn), and never touches a d.loop-owned field.
type armReservations struct {
	mu      sync.Mutex
	pending map[armReservationKey]*armReservation
}

// armReservation is one admitted arm's reservation: when it was made.
type armReservation struct{ at time.Time }

// admit decides whether an arm for (repo, pr) may spawn under the live
// supervisor caps. It reads armLiveSupervisors under the mutex, so concurrent
// arms see each other's reservations, and prunes reservations that expired or
// whose supervisor became live (counted once, on disk). A (repo, PR) that is
// already live or reserved is never refused: its supervisor's .lock refuses the
// duplicate with the clearer "supervisor already running". Otherwise the arm
// is refused when the repo's live count plus its reservations reaches
// armRepoSupervisorCap, or the host total plus every reservation reaches
// armHostSupervisorCap; an admitted arm is reserved.
//
// It returns either a non-empty nack reason (with the count error for the
// host log, when there is one) or a release func that drops this arm's own
// reservation -- the caller runs it at once when the spawn fails, so a failed
// arm never holds quota.
func (r *armReservations) admit(repo, pr string) (release func(), reason string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	live, err := armLiveSupervisors("")
	if err != nil {
		return nil, ReasonArmSupervisorCountUnavailable, err
	}

	now := armNow()
	for k, res := range r.pending {
		if now.Sub(res.at) >= babysit.ArmingLivenessGrace || live.Live(k.repo, k.pr) {
			delete(r.pending, k)
		}
	}

	key := armReservationKey{strings.ToLower(repo), pr}
	if _, reserved := r.pending[key]; reserved || live.Live(key.repo, key.pr) {
		return func() {}, "", nil
	}

	pendingForRepo := 0
	for k := range r.pending {
		if k.repo == key.repo {
			pendingForRepo++
		}
	}
	if live.ByRepo[key.repo]+pendingForRepo >= armRepoSupervisorCap || live.Total+len(r.pending) >= armHostSupervisorCap {
		return nil, ReasonArmSupervisorCapReached, nil
	}

	if r.pending == nil {
		r.pending = map[armReservationKey]*armReservation{}
	}
	own := &armReservation{at: now}
	r.pending[key] = own
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		// Only this arm's own entry: a pruned-then-renewed key belongs to
		// another arm.
		if r.pending[key] == own {
			delete(r.pending, key)
		}
	}, "", nil
}

// armResolveHostRepo is the injectable seam over hostrepo.Resolve (#1095),
// used by the forwarded babysit stop/status requests (#1390, babysitctl.go),
// which stay identity-blind; the arm path resolves through
// armResolveContainerRepo instead (#1135). Tests substitute a scripted fake so
// the daemon suite never shells out to a real container runtime or git.
var armResolveHostRepo = hostrepo.Resolve

// armResolveContainerRepo is the injectable seam over
// hostrepo.ResolveContainer (#1135): the arm path's repo resolution from the
// one container its pane records. It mirrors armResolveHostRepo's seam style.
var armResolveContainerRepo = hostrepo.ResolveContainer

// armLiveSupervisors is the injectable seam over babysit.LiveSupervisors, the
// live-supervisor counts armReservations.admit caps against (#1135). Tests
// keep the real function over an isolated XDG_STATE_HOME, and replace it only
// to inject a count failure that does not depend on the runner's uid.
var armLiveSupervisors = babysit.LiveSupervisors

// armHostRepoFailureReason maps an armResolveContainerRepo or
// armResolveHostRepo error to its nack reason: a timeout ahead of the
// step-specific classes, and the probe-failed reason for everything
// unrecognized. Shared by the arm spawn and the babysit stop/status requests
// (#1390); ErrAmbiguous comes only from the stop/status scan, never from
// ResolveContainer.
func armHostRepoFailureReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ReasonArmResolutionTimedOut
	case errors.Is(err, hostrepo.ErrNoMatch):
		return ReasonArmHostRepoNotFound
	case errors.Is(err, hostrepo.ErrAmbiguous):
		return ReasonArmHostRepoAmbiguous
	default:
		return ReasonArmHostRepoProbeFailed
	}
}

// armPaneInfo is one pane-identity read: the pane's session and the three
// launch options the sandbox launcher alone writes on its own pane (#1252).
// Any field may be empty (an untagged pane has no launch options).
type armPaneInfo struct {
	session   string
	launchID  string
	container string
	runtime   string
}

// armPaneIdentityFormat is the one `display-message -p` format the pane read
// uses: the session and the three launch options, tab-separated.
const armPaneIdentityFormat = "#{session_name}\t#{" + sandbox.PaneOptionLaunchID + "}\t#{" + sandbox.PaneOptionContainer + "}\t#{" + sandbox.PaneOptionRuntime + "}"

// errArmPaneGone is a pane read that came back empty: tmux's answer for a pane
// that does not exist (watch/docs/tmux.md). errArmPaneMalformed is an answer
// that is not exactly four tab-separated fields. Neither carries any part of
// the answer, so no option value (the pane's launch ID included) can reach a
// log through them.
var (
	errArmPaneGone      = errors.New("tmux pane not found")
	errArmPaneMalformed = errors.New("tmux pane identity is not four tab-separated fields")
)

// armPaneIdentity is the injectable seam over the bounded pane-identity read
// (#1135, replacing #1095's session-only armSessionForPane) on the request's
// own socket, or the daemon's ambient server when that is empty (#1431).
// Distinct from internal/babysit's currentTmuxSession, which resolves the
// *current* process's own $TMUX_PANE -- this seam reads an arbitrary
// caller-supplied pane ID crossing the container->host trust boundary.
var armPaneIdentity = defaultArmPaneIdentity

// armStartBabysit is the injectable seam over the detached `cenci babysit`
// child's process start, mirroring internal/babysit's startSupervisor seam
// shape exactly: it receives the fully-built *exec.Cmd (argv, Dir, Env,
// SysProcAttr all already set by hostArmSpawn) so tests can capture and
// assert on it without a real process ever starting.
var armStartBabysit = defaultArmStartBabysit

// armResolveSelfExe is the injectable seam over selfexe.Resolve (#1372): the
// binary hostArmSpawn starts as the detached `cenci babysit` child. It is not
// os.Executable, which names a deleted path once a plugin refresh removes the
// daemon's own cache directory.
var armResolveSelfExe = selfexe.Resolve

// defaultArmPaneIdentity is armPaneIdentity's production implementation: one
// bounded `tmux display-message -t <pane> -p <armPaneIdentityFormat>`, parsed
// by parseArmPaneIdentity.
//
// A pane id like %5 repeats across tmux servers, so a non-empty socket (the
// arm request's own, already validated by handleArmRequest, #1431) reads the
// pane on that server through the hardened tmux.RunSocket -- `-S <socket>`,
// allowlisted env, inside ctx (armSpawnBudget). An empty socket keeps the bare
// execArmTmux call. Neither path trims stdout: an untagged pane's answer ends
// in empty fields that whitespace trimming would drop.
func defaultArmPaneIdentity(ctx context.Context, socket, pane string) (armPaneInfo, error) {
	args := []string{"display-message", "-t", pane, "-p", armPaneIdentityFormat}
	var stdout, stderr string
	var err error
	if socket == "" {
		stdout, stderr, err = execArmTmux(ctx, args...)
	} else {
		var outBuf, errBuf bytes.Buffer
		err = tmux.RunSocket(ctx, socket, &outBuf, &errBuf, args...)
		stdout, stderr = outBuf.String(), strings.TrimSpace(errBuf.String())
	}
	if err != nil {
		return armPaneInfo{}, fmt.Errorf("tmux display-message: %s: %w", stderr, err)
	}
	return parseArmPaneIdentity(stdout)
}

// parseArmPaneIdentity parses one pane-identity answer: exactly one trailing
// newline is stripped (never other whitespace), an empty answer is
// errArmPaneGone (tmux exits 0 with empty stdout for a pane that does not
// exist, watch/docs/tmux.md), and anything but exactly four tab-separated
// fields is errArmPaneMalformed -- fail closed, never shifted into the wrong
// field.
func parseArmPaneIdentity(out string) (armPaneInfo, error) {
	out = strings.TrimSuffix(out, "\n")
	if out == "" {
		return armPaneInfo{}, errArmPaneGone
	}
	fields := strings.Split(out, "\t")
	if len(fields) != 4 {
		return armPaneInfo{}, errArmPaneMalformed
	}
	return armPaneInfo{session: fields[0], launchID: fields[1], container: fields[2], runtime: fields[3]}, nil
}

// execArmTmux runs `tmux <args...>` bounded by ctx, with cmd.WaitDelay
// bounding a lingering grandchild, separate bounded stdout/stderr buffers,
// and cmd.Stdin = nil -- mirrors internal/babysit/tmux.go's execTmux
// bounded-subprocess convention (watch/AGENTS.md rule #5). stdout is returned
// as tmux wrote it (the pane-identity parser needs its empty trailing
// fields); stderr is trimmed.
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
	return outBuf.String(), strings.TrimSpace(errBuf.String()), runErr
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

// armLaunchMatches reports whether the pane's recorded launch ID is well formed
// and equal to the request's, compared in constant time (#1135).
func armLaunchMatches(pane armPaneInfo, requestID string) bool {
	return sandbox.ValidLaunchID(pane.launchID) && subtle.ConstantTimeCompare([]byte(pane.launchID), []byte(requestID)) == 1
}

// hostArmSpawn is the real armSpawn seam Ticket #1095 supplies, installed
// by daemon.Run as d.armSpawn = d.hostArmSpawn. It executes on the
// ipc.EventReceiver's connection goroutine, not d.loop -- the only daemon
// state it touches is d.armLimiter and d.armReservations (both mutex-guarded)
// and d.cfg (immutable after construction).
//
// Sequencing (#1095 Decisions, #1135):
//  1. The token-bucket take is the FIRST action, so an over-rate request never
//     burns a tmux read, an inspect, a git call or a fork/exec.
//  2. One pane-identity read (session plus launch options) on the request's
//     socket; a gone pane or empty session is ReasonArmPaneUnresolvable.
//  3. The pane's launch ID must be well formed and equal req.LaunchID
//     (constant-time), and its container and runtime must pass the sandbox
//     validators; every identity failure is the one ReasonArmLaunchUnverified,
//     so a caller learns nothing about which check failed.
//  4. The repo is resolved from that container alone (ResolveContainer).
//  5. The live-supervisor caps admit and reserve the arm.
//  6. selfexe resolution and the spawn; a failure releases the reservation.
//
// A context.DeadlineExceeded-wrapped resolution failure classifies as
// ReasonArmResolutionTimedOut ahead of the step-specific reason. Neither the
// request's nor the pane's launch ID ever reaches a reply, a log line, the
// child's argv or its environment.
func (d *Daemon) hostArmSpawn(req ipc.ArmRequest) ipc.ArmResponse {
	if !d.armLimiter.take() {
		d.logArmSpawnReject(req, ReasonArmRateLimited, nil)
		return ipc.ArmResponse{OK: false, Reason: ReasonArmRateLimited}
	}

	ctx, cancel := context.WithTimeout(context.Background(), armSpawnBudget)
	defer cancel()

	pane, err := armPaneIdentity(ctx, req.TmuxSocket, req.TmuxPane)
	if err == nil && pane.session == "" {
		err = errArmPaneGone
	}
	if err != nil {
		reason := ReasonArmPaneUnresolvable
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			reason = ReasonArmResolutionTimedOut
		case errors.Is(err, errArmPaneMalformed):
			reason = ReasonArmLaunchUnverified
		}
		d.logArmSpawnReject(req, reason, err)
		return ipc.ArmResponse{OK: false, Reason: reason}
	}

	if !armLaunchMatches(pane, req.LaunchID) || !sandbox.IsSandboxContainerName(pane.container) || !sandbox.ValidLaunchRuntime(pane.runtime) {
		d.logArmSpawnReject(req, ReasonArmLaunchUnverified, nil)
		return ipc.ArmResponse{OK: false, Reason: ReasonArmLaunchUnverified}
	}

	dir, err := armResolveContainerRepo(ctx, pane.runtime, pane.container, req.Repo)
	if err != nil {
		reason := armHostRepoFailureReason(err)
		d.logArmSpawnReject(req, reason, err)
		return ipc.ArmResponse{OK: false, Reason: reason}
	}

	release, reason, err := d.armReservations.admit(req.Repo, req.PR)
	if reason != "" {
		if reason == ReasonArmSupervisorCountUnavailable {
			d.logArmSupervisorCountFailure(req, err)
		} else {
			d.logArmSpawnReject(req, reason, err)
		}
		return ipc.ArmResponse{OK: false, Reason: reason}
	}

	exePath, err := armResolveSelfExe()
	if err != nil {
		release()
		d.logArmSpawnFailure(req, "", err)
		return ipc.ArmResponse{OK: false, Reason: ReasonArmSpawnFailed}
	}

	args := []string{"babysit", req.PR, "--agent", req.Agent, "--interval", req.Interval.String(), "--session", pane.session, "--dir", dir}
	// The supervisor records the server its session was found on (#1431); with
	// no socket the argv is today's.
	if req.TmuxSocket != "" {
		args = append(args, "--tmux-socket", req.TmuxSocket)
	}
	cmd := exec.Command(exePath, args...)
	cmd.Dir = dir
	cmd.Env = scrubArmSpawnEnv(os.Environ())
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := armStartBabysit(cmd); err != nil {
		release()
		d.logArmSpawnFailure(req, exePath, err)
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

// logArmSpawnFailure writes the always-on host log line for a spawn failure
// (#1372): a selfexe resolution failure (exePath empty) or a start failure of
// the resolved exePath. Unlike logArmSpawnReject it ignores cfg.Verbose, so a
// normal daemon leaves a trace of why every arm request is nacked; it is
// bounded to one line per admitted request by armTokenBucket. The nack reason
// stays the bare ReasonArmSpawnFailed -- path and error only reach this log.
// Like every arm log line it names the PR and repo, never the launch ID.
func (d *Daemon) logArmSpawnFailure(req ipc.ArmRequest, exePath string, err error) {
	const hint = "restart the daemon with `systemctl --user restart cenci-watch.service` or `cenci daemon restart`"
	if exePath != "" {
		log.Printf("arm: failed to spawn host babysit supervisor pr=%q repo=%q exe=%q: %v (%s)", req.PR, req.Repo, exePath, err, hint)
		return
	}
	log.Printf("arm: failed to spawn host babysit supervisor pr=%q repo=%q: %v (%s)", req.PR, req.Repo, err, hint)
}

// logArmSupervisorCountFailure writes the always-on host log line for a
// live-supervisor count failure (ReasonArmSupervisorCountUnavailable). It is a
// host fault -- an unreadable or misplaced babysit state directory -- that nacks
// every arm until fixed and that no container can trigger, so like
// logArmSpawnFailure it ignores cfg.Verbose and is bounded to one line per
// admitted request by armTokenBucket. The nack reason stays the bare constant;
// the wrapped state-directory error only reaches this log. Like every arm log
// line it names the PR and repo, never the launch ID.
func (d *Daemon) logArmSupervisorCountFailure(req ipc.ArmRequest, err error) {
	log.Printf("arm: cannot count live babysit supervisors pr=%q repo=%q: %v", req.PR, req.Repo, err)
}

// logArmSpawnReject logs a hostArmSpawn reject branch under cfg.Verbose
// (watch/docs/hook-events.md rule 2). The nack reason returned to the
// client carries no host detail (arm.go's Reason* constants); err, when
// non-nil, is only ever written to this host-side log, and no error on the arm
// path carries a launch ID (the pane-read sentinels carry no option value).
// Spawn failures (resolver and start errors) and live-supervisor count
// failures are not logged here: logArmSpawnFailure and
// logArmSupervisorCountFailure log them always, being host faults rather than
// container-triggerable rejections.
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
