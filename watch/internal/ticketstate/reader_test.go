package ticketstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testRepo = Repo{Owner: "zorgon-labs", Name: "frobnicate"}

// --- fake gh: records every call's argv and answers from files ---

type fakeGh struct{ dir string }

// installFakeGh puts a gh shim first on PATH. Every call appends its argv to
// argv.log and then sources behavior.sh (written by respond/behave). The gh
// credential variables are neutralized as defense in depth
// (watch/docs/test-isolation.md "Ambient gh-credential isolation").
func installFakeGh(t *testing.T) *fakeGh {
	t.Helper()
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
		t.Setenv(k, "")
	}
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	dir := installShim(t, "gh", `dir=$(dirname "$0")
for a in "$@"; do printf '%s\n' "$a"; done >> "$dir/argv.log"
printf '%s\n' '--END--' >> "$dir/argv.log"
. "$dir/behavior.sh"`)
	return &fakeGh{dir: dir}
}

// behave sets the shell snippet run (after argv is logged) on every call.
func (f *fakeGh) behave(t *testing.T, snippet string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "behavior.sh"), []byte(snippet+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// respond makes every call print body on stdout, stderr text on stderr, and
// exit with code.
func (f *fakeGh) respond(t *testing.T, body string, code int, stderr string) {
	t.Helper()
	for name, content := range map[string]string{"body.json": body, "stderr.txt": stderr} {
		if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.behave(t, fmt.Sprintf(`if [ -s "$dir/stderr.txt" ]; then cat "$dir/stderr.txt" >&2; fi
cat "$dir/body.json"
exit %d`, code))
}

// respondAllClosed answers every call with a CLOSED issue for each alias the
// call's own query asked for, so chunked reads can be answered without
// knowing which numbers land in which call.
func (f *fakeGh) respondAllClosed(t *testing.T) {
	t.Helper()
	f.behave(t, `for a in "$@"; do case "$a" in query=*) q="$a" ;; esac; done
sep=''
printf '{"data":{"repository":{'
for alias in $(printf '%s' "$q" | grep -o 'n[0-9][0-9]*:' | tr -d ':'); do
  printf '%s"%s":{"__typename":"Issue","state":"CLOSED","closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":false},"nodes":[]}}' "$sep" "$alias"
  sep=','
done
printf '}}}'
exit 0`)
}

// calls returns the argv (without the program name) of every gh invocation.
func (f *fakeGh) calls(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, "argv.log"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	var cur []string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "--END--" {
			calls = append(calls, cur)
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	return calls
}

// queryArg returns the GraphQL query text passed as `-f query=...`.
func queryArg(t *testing.T, argv []string) string {
	t.Helper()
	for i, a := range argv {
		if strings.HasPrefix(a, "query=") {
			if i == 0 || argv[i-1] != "-f" {
				t.Fatalf("query arg %q is not preceded by -f: %q", a, argv)
			}
			return strings.TrimPrefix(a, "query=")
		}
	}
	t.Fatalf("no query= argument in %q", argv)
	return ""
}

var aliasRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])n(\d+):`)

func queryAliases(query string) []string {
	var out []string
	for _, m := range aliasRE.FindAllStringSubmatch(query, -1) {
		out = append(out, m[1])
	}
	return out
}

// --- canned GraphQL bodies ---

func issueJSON(state string, hasNext bool, linkedMerged ...bool) string {
	nodes := make([]string, len(linkedMerged))
	for i, m := range linkedMerged {
		nodes[i] = fmt.Sprintf(`{"merged":%t}`, m)
	}
	return fmt.Sprintf(`{"__typename":"Issue","state":%q,"closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":%t},"nodes":[%s]}}`,
		state, hasNext, strings.Join(nodes, ","))
}

// openIssueClosing builds an OPEN issue whose closedByPullRequestsReferences
// has hasNextPage:false and the given raw extra member (e.g. `"nodes":[null]`),
// for shapes issueJSON cannot express.
func openIssueClosing(extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return `{"__typename":"Issue","state":"OPEN","closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":false}` + extra + `}}`
}

func prJSON(state string, merged bool) string {
	return fmt.Sprintf(`{"__typename":"PullRequest","state":%q,"merged":%t}`, state, merged)
}

// repoBody builds a successful response keyed by the n<N> aliases the reader sends.
func repoBody(nodes map[int]string) string {
	parts := make([]string, 0, len(nodes))
	for n, node := range nodes {
		parts = append(parts, fmt.Sprintf(`"n%d":%s`, n, node))
	}
	return `{"data":{"repository":{` + strings.Join(parts, ",") + `}}}`
}

func withErrors(body, errorsJSON string) string {
	return strings.TrimSuffix(body, "}") + `,"errors":` + errorsJSON + `}`
}

func closedNodes(nums ...int) map[int]string {
	m := make(map[int]string, len(nums))
	for _, n := range nums {
		m[n] = issueJSON("CLOSED", false)
	}
	return m
}

func rangeInts(from, to int) []int {
	var out []int
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

// requireResult fetches n's result and enforces the Err-iff-Unknown contract.
func requireResult(t *testing.T, got map[int]Result, n int) Result {
	t.Helper()
	r, ok := got[n]
	if !ok {
		t.Fatalf("no result for #%d in %v", n, got)
	}
	if (r.Verdict == Unknown) != (r.Err != nil) {
		t.Errorf("#%d: Verdict=%v Err=%v, want Err non-nil exactly when Unknown", n, r.Verdict, r.Err)
	}
	return r
}

func assertVerdict(t *testing.T, got map[int]Result, n int, want Verdict) {
	t.Helper()
	if r := requireResult(t, got, n); r.Verdict != want {
		t.Errorf("#%d verdict = %v (err %v), want %v", n, r.Verdict, r.Err, want)
	}
}

// --- verdict table: one explicit case per row ---

func TestReadVerdictTable(t *testing.T) {
	const n = 7
	cases := []struct {
		name string
		node string
		want Verdict
	}{
		{"issue CLOSED", issueJSON("CLOSED", false), Finished},
		{"issue CLOSED even with only an unmerged linked PR", issueJSON("CLOSED", false, false), Finished},
		{"issue OPEN with a merged linked PR", issueJSON("OPEN", false, true), Finished},
		{"issue OPEN with one merged among unmerged linked PRs", issueJSON("OPEN", false, false, true, false), Finished},
		{"issue OPEN with a merged linked PR even if the list is truncated", issueJSON("OPEN", true, true), Finished},
		{"issue OPEN with an unmerged linked PR", issueJSON("OPEN", false, false), NotFinished},
		{"issue OPEN with no linked PRs", issueJSON("OPEN", false), NotFinished},
		{"issue OPEN, unmerged, closing list truncated", issueJSON("OPEN", true, false), Unknown},
		{"issue OPEN, no linked PRs seen, closing list truncated", issueJSON("OPEN", true), Unknown},
		{"PR merged", prJSON("MERGED", true), Finished},
		{"PR merged:true with CLOSED state", prJSON("CLOSED", true), Finished},
		{"PR closed without merging", prJSON("CLOSED", false), Finished},
		{"PR open", prJSON("OPEN", false), NotFinished},
		{"unrecognized __typename", `{"__typename":"Discussion","state":"CLOSED"}`, Unknown},
		{"missing __typename", `{"state":"CLOSED"}`, Unknown},
		{"issue with an unrecognized state", issueJSON("ARCHIVED", false), Unknown},
		{"issue with no state", `{"__typename":"Issue","closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":false},"nodes":[]}}`, Unknown},
		{"PR with an unrecognized state", prJSON("DRAFT", false), Unknown},
		{"null node", `null`, Unknown},
		// An open issue whose closing-PR list is malformed cannot prove
		// NotFinished: a null or merged-less entry might be the merged PR.
		{"issue OPEN, closing nodes holds a null entry", openIssueClosing(`"nodes":[null]`), Unknown},
		{"issue OPEN, closing node omits merged", openIssueClosing(`"nodes":[{}]`), Unknown},
		{"issue OPEN, closing node has merged:null", openIssueClosing(`"nodes":[{"merged":null}]`), Unknown},
		{"issue OPEN, closing nodes key absent", openIssueClosing(``), Unknown},
		{"issue OPEN, closing nodes is null", openIssueClosing(`"nodes":null`), Unknown},
		{"issue OPEN, unmerged then merged-less closing nodes", openIssueClosing(`"nodes":[{"merged":false},{}]`), Unknown},
		{"issue OPEN, null entry beside a merged PR still wins", openIssueClosing(`"nodes":[null,{"merged":true}]`), Finished},
		{"issue OPEN, empty closing nodes list", openIssueClosing(`"nodes":[]`), NotFinished},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := installFakeGh(t)
			gh.respond(t, repoBody(map[int]string{n: tc.node}), 0, "")

			got := (&Reader{}).Read(context.Background(), testRepo, []int{n})
			assertVerdict(t, got, n, tc.want)
			if len(got) != 1 {
				t.Errorf("results = %v, want exactly one entry", got)
			}
			if c := gh.calls(t); len(c) != 1 {
				t.Errorf("gh calls = %d, want 1", len(c))
			}
		})
	}
}

func TestReadMissingAliasIsUnknownAndOthersStillClassified(t *testing.T) {
	cases := []struct {
		name  string
		nodes map[int]string
		raw   string  // when set, replaces the built body
		want8 Verdict // #8's expected verdict in this case
	}{
		{name: "alias absent from repository", nodes: closedNodes(8), want8: Finished},
		{name: "empty repository object", nodes: map[int]string{}, want8: Unknown},
		{name: "alias key differs in case", raw: `{"data":{"repository":{"N7":` + issueJSON("CLOSED", false) + `,"n8":` + issueJSON("CLOSED", false) + `}}}`, want8: Finished},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.raw
			if body == "" {
				body = repoBody(tc.nodes)
			}
			gh := installFakeGh(t)
			gh.respond(t, body, 0, "")

			got := (&Reader{}).Read(context.Background(), testRepo, []int{7, 8})
			assertVerdict(t, got, 7, Unknown)
			assertVerdict(t, got, 8, tc.want8)
		})
	}
}

func TestVerdictString(t *testing.T) {
	for v, want := range map[Verdict]string{Unknown: "Unknown", NotFinished: "NotFinished", Finished: "Finished"} {
		if got := v.String(); got != want {
			t.Errorf("Verdict(%d).String() = %q, want %q", int(v), got, want)
		}
	}
	got := Verdict(99).String()
	if got == "Finished" || got == "NotFinished" || got == "Unknown" {
		t.Errorf("an out-of-range Verdict stringifies as the valid name %q", got)
	}
	if !strings.Contains(got, "99") {
		t.Errorf("Verdict(99).String() = %q, want it to carry the raw value", got)
	}
}

// --- batching and argv ---

func TestReadMakesOneCallPerBatchOfAtMostFifty(t *testing.T) {
	cases := []struct {
		count     int
		wantCalls int
	}{{1, 1}, {50, 1}, {51, 2}, {100, 2}, {101, 3}}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d numbers", tc.count), func(t *testing.T) {
			gh := installFakeGh(t)
			gh.respondAllClosed(t)
			nums := rangeInts(1000, 1000+tc.count-1)

			got := (&Reader{}).Read(context.Background(), testRepo, nums)

			calls := gh.calls(t)
			if len(calls) != tc.wantCalls {
				t.Fatalf("gh calls = %d, want %d", len(calls), tc.wantCalls)
			}
			seen := map[string]bool{}
			for _, argv := range calls {
				aliases := queryAliases(queryArg(t, argv))
				if len(aliases) > MaxBatch {
					t.Errorf("a call carried %d numbers, want at most %d", len(aliases), MaxBatch)
				}
				for _, a := range aliases {
					if seen[a] {
						t.Errorf("number %s was sent in more than one call", a)
					}
					seen[a] = true
				}
			}
			if len(seen) != tc.count {
				t.Errorf("numbers sent = %d, want %d", len(seen), tc.count)
			}
			if len(got) != tc.count {
				t.Errorf("results = %d, want %d", len(got), tc.count)
			}
			for _, n := range nums {
				assertVerdict(t, got, n, Finished)
			}
		})
	}
}

func TestReadPassesOwnerAndNameOnlyAsVariablesAndPinsTheQuery(t *testing.T) {
	gh := installFakeGh(t)
	gh.respond(t, repoBody(closedNodes(7)), 0, "")

	(&Reader{}).Read(context.Background(), testRepo, []int{7})

	calls := gh.calls(t)
	if len(calls) != 1 {
		t.Fatalf("gh calls = %d, want 1", len(calls))
	}
	argv := calls[0]
	if len(argv) < 2 || argv[0] != "api" || argv[1] != "graphql" {
		t.Fatalf("argv = %q, want it to start with `api graphql`", argv)
	}
	query := queryArg(t, argv)

	for _, v := range []struct{ arg, value string }{{"owner=" + testRepo.Owner, testRepo.Owner}, {"name=" + testRepo.Name, testRepo.Name}} {
		found := false
		for i, a := range argv {
			if a == v.arg && i > 0 && argv[i-1] == "-f" {
				found = true
			}
		}
		if !found {
			t.Errorf("argv = %q, want `-f %s`", argv, v.arg)
		}
		if strings.Contains(query, v.value) {
			t.Errorf("query splices %q into the query text: %s", v.value, query)
		}
		for _, a := range argv {
			if a != v.arg && strings.Contains(a, v.value) {
				t.Errorf("argument %q carries %q outside its -f variable", a, v.value)
			}
		}
	}
	for _, want := range []string{
		"$owner", "$name",
		"closedByPullRequestsReferences(first: 25, includeClosedPrs: true)",
		"issueOrPullRequest(number:7)",
		"n7:",
	} {
		if !strings.Contains(query, want) {
			t.Errorf("query is missing %q:\n%s", want, query)
		}
	}
	if strings.ContainsAny(query, "\r\n") {
		t.Errorf("query must be a single line:\n%q", query)
	}
}

func TestReadSendsEachDistinctNumberOnce(t *testing.T) {
	gh := installFakeGh(t)
	gh.respond(t, repoBody(closedNodes(5, 9)), 0, "")

	got := (&Reader{}).Read(context.Background(), testRepo, []int{5, 5, 9, 5})

	calls := gh.calls(t)
	if len(calls) != 1 {
		t.Fatalf("gh calls = %d, want 1", len(calls))
	}
	if aliases := queryAliases(queryArg(t, calls[0])); len(aliases) != 2 {
		t.Errorf("query aliases = %v, want one each for 5 and 9", aliases)
	}
	if len(got) != 2 {
		t.Errorf("results = %v, want entries for 5 and 9 only", got)
	}
	assertVerdict(t, got, 5, Finished)
	assertVerdict(t, got, 9, Finished)
}

func TestReadNonPositiveNumbersGetAnErrorAndAreNeverSent(t *testing.T) {
	t.Run("mixed with a valid number", func(t *testing.T) {
		gh := installFakeGh(t)
		gh.respond(t, repoBody(closedNodes(7)), 0, "")

		got := (&Reader{}).Read(context.Background(), testRepo, []int{0, -3, 7})

		assertVerdict(t, got, 0, Unknown)
		assertVerdict(t, got, -3, Unknown)
		assertVerdict(t, got, 7, Finished)
		calls := gh.calls(t)
		if len(calls) != 1 {
			t.Fatalf("gh calls = %d, want 1", len(calls))
		}
		query := queryArg(t, calls[0])
		if aliases := queryAliases(query); len(aliases) != 1 || aliases[0] != "7" {
			t.Errorf("query aliases = %v, want only 7:\n%s", aliases, query)
		}
		if strings.Contains(query, "-3") {
			t.Errorf("query carries the negative number:\n%s", query)
		}
	})
	t.Run("only invalid numbers make no call", func(t *testing.T) {
		gh := installFakeGh(t)
		gh.respond(t, repoBody(nil), 0, "")

		got := (&Reader{}).Read(context.Background(), testRepo, []int{0, -1})

		assertVerdict(t, got, 0, Unknown)
		assertVerdict(t, got, -1, Unknown)
		if c := gh.calls(t); len(c) != 0 {
			t.Errorf("gh was called %d time(s) with nothing valid to ask", len(c))
		}
	})
}

// TestReadNumbersBeyondGraphQLIntAreErroredAndNeverSent: GraphQL Int is 32-bit,
// and an out-of-range literal is an un-aliased coercion error that would fail
// the whole batch, so such a number is rejected on its own before any call.
func TestReadNumbersBeyondGraphQLIntAreErroredAndNeverSent(t *testing.T) {
	const tooBig = math.MaxInt32 + 1
	t.Run("mixed with valid numbers, including the largest valid one", func(t *testing.T) {
		gh := installFakeGh(t)
		gh.respondAllClosed(t)

		got := (&Reader{}).Read(context.Background(), testRepo, []int{7, tooBig, math.MaxInt32})

		assertVerdict(t, got, tooBig, Unknown)
		if err := got[tooBig].Err; err != nil && !strings.Contains(err.Error(), strconv.Itoa(tooBig)) {
			t.Errorf("err = %q, want it to name the rejected number", err)
		}
		assertVerdict(t, got, 7, Finished)
		assertVerdict(t, got, math.MaxInt32, Finished)
		calls := gh.calls(t)
		if len(calls) != 1 {
			t.Fatalf("gh calls = %d, want 1", len(calls))
		}
		for _, a := range calls[0] {
			if strings.Contains(a, strconv.Itoa(tooBig)) {
				t.Errorf("argument %q carries the out-of-range number", a)
			}
		}
		if aliases := queryAliases(queryArg(t, calls[0])); len(aliases) != 2 {
			t.Errorf("query aliases = %v, want only 7 and %d", aliases, math.MaxInt32)
		}
	})
	t.Run("only an out-of-range number makes no call", func(t *testing.T) {
		gh := installFakeGh(t)
		gh.respondAllClosed(t)

		got := (&Reader{}).Read(context.Background(), testRepo, []int{tooBig})

		assertVerdict(t, got, tooBig, Unknown)
		if c := gh.calls(t); len(c) != 0 {
			t.Errorf("gh was called %d time(s) with nothing valid to ask", len(c))
		}
	})
}

func TestReadEmptyInputMakesNoCall(t *testing.T) {
	for name, nums := range map[string][]int{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			gh := installFakeGh(t)
			gh.respond(t, repoBody(nil), 0, "")

			got := (&Reader{}).Read(context.Background(), testRepo, nums)

			if len(got) != 0 {
				t.Errorf("results = %v, want none", got)
			}
			if c := gh.calls(t); len(c) != 0 {
				t.Errorf("gh was called %d time(s) for no numbers", len(c))
			}
		})
	}
}

func TestReadInvalidRepoErrorsEveryNumberAndMakesNoCall(t *testing.T) {
	cases := []struct {
		name string
		repo Repo
	}{
		{"empty owner", Repo{Owner: "", Name: "frobnicate"}},
		{"empty name", Repo{Owner: "zorgon-labs", Name: ""}},
		{"owner with a leading dash", Repo{Owner: "-evil", Name: "frobnicate"}},
		{"name with a leading dash", Repo{Owner: "zorgon-labs", Name: "-evil"}},
		{"owner with a slash", Repo{Owner: "a/b", Name: "frobnicate"}},
		{"name with a slash", Repo{Owner: "zorgon-labs", Name: "a/b"}},
		{"owner with a space", Repo{Owner: "a b", Name: "frobnicate"}},
		{"name with a quote", Repo{Owner: "zorgon-labs", Name: `x"y`}},
		{"owner with a newline", Repo{Owner: "a\nb", Name: "frobnicate"}},
		{"name with shell metacharacters", Repo{Owner: "zorgon-labs", Name: "x;rm"}},
	}
	nums := []int{1, 2, 3}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := installFakeGh(t)
			gh.respond(t, repoBody(closedNodes(nums...)), 0, "")

			got := (&Reader{}).Read(context.Background(), tc.repo, nums)

			for _, n := range nums {
				assertVerdict(t, got, n, Unknown)
			}
			if c := gh.calls(t); len(c) != 0 {
				t.Errorf("gh was called %d time(s) for an invalid repo %+v", len(c), tc.repo)
			}
		})
	}
}

// --- whole-call failures: every number errors, nothing is Finished ---

func TestReadWholeCallFailuresErrorEveryNumber(t *testing.T) {
	nums := []int{1, 2, 3}
	decodable := repoBody(closedNodes(nums...)) // would read as Finished if trusted
	cases := []struct {
		name  string
		setup func(t *testing.T, gh *fakeGh)
		check func(t *testing.T, err error)
	}{
		{
			name:  "non-zero exit with no body",
			setup: func(t *testing.T, gh *fakeGh) { gh.respond(t, "", 1, "gh: HTTP 502 upstream\nsecond line") },
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "HTTP 502") {
					t.Errorf("err = %q, want gh's stderr surfaced", err)
				}
				if strings.ContainsAny(err.Error(), "\r\n") {
					t.Errorf("err = %q, want newlines collapsed", err)
				}
			},
		},
		{
			name: "timeout",
			setup: func(t *testing.T, gh *fakeGh) {
				setVar(t, &ghTimeout, 300*time.Millisecond)
				gh.behave(t, `exec sleep 30`)
			},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, errTimeout) {
					t.Errorf("err = %v, want errors.Is(_, errTimeout)", err)
				}
			},
		},
		{
			name:  "malformed JSON",
			setup: func(t *testing.T, gh *fakeGh) { gh.respond(t, "<html>oops</html>", 0, "") },
			check: func(t *testing.T, err error) {
				var syntaxErr *json.SyntaxError
				if !errors.As(err, &syntaxErr) {
					t.Errorf("err = %v, want it to wrap a *json.SyntaxError", err)
				}
			},
		},
		{
			name:  "empty stdout with exit 0",
			setup: func(t *testing.T, gh *fakeGh) { gh.respond(t, "", 0, "") },
		},
		{
			name:  "null repository",
			setup: func(t *testing.T, gh *fakeGh) { gh.respond(t, `{"data":{"repository":null}}`, 0, "") },
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "repository") {
					t.Errorf("err = %q, want it to name the repository", err)
				}
			},
		},
		{
			name:  "missing repository",
			setup: func(t *testing.T, gh *fakeGh) { gh.respond(t, `{"data":{}}`, 0, "") },
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "repository") {
					t.Errorf("err = %q, want it to name the repository", err)
				}
			},
		},
		{
			name: "null repository with a NOT_FOUND error and exit 1",
			setup: func(t *testing.T, gh *fakeGh) {
				gh.respond(t, `{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","path":["repository"],"message":"Could not resolve to a Repository with the name 'zorgon-labs/frobnicate'."}]}`, 1, "")
			},
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "Could not resolve to a Repository") {
					t.Errorf("err = %q, want the GraphQL error message surfaced", err)
				}
			},
		},
		{
			// gh prints an HTTP failure body with no repository key and exits
			// non-zero; the exit and stderr are the only explanation.
			name: "auth failure body with no repository, exit 1",
			setup: func(t *testing.T, gh *fakeGh) {
				gh.respond(t, `{"message":"Bad credentials"}`, 1, "gh: HTTP 401: Bad credentials")
			},
			check: func(t *testing.T, err error) {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Errorf("err = %v, want it to wrap the *exec.ExitError", err)
				}
				if !strings.Contains(err.Error(), "HTTP 401") {
					t.Errorf("err = %q, want gh's stderr surfaced", err)
				}
			},
		},
		{
			name: "stdout over the cap",
			setup: func(t *testing.T, gh *fakeGh) {
				gh.behave(t, `head -c 1200000 /dev/zero | tr '\0' x`)
			},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, errOutputTruncated) {
					t.Errorf("err = %v, want errors.Is(_, errOutputTruncated)", err)
				}
			},
		},
		{
			name: "errors[] entry with no path",
			setup: func(t *testing.T, gh *fakeGh) {
				gh.respond(t, withErrors(decodable, `[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]`), 0, "")
			},
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "API rate limit exceeded") {
					t.Errorf("err = %q, want the GraphQL error message surfaced", err)
				}
			},
		},
		{
			name: "errors[] path names an alias outside the batch",
			setup: func(t *testing.T, gh *fakeGh) {
				gh.respond(t, withErrors(decodable, `[{"path":["repository","n99"],"message":"boom-unknown-alias"}]`), 1, "")
			},
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "boom-unknown-alias") {
					t.Errorf("err = %q, want the GraphQL error message surfaced", err)
				}
			},
		},
		{
			name: "errors[] path stops short of an alias",
			setup: func(t *testing.T, gh *fakeGh) {
				gh.respond(t, withErrors(decodable, `[{"path":["repository"],"message":"boom-short-path"}]`), 1, "")
			},
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "boom-short-path") {
					t.Errorf("err = %q, want the GraphQL error message surfaced", err)
				}
			},
		},
		{
			name: "one aliased and one un-aliased errors[] entry",
			setup: func(t *testing.T, gh *fakeGh) {
				gh.respond(t, withErrors(decodable, `[{"path":["repository","n2"],"message":"tied"},{"message":"boom-untied"}]`), 1, "")
			},
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "boom-untied") {
					t.Errorf("err = %q, want the un-aliased GraphQL error surfaced", err)
				}
			},
		},
		{
			// A non-zero exit that explains nothing is not a partial read: with
			// no errors[] entry to attribute it to, no alias may be trusted.
			name:  "exit 1 with a decodable body and no errors[]",
			setup: func(t *testing.T, gh *fakeGh) { gh.respond(t, decodable, 1, "gh: something went wrong") },
			check: func(t *testing.T, err error) {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Errorf("err = %v, want it to wrap the *exec.ExitError", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := installFakeGh(t)
			tc.setup(t, gh)

			got := (&Reader{}).Read(context.Background(), testRepo, nums)

			if len(got) != len(nums) {
				t.Fatalf("results = %v, want an entry for every requested number", got)
			}
			for _, n := range nums {
				r := requireResult(t, got, n)
				if r.Verdict == Finished {
					t.Errorf("#%d is Finished despite a failed read", n)
				}
				if r.Verdict != Unknown || r.Err == nil {
					t.Errorf("#%d = %+v, want Unknown with an error", n, r)
					continue
				}
				if tc.check != nil {
					tc.check(t, r.Err)
				}
			}
		})
	}
}

// --- per-alias failures (plan Q1: partial classification) ---

func TestReadPartialClassificationMarksOnlyTheFailedAliasUnknown(t *testing.T) {
	const notFound = "Could not resolve to an issue or pull request with the number of 2."
	cases := []struct {
		name        string
		body        string
		wantUnknown []int
		wantMsg     string
	}{
		{
			name: "exit 1, null node, path tagged with the alias",
			body: withErrors(repoBody(map[int]string{1: issueJSON("CLOSED", false), 2: "null", 3: prJSON("OPEN", false)}),
				`[{"type":"NOT_FOUND","path":["repository","n2"],"message":"`+notFound+`"}]`),
			wantUnknown: []int{2},
			wantMsg:     notFound,
		},
		{
			name: "path runs deeper than the alias",
			body: withErrors(repoBody(map[int]string{1: issueJSON("CLOSED", false), 2: "null", 3: prJSON("OPEN", false)}),
				`[{"type":"NOT_FOUND","path":["repository","n2","state"],"message":"`+notFound+`"}]`),
			wantUnknown: []int{2},
			wantMsg:     notFound,
		},
		{
			name: "an errored alias is Unknown even when its node would read Finished",
			body: withErrors(repoBody(map[int]string{1: issueJSON("CLOSED", false), 2: issueJSON("CLOSED", false), 3: prJSON("OPEN", false)}),
				`[{"path":["repository","n2"],"message":"`+notFound+`"}]`),
			wantUnknown: []int{2},
			wantMsg:     notFound,
		},
		{
			name: "several errored aliases",
			body: withErrors(repoBody(map[int]string{1: "null", 2: issueJSON("CLOSED", false), 3: "null"}),
				`[{"path":["repository","n1"],"message":"gone-1"},{"path":["repository","n3"],"message":"gone-3"}]`),
			wantUnknown: []int{1, 3},
			wantMsg:     "gone-",
		},
	}
	// Unaffected aliases keep these verdicts in every case above.
	baseline := map[int]Verdict{1: Finished, 2: Finished, 3: NotFinished}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := installFakeGh(t)
			gh.respond(t, tc.body, 1, "gh: "+notFound)

			got := (&Reader{}).Read(context.Background(), testRepo, []int{1, 2, 3})

			unknown := map[int]bool{}
			for _, n := range tc.wantUnknown {
				unknown[n] = true
			}
			for _, n := range []int{1, 2, 3} {
				r := requireResult(t, got, n)
				if unknown[n] {
					if r.Verdict != Unknown || r.Err == nil {
						t.Errorf("#%d = %+v, want Unknown with an error", n, r)
					} else if !strings.Contains(r.Err.Error(), tc.wantMsg) {
						t.Errorf("#%d err = %q, want it to carry the GraphQL message %q", n, r.Err, tc.wantMsg)
					}
					continue
				}
				if want := baseline[n]; r.Verdict != want {
					t.Errorf("#%d verdict = %v (err %v), want %v: one bad alias must not poison the rest", n, r.Verdict, r.Err, want)
				}
			}
		})
	}
}

// --- ReadOutcomes: detailed per-number outcome with merged head SHAs (#1263) ---

// Valid head SHAs: lowercase hex of 40 (SHA-1) or 64 (SHA-256) characters.
var (
	headA     = strings.Repeat("ab", 20)
	headB     = strings.Repeat("cd", 20)
	headSHA64 = strings.Repeat("ef", 32)
)

const (
	// noHead omits the headRefOid key from a built node.
	noHead = ""
	// nullHead renders headRefOid as JSON null.
	nullHead = "<null>"
)

func headField(head string) string {
	switch head {
	case noHead:
		return ""
	case nullHead:
		return `,"headRefOid":null`
	default:
		return fmt.Sprintf(`,"headRefOid":%q`, head)
	}
}

// closingPR builds one closedByPullRequestsReferences node.
func closingPR(merged bool, head string) string {
	return fmt.Sprintf(`{"merged":%t%s}`, merged, headField(head))
}

// issueWithClosing builds an Issue whose closing-PR list holds the given raw nodes.
func issueWithClosing(state string, hasNext bool, nodes ...string) string {
	return fmt.Sprintf(`{"__typename":"Issue","state":%q,"closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":%t},"nodes":[%s]}}`,
		state, hasNext, strings.Join(nodes, ","))
}

// issueWithClosingRaw builds an Issue whose closedByPullRequestsReferences has
// hasNextPage:false plus the given raw extra member, for malformed list shapes.
func issueWithClosingRaw(state, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return fmt.Sprintf(`{"__typename":"Issue","state":%q,"closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":false}%s}}`, state, extra)
}

// prWithHead builds a PullRequest node carrying its own head SHA.
func prWithHead(state string, merged bool, head string) string {
	return fmt.Sprintf(`{"__typename":"PullRequest","state":%q,"merged":%t%s}`, state, merged, headField(head))
}

// requireOutcome fetches n's result and enforces the contract that Err is
// non-nil exactly when the outcome is Unknown and Heads is non-empty exactly
// when the outcome is PR merged.
func requireOutcome(t *testing.T, got map[int]OutcomeResult, n int) OutcomeResult {
	t.Helper()
	r, ok := got[n]
	if !ok {
		t.Fatalf("no result for #%d in %v", n, got)
	}
	if (r.Outcome == OutcomeUnknown) != (r.Err != nil) {
		t.Errorf("#%d: Outcome=%v Err=%v, want Err non-nil exactly when Unknown", n, r.Outcome, r.Err)
	}
	if (r.Outcome == OutcomePRMerged) != (len(r.Heads) > 0) {
		t.Errorf("#%d: Outcome=%v Heads=%v, want Heads non-empty exactly when PR merged", n, r.Outcome, r.Heads)
	}
	return r
}

func assertOutcome(t *testing.T, got map[int]OutcomeResult, n int, want Outcome) {
	t.Helper()
	if r := requireOutcome(t, got, n); r.Outcome != want {
		t.Errorf("#%d outcome = %v (err %v), want %v", n, r.Outcome, r.Err, want)
	}
}

func TestOutcomeString(t *testing.T) {
	var zero Outcome
	if zero != OutcomeUnknown {
		t.Errorf("the zero Outcome is %v, want OutcomeUnknown so an unset value never reads as removable", zero)
	}
	valid := []Outcome{OutcomeUnknown, OutcomeOpen, OutcomeIssueClosed, OutcomePRMerged, OutcomePRClosedUnmerged}
	names := map[string]Outcome{}
	for _, o := range valid {
		name := o.String()
		if name == "" || strings.Contains(name, "Outcome(") {
			t.Errorf("Outcome(%d).String() = %q, want a real name", int(o), name)
		}
		if prev, dup := names[name]; dup {
			t.Errorf("Outcome(%d) and Outcome(%d) share the name %q", int(prev), int(o), name)
		}
		names[name] = o
	}
	for _, bad := range []Outcome{99, -1} {
		got := bad.String()
		if _, isValidName := names[got]; isValidName {
			t.Errorf("an out-of-range Outcome stringifies as the valid name %q", got)
		}
		if want := fmt.Sprintf("Outcome(%d)", int(bad)); got != want {
			t.Errorf("Outcome(%d).String() = %q, want %q", int(bad), got, want)
		}
	}
}

// TestReadOutcomesTable: one explicit row per classification, one number, one
// gh call. Every row also enforces the Err-iff-Unknown and Heads-iff-PR-merged
// contract through requireOutcome.
func TestReadOutcomesTable(t *testing.T) {
	const n = 7
	cases := []struct {
		name      string
		node      string
		want      Outcome
		wantHeads []string // compared as a set, order-insensitive
	}{
		// PR numbers.
		{"PR merged with a valid SHA", prWithHead("MERGED", true, headA), OutcomePRMerged, []string{headA}},
		{"PR merged with a 64-char SHA", prWithHead("MERGED", true, headSHA64), OutcomePRMerged, []string{headSHA64}},
		{"PR merged:true with CLOSED state", prWithHead("CLOSED", true, headA), OutcomePRMerged, []string{headA}},
		{"PR closed without merging", prWithHead("CLOSED", false, headA), OutcomePRClosedUnmerged, nil},
		{"PR open", prWithHead("OPEN", false, headA), OutcomeOpen, nil},
		{"PR merged with a null head SHA", prWithHead("MERGED", true, nullHead), OutcomeUnknown, nil},
		{"PR merged with no head SHA", prWithHead("MERGED", true, noHead), OutcomeUnknown, nil},
		{"PR merged with an uppercase head SHA", prWithHead("MERGED", true, strings.ToUpper(headA)), OutcomeUnknown, nil},
		{"PR merged with a short head SHA", prWithHead("MERGED", true, headA[:39]), OutcomeUnknown, nil},
		{"PR merged with an over-long head SHA", prWithHead("MERGED", true, headA+"a"), OutcomeUnknown, nil},
		{"PR merged with a non-hex head SHA", prWithHead("MERGED", true, strings.Repeat("g", 40)), OutcomeUnknown, nil},
		{"PR merged with a head SHA starting with a dash", prWithHead("MERGED", true, "-"+headA[1:]), OutcomeUnknown, nil},
		{"PR CLOSED with a null merged flag", `{"__typename":"PullRequest","state":"CLOSED","merged":null}`, OutcomeUnknown, nil},
		{"PR CLOSED with no merged flag", `{"__typename":"PullRequest","state":"CLOSED"}`, OutcomeUnknown, nil},

		// Issue numbers.
		{"issue CLOSED with no closing PRs", issueWithClosing("CLOSED", false), OutcomeIssueClosed, nil},
		{"issue CLOSED with only unmerged closing PRs", issueWithClosing("CLOSED", false, closingPR(false, noHead), closingPR(false, headA)), OutcomeIssueClosed, nil},
		{"issue CLOSED with a merged closing PR", issueWithClosing("CLOSED", false, closingPR(true, headA)), OutcomePRMerged, []string{headA}},
		{"issue OPEN with a merged closing PR", issueWithClosing("OPEN", false, closingPR(true, headA)), OutcomePRMerged, []string{headA}},
		{"issue with several merged closing PRs and a duplicate SHA",
			issueWithClosing("CLOSED", false, closingPR(true, headA), closingPR(false, headSHA64), closingPR(true, headB), closingPR(true, headA)),
			OutcomePRMerged, []string{headA, headB}},
		{"issue OPEN with an unmerged closing PR", issueWithClosing("OPEN", false, closingPR(false, headA)), OutcomeOpen, nil},
		{"issue OPEN with an empty closing list", issueWithClosing("OPEN", false), OutcomeOpen, nil},
		{"issue OPEN, closing list truncated, no merge", issueWithClosing("OPEN", true, closingPR(false, headA)), OutcomeUnknown, nil},
		{"issue CLOSED, closing list truncated, no merge", issueWithClosing("CLOSED", true, closingPR(false, headA)), OutcomeUnknown, nil},
		{"issue OPEN, truncated list that still holds a merged PR", issueWithClosing("OPEN", true, closingPR(true, headA)), OutcomePRMerged, []string{headA}},
		{"issue CLOSED, truncated list that still holds a merged PR", issueWithClosing("CLOSED", true, closingPR(true, headA)), OutcomePRMerged, []string{headA}},
		{"issue OPEN, unmerged closing PR with a null head SHA is ignored", issueWithClosing("OPEN", false, closingPR(false, nullHead)), OutcomeOpen, nil},
		{"issue CLOSED, unmerged closing PR with a null head SHA is ignored", issueWithClosing("CLOSED", false, closingPR(false, nullHead)), OutcomeIssueClosed, nil},
		{"issue CLOSED, unmerged closing PR with a malformed head SHA is ignored", issueWithClosing("CLOSED", false, closingPR(false, "NOT-A-SHA")), OutcomeIssueClosed, nil},
		{"issue CLOSED, merged closing PR with a null head SHA", issueWithClosing("CLOSED", false, closingPR(true, nullHead)), OutcomeUnknown, nil},
		{"issue OPEN, merged closing PR with no head SHA", issueWithClosing("OPEN", false, closingPR(true, noHead)), OutcomeUnknown, nil},
		{"issue CLOSED, malformed merged SHA beside a valid one", issueWithClosing("CLOSED", false, closingPR(true, headA), closingPR(true, "xyz")), OutcomeUnknown, nil},
		{"issue OPEN, malformed merged SHA beside a valid one", issueWithClosing("OPEN", false, closingPR(true, "xyz"), closingPR(true, headA)), OutcomeUnknown, nil},

		// Malformed closing-PR lists with no merge seen: neither open nor closed
		// can be proven.
		{"issue OPEN, closing nodes holds a null entry", issueWithClosingRaw("OPEN", `"nodes":[null]`), OutcomeUnknown, nil},
		{"issue CLOSED, closing nodes holds a null entry", issueWithClosingRaw("CLOSED", `"nodes":[null]`), OutcomeUnknown, nil},
		{"issue OPEN, closing node omits merged", issueWithClosingRaw("OPEN", `"nodes":[{}]`), OutcomeUnknown, nil},
		{"issue CLOSED, closing node omits merged", issueWithClosingRaw("CLOSED", `"nodes":[{}]`), OutcomeUnknown, nil},
		{"issue OPEN, closing node has merged:null", issueWithClosingRaw("OPEN", `"nodes":[{"merged":null}]`), OutcomeUnknown, nil},
		{"issue CLOSED, closing node has merged:null", issueWithClosingRaw("CLOSED", `"nodes":[{"merged":null}]`), OutcomeUnknown, nil},
		{"issue OPEN, closing nodes key absent", issueWithClosingRaw("OPEN", ``), OutcomeUnknown, nil},
		{"issue CLOSED, closing nodes key absent", issueWithClosingRaw("CLOSED", ``), OutcomeUnknown, nil},
		{"issue OPEN, closing nodes is null", issueWithClosingRaw("OPEN", `"nodes":null`), OutcomeUnknown, nil},
		{"issue CLOSED, closing nodes is null", issueWithClosingRaw("CLOSED", `"nodes":null`), OutcomeUnknown, nil},
		{"issue OPEN, no closing-PR field at all", `{"__typename":"Issue","state":"OPEN"}`, OutcomeUnknown, nil},
		{"issue CLOSED, no closing-PR field at all", `{"__typename":"Issue","state":"CLOSED"}`, OutcomeUnknown, nil},

		// Generic unrecognized shapes.
		{"unrecognized __typename", `{"__typename":"Discussion","state":"CLOSED"}`, OutcomeUnknown, nil},
		{"missing __typename", `{"state":"CLOSED"}`, OutcomeUnknown, nil},
		{"issue with an unrecognized state", issueWithClosing("ARCHIVED", false), OutcomeUnknown, nil},
		{"PR with an unrecognized state", prWithHead("DRAFT", false, headA), OutcomeUnknown, nil},
		{"null node", `null`, OutcomeUnknown, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := installFakeGh(t)
			gh.respond(t, repoBody(map[int]string{n: tc.node}), 0, "")

			got := (&Reader{}).ReadOutcomes(context.Background(), testRepo, []int{n})

			r := requireOutcome(t, got, n)
			if r.Outcome != tc.want {
				t.Errorf("outcome = %v (err %v), want %v", r.Outcome, r.Err, tc.want)
			}
			gotHeads := slices.Sorted(slices.Values(r.Heads))
			wantHeads := slices.Sorted(slices.Values(tc.wantHeads))
			if !slices.Equal(gotHeads, wantHeads) {
				t.Errorf("heads = %v, want %v (each exactly once)", r.Heads, tc.wantHeads)
			}
			if len(got) != 1 {
				t.Errorf("results = %v, want exactly one entry", got)
			}
			if c := gh.calls(t); len(c) != 1 {
				t.Errorf("gh calls = %d, want 1", len(c))
			}
		})
	}
}

// TestMalformedPaginationAndWrongTypedFieldsAreUnknown pins the fail-closed
// reading of a missing, null or wrongly typed pagination flag and of wrongly
// typed fields: ReadOutcomes must answer Unknown with a cause for every row, so
// no malformed shape can read as "not truncated" or be swallowed by the decoder.
// Read is checked where it has the same contract: it answers Unknown for an OPEN
// issue's malformed pagination and for any decode failure, but a CLOSED issue is
// Finished without consulting its closing list, so those rows skip Read.
func TestMalformedPaginationAndWrongTypedFieldsAreUnknown(t *testing.T) {
	closingWith := func(state, closing string) string {
		return fmt.Sprintf(`{"__typename":"Issue","state":%q,"closedByPullRequestsReferences":{%s}}`, state, closing)
	}
	paginations := []struct{ name, closing string }{
		{"pageInfo absent", `"nodes":[]`},
		{"pageInfo null", `"pageInfo":null,"nodes":[]`},
		{"pageInfo empty", `"pageInfo":{},"nodes":[]`},
		{"hasNextPage null", `"pageInfo":{"hasNextPage":null},"nodes":[]`},
		{"hasNextPage a string", `"pageInfo":{"hasNextPage":"false"},"nodes":[]`},
	}

	type row struct {
		name        string
		node        string
		readUnknown bool // Read also answers Unknown for this node
	}
	var rows []row
	for _, state := range []string{"OPEN", "CLOSED"} {
		for _, p := range paginations {
			// Read's CLOSED issue is Finished regardless of the closing list, except
			// when the node fails to decode (the string hasNextPage).
			readUnknown := state == "OPEN" || p.name == "hasNextPage a string"
			rows = append(rows, row{"issue " + state + ", " + p.name, closingWith(state, p.closing), readUnknown})
		}
	}
	rows = append(rows,
		row{"PR merged is a string", `{"__typename":"PullRequest","state":"CLOSED","merged":"true","headRefOid":"` + headA + `"}`, true},
		row{"merged PR headRefOid is a number", `{"__typename":"PullRequest","state":"MERGED","merged":true,"headRefOid":123}`, true},
		row{"issue OPEN, closing nodes is an object", issueWithClosingRaw("OPEN", `"nodes":{}`), true},
		row{"issue CLOSED, closing nodes is an object", issueWithClosingRaw("CLOSED", `"nodes":{}`), true},
		row{"issue OPEN, closing node merged is a string", issueWithClosingRaw("OPEN", `"nodes":[{"merged":"true"}]`), true},
		row{"issue CLOSED, merged closing node headRefOid is a number", issueWithClosingRaw("CLOSED", `"nodes":[{"merged":true,"headRefOid":123}]`), true},
		row{"issue state is a number", `{"__typename":"Issue","state":123,"closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":false},"nodes":[]}}`, true},
		row{"PR state is a number", `{"__typename":"PullRequest","state":123,"merged":false}`, true},
	)

	const n = 7
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			gh := installFakeGh(t)
			gh.respond(t, repoBody(map[int]string{n: tc.node}), 0, "")

			r := requireOutcome(t, (&Reader{}).ReadOutcomes(context.Background(), testRepo, []int{n}), n)
			if r.Outcome != OutcomeUnknown || r.Err == nil {
				t.Errorf("ReadOutcomes = %v (err %v), want Unknown with a cause", r.Outcome, r.Err)
			}
			if tc.readUnknown {
				assertVerdict(t, (&Reader{}).Read(context.Background(), testRepo, []int{n}), n, Unknown)
			}
		})
	}
}

// TestReadOutcomesNeverRemovableWhereReadIsNotFinished runs a copy of every row
// of TestReadVerdictTable, plus head-bearing variants of its merged rows, through
// both Read and ReadOutcomes. ReadOutcomes may be stricter than Read (Unknown
// where Read says Finished) but must never offer a removable outcome where Read
// says NotFinished or Unknown, and must never say Open where Read says Finished.
func TestReadOutcomesNeverRemovableWhereReadIsNotFinished(t *testing.T) {
	rows := []struct{ name, node string }{
		// Verbatim copies of the TestReadVerdictTable nodes.
		{"issue CLOSED", issueJSON("CLOSED", false)},
		{"issue CLOSED even with only an unmerged linked PR", issueJSON("CLOSED", false, false)},
		{"issue OPEN with a merged linked PR", issueJSON("OPEN", false, true)},
		{"issue OPEN with one merged among unmerged linked PRs", issueJSON("OPEN", false, false, true, false)},
		{"issue OPEN with a merged linked PR even if the list is truncated", issueJSON("OPEN", true, true)},
		{"issue OPEN with an unmerged linked PR", issueJSON("OPEN", false, false)},
		{"issue OPEN with no linked PRs", issueJSON("OPEN", false)},
		{"issue OPEN, unmerged, closing list truncated", issueJSON("OPEN", true, false)},
		{"issue OPEN, no linked PRs seen, closing list truncated", issueJSON("OPEN", true)},
		{"PR merged", prJSON("MERGED", true)},
		{"PR merged:true with CLOSED state", prJSON("CLOSED", true)},
		{"PR closed without merging", prJSON("CLOSED", false)},
		{"PR open", prJSON("OPEN", false)},
		{"unrecognized __typename", `{"__typename":"Discussion","state":"CLOSED"}`},
		{"missing __typename", `{"state":"CLOSED"}`},
		{"issue with an unrecognized state", issueJSON("ARCHIVED", false)},
		{"issue with no state", `{"__typename":"Issue","closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":false},"nodes":[]}}`},
		{"PR with an unrecognized state", prJSON("DRAFT", false)},
		{"null node", `null`},
		{"issue OPEN, closing nodes holds a null entry", openIssueClosing(`"nodes":[null]`)},
		{"issue OPEN, closing node omits merged", openIssueClosing(`"nodes":[{}]`)},
		{"issue OPEN, closing node has merged:null", openIssueClosing(`"nodes":[{"merged":null}]`)},
		{"issue OPEN, closing nodes key absent", openIssueClosing(``)},
		{"issue OPEN, closing nodes is null", openIssueClosing(`"nodes":null`)},
		{"issue OPEN, unmerged then merged-less closing nodes", openIssueClosing(`"nodes":[{"merged":false},{}]`)},
		{"issue OPEN, null entry beside a merged PR still wins", openIssueClosing(`"nodes":[null,{"merged":true}]`)},
		{"issue OPEN, empty closing nodes list", openIssueClosing(`"nodes":[]`)},

		// Variants carrying head SHAs, so the removable outcomes are reachable.
		{"with heads: issue OPEN with a merged linked PR", issueWithClosing("OPEN", false, closingPR(true, headA))},
		{"with heads: issue OPEN, truncated list holding a merged PR", issueWithClosing("OPEN", true, closingPR(true, headA))},
		{"with heads: issue CLOSED with a merged linked PR", issueWithClosing("CLOSED", false, closingPR(true, headA))},
		{"with heads: PR merged", prWithHead("MERGED", true, headA)},
		{"with heads: PR merged:true with CLOSED state", prWithHead("CLOSED", true, headA)},
		{"with heads: PR closed without merging", prWithHead("CLOSED", false, headA)},
		{"with heads: PR open", prWithHead("OPEN", false, headA)},
		{"with heads: issue OPEN with an unmerged linked PR", issueWithClosing("OPEN", false, closingPR(false, headA))},
		{"with heads: issue OPEN, truncated, unmerged", issueWithClosing("OPEN", true, closingPR(false, headA))},
	}
	const n = 7
	sawRemovable, sawOpen := false, false
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			gh := installFakeGh(t)
			gh.respond(t, repoBody(map[int]string{n: tc.node}), 0, "")

			verdict := (&Reader{}).Read(context.Background(), testRepo, []int{n})[n].Verdict
			r := requireOutcome(t, (&Reader{}).ReadOutcomes(context.Background(), testRepo, []int{n}), n)

			switch verdict {
			case Unknown:
				if r.Outcome != OutcomeUnknown {
					t.Errorf("Read says Unknown but ReadOutcomes = %v", r.Outcome)
				}
			case NotFinished:
				if r.Outcome != OutcomeUnknown && r.Outcome != OutcomeOpen {
					t.Errorf("Read says NotFinished but ReadOutcomes = %v, want Unknown or Open", r.Outcome)
				}
			case Finished:
				switch r.Outcome {
				case OutcomeUnknown, OutcomeIssueClosed, OutcomePRMerged, OutcomePRClosedUnmerged:
				default:
					t.Errorf("Read says Finished but ReadOutcomes = %v, want Unknown or a removable outcome", r.Outcome)
				}
			default:
				t.Fatalf("Read returned the out-of-range verdict %v", verdict)
			}
			switch r.Outcome {
			case OutcomeIssueClosed, OutcomePRMerged, OutcomePRClosedUnmerged:
				sawRemovable = true
			case OutcomeOpen:
				sawOpen = true
			}
		})
	}
	if !sawRemovable || !sawOpen {
		t.Errorf("rows exercised removable=%v open=%v outcomes, want both so the invariant is not vacuous", sawRemovable, sawOpen)
	}
}

func TestReadOutcomesMakesOneCallPerBatchOfAtMostFifty(t *testing.T) {
	cases := []struct {
		count     int
		wantCalls int
	}{{1, 1}, {50, 1}, {51, 2}, {100, 2}, {101, 3}}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d numbers", tc.count), func(t *testing.T) {
			gh := installFakeGh(t)
			gh.respondAllClosed(t)
			nums := rangeInts(1000, 1000+tc.count-1)

			got := (&Reader{}).ReadOutcomes(context.Background(), testRepo, nums)

			calls := gh.calls(t)
			if len(calls) != tc.wantCalls {
				t.Fatalf("gh calls = %d, want %d", len(calls), tc.wantCalls)
			}
			seen := map[string]bool{}
			for _, argv := range calls {
				aliases := queryAliases(queryArg(t, argv))
				if len(aliases) > MaxBatch {
					t.Errorf("a call carried %d numbers, want at most %d", len(aliases), MaxBatch)
				}
				for _, a := range aliases {
					if seen[a] {
						t.Errorf("number %s was sent in more than one call", a)
					}
					seen[a] = true
				}
			}
			if len(seen) != tc.count {
				t.Errorf("numbers sent = %d, want %d", len(seen), tc.count)
			}
			if len(got) != tc.count {
				t.Errorf("results = %d, want %d", len(got), tc.count)
			}
			for _, n := range nums {
				assertOutcome(t, got, n, OutcomeIssueClosed)
			}
		})
	}
}

func TestReadOutcomesSendsEachDistinctNumberOnce(t *testing.T) {
	gh := installFakeGh(t)
	gh.respond(t, repoBody(closedNodes(5, 9)), 0, "")

	got := (&Reader{}).ReadOutcomes(context.Background(), testRepo, []int{5, 5, 9, 5})

	calls := gh.calls(t)
	if len(calls) != 1 {
		t.Fatalf("gh calls = %d, want 1", len(calls))
	}
	if aliases := queryAliases(queryArg(t, calls[0])); len(aliases) != 2 {
		t.Errorf("query aliases = %v, want one each for 5 and 9", aliases)
	}
	if len(got) != 2 {
		t.Errorf("results = %v, want entries for 5 and 9 only", got)
	}
	assertOutcome(t, got, 5, OutcomeIssueClosed)
	assertOutcome(t, got, 9, OutcomeIssueClosed)
}

// TestReadOutcomesRejectsInvalidInputWithoutACall: a non-positive or
// out-of-range number is Unknown on its own and never sent; an invalid repo makes
// every number Unknown with no call at all.
func TestReadOutcomesRejectsInvalidInputWithoutACall(t *testing.T) {
	const tooBig = math.MaxInt32 + 1

	t.Run("invalid numbers beside a valid one", func(t *testing.T) {
		gh := installFakeGh(t)
		gh.respondAllClosed(t)

		got := (&Reader{}).ReadOutcomes(context.Background(), testRepo, []int{0, -3, tooBig, 7})

		for _, n := range []int{0, -3, tooBig} {
			assertOutcome(t, got, n, OutcomeUnknown)
			if err := got[n].Err; err != nil && !strings.Contains(err.Error(), "invalid ticket number") {
				t.Errorf("#%d err = %q, want it to say the number is invalid", n, err)
			}
		}
		assertOutcome(t, got, 7, OutcomeIssueClosed)
		calls := gh.calls(t)
		if len(calls) != 1 {
			t.Fatalf("gh calls = %d, want 1", len(calls))
		}
		if aliases := queryAliases(queryArg(t, calls[0])); len(aliases) != 1 || aliases[0] != "7" {
			t.Errorf("query aliases = %v, want only 7", aliases)
		}
	})

	t.Run("only invalid numbers make no call", func(t *testing.T) {
		gh := installFakeGh(t)
		gh.respondAllClosed(t)

		got := (&Reader{}).ReadOutcomes(context.Background(), testRepo, []int{0, -1, tooBig})

		for _, n := range []int{0, -1, tooBig} {
			assertOutcome(t, got, n, OutcomeUnknown)
		}
		if c := gh.calls(t); len(c) != 0 {
			t.Errorf("gh was called %d time(s) with nothing valid to ask", len(c))
		}
	})

	t.Run("empty input makes no call", func(t *testing.T) {
		gh := installFakeGh(t)
		gh.respondAllClosed(t)

		if got := (&Reader{}).ReadOutcomes(context.Background(), testRepo, nil); len(got) != 0 {
			t.Errorf("results = %v, want none", got)
		}
		if c := gh.calls(t); len(c) != 0 {
			t.Errorf("gh was called %d time(s) for no numbers", len(c))
		}
	})

	for name, repo := range map[string]Repo{
		"empty owner":               {Owner: "", Name: "frobnicate"},
		"owner with a leading dash": {Owner: "-evil", Name: "frobnicate"},
		"name with a slash":         {Owner: "zorgon-labs", Name: "a/b"},
		"name with a quote":         {Owner: "zorgon-labs", Name: `x"y`},
	} {
		t.Run("invalid repo: "+name, func(t *testing.T) {
			gh := installFakeGh(t)
			gh.respondAllClosed(t)
			nums := []int{1, 2, 3}

			got := (&Reader{}).ReadOutcomes(context.Background(), repo, nums)

			for _, n := range nums {
				assertOutcome(t, got, n, OutcomeUnknown)
				if err := got[n].Err; err != nil && !strings.Contains(err.Error(), "invalid repo") {
					t.Errorf("#%d err = %q, want it to say the repo is invalid", n, err)
				}
			}
			if c := gh.calls(t); len(c) != 0 {
				t.Errorf("gh was called %d time(s) for an invalid repo %+v", len(c), repo)
			}
		})
	}
}

// TestHeadRefOidIsRequestedInBothSelections pins the shared query: headRefOid is
// selected on the closing-PR nodes and on the PullRequest fragment, for Read and
// ReadOutcomes alike, and the query stays one line.
func TestHeadRefOidIsRequestedInBothSelections(t *testing.T) {
	calls := map[string]func(*Reader){
		"Read":         func(r *Reader) { r.Read(context.Background(), testRepo, []int{7}) },
		"ReadOutcomes": func(r *Reader) { r.ReadOutcomes(context.Background(), testRepo, []int{7}) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			gh := installFakeGh(t)
			gh.respond(t, repoBody(closedNodes(7)), 0, "")

			call(&Reader{})

			recorded := gh.calls(t)
			if len(recorded) != 1 {
				t.Fatalf("gh calls = %d, want 1", len(recorded))
			}
			query := queryArg(t, recorded[0])

			selection := func(open string) string {
				start := strings.Index(query, open)
				if start < 0 {
					t.Fatalf("query is missing %q:\n%s", open, query)
				}
				rest := query[start+len(open):]
				end := strings.Index(rest, "}")
				if end < 0 {
					t.Fatalf("query has no closing brace after %q:\n%s", open, query)
				}
				return rest[:end]
			}
			closingNodes := selection("closedByPullRequestsReferences(first: 25, includeClosedPrs: true){pageInfo{hasNextPage} nodes{")
			if !strings.Contains(closingNodes, "merged") || !strings.Contains(closingNodes, "headRefOid") {
				t.Errorf("closing-PR nodes selection = %q, want merged and headRefOid", closingNodes)
			}
			prFragment := selection("... on PullRequest{")
			for _, field := range []string{"state", "merged", "headRefOid"} {
				if !strings.Contains(prFragment, field) {
					t.Errorf("PullRequest selection = %q, want %s", prFragment, field)
				}
			}
			if strings.ContainsAny(query, "\r\n") {
				t.Errorf("query must be a single line:\n%q", query)
			}
		})
	}
}

// --- ReadOutcomes fail-open: the same failure tables as Read, run on ReadOutcomes ---

func TestReadOutcomesWholeCallFailuresErrorEveryNumber(t *testing.T) {
	nums := []int{1, 2, 3}
	// Every alias is a merged PR with a valid head, so each would read as a
	// removable PR merged outcome if the body were trusted.
	trusted := map[int]string{}
	for _, n := range nums {
		trusted[n] = prWithHead("MERGED", true, headA)
	}
	decodable := repoBody(trusted)
	cases := []struct {
		name  string
		setup func(t *testing.T, gh *fakeGh)
		check func(t *testing.T, err error)
	}{
		{
			name:  "non-zero exit with no body",
			setup: func(t *testing.T, gh *fakeGh) { gh.respond(t, "", 1, "gh: HTTP 502 upstream\nsecond line") },
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "HTTP 502") {
					t.Errorf("err = %q, want gh's stderr surfaced", err)
				}
				if strings.ContainsAny(err.Error(), "\r\n") {
					t.Errorf("err = %q, want newlines collapsed", err)
				}
			},
		},
		{
			name: "timeout",
			setup: func(t *testing.T, gh *fakeGh) {
				setVar(t, &ghTimeout, 300*time.Millisecond)
				gh.behave(t, `exec sleep 30`)
			},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, errTimeout) {
					t.Errorf("err = %v, want errors.Is(_, errTimeout)", err)
				}
			},
		},
		{
			name:  "malformed JSON",
			setup: func(t *testing.T, gh *fakeGh) { gh.respond(t, "<html>oops</html>", 0, "") },
			check: func(t *testing.T, err error) {
				var syntaxErr *json.SyntaxError
				if !errors.As(err, &syntaxErr) {
					t.Errorf("err = %v, want it to wrap a *json.SyntaxError", err)
				}
			},
		},
		{
			name:  "null repository",
			setup: func(t *testing.T, gh *fakeGh) { gh.respond(t, `{"data":{"repository":null}}`, 0, "") },
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "repository") {
					t.Errorf("err = %q, want it to name the repository", err)
				}
			},
		},
		{
			name: "auth failure body with no repository, exit 1",
			setup: func(t *testing.T, gh *fakeGh) {
				gh.respond(t, `{"message":"Bad credentials"}`, 1, "gh: HTTP 401: Bad credentials")
			},
			check: func(t *testing.T, err error) {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Errorf("err = %v, want it to wrap the *exec.ExitError", err)
				}
				if !strings.Contains(err.Error(), "HTTP 401") {
					t.Errorf("err = %q, want gh's stderr surfaced", err)
				}
			},
		},
		{
			name: "stdout over the cap",
			setup: func(t *testing.T, gh *fakeGh) {
				gh.behave(t, `head -c 1200000 /dev/zero | tr '\0' x`)
			},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, errOutputTruncated) {
					t.Errorf("err = %v, want errors.Is(_, errOutputTruncated)", err)
				}
			},
		},
		{
			name: "errors[] entry with no path",
			setup: func(t *testing.T, gh *fakeGh) {
				gh.respond(t, withErrors(decodable, `[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]`), 0, "")
			},
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "API rate limit exceeded") {
					t.Errorf("err = %q, want the GraphQL error message surfaced", err)
				}
			},
		},
		{
			name: "errors[] path names an alias outside the batch",
			setup: func(t *testing.T, gh *fakeGh) {
				gh.respond(t, withErrors(decodable, `[{"path":["repository","n99"],"message":"boom-unknown-alias"}]`), 1, "")
			},
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "boom-unknown-alias") {
					t.Errorf("err = %q, want the GraphQL error message surfaced", err)
				}
			},
		},
		{
			name: "one aliased and one un-aliased errors[] entry",
			setup: func(t *testing.T, gh *fakeGh) {
				gh.respond(t, withErrors(decodable, `[{"path":["repository","n2"],"message":"tied"},{"message":"boom-untied"}]`), 1, "")
			},
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "boom-untied") {
					t.Errorf("err = %q, want the un-aliased GraphQL error surfaced", err)
				}
			},
		},
		{
			// A non-zero exit that explains nothing is not a partial read.
			name:  "exit 1 with a decodable body and no errors[]",
			setup: func(t *testing.T, gh *fakeGh) { gh.respond(t, decodable, 1, "gh: something went wrong") },
			check: func(t *testing.T, err error) {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Errorf("err = %v, want it to wrap the *exec.ExitError", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := installFakeGh(t)
			tc.setup(t, gh)

			got := (&Reader{}).ReadOutcomes(context.Background(), testRepo, nums)

			if len(got) != len(nums) {
				t.Fatalf("results = %v, want an entry for every requested number", got)
			}
			for _, n := range nums {
				r := requireOutcome(t, got, n)
				if r.Outcome != OutcomeUnknown || r.Err == nil {
					t.Errorf("#%d = %+v, want Unknown with an error", n, r)
					continue
				}
				tc.check(t, r.Err)
			}
		})
	}
}

func TestReadOutcomesPartialClassificationMarksOnlyTheFailedAliasUnknown(t *testing.T) {
	const notFound = "Could not resolve to an issue or pull request with the number of 2."
	merged := prWithHead("MERGED", true, headA)
	cases := []struct {
		name        string
		body        string
		wantUnknown []int
		wantMsg     string
	}{
		{
			name: "exit 1, null node, path tagged with the alias",
			body: withErrors(repoBody(map[int]string{1: issueWithClosing("CLOSED", false), 2: "null", 3: prWithHead("OPEN", false, headA)}),
				`[{"type":"NOT_FOUND","path":["repository","n2"],"message":"`+notFound+`"}]`),
			wantUnknown: []int{2},
			wantMsg:     notFound,
		},
		{
			name: "path runs deeper than the alias",
			body: withErrors(repoBody(map[int]string{1: issueWithClosing("CLOSED", false), 2: "null", 3: prWithHead("OPEN", false, headA)}),
				`[{"type":"NOT_FOUND","path":["repository","n2","state"],"message":"`+notFound+`"}]`),
			wantUnknown: []int{2},
			wantMsg:     notFound,
		},
		{
			name: "an errored alias is Unknown even when its node would read as PR merged",
			body: withErrors(repoBody(map[int]string{1: issueWithClosing("CLOSED", false), 2: merged, 3: prWithHead("OPEN", false, headA)}),
				`[{"path":["repository","n2"],"message":"`+notFound+`"}]`),
			wantUnknown: []int{2},
			wantMsg:     notFound,
		},
		{
			name: "several errored aliases",
			body: withErrors(repoBody(map[int]string{1: "null", 2: issueWithClosing("CLOSED", false), 3: "null"}),
				`[{"path":["repository","n1"],"message":"gone-1"},{"path":["repository","n3"],"message":"gone-3"}]`),
			wantUnknown: []int{1, 3},
			wantMsg:     "gone-",
		},
	}
	// Unaffected aliases keep these outcomes in every case above.
	baseline := map[int]Outcome{1: OutcomeIssueClosed, 2: OutcomeIssueClosed, 3: OutcomeOpen}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := installFakeGh(t)
			gh.respond(t, tc.body, 1, "gh: "+notFound)

			got := (&Reader{}).ReadOutcomes(context.Background(), testRepo, []int{1, 2, 3})

			unknown := map[int]bool{}
			for _, n := range tc.wantUnknown {
				unknown[n] = true
			}
			for _, n := range []int{1, 2, 3} {
				r := requireOutcome(t, got, n)
				if unknown[n] {
					if r.Outcome != OutcomeUnknown || r.Err == nil {
						t.Errorf("#%d = %+v, want Unknown with an error", n, r)
					} else if !strings.Contains(r.Err.Error(), tc.wantMsg) {
						t.Errorf("#%d err = %q, want it to carry the GraphQL message %q", n, r.Err, tc.wantMsg)
					}
					continue
				}
				if want := baseline[n]; r.Outcome != want {
					t.Errorf("#%d outcome = %v (err %v), want %v: one bad alias must not poison the rest", n, r.Outcome, r.Err, want)
				}
			}
		})
	}
}

func TestReadOutcomesMissingAliasIsUnknownAndOthersStillClassified(t *testing.T) {
	gh := installFakeGh(t)
	gh.respond(t, repoBody(closedNodes(8)), 0, "")

	got := (&Reader{}).ReadOutcomes(context.Background(), testRepo, []int{7, 8})

	assertOutcome(t, got, 7, OutcomeUnknown)
	if err := got[7].Err; err != nil && !strings.Contains(err.Error(), "missing") {
		t.Errorf("#7 err = %q, want it to say the alias is missing from the response", err)
	}
	assertOutcome(t, got, 8, OutcomeIssueClosed)
}
