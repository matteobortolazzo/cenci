package daemon

// Ticket #1094: daemon-side validation of a forwarded babysit-arm request,
// behind the injectable armSpawn seam (mirroring closeGuard/killer/reaper).
// d.armSpawn and d.handleArmRequest don't exist yet; this file is expected
// to fail to compile until Phase 4 adds arm.go.

import (
	"bytes"
	"log"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

// captureLog redirects the standard logger's output into a buffer for the
// duration of the test and restores the original output in t.Cleanup,
// mirroring pkg/watch/socket_test.go's helper of the same name.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	return &buf
}

// recordingArmSpawn is a call-recording fake for the injectable armSpawn
// seam (#1094), mirroring the package's existing fakeKiller/countingGuard
// pattern: it records every validated request handed to it and returns a
// scripted response, so tests can assert both "never invoked on a rejected
// request" and "ack/nack passthrough" without any real host resolution.
type recordingArmSpawn struct {
	calls []ipc.ArmRequest
	resp  ipc.ArmResponse
}

func (s *recordingArmSpawn) spawn(req ipc.ArmRequest) ipc.ArmResponse {
	s.calls = append(s.calls, req)
	return s.resp
}

func validArmRequest() ipc.ArmRequest {
	return ipc.ArmRequest{PR: "42", Repo: "o/r", Agent: "claude", Interval: 15 * time.Minute, TmuxPane: "%3"}
}

// -- validation: PR must be a positive integer -------------------------------

// TestHandleArmRequest_InvalidPRRejectedWithoutInvokingSpawn covers the
// ticket's fail-closed decision: a PR that isn't a positive integer (zero,
// negative, non-numeric, empty) must nack without ever invoking the spawn
// seam, and every such malformed PR shares one stable reason.
func TestHandleArmRequest_InvalidPRRejectedWithoutInvokingSpawn(t *testing.T) {
	spawn := &recordingArmSpawn{resp: ipc.ArmResponse{OK: true}}
	d := newTestDaemon(&tmuxtest.MockClient{})
	d.armSpawn = spawn.spawn

	var reasons []string
	for _, pr := range []string{"0", "-5", "abc", ""} {
		req := validArmRequest()
		req.PR = pr
		resp := d.handleArmRequest(req)
		if resp.OK {
			t.Errorf("PR=%q: resp.OK = true, want a nack for a non-positive-integer PR", pr)
		}
		if resp.Reason == "" {
			t.Errorf("PR=%q: resp.Reason is empty, want a stable nack reason", pr)
		}
		reasons = append(reasons, resp.Reason)
	}
	for i := 1; i < len(reasons); i++ {
		if reasons[i] != reasons[0] {
			t.Errorf("invalid-PR reasons = %v, want every non-positive-integer PR to share one stable reason", reasons)
		}
	}
	if len(spawn.calls) != 0 {
		t.Fatalf("armSpawn calls = %d, want 0 -- validation must fail closed before invoking the spawn seam", len(spawn.calls))
	}
}

// -- validation: repo must be owner/repo-shaped ------------------------------

// TestHandleArmRequest_InvalidRepoRejectedWithoutInvokingSpawn covers the
// repo-shape half of the validation gate: anything that isn't a single
// "owner/repo" pair shaped like GitHub's own owner/repo charset must nack
// without invoking the spawn seam, sharing one stable reason distinct from
// the PR-validation reason. Includes regression cases for the tightened
// charset (#1094 security review): "../.." and a leading-dash owner segment
// must not slip through a merely "one non-slash segment, slash, one more
// non-slash segment" shape check, and an embedded space must not either.
func TestHandleArmRequest_InvalidRepoRejectedWithoutInvokingSpawn(t *testing.T) {
	spawn := &recordingArmSpawn{resp: ipc.ArmResponse{OK: true}}
	d := newTestDaemon(&tmuxtest.MockClient{})
	d.armSpawn = spawn.spawn

	var reasons []string
	for _, repo := range []string{"norepoowner", "o/r/extra", "", "/r", "o/", "../..", "-x/y", "a b/c"} {
		req := validArmRequest()
		req.Repo = repo
		resp := d.handleArmRequest(req)
		if resp.OK {
			t.Errorf("Repo=%q: resp.OK = true, want a nack for a non-owner/repo-shaped repo", repo)
		}
		if resp.Reason == "" {
			t.Errorf("Repo=%q: resp.Reason is empty, want a stable nack reason", repo)
		}
		reasons = append(reasons, resp.Reason)
	}
	for i := 1; i < len(reasons); i++ {
		if reasons[i] != reasons[0] {
			t.Errorf("invalid-repo reasons = %v, want every malformed repo to share one stable reason", reasons)
		}
	}
	if len(spawn.calls) != 0 {
		t.Fatalf("armSpawn calls = %d, want 0 -- validation must fail closed before invoking the spawn seam", len(spawn.calls))
	}
}

// -- validation: agent must be in the closed set -----------------------------

// TestHandleArmRequest_InvalidAgentRejectedWithoutInvokingSpawn covers
// auto-adopted answer #5: an agent outside parseBabysitArgs' closed set
// (claude, codex, opencode) must nack without invoking the spawn seam.
func TestHandleArmRequest_InvalidAgentRejectedWithoutInvokingSpawn(t *testing.T) {
	spawn := &recordingArmSpawn{resp: ipc.ArmResponse{OK: true}}
	d := newTestDaemon(&tmuxtest.MockClient{})
	d.armSpawn = spawn.spawn

	req := validArmRequest()
	req.Agent = "not-a-real-agent"
	resp := d.handleArmRequest(req)
	if resp.OK {
		t.Error("resp.OK = true, want a nack for an agent outside the closed set")
	}
	if resp.Reason == "" {
		t.Error("resp.Reason is empty, want a stable nack reason")
	}
	if len(spawn.calls) != 0 {
		t.Fatalf("armSpawn calls = %d, want 0 -- validation must fail closed before invoking the spawn seam", len(spawn.calls))
	}
}

// -- validation: interval must be within [1m, 1h] ----------------------------

// TestHandleArmRequest_IntervalOutOfBoundsRejectedWithoutInvokingSpawn covers
// the daemon's own bound check on the container-supplied Interval (#1094
// security review): the daemon's validation gate must not forward an
// out-of-bounds duration to armSpawn and rely solely on babysit.Run's
// downstream clamping. Zero, negative, and just-outside-each-bound values
// must all nack without invoking the spawn seam.
func TestHandleArmRequest_IntervalOutOfBoundsRejectedWithoutInvokingSpawn(t *testing.T) {
	spawn := &recordingArmSpawn{resp: ipc.ArmResponse{OK: true}}
	d := newTestDaemon(&tmuxtest.MockClient{})
	d.armSpawn = spawn.spawn

	var reasons []string
	for _, interval := range []time.Duration{
		0,
		-time.Minute,
		time.Minute - time.Nanosecond, // just under the 1m lower bound
		time.Hour + time.Nanosecond,   // just over the 1h upper bound
	} {
		req := validArmRequest()
		req.Interval = interval
		resp := d.handleArmRequest(req)
		if resp.OK {
			t.Errorf("Interval=%v: resp.OK = true, want a nack for an out-of-bounds interval", interval)
		}
		if resp.Reason == "" {
			t.Errorf("Interval=%v: resp.Reason is empty, want a stable nack reason", interval)
		}
		reasons = append(reasons, resp.Reason)
	}
	for i := 1; i < len(reasons); i++ {
		if reasons[i] != reasons[0] {
			t.Errorf("invalid-interval reasons = %v, want every out-of-bounds interval to share one stable reason", reasons)
		}
	}
	if len(spawn.calls) != 0 {
		t.Fatalf("armSpawn calls = %d, want 0 -- validation must fail closed before invoking the spawn seam", len(spawn.calls))
	}
}

// TestHandleArmRequest_IntervalAtBoundsAccepted covers the inclusive edges of
// the [1m, 1h] interval bound: exactly 1 minute and exactly 1 hour must both
// pass validation and reach the spawn seam.
func TestHandleArmRequest_IntervalAtBoundsAccepted(t *testing.T) {
	for _, interval := range []time.Duration{time.Minute, time.Hour} {
		spawn := &recordingArmSpawn{resp: ipc.ArmResponse{OK: true}}
		d := newTestDaemon(&tmuxtest.MockClient{})
		d.armSpawn = spawn.spawn

		req := validArmRequest()
		req.Interval = interval
		resp := d.handleArmRequest(req)
		if !resp.OK {
			t.Errorf("Interval=%v: resp.OK = false (reason %q), want an ack -- bounds are inclusive", interval, resp.Reason)
		}
		if len(spawn.calls) != 1 {
			t.Errorf("Interval=%v: armSpawn calls = %d, want exactly 1", interval, len(spawn.calls))
		}
	}
}

// -- validation: tmux pane must match tmux's pane-id grammar -----------------

// TestHandleArmRequest_InvalidTmuxPaneRejectedWithoutInvokingSpawn covers the
// container->host trust-boundary crossing TmuxPane makes (#1094 security
// review): a pane ID that doesn't match tmux's own "%<digits>" grammar must
// nack without invoking the spawn seam -- a missing "%", a
// "session:window.pane" form, and an empty string must all be rejected.
func TestHandleArmRequest_InvalidTmuxPaneRejectedWithoutInvokingSpawn(t *testing.T) {
	spawn := &recordingArmSpawn{resp: ipc.ArmResponse{OK: true}}
	d := newTestDaemon(&tmuxtest.MockClient{})
	d.armSpawn = spawn.spawn

	var reasons []string
	for _, pane := range []string{"3", "session:window.pane", ""} {
		req := validArmRequest()
		req.TmuxPane = pane
		resp := d.handleArmRequest(req)
		if resp.OK {
			t.Errorf("TmuxPane=%q: resp.OK = true, want a nack for a non-pane-id-shaped tmux pane", pane)
		}
		if resp.Reason == "" {
			t.Errorf("TmuxPane=%q: resp.Reason is empty, want a stable nack reason", pane)
		}
		reasons = append(reasons, resp.Reason)
	}
	for i := 1; i < len(reasons); i++ {
		if reasons[i] != reasons[0] {
			t.Errorf("invalid-tmux-pane reasons = %v, want every malformed pane to share one stable reason", reasons)
		}
	}
	if len(spawn.calls) != 0 {
		t.Fatalf("armSpawn calls = %d, want 0 -- validation must fail closed before invoking the spawn seam", len(spawn.calls))
	}
}

// TestHandleArmRequest_ValidTmuxPaneAccepted covers the accept side of the
// tmux pane-id grammar: single- and multi-digit pane IDs must both pass
// validation and reach the spawn seam.
func TestHandleArmRequest_ValidTmuxPaneAccepted(t *testing.T) {
	for _, pane := range []string{"%0", "%12"} {
		spawn := &recordingArmSpawn{resp: ipc.ArmResponse{OK: true}}
		d := newTestDaemon(&tmuxtest.MockClient{})
		d.armSpawn = spawn.spawn

		req := validArmRequest()
		req.TmuxPane = pane
		resp := d.handleArmRequest(req)
		if !resp.OK {
			t.Errorf("TmuxPane=%q: resp.OK = false (reason %q), want an ack", pane, resp.Reason)
		}
		if len(spawn.calls) != 1 {
			t.Errorf("TmuxPane=%q: armSpawn calls = %d, want exactly 1", pane, len(spawn.calls))
		}
	}
}

// -- validation: PR is canonicalized before forwarding ------------------------

// TestHandleArmRequest_PRCanonicalizedBeforeForwarding covers the
// canonicalization fix (#1094 security review): strconv.Atoi validates
// "+42"/"0042" as positive integers, but the seam must receive the
// canonicalized decimal form, not the raw un-canonicalized input string.
func TestHandleArmRequest_PRCanonicalizedBeforeForwarding(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{raw: "+42", want: "42"},
		{raw: "0042", want: "42"},
	} {
		spawn := &recordingArmSpawn{resp: ipc.ArmResponse{OK: true}}
		d := newTestDaemon(&tmuxtest.MockClient{})
		d.armSpawn = spawn.spawn

		req := validArmRequest()
		req.PR = tc.raw
		resp := d.handleArmRequest(req)
		if !resp.OK {
			t.Fatalf("PR=%q: resp.OK = false (reason %q), want an ack", tc.raw, resp.Reason)
		}
		if len(spawn.calls) != 1 {
			t.Fatalf("PR=%q: armSpawn calls = %d, want exactly 1", tc.raw, len(spawn.calls))
		}
		if spawn.calls[0].PR != tc.want {
			t.Errorf("PR=%q: armSpawn called with PR=%q, want the canonicalized form %q", tc.raw, spawn.calls[0].PR, tc.want)
		}
	}
}

// TestHandleArmRequest_ValidationReasonsAreDistinctPerCategory pins the "own
// stable nack reason" per validation category (#1094 auto-adopted answer
// #5): a caller must be able to tell a bad PR from a bad repo from a bad
// agent from a bad interval from a bad tmux pane apart, never one
// interchangeable placeholder string for all of them (watch/docs/error-handling.md #446).
func TestHandleArmRequest_ValidationReasonsAreDistinctPerCategory(t *testing.T) {
	spawn := &recordingArmSpawn{resp: ipc.ArmResponse{OK: true}}
	d := newTestDaemon(&tmuxtest.MockClient{})
	d.armSpawn = spawn.spawn

	badPR := validArmRequest()
	badPR.PR = "abc"
	badRepo := validArmRequest()
	badRepo.Repo = "not-owner-repo-shaped"
	badAgent := validArmRequest()
	badAgent.Agent = "not-a-real-agent"
	badInterval := validArmRequest()
	badInterval.Interval = 0
	badTmuxPane := validArmRequest()
	badTmuxPane.TmuxPane = "not-a-pane-id"

	prReason := d.handleArmRequest(badPR).Reason
	repoReason := d.handleArmRequest(badRepo).Reason
	agentReason := d.handleArmRequest(badAgent).Reason
	intervalReason := d.handleArmRequest(badInterval).Reason
	tmuxPaneReason := d.handleArmRequest(badTmuxPane).Reason

	reasons := map[string]string{
		"pr":        prReason,
		"repo":      repoReason,
		"agent":     agentReason,
		"interval":  intervalReason,
		"tmux_pane": tmuxPaneReason,
	}
	seen := make(map[string]string, len(reasons))
	for category, reason := range reasons {
		if reason == "" {
			t.Fatalf("reasons = %v, want %s non-empty", reasons, category)
		}
		if other, ok := seen[reason]; ok {
			t.Errorf("reasons = %v, want %s and %s distinct, both got %q", reasons, category, other, reason)
		}
		seen[reason] = category
	}
}

// -- ack/nack passthrough -----------------------------------------------------

// TestHandleArmRequest_ValidRequestInvokesSpawnAndRelaysVerbatim covers the
// synchronous-handler seam's whole point: a validated request reaches
// armSpawn exactly once, and whatever ArmResponse it returns (ack or nack,
// including a reason the daemon package never itself defines) is relayed
// back unchanged -- the "relay, never re-derive" rule.
func TestHandleArmRequest_ValidRequestInvokesSpawnAndRelaysVerbatim(t *testing.T) {
	req := validArmRequest()
	for _, want := range []ipc.ArmResponse{
		{OK: true},
		{OK: false, Reason: "a reason ticket 2 will define, not this daemon package"},
	} {
		spawn := &recordingArmSpawn{resp: want}
		d := newTestDaemon(&tmuxtest.MockClient{})
		d.armSpawn = spawn.spawn

		got := d.handleArmRequest(req)
		if got != want {
			t.Errorf("handleArmRequest = %+v, want the spawn seam's response %+v relayed verbatim", got, want)
		}
		if len(spawn.calls) != 1 {
			t.Fatalf("armSpawn calls = %d, want exactly 1 for a valid request", len(spawn.calls))
		}
		if spawn.calls[0] != req {
			t.Errorf("armSpawn called with %+v, want the validated request %+v", spawn.calls[0], req)
		}
	}
}

// TestHandleArmRequest_DefaultSpawnNacksHostRepoResolutionUnavailable pins
// the interim-honesty contract (#1094 Goal): until #1095 supplies the real
// resolver, newDaemon's default armSpawn nacks every otherwise-valid request
// with this exact, stable reason.
func TestHandleArmRequest_DefaultSpawnNacksHostRepoResolutionUnavailable(t *testing.T) {
	d := newTestDaemon(&tmuxtest.MockClient{})
	// newTestDaemon does not override armSpawn -- exercise newDaemon's real
	// default seam.
	resp := d.handleArmRequest(validArmRequest())
	if resp.OK {
		t.Fatal("resp.OK = true, want the default seam to nack until #1095 lands")
	}
	if resp.Reason != "host repo resolution unavailable" {
		t.Errorf("resp.Reason = %q, want %q", resp.Reason, "host repo resolution unavailable")
	}
}

// -- verbose-gated reject logging ---------------------------------------------

// TestHandleArmRequest_RejectLogsUnderVerbose covers the reject-branch
// logging rule (watch/docs/hook-events.md): a validation-reject branch must
// log under cfg.Verbose, and must stay silent when it's off.
func TestHandleArmRequest_RejectLogsUnderVerbose(t *testing.T) {
	d := newTestDaemon(&tmuxtest.MockClient{})
	req := validArmRequest()
	req.PR = "not-a-number"

	buf := captureLog(t)
	d.cfg.Verbose = false
	d.handleArmRequest(req)
	if buf.Len() != 0 {
		t.Errorf("log output with cfg.Verbose=false: %q, want no output", buf.String())
	}

	buf.Reset()
	d.cfg.Verbose = true
	d.handleArmRequest(req)
	if buf.Len() == 0 {
		t.Error("log output with cfg.Verbose=true: empty, want the reject branch to log")
	}
}
