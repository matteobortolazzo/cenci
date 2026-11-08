package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
)

// dispatchApproveUsage is the one-line hint printed for every malformed
// `cenci dispatch approve` invocation (no id, a non-numeric id, an extra
// positional, an unknown flag) — exit 2, per docs/cli-conventions.md.
const dispatchApproveUsage = "cenci dispatch approve: usage: cenci dispatch approve <id> [--repo PATH]"

// approveShortHashLen is how many hex characters of the plan's SHA-256 are
// printed: enough to compare against the state file or a later skip line, short
// enough to read.
const approveShortHashLen = 12

// runDispatchApprove is the `cenci dispatch approve <id> [--repo PATH]` verb
// (#1464): it releases a plan held for human review by recording a human
// authorization bound to the plan file's current bytes. runDispatch routes the
// `approve` token here with the arguments after it.
//
// The release channel is deliberately local: the record lands in
// .cenci/pipeline/<id>.json on this machine, and the command refuses an
// unattended session, so no GitHub comment, label or answered question can
// release a plan.
func runDispatchApprove(args []string) {
	if code := dispatchApprove(args, os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

// dispatchApprove is runDispatchApprove's testable core: it returns the exit
// code (0 released or not held, 1 refusal or runtime failure, 2 usage error)
// instead of exiting.
func dispatchApprove(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		_, _ = fmt.Fprintln(stderr, dispatchApproveUsage)
		return 2
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") || !pipelineIDPattern.MatchString(args[0]) {
		return usage()
	}
	id := args[0]

	fs := flag.NewFlagSet("dispatch approve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	repo := fs.String("repo", "", "override the resolved repo root (default: the git repo containing the current directory)")
	if err := fs.Parse(args[1:]); err != nil || len(fs.Args()) > 0 {
		return usage()
	}

	res, err := pipeline.ApproveHeldPlan(pipeline.ApproveOpts{ID: id, RepoRoot: *repo})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cenci dispatch approve: %v\n", err)
		return 1
	}
	if !res.Released {
		_, _ = fmt.Fprintf(stdout, "#%s is not awaiting human review (%s)\n", id, res.Kind)
		return 0
	}
	_, _ = fmt.Fprintf(stdout, "approved #%s: %s (sha256 %s)\n", id, res.PlanPath, res.Fingerprint[:approveShortHashLen])
	if res.ReplacedKind != "" {
		_, _ = fmt.Fprintf(stdout, "replaced authorization record: %s\n", res.ReplacedKind)
	}
	return 0
}
