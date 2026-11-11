package babysit

import (
	"errors"
	"fmt"
	"os"
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
)

// ValidResolveKey reports whether key is shaped like a feedback key `resolve` can
// be pointed at: `discussion:<positive id>`, or a `comment:`/`review:` key (which
// Resolve then refuses with a pointer to GitHub). Anything else is a usage error.
func ValidResolveKey(key string) bool {
	if rest, ok := strings.CutPrefix(key, discussionKeyPrefix); ok {
		id, err := strconv.ParseInt(rest, 10, 64)
		return err == nil && id > 0 && rest == strconv.FormatInt(id, 10)
	}
	for _, prefix := range []string{commentKeyPrefix, reviewKeyPrefix} {
		if rest, ok := strings.CutPrefix(key, prefix); ok && rest != "" {
			return true
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
//   - key is a `comment:` or `review:` key (GitHub is authoritative for those),
//   - key is not in PendingKeys: an addressed or unknown key has nothing to resolve.
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
		return fmt.Errorf("%w: key %q is not discussion:<id>", ErrResolveUsage, key)
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
	switch {
	case strings.HasPrefix(key, commentKeyPrefix):
		return fmt.Errorf("not resolved: %s is an inline review thread; resolve it on GitHub", key)
	case strings.HasPrefix(key, reviewKeyPrefix):
		return fmt.Errorf("not resolved: %s is a PR review; resolve it on GitHub", key)
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
