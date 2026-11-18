package dispatch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// -- #847: planning trust gate -----------------------------------------------

// trustComment is one REST issue comment fixture for the trust classifier.
type trustComment struct {
	id    int64
	login string
	typ   string
	assoc string
	body  string
}

func restComments(cs ...trustComment) []restIssueComment {
	out := make([]restIssueComment, 0, len(cs))
	for _, c := range cs {
		typ := c.typ
		if typ == "" {
			typ = "User"
		}
		out = append(out, restIssueComment{
			ID:                c.id,
			Body:              c.body,
			Author:            restCommentAuthor{Login: c.login, Type: typ},
			AuthorAssociation: c.assoc,
		})
	}
	return out
}

func trustCommentsJSON(t *testing.T, cs ...trustComment) string {
	t.Helper()
	data, err := json.Marshal(restComments(cs...))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// recordingPermOf is a permOf seam that answers from perms and records every
// login it was asked about. A login missing from perms answers api_error, so
// an unexpected lookup can never read as granted.
func recordingPermOf(perms map[string]WritePermission) (func(string) WritePermission, *[]string) {
	var calls []string
	return func(login string) WritePermission {
		calls = append(calls, login)
		if p, ok := perms[login]; ok {
			return p
		}
		return WritePermissionAPIError
	}, &calls
}

// TestClassifyPlanningTrust is the pure classifier table: every author and
// commenter class from the ticket's Acceptance Criteria and Edge Cases, the
// reporting precedence (comments read unresolved > permission unresolved >
// author denied > comment denied) and which logins must never reach the
// permission seam.
func TestClassifyPlanningTrust(t *testing.T) {
	const writer = "maintainer"
	granted := WritePermissionGranted
	cases := []struct {
		name        string
		author      string
		authorIsBot bool
		comments    []trustComment
		perms       map[string]WritePermission
		want        PlanningTrustProbe
		noCallFor   []string // logins that must never reach permOf
		wantNoCalls bool     // permOf must not be called at all
	}{
		{
			name:   "trusted author and empty thread is trusted",
			author: "octocat",
			perms:  map[string]WritePermission{"octocat": granted},
			want:   PlanningTrustProbe{Class: PlanningTrustTrusted},
		},
		{
			name:   "author with read permission is author denied",
			author: "octocat",
			perms:  map[string]WritePermission{"octocat": WritePermissionDenied},
			want:   PlanningTrustProbe{Class: PlanningTrustAuthorDenied},
		},
		{
			name:        "github-actions[bot] author is author denied without a lookup",
			author:      "github-actions[bot]",
			want:        PlanningTrustProbe{Class: PlanningTrustAuthorDenied},
			wantNoCalls: true,
		},
		{
			name:        "app/ author is author denied without a lookup",
			author:      "app/github-actions",
			want:        PlanningTrustProbe{Class: PlanningTrustAuthorDenied},
			wantNoCalls: true,
		},
		{
			name:        "author flagged is_bot is author denied without a lookup",
			author:      "renovate",
			authorIsBot: true,
			perms:       map[string]WritePermission{"renovate": granted},
			want:        PlanningTrustProbe{Class: PlanningTrustAuthorDenied},
			wantNoCalls: true,
		},
		{
			name:        "empty author login is author denied without a lookup",
			author:      "",
			want:        PlanningTrustProbe{Class: PlanningTrustAuthorDenied},
			wantNoCalls: true,
		},
		{
			name:        "invalid author login is author denied without a lookup",
			author:      "bad/login",
			want:        PlanningTrustProbe{Class: PlanningTrustAuthorDenied},
			wantNoCalls: true,
		},
		{
			name:     "MEMBER commenter with write is trusted",
			author:   "octocat",
			comments: []trustComment{{id: 11, login: writer, assoc: "MEMBER", body: "lgtm"}},
			perms:    map[string]WritePermission{"octocat": granted, writer: granted},
			want:     PlanningTrustProbe{Class: PlanningTrustTrusted},
		},
		{
			name:      "[bot] commenter holds the ticket without a lookup",
			author:    "octocat",
			comments:  []trustComment{{id: 12, login: "renovate[bot]", assoc: "COLLABORATOR"}},
			perms:     map[string]WritePermission{"octocat": granted},
			want:      PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 12},
			noCallFor: []string{"renovate[bot]"},
		},
		{
			name:      "app/ commenter holds the ticket without a lookup",
			author:    "octocat",
			comments:  []trustComment{{id: 13, login: "app/dependabot", assoc: "COLLABORATOR"}},
			perms:     map[string]WritePermission{"octocat": granted},
			want:      PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 13},
			noCallFor: []string{"app/dependabot"},
		},
		{
			name:      "user.type Bot commenter with repository access holds the ticket",
			author:    "octocat",
			comments:  []trustComment{{id: 14, login: "deploybot", typ: "Bot", assoc: "COLLABORATOR"}},
			perms:     map[string]WritePermission{"octocat": granted, "deploybot": granted},
			want:      PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 14},
			noCallFor: []string{"deploybot"},
		},
		{
			name:      "cenci marker by an untrusted login holds the ticket",
			author:    "octocat",
			comments:  []trustComment{{id: 15, login: "stranger", assoc: "NONE", body: "<!-- cenci-dispatch-attempt -->\nretry"}},
			perms:     map[string]WritePermission{"octocat": granted},
			want:      PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 15},
			noCallFor: []string{"stranger"},
		},
		{
			name:     "cenci marker by a read collaborator still holds the ticket",
			author:   "octocat",
			comments: []trustComment{{id: 16, login: "reader", assoc: "COLLABORATOR", body: "<!-- cenci-planner-escalation:abc -->"}},
			perms:    map[string]WritePermission{"octocat": granted, "reader": WritePermissionDenied},
			want:     PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 16},
		},
		{
			name:      "CONTRIBUTOR commenter holds the ticket without a lookup",
			author:    "octocat",
			comments:  []trustComment{{id: 17, login: "contrib", assoc: "CONTRIBUTOR"}},
			perms:     map[string]WritePermission{"octocat": granted, "contrib": granted},
			want:      PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 17},
			noCallFor: []string{"contrib"},
		},
		{
			name:      "FIRST_TIME_CONTRIBUTOR commenter holds the ticket without a lookup",
			author:    "octocat",
			comments:  []trustComment{{id: 18, login: "newbie", assoc: "FIRST_TIME_CONTRIBUTOR"}},
			perms:     map[string]WritePermission{"octocat": granted, "newbie": granted},
			want:      PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 18},
			noCallFor: []string{"newbie"},
		},
		{
			name:      "FIRST_TIMER commenter holds the ticket without a lookup",
			author:    "octocat",
			comments:  []trustComment{{id: 19, login: "firsttimer", assoc: "FIRST_TIMER"}},
			perms:     map[string]WritePermission{"octocat": granted, "firsttimer": granted},
			want:      PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 19},
			noCallFor: []string{"firsttimer"},
		},
		{
			name:      "NONE commenter holds the ticket without a lookup",
			author:    "octocat",
			comments:  []trustComment{{id: 20, login: "drive-by", assoc: "NONE"}},
			perms:     map[string]WritePermission{"octocat": granted, "drive-by": granted},
			want:      PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 20},
			noCallFor: []string{"drive-by"},
		},
		{
			name:     "COLLABORATOR with read permission holds the ticket",
			author:   "octocat",
			comments: []trustComment{{id: 21, login: "reader", assoc: "COLLABORATOR"}},
			perms:    map[string]WritePermission{"octocat": granted, "reader": WritePermissionDenied},
			want:     PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 21},
		},
		{
			name:     "COLLABORATOR with triage permission holds the ticket",
			author:   "octocat",
			comments: []trustComment{{id: 22, login: "triager", assoc: "COLLABORATOR"}},
			perms:    map[string]WritePermission{"octocat": granted, "triager": WritePermissionDenied},
			want:     PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 22},
		},
		{
			name:      "deleted-user ghost commenter holds the ticket",
			author:    "octocat",
			comments:  []trustComment{{id: 23, login: "ghost", assoc: "NONE"}},
			perms:     map[string]WritePermission{"octocat": granted},
			want:      PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 23},
			noCallFor: []string{"ghost"},
		},
		{
			name:   "hidden (minimized) comment by an untrusted login still holds the ticket",
			author: "octocat",
			// The REST comments response carries minimized comments like any
			// other; the classifier has no notion of hidden and must not skip it.
			comments: []trustComment{
				{id: 24, login: writer, assoc: "OWNER", body: "spec"},
				{id: 25, login: "spammer", assoc: "NONE", body: "hidden as spam"},
			},
			perms: map[string]WritePermission{"octocat": granted, writer: granted},
			want:  PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 25},
		},
		{
			name:      "invalid commenter login is comment denied without a lookup",
			author:    "octocat",
			comments:  []trustComment{{id: 26, login: "bad login", assoc: "COLLABORATOR"}},
			perms:     map[string]WritePermission{"octocat": granted},
			want:      PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 26},
			noCallFor: []string{"bad login"},
		},
		{
			name:   "first untrusted comment in ascending-ID order is reported",
			author: "octocat",
			comments: []trustComment{
				{id: 30, login: "late", assoc: "NONE"},
				{id: 10, login: writer, assoc: "MEMBER"},
				{id: 20, login: "early", assoc: "NONE"},
			},
			perms: map[string]WritePermission{"octocat": granted, writer: granted},
			want:  PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 20},
		},
		{
			name:     "comment permission unresolved is permission unresolved",
			author:   "octocat",
			comments: []trustComment{{id: 31, login: "flaky", assoc: "COLLABORATOR"}},
			perms:    map[string]WritePermission{"octocat": granted, "flaky": WritePermissionTimeout},
			want:     PlanningTrustProbe{Class: PlanningTrustPermissionUnresolved},
		},
		{
			name:     "unresolved beats author denied",
			author:   "octocat",
			comments: []trustComment{{id: 32, login: "flaky", assoc: "COLLABORATOR"}},
			perms:    map[string]WritePermission{"octocat": WritePermissionDenied, "flaky": WritePermissionAPIError},
			want:     PlanningTrustProbe{Class: PlanningTrustPermissionUnresolved},
		},
		{
			name:     "unresolved author beats comment denied",
			author:   "octocat",
			comments: []trustComment{{id: 33, login: "drive-by", assoc: "NONE"}},
			perms:    map[string]WritePermission{"octocat": WritePermissionCapExhausted},
			want:     PlanningTrustProbe{Class: PlanningTrustPermissionUnresolved},
		},
		{
			name:   "unresolved later comment beats earlier denied comment (no short-circuit)",
			author: "octocat",
			comments: []trustComment{
				{id: 34, login: "drive-by", assoc: "NONE"},
				{id: 35, login: "flaky", assoc: "COLLABORATOR"},
			},
			perms: map[string]WritePermission{"octocat": granted, "flaky": WritePermissionMalformed},
			want:  PlanningTrustProbe{Class: PlanningTrustPermissionUnresolved},
		},
		{
			name:   "unresolved earlier comment beats later denied comment",
			author: "octocat",
			comments: []trustComment{
				{id: 36, login: "flaky", assoc: "COLLABORATOR"},
				{id: 37, login: "drive-by", assoc: "NONE"},
			},
			perms: map[string]WritePermission{"octocat": granted, "flaky": WritePermissionMissingField},
			want:  PlanningTrustProbe{Class: PlanningTrustPermissionUnresolved},
		},
		{
			name:     "author denied beats comment denied",
			author:   "octocat",
			comments: []trustComment{{id: 38, login: "drive-by", assoc: "NONE"}},
			perms:    map[string]WritePermission{"octocat": WritePermissionDenied},
			want:     PlanningTrustProbe{Class: PlanningTrustAuthorDenied},
		},
		{
			name:        "non-positive comment id is unresolved with no lookups at all",
			author:      "octocat",
			comments:    []trustComment{{id: 0, login: writer, assoc: "MEMBER"}},
			perms:       map[string]WritePermission{"octocat": granted, writer: granted},
			want:        PlanningTrustProbe{Class: PlanningTrustUnresolved},
			wantNoCalls: true,
		},
		{
			name:        "negative comment id beats a permission failure",
			author:      "octocat",
			comments:    []trustComment{{id: -5, login: "flaky", assoc: "COLLABORATOR"}},
			perms:       map[string]WritePermission{"octocat": WritePermissionTimeout},
			want:        PlanningTrustProbe{Class: PlanningTrustUnresolved},
			wantNoCalls: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			permOf, calls := recordingPermOf(c.perms)
			got := classifyPlanningTrust(c.author, c.authorIsBot, restComments(c.comments...), permOf)
			if got != c.want {
				t.Errorf("classifyPlanningTrust = %+v, want %+v", got, c.want)
			}
			if c.wantNoCalls && len(*calls) != 0 {
				t.Errorf("permOf called with %q, want no lookups at all", *calls)
			}
			for _, login := range c.noCallFor {
				for _, called := range *calls {
					if called == login {
						t.Errorf("permOf called for %q, which must fail the prefilter without a lookup", login)
					}
				}
			}
		})
	}
}

// TestTrustVerdict_ZeroValueNeverGrants pins the default-deny invariant: an
// unset trustVerdict must not read as granted.
func TestTrustVerdict_ZeroValueNeverGrants(t *testing.T) {
	var v trustVerdict
	if v == trustGranted {
		t.Fatal("the zero trustVerdict is trustGranted; it must be a non-granting value")
	}
}

// TestClassifyPlanningTrust_EveryUnresolvedPermissionClass pins that each
// non-granted, non-denied WritePermission on the author resolves to
// permission unresolved, never to author denied or trusted.
func TestClassifyPlanningTrust_EveryUnresolvedPermissionClass(t *testing.T) {
	for _, perm := range []WritePermission{
		WritePermissionAPIError, WritePermissionTimeout, WritePermissionTruncated,
		WritePermissionMalformed, WritePermissionMissingField, WritePermissionUnknownValue,
		WritePermissionCapExhausted, WritePermission(""), WritePermission("bogus"),
	} {
		t.Run(string(perm), func(t *testing.T) {
			permOf, _ := recordingPermOf(map[string]WritePermission{"octocat": perm})
			got := classifyPlanningTrust("octocat", false, nil, permOf)
			if got.Class != PlanningTrustPermissionUnresolved {
				t.Errorf("author permission %q classified %+v, want permission unresolved", perm, got)
			}
		})
	}
}

// -- probe tests over a fake gh -----------------------------------------------

// trustFakeGH is a fake gh for the trust probe: comments are served from
// per-issue files and permissions from per-login files, every call is logged,
// and anything unseeded fails with a non-zero exit.
type trustFakeGH struct {
	dir string
	log string
}

func installTrustFakeGH(t *testing.T) *trustFakeGH {
	t.Helper()
	f := &trustFakeGH{dir: t.TempDir()}
	f.log = filepath.Join(f.dir, "calls.log")
	for _, sub := range []string{"comments", "perm"} {
		if err := os.MkdirAll(filepath.Join(f.dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	installFakeGHOnPath(t, fmt.Sprintf(`
printf '%%s\n' "$*" >> %[1]q
case "$1" in
  api)
    case "$2" in
      */collaborators/*/permission)
        login=${2#*/collaborators/}
        login=${login%%/permission}
        if [ -f %[2]q/perm/"$login" ]; then cat %[2]q/perm/"$login"; exit 0; fi
        echo "HTTP 404: Not Found" >&2
        exit 1 ;;
      repos/*/issues/*/comments*)
        key=$(printf '%%s' "$2" | sed 's/?.*//; s#/#_#g')
        if [ -f %[2]q/comments/"$key" ]; then cat %[2]q/comments/"$key"; exit 0; fi
        echo "HTTP 502: Bad Gateway" >&2
        exit 1 ;;
    esac ;;
esac
exit 1
`, f.log, f.dir))
	return f
}

func (f *trustFakeGH) setComments(t *testing.T, repo string, number int, body string) {
	t.Helper()
	key := strings.ReplaceAll(fmt.Sprintf("repos/%s/issues/%d/comments", repo, number), "/", "_")
	if err := os.WriteFile(filepath.Join(f.dir, "comments", key), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *trustFakeGH) setPermission(t *testing.T, login, perm string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "perm", login), []byte(permissionJSON(perm)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// installTrustCommentsScriptGH installs a fake gh whose comments-thread read
// runs commentsScript (one shell case arm body) and whose every other call
// answers admin, for the probe's read-failure paths.
func installTrustCommentsScriptGH(t *testing.T, commentsScript string) {
	t.Helper()
	installFakeGHOnPath(t, `
case "$2" in
  repos/*/comments*) `+commentsScript+` ;;
esac
printf '`+permissionJSON("admin")+`'
`)
}

func (f *trustFakeGH) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func countCalls(calls []string, substr string) int {
	n := 0
	for _, c := range calls {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

// trustCandidate is a fresh Refined planning candidate in "o/r", authored by
// and assigned only to octocat.
func trustCandidate(number int) Ticket {
	return Ticket{Repo: "o/r", Number: number, Labels: []string{"Refined"}, Assignees: []string{"octocat"}, Author: "octocat"}
}

// trustInputs is the probe's input for a lean "o/r" with planRefined on.
func trustInputs(tickets ...Ticket) Inputs {
	return Inputs{
		Tickets:      tickets,
		Config:       Config{PlanRefined: true, PlanStalenessTolerance: 5},
		RepoAutonomy: leanAutonomy("o/r"),
		CurrentUser:  "octocat",
	}
}

func TestResolvePlanningTrust_TrustedAuthorEmptyThreadUsesRESTCommentsCall(t *testing.T) {
	f := installTrustFakeGH(t)
	f.setComments(t, "o/r", 42, `[]`)
	f.setPermission(t, "octocat", "admin")

	got := resolvePlanningTrust(trustInputs(trustCandidate(42)), &bytes.Buffer{})
	if want := (PlanningTrustProbe{Class: PlanningTrustTrusted}); got["o/r#42"] != want {
		t.Fatalf("o/r#42 = %+v, want %+v", got["o/r#42"], want)
	}
	calls := f.calls(t)
	const wantComments = "api repos/o/r/issues/42/comments?per_page=100 --paginate"
	if countCalls(calls, wantComments) != 1 {
		t.Errorf("calls = %q, want exactly one %q", calls, wantComments)
	}
	if countCalls(calls, "api repos/o/r/collaborators/octocat/permission") != 1 {
		t.Errorf("calls = %q, want exactly one author permission lookup", calls)
	}
	if len(calls) != 2 {
		t.Errorf("calls = %q, want exactly two gh calls (comments + author permission)", calls)
	}
}

func TestResolvePlanningTrust_AuthorReadPermissionIsAuthorDenied(t *testing.T) {
	for _, perm := range []string{"read", "triage", "none"} {
		t.Run(perm, func(t *testing.T) {
			f := installTrustFakeGH(t)
			f.setComments(t, "o/r", 42, `[]`)
			f.setPermission(t, "octocat", perm)
			got := resolvePlanningTrust(trustInputs(trustCandidate(42)), &bytes.Buffer{})
			if got["o/r#42"].Class != PlanningTrustAuthorDenied {
				t.Errorf("o/r#42 = %+v, want author denied", got["o/r#42"])
			}
		})
	}
}

// TestResolvePlanningTrust_UnreadableCommentsAreUnresolved covers AC4's read
// failures: a failed call, malformed JSON and a JSON null all hold as trust
// unresolved, and none of them spends a permission lookup.
func TestResolvePlanningTrust_UnreadableCommentsAreUnresolved(t *testing.T) {
	cases := []struct {
		name     string
		comments string // "" leaves the route unseeded, so the call fails
	}{
		{"API error", ""},
		{"malformed JSON", `[{"id":1,`},
		{"JSON null", `null`},
		{"JSON object", `{"message":"Not Found"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := installTrustFakeGH(t)
			if c.comments != "" {
				f.setComments(t, "o/r", 42, c.comments)
			}
			f.setPermission(t, "octocat", "admin")
			got := resolvePlanningTrust(trustInputs(trustCandidate(42)), &bytes.Buffer{})
			if got["o/r#42"].Class != PlanningTrustUnresolved {
				t.Errorf("o/r#42 = %+v, want trust unresolved", got["o/r#42"])
			}
			if n := countCalls(f.calls(t), "/permission"); n != 0 {
				t.Errorf("made %d permission lookups after a failed comments read, want 0", n)
			}
		})
	}
}

// TestResolvePlanningTrust_NonPositiveCommentIDIsLoggedAndUnresolved covers
// the malformed-ID read: a comment whose REST id is missing, zero or negative
// holds the candidate as trust unresolved, writes one clean probe log line
// naming only repo#n, and spends no permission lookup -- not even the
// author's, although a well-formed comment sits beside it.
func TestResolvePlanningTrust_NonPositiveCommentIDIsLoggedAndUnresolved(t *testing.T) {
	const sentinel = "BODY-SENTINEL-847-ID"
	cases := []struct {
		name     string
		comments string
	}{
		{"missing id", `[{"body":"` + sentinel + `","user":{"login":"writer","type":"User"},"author_association":"MEMBER"}]`},
		{"zero id", trustCommentsJSON(t, trustComment{id: 0, login: "writer", assoc: "MEMBER", body: sentinel})},
		{"negative id beside a valid comment", trustCommentsJSON(t,
			trustComment{id: 5, login: "writer", assoc: "MEMBER", body: "fine"},
			trustComment{id: -3, login: "writer", assoc: "MEMBER", body: sentinel})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := installTrustFakeGH(t)
			f.setComments(t, "o/r", 42, c.comments)
			f.setPermission(t, "octocat", "admin")
			f.setPermission(t, "writer", "write")

			var buf bytes.Buffer
			got := resolvePlanningTrust(trustInputs(trustCandidate(42)), &buf)
			if want := (PlanningTrustProbe{Class: PlanningTrustUnresolved}); got["o/r#42"] != want {
				t.Errorf("o/r#42 = %+v, want %+v", got["o/r#42"], want)
			}
			log := buf.String()
			const wantLine = "dispatch: planning trust probe for o/r#42 returned a comment with a non-positive id\n"
			if !strings.Contains(log, wantLine) {
				t.Errorf("log = %q, want the line %q", log, wantLine)
			}
			assertTrustLogClean(t, log, sentinel)
			if n := countCalls(f.calls(t), "/permission"); n != 0 {
				t.Errorf("made %d permission lookups after a non-positive comment id, want 0", n)
			}
		})
	}
}

func TestResolvePlanningTrust_CommentsTimeoutIsUnresolved(t *testing.T) {
	orig := planningTrustProbeTimeout
	planningTrustProbeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { planningTrustProbeTimeout = orig })

	installTrustCommentsScriptGH(t, `exec sleep 5`)
	start := time.Now()
	var buf bytes.Buffer
	got := resolvePlanningTrust(trustInputs(trustCandidate(42)), &buf)
	if got["o/r#42"].Class != PlanningTrustUnresolved {
		t.Errorf("o/r#42 = %+v, want trust unresolved", got["o/r#42"])
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("probe took %v, want it bounded by planningTrustProbeTimeout", elapsed)
	}
	if !strings.Contains(buf.String(), "timed out") {
		t.Errorf("log = %q, want a timed-out line", buf.String())
	}
}

func TestResolvePlanningTrust_CommentThreadOverOneMiBIsUnresolved(t *testing.T) {
	installTrustCommentsScriptGH(t, `yes x | head -c 1100000 ; exit 0`)
	var buf bytes.Buffer
	got := resolvePlanningTrust(trustInputs(trustCandidate(42)), &buf)
	if got["o/r#42"].Class != PlanningTrustUnresolved {
		t.Errorf("o/r#42 = %+v, want trust unresolved", got["o/r#42"])
	}
	if !strings.Contains(buf.String(), "byte stdout cap") {
		t.Errorf("log = %q, want the stdout-cap line", buf.String())
	}
}

// TestResolvePlanningTrust_ProbeBudgetSpentIsUnresolved: past
// maxPlanningTrustProbes comments reads, every further candidate holds as
// unresolved without a gh call, and candidates are probed in ascending number
// order, the key Decide sorts by.
func TestResolvePlanningTrust_ProbeBudgetSpentIsUnresolved(t *testing.T) {
	f := installTrustFakeGH(t)
	f.setPermission(t, "octocat", "admin")
	var tickets []Ticket
	for n := maxPlanningTrustProbes + 1; n >= 1; n-- { // reversed on purpose
		f.setComments(t, "o/r", n, `[]`)
		tickets = append(tickets, trustCandidate(n))
	}
	var buf bytes.Buffer
	got := resolvePlanningTrust(trustInputs(tickets...), &buf)

	for n := 1; n <= maxPlanningTrustProbes; n++ {
		if got[planKey("o/r", n)].Class != PlanningTrustTrusted {
			t.Fatalf("o/r#%d = %+v, want trusted (within budget)", n, got[planKey("o/r", n)])
		}
	}
	last := planKey("o/r", maxPlanningTrustProbes+1)
	if got[last].Class != PlanningTrustUnresolved {
		t.Errorf("%s = %+v, want trust unresolved (budget spent)", last, got[last])
	}
	calls := f.calls(t)
	if n := countCalls(calls, "/comments"); n != maxPlanningTrustProbes {
		t.Errorf("made %d comments reads, want exactly %d", n, maxPlanningTrustProbes)
	}
	if n := countCalls(calls, "/permission"); n != 1 {
		t.Errorf("made %d permission lookups for one shared author, want exactly 1 (per-pass cache)", n)
	}
	if n := strings.Count(buf.String(), "probe cap"); n != 1 {
		t.Errorf("cap line logged %d times, want once: %q", n, buf.String())
	}
}

// TestResolvePlanningTrust_ProbeBudgetTieBreaksSameNumberByRepo: the probe's own
// order is number ascending, then repo ascending (#847 Q&A 12). With the input
// reversed and the 50-probe budget cutting between o/a#50 and o/b#50, the lower
// repo name is probed and trusted and the higher one holds as unresolved
// without a comments read.
func TestResolvePlanningTrust_ProbeBudgetTieBreaksSameNumberByRepo(t *testing.T) {
	f := installTrustFakeGH(t)
	f.setPermission(t, "octocat", "admin")
	boundary := maxPlanningTrustProbes
	ticket := func(repo string, n int) Ticket {
		tk := trustCandidate(n)
		tk.Repo = repo
		f.setComments(t, repo, n, `[]`)
		return tk
	}
	tickets := []Ticket{ticket("o/b", boundary), ticket("o/a", boundary)} // reversed on purpose
	for n := boundary - 1; n >= 1; n-- {
		tickets = append(tickets, ticket("o/a", n))
	}
	in := trustInputs(tickets...)
	in.RepoAutonomy = map[string]RepoAutonomy{"o/a": RepoAutonomyLean, "o/b": RepoAutonomyLean}

	got := resolvePlanningTrust(in, &bytes.Buffer{})

	if k := planKey("o/a", boundary); got[k].Class != PlanningTrustTrusted {
		t.Errorf("%s = %+v, want trusted (lower repo wins the tie, last probe in budget)", k, got[k])
	}
	if k := planKey("o/b", boundary); got[k].Class != PlanningTrustUnresolved {
		t.Errorf("%s = %+v, want trust unresolved (higher repo loses the tie, budget spent)", k, got[k])
	}
	calls := f.calls(t)
	if n := countCalls(calls, fmt.Sprintf("repos/o/b/issues/%d/comments", boundary)); n != 0 {
		t.Errorf("made %d comments reads for o/b#%d, want 0 (past the budget)", n, boundary)
	}
	if n := countCalls(calls, "/comments"); n != maxPlanningTrustProbes {
		t.Errorf("made %d comments reads, want exactly %d", n, maxPlanningTrustProbes)
	}
}

// TestResolvePlanningTrust_OneLoginAcrossCandidatesCostsOneLookup covers the
// per-pass, per-repo cache: one author and one commenter shared by several
// candidates in one repo cost exactly one permission call each.
func TestResolvePlanningTrust_OneLoginAcrossCandidatesCostsOneLookup(t *testing.T) {
	f := installTrustFakeGH(t)
	f.setPermission(t, "octocat", "admin")
	f.setPermission(t, "maintainer", "write")
	for _, n := range []int{1, 2, 3} {
		f.setComments(t, "o/r", n, trustCommentsJSON(t, trustComment{id: int64(100 + n), login: "maintainer", assoc: "MEMBER"}))
	}
	got := resolvePlanningTrust(trustInputs(trustCandidate(1), trustCandidate(2), trustCandidate(3)), &bytes.Buffer{})
	for _, n := range []int{1, 2, 3} {
		if got[planKey("o/r", n)].Class != PlanningTrustTrusted {
			t.Errorf("o/r#%d = %+v, want trusted", n, got[planKey("o/r", n)])
		}
	}
	calls := f.calls(t)
	if n := countCalls(calls, "collaborators/maintainer/permission"); n != 1 {
		t.Errorf("maintainer looked up %d times, want exactly 1: %q", n, calls)
	}
	if n := countCalls(calls, "collaborators/octocat/permission"); n != 1 {
		t.Errorf("octocat looked up %d times, want exactly 1: %q", n, calls)
	}
}

// TestResolvePlanningTrust_LookupCapReachedIsPermissionUnresolved: a repo
// needing more distinct lookups than maxPermissionLookupsPerRepo holds the
// candidate whose lookup goes over the cap as permission unresolved.
func TestResolvePlanningTrust_LookupCapReachedIsPermissionUnresolved(t *testing.T) {
	f := installTrustFakeGH(t)
	f.setPermission(t, "octocat", "admin")
	var cs []trustComment
	for i := 1; i <= maxPermissionLookupsPerRepo; i++ { // author + 50 commenters = 51 lookups
		login := fmt.Sprintf("writer%d", i)
		f.setPermission(t, login, "write")
		cs = append(cs, trustComment{id: int64(i), login: login, assoc: "COLLABORATOR"})
	}
	f.setComments(t, "o/r", 42, trustCommentsJSON(t, cs...))

	got := resolvePlanningTrust(trustInputs(trustCandidate(42)), &bytes.Buffer{})
	if got["o/r#42"].Class != PlanningTrustPermissionUnresolved {
		t.Errorf("o/r#42 = %+v, want permission unresolved (lookup cap reached)", got["o/r#42"])
	}
	if n := countCalls(f.calls(t), "/permission"); n != maxPermissionLookupsPerRepo {
		t.Errorf("made %d permission lookups, want exactly the cap %d", n, maxPermissionLookupsPerRepo)
	}
}

// TestResolvePlanningTrust_DeletedCommentReleasesOnNextPass: no state is
// carried over from an earlier hold.
func TestResolvePlanningTrust_DeletedCommentReleasesOnNextPass(t *testing.T) {
	f := installTrustFakeGH(t)
	f.setPermission(t, "octocat", "admin")
	f.setComments(t, "o/r", 42, trustCommentsJSON(t, trustComment{id: 7, login: "drive-by", assoc: "NONE"}))
	in := trustInputs(trustCandidate(42))

	first := resolvePlanningTrust(in, &bytes.Buffer{})
	if want := (PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 7}); first["o/r#42"] != want {
		t.Fatalf("pass 1 = %+v, want %+v", first["o/r#42"], want)
	}
	f.setComments(t, "o/r", 42, `[]`) // the operator deletes the comment
	second := resolvePlanningTrust(in, &bytes.Buffer{})
	if second["o/r#42"].Class != PlanningTrustTrusted {
		t.Errorf("pass 2 = %+v, want trusted once the comment is deleted", second["o/r#42"])
	}
}

// TestResolvePlanningTrust_RevokedAccessHoldsOnNextPass: permission is never
// cached across passes, so a collaborator whose access is removed holds the
// ticket on the very next pass.
func TestResolvePlanningTrust_RevokedAccessHoldsOnNextPass(t *testing.T) {
	f := installTrustFakeGH(t)
	f.setPermission(t, "octocat", "admin")
	f.setPermission(t, "maintainer", "write")
	f.setComments(t, "o/r", 42, trustCommentsJSON(t, trustComment{id: 9, login: "maintainer", assoc: "COLLABORATOR"}))
	in := trustInputs(trustCandidate(42))

	if got := resolvePlanningTrust(in, &bytes.Buffer{}); got["o/r#42"].Class != PlanningTrustTrusted {
		t.Fatalf("pass 1 = %+v, want trusted", got["o/r#42"])
	}
	f.setPermission(t, "maintainer", "none") // access removed
	got := resolvePlanningTrust(in, &bytes.Buffer{})
	if want := (PlanningTrustProbe{Class: PlanningTrustCommentDenied, CommentID: 9}); got["o/r#42"] != want {
		t.Errorf("pass 2 = %+v, want %+v", got["o/r#42"], want)
	}
	if n := countCalls(f.calls(t), "collaborators/maintainer/permission"); n != 2 {
		t.Errorf("maintainer looked up %d times over two passes, want 2 (no cross-pass cache)", n)
	}
}

// TestResolvePlanningTrust_ZeroCallsForNonCandidates covers the shared
// candidate predicate: nothing is probed when planRefined is off, the repo is
// not lean (attended-narrowed included), or the ticket can never be a
// planning or re-plan pickup.
func TestResolvePlanningTrust_ZeroCallsForNonCandidates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(in *Inputs)
	}{
		{"planRefined off", func(in *Inputs) { in.Config.PlanRefined = false }},
		{"repo not lean", func(in *Inputs) { in.RepoAutonomy = map[string]RepoAutonomy{"o/r": RepoAutonomyInteractive} }},
		{"attended-narrowed repo", func(in *Inputs) { in.RepoAutonomy = map[string]RepoAutonomy{"o/r": RepoAutonomyAttended} }},
		{"no autonomy entry", func(in *Inputs) { in.RepoAutonomy = nil }},
		{"assigned to someone else", func(in *Inputs) { in.Tickets[0].Assignees = []string{"someone"} }},
		{"multiple assignees", func(in *Inputs) { in.Tickets[0].Assignees = []string{"octocat", "someone"} }},
		{"unassigned", func(in *Inputs) { in.Tickets[0].Assignees = nil }},
		{"already Working", func(in *Inputs) { in.Tickets[0].Labels = []string{"Refined", "Working"} }},
		{"Blocked", func(in *Inputs) { in.Tickets[0].Labels = []string{"Refined", "Blocked"} }},
		{"Input Needed", func(in *Inputs) { in.Tickets[0].Labels = []string{"Refined", "Input Needed"} }},
		{"Refined and Planned with no plan", func(in *Inputs) { in.Tickets[0].Labels = []string{"Refined", "Planned"} }},
		{"not Refined", func(in *Inputs) { in.Tickets[0].Labels = []string{"Ready"} }},
		{"main sync diverged", func(in *Inputs) { in.Tickets[0].MainSync = MainSyncDiverged }},
		{"plan inventory unreadable", func(in *Inputs) {
			in.PlanInventories = map[string]PlanInventory{"o/r": PlanInventoryUnreadable}
		}},
		{"plan file malformed", func(in *Inputs) { in.PlanProbes = map[string]PlanProbe{"o/r#42": PlanProbeParseError} }},
		{"machine-local filter excludes it", func(in *Inputs) {
			in.Config.Repos = []RepoConfig{{Repo: "o/r", Filter: RepoFilter{Milestones: []int{9}}}}
		}},
		{"fresh Planned plan (ordinary dispatch)", func(in *Inputs) {
			in.Tickets[0].Labels = []string{"Planned"}
			in.Plans = []Plan{{Repo: "o/r", TicketID: 42, Status: "planned", CommitsBehind: 0}}
		}},
		{"stale plan not yet planned", func(in *Inputs) {
			in.Tickets[0].Labels = []string{"Planned"}
			in.Plans = []Plan{{Repo: "o/r", TicketID: 42, Status: "draft", CommitsBehind: 10}}
		}},
		{"stale plan with staleness error", func(in *Inputs) {
			in.Tickets[0].Labels = []string{"Planned"}
			in.Plans = []Plan{{Repo: "o/r", TicketID: 42, Status: "planned", CommitsBehind: 10}}
			in.PlanProbes = map[string]PlanProbe{"o/r#42": PlanProbeStalenessError}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := installTrustFakeGH(t)
			in := trustInputs(trustCandidate(42))
			c.mutate(&in)
			got := resolvePlanningTrust(in, &bytes.Buffer{})
			if calls := f.calls(t); len(calls) != 0 {
				t.Errorf("made gh calls %q, want zero for a non-candidate", calls)
			}
			if _, ok := got["o/r#42"]; ok {
				t.Errorf("o/r#42 got a trust entry %+v, want none for a non-candidate", got["o/r#42"])
			}
		})
	}
}

// TestResolvePlanningTrust_StaleReplanCandidateIsProbed: the autonomous
// re-plan path is a candidate too.
func TestResolvePlanningTrust_StaleReplanCandidateIsProbed(t *testing.T) {
	f := installTrustFakeGH(t)
	f.setComments(t, "o/r", 42, `[]`)
	f.setPermission(t, "octocat", "write")
	tk := trustCandidate(42)
	tk.Labels = []string{"Planned"}
	in := trustInputs(tk)
	in.Plans = []Plan{{Repo: "o/r", TicketID: 42, Status: "planned", CommitsBehind: 10}}
	got := resolvePlanningTrust(in, &bytes.Buffer{})
	if got["o/r#42"].Class != PlanningTrustTrusted {
		t.Errorf("o/r#42 = %+v, want trusted", got["o/r#42"])
	}
	if n := countCalls(f.calls(t), "/comments"); n != 1 {
		t.Errorf("made %d comments reads, want 1", n)
	}
}

// TestResolvePlanningTrust_LogsCarryNoBodyText: probe log lines never carry
// comment or issue body text and never the lazyboards decision substrings,
// on every failure path that has stdout to leak.
func TestResolvePlanningTrust_LogsCarryNoBodyText(t *testing.T) {
	const sentinel = "BODY-SENTINEL-847"
	cases := []struct {
		name   string
		script string
	}{
		{"malformed JSON carrying body text", `printf '[{"id":1,"body":"` + sentinel + `"'`},
		{"API error with body text on stdout", `printf '[{"id":1,"body":"` + sentinel + `"}]'; echo "HTTP 500" >&2; exit 1`},
		{"oversized thread of body text", `yes ` + sentinel + ` | head -c 1100000; exit 0`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			installTrustCommentsScriptGH(t, c.script)
			var buf bytes.Buffer
			got := resolvePlanningTrust(trustInputs(trustCandidate(42)), &buf)
			if got["o/r#42"].Class != PlanningTrustUnresolved {
				t.Errorf("o/r#42 = %+v, want trust unresolved", got["o/r#42"])
			}
			assertTrustLogClean(t, buf.String(), sentinel)
		})
	}

	t.Run("held comment with body text", func(t *testing.T) {
		f := installTrustFakeGH(t)
		f.setPermission(t, "octocat", "admin")
		f.setComments(t, "o/r", 42, trustCommentsJSON(t, trustComment{id: 5, login: "drive-by", assoc: "NONE", body: sentinel}))
		var buf bytes.Buffer
		resolvePlanningTrust(trustInputs(trustCandidate(42)), &buf)
		assertTrustLogClean(t, buf.String(), sentinel)
	})
}

func assertTrustLogClean(t *testing.T, log, sentinel string) {
	t.Helper()
	if strings.Contains(log, sentinel) {
		t.Errorf("log carries body text %q: %q", sentinel, log)
	}
	for _, line := range strings.Split(log, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "dispatch: ") {
			t.Errorf("log line %q must start with %q", line, "dispatch: ")
		}
		if strings.Contains(line, " skip:") || strings.Contains(line, " dispatch ") {
			t.Errorf("log line %q must not carry the lazyboards decision substrings", line)
		}
	}
}

// TestPlanningTrustParity_DecideConsultsOnlyProbeCandidates is the
// predicate-drift backstop's companion: across a grid of ticket shapes, every
// ticket Decide holds as "probe unrecognized" with an empty trust map is one
// the probe would have probed, and with every probe candidate trusted no
// ticket is ever held as unrecognized.
func TestPlanningTrustParity_DecideConsultsOnlyProbeCandidates(t *testing.T) {
	labelSets := [][]string{
		{"Refined"}, {"Planned"}, {"Refined", "Planned"}, {"Refined", "Working"},
		{"Planned", "Blocked"}, {"Input Needed"}, {"Refined", "Input Needed"}, {"Ready"},
	}
	assigneeSets := [][]string{{"octocat"}, {"OctoCat"}, {"someone"}, {"octocat", "someone"}, nil}
	planShapes := []*Plan{
		nil,
		{Status: "planned", CommitsBehind: 0},
		{Status: "planned", CommitsBehind: 10},
		{Status: "draft", CommitsBehind: 10},
		{Status: "awaiting-input", CommitsBehind: 10},
	}
	autonomies := []RepoAutonomy{RepoAutonomyLean, RepoAutonomyInteractive, RepoAutonomyAttended}

	var tickets []Ticket
	var plans []Plan
	n := 0
	for _, labels := range labelSets {
		for _, assignees := range assigneeSets {
			for _, shape := range planShapes {
				for _, openPR := range []bool{false, true} {
					n++
					tickets = append(tickets, Ticket{Repo: "o/r", Number: n, Labels: labels, Assignees: assignees, HasOpenPR: openPR, Author: "octocat"})
					if shape != nil {
						p := *shape
						p.Repo, p.TicketID, p.Path = "o/r", n, fmt.Sprintf(".plans/%d-x.md", n)
						plans = append(plans, p)
					}
				}
			}
		}
	}

	for _, autonomy := range autonomies {
		for _, planRefined := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/planRefined=%v", autonomy, planRefined), func(t *testing.T) {
				in := baseInputs()
				in.Tickets, in.Plans = tickets, plans
				in.Config.PlanRefined = planRefined
				in.RepoAutonomy = map[string]RepoAutonomy{"o/r": autonomy}

				candidates := map[string]bool{}
				for _, c := range planningTrustCandidates(in) {
					candidates[planKey(c.Repo, c.Number)] = true
				}

				in.PlanningTrust = nil
				for _, d := range Decide(in) {
					key := planKey(d.Ticket.Repo, d.Ticket.Number)
					if strings.Contains(d.Reason, "planning held: ticket trust probe unrecognized") && !candidates[key] {
						t.Errorf("%s held as %q but the probe would never probe it", key, d.Reason)
					}
					if d.Action == ActionDispatch && d.Planning {
						t.Errorf("%s dispatched a planning session with no trust entry", key)
					}
				}

				in.PlanningTrust = map[string]PlanningTrustProbe{}
				for key := range candidates {
					in.PlanningTrust[key] = PlanningTrustProbe{Class: PlanningTrustTrusted}
				}
				for _, d := range Decide(in) {
					if strings.Contains(d.Reason, "probe unrecognized") && strings.Contains(d.Reason, "planning held") {
						t.Errorf("%s held as %q although every probe candidate is trusted", planKey(d.Ticket.Repo, d.Ticket.Number), d.Reason)
					}
				}
			})
		}
	}
}
