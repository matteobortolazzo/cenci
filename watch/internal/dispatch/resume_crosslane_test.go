package dispatch

// Cross-lane integration test for #849 AC6: "Cross-lane integration tests
// use the actual producer payload and consumer parser rather than separate
// synthetic contracts." This file is the mandatory Go half of that
// requirement (the plan's Q3): it reads the REAL producer documentation
// (flow/skills/implement/phases/phase-1-plan.md), extracts the documented
// escalation-anchor marker/nonce template from its `## Escalation Anchor`
// section, renders a genuine comment body from that template with a
// real-shaped nonce, and feeds it -- plus a qualifying reply -- through
// classifyComments, the REAL production parser. No hand-written synthetic
// anchor body is used for the rendered payload.
//
// Hard requirement (the plan's Q3, restated here verbatim): if this file
// cannot read phase-1-plan.md, or cannot extract the marker/nonce template
// from it, the test MUST t.Fatal -- never t.Skip. A skip here would defeat
// AC6 entirely, since this is the only Go-side coverage of the producer
// payload actually round-tripping through the real consumer parser.
//
// This repo has no precedent for a Go test reading flow/ files (the plan's
// Q3 rationale); extractMarkdownSection below is a fresh, minimal, awk-free
// Go port of the fence-aware "## <heading>" section-extraction idiom
// flow/tests/*.test.sh's own extract_*_section awk helpers already use
// (docs/shell-scripting-gotchas.md's fence-aware extraction rule).

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// escalationAnchorTemplatePattern is the literal marker/nonce template the
// plan's Assumptions document phase-1-plan.md's `## Escalation Anchor`
// section must state: `<!-- cenci-planner-escalation:<nonce> -->` with the
// literal placeholder token `<nonce>` (not a real nonce value) -- the
// producer's own documentation of the shape it mints and posts.
var escalationAnchorTemplatePattern = regexp.MustCompile(`<!-- cenci-planner-escalation:<nonce> -->`)

func TestResumeCrossLane_ProducerTemplateThroughRealClassifyComments(t *testing.T) {
	// go test always chdirs to the package directory before running, so
	// this relative path is stable regardless of which directory `go test`
	// itself was invoked from. Package dir: watch/internal/dispatch; three
	// levels up is the repo root.
	path := filepath.Join("..", "..", "..", "flow", "skills", "implement", "phases", "phase-1-plan.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cross-lane (#849 AC6): could not read the producer doc at %s: %v -- this is a hard failure, never a skip, per the plan's Q3 hard requirement", path, err)
	}
	content := string(data)

	section := extractMarkdownSection(content, "## Escalation Anchor")
	if section == "" {
		t.Fatalf("cross-lane (#849 AC6): could not locate '## Escalation Anchor' in %s -- this is a hard failure, never a skip. Expected as of #849 Implementation Order step 4.", path)
	}

	template := escalationAnchorTemplatePattern.FindString(section)
	if template == "" {
		t.Fatalf("cross-lane (#849 AC6): could not extract the documented marker/nonce template from %s's '## Escalation Anchor' section -- this is a hard failure, never a skip. Expected to find the literal template `<!-- cenci-planner-escalation:<nonce> -->` documented there (per the plan's Assumptions).", path)
	}

	nonce := "0123456789abcdef0123456789abcdef"
	if !escalationNoncePattern.MatchString(nonce) {
		t.Fatalf("test setup: fixture nonce %q does not itself match escalationNoncePattern %s", nonce, escalationNoncePattern.String())
	}
	rendered := strings.ReplaceAll(template, "<nonce>", nonce)
	if rendered == "" || rendered == template {
		t.Fatalf("cross-lane (#849 AC6): could not render a concrete comment body from the extracted template %q with nonce %q", template, nonce)
	}

	// Feed the producer-rendered anchor, plus a qualifying reply, through
	// the REAL classifyComments -- no hand-written synthetic anchor body.
	// classifyComments' signature is (comments []restIssueComment, anchorID
	// int64, nonce string): anchor identity is the exact stored comment ID
	// (here, 100), verified against the rendered marker's nonce.
	const anchorID = int64(100)
	comments := []restIssueComment{
		{ID: anchorID, Body: "Question text.\n" + rendered, Author: restCommentAuthor{Login: "matteobortolazzo", Type: "User"}},
		{ID: anchorID + 1, Body: "Here's my answer.", Author: restCommentAuthor{Login: "octocat", Type: "User"}, AuthorAssociation: "COLLABORATOR"},
	}
	got := classifyComments(comments, anchorID, nonce)
	if got != AnswerProbeAnswered {
		t.Errorf("classifyComments(real producer template, rendered) = %q, want AnswerProbeAnswered", got)
	}
}

// -- #853: label-set contract + unknown⇒stale rule, cross-lane -------------
//
// Extends the #849 AC6 precedent above: the real producer doc is the input,
// the real Go constants/behavior are the consumer -- no synthetic contract
// on either side. Two distinct cross-lane checks:
//
//  1. SKILL.md's `## Label "Working"` section must literally name both
//     labels the "working" transition's atomic swap touches (#853's Q1/D1):
//     the exact strings labelWorking ("Working") and labelInputNeeded
//     ("Input Needed") -- already-existing Go constants (reconcile.go), so
//     this half needs no new production symbol and is real coverage today.
//  2. phase-1-plan.md's `## Resume From Draft` section must state the
//     unknown⇒stale rule using the same literal freshness values
//     PlanCheck.DraftFreshness resolves to ("unknown", "stale") -- these are
//     plain string literals (mirroring PlanCheck.Decision's own
//     "resume"/"stale"/"awaiting-input" convention, never exported Go
//     constants), hardcoded here as the two the design fixes, per this
//     ticket's Alternatives Considered section ("draft_freshness:
//     'fresh'|'stale'|'unknown'").

func TestResumeCrossLane_LabelSetContractStatedInSkillMdMatchesGoConstants(t *testing.T) {
	path := filepath.Join("..", "..", "..", "flow", "skills", "implement", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cross-lane (#853): could not read the producer doc at %s: %v -- this is a hard failure, never a skip", path, err)
	}
	content := string(data)

	section := extractMarkdownSection(content, `## Label "Working"`)
	if section == "" {
		t.Fatalf("cross-lane (#853): could not locate '## Label \"Working\"' in %s -- this is a hard failure, never a skip", path)
	}

	if !strings.Contains(section, labelWorking) {
		t.Errorf("cross-lane (#853): %s's '## Label \"Working\"' section does not mention the real Go label constant labelWorking (%q)", path, labelWorking)
	}
	if !strings.Contains(section, labelInputNeeded) {
		t.Errorf("cross-lane (#853): %s's '## Label \"Working\"' section does not mention the real Go label constant labelInputNeeded (%q) -- the doc must state that --transition working atomically retires it", path, labelInputNeeded)
	}
	if !strings.Contains(section, "atomically retires") {
		t.Errorf("cross-lane (#853): %s's '## Label \"Working\"' section must state the atomic-retirement wording (\"atomically retires `Input Needed`\") verbatim", path)
	}
}

func TestResumeCrossLane_UnknownStaleRuleStatedInPhase1PlanMatchesDraftFreshnessValues(t *testing.T) {
	path := filepath.Join("..", "..", "..", "flow", "skills", "implement", "phases", "phase-1-plan.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cross-lane (#853): could not read the producer doc at %s: %v -- this is a hard failure, never a skip", path, err)
	}
	content := string(data)

	section := extractMarkdownSection(content, "## Resume From Draft")
	if section == "" {
		t.Fatalf("cross-lane (#853): could not locate '## Resume From Draft' in %s -- this is a hard failure, never a skip", path)
	}

	// The two literal freshness values PlanCheck.DraftFreshness resolves to
	// (plain string literals, mirroring PlanCheck.Decision's own convention
	// -- see this file's doc comment above).
	for _, want := range []string{"unknown", "stale"} {
		if !strings.Contains(section, "`"+want+"`") {
			t.Errorf("cross-lane (#853): %s's '## Resume From Draft' section does not mention the literal draft_freshness value `%s`", path, want)
		}
	}
	if !strings.Contains(section, "`unknown` is treated exactly as `stale`") {
		t.Errorf("cross-lane (#853): %s's '## Resume From Draft' section must state the unknown⇒stale rule verbatim (\"`unknown` is treated exactly as `stale`\")", path)
	}
}

// -- #880: re-escalation replacement-nonce cross-lane case -----------------
//
// Extends the #849 AC6 precedent above to the resume/re-escalation path
// specifically: `## Resume From Draft` step 3 mints and persists a *fresh
// replacement* nonce (never reusing the original escalation's), so this test
// extracts the marker/nonce template from that section -- not from
// `## Escalation Anchor` -- renders a payload for a genuine replacement
// nonce, and proves three distinct classifyComments outcomes through the
// REAL producer template and the REAL consumer parser (the plan's AC5/AC7):
// (a) the new anchor plus a qualifying reply resumes; (b) a reply attached
// only to the old/orphaned anchor never resumes, even though that anchor
// once carried a genuine (now-superseded) marker; (c) the nonce-without-ID
// intermediate state (#880's reused representation, Q2: escalationNonce set,
// escalationCommentId absent/0) fails closed without ever consulting the
// thread. Same hard t.Fatal-never-t.Skip requirement as the #849 test above.
func TestResumeCrossLane_ReEscalationReplacementNonceThroughRealClassifyComments(t *testing.T) {
	path := filepath.Join("..", "..", "..", "flow", "skills", "implement", "phases", "phase-1-plan.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cross-lane (#880): could not read the producer doc at %s: %v -- this is a hard failure, never a skip", path, err)
	}
	content := string(data)

	section := extractMarkdownSection(content, "## Resume From Draft")
	if section == "" {
		t.Fatalf("cross-lane (#880): could not locate '## Resume From Draft' in %s -- this is a hard failure, never a skip", path)
	}

	template := escalationAnchorTemplatePattern.FindString(section)
	if template == "" {
		t.Fatalf("cross-lane (#880): could not extract the documented replacement-nonce marker/nonce template from %s's '## Resume From Draft' section -- this is a hard failure, never a skip. Expected to find the literal template `<!-- cenci-planner-escalation:<nonce> -->` documented there for the re-escalation's fresh replacement nonce.", path)
	}

	newNonce := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	oldNonce := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if !escalationNoncePattern.MatchString(newNonce) || !escalationNoncePattern.MatchString(oldNonce) {
		t.Fatalf("test setup: fixture nonces do not both match escalationNoncePattern %s", escalationNoncePattern.String())
	}
	newRendered := strings.ReplaceAll(template, "<nonce>", newNonce)
	oldRendered := strings.ReplaceAll(template, "<nonce>", oldNonce)
	if newRendered == template || oldRendered == template {
		t.Fatalf("cross-lane (#880): could not render concrete comment bodies from the extracted template %q", template)
	}

	const oldAnchorID = int64(100)
	const newAnchorID = int64(200)

	t.Run("NewAnchorPlusQualifyingReply_Answered", func(t *testing.T) {
		comments := []restIssueComment{
			{ID: oldAnchorID, Body: "Original question.\n" + oldRendered, Author: restCommentAuthor{Login: "matteobortolazzo", Type: "User"}},
			{ID: newAnchorID, Body: "Still-open question.\n" + newRendered, Author: restCommentAuthor{Login: "matteobortolazzo", Type: "User"}},
			{ID: newAnchorID + 1, Body: "Here's my answer.", Author: restCommentAuthor{Login: "octocat", Type: "User"}, AuthorAssociation: "COLLABORATOR"},
		}
		got := classifyComments(comments, newAnchorID, newNonce)
		if got != AnswerProbeAnswered {
			t.Errorf("classifyComments(new anchor + qualifying reply) = %q, want AnswerProbeAnswered", got)
		}
	})

	// A reply positioned only after the OLD anchor (the new anchor is never
	// posted to this particular thread snapshot) must never resume when
	// probed against the persisted (new) anchor identity -- the exact stored
	// comment ID, never a content scan, is the only trusted identity.
	t.Run("ReplyToOldOrphanedAnchor_NeverResumes", func(t *testing.T) {
		comments := []restIssueComment{
			{ID: oldAnchorID, Body: "Original question.\n" + oldRendered, Author: restCommentAuthor{Login: "matteobortolazzo", Type: "User"}},
			{ID: oldAnchorID + 1, Body: "Here's my answer.", Author: restCommentAuthor{Login: "octocat", Type: "User"}, AuthorAssociation: "COLLABORATOR"},
		}
		got := classifyComments(comments, newAnchorID, newNonce)
		if got != AnswerProbeAnchorMismatch {
			t.Errorf("classifyComments(reply to old/orphaned anchor only) = %q, want AnswerProbeAnchorMismatch", got)
		}
	})

	// Nonce-without-ID (#880 Q2's reused representation): a fresh nonce is
	// persisted with escalationCommentId cleared/absent -- anchorID <= 0 --
	// fails closed before the thread is even consulted.
	t.Run("NonceWithoutID_AnchorUnset", func(t *testing.T) {
		comments := []restIssueComment{
			{ID: newAnchorID, Body: "Still-open question.\n" + newRendered, Author: restCommentAuthor{Login: "matteobortolazzo", Type: "User"}},
		}
		got := classifyComments(comments, 0, newNonce)
		if got != AnswerProbeAnchorUnset {
			t.Errorf("classifyComments(nonce without ID) = %q, want AnswerProbeAnchorUnset", got)
		}
	})
}

// extractMarkdownSection returns the body of the named "## <heading>"
// section in content, bounded to the next "## "-level heading (fence-aware:
// a "## " line inside a fenced ``` code block does not end the section) --
// or "" if the heading is not found at all. Pure, no test-framework calls,
// so the caller controls fail-vs-fatal per AC6's hard requirement. Mirrors
// flow/tests/*.test.sh's own extract_*_section awk idiom, ported to Go
// since this is the first Go test in this repo to read a flow/ doc file.
func extractMarkdownSection(content, heading string) string {
	lines := strings.Split(content, "\n")
	var body []string
	on := false
	inFence := false
	for _, l := range lines {
		if !on {
			if strings.TrimRight(l, "\r") == heading {
				on = true
			}
			continue
		}
		if strings.HasPrefix(l, "```") {
			inFence = !inFence
			body = append(body, l)
			continue
		}
		if !inFence && strings.HasPrefix(l, "## ") {
			break
		}
		body = append(body, l)
	}
	if !on {
		return ""
	}
	return strings.Join(body, "\n")
}
