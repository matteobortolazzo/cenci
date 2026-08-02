package dispatch

// Ticket #882: direct package-boundary coverage for permission.go's
// write-permission probe -- the impure adapter that resolves a comment
// author's CURRENT repository write permission via `gh api
// repos/<owner>/<repo>/collaborators/<login>/permission`, replacing
// author_association (demoted to a coarse prefilter, resume.go) as the
// actual answer-probe authorization gate.
//
// Per the plan's Q1: the top-level `permission` field is the sole
// authoritative signal (accepted set {"admin","write"} exactly -- role_name
// is never consulted, since custom org roles make it an open set). Per Q2:
// nine distinct failure classes, mirroring OpenPRProbe's own fine-grained
// precedent (openpr.go/openpr_test.go) -- each needs its own fixture and its
// own exact-reason assertion (#446/#598), never a bare non-empty check.
//
// fetchWritePermission is the single, uncached `gh` call + classification
// (mirrors dependency.go's fetchDependencyState); permissionCache wraps it
// with the per-pass, per-(repo, lowercased-login) cache and the per-repo
// lookup budget (mirrors dependencyResolutionBudget/resolveDependencyState).
// writePermissionAnswerProbe is the pure WritePermission -> AnswerProbe
// mapping with a default-deny branch (types.go's WritePermission has no
// permissive zero value, mirroring AnswerProbe/RepoAutonomy).

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// permissionJSON renders the exact top-level shape GET
// .../collaborators/<login>/permission returns for its `permission` field.
func permissionJSON(value string) string {
	return fmt.Sprintf(`{"permission":%q}`, value)
}

// -- pure mapping: WritePermission -> AnswerProbe ----------------------------

// TestWritePermissionAnswerProbeMapping covers every WritePermission closed-set
// value's mapping into its own AnswerProbe class, plus the zero-value/
// unrecognized default-deny branch (#598): a regression collapsing two
// distinct failure classes into the same AnswerProbe must be caught by this
// content-specific table, never a bare non-empty check (#446).
func TestWritePermissionAnswerProbeMapping(t *testing.T) {
	tests := []struct {
		perm WritePermission
		want AnswerProbe
	}{
		{WritePermissionGranted, AnswerProbeAnswered},
		{WritePermissionDenied, AnswerProbeUnauthorized},
		{WritePermissionLoginInvalid, AnswerProbeLoginInvalid},
		{WritePermissionAPIError, AnswerProbePermissionAPIError},
		{WritePermissionTimeout, AnswerProbePermissionTimeout},
		{WritePermissionTruncated, AnswerProbePermissionTruncated},
		{WritePermissionMalformed, AnswerProbePermissionMalformed},
		{WritePermissionMissingField, AnswerProbePermissionMissingField},
		{WritePermissionUnknownValue, AnswerProbePermissionUnknownValue},
		{WritePermissionCapExhausted, AnswerProbePermissionCapExhausted},
		// Zero-value/unrecognized WritePermission must default-deny distinctly
		// rather than silently reproducing a permissive or already-enumerated
		// outcome (go-gotchas #598).
		{WritePermission(""), AnswerProbePermissionUnknownValue},
		{WritePermission("bogus-future-value"), AnswerProbePermissionUnknownValue},
	}
	for _, tc := range tests {
		t.Run(string(tc.perm), func(t *testing.T) {
			if got := writePermissionAnswerProbe(tc.perm); got != tc.want {
				t.Errorf("writePermissionAnswerProbe(%q) = %q, want %q", tc.perm, got, tc.want)
			}
		})
	}
}

// -- login-shape validation (path-injection guard) ---------------------------

// TestGithubLoginPattern pins the exact regex the plan's Assumptions specify
// (`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`) -- a defensive guard before login is
// ever interpolated into the `gh api` endpoint path (go-gotchas #855's
// splicing-validation rule), even though every current call site's login
// comes from GitHub's own REST response.
func TestGithubLoginPattern(t *testing.T) {
	tests := []struct {
		login string
		want  bool
	}{
		{"octocat", true},
		{"Octocat123", true},
		{"o", true},
		{"a-b-c", true},
		{strings.Repeat("a", 39), true},
		{strings.Repeat("a", 40), false},
		{"", false},
		{"-octocat", false},
		{"octocat!", false},
		{"octo cat", false},
		{"../../etc/passwd", false},
		{"repos/o/r", false},
	}
	for _, tc := range tests {
		if got := githubLoginPattern.MatchString(tc.login); got != tc.want {
			t.Errorf("githubLoginPattern.MatchString(%q) = %v, want %v", tc.login, got, tc.want)
		}
	}
}

// -- fetchWritePermission: the uncached, single `gh` call --------------------

func TestFetchWritePermission_GrantedForAdminAndWrite(t *testing.T) {
	for _, v := range []string{"admin", "write"} {
		t.Run(v, func(t *testing.T) {
			installFakeGH(t, fmt.Sprintf("printf '%s'\n", permissionJSON(v)))
			var buf bytes.Buffer
			got := fetchWritePermission("o/r", "octocat", &buf)
			if got != WritePermissionGranted {
				t.Fatalf("fetchWritePermission(permission=%q) = %q, want WritePermissionGranted", v, got)
			}
		})
	}
}

// TestFetchWritePermission_DeniedForReadTriageNone pins the plan's Q2
// "positively denied" class: a recognized-but-insufficient permission value
// distinct from an unrecognized future value (WritePermissionUnknownValue
// below).
func TestFetchWritePermission_DeniedForReadTriageNone(t *testing.T) {
	for _, v := range []string{"read", "triage", "none"} {
		t.Run(v, func(t *testing.T) {
			installFakeGH(t, fmt.Sprintf("printf '%s'\n", permissionJSON(v)))
			var buf bytes.Buffer
			got := fetchWritePermission("o/r", "octocat", &buf)
			if got != WritePermissionDenied {
				t.Fatalf("fetchWritePermission(permission=%q) = %q, want WritePermissionDenied", v, got)
			}
		})
	}
}

// TestFetchWritePermission_UnknownValueForFutureString pins Q1's closed
// accepted-set discipline: "maintain" is a real GitHub permission concept but
// is NOT in the accepted granting set {"admin","write"} the plan specifies,
// and is also not one of the enumerated denied values -- it must fail closed
// as unrecognized, never silently granted nor silently folded into "denied".
func TestFetchWritePermission_UnknownValueForFutureString(t *testing.T) {
	installFakeGH(t, fmt.Sprintf("printf '%s'\n", permissionJSON("maintain")))
	var buf bytes.Buffer
	got := fetchWritePermission("o/r", "octocat", &buf)
	if got != WritePermissionUnknownValue {
		t.Fatalf("fetchWritePermission(permission=maintain) = %q, want WritePermissionUnknownValue", got)
	}
	if !strings.Contains(buf.String(), "maintain") {
		t.Errorf("expected a logged detail naming the unrecognized value, got: %q", buf.String())
	}
}

// TestFetchWritePermission_InvalidLogin_NeverCallsGh proves the login-shape
// guard runs BEFORE any gh invocation (a path-injection guard that must never
// spend a network call, mirroring probeEscalationAnswer's own
// validAnchor-before-budget short-circuit).
func TestFetchWritePermission_InvalidLogin_NeverCallsGh(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "calls.count")
	installFakeGH(t, fmt.Sprintf("printf 'x' >> %q\nexit 1\n", countFile))

	var buf bytes.Buffer
	got := fetchWritePermission("o/r", "-bad-login", &buf)
	if got != WritePermissionLoginInvalid {
		t.Fatalf("fetchWritePermission(invalid login) = %q, want WritePermissionLoginInvalid", got)
	}
	if _, err := os.Stat(countFile); !os.IsNotExist(err) {
		t.Fatalf("expected zero gh calls for an invalid login, but the call-count file exists")
	}
}

func TestFetchWritePermission_NonzeroExit_APIError(t *testing.T) {
	installFakeGH(t, "printf 'boom' >&2\nexit 1\n")
	var buf bytes.Buffer
	got := fetchWritePermission("o/r", "octocat", &buf)
	if got != WritePermissionAPIError {
		t.Fatalf("fetchWritePermission = %q, want WritePermissionAPIError", got)
	}
	if !strings.Contains(buf.String(), "o/r") || !strings.Contains(buf.String(), "octocat") {
		t.Errorf("expected a logged detail naming repo and login, got: %q", buf.String())
	}
}

// TestFetchWritePermission_Timeout_ClassifiesDistinctly covers the timeout
// class distinctly from an ordinary API error, mirroring
// TestOpenPRInventory_Timeout_SleepingShimClassifiesTimeout's overridable-var
// pattern (openpr_test.go) so the test does not have to wait out a real
// 60-second ghTimeout bound. Uses `exec sleep` (go-gotchas #886): a bare
// `sleep` would fork as a grandchild the kill signal never reaches, defeating
// the timeout-bound assertion on elapsed wall-clock time. Uses
// installFakeGHOnPath, not installFakeGH (go-gotchas #852): the fake `gh`
// script's own `sleep` invocation needs the real binary resolvable on PATH --
// installFakeGH's wholesale PATH replacement would make `sleep` itself
// unresolvable ("not found"), which is still a nonzero-exit gh failure and
// would silently misclassify as WritePermissionAPIError instead of actually
// exercising the timeout path.
func TestFetchWritePermission_Timeout_ClassifiesDistinctly(t *testing.T) {
	orig := permissionProbeTimeout
	permissionProbeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { permissionProbeTimeout = orig })

	installFakeGHOnPath(t, "exec sleep 30\n")
	var buf bytes.Buffer
	start := time.Now()
	got := fetchWritePermission("o/r", "octocat", &buf)
	elapsed := time.Since(start)
	if got != WritePermissionTimeout {
		t.Fatalf("fetchWritePermission = %q, want WritePermissionTimeout", got)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("fetchWritePermission took %v, want it bounded well within the overridden timeout, not the full 30s sleep", elapsed)
	}
}

// TestFetchWritePermission_OversizedOutput_Truncated covers execGhBounded's
// output-cap classification (errGhOutputTruncated): an oversized response
// must fail closed rather than decoding a truncated/corrupted partial
// payload. Uses installFakeGHOnPath, not installFakeGH (go-gotchas #852):
// the fake `gh` script's `yes | tr | head` pipeline needs those real
// shell-utility binaries resolvable on PATH -- installFakeGH's wholesale PATH
// replacement would make them unresolvable, so the pipeline would fail for
// an unrelated reason (silently misclassified as WritePermissionAPIError)
// instead of actually producing the oversized payload this test exercises.
func TestFetchWritePermission_OversizedOutput_Truncated(t *testing.T) {
	installFakeGHOnPath(t, fmt.Sprintf("yes x | tr -d '\\n' | head -c %d\n", maxPermissionStdoutBytes+1000))
	var buf bytes.Buffer
	got := fetchWritePermission("o/r", "octocat", &buf)
	if got != WritePermissionTruncated {
		t.Fatalf("fetchWritePermission = %q, want WritePermissionTruncated", got)
	}
}

func TestFetchWritePermission_MalformedJSON(t *testing.T) {
	installFakeGH(t, "printf 'not valid json'\n")
	var buf bytes.Buffer
	got := fetchWritePermission("o/r", "octocat", &buf)
	if got != WritePermissionMalformed {
		t.Fatalf("fetchWritePermission = %q, want WritePermissionMalformed", got)
	}
}

// TestFetchWritePermission_MissingPermissionField covers a well-formed JSON
// object that nonetheless omits the top-level `permission` field -- e.g. a
// response carrying only `role_name` (a custom org role), which per the
// plan's Q1 must never be consulted as a substitute signal.
func TestFetchWritePermission_MissingPermissionField(t *testing.T) {
	installFakeGH(t, `printf '{"role_name":"custom-role"}'`+"\n")
	var buf bytes.Buffer
	got := fetchWritePermission("o/r", "octocat", &buf)
	if got != WritePermissionMissingField {
		t.Fatalf("fetchWritePermission = %q, want WritePermissionMissingField (role_name alone must never be consulted, plan Q1)", got)
	}
}

// TestFetchWritePermission_EndpointShape pins the exact `gh api` argv shape
// the ticket specifies: `repos/<owner>/<repo>/collaborators/<login>/permission`,
// with no extra flags that could change the response shape.
func TestFetchWritePermission_EndpointShape(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "argv.log")
	installFakeGH(t, fmt.Sprintf(`
echo "$*" >> %q
printf '%s'
`, logFile, permissionJSON("write")))

	var buf bytes.Buffer
	fetchWritePermission("o/r", "octocat", &buf)

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("reading argv log: %v", err)
	}
	want := "api repos/o/r/collaborators/octocat/permission\n"
	if string(data) != want {
		t.Fatalf("gh argv = %q, want %q", string(data), want)
	}
}

// -- permissionCache: per-pass cache + per-repo budget -----------------------

// TestPermissionCache_DedupesSameRepoLoginCaseInsensitively covers the plan's
// Assumptions: permission verdicts are cached per pass keyed by (repo,
// lowercased login), so one login answering N tickets in one repo costs
// exactly one gh call -- case must not matter (GitHub logins are themselves
// case-insensitive).
func TestPermissionCache_DedupesSameRepoLoginCaseInsensitively(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "calls.count")
	installFakeGH(t, fmt.Sprintf(`
printf 'x' >> %q
printf '%s'
`, countFile, permissionJSON("write")))

	cache := newPermissionCache()
	var buf bytes.Buffer
	if got := cache.resolveWritePermission("o/r", "octocat", &buf); got != WritePermissionGranted {
		t.Fatalf("first resolve = %q, want WritePermissionGranted", got)
	}
	if got := cache.resolveWritePermission("o/r", "Octocat", &buf); got != WritePermissionGranted {
		t.Fatalf("second resolve (different case) = %q, want WritePermissionGranted from cache", got)
	}
	data, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("reading call-count file: %v", err)
	}
	if len(data) != 1 {
		t.Fatalf("gh invoked %d time(s), want exactly 1 (case-insensitive cache dedup)", len(data))
	}
}

func TestPermissionCache_DifferentLoginsEachCallGh(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "calls.count")
	installFakeGH(t, fmt.Sprintf(`
printf 'x' >> %q
printf '%s'
`, countFile, permissionJSON("write")))

	cache := newPermissionCache()
	var buf bytes.Buffer
	cache.resolveWritePermission("o/r", "octocat", &buf)
	cache.resolveWritePermission("o/r", "hubot", &buf)
	data, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("reading call-count file: %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("gh invoked %d time(s), want exactly 2 (distinct logins each cost one call)", len(data))
	}
}

// TestPermissionCache_PerRepoBudgetCapExhausted mirrors
// TestProbeEscalationAnswer_CapBoundsGhCalls's cap-hit discipline: past
// maxPermissionLookupsPerRepo, no further gh call is made for that repo, and
// the cap-hit line logs exactly once.
func TestPermissionCache_PerRepoBudgetCapExhausted(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "calls.count")
	installFakeGH(t, fmt.Sprintf(`
printf 'x' >> %q
printf '%s'
`, countFile, permissionJSON("write")))

	cache := newPermissionCache()
	var buf bytes.Buffer
	const extra = 5
	for i := 0; i < maxPermissionLookupsPerRepo+extra; i++ {
		login := fmt.Sprintf("user%d", i)
		got := cache.resolveWritePermission("o/r", login, &buf)
		want := WritePermissionGranted
		if i >= maxPermissionLookupsPerRepo {
			want = WritePermissionCapExhausted
		}
		if got != want {
			t.Fatalf("resolve[%d] = %q, want %q", i, got, want)
		}
	}
	data, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("reading call-count file: %v", err)
	}
	if len(data) != maxPermissionLookupsPerRepo {
		t.Fatalf("gh invoked %d time(s), want exactly maxPermissionLookupsPerRepo (%d)", len(data), maxPermissionLookupsPerRepo)
	}

	capMsg := fmt.Sprintf("write-permission lookup cap (%d) reached", maxPermissionLookupsPerRepo)
	if got := strings.Count(buf.String(), capMsg); got != 1 {
		t.Fatalf("cap-hit line naming %q logged %d time(s), want exactly 1: %q", capMsg, got, buf.String())
	}
}

// TestPermissionCache_InvalidLoginNeverConsumesBudget is a review-fix
// regression test (#882 follow-up): a login failing githubLoginPattern's
// shape check must never consume the per-repo budget counter, since
// fetchWritePermission's own doc comment states such a login "never spends a
// gh call" -- the budget exists to bound actual gh invocations, not attempted
// resolutions. Before the fix, resolveWritePermission incremented the budget
// counter before delegating to fetchWritePermission, so a flood of
// shape-invalid logins could exhaust a repo's budget without a single real gh
// call ever being made.
func TestPermissionCache_InvalidLoginNeverConsumesBudget(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "calls.count")
	installFakeGH(t, fmt.Sprintf(`
printf 'x' >> %q
printf '%s'
`, countFile, permissionJSON("write")))

	cache := newPermissionCache()
	var buf bytes.Buffer
	for i := 0; i < maxPermissionLookupsPerRepo; i++ {
		login := fmt.Sprintf("-bad-login-%d", i)
		if got := cache.resolveWritePermission("o/r", login, &buf); got != WritePermissionLoginInvalid {
			t.Fatalf("resolve[%d] = %q, want WritePermissionLoginInvalid", i, got)
		}
	}

	// The per-repo budget must be untouched by the invalid logins above: a
	// valid login resolved next must still succeed under the full budget,
	// not read WritePermissionCapExhausted.
	if got := cache.resolveWritePermission("o/r", "octocat", &buf); got != WritePermissionGranted {
		t.Fatalf("resolve for valid login after %d invalid logins = %q, want WritePermissionGranted (invalid logins must never consume budget)", maxPermissionLookupsPerRepo, got)
	}
	data, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("reading call-count file: %v", err)
	}
	if len(data) != 1 {
		t.Fatalf("gh invoked %d time(s), want exactly 1 (only the one valid login's call)", len(data))
	}
}

// TestPermissionCache_BudgetIsPerRepoNotGlobal proves the lookup budget is
// bound per repository (the ticket's own wording), not a single pass-wide
// global cap (the plan's Open Questions section) -- a second, distinct repo's
// first lookup must still make a real gh call after another repo's budget is
// exhausted.
func TestPermissionCache_BudgetIsPerRepoNotGlobal(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "calls.count")
	installFakeGH(t, fmt.Sprintf(`
printf 'x' >> %q
printf '%s'
`, countFile, permissionJSON("write")))

	cache := newPermissionCache()
	var buf bytes.Buffer
	for i := 0; i < maxPermissionLookupsPerRepo; i++ {
		cache.resolveWritePermission("o/r1", fmt.Sprintf("user%d", i), &buf)
	}
	got := cache.resolveWritePermission("o/r2", "someone", &buf)
	if got != WritePermissionGranted {
		t.Fatalf("resolve for a different repo = %q, want WritePermissionGranted (budgets are per-repo, #882)", got)
	}
	data, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("reading call-count file: %v", err)
	}
	if len(data) != maxPermissionLookupsPerRepo+1 {
		t.Fatalf("gh invoked %d time(s), want exactly %d (repo o/r1's cap plus one more for o/r2)", len(data), maxPermissionLookupsPerRepo+1)
	}
}
