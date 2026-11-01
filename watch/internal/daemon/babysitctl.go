package daemon

// Ticket #1390: the daemon half of the forwarded `cenci babysit stop` and
// `cenci babysit status`. A request from a sandbox is validated like a
// babysit-arm request, then must match a running sandbox's host repo before
// the repo-keyed babysit.StopRepo/StatusRepo seam runs. Because a container
// can thereby cause a host process to receive SIGTERM, every reply carries
// only fixed reason text -- never a host path, a PID or command output.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/babysit"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/hostrepo"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// Nack reasons specific to the stop/status control requests (#1390). Like the
// arm reasons in arm.go they are plain exported constants (not CENCI-* codes)
// that a client relays verbatim, so none may carry host detail. The two
// ...Fmt reasons embed only the PR number the caller itself supplied.
const (
	// ReasonBabysitControlUnavailable is the inert default seam's nack:
	// newDaemon leaves the stop/status seams unwired so newDaemon-based tests
	// never read or signal real host state.
	ReasonBabysitControlUnavailable = "babysit control unavailable"
	// ReasonBabysitNoSupervisorFmt is the nack for a PR with no host state
	// file (P20); the client prints it after "not stopped: ".
	ReasonBabysitNoSupervisorFmt = "no supervisor found for PR #%d"
	// ReasonBabysitStopUnverified is the nack when the state records a PID
	// that is not the recorded supervisor; nothing was signalled.
	ReasonBabysitStopUnverified = "the recorded supervisor process could not be verified"
	// ReasonBabysitStopFailed is the nack for any other stop failure.
	ReasonBabysitStopFailed = "failed to stop the host supervisor"
	// ReasonBabysitStateUnreadableFmt is the nack for an unreadable or
	// corrupt state file; unlike the host error it never names the path.
	ReasonBabysitStateUnreadableFmt = "babysit state for PR #%d could not be read"
	// ReasonBabysitStatusFailed is the nack for any other status failure.
	ReasonBabysitStatusFailed = "failed to read the host supervisor status"
)

// errBabysitControlUnavailable is what the inert default seams return.
var errBabysitControlUnavailable = errors.New("babysit control seam not installed")

func defaultBabysitStop(string, int) error { return errBabysitControlUnavailable }

func defaultBabysitStatus(string, int) (ipc.BabysitStatus, error) {
	return ipc.BabysitStatus{}, errBabysitControlUnavailable
}

// hostBabysitStop is the real stop seam: the host's default state directory.
func hostBabysitStop(repo string, pr int) error {
	return babysit.StopRepo(repo, strconv.Itoa(pr), "")
}

// hostBabysitStatus is the real status seam.
func hostBabysitStatus(repo string, pr int) (ipc.BabysitStatus, error) {
	return babysit.StatusRepo(repo, strconv.Itoa(pr), "")
}

// handleBabysitStop validates a forwarded stop and, only once validation and
// the running-sandbox match pass, delegates to the babysitStop seam. It runs
// on the ipc.EventReceiver's connection goroutine, not d.loop: it reads only
// d.cfg, d.ctlLimiter (mutex-guarded) and the seam fields, all set once at
// construction, and never touches d.sessions/d.pending/d.attention.
func (d *Daemon) handleBabysitStop(req ipc.BabysitStopRequest) ipc.BabysitStopResponse {
	n, reason := d.admitBabysitControl("stop", req.PR, req.Repo)
	if reason != "" {
		return ipc.BabysitStopResponse{OK: false, Reason: reason}
	}
	if err := d.babysitStop(req.Repo, n); err != nil {
		reason = stopFailureReason(err, n)
		d.logBabysitControlReject("stop", req.PR, req.Repo, reason, err)
		return ipc.BabysitStopResponse{OK: false, Reason: reason}
	}
	return ipc.BabysitStopResponse{OK: true}
}

// handleBabysitStatus is handleBabysitStop's counterpart for status.
func (d *Daemon) handleBabysitStatus(req ipc.BabysitStatusRequest) ipc.BabysitStatusResponse {
	n, reason := d.admitBabysitControl("status", req.PR, req.Repo)
	if reason != "" {
		return ipc.BabysitStatusResponse{OK: false, Reason: reason}
	}
	status, err := d.babysitStatus(req.Repo, n)
	if err != nil {
		reason = statusFailureReason(err, n)
		d.logBabysitControlReject("status", req.PR, req.Repo, reason, err)
		return ipc.BabysitStatusResponse{OK: false, Reason: reason}
	}
	return ipc.BabysitStatusResponse{OK: true, Status: &status}
}

// admitBabysitControl runs the checks both control requests share, in this
// order: PR, repo shape (and dot segments), a control-bucket token, then the
// running-sandbox match. The token comes before the match so an over-rate
// request never pays for a container inspect. It returns the canonical PR
// number, or a non-empty nack reason (already logged).
//
// hostrepo.ErrAmbiguous is accepted -- unlike arm, which needs one directory
// to spawn in, the state path depends only on repo and PR -- while ErrNoMatch
// and every probe failure nack. The resolved directory is discarded: it is a
// host path and must never reach a reply.
func (d *Daemon) admitBabysitControl(kind, pr, repo string) (int, string) {
	n, err := strconv.Atoi(pr)
	if err != nil || n <= 0 {
		d.logBabysitControlReject(kind, pr, repo, ReasonInvalidPR, nil)
		return 0, ReasonInvalidPR
	}
	if !armRepoPattern.MatchString(repo) || isArmRepoDotSegment(repo) {
		d.logBabysitControlReject(kind, pr, repo, ReasonInvalidRepo, nil)
		return 0, ReasonInvalidRepo
	}
	if !d.ctlLimiter.take() {
		d.logBabysitControlReject(kind, pr, repo, ReasonArmRateLimited, nil)
		return 0, ReasonArmRateLimited
	}

	ctx, cancel := context.WithTimeout(context.Background(), armSpawnBudget)
	defer cancel()
	if _, err := armResolveHostRepo(ctx, repo); err != nil && !errors.Is(err, hostrepo.ErrAmbiguous) {
		reason := armHostRepoFailureReason(err)
		d.logBabysitControlReject(kind, pr, repo, reason, err)
		return 0, reason
	}
	return n, ""
}

// stopFailureReason maps a StopRepo error to its fixed reason. The error text
// names paths and PIDs and is only ever logged host-side.
func stopFailureReason(err error, pr int) string {
	switch {
	case errors.Is(err, babysit.ErrNoSupervisor):
		return fmt.Sprintf(ReasonBabysitNoSupervisorFmt, pr)
	case errors.Is(err, babysit.ErrSupervisorUnverified):
		return ReasonBabysitStopUnverified
	case errors.Is(err, babysit.ErrStateUnreadable):
		return fmt.Sprintf(ReasonBabysitStateUnreadableFmt, pr)
	case errors.Is(err, errBabysitControlUnavailable):
		return ReasonBabysitControlUnavailable
	default:
		return ReasonBabysitStopFailed
	}
}

// statusFailureReason maps a StatusRepo error to its fixed reason.
func statusFailureReason(err error, pr int) string {
	switch {
	case errors.Is(err, babysit.ErrNoSupervisor):
		return fmt.Sprintf(ReasonBabysitNoSupervisorFmt, pr)
	case errors.Is(err, babysit.ErrStateUnreadable):
		return fmt.Sprintf(ReasonBabysitStateUnreadableFmt, pr)
	case errors.Is(err, errBabysitControlUnavailable):
		return ReasonBabysitControlUnavailable
	default:
		return ReasonBabysitStatusFailed
	}
}

// logBabysitControlReject logs a rejected or failed control request under
// cfg.Verbose (watch/docs/hook-events.md rule 2). err, when non-nil, is the
// host-side detail the reply never carries.
func (d *Daemon) logBabysitControlReject(kind, pr, repo, reason string, err error) {
	if !d.cfg.Verbose {
		return
	}
	if err != nil {
		log.Printf("babysit-%s: rejected pr=%q repo=%q: %s: %v", kind, pr, repo, reason, err)
		return
	}
	log.Printf("babysit-%s: rejected pr=%q repo=%q: %s", kind, pr, repo, reason)
}
