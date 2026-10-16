package worktreeclean

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
)

// Verdict is what a worktree's facts allow.
type Verdict string

const (
	// VerdictKeep: the worktree must not be removed; Reason says why.
	VerdictKeep Verdict = "keep"
	// VerdictFinished: the ticket's work is over and the worktree is safe to
	// remove together with its local branch.
	VerdictFinished Verdict = "finished"
	// VerdictStale: the worktree has been idle for longer than staleAfter and is
	// safe to remove; its local branch is kept so it can be restored.
	VerdictStale Verdict = "stale"
)

// KeepReason is the closed set of reasons a worktree is kept. Audit every switch
// over it as a group when adding a value.
type KeepReason string

const (
	KeepUnmapped           KeepReason = "unmapped"
	KeepGitError           KeepReason = "git-error"
	KeepLocked             KeepReason = "locked"
	KeepPipelineMidStage   KeepReason = "pipeline-mid-stage"
	KeepPipelineUnreadable KeepReason = "pipeline-unreadable"
	KeepRecentlyActive     KeepReason = "recently-active"
	KeepDirty              KeepReason = "dirty"
	KeepUntracked          KeepReason = "untracked"
	KeepUnpushed           KeepReason = "unpushed"
	KeepOutcomeUnknown     KeepReason = "outcome-unknown"
	KeepActive             KeepReason = "active"
	KeepInUse              KeepReason = "in-use"
	KeepInUseUnknown       KeepReason = "in-use-unknown"
	KeepBabysit            KeepReason = "babysit"
	KeepBabysitUnknown     KeepReason = "babysit-unknown"
	KeepChanged            KeepReason = "changed"
	KeepInvalidOptions     KeepReason = "invalid-options"
)

// finishedGrace is the fixed grace a finished worktree gets: activity
// newer than this keeps it (#1265 Decisions).
const finishedGrace = time.Hour

// Target identifies a worktree to an injected Guard.
type Target struct {
	// Ticket is the number in the dir name's `<digits>-` prefix; 0 when the name
	// has no such prefix.
	Ticket    int
	Name      string
	LocalPath string
}

// Guard answers whether a worktree is in use right now (a tmux window, a
// babysit session). An error means "could not tell" and keeps the worktree.
type Guard func(ctx context.Context, t Target) (blocked bool, err error)

// Options configures Classify, Remove and Reclaim.
type Options struct {
	// StaleAfter is the idle time after which a worktree is stale; it must be
	// positive.
	StaleAfter time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Outcomes is the per-ticket outcome; a ticket missing from it is Unknown.
	Outcomes map[int]ticketstate.OutcomeResult
	// InUse and Babysit are required. Only Classify consults them: Remove re-runs
	// the git checks and the pipeline guard, and the caller re-checks its own
	// guards right before calling Remove.
	InUse, Babysit Guard
}

func (o Options) now() time.Time {
	if o.Now == nil {
		return time.Now()
	}
	return o.Now()
}

// Classification is the verdict for one worktree. Err carries the cause of a
// git-error, pipeline-unreadable or *-unknown keep. Facts are advisory unless
// the verdict is removable.
type Classification struct {
	Entry   Entry
	Verdict Verdict
	Reason  KeepReason
	Facts   Facts
	Err     error
}

// ClassifyAll classifies every entry in order; the result has one
// Classification per entry.
func ClassifyAll(ctx context.Context, repo Repo, entries []Entry, o Options) []Classification {
	out := make([]Classification, len(entries))
	for i, e := range entries {
		out[i] = Classify(ctx, repo, e, o)
	}
	return out
}

// Classify decides whether one worktree is finished, stale or to be kept. It
// only reads. The entry is re-mapped from its AdminName, so a stale or forged
// Entry cannot steer it. Every doubt keeps the worktree: invalid options, an
// unmapped entry, any git failure, a lock, an unreadable or mid-stage pipeline
// run, an Unknown outcome, unsaved work, and a blocking or failing guard.
//
// The finished path applies to `<digits>-*` worktrees on a branch whose ticket
// is closed (issue closed, PR merged, PR closed unmerged). The stale path
// applies to every worktree. When both apply and the finished path keeps the
// worktree, the stale path still gets its say; the reported reason is then the
// finished path's. An Unknown outcome blocks only the finished path.
//
// The git reads here (hasCommit, isAncestor) address the repository by its
// common dir, so they see the live main-repo config rather than the snapshot
// Inspect works from. They run no filter, fsmonitor or transport: they only
// read refs and objects, with GIT_NO_LAZY_FETCH and GIT_ALLOW_PROTOCOL=none
// coming from gitEnv.
func Classify(ctx context.Context, repo Repo, e Entry, o Options) Classification {
	if o.InUse == nil || o.Babysit == nil {
		return kept(e, Facts{}, KeepInvalidOptions, errors.New("worktreeclean: the InUse and Babysit guards are required"))
	}
	c := evaluate(ctx, repo, e, o)
	if c.Verdict == VerdictKeep {
		return c
	}
	ticket, _ := ticketOf(c.Entry.Name)
	target := Target{Ticket: ticket, Name: c.Entry.Name, LocalPath: c.Entry.LocalPath}
	for _, g := range []struct {
		name             string
		guard            Guard
		onBlock, onError KeepReason
	}{
		{"in-use", o.InUse, KeepInUse, KeepInUseUnknown},
		{"babysit", o.Babysit, KeepBabysit, KeepBabysitUnknown},
	} {
		blocked, err := g.guard(ctx, target)
		switch {
		case err != nil:
			return kept(c.Entry, c.Facts, g.onError, fmt.Errorf("%s guard for %s: %w", g.name, c.Entry.Name, err))
		case blocked:
			return kept(c.Entry, c.Facts, g.onBlock, nil)
		}
	}
	return c
}

func kept(e Entry, f Facts, reason KeepReason, err error) Classification {
	return Classification{Entry: e, Verdict: VerdictKeep, Reason: reason, Facts: f, Err: err}
}

// evaluate is Classify without the injected guards: it is also what Remove
// re-runs immediately before deleting.
func evaluate(ctx context.Context, repo Repo, e Entry, o Options) Classification {
	m := mapEntry(repo, e.AdminName)
	if o.StaleAfter <= 0 {
		return kept(m, Facts{}, KeepInvalidOptions, fmt.Errorf("worktreeclean: StaleAfter %s must be positive", o.StaleAfter))
	}
	facts, err := Inspect(ctx, repo, m)
	switch {
	case errors.Is(err, ErrUnmapped):
		return kept(m, facts, KeepUnmapped, err)
	case err != nil:
		return kept(m, facts, KeepGitError, err)
	case facts.Locked:
		return kept(m, facts, KeepLocked, nil)
	case facts.LastActivity.IsZero():
		return kept(m, facts, KeepGitError, fmt.Errorf("worktree %s reports no activity time", m.LocalPath))
	}

	now := o.now()
	ticket, id := ticketOf(m.Name)
	if ticket > 0 {
		blocked, err := pipelineMidStage(repo, id, o.StaleAfter, now)
		switch {
		case err != nil:
			return kept(m, facts, KeepPipelineUnreadable, err)
		case blocked:
			return kept(m, facts, KeepPipelineMidStage, nil)
		}
	}

	verdict, reason, err := decide(ctx, repo, facts, ticket, o, now)
	if err != nil {
		return kept(m, facts, KeepGitError, err)
	}
	return Classification{Entry: m, Verdict: verdict, Reason: reason, Facts: facts}
}

// decide picks the verdict from the facts of a worktree that is mapped, unlocked
// and not mid-pipeline. The reason it reports for a keep is, in order: the
// finished path's if that path applied, else the stale path's if the worktree is
// stale, else outcome-unknown for a ticket with no usable outcome, else active.
func decide(ctx context.Context, repo Repo, facts Facts, ticket int, o Options, now time.Time) (Verdict, KeepReason, error) {
	idle := now.Sub(facts.LastActivity)
	var outcome ticketstate.OutcomeResult // zero value: OutcomeUnknown
	if ticket > 0 {
		outcome = o.Outcomes[ticket]
	}

	var finishedReason KeepReason
	finishedApplies := ticket > 0 && !facts.Detached && finishedOutcome(outcome)
	if finishedApplies {
		reason, err := finishedKeepReason(ctx, repo, facts, outcome, idle)
		if err != nil {
			return "", "", err
		}
		if reason == "" {
			return VerdictFinished, "", nil
		}
		finishedReason = reason
	}

	var staleReason KeepReason
	if idle > o.StaleAfter {
		staleReason = unsavedWork(facts)
		if staleReason == "" {
			return VerdictStale, "", nil
		}
	}

	switch {
	case finishedApplies:
		return VerdictKeep, finishedReason, nil
	case staleReason != "":
		return VerdictKeep, staleReason, nil
	case ticket > 0 && !knownOutcome(outcome):
		return VerdictKeep, KeepOutcomeUnknown, nil
	}
	return VerdictKeep, KeepActive, nil
}

// finishedOutcome reports whether the outcome says the ticket's work is over.
// An outcome that carries an error is not trusted.
func finishedOutcome(r ticketstate.OutcomeResult) bool {
	return r.Err == nil && (r.Outcome == ticketstate.OutcomeIssueClosed ||
		r.Outcome == ticketstate.OutcomePRMerged ||
		r.Outcome == ticketstate.OutcomePRClosedUnmerged)
}

// knownOutcome reports whether the outcome is a successful read of a value this
// package knows. Anything else is treated as Unknown.
func knownOutcome(r ticketstate.OutcomeResult) bool {
	return finishedOutcome(r) || (r.Err == nil && r.Outcome == ticketstate.OutcomeOpen)
}

// finishedKeepReason applies the finished path's conditions: the recent-activity
// grace, a clean tree, then pushed work. It returns "" when the worktree may go.
func finishedKeepReason(ctx context.Context, repo Repo, facts Facts, r ticketstate.OutcomeResult, idle time.Duration) (KeepReason, error) {
	if idle < finishedGrace {
		return KeepRecentlyActive, nil
	}
	if reason := dirtyReason(facts); reason != "" {
		return reason, nil
	}
	if r.Outcome != ticketstate.OutcomePRMerged {
		return pushedReason(facts), nil
	}
	covered, err := coveredByMergedHead(ctx, repo, facts.HeadSHA, r.Heads)
	switch {
	case err != nil:
		return "", err
	case !covered:
		return KeepUnpushed, nil
	}
	return "", nil
}

// unsavedWork is the stale path's condition: a clean tree and pushed work. It
// returns "" when the worktree may go.
func unsavedWork(facts Facts) KeepReason {
	if reason := dirtyReason(facts); reason != "" {
		return reason
	}
	return pushedReason(facts)
}

func dirtyReason(facts Facts) KeepReason {
	switch {
	case facts.Dirty:
		return KeepDirty
	case facts.Untracked:
		return KeepUntracked
	}
	return ""
}

// pushedReason applies the generic pushed rule: with an upstream that exists,
// HEAD must not be ahead of it; otherwise (no upstream, upstream gone or a
// detached HEAD) HEAD must be reachable from some refs/remotes/* ref. An unborn
// HEAD is never pushed.
func pushedReason(facts Facts) KeepReason {
	pushed := false
	switch {
	case facts.HeadSHA == "":
	case facts.AheadKnown:
		pushed = facts.Ahead == 0
	default:
		pushed = facts.ReachableFromRemote
	}
	if !pushed {
		return KeepUnpushed
	}
	return ""
}

// coveredByMergedHead reports whether head (the worktree's HEAD) equals or is an
// ancestor of any merged PR head. A PR head that is not a hex object name or is
// not in the local object store cannot cover it. A git failure with no covering
// head found is an error, never a quiet "not covered".
func coveredByMergedHead(ctx context.Context, repo Repo, head string, prHeads []string) (bool, error) {
	if !isHexSHA(head) {
		return false, nil
	}
	var errs []error
	for _, prHead := range prHeads {
		if !isHexSHA(prHead) {
			continue
		}
		present, err := hasCommit(ctx, repo, prHead)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !present {
			continue
		}
		ancestor, err := isAncestor(ctx, repo, head, prHead)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ancestor {
			return true, nil
		}
	}
	return false, errors.Join(errs...)
}

// gitCommon runs git against the common dir of the main checkout with the
// hardening every call here shares: no optional locks, no fsmonitor, and no
// repo hooks (core.hooksPath=/dev/null; the hooks dir is agent-writable and
// ref updates run reference-transaction, go-gotchas). Callers gate every SHA with
// isHexSHA and every branch with branchPattern.
//
// A commondir file inside --git-dir makes git take its shared state from the
// directory that file names, and an agent that can write the repo's .git could
// point it at another repository. GIT_COMMON_DIR is pinned to the common dir,
// which covers config and the object directory, but the refs backend reads the
// commondir file regardless of it (git 2.43: get_common_dir_noenv), so refs and
// ref updates would still be redirected. A main repo's .git never has such a file
// (only worktree admin dirs do), so the call is refused when one exists before it
// runs, and its result is discarded when one appeared while it ran.
func gitCommon(ctx context.Context, repo Repo, args ...string) (stdout, stderr string, err error) {
	if err := rejectCommondirFile(repo); err != nil {
		return "", "", err
	}
	pinned := []string{
		"--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
		"--git-dir=" + repo.CommonDir,
	}
	stdout, stderr, err = execCapture(ctx, repo.MainRoot, append(pinned, args...), "GIT_COMMON_DIR="+repo.CommonDir)
	if guardErr := rejectCommondirFile(repo); guardErr != nil {
		return "", stderr, errors.Join(err, guardErr)
	}
	return stdout, stderr, err
}

// rejectCommondirFile errors when <CommonDir>/commondir exists or cannot be
// proven absent.
func rejectCommondirFile(repo Repo) error {
	path := filepath.Join(repo.CommonDir, "commondir")
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return fmt.Errorf("refusing to run git: %s exists and would redirect it to another repository", path)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	}
	return fmt.Errorf("refusing to run git: cannot check %s: %w", path, err)
}

// gitYesNo runs a git command that answers by exit code: 0 is yes, 1 is no and
// anything else (including a timeout joined to an exit error) is an error.
func gitYesNo(ctx context.Context, repo Repo, args ...string) (bool, error) {
	_, stderr, err := gitCommon(ctx, repo, args...)
	if err == nil {
		return true, nil
	}
	// Compared by type, not errors.As: a timeout or truncation joined to the
	// exit error must not read as a clean "no".
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, withStderr(err, stderr)
}

// hasCommit reports whether the commit sha is in the local object store.
func hasCommit(ctx context.Context, repo Repo, sha string) (bool, error) {
	if !isHexSHA(sha) {
		return false, fmt.Errorf("malformed object name %q", sha)
	}
	present, err := gitYesNo(ctx, repo, "rev-parse", "--verify", "--quiet", "--end-of-options", sha+"^{commit}")
	if err != nil {
		return false, fmt.Errorf("git rev-parse %s: %w", sha, err)
	}
	return present, nil
}

// isAncestor reports whether commit ancestor is contained in descendant.
func isAncestor(ctx context.Context, repo Repo, ancestor, descendant string) (bool, error) {
	if !isHexSHA(ancestor) || !isHexSHA(descendant) {
		return false, fmt.Errorf("malformed object name in %q, %q", ancestor, descendant)
	}
	yes, err := gitYesNo(ctx, repo, "merge-base", "--is-ancestor", ancestor, descendant)
	if err != nil {
		return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %w", ancestor, descendant, err)
	}
	return yes, nil
}

// ticketOf reads the `<digits>-` prefix of a worktree dir name. id is the digits
// exactly as written (the pipeline names its worktree and state file
// `id + "-" + slug`), ticket their value; ticket is 0 when the name has no such
// prefix or the number is outside 1..MaxInt32.
func ticketOf(name string) (ticket int, id string) {
	end := 0
	for end < len(name) && name[end] >= '0' && name[end] <= '9' {
		end++
	}
	if end == 0 || end == len(name) || name[end] != '-' {
		return 0, ""
	}
	n, err := strconv.Atoi(name[:end])
	if err != nil || n < 1 || n > math.MaxInt32 {
		return 0, ""
	}
	return n, name[:end]
}

// TicketNumbers lists the distinct ticket numbers of the entries' dir names,
// sorted ascending: what to ask the ticket-state reader about.
func TicketNumbers(entries []Entry) []int {
	numbers := make([]int, 0, len(entries))
	for _, e := range entries {
		if n, _ := ticketOf(e.Name); n > 0 {
			numbers = append(numbers, n)
		}
	}
	slices.Sort(numbers)
	return slices.Compact(numbers)
}
