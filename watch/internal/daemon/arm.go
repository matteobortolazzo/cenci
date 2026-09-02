package daemon

import (
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// Stable nack reason constants for a rejected babysit-arm request (#1094).
// These are plain exported Go string constants, not CENCI-* error codes
// (auto-adopted answer #6): the ticket says "stable reason", the client
// relays it verbatim, and internal/errcode is scoped to user-facing
// diagnose/launch findings, not wire payloads. Each validation category gets
// its own reason so a caller can tell a bad PR from a bad repo from a bad
// agent apart.
const (
	// ReasonInvalidPR is returned when the request's PR is not a positive
	// integer (zero, negative, non-numeric, or empty).
	ReasonInvalidPR = "PR must be a positive integer"
	// ReasonInvalidRepo is returned when the request's repo is not shaped
	// like a single "owner/repo" pair.
	ReasonInvalidRepo = "repo must be owner/repo-shaped"
	// ReasonInvalidAgent is returned when the request's agent is outside the
	// closed set parseBabysitArgs accepts (watch/babysit_cmd.go:41).
	ReasonInvalidAgent = "agent must be one of claude, codex, opencode"
	// ReasonInvalidInterval is returned when the request's Interval falls
	// outside [armIntervalMin, armIntervalMax] -- the daemon's own validation
	// gate must not forward an unbounded, container-supplied duration to
	// armSpawn and rely solely on babysit.Run's downstream clamping.
	ReasonInvalidInterval = "interval must be between 1m and 1h"
	// ReasonInvalidTmuxPane is returned when the request's TmuxPane does not
	// match tmux's own pane-id grammar (%<digits>) -- TmuxPane crosses the
	// container->host trust boundary and must be shape-validated like
	// PR/Repo/Agent before ever reaching armSpawn.
	ReasonInvalidTmuxPane = "tmux pane must match %<digits>"
	// ReasonHostRepoResolutionUnavailable is the default armSpawn seam's
	// nack reason until #1095 supplies the real host repo resolver (#1094
	// Goal: an interim state strictly better than today, because the
	// container-side supervisor it replaces never worked).
	ReasonHostRepoResolutionUnavailable = "host repo resolution unavailable"
)

// armRepoPattern matches a single "owner/repo" pair shaped like GitHub's own
// owner/repo charset: an owner segment (alphanumeric, starting with an
// alphanumeric, hyphens allowed, up to 39 chars) and a repo segment
// (alphanumeric, dot, underscore, hyphen, up to 100 chars). This is
// intentionally tighter than a bare "^[^/]+/[^/]+$" split, which would admit
// "../..", leading-dash segments, embedded spaces, and control characters.
var armRepoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)

// armTmuxPanePattern matches tmux's own pane-id grammar: a literal "%"
// followed by 1-9 digits (e.g. "%0", "%42"). TmuxPane crosses the
// container->host trust boundary unvalidated otherwise, despite reaching
// d.armSpawn and, downstream, host tmux commands.
var armTmuxPanePattern = regexp.MustCompile(`^%[0-9]{1,9}$`)

// armIntervalMin and armIntervalMax bound ArmRequest.Interval at the
// daemon's own validation gate -- babysit.Run clamps downstream, but that
// clamping doesn't belong to this gate, so the gate must not forward an
// out-of-bounds, container-supplied duration to armSpawn unchecked.
const (
	armIntervalMin = time.Minute
	armIntervalMax = time.Hour
)

// armAgents is the closed set parseBabysitArgs accepts
// (watch/babysit_cmd.go:41), duplicated here rather than imported because
// internal/daemon must not import package main.
var armAgents = map[string]bool{"claude": true, "codex": true, "opencode": true}

// defaultArmSpawn is newDaemon's default armSpawn seam: it nacks every
// otherwise-valid request until #1095 supplies the real host repo resolver
// (#1094 Goal).
func defaultArmSpawn(ipc.ArmRequest) ipc.ArmResponse {
	return ipc.ArmResponse{OK: false, Reason: ReasonHostRepoResolutionUnavailable}
}

// handleArmRequest validates a forwarded babysit-arm request and, only once
// validation passes, delegates to the injectable armSpawn seam -- fail
// closed: any validation failure nacks with a stable reason and never
// invokes armSpawn (#1094 Decisions). Every reject branch logs under
// cfg.Verbose (watch/docs/hook-events.md).
//
// This executes on the ipc.EventReceiver's connection goroutine, not
// d.loop -- it must stay pure over d.cfg/d.armSpawn (both set once at
// construction) and never touch d.sessions/d.pending/d.attention.
func (d *Daemon) handleArmRequest(req ipc.ArmRequest) ipc.ArmResponse {
	n, err := strconv.Atoi(req.PR)
	if err != nil || n <= 0 {
		d.logArmReject(req, ReasonInvalidPR)
		return ipc.ArmResponse{OK: false, Reason: ReasonInvalidPR}
	}
	if !armRepoPattern.MatchString(req.Repo) || isArmRepoDotSegment(req.Repo) {
		d.logArmReject(req, ReasonInvalidRepo)
		return ipc.ArmResponse{OK: false, Reason: ReasonInvalidRepo}
	}
	if !armAgents[req.Agent] {
		d.logArmReject(req, ReasonInvalidAgent)
		return ipc.ArmResponse{OK: false, Reason: ReasonInvalidAgent}
	}
	if req.Interval < armIntervalMin || req.Interval > armIntervalMax {
		d.logArmReject(req, ReasonInvalidInterval)
		return ipc.ArmResponse{OK: false, Reason: ReasonInvalidInterval}
	}
	if !armTmuxPanePattern.MatchString(req.TmuxPane) {
		d.logArmReject(req, ReasonInvalidTmuxPane)
		return ipc.ArmResponse{OK: false, Reason: ReasonInvalidTmuxPane}
	}
	// Forward the canonicalized decimal form, not the raw validated string --
	// strconv.Atoi accepts "+42"/"0042" as valid but the raw string would
	// reach armSpawn un-canonicalized otherwise.
	req.PR = strconv.Itoa(n)
	return d.armSpawn(req)
}

// isArmRepoDotSegment reports whether req.Repo's owner or repo segment is
// exactly "." or ".." -- armRepoPattern's character classes permit those as
// sub-strings within a longer name, so this is an explicit equality check
// after the regex match, not a regex lookaround.
func isArmRepoDotSegment(repo string) bool {
	owner, name, found := strings.Cut(repo, "/")
	if !found {
		return false
	}
	return owner == "." || owner == ".." || name == "." || name == ".."
}

// logArmReject logs a validation-reject branch under cfg.Verbose
// (watch/docs/hook-events.md rule 2).
func (d *Daemon) logArmReject(req ipc.ArmRequest, reason string) {
	if !d.cfg.Verbose {
		return
	}
	log.Printf("arm: rejected pr=%q repo=%q agent=%q: %s", req.PR, req.Repo, req.Agent, reason)
}
