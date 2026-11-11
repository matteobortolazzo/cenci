package babysit

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// -- producer receipt template vs the real parser (#1472) ---------------------
//
// The finite `babysit-review` worker is a markdown skill run by a model, so its
// procedure cannot be executed here. What can be executed is the artifact it is
// told to emit: the fenced receipt template in flow/skills/address-review/
// SKILL.md (Claude Code) and codex.md (Codex). These tests read each template
// from the repository, fill the placeholders with concrete values the way a
// conforming worker would, and run the result through the real parseReceipt and
// verifyReceipt. A drift between a producer's template and this grammar fails
// here, in the language of the consumer. The shell-side twin is
// flow/tests/review-receipt-contract.test.sh, which keeps the literal and gate
// text in sync; the gating itself (when the model takes the finite branch)
// stays contract-only because it is a model decision.

type receiptProducer struct {
	name    string
	file    string
	heading string
}

var receiptProducers = []receiptProducer{
	{"SKILL.md", "SKILL.md", "## Finite Receipt Branch (babysit-review)"},
	{"codex.md", "codex.md", "## Finite receipt branch (babysit-review)"},
}

const (
	templateTestRevision = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	templateTestEntryID  = int64(4242)
)

// readProducer returns the producer's file, read from the repository.
func readProducer(t *testing.T, p receiptProducer) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate the receipt template contract test")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", "flow", "skills", "address-review", p.file))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read producer %s: %v", path, err)
	}
	return string(raw)
}

// producerReceiptTemplate returns the lines of the first ```markdown block in
// the producer's finite-branch section, read from the repository.
func producerReceiptTemplate(t *testing.T, p receiptProducer) string {
	t.Helper()
	raw := readProducer(t, p)

	var (
		inSection, inFence, inTemplate, done bool
		template                             []string
	)
	for _, line := range strings.Split(raw, "\n") {
		if !inSection {
			inSection = line == p.heading
			continue
		}
		fenceLine := strings.HasPrefix(line, "```")
		if !inFence && !fenceLine && strings.HasPrefix(line, "## ") {
			break
		}
		switch {
		case fenceLine && !inFence:
			inFence = true
			inTemplate = !done && strings.TrimRight(line, " \t") == "```markdown"
		case fenceLine && inFence:
			if inTemplate {
				done = true
			}
			inFence, inTemplate = false, false
		case inTemplate:
			template = append(template, line)
		}
	}
	if !done {
		t.Fatalf("%s: no ```markdown receipt template under %q", p.name, p.heading)
	}
	return strings.Join(template, "\n")
}

// fillReceiptTemplate replaces each grammar placeholder with a concrete value.
func fillReceiptTemplate(template, nonce, head, outcome string) string {
	return strings.NewReplacer(
		"<32-hex>", nonce,
		"<40|64-hex>", head,
		"<64-hex>", templateTestRevision,
		"<id>", "4242",
		"<addressed|no-action|blocked|failed>", outcome,
	).Replace(template)
}

func TestProducerReceiptTemplateParsesUnderRealParser(t *testing.T) {
	for _, p := range receiptProducers {
		t.Run(p.name, func(t *testing.T) {
			template := producerReceiptTemplate(t, p)
			body := fillReceiptTemplate(template, testReviewNonce, testReceiptHead, receiptOutcomeAddressed)
			if rest := strings.ReplaceAll(body, receiptMarker(testReviewNonce), ""); strings.Contains(rest, "<") {
				t.Fatalf("unfilled placeholder left in template:\n%s", body)
			}

			got, err := parseReceipt(body)
			if err != nil {
				t.Fatalf("parseReceipt rejected the producer's template: %v\n%s", err, body)
			}
			if got.Nonce != testReviewNonce || got.HeadSHA != testReceiptHead {
				t.Fatalf("nonce/head = %q/%q, want %q/%q", got.Nonce, got.HeadSHA, testReviewNonce, testReceiptHead)
			}
			want := []receiptEntry{{ID: templateTestEntryID, Revision: templateTestRevision, Outcome: receiptOutcomeAddressed}}
			if len(got.Entries) != 1 || got.Entries[0] != want[0] {
				t.Fatalf("entries = %+v, want %+v", got.Entries, want)
			}
		})
	}
}

// A conforming worker may post a receipt with zero discussion lines (a launch
// carrying only inline or review feedback): dropping the template's entry line
// must still parse.
func TestProducerReceiptTemplateWithoutEntryParsesWithZeroEntries(t *testing.T) {
	for _, p := range receiptProducers {
		t.Run(p.name, func(t *testing.T) {
			body := fillReceiptTemplate(producerReceiptTemplate(t, p), testReviewNonce, testReceiptHead, receiptOutcomeAddressed)
			got, err := parseReceipt(dropLinesWithPrefix(body, receiptEntryPrefix))
			if err != nil {
				t.Fatalf("zero-entry receipt rejected: %v", err)
			}
			if len(got.Entries) != 0 || got.Nonce != testReviewNonce {
				t.Fatalf("got %+v, want nonce %q and no entries", got, testReviewNonce)
			}
		})
	}
}

// The filled template, posted as the launch's receipt comment, is accepted by
// verifyReceipt and moves the key to the bucket its outcome names.
func TestProducerReceiptTemplateIsAcceptedAndBucketedByVerifyReceipt(t *testing.T) {
	key := discussionKey(templateTestEntryID)
	cases := []struct {
		outcome        string
		wantAddressed  bool
		wantUnresolved bool
	}{
		{receiptOutcomeAddressed, true, false},
		{receiptOutcomeNoAction, true, false},
		{receiptOutcomeBlocked, false, true},
		{receiptOutcomeFailed, false, true},
	}
	for _, p := range receiptProducers {
		template := producerReceiptTemplate(t, p)
		for _, tc := range cases {
			t.Run(p.name+"/"+tc.outcome, func(t *testing.T) {
				launch := &reviewLaunchRecord{Nonce: testReviewNonce, LaunchedAt: testLaunchAt, Keys: []string{key}, TrustedKeys: []string{key}}
				created := testLaunchAt.Add(time.Minute).UTC().Format(time.RFC3339)
				comment := discussionComment{
					ID:        9001,
					Login:     "reviewer",
					Body:      fillReceiptTemplate(template, testReviewNonce, testReceiptHead, tc.outcome),
					CreatedAt: created,
					UpdatedAt: created,
				}
				revisions := map[string]string{key: templateTestRevision}

				res := verifyReceipt(launch, nil, []discussionComment{comment}, revisions, allowAll)
				if !res.Accepted {
					t.Fatalf("receipt not accepted: %+v", res.Rejections)
				}
				if res.HeadSHA != testReceiptHead || res.ReceiptCommentID != comment.ID {
					t.Fatalf("head/comment = %q/%d, want %q/%d", res.HeadSHA, res.ReceiptCommentID, testReceiptHead, comment.ID)
				}
				if _, ok := res.Addressed[key]; ok != tc.wantAddressed {
					t.Fatalf("Addressed has %s = %v, want %v", key, ok, tc.wantAddressed)
				}
				if _, ok := res.Unresolved[key]; ok != tc.wantUnresolved {
					t.Fatalf("Unresolved has %s = %v, want %v", key, ok, tc.wantUnresolved)
				}
			})
		}
	}
}

// The parser is strict, so the template check above can fail: each mutation of
// the producer's template breaks one rule of the grammar and must be refused.
func TestProducerReceiptTemplateMutationsAreRejected(t *testing.T) {
	for _, p := range receiptProducers {
		template := producerReceiptTemplate(t, p)
		filled := fillReceiptTemplate(template, testReviewNonce, testReceiptHead, receiptOutcomeAddressed)

		mutations := []struct {
			name    string
			body    string
			wantErr error
		}{
			{"short nonce", fillReceiptTemplate(template, testReviewNonce[:31], testReceiptHead, receiptOutcomeAddressed), errReceiptMalformed},
			{"short head", fillReceiptTemplate(template, testReviewNonce, testReceiptHead[:39], receiptOutcomeAddressed), errReceiptMalformed},
			{"unknown outcome", fillReceiptTemplate(template, testReviewNonce, testReceiptHead, "done"), errReceiptMalformed},
			{"missing head", dropLinesWithPrefix(filled, receiptHeadPrefix), errReceiptMalformed},
			{"duplicate key", filled + "\n" + lastLineWithPrefix(filled, receiptEntryPrefix), errReceiptMalformed},
			{"duplicate marker", filled + "\n" + receiptMarker(testReviewNonce), errReceiptMalformed},
			{"blockquoted marker", strings.Replace(filled, receiptMarker(testReviewNonce), "> "+receiptMarker(testReviewNonce), 1), errNotReceipt},
		}
		for _, m := range mutations {
			t.Run(p.name+"/"+m.name, func(t *testing.T) {
				if m.body == filled {
					t.Fatal("mutation did not change the template (test bug)")
				}
				if _, err := parseReceipt(m.body); !errors.Is(err, m.wantErr) {
					t.Fatalf("parseReceipt error = %v, want %v", err, m.wantErr)
				}
			})
		}
	}
}

// The token validation each producer states (the `^discussion:...$` pattern in its
// Arguments paragraph) must accept every token the supervisor puts in a launch
// argument, with and without the `:untrusted` suffix, and refuse a malformed one:
// a drift between the supervisor's argument and a producer's regex would make the
// worker reject its own launch and post nothing.
func TestProducerLaunchTokenGrammarAcceptsTheSupervisorsTokens(t *testing.T) {
	tokenPattern := regexp.MustCompile("`(\\^discussion:[^`]+\\$)`")
	s := &State{PR: "42", DiscussionRevisions: map[string]string{
		"discussion:11": bodyRevision("trusted body"),
		"discussion:12": bodyRevision("untrusted body"),
	}}
	arg := reviewLaunchArg(s, testReviewNonce, []string{"discussion:11", "discussion:12"}, []string{"discussion:11"})
	tokens := strings.Fields(arg)[3:]
	if len(tokens) != 2 || strings.HasSuffix(tokens[0], untrustedSuffix) || !strings.HasSuffix(tokens[1], untrustedSuffix) {
		t.Fatalf("launch tokens = %q, want a bare token then an :untrusted one", tokens)
	}
	for _, p := range receiptProducers {
		t.Run(p.name, func(t *testing.T) {
			m := tokenPattern.FindStringSubmatch(readProducer(t, p))
			if m == nil {
				t.Fatalf("%s: no `^discussion:...$` token pattern found", p.name)
			}
			re, err := regexp.Compile(m[1])
			if err != nil {
				t.Fatalf("%s: token pattern %q does not compile: %v", p.name, m[1], err)
			}
			for _, tok := range tokens {
				if !re.MatchString(tok) {
					t.Errorf("%s: pattern %q refuses the supervisor's token %q", p.name, m[1], tok)
				}
			}
			for _, bad := range []string{tokens[0] + ":trusted", tokens[0] + ":untrusted:untrusted", "discussion:11=short:untrusted", "discussion:x=" + bodyRevision("x")} {
				if re.MatchString(bad) {
					t.Errorf("%s: pattern %q accepts the malformed token %q", p.name, m[1], bad)
				}
			}
		})
	}
}

func dropLinesWithPrefix(body, prefix string) string {
	var kept []string
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(l, prefix) {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

func lastLineWithPrefix(body, prefix string) string {
	last := ""
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, prefix) {
			last = l
		}
	}
	return last
}
