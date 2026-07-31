package babysit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// -- test helpers -------------------------------------------------------------

// intp returns a pointer to n, for policyBlock's pointer-typed cap fields.
func intp(n int) *int { return &n }

// greenAutomergeInputs returns an automergeInputs value that satisfies every
// stage of the condition chain -- the starting point for the condition-matrix
// table below, which mutates exactly one field per case to isolate a single
// reason. A fresh struct (with its own *effectivePolicy) is returned on every
// call so mutating a subtest's copy never leaks into another subtest (#822 --
// a shared pointer would let one case's mutation silently corrupt another's).
func greenAutomergeInputs() automergeInputs {
	return automergeInputs{
		Enabled:       true,
		ClosingIssues: []int{9},
		IssueLabels:   map[int][]string{9: {"automerge:ok"}},
		CIStatus:      "green",
		RepairPending: false,
		PendingKeys:   nil,
		IsDraft:       false,
		Mergeable:     "MERGEABLE",
		HeadRefOID:    "abc",
		ChangedFiles:  2,
		Additions:     10,
		Deletions:     5,
		Files: []string{
			"watch/internal/babysit/automerge.go",
			"watch/internal/babysit/automerge_test.go",
		},
		Policy: &effectivePolicy{
			ProtectedPaths:  []string{"*secret*"},
			MaxChangedFiles: 10,
			MaxDiffLines:    500,
			MergeMethod:     "squash",
		},
		MergeMethod:    "squash",
		AllowedMethods: map[string]bool{"squash": true, "merge": false, "rebase": true},
	}
}

// withScriptedCommands replaces the package's command seam for the test's
// duration with one that serves script in order for every "gh" invocation
// (each entry providing both an output body and an error, so a rejected `gh
// pr merge` can be modeled -- unlike babysit_test.go's withCommands, which
// always returns a nil error). Every invocation, gh or not, is recorded into
// calls.
func withScriptedCommands(t *testing.T, script []scriptedCall, calls *[][]string) {
	t.Helper()
	original := command
	i := 0
	command = func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		if name != "gh" {
			return []byte(""), nil
		}
		if i >= len(script) {
			return nil, fmt.Errorf("unexpected command: %s", strings.Join(args, " "))
		}
		c := script[i]
		i++
		return []byte(c.out), c.err
	}
	t.Cleanup(func() { command = original })
}

type scriptedCall struct {
	out string
	err error
}

// withFleetAutomergeEnabled points the fleetConfigPath seam at a temp fleet
// config file with automerge.enabled set to enabled, restoring the previous
// seam value (the package TestMain's "" pin) on cleanup.
func withFleetAutomergeEnabled(t *testing.T, enabled bool) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := fmt.Sprintf(`{"automerge":{"enabled":%v}}`, enabled)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	original := fleetConfigPath
	fleetConfigPath = func() string { return path }
	t.Cleanup(func() { fleetConfigPath = original })
}

// automergeEligiblePR is a `pr view` fixture with every field the automerge
// gate chain needs already at its green value: not a draft, MERGEABLE, one
// changed file matching changedFiles (no truncation), and a base ref distinct
// from the head ref -- so a test asserting the policy fetch used baseRefName
// (never headRefName) has something to actually distinguish.
func automergeEligiblePR() string {
	return `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"abc","baseRefName":"main","mergeable":"MERGEABLE","isDraft":false,"changedFiles":1,"additions":5,"deletions":2,"files":[{"path":"watch/internal/babysit/x.go"}],"url":"https://example/pr/42","closingIssuesReferences":[{"number":9}]}`
}

// hasArg reports whether call contains arg anywhere after the command name.
func hasArg(call []string, arg string) bool {
	for _, a := range call {
		if a == arg {
			return true
		}
	}
	return false
}

// argValue returns the value immediately following the first occurrence of
// flag in call, and whether flag was found at all -- for asserting an exact
// flag=value pair (e.g. "--match-head-commit abc") rather than just the
// flag's bare presence.
func argValue(call []string, flag string) (string, bool) {
	for i, a := range call {
		if a == flag && i+1 < len(call) {
			return call[i+1], true
		}
	}
	return "", false
}

// -- evaluateAutomerge: condition matrix (#824) ------------------------------
//
// One case per reason constant in the condition chain, each starting from an
// all-green automergeInputs and mutating exactly one field so the case
// isolates that single reason -- per watch/docs/error-handling.md #446, the
// assertion is the exact reason string, never a bare "denied" check, so a
// regression collapsing two failure classes into the same reason is caught.
func TestEvaluateAutomergeConditionMatrix(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(*automergeInputs)
		wantReason string
	}{
		{"fleet automerge disabled", func(in *automergeInputs) { in.Enabled = false }, reasonAutomergeDisabled},
		{"no closing issue", func(in *automergeInputs) { in.ClosingIssues = nil }, reasonNoClosingIssue},
		{"closing issue labels unreadable", func(in *automergeInputs) { in.LabelsErr = errors.New("boom") }, reasonLabelUnreadable},
		{"closing issue lacks automerge:ok", func(in *automergeInputs) { in.IssueLabels = map[int][]string{9: {"bug"}} }, reasonLabelMissing},
		{"no CI checks at all", func(in *automergeInputs) { in.CIStatus = "" }, reasonNoChecks},
		{"CI not green", func(in *automergeInputs) { in.CIStatus = "pending" }, reasonCINotGreen},
		{"CI repair pending", func(in *automergeInputs) { in.RepairPending = true }, reasonRepairPending},
		{"review feedback pending", func(in *automergeInputs) { in.PendingKeys = []string{"comment:1"} }, reasonReviewPending},
		// The four new #850 feedback-hold reasons: FeedbackHold is checked
		// after RepairPending and before len(PendingKeys), so any of these
		// four never masquerades as the ordinary reasonReviewPending hold.
		{"review feedback state unreadable (API error)", func(in *automergeInputs) { in.FeedbackHold = reasonFeedbackUnreadable }, reasonFeedbackUnreadable},
		{"review feedback state truncated (incomplete pagination)", func(in *automergeInputs) { in.FeedbackHold = reasonFeedbackTruncated }, reasonFeedbackTruncated},
		{"review feedback state unknown (absent thread/review or unknown review state)", func(in *automergeInputs) { in.FeedbackHold = reasonReviewStateUnknown }, reasonReviewStateUnknown},
		{"unsupported review feedback type", func(in *automergeInputs) { in.FeedbackHold = reasonFeedbackUnsupported }, reasonFeedbackUnsupported},
		{"draft PR", func(in *automergeInputs) { in.IsDraft = true }, reasonDraft},
		{"mergeable state UNKNOWN", func(in *automergeInputs) { in.Mergeable = "UNKNOWN" }, reasonMergeableUnknown},
		{"mergeable state CONFLICTING", func(in *automergeInputs) { in.Mergeable = "CONFLICTING" }, reasonNotMergeable},
		{"head commit SHA unknown", func(in *automergeInputs) { in.HeadRefOID = "" }, reasonHeadSHAUnknown},
		{"no changed files", func(in *automergeInputs) { in.ChangedFiles = 0 }, reasonNoChanges},
		{"diff file list truncated", func(in *automergeInputs) { in.Files = in.Files[:1] }, reasonDiffTruncated},
		{"policy unreadable", func(in *automergeInputs) { in.PolicyErr = errors.New("boom") }, reasonPolicyUnreadable},
		{"policy absent", func(in *automergeInputs) { in.Policy = nil; in.PolicyReason = reasonPolicyAbsent }, reasonPolicyAbsent},
		{"policy malformed", func(in *automergeInputs) { in.Policy = nil; in.PolicyReason = reasonPolicyMalformed }, reasonPolicyMalformed},
		{"too many changed files", func(in *automergeInputs) { in.Policy.MaxChangedFiles = 1 }, reasonTooManyFiles},
		{"too many changed lines", func(in *automergeInputs) { in.Policy.MaxDiffLines = 1 }, reasonTooManyLines},
		{"touches a protected path", func(in *automergeInputs) {
			in.Policy.ProtectedPaths = []string{"watch/internal/babysit/*"}
		}, reasonProtectedPath},
		{"merge method disallowed", func(in *automergeInputs) { in.MergeMethod = "merge" }, reasonMergeMethodDisallowed},
		{"repo allowed merge methods unknown", func(in *automergeInputs) {
			in.AllowedMethodsErr = errors.New("boom")
		}, reasonMergeMethodUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := greenAutomergeInputs()
			tc.mutate(&in)
			got := evaluateAutomerge(in)
			if got.Merge {
				t.Fatalf("evaluateAutomerge(%s).Merge = true, want false", tc.name)
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("evaluateAutomerge(%s).Reason = %q, want %q", tc.name, got.Reason, tc.wantReason)
			}
		})
	}
}

// TestEvaluateAutomergeRequiresEveryClosingIssueLabeled pins the "all" in
// "all closing issues carry automerge:ok" -- a PR closing two issues where
// only one carries the grant must still be denied, not merged on a
// first-match basis.
func TestEvaluateAutomergeRequiresEveryClosingIssueLabeled(t *testing.T) {
	in := greenAutomergeInputs()
	in.ClosingIssues = []int{9, 10}
	in.IssueLabels = map[int][]string{9: {"automerge:ok"}, 10: {"bug"}}
	got := evaluateAutomerge(in)
	if got.Merge {
		t.Fatal("Merge = true, want false: issue #10 lacks automerge:ok")
	}
	if got.Reason != reasonLabelMissing {
		t.Fatalf("Reason = %q, want %q", got.Reason, reasonLabelMissing)
	}
}

// TestEvaluateAutomergeAllGreenMerges is the matrix's counterpart all-pass
// case: Merge is true and every stage's condition reports Reached && Pass, so
// the full-verdict Conditions slice used by logLine's [k=v ...] rendering is
// actually populated end to end, not just up to the first (nonexistent)
// failure.
func TestEvaluateAutomergeAllGreenMerges(t *testing.T) {
	got := evaluateAutomerge(greenAutomergeInputs())
	if !got.Merge {
		t.Fatalf("Merge = false (reason %q), want true", got.Reason)
	}
	wantStages := []string{"enabled", "label", "ci", "review", "mergeable", "headsha", "files", "policy", "filecap", "lines", "protected", "method"}
	seen := map[string]bool{}
	for _, c := range got.Conditions {
		if !c.Reached || !c.Pass {
			t.Errorf("condition %q = %+v, want Reached=true Pass=true on the all-green path", c.Key, c)
		}
		seen[c.Key] = true
	}
	for _, k := range wantStages {
		if !seen[k] {
			t.Errorf("Conditions never recorded stage %q: %#v", k, got.Conditions)
		}
	}
}

// TestEvaluateAutomergeTooManyFilesRendersDistinctFilecapKey pins the fix for
// a log-rendering bug: stage 8's file-count cap check used to reuse stage 6's
// "files" key, so a hold on reasonTooManyFiles still rendered "files=yes" in
// the log line (stage 6's earlier passing entry won conditionSymbol's
// first-match lookup). The cap check must render under its own "filecap" key
// so the log line accurately reflects which check actually failed.
func TestEvaluateAutomergeTooManyFilesRendersDistinctFilecapKey(t *testing.T) {
	in := greenAutomergeInputs()
	in.Policy.MaxChangedFiles = 1
	got := evaluateAutomerge(in)
	if got.Reason != reasonTooManyFiles {
		t.Fatalf("Reason = %q, want %q", got.Reason, reasonTooManyFiles)
	}
	line := got.logLine()
	if !strings.Contains(line, "filecap=no") {
		t.Errorf("logLine() = %q, want it to contain %q", line, "filecap=no")
	}
	if !strings.Contains(line, "files=yes") {
		t.Errorf("logLine() = %q, want it to still contain %q (stage 6's changedFiles>0/no-truncation check passed)", line, "files=yes")
	}
}

// TestEvaluateAutomergeSurfacesFetchErrorDetail pins that the three wrapped
// fetch errors (LabelsErr/PolicyErr/AllowedMethodsErr) -- which already wrap
// the underlying `gh` output via %w -- have their .Error() text surfaced onto
// the decision's Detail field, not silently collapsed into just the generic
// reason constant.
func TestEvaluateAutomergeSurfacesFetchErrorDetail(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(*automergeInputs)
		wantReason string
		detail     func(automergeInputs) string
	}{
		{
			"closing issue labels unreadable surfaces detail",
			func(in *automergeInputs) { in.LabelsErr = errors.New("gh issue view: exit status 1: rate limited") },
			reasonLabelUnreadable,
			func(in automergeInputs) string { return in.LabelsErr.Error() },
		},
		{
			"policy unreadable surfaces detail",
			func(in *automergeInputs) { in.PolicyErr = errors.New("automerge policy unreadable: 404 Not Found") },
			reasonPolicyUnreadable,
			func(in automergeInputs) string { return in.PolicyErr.Error() },
		},
		{
			"allowed merge methods unreadable surfaces detail",
			func(in *automergeInputs) {
				in.AllowedMethodsErr = errors.New("gh api repos/o/r: exit status 1: auth failure")
			},
			reasonMergeMethodUnknown,
			func(in automergeInputs) string { return in.AllowedMethodsErr.Error() },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := greenAutomergeInputs()
			tc.mutate(&in)
			got := evaluateAutomerge(in)
			if got.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			wantDetail := tc.detail(in)
			if got.Detail != wantDetail {
				t.Fatalf("Detail = %q, want %q", got.Detail, wantDetail)
			}
		})
	}
}

// TestEvaluateAutomergeSanitizesMultilineAndLongDetail pins that a raw `gh`
// failure message -- with embedded newlines, or exceeding the bounded length
// -- is sanitized before landing on Detail (newlines collapsed to spaces,
// truncated with a "..." suffix), and that the resulting log line stays a
// single line: Detail is raw, unbounded `gh` output, but it feeds logLine's
// single-line-per-tick format that downstream tooling substring-classifies.
func TestEvaluateAutomergeSanitizesMultilineAndLongDetail(t *testing.T) {
	in := greenAutomergeInputs()
	longTail := strings.Repeat("x", 250)
	in.LabelsErr = fmt.Errorf("boom:\nsecond line\nthird line: %s", longTail)
	got := evaluateAutomerge(in)
	if got.Reason != reasonLabelUnreadable {
		t.Fatalf("Reason = %q, want %q", got.Reason, reasonLabelUnreadable)
	}
	if strings.Contains(got.Detail, "\n") {
		t.Fatalf("Detail = %q, must not contain an embedded newline", got.Detail)
	}
	if len(got.Detail) > detailMaxLen+len("...") {
		t.Fatalf("Detail length = %d, want capped at roughly %d", len(got.Detail), detailMaxLen)
	}
	if !strings.HasSuffix(got.Detail, "...") {
		t.Fatalf("Detail = %q, want a truncated value to end with \"...\"", got.Detail)
	}
	line := got.logLine()
	if strings.Contains(line, "\n") {
		t.Fatalf("logLine() = %q, must render as a single line", line)
	}
}

// -- automergeDecision.logLine (#824) ----------------------------------------

// TestAutomergeDecisionLogLine pins the exact one-line log format from the
// plan, including the lazyboards-safe substrings check (mainsync.go:217-219's
// rule applied to this new log line).
func TestAutomergeDecisionLogLine(t *testing.T) {
	d := automergeDecision{
		PR:     "42",
		Merge:  false,
		Reason: reasonLabelMissing,
		Conditions: []conditionResult{
			{Key: "enabled", Reached: true, Pass: true},
			{Key: "label", Reached: true, Pass: false},
		},
	}
	want := `babysit: automerge PR #42 held: ticket lacks automerge:ok [enabled=yes label=no ci=- review=- mergeable=- headsha=- policy=- files=- filecap=- lines=- protected=- method=-]`
	got := d.logLine()
	if got != want {
		t.Fatalf("logLine() = %q, want %q", got, want)
	}
	if strings.Contains(got, " skip:") || strings.Contains(got, " dispatch ") {
		t.Errorf("logLine() must avoid lazyboards' classification substrings: %q", got)
	}
}

// TestAutomergeDecisionLogLineIncludesDetail pins that a non-empty Detail
// (e.g. a rejected merge's captured `gh` output, or a wrapped fetch error's
// message) is surfaced in the rendered log line alongside the stable Reason
// constant, rather than discarded.
func TestAutomergeDecisionLogLineIncludesDetail(t *testing.T) {
	d := automergeDecision{
		PR:     "42",
		Merge:  false,
		Reason: reasonMergeFailed,
		Detail: "branch protection required review",
		Conditions: []conditionResult{
			{Key: "enabled", Reached: true, Pass: true},
		},
	}
	got := d.logLine()
	if !strings.Contains(got, reasonMergeFailed) || !strings.Contains(got, "branch protection required review") {
		t.Fatalf("logLine() = %q, want it to contain both the reason and the detail", got)
	}
}

// -- matchesProtected (#824) --------------------------------------------------
//
// Unit tests: an integration test through tick cannot economically cover
// these glob edge cases (Test Strategy table).
func TestMatchesProtected(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern string
		path    string
		want    bool
		wantErr bool
	}{
		{"exact literal match", "watch/internal/sandbox/", "watch/internal/sandbox/", true, false},
		{"star spans the path separator", "watch/internal/sandbox/*", "watch/internal/sandbox/deep/nested/file.go", true, false},
		{"case-insensitive", "*SECRET*", "path/to/secret.txt", true, false},
		{"metacharacters are escaped: literal dot matches", "config.json", "config.json", true, false},
		{"metacharacters are escaped: dot never matches any char", "config.json", "configXjson", false, false},
		{"no match", "*credential*", "watch/internal/sandbox/launch.go", false, false},
		{"empty pattern is malformed", "", "any/path.go", false, true},
		// A bare directory-prefix entry (trailing "/", no "*") must match
		// everything under it, not just the exact literal string -- the
		// shipped .cenci/config.json ships entries in exactly this shape
		// ("watch/internal/sandbox/", "watch/plugin/"), and without this fix
		// they can only ever match that one literal path.
		{"directory-prefix pattern matches a nested file", "watch/internal/sandbox/", "watch/internal/sandbox/launch.go", true, false},
		{"directory-prefix pattern still matches its own literal path", "watch/plugin/", "watch/plugin/", true, false},
		{"directory-prefix pattern does not match a sibling directory", "watch/plugin/", "watch/plugins/x.go", false, false},
		// A "*"-wildcard pattern must span an embedded newline in the path --
		// without the "s" (dot-matches-\n) regex flag, an attacker-
		// controlled changed-file path containing "\n" could dodge every
		// "*"-wildcard protected-path pattern.
		{"star wildcard spans an embedded newline", "watch/internal/sandbox/*", "watch/internal/sandbox/deep\nnested/file.go", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := matchesProtected(tc.pattern, tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("matchesProtected(%q, %q) err = nil, want an error (malformed pattern)", tc.pattern, tc.path)
				}
				return
			}
			if err != nil {
				t.Fatalf("matchesProtected(%q, %q) unexpected err: %v", tc.pattern, tc.path, err)
			}
			if got != tc.want {
				t.Errorf("matchesProtected(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

// -- resolvePolicy (#824) -----------------------------------------------------
//
// Unit tests: monorepo prefix mapping, most-restrictive merge, mergeMethod
// conflict, malformed-cap detection, and absent-block-denies (Q2) are
// combinatorial and unreachable one-at-a-time through tick (Test Strategy
// table).
func TestResolvePolicyProjectPrefixMapping(t *testing.T) {
	cfg := repoConfigFile{
		Automerge: &policyBlock{MaxChangedFiles: intp(100), MaxDiffLines: intp(1000), MergeMethod: "squash"},
		Projects: []projectPolicy{
			{Path: "watch", Automerge: &policyBlock{MaxChangedFiles: intp(50), MaxDiffLines: intp(500), MergeMethod: "squash"}},
			{Path: "watch/internal/sandbox", Automerge: &policyBlock{MaxChangedFiles: intp(1), MaxDiffLines: intp(10), MergeMethod: "squash"}},
		},
	}

	t.Run("longest project prefix wins over a shorter one", func(t *testing.T) {
		got, reason := resolvePolicy(cfg, []string{"watch/internal/sandbox/launch.go"})
		if reason != "" {
			t.Fatalf("resolvePolicy reason = %q, want none", reason)
		}
		if got.MaxChangedFiles != 1 {
			t.Errorf("MaxChangedFiles = %d, want 1 (the longest-prefix project block)", got.MaxChangedFiles)
		}
	})

	t.Run("a project-owned file uses its own block over the top-level fallback", func(t *testing.T) {
		got, reason := resolvePolicy(cfg, []string{"watch/daemon_cmd.go"})
		if reason != "" {
			t.Fatalf("resolvePolicy reason = %q, want none", reason)
		}
		if got.MaxChangedFiles != 50 {
			t.Errorf("MaxChangedFiles = %d, want 50 (the watch project block)", got.MaxChangedFiles)
		}
	})

	t.Run("a file owned by no project falls back to the top-level block", func(t *testing.T) {
		got, reason := resolvePolicy(cfg, []string{"README.md"})
		if reason != "" {
			t.Fatalf("resolvePolicy reason = %q, want none", reason)
		}
		if got.MaxChangedFiles != 100 {
			t.Errorf("MaxChangedFiles = %d, want 100 (the top-level block)", got.MaxChangedFiles)
		}
	})
}

func TestResolvePolicyMostRestrictiveMerge(t *testing.T) {
	cfg := repoConfigFile{
		Projects: []projectPolicy{
			{Path: "a", Automerge: &policyBlock{ProtectedPaths: []string{"*secretA*"}, MaxChangedFiles: intp(10), MaxDiffLines: intp(100), MergeMethod: "squash"}},
			{Path: "b", Automerge: &policyBlock{ProtectedPaths: []string{"*secretB*"}, MaxChangedFiles: intp(5), MaxDiffLines: intp(500), MergeMethod: "squash"}},
		},
	}
	got, reason := resolvePolicy(cfg, []string{"a/x.go", "b/y.go"})
	if reason != "" {
		t.Fatalf("resolvePolicy reason = %q, want none", reason)
	}
	if got.MaxChangedFiles != 5 {
		t.Errorf("MaxChangedFiles = %d, want the min across applicable blocks (5)", got.MaxChangedFiles)
	}
	if got.MaxDiffLines != 100 {
		t.Errorf("MaxDiffLines = %d, want the min across applicable blocks (100)", got.MaxDiffLines)
	}
	wantPaths := map[string]bool{"*secretA*": true, "*secretB*": true}
	if len(got.ProtectedPaths) != len(wantPaths) {
		t.Fatalf("ProtectedPaths = %v, want the union of both blocks (%v)", got.ProtectedPaths, wantPaths)
	}
	for _, p := range got.ProtectedPaths {
		if !wantPaths[p] {
			t.Errorf("ProtectedPaths contains unexpected pattern %q", p)
		}
	}
}

func TestResolvePolicyConflictingMergeMethodIsMalformed(t *testing.T) {
	cfg := repoConfigFile{
		Projects: []projectPolicy{
			{Path: "a", Automerge: &policyBlock{MaxChangedFiles: intp(10), MaxDiffLines: intp(100), MergeMethod: "squash"}},
			{Path: "b", Automerge: &policyBlock{MaxChangedFiles: intp(10), MaxDiffLines: intp(100), MergeMethod: "rebase"}},
		},
	}
	_, reason := resolvePolicy(cfg, []string{"a/x.go", "b/y.go"})
	if reason != reasonPolicyMalformed {
		t.Fatalf("resolvePolicy reason = %q, want %q", reason, reasonPolicyMalformed)
	}
}

func TestResolvePolicyMalformedCapDetection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block policyBlock
	}{
		{"maxChangedFiles absent", policyBlock{MaxDiffLines: intp(100)}},
		{"maxChangedFiles zero", policyBlock{MaxChangedFiles: intp(0), MaxDiffLines: intp(100)}},
		{"maxChangedFiles negative", policyBlock{MaxChangedFiles: intp(-1), MaxDiffLines: intp(100)}},
		{"maxDiffLines absent", policyBlock{MaxChangedFiles: intp(10)}},
		{"maxDiffLines zero", policyBlock{MaxChangedFiles: intp(10), MaxDiffLines: intp(0)}},
		{"empty protected-path pattern", policyBlock{MaxChangedFiles: intp(10), MaxDiffLines: intp(100), ProtectedPaths: []string{""}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := repoConfigFile{Automerge: &tc.block}
			_, reason := resolvePolicy(cfg, []string{"any/file.go"})
			if reason != reasonPolicyMalformed {
				t.Fatalf("resolvePolicy reason = %q, want %q", reason, reasonPolicyMalformed)
			}
		})
	}
}

func TestResolvePolicyAbsentBlockDenies(t *testing.T) {
	t.Run("no automerge key anywhere (Q2: no fallback to built-in defaults)", func(t *testing.T) {
		_, reason := resolvePolicy(repoConfigFile{}, []string{"any/file.go"})
		if reason != reasonPolicyAbsent {
			t.Fatalf("resolvePolicy reason = %q, want %q", reason, reasonPolicyAbsent)
		}
	})
	t.Run("touched project has no automerge block and there is no top-level fallback", func(t *testing.T) {
		cfg := repoConfigFile{Projects: []projectPolicy{{Path: "watch"}}}
		_, reason := resolvePolicy(cfg, []string{"watch/x.go"})
		if reason != reasonPolicyAbsent {
			t.Fatalf("resolvePolicy reason = %q, want %q", reason, reasonPolicyAbsent)
		}
	})
}

// -- fetchPolicy (#824) -------------------------------------------------------

// TestFetchPolicyUsesBaseRefNotHeadRef pins the exact invocation shape (Q1):
// the base ref, never the head ref -- a PR must never be able to widen its
// own policy to self-approve by editing .cenci/config.json on its own branch.
func TestFetchPolicyUsesBaseRefNotHeadRef(t *testing.T) {
	var calls [][]string
	original := command
	command = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return []byte(`{"automerge":{"maxChangedFiles":5,"maxDiffLines":200,"mergeMethod":"squash"}}`), nil
	}
	t.Cleanup(func() { command = original })

	if _, err := fetchPolicy("o/r", "main"); err != nil {
		t.Fatalf("fetchPolicy: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want exactly 1", calls)
	}
	want := []string{"gh", "api", "-H", "Accept: application/vnd.github.raw", "repos/o/r/contents/.cenci/config.json?ref=main"}
	if !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("fetchPolicy invocation = %#v, want %#v", calls[0], want)
	}
}

// TestFetchPolicyEscapesBaseRef pins that a baseRef containing characters
// that are legal in a git ref but reserved in a URL query string ("#", "&")
// is query-escaped before interpolation -- raw string concatenation would
// otherwise corrupt the query string (e.g. truncate it at "#" or append a
// bogus second query parameter at "&").
func TestFetchPolicyEscapesBaseRef(t *testing.T) {
	var calls [][]string
	original := command
	command = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return []byte(`{"automerge":{"maxChangedFiles":5,"maxDiffLines":200,"mergeMethod":"squash"}}`), nil
	}
	t.Cleanup(func() { command = original })

	if _, err := fetchPolicy("o/r", "feature#1&x"); err != nil {
		t.Fatalf("fetchPolicy: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want exactly 1", calls)
	}
	want := []string{"gh", "api", "-H", "Accept: application/vnd.github.raw", "repos/o/r/contents/.cenci/config.json?ref=feature%231%26x"}
	if !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("fetchPolicy invocation = %#v, want %#v", calls[0], want)
	}
}

func TestFetchPolicyNonJSONBodyIsUnreadable(t *testing.T) {
	original := command
	command = func(string, ...string) ([]byte, error) { return []byte("not json"), nil }
	t.Cleanup(func() { command = original })
	_, err := fetchPolicy("o/r", "main")
	if err == nil || !strings.Contains(err.Error(), reasonPolicyUnreadable) {
		t.Fatalf("fetchPolicy non-JSON body: err = %v, want an error containing %q", err, reasonPolicyUnreadable)
	}
}

// -- tick automerge wiring (#824) ---------------------------------------------

// TestTickIssuesAutomergeWhenGreenLabeledAndWithinPolicy is test-strategy
// case (a): green + label + policy ⇒ gh pr merge --squash issued, actionable
// is set (the backoff delay resets to the interval), and the untouched
// MERGED-branch relabel does not fire inline (it fires on the *next* tick,
// once `pr view` itself reports MERGED).
func TestTickIssuesAutomergeWhenGreenLabeledAndWithinPolicy(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{out: automergeEligiblePR()},                                 // pr view
		{out: `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`}, // pr checks
		{out: `[]`}, // comments
		{out: `[]`}, // reviews
		{out: `{"labels":[{"name":"automerge:ok"}]}`},                                           // closing issue #9 labels
		{out: `{"automerge":{"maxChangedFiles":10,"maxDiffLines":500,"mergeMethod":"squash"}}`}, // base-ref policy
		{out: `{"squash":true,"merge":false,"rebase":true}`},                                    // repo allowed-methods probe
		{out: "Merged pull request #42 (o/r)"},                                                  // gh pr merge
	}, &calls)

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 900}
	terminal, delay, err := tick(&s)
	if err != nil {
		t.Fatal(err)
	}
	if terminal {
		t.Fatal("tick must not be terminal on the merge-issuing tick itself")
	}
	if delay != 60*time.Second || s.CurrentDelaySeconds != 60 {
		t.Fatalf("a successful merge must reset the backoff delay to the interval: delay=%v state=%d", delay, s.CurrentDelaySeconds)
	}

	var mergeCall, relabelCall []string
	for _, c := range calls {
		if len(c) > 2 && c[1] == "pr" && c[2] == "merge" {
			mergeCall = c
		}
		if len(c) > 3 && c[1] == "issue" && c[2] == "edit" && c[3] == "9" {
			relabelCall = c
		}
	}
	if mergeCall == nil {
		t.Fatalf("gh pr merge was not issued: %#v", calls)
	}
	if !hasArg(mergeCall, "--squash") {
		t.Errorf("merge call = %#v, want --squash", mergeCall)
	}
	if hasArg(mergeCall, "--delete-branch") {
		t.Errorf("merge call = %#v, must never pass --delete-branch", mergeCall)
	}
	// TOCTOU pin: the merge must be pinned to the exact head commit
	// evaluateAutomerge actually evaluated (automergeEligiblePR's
	// headRefOid "abc"), so a push landing between evaluation and merge can
	// never get content merged that was never checked.
	if v, ok := argValue(mergeCall, "--match-head-commit"); !ok || v != "abc" {
		t.Errorf("merge call = %#v, want --match-head-commit abc", mergeCall)
	}
	if relabelCall != nil {
		t.Fatalf("the In Review -> Implemented relabel must not run inline on the merge tick: %#v", calls)
	}
	for _, c := range calls {
		if len(c) > 4 && c[1] == "api" && strings.Contains(c[4], "ref=feature") {
			t.Fatalf("policy fetch used the head ref, want the base ref: %#v", c)
		}
	}
	if s.AutomergeDecision != "merge" {
		t.Errorf("AutomergeDecision = %q, want %q", s.AutomergeDecision, "merge")
	}
	if s.AutomergeReason != "" {
		t.Errorf("AutomergeReason = %q, want empty on a successful merge", s.AutomergeReason)
	}
	if s.AutomergeCheckedAt.IsZero() {
		t.Error("AutomergeCheckedAt was not stamped")
	}
}

// TestTickAutomergeUsesResolvedMergeMethod pins that the merge actually
// issued uses the resolved policy's mergeMethod ("rebase" here), not a
// hardcoded --squash regardless of what stage 10 validated.
func TestTickAutomergeUsesResolvedMergeMethod(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{out: automergeEligiblePR()},
		{out: `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`},
		{out: `[]`},
		{out: `[]`},
		{out: `{"labels":[{"name":"automerge:ok"}]}`},
		{out: `{"automerge":{"maxChangedFiles":10,"maxDiffLines":500,"mergeMethod":"rebase"}}`},
		{out: `{"squash":false,"merge":false,"rebase":true}`},
		{out: "Merged pull request #42 (o/r)"},
	}, &calls)

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 900}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	var mergeCall []string
	for _, c := range calls {
		if len(c) > 2 && c[1] == "pr" && c[2] == "merge" {
			mergeCall = c
		}
	}
	if mergeCall == nil {
		t.Fatalf("gh pr merge was not issued: %#v", calls)
	}
	if !hasArg(mergeCall, "--rebase") {
		t.Errorf("merge call = %#v, want --rebase (the resolved policy mergeMethod)", mergeCall)
	}
	if hasArg(mergeCall, "--squash") {
		t.Errorf("merge call = %#v, must not default to --squash when the resolved policy specifies rebase", mergeCall)
	}
}

// TestTickAutomergeDisabledMakesNoExtraGHCalls is test-strategy case (b):
// automerge.enabled unset ⇒ zero extra gh calls and identical pre-existing
// behavior -- the existing babysit_test.go fixtures script exactly 4 gh
// responses, and this must keep passing unchanged (Alternatives Considered).
// Relies on the package TestMain pinning fleetConfigPath to "" (disabled).
func TestTickAutomergeDisabledMakesNoExtraGHCalls(t *testing.T) {
	var calls [][]string
	withCommands(t, []string{
		automergeEligiblePR(),
		`[{"bucket":"pass","name":"test","state":"SUCCESS"}]`,
		`[]`,
		`[]`,
	}, &calls)
	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 900}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	ghCalls := 0
	for _, c := range calls {
		if len(c) > 0 && c[0] == "gh" {
			ghCalls++
		}
	}
	if ghCalls != 4 {
		t.Fatalf("gh calls = %d, want exactly 4 (the pre-existing fixture; automerge disabled must add none)", ghCalls)
	}
	if s.AutomergeReason != reasonAutomergeDisabled {
		t.Errorf("AutomergeReason = %q, want %q", s.AutomergeReason, reasonAutomergeDisabled)
	}
}

// TestTickAutomergeHeldOnRepairOrReviewPending is test-strategy case (c):
// RepairPending or a non-empty PendingKeys holds automerge, without ever
// reaching the policy fetch or merge call.
func TestTickAutomergeHeldOnRepairOrReviewPending(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*State)
		// extraScript is the lazy GraphQL thread fetch (#850): reconcile
		// runs in tick() via reconcilePendingFeedback unconditionally, right
		// before runAutomerge is even called -- i.e. before runAutomerge's
		// own labels fetch -- but only the "review feedback pending"
		// subtest's "comment:1" PendingKeys entry actually triggers it.
		extraScript []scriptedCall
		wantReason  string
	}{
		// LastHeadSHA is pinned to automergeEligiblePR's "abc" headRefOid: the
		// pre-existing (out-of-scope) CI-repair head-advance detection treats
		// a HeadRefOID/LastHeadSHA mismatch as "the head moved on since this
		// was recorded, so it's now stale" and resets RepairPending to false
		// -- well before automerge's insertion point after the
		// review-feedback block. Without pinning it, the "CI repair pending"
		// subtest's RepairPending would already be wiped by the time
		// automerge runs and would spuriously fall through to the policy
		// stage. PendingHeadSHA is no longer compared for clearing purposes
		// at all (#850 deletes that rule) -- it survives only as
		// repair-attempt/dedup metadata, so the "review feedback pending"
		// subtest sets it purely for fixture realism, not because anything
		// still reads it to decide resolution.
		{"CI repair pending", func(s *State) { s.RepairPending = true; s.LastHeadSHA = "abc" }, nil, reasonRepairPending},
		{"review feedback pending", func(s *State) { s.PendingKeys = []string{"comment:1"}; s.PendingHeadSHA = "abc" }, []scriptedCall{
			// Left unresolved so PendingKeys stays non-empty and the hold
			// still reaches reasonReviewPending (an ordinary hold, not one
			// of the four new feedback-hold reasons -- see
			// TestTickFeedbackHoldsAutomerge for those).
			{out: `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"isResolved":false,"comments":{"totalCount":1,"nodes":[{"databaseId":1}]}}]}}}}}`},
		}, reasonReviewPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withFleetAutomergeEnabled(t, true)
			var calls [][]string
			script := []scriptedCall{
				{out: automergeEligiblePR()},
				{out: `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`},
				{out: `[]`},
				{out: `[]`},
			}
			script = append(script, tc.extraScript...)
			script = append(script, scriptedCall{out: `{"labels":[{"name":"automerge:ok"}]}`})
			withScriptedCommands(t, script, &calls)
			s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
			tc.setup(&s)
			if _, _, err := tick(&s); err != nil {
				t.Fatal(err)
			}
			if s.AutomergeReason != tc.wantReason {
				t.Fatalf("AutomergeReason = %q, want %q", s.AutomergeReason, tc.wantReason)
			}
			for _, c := range calls {
				if len(c) > 2 && c[1] == "pr" && c[2] == "merge" {
					t.Fatalf("gh pr merge must not be issued while held: %#v", calls)
				}
			}
		})
	}
}

// TestTickAutomergeHeldWhenMergeRejected is test-strategy case (d): a
// rejected merge (e.g. branch protection) is a logged hold, not a tick
// error -- no backoff escalation, retried next tick.
func TestTickAutomergeHeldWhenMergeRejected(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{out: automergeEligiblePR()},
		{out: `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`},
		{out: `[]`},
		{out: `[]`},
		{out: `{"labels":[{"name":"automerge:ok"}]}`},
		{out: `{"automerge":{"maxChangedFiles":10,"maxDiffLines":500,"mergeMethod":"squash"}}`},
		{out: `{"squash":true,"merge":false,"rebase":true}`},
		{out: "branch protection required review", err: errors.New("exit status 1")},
	}, &calls)

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	terminal, _, err := tick(&s)
	if err != nil {
		t.Fatalf("tick returned an error on a rejected merge, want nil (retried next tick, no backoff escalation): %v", err)
	}
	if terminal {
		t.Fatal("tick must not treat a rejected merge as terminal")
	}
	if s.AutomergeReason != reasonMergeFailed {
		t.Fatalf("AutomergeReason = %q, want %q", s.AutomergeReason, reasonMergeFailed)
	}
	// The rejected merge's combined `gh` output (branch-protection block,
	// auth failure, rate limit, real conflict, ...) must not be discarded --
	// it's the only thing that distinguishes those failure classes from one
	// another beyond the single generic reasonMergeFailed constant.
	if !strings.Contains(s.AutomergeDetail, "branch protection required review") {
		t.Fatalf("AutomergeDetail = %q, want it to contain the captured gh pr merge output", s.AutomergeDetail)
	}
}

// TestTickAutomergeHoldsWhenHeadSHAUnknown pins that an empty HeadRefOID --
// e.g. a transient `gh pr view` gap -- holds automerge under its own distinct
// reason and issues no `gh pr merge` at all: an empty --match-head-commit
// value would let `gh` silently drop the TOCTOU pin from the merge mutation
// instead of failing closed.
func TestTickAutomergeHoldsWhenHeadSHAUnknown(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	pr := `{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":"","baseRefName":"main","mergeable":"MERGEABLE","isDraft":false,"changedFiles":1,"additions":5,"deletions":2,"files":[{"path":"watch/internal/babysit/x.go"}],"url":"https://example/pr/42","closingIssuesReferences":[{"number":9}]}`
	var calls [][]string
	withScriptedCommands(t, []scriptedCall{
		{out: pr},
		{out: `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`},
		{out: `[]`},
		{out: `[]`},
		{out: `{"labels":[{"name":"automerge:ok"}]}`},
	}, &calls)

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if s.AutomergeReason != reasonHeadSHAUnknown {
		t.Fatalf("AutomergeReason = %q, want %q", s.AutomergeReason, reasonHeadSHAUnknown)
	}
	for _, c := range calls {
		if len(c) > 2 && c[1] == "pr" && c[2] == "merge" {
			t.Fatalf("gh pr merge must not be issued when the head SHA is unknown: %#v", calls)
		}
	}
}

// TestTickAutomergeSanitizesRejectedMergeDetail pins that a rejected merge's
// captured `gh` output -- multi-line and arbitrarily long -- is sanitized
// (newlines collapsed to spaces, truncated) before landing in
// s.AutomergeDetail, since it feeds a single-line log format downstream
// tooling substring-classifies and is also persisted onto State.
func TestTickAutomergeSanitizesRejectedMergeDetail(t *testing.T) {
	withFleetAutomergeEnabled(t, true)
	var calls [][]string
	longOutput := "branch protection required review\nsecond line\nthird line: " + strings.Repeat("y", 250)
	withScriptedCommands(t, []scriptedCall{
		{out: automergeEligiblePR()},
		{out: `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`},
		{out: `[]`},
		{out: `[]`},
		{out: `{"labels":[{"name":"automerge:ok"}]}`},
		{out: `{"automerge":{"maxChangedFiles":10,"maxDiffLines":500,"mergeMethod":"squash"}}`},
		{out: `{"squash":true,"merge":false,"rebase":true}`},
		{out: longOutput, err: errors.New("exit status 1")},
	}, &calls)

	s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60}
	if _, _, err := tick(&s); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.AutomergeDetail, "\n") {
		t.Fatalf("AutomergeDetail = %q, must not contain an embedded newline", s.AutomergeDetail)
	}
	if len(s.AutomergeDetail) > detailMaxLen+len("...") {
		t.Fatalf("AutomergeDetail length = %d, want capped at roughly %d", len(s.AutomergeDetail), detailMaxLen)
	}
}

// -- #850: feedback-hold reasons wired into automerge -------------------------

// TestTickFeedbackHoldsAutomerge is AC 5: an API/parsing failure, an unknown
// review state, or an unsupported feedback-key type each hold automerge under
// their own distinct reason constant -- asserting the exact AutomergeReason
// (per watch/docs/error-handling.md #446) and that no `gh pr merge` call ever
// appears in recorded calls.
func TestTickFeedbackHoldsAutomerge(t *testing.T) {
	unreadableThread := "not json"
	truncatedThread := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"isResolved":false,"comments":{"totalCount":2,"nodes":[{"databaseId":5}]}}]}}}}}`

	for _, tc := range []struct {
		name        string
		pendingKeys []string
		extraScript []scriptedCall // the lazy GraphQL fetch, only for comment: keys
		wantReason  string
	}{
		{
			"unreadable: the GraphQL thread fetch itself fails (malformed body)",
			[]string{"comment:5"},
			[]scriptedCall{{out: unreadableThread}},
			reasonFeedbackUnreadable,
		},
		{
			"truncated: a thread's comments.totalCount exceeds its fetched node count",
			[]string{"comment:5"},
			[]scriptedCall{{out: truncatedThread}},
			reasonFeedbackTruncated,
		},
		{
			"unknown: a pending review key GitHub no longer reports at all (no GraphQL call needed)",
			[]string{"review:99"},
			nil,
			reasonReviewStateUnknown,
		},
		{
			"unsupported: a pending key of a type this classifier does not recognize (no GraphQL call needed)",
			[]string{"label:1"},
			nil,
			reasonFeedbackUnsupported,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withFleetAutomergeEnabled(t, true)
			var calls [][]string
			// The lazy GraphQL thread fetch (tc.extraScript, when a
			// comment: key is pending) runs in tick() via
			// reconcilePendingFeedback immediately before runAutomerge is
			// called -- i.e. before runAutomerge's own labels fetch, which
			// must come last in the script.
			script := []scriptedCall{
				{out: automergeEligiblePR()},
				{out: `[{"bucket":"pass","name":"test","state":"SUCCESS"}]`},
				{out: `[]`},
				{out: `[]`},
			}
			script = append(script, tc.extraScript...)
			script = append(script, scriptedCall{out: `{"labels":[{"name":"automerge:ok"}]}`})
			withScriptedCommands(t, script, &calls)

			s := State{PR: "42", Repo: "o/r", Agent: "codex", IntervalSeconds: 60, CurrentDelaySeconds: 60, PendingKeys: tc.pendingKeys}
			if _, _, err := tick(&s); err != nil {
				t.Fatal(err)
			}
			if s.AutomergeReason != tc.wantReason {
				t.Fatalf("AutomergeReason = %q, want %q", s.AutomergeReason, tc.wantReason)
			}
			for _, c := range calls {
				if len(c) > 2 && c[1] == "pr" && c[2] == "merge" {
					t.Fatalf("gh pr merge must not be issued while a feedback hold is in effect: %#v", calls)
				}
			}
			if !reflect.DeepEqual(s.PendingKeys, tc.pendingKeys) {
				t.Fatalf("PendingKeys = %v, want unchanged %v: a fail-closed hold must not clear the item", s.PendingKeys, tc.pendingKeys)
			}
		})
	}
}
