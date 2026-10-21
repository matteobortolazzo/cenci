package main

// `cenci pipeline followup` (ticket #1339): the deterministic deferred-item
// bookkeeping verb behind implement Phase 9 and address-review. This file owns
// the CLI surface only -- positional/flag parsing, conflict and shape
// validation (usage errors exit 2 with a one-line stderr hint, per
// docs/cli-conventions.md) and JSON rendering. Every domain decision lives in
// internal/pipeline's Followup; a domain or GitHub failure prints the full
// result and exits 1. The result is its own {followup, warnings, errors}
// object, not renderMechanicsOutput's stage contract: the verb is stateless
// and a ticketless run has no <id> whose pipeline state could be read.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/pipeline"
)

const pipelineFollowupUsage = "cenci pipeline followup: usage: cenci pipeline followup <id>|--ticketless " +
	"--source implement|address-review --items-file PATH --pr-url URL [--title-file PATH] [--area TOKEN] " +
	"[--append-to N | --create] [--possibly-related N,...] [--repo-slug OWNER/REPO]"

var (
	followupPositiveIntRe = regexp.MustCompile(`^[1-9][0-9]*$`)
	followupPRURLArgRe    = regexp.MustCompile(`^https://\S+/pull/[0-9]+$`)
	followupRepoSlugArgRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	followupRelatedArgRe  = regexp.MustCompile(`^[0-9]+(,[0-9]+)*$`)
)

func pipelineFollowupUsageExit() {
	fmt.Fprintln(os.Stderr, pipelineFollowupUsage)
	os.Exit(2)
}

// runPipelineFollowup implements `cenci pipeline followup <id>|--ticketless
// --source implement|address-review --items-file PATH --pr-url URL
// [--title-file PATH] [--area TOKEN] [--append-to N | --create]
// [--possibly-related N,...] [--repo-slug OWNER/REPO]`.
//
// <id> is an optional leading positional (it cannot go through mechanicsID,
// which exits on a missing id): exactly one of <id> / --ticketless is required,
// and both together is a conflicting-input usage error, never silent
// precedence. Exit codes: 2 for malformed CLI input, 1 when the run failed
// (non-empty errors), 0 otherwise -- needs-judgment and
// skipped/origin-is-followup are successes.
func runPipelineFollowup(rest []string) {
	id := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		id, rest = rest[0], rest[1:]
	}

	fs := flag.NewFlagSet("pipeline followup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("source", "", "caller semantics: implement|address-review")
	itemsFile := fs.String("items-file", "", "JSON array of {context, area} deferred items")
	prURL := fs.String("pr-url", "", "pull request URL (https://.../pull/<n>)")
	titleFile := fs.String("title-file", "", "file holding the title of a created issue")
	area := fs.String("area", "", "project token used by the cross-ticket gates")
	appendTo := fs.String("append-to", "", "append to this eligible open Followup issue number")
	create := fs.Bool("create", false, "create a new Followup instead of stopping for judgment")
	possiblyRelated := fs.String("possibly-related", "", "comma list of issue numbers for the created body (with --create)")
	repoSlug := fs.String("repo-slug", "", "OWNER/REPO gh target (default: the current repository)")
	ticketless := fs.Bool("ticketless", false, "no origin ticket")
	if err := fs.Parse(rest); err != nil {
		pipelineFollowupUsageExit()
	}
	if len(fs.Args()) > 0 {
		pipelineFollowupUsageExit()
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if *ticketless == (id != "") {
		// Neither set, or both set: an ambiguous mode is always a usage error.
		pipelineFollowupUsageExit()
	}
	if id != "" && !followupPositiveIntRe.MatchString(id) {
		pipelineFollowupUsageExit()
	}
	switch *source {
	case pipeline.FollowupSourceImplement, pipeline.FollowupSourceAddressReview:
	default:
		pipelineFollowupUsageExit()
	}
	if *itemsFile == "" || !followupPRURLArgRe.MatchString(*prURL) {
		pipelineFollowupUsageExit()
	}
	if set["title-file"] && *titleFile == "" {
		pipelineFollowupUsageExit()
	}
	if set["area"] && *area == "" {
		pipelineFollowupUsageExit()
	}
	if set["repo-slug"] && !followupRepoSlugArgRe.MatchString(*repoSlug) {
		pipelineFollowupUsageExit()
	}

	appendToN := 0
	if set["append-to"] {
		if !followupPositiveIntRe.MatchString(*appendTo) {
			pipelineFollowupUsageExit()
		}
		n, err := strconv.Atoi(*appendTo)
		if err != nil {
			pipelineFollowupUsageExit()
		}
		appendToN = n
		// --append-to names a candidate whose area is read from --area; with
		// no area every candidate is ineligible, so the call can never succeed.
		if *create || *area == "" {
			pipelineFollowupUsageExit()
		}
	}
	if *create && *titleFile == "" {
		pipelineFollowupUsageExit()
	}
	if set["possibly-related"] && (!*create || !followupRelatedArgRe.MatchString(*possiblyRelated)) {
		pipelineFollowupUsageExit()
	}

	out, err := pipeline.Followup(pipeline.FollowupOpts{
		ID:              id,
		Ticketless:      *ticketless,
		Source:          *source,
		ItemsFile:       *itemsFile,
		PRURL:           *prURL,
		TitleFile:       *titleFile,
		Area:            *area,
		AppendTo:        appendToN,
		Create:          *create,
		PossiblyRelated: *possiblyRelated,
		RepoSlug:        *repoSlug,
	})
	if err != nil && len(out.Errors) == 0 {
		out.Errors = []string{err.Error()}
	}

	b, marshalErr := json.Marshal(out)
	if marshalErr != nil {
		fmt.Fprintf(os.Stderr, "cenci pipeline followup: %v\n", marshalErr)
		os.Exit(1)
	}
	fmt.Println(string(b))

	if err != nil || out.Failed() {
		os.Exit(1)
	}
}
