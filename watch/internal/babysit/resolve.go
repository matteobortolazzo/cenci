package babysit

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrResolveUsage: the PR or key given to Resolve is malformed. The CLI maps it
// to exit 2, like every other usage error.
var ErrResolveUsage = errors.New("malformed resolve arguments")

const (
	commentKeyPrefix = "comment:"
	reviewKeyPrefix  = "review:"
	headKeyPrefix    = "head:"
)

// headSHAPattern is the SHA a `head:` key names: a full 40 (SHA-1) or 64 (SHA-256)
// character lowercase hex object id, the same shape a receipt's head line takes.
var headSHAPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// ValidResolveKey reports whether key is shaped like a target `resolve` can be
// pointed at: `discussion:<positive id>`, `comment:<positive id>` or
// `review:<positive id>` (#862: the keys held for their resolution actor; a
// positive decimal integer without a sign or leading zeros, as a discussion id),
// or `head:<sha>` (the head-move hold, #1539: exactly 40 or 64 lowercase hex
// characters). Anything else is a usage error.
func ValidResolveKey(key string) bool {
	if rest, ok := strings.CutPrefix(key, headKeyPrefix); ok {
		return headSHAPattern.MatchString(rest)
	}
	for _, prefix := range []string{discussionKeyPrefix, commentKeyPrefix, reviewKeyPrefix} {
		if rest, ok := strings.CutPrefix(key, prefix); ok {
			id, err := strconv.ParseInt(rest, 10, 64)
			return err == nil && id > 0 && rest == strconv.FormatInt(id, 10)
		}
	}
	return false
}

// Resolve is the host-only, attended-only `cenci babysit resolve <pr> <key>`
// (#1472): a human records one pending `discussion:<id>` key as addressed, with a
// receipt whose source is human-cli. It is the documented recovery for the holds a
// key can be stuck in: a worker that ended without a valid receipt, a
// blocked/failed receipt, and a pending comment that was deleted.
//
// It refuses, changing nothing, when:
//   - CENCI_SANDBOX=1 (the supervisor and its state live on the host),
//   - CENCI_BABYSIT_SUPERVISOR is set (as Reset: it cannot re-arm from inside one),
//   - CENCI_ATTENDED=0, whatever set it. This is the ApproveHeldPlan precedent
//     (#1464) applied literally and fail closed: pipeline.Unattended exempts a
//     host-config origin, this gate does not. A supervisor-launched worker is always
//     unattended (#1497), so it can never use resolve to mark its own work done.
//   - key is not in PendingKeys: an addressed or unknown key has nothing to resolve.
//
// A `comment:<id>` or `review:<id>` key (#862) is accepted only while it is pending
// and in State.ActorHeldKeys: GitHub reports it resolved, but by an actor that fails
// the actor rule (the automation login, an unknown actor, a login without write
// access). Resolve then records an attestation (State.ActorAttestations) for the
// key and the actor the supervisor saw, and removes it from ActorHeldKeys and
// HeldUntrustedKeys. It never marks the key addressed: GitHub stays authoritative
// for whether the thread or review is resolved, and the next tick moves the key. Any
// other comment:/review: key is refused, pointing at GitHub.
//
// A `head:<sha>` key (#1539) is the only way to clear State.HeadMoveHold, the
// PR-level hold raised when a launch that carried untrusted input left the PR head
// moved: the worker acts under the maintainer's GitHub identity and could forge any
// GitHub-side signal, so only this attended host command may clear it. It runs the
// same environment refusals, then refuses (changing nothing) when no hold is
// recorded or <sha> is not the recorded head (a second raise replaces the head, so
// only the newest resolves). It quiesces the supervisor, re-checks the hold in the
// state it re-reads, clears it, saves and re-arms exactly as a discussion key does.
//
// It follows Reset (Q&A 12): quiesceSupervisor verifies and terminates a live
// owned supervisor, the state is rewritten, and the supervisor is re-armed with
// the recorded settings. A `stopped` state is only written: resolve never
// restarts a supervisor the human stopped, and the agent check that guards a
// re-arm is not consulted for it. An unreadable state is refused with
// reasonStateUnreadable and its path, byte-identical.
//
// The key's recorded body revision is what the receipt binds, so a later edit of
// the comment reopens the key. A still-outstanding ReviewLaunch is left to the
// tick, which clears it once its window has ended: the worker may still be live.
// refusal is the CLI's agent check (babysitAgentRefusal), passed in because it
// lives in package main.
func Resolve(pr, key, explicit string, refusal func(agent string) string) error {
	cleanPR := strings.TrimPrefix(pr, "#")
	if n, err := strconv.Atoi(cleanPR); err != nil || n <= 0 {
		return fmt.Errorf("%w: PR must be a positive integer", ErrResolveUsage)
	}
	if !ValidResolveKey(key) {
		return fmt.Errorf("%w: key %q is not discussion:<id>, comment:<id>, review:<id> or head:<sha> (a 40 or 64 character lowercase hex SHA)", ErrResolveUsage, key)
	}
	if os.Getenv("CENCI_SANDBOX") == "1" {
		return fmt.Errorf("not resolved: the babysit supervisor runs on the host and a resolution can only be recorded there; run `cenci babysit resolve %s %s` from a host tmux pane", cleanPR, key)
	}
	if os.Getenv("CENCI_BABYSIT_SUPERVISOR") != "" {
		return errors.New("not resolved: cannot re-arm from inside a babysit supervisor; run `cenci babysit resolve` from a host tmux pane")
	}
	if os.Getenv("CENCI_ATTENDED") == "0" {
		return fmt.Errorf("not resolved: CENCI_ATTENDED=0 marks this session as unattended, and only a human may record feedback as addressed; run `cenci babysit resolve %s %s` from an attended host session", cleanPR, key)
	}
	if sha, ok := strings.CutPrefix(key, headKeyPrefix); ok {
		return resolveHeadMove(cleanPR, sha, explicit, refusal)
	}
	if strings.HasPrefix(key, commentKeyPrefix) || strings.HasPrefix(key, reviewKeyPrefix) {
		return resolveActorHeld(cleanPR, key, explicit, refusal)
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
		if !slices.Contains(s.PendingKeys, key) {
			return fmt.Errorf("not resolved: %s is not pending for PR #%s; resolve only accepts a pending discussion key", key, cleanPR)
		}
		if !stoppedFeedbackOnly(s) && refusal != nil {
			if reason := refusal(s.Agent); reason != "" {
				return &classifiedError{class: ErrAgentRefused, msg: reason}
			}
		}
		return nil
	})
	if errors.Is(err, ErrStateUnreadable) {
		return unreadableStateError(path, err)
	}
	if err != nil {
		return err
	}

	// The supervisor may have saved while it was dying: if it already moved the key
	// (a valid receipt), the resolution is done and its own evidence is kept.
	recorded := slices.Contains(s.PendingKeys, key)
	if recorded {
		recordHumanResolution(&s, key)
	}
	s.PID = 0
	s.UpdatedAt = time.Now().UTC()
	if err := save(path, s); err != nil {
		if stoppedFeedbackOnly(s) {
			return err
		}
		return fmt.Errorf("any supervisor for PR #%s was stopped, but %s was not resolved and nothing was re-armed: %w; fix the cause, then re-arm with cenci babysit %s --agent %s", cleanPR, key, err, cleanPR, s.Agent)
	}

	switch {
	case !recorded:
		fmt.Printf("%s was already addressed for PR #%s; nothing to record.\n", key, cleanPR)
	case stoppedFeedbackOnly(s):
		fmt.Printf("Resolved %s for PR #%s; the supervisor is stopped, so it was not re-armed. Re-arm it with cenci babysit %s --agent <claude|codex>.\n", key, cleanPR, cleanPR)
	default:
		fmt.Printf("Resolved %s for PR #%s.\n", key, cleanPR)
	}
	if stoppedFeedbackOnly(s) {
		return nil
	}
	if err := reArm(dir, cleanPR, s); err != nil {
		return fmt.Errorf("resolved %s for PR #%s but not re-armed: %w; re-arm with cenci babysit %s --agent %s", key, cleanPR, err, cleanPR, s.Agent)
	}
	return nil
}

// headMoveRefusal is why a resolve of head sha cannot clear s's head-move hold, or
// nil when it can: no hold is recorded, or sha is not the recorded (newest) head.
func headMoveRefusal(s State, pr, sha string) error {
	if s.HeadMoveHold == nil {
		return fmt.Errorf("not resolved: no head-move hold is recorded for PR #%s; head:<sha> resolves only the %q hold", pr, reasonHeadMovedUntrustedReview)
	}
	if recorded := s.HeadMoveHold.HeadSHA; recorded != sha {
		return fmt.Errorf("not resolved: the head-move hold for PR #%s is recorded at %s, not %s; %s", pr, recorded, sha, headMoveResolveHint(pr, recorded))
	}
	return nil
}

// resolveHeadMove is Resolve's `head:<sha>` branch: the environment refusals have
// already passed. It follows the discussion branch: quiesceSupervisor, clear the
// hold, save, re-arm (a stopped state is written and never re-armed).
func resolveHeadMove(pr, sha, explicit string, refusal func(agent string) string) error {
	dir, err := stateDir(explicit)
	if err != nil {
		return err
	}
	repo, err := repository()
	if err != nil {
		return err
	}
	path := statePath(dir, repo, pr)

	s, err := quiesceSupervisor(path, pr, func(s State) error {
		if err := headMoveRefusal(s, pr, sha); err != nil {
			return err
		}
		if !stoppedFeedbackOnly(s) && refusal != nil {
			if reason := refusal(s.Agent); reason != "" {
				return &classifiedError{class: ErrAgentRefused, msg: reason}
			}
		}
		return nil
	})
	if errors.Is(err, ErrStateUnreadable) {
		return unreadableStateError(path, err)
	}
	if err != nil {
		return err
	}

	// The supervisor may have saved while it was dying: it may have recorded a newer
	// head (a second raise), which the human has not reviewed.
	if err := headMoveRefusal(s, pr, sha); err != nil {
		if stoppedFeedbackOnly(s) {
			return err
		}
		return fmt.Errorf("%w; the supervisor for PR #%s was stopped to resolve it and was not re-armed, so re-arm it with cenci babysit %s --agent %s", err, pr, pr, s.Agent)
	}
	s.HeadMoveHold = nil
	s.PID = 0
	s.UpdatedAt = time.Now().UTC()
	if err := save(path, s); err != nil {
		if stoppedFeedbackOnly(s) {
			return err
		}
		return fmt.Errorf("any supervisor for PR #%s was stopped, but the head-move hold at %s was not resolved and nothing was re-armed: %w; fix the cause, then re-arm with cenci babysit %s --agent %s", pr, sha, err, pr, s.Agent)
	}

	if stoppedFeedbackOnly(s) {
		fmt.Printf("Resolved the head-move hold at %s for PR #%s; the supervisor is stopped, so it was not re-armed. Re-arm it with cenci babysit %s --agent <claude|codex>.\n", sha, pr, pr)
		return nil
	}
	fmt.Printf("Resolved the head-move hold at %s for PR #%s.\n", sha, pr)
	if err := reArm(dir, pr, s); err != nil {
		return fmt.Errorf("resolved the head-move hold at %s for PR #%s but not re-armed: %w; re-arm with cenci babysit %s --agent %s", sha, pr, err, pr, s.Agent)
	}
	return nil
}

// actorHeldRefusal is why key cannot be attested in s, or nil when it can: it is
// not pending, or not held for its resolution actor.
func actorHeldRefusal(s State, pr, key string) error {
	if _, held := s.ActorHeldKeys[key]; !held || !slices.Contains(s.PendingKeys, key) {
		return fmt.Errorf("not resolved: %s is not held for an unauthorized resolution actor on PR #%s; resolve it on GitHub", key, pr)
	}
	return nil
}

// resolveActorHeld is Resolve's `comment:<id>`/`review:<id>` branch (#862): the
// environment refusals have already passed. It follows resolveHeadMove:
// quiesceSupervisor, re-check the key in the state it re-reads (the supervisor may
// have moved it while dying), record the attestation, save, re-arm (a stopped state
// is written and never re-armed).
func resolveActorHeld(pr, key, explicit string, refusal func(agent string) string) error {
	dir, err := stateDir(explicit)
	if err != nil {
		return err
	}
	repo, err := repository()
	if err != nil {
		return err
	}
	path := statePath(dir, repo, pr)

	s, err := quiesceSupervisor(path, pr, func(s State) error {
		if err := actorHeldRefusal(s, pr, key); err != nil {
			return err
		}
		if !stoppedFeedbackOnly(s) && refusal != nil {
			if reason := refusal(s.Agent); reason != "" {
				return &classifiedError{class: ErrAgentRefused, msg: reason}
			}
		}
		return nil
	})
	if errors.Is(err, ErrStateUnreadable) {
		return unreadableStateError(path, err)
	}
	if err != nil {
		return err
	}

	// The supervisor may have saved while it was dying: an authorized actor may have
	// resolved the key in the meantime, and then there is nothing to attest.
	if err := actorHeldRefusal(s, pr, key); err != nil {
		if stoppedFeedbackOnly(s) {
			return err
		}
		return fmt.Errorf("%w; the supervisor for PR #%s was stopped to resolve it and was not re-armed, so re-arm it with cenci babysit %s --agent %s", err, pr, pr, s.Agent)
	}
	if s.ActorAttestations == nil {
		s.ActorAttestations = map[string]actorAttestation{}
	}
	now := time.Now().UTC()
	s.ActorAttestations[key] = actorAttestation{Actor: s.ActorHeldKeys[key], AttestedAt: now, Source: attestationSourceHumanCLI}
	delete(s.ActorHeldKeys, key)
	if len(s.ActorHeldKeys) == 0 {
		s.ActorHeldKeys = nil
	}
	s.HeldUntrustedKeys = removeKeys(s.HeldUntrustedKeys, []string{key})
	s.PID = 0
	s.UpdatedAt = now
	if err := save(path, s); err != nil {
		if stoppedFeedbackOnly(s) {
			return err
		}
		return fmt.Errorf("any supervisor for PR #%s was stopped, but %s was not resolved and nothing was re-armed: %w; fix the cause, then re-arm with cenci babysit %s --agent %s", pr, key, err, pr, s.Agent)
	}

	if stoppedFeedbackOnly(s) {
		fmt.Printf("Resolved %s for PR #%s; the supervisor is stopped, so it was not re-armed. Re-arm it with cenci babysit %s --agent <claude|codex>.\n", key, pr, pr)
		return nil
	}
	fmt.Printf("Resolved %s for PR #%s.\n", key, pr)
	if err := reArm(dir, pr, s); err != nil {
		return fmt.Errorf("resolved %s for PR #%s but not re-armed: %w; re-arm with cenci babysit %s --agent %s", key, pr, err, pr, s.Agent)
	}
	return nil
}

// recordHumanResolution moves key from pending and launched to addressed with a
// human-cli receipt at the revision the supervisor last recorded for it (no receipt
// comment, so ReceiptCommentID is 0). Every other field is left alone.
func recordHumanResolution(s *State, key string) {
	markDiscussionAddressed(s, key, discussionReceipt{
		CommentID: discussionKeyID(key),
		Revision:  s.DiscussionRevisions[key],
		Outcome:   receiptOutcomeAddressed,
		Source:    receiptSourceHumanCLI,
	})
}

// unreadableStateError renders the strict-read failure the way host `status`
// does: reasonStateUnreadable, the path, then the underlying cause.
func unreadableStateError(path string, err error) error {
	cause := err
	var classified *classifiedError
	if errors.As(err, &classified) && classified.cause != nil {
		cause = classified.cause
		if inner := errors.Unwrap(cause); inner != nil {
			cause = inner
		}
	}
	return &classifiedError{class: ErrStateUnreadable, cause: cause, msg: fmt.Sprintf("%s: %s: %v", reasonStateUnreadable, path, cause)}
}
