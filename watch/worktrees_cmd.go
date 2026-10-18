package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"unicode"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/babysit"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/worktreeclean"
)

// This file implements the `cenci worktrees <verb>` group (ls, prune): the manual
// front end of internal/worktreeclean for the repo containing the cwd. Removal is
// irreversible, so every doubt keeps the worktree and prune re-checks the in-use
// and babysit guards right before each removal (worktreeclean.Remove does not).

// worktreesUnknownNote marks a row that is removable but whose in-use status could
// not be read: ls and --dry-run show the real verdict, prune keeps it.
const worktreesUnknownNote = "(in-use status unknown: prune will keep it here)"

// runWorktrees implements `cenci worktrees <verb> [flags]`.
func runWorktrees(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "cenci worktrees: expected a subcommand: ls, prune")
		os.Exit(2)
	}
	switch verb := args[0]; verb {
	case "ls":
		runWorktreesLs(args[1:])
	case "prune":
		runWorktreesPrune(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "cenci worktrees: unknown subcommand %q\n", verb)
		os.Exit(2)
	}
}

// runWorktreesLs implements `cenci worktrees ls`: every worktree of the repo with
// its verdict, reason and size, judged exactly as prune would judge it, changing
// nothing.
func runWorktreesLs(args []string) {
	fs := flag.NewFlagSet("worktrees ls", flag.ExitOnError)
	_ = fs.Parse(args)
	rejectExtra("cenci worktrees ls", fs.Args())
	os.Exit(worktreesLs(os.Stdout, os.Stderr))
}

// runWorktreesPrune implements `cenci worktrees prune [--dry-run]`: remove the
// finished and stale worktrees of the repo, or with --dry-run print exactly what
// would go.
func runWorktreesPrune(args []string) {
	fs := flag.NewFlagSet("worktrees prune", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "print the removals a pass would make, with reasons and sizes, and change nothing")
	_ = fs.Parse(args)
	rejectExtra("cenci worktrees prune", fs.Args())
	os.Exit(worktreesPrune(os.Stdout, os.Stderr, *dryRun))
}

// worktreesPass is everything one pass reads before judging the worktrees.
type worktreesPass struct {
	repo     worktreeclean.Repo
	entries  []worktreeclean.Entry
	cfg      ticketstate.WorktreeCleanup
	outcomes map[int]ticketstate.OutcomeResult
	live     worktreeclean.LiveState
}

// loadWorktreesPass enumerates the worktrees of the repo containing the cwd and
// reads the config, ticket outcomes and live state, printing a note to out for
// each source that is off or unreadable. A malformed config is an error: a
// mistyped longer staleAfter must not be swapped for the default.
func loadWorktreesPass(ctx context.Context, out io.Writer) (worktreesPass, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return worktreesPass{}, err
	}
	repo, entries, err := worktreeclean.Enumerate(ctx, cwd)
	if err != nil {
		return worktreesPass{}, err
	}
	cfg, err := ticketstate.WorktreeCleanupConfig(repo.MainRoot)
	if err != nil {
		return worktreesPass{}, err
	}
	p := worktreesPass{repo: repo, entries: entries, cfg: cfg}
	if !cfg.Enabled {
		say(out, "note: worktreeCleanup.enabled is false, which governs only the automatic pass; this manual run goes on\n")
	}

	p.outcomes = readWorktreeOutcomes(ctx, out, repo, entries)

	p.live = worktreeclean.ReadLiveState(ctx, worktreeclean.LiveSources{})
	if p.live.SnapshotErr != nil {
		say(out, "note: daemon snapshot unavailable (%s); only tmux windows and pane directories are checked\n", oneLine(p.live.SnapshotErr.Error()))
	}
	if p.live.WindowsErr != nil {
		say(out, "note: in-use status unknown: %s\n", oneLine(p.live.WindowsErr.Error()))
	}
	return p, nil
}

// readWorktreeOutcomes reads the GitHub outcome of every ticket the worktrees are
// named for. Failure is not fatal: a ticket with no outcome is Unknown, which
// blocks the finished path while the stale path still applies.
func readWorktreeOutcomes(ctx context.Context, out io.Writer, repo worktreeclean.Repo, entries []worktreeclean.Entry) map[int]ticketstate.OutcomeResult {
	tickets := worktreeclean.TicketNumbers(entries)
	if len(tickets) == 0 {
		return nil
	}
	const note = "note: ticket state unavailable (%s); finished worktrees are kept, idle ones are still judged\n"
	identity, err := (&ticketstate.Resolver{}).Resolve(ctx, repo.MainRoot)
	if err != nil {
		say(out, note, oneLine(err.Error()))
		return nil
	}
	outcomes := (&ticketstate.Reader{}).ReadOutcomes(ctx, identity.Repo, tickets)
	for _, n := range tickets {
		if err := outcomes[n].Err; err != nil {
			say(out, note, oneLine(fmt.Sprintf("#%d: %v", n, err)))
			break
		}
	}
	return outcomes
}

// babysitBlocks is the production babysit check: the ticket alone, not scoped to
// a repo root.
func babysitBlocks(ticket string) (bool, string) {
	return babysit.BlocksClose(ticket, "", "")
}

// options builds the classification options over live. With strict an unreadable
// window list keeps a worktree as in-use-unknown; without it the verdict is the
// real one.
func (p worktreesPass) options(live worktreeclean.LiveState, strict bool) worktreeclean.Options {
	return worktreeclean.Options{
		StaleAfter: p.cfg.StaleAfter,
		Outcomes:   p.outcomes,
		InUse:      worktreeclean.InUseGuard(live, strict),
		Babysit:    worktreeclean.BabysitGuard(babysitBlocks),
	}
}

func worktreesLs(out, errOut io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p, err := loadWorktreesPass(ctx, out)
	if err != nil {
		say(errOut, "cenci worktrees ls: %v\n", err)
		return 1
	}
	cs := worktreeclean.ClassifyAll(ctx, p.repo, p.entries, p.options(p.live, false))
	if interrupted(ctx, errOut, "ls") {
		return 1
	}
	if len(cs) == 0 {
		say(out, "no worktrees found\n")
		return 0
	}

	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tVERDICT\tREASON\tSIZE")
	for _, c := range cs {
		reason := "-"
		if c.Verdict == worktreeclean.VerdictKeep {
			reason = keepLabel(c.Reason)
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", worktreeName(c.Entry), c.Verdict, reason, sizeText(c)+p.unknownSuffix(c))
	}
	_ = w.Flush()
	for _, c := range cs {
		printWorktreeCause(out, c)
	}
	return 0
}

func worktreesPrune(out, errOut io.Writer, dryRun bool) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p, err := loadWorktreesPass(ctx, out)
	if err != nil {
		say(errOut, "cenci worktrees prune: %v\n", err)
		return 1
	}
	if dryRun {
		cs := worktreeclean.ClassifyAll(ctx, p.repo, p.entries, p.options(p.live, false))
		if interrupted(ctx, errOut, "prune") {
			return 1
		}
		for _, c := range cs {
			if removable(c) {
				say(out, "would remove %s: %s, %s%s\n", worktreeName(c.Entry), c.Verdict, sizeText(c), p.unknownSuffix(c))
			} else {
				say(out, "keep %s: %s\n", worktreeName(c.Entry), keepLabel(c.Reason))
			}
			printWorktreeCause(out, c)
		}
		return 0
	}

	failed := false
	for _, c := range worktreeclean.ClassifyAll(ctx, p.repo, p.entries, p.options(p.live, true)) {
		if removable(c) {
			// The classification above is a minute old by the time a long pass
			// reaches this worktree: read what is live again and judge it again
			// with the strict guards right before the removal.
			o := p.options(worktreeclean.ReadLiveState(ctx, worktreeclean.LiveSources{}), true)
			c = worktreeclean.Classify(ctx, p.repo, c.Entry, o)
			if removable(c) {
				failed = pruneOne(ctx, out, errOut, p.repo, c, o) || failed
				continue
			}
		}
		say(out, "kept %s: %s\n", worktreeName(c.Entry), keepLabel(c.Reason))
		printWorktreeCause(out, c)
	}
	if interrupted(ctx, errOut, "prune") || failed {
		return 1
	}
	return 0
}

// interrupted reports, on errOut, that a signal cut the pass short: whatever was
// not handled by then was kept only because the pass stopped, so it is a failure
// and not a clean run.
func interrupted(ctx context.Context, errOut io.Writer, verb string) bool {
	if ctx.Err() == nil {
		return false
	}
	say(errOut, "cenci worktrees %s: interrupted before the pass finished; the rest was left untouched\n", verb)
	return true
}

// pruneOne removes one worktree that passed every check just now and reports the
// result. It returns true when the removal failed or only half completed.
func pruneOne(ctx context.Context, out, errOut io.Writer, repo worktreeclean.Repo, c worktreeclean.Classification, o worktreeclean.Options) (failed bool) {
	return reportRemoval(out, errOut, worktreeName(c.Entry), worktreeclean.Remove(ctx, repo, c, o))
}

// reportRemoval prints the outcome of one Remove and returns true when it counts
// as a failure (exit 1): a removal that errored, or one that took the worktree
// but left something behind. A worktree that changed since it was classified is
// a keep, not a failure.
func reportRemoval(out, errOut io.Writer, name string, rem worktreeclean.Removal) (failed bool) {
	switch {
	case rem.Removed:
		say(out, "removed %s: %s, freed %s (%d bytes)\n", name, rem.Verdict, humanSize(rem.BytesFreed), rem.BytesFreed)
		if rem.Err != nil {
			say(errOut, "cenci worktrees prune: %s: removed but not cleaned up completely: %s\n", name, oneLine(rem.Err.Error()))
			return true
		}
	case rem.Reason == worktreeclean.KeepChanged:
		say(out, "kept %s: %s\n", name, keepLabel(rem.Reason))
		say(out, "note: %s: %s\n", name, oneLine(fmt.Sprint(rem.Err)))
	default:
		say(errOut, "cenci worktrees prune: %s: not removed: %s\n", name, oneLine(fmt.Sprint(rem.Err)))
		return true
	}
	return false
}

// say writes one formatted message; a failed write to the terminal has nowhere
// better to be reported.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func removable(c worktreeclean.Classification) bool {
	return c.Verdict == worktreeclean.VerdictFinished || c.Verdict == worktreeclean.VerdictStale
}

// unknownSuffix is the row note for a removable worktree judged without a
// readable window list.
func (p worktreesPass) unknownSuffix(c worktreeclean.Classification) string {
	if removable(c) && p.live.WindowsErr != nil {
		return "  " + worktreesUnknownNote
	}
	return ""
}

// printWorktreeCause prints the cause behind a keep (a git error, an unmapped
// entry, an unknown guard) that the reason label alone does not carry.
func printWorktreeCause(out io.Writer, c worktreeclean.Classification) {
	if c.Err != nil {
		say(out, "note: %s: %s\n", worktreeName(c.Entry), oneLine(c.Err.Error()))
	}
}

// worktreeName is the name to show: an unmapped entry has no trustworthy
// worktree name, only its admin directory.
func worktreeName(e worktreeclean.Entry) string {
	if e.Mapping == worktreeclean.MappingUnmapped || e.Name == "" {
		return oneLine(e.AdminName)
	}
	return oneLine(e.Name)
}

// sizeText is the worktree's size, or "-" when the facts behind it are not
// trustworthy (an unmapped entry, a git failure, invalid options).
func sizeText(c worktreeclean.Classification) string {
	switch c.Reason {
	case worktreeclean.KeepUnmapped, worktreeclean.KeepGitError, worktreeclean.KeepInvalidOptions:
		return "-"
	}
	return humanSize(c.Facts.SizeBytes)
}

// keepLabel is the user-facing text of a keep reason. It is a closed-set switch:
// a reason added to worktreeclean falls through to its raw value, visibly.
func keepLabel(r worktreeclean.KeepReason) string {
	switch r {
	case worktreeclean.KeepUnmapped:
		return "unmapped"
	case worktreeclean.KeepGitError:
		return "git error"
	case worktreeclean.KeepLocked:
		return "locked"
	case worktreeclean.KeepPipelineMidStage:
		return "pipeline mid-stage"
	case worktreeclean.KeepPipelineUnreadable:
		return "pipeline state unreadable"
	case worktreeclean.KeepRecentlyActive:
		return "recently active"
	case worktreeclean.KeepDirty:
		return "dirty"
	case worktreeclean.KeepUntracked:
		return "untracked"
	case worktreeclean.KeepUnpushed:
		return "unpushed"
	case worktreeclean.KeepOutcomeUnknown:
		return "ticket outcome unknown"
	case worktreeclean.KeepActive:
		return "active"
	case worktreeclean.KeepInUse:
		return "in use"
	case worktreeclean.KeepInUseUnknown:
		return "in-use status unknown"
	case worktreeclean.KeepBabysit:
		return "babysit armed"
	case worktreeclean.KeepBabysitUnknown:
		return "babysit status unknown"
	case worktreeclean.KeepChanged:
		return "changed since classified"
	case worktreeclean.KeepInvalidOptions:
		return "invalid options"
	default:
		return string(r)
	}
}

// humanSize formats a byte count in binary units, e.g. "512 B" or "12.3 KiB".
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// oneLine makes untrusted text (git, tmux and gh stderr, names read from disk)
// safe for one terminal line: control characters (C0, DEL, C1) and the bidi
// controls that can reorder what follows become spaces.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == 0x200E || r == 0x200F || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) {
			return ' '
		}
		return r
	}, s)
}
