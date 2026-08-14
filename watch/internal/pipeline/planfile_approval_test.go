package pipeline

import "testing"

// The `approval` front-matter key (#1050) records which path approved a plan
// — `human`, `lean`, `trivial`, or `lean-resumed`. It is written by flow's
// `## Persist the Plan` and read by no Go consumer: ParseFrontMatter collects
// front matter into a flat map and parseAndValidatePlan reads only the keys
// it knows, so an unrecognized key is inert by construction.
//
// That construction is exactly what these tests pin. The key lands in real
// plan files on disk from the moment flow ships it, so a future front-matter
// validator that rejected unknown keys would not fail loudly here — it would
// classify every lean-planned ticket's plan as malformed, stranding it
// mid-pipeline. And plans written before the key existed must keep validating
// unchanged, since absent means "unrecorded", never "not approved".

// TestCheckPlan_ApprovalKeyPresent_StillHealthy pins the forward direction:
// a plan carrying the new key validates exactly as one without it.
func TestCheckPlan_ApprovalKeyPresent_StillHealthy(t *testing.T) {
	for _, approval := range []string{"human", "lean", "trivial", "lean-resumed"} {
		t.Run(approval, func(t *testing.T) {
			repoRoot := t.TempDir()
			initGitRepoWithCommit(t, repoRoot)
			sha := gitHeadSha(t, repoRoot)
			createdAt := "2026-07-20T20:00:00Z"

			fields := defaultPlanFields("42", "add-thing", sha, createdAt)
			fields["approval"] = approval
			writePlanFile(t, repoRoot, "42", "add-thing", fields, validPlanBody)

			gh := newFakeGhTicket(t, "OPEN", "2026-07-20T19:00:00Z")
			gh.install()

			_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
			if err != nil {
				t.Fatalf("CheckPlan on a plan carrying approval: %s: %v", approval, err)
			}
			if check.Decision != "resume" {
				t.Fatalf("Decision = %q, want resume: an unrecognized front-matter key must never make a plan malformed", check.Decision)
			}
		})
	}
}

// TestCheckPlan_ApprovalKeyAbsent_StillHealthy pins the backward direction:
// every plan written before #1050 has no approval key, and that must stay a
// valid plan rather than becoming a required-field violation.
func TestCheckPlan_ApprovalKeyAbsent_StillHealthy(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	sha := gitHeadSha(t, repoRoot)
	createdAt := "2026-07-20T20:00:00Z"

	fields := defaultPlanFields("42", "add-thing", sha, createdAt)
	if _, ok := fields["approval"]; ok {
		t.Fatal("defaultPlanFields must not carry approval: this test's whole point is the key's absence")
	}
	writePlanFile(t, repoRoot, "42", "add-thing", fields, validPlanBody)

	gh := newFakeGhTicket(t, "OPEN", "2026-07-20T19:00:00Z")
	gh.install()

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan on a pre-#1050 plan with no approval key: %v", err)
	}
	if check.Decision != "resume" {
		t.Fatalf("Decision = %q, want resume: an absent approval key means unrecorded, never invalid", check.Decision)
	}
}

// TestCheckPlan_AwaitingInputDraft_NeedsNoApprovalKey pins the one path that
// deliberately writes no approval value: an escalation draft is not an
// approved plan, so it carries no key and must still classify as
// awaiting-input rather than malformed.
func TestCheckPlan_AwaitingInputDraft_NeedsNoApprovalKey(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepoWithCommit(t, repoRoot)
	sha := gitHeadSha(t, repoRoot)
	createdAt := "2026-07-20T20:00:00Z"

	fields := defaultPlanFields("42", "add-thing", sha, createdAt)
	fields["status"] = "awaiting-input"
	writePlanFile(t, repoRoot, "42", "add-thing", fields, validPlanBody)

	gh := newFakeGhTicket(t, "OPEN", "2026-07-20T19:00:00Z")
	gh.install()

	_, check, err := CheckPlan(PlanCheckOpts{ID: "42", RepoRoot: repoRoot, RepoSlug: "o/r"})
	if err != nil {
		t.Fatalf("CheckPlan on an awaiting-input draft with no approval key: %v", err)
	}
	if check.Decision != "awaiting-input" {
		t.Fatalf("Decision = %q, want awaiting-input", check.Decision)
	}
}
