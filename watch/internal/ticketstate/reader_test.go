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
