package babysit

import "strings"

// Automerge attribution (#1049).
//
// `gh pr merge` authenticates as the operator, so GitHub records an
// automerged PR exactly as it records a human clicking "Squash and merge":
// same actor, no comment, no label, no trailer. The ticket's `Implemented`
// relabel (babysit.go's MERGED branch) fires on any detected merge, auto or
// manual, so it is not a signal either. Before this, the only record that a
// merge had been made autonomously was the tick's own stdout log line and
// the Automerge* fields on State -- both of which expire with the daemon's
// log retention, leaving nothing behind on the PR itself.
//
// A confirmed merge therefore posts one comment carrying the same
// condition-chain bracket the log line renders, so the durable record on the
// PR and the operator's log agree verbatim.
//
// Banner only, no `<!-- cenci-<kind> -->` marker: flow/docs/comment-
// attribution.md scopes the marker invariant to *issue* threads (dispatch's
// classifyComments only ever reads a ticket's own comment thread, never a
// PR's), so a marker here would be inert weight with no consumer.

// mergeAttributionBanner is the blockquoted cenci attribution banner the
// comment opens with, matching flow/docs/comment-attribution.md's
// `> 🤖 **cenci** — <what> posted by ... (<phase>).` convention. Blockquoted
// deliberately: dispatch's stripBlockquoteLines removes every `>`-prefixed
// line before any marker or nonce scan, so a banner can never be mistaken
// for machine-readable state.
const mergeAttributionBanner = "> 🤖 **cenci** — merged automatically by `cenci babysit` (automerge policy). No human approved this merge."

// commentBannerPrefix (#897) is the prefix -- through the em dash -- of the
// cenci attribution banner every flow-posted comment opens with, per
// flow/docs/comment-attribution.md (source of truth for the exact literal).
// detectNewFeedbackKeys (babysit.go) is the one sanctioned machine-read of
// this banner, named as a narrow, explicitly-scoped exception in that doc: it
// matches a comment's first line against this prefix only -- never a
// substring scan of the whole body -- to exclude cenci's own address-review
// inline replies (flow/skills/address-review/SKILL.md's "Posting Replies"
// template, the one producer this filter's consumer pairs with) from
// re-triggering dispatch once a reply lands on an already-resolved thread. A
// prefix, not one full banner literal, since every cenci-posted comment body
// this filter must recognize shares this same opening regardless of which
// call site produced it. A drift in this prefix against the doc's literal
// would silently un-filter cenci's own replies.
const commentBannerPrefix = "> 🤖 **cenci** —"

// isCommentBannerFirstLine reports whether body's first line opens with
// commentBannerPrefix -- narrowly scoped to the first line only (never a
// substring scan of the whole body), so a human quoting cenci's own reply
// later in a genuine new comment is still detected normally
// (watch/AGENTS.md's narrow-the-exclusion rule).
func isCommentBannerFirstLine(body string) bool {
	firstLine, _, _ := strings.Cut(body, "\n")
	return strings.HasPrefix(firstLine, commentBannerPrefix)
}

// mergeAttributionTrustNote is the disclaimer every attribution comment
// carries. flow/docs/comment-attribution.md is explicit that the banner is
// human-facing attribution only and never a trust signal: `gh` acts under
// the operator's own identity and the banner literal is public, so anyone
// can post a byte-identical comment. Stating that in the comment itself
// keeps a reader from treating it as proof of who merged.
const mergeAttributionTrustNote = "Attribution only, never proof: `gh` merges and comments under the operator's own GitHub account, so this comment records what babysit did — it does not authenticate it."

// mergeAttributionBody renders the comment posted after a confirmed merge:
// the banner, the head commit the merge was pinned to via
// --match-head-commit, the condition-chain bracket verbatim, and the trust
// disclaimer. headSHA renders as "unknown" when empty rather than as an
// empty code span -- runAutomerge always merges with a pinned SHA, so this
// is defensive rather than a reachable path today.
func mergeAttributionBody(headSHA string, d automergeDecision) string {
	sha := headSHA
	if sha == "" {
		sha = "unknown"
	}
	var b strings.Builder
	b.WriteString(mergeAttributionBanner)
	b.WriteString("\n\nSquash-merged, pinned to head commit `")
	b.WriteString(sha)
	b.WriteString("`. Every automerge policy condition below passed on a full re-evaluation immediately before the merge was issued:\n\n```\n")
	b.WriteString(d.conditionBracket())
	b.WriteString("\n```\n\n")
	b.WriteString(mergeAttributionTrustNote)
	b.WriteString("\n")
	return b.String()
}

// postMergeAttributionComment posts the attribution comment for a merge the
// post-merge refetch already confirmed, and returns decision updated with a
// diagnostic Detail if the comment itself failed.
//
// The comment is strictly post-hoc and must never disturb the merge verdict:
// the merge has already landed by the time this runs, so a failed comment is
// a lost audit record, not a failed merge. It therefore never clears Merge,
// never sets a Reason, and never sets a FailureClass -- FailureClass drives
// the log line's " class=" suffix, and stamping one here would render a
// successful merge as a `gh` failure during incident triage. The failure
// survives in Detail, which logLine renders on the merged branch too (#886).
//
// Called exactly once per confirmed merge: runAutomerge reaches executeMerge
// at most once per tick, and every later tick short-circuits at tick's
// `pr.State == "MERGED"` branch before automerge is evaluated at all, so no
// re-tick (or restarted supervisor) can accumulate a second comment.
func postMergeAttributionComment(s *State, headSHA string, decision automergeDecision) automergeDecision {
	_, stderr, err := execGh("pr", "comment", s.PR, "--repo", s.Repo, "--body", mergeAttributionBody(headSHA, decision))
	if err == nil {
		return decision
	}
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = err.Error()
	}
	note := "merge attribution comment failed: " + detail
	if decision.Detail != "" {
		note = decision.Detail + "; " + note
	}
	decision.Detail = sanitizeDetail(note)
	return decision
}
