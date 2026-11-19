package babysit

// #862: the actor rule and its lookups. The tick-level and recheck-level behavior
// is covered through the fake gh in babysit_test.go and automerge_test.go; this file
// holds the pieces whose matrix is combinatorial over injected failures and that a
// tick can not reach one at a time (the rule's every branch, the identity lookup's
// validation, the dismissal timeline's pagination, null actors, conflicting events,
// memoization and the sticky error). Each case is a fake func or a stubbed execGh;
// nothing here sleeps or spawns a process.

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// fakeActors is an actorContext's three lookups as plain data, with counters. The
// automation login defaults to defaultAutomationLogin, no review has a dismissal
// event, and every login's permission is denied unless perms says otherwise.
type fakeActors struct {
	login         string
	loginErr      error
	dismissals    map[int64]string
	dismissalsErr error
	perms         map[string]writePermission

	loginReads, dismissalReads int
	permReads                  map[string]int
}

func newFakeActors() *fakeActors {
	return &fakeActors{login: defaultAutomationLogin, perms: map[string]writePermission{}, permReads: map[string]int{}}
}

func (f *fakeActors) ctx() *actorContext {
	return newActorContext(
		func() (string, error) {
			f.loginReads++
			return f.login, f.loginErr
		},
		func() (map[int64]string, error) {
			f.dismissalReads++
			return f.dismissals, f.dismissalsErr
		},
		func(login string) writePermission {
			f.permReads[login]++
			if p, ok := f.perms[login]; ok {
				return p
			}
			return writePermissionDenied
		},
	)
}

// okActors is an actor context over the default fake with the given dismissal events
// (review databaseId -> actor login).
func okActors(dismissals map[int64]string) *actorContext {
	f := newFakeActors()
	f.dismissals = dismissals
	return f.ctx()
}

// resolvedThread is a resolved thread state: resolved by by ("" is a null resolver),
// first comment by author.
func resolvedThread(by, author string) threadState {
	return threadState{Resolved: true, ResolvedBy: by, ResolvedByKnown: by != "", Author: author}
}

// The actor rule, one branch at a time. The rule's order is: an attestation for
// exactly this actor accepts; an unknown actor is denied; the automation login (case
// folded, even as the item's own author) is denied; an actor equal to the non-empty
// author is accepted; anyone else needs admin/write.
func TestClassifyActorRule(t *testing.T) {
	const bot = defaultAutomationLogin
	attest := func(actor string) *actorAttestation {
		return &actorAttestation{Actor: actor, Source: attestationSourceHumanCLI}
	}
	for _, tc := range []struct {
		name      string
		actor     string
		known     bool
		author    string
		attested  *actorAttestation
		perms     map[string]writePermission
		want      actorDecision
		wantLogin int
		wantPerms map[string]int
	}{
		{name: "an attestation for exactly this actor accepts the automation login with no lookup at all",
			actor: bot, known: true, author: "alice", attested: attest(bot), want: actorAccepted},
		{name: "an attestation for an unknown actor accepts a still-unknown actor, so a null resolver can be recovered",
			actor: "", known: false, author: "alice", attested: attest(""), want: actorAccepted},
		{name: "an attestation for another actor does not vouch for the automation login",
			actor: bot, known: true, author: "alice", attested: attest("maintainer"), want: actorDenied, wantLogin: 1},
		{name: "an unknown actor is denied before any lookup",
			actor: "", known: false, author: "alice", want: actorDenied},
		{name: "the automation login is denied and costs no permission lookup",
			actor: bot, known: true, author: "alice", want: actorDenied, wantLogin: 1},
		{name: "the automation login is denied even as the item's own author",
			actor: bot, known: true, author: bot, want: actorDenied, wantLogin: 1},
		{name: "a case variant of the automation login is the automation login",
			actor: strings.ToUpper(bot), known: true, author: "alice", want: actorDenied, wantLogin: 1},
		{name: "the item's own author is accepted with no permission lookup",
			actor: "alice", known: true, author: "alice", want: actorAccepted, wantLogin: 1},
		{name: "author equality is exact: a case variant of the author needs write access",
			actor: "Alice", known: true, author: "alice", perms: map[string]writePermission{"Alice": writePermissionGranted},
			want: actorAccepted, wantLogin: 1, wantPerms: map[string]int{"Alice": 1}},
		{name: "author equality is exact: a case variant without write access is denied",
			actor: "Alice", known: true, author: "alice", want: actorDenied, wantLogin: 1, wantPerms: map[string]int{"Alice": 1}},
		{name: "an empty author never matches: the actor needs write access",
			actor: "alice", known: true, author: "", perms: map[string]writePermission{"alice": writePermissionGranted},
			want: actorAccepted, wantLogin: 1, wantPerms: map[string]int{"alice": 1}},
		{name: "a write-access actor that is not the author is accepted after one lookup",
			actor: "maintainer", known: true, author: "alice", perms: map[string]writePermission{"maintainer": writePermissionGranted},
			want: actorAccepted, wantLogin: 1, wantPerms: map[string]int{"maintainer": 1}},
		{name: "an actor GitHub says lacks write access is denied",
			actor: "outsider", known: true, author: "alice", perms: map[string]writePermission{"outsider": writePermissionDenied},
			want: actorDenied, wantLogin: 1, wantPerms: map[string]int{"outsider": 1}},
		{name: "an unknown permission is a lookup failure, not a denial",
			actor: "outsider", known: true, author: "alice", perms: map[string]writePermission{"outsider": writePermissionUnknown},
			want: actorLookupFailed, wantLogin: 1, wantPerms: map[string]int{"outsider": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeActors()
			for login, p := range tc.perms {
				f.perms[login] = p
			}
			c := f.ctx()

			got := classifyActor(c, tc.actor, tc.known, tc.author, tc.attested)

			if got != tc.want {
				t.Errorf("classifyActor = %v, want %v", got, tc.want)
			}
			if f.loginReads != tc.wantLogin {
				t.Errorf("automation login reads = %d, want %d", f.loginReads, tc.wantLogin)
			}
			if !reflect.DeepEqual(f.permReads, orEmpty(tc.wantPerms)) {
				t.Errorf("permission reads = %v, want %v", f.permReads, tc.wantPerms)
			}
			if c.err != nil {
				t.Errorf("sticky error = %v, want none: a per-key permission failure must not fail the whole pass", c.err)
			}
		})
	}
}

// A failed automation-login lookup is a lookup failure for the decision and a sticky
// pass-level error wrapping errAutomationIdentity; it is read once, and every later
// decision in the pass fails the same way without another read. An attestation for the
// exact actor still accepts without needing the login.
func TestClassifyActorAutomationIdentityFailureIsStickyAndPassWide(t *testing.T) {
	f := newFakeActors()
	f.loginErr = errors.New("gh: api user failed")
	c := f.ctx()

	for i := range 3 {
		if got := classifyActor(c, "maintainer", true, "alice", nil); got != actorLookupFailed {
			t.Fatalf("decision %d = %v, want actorLookupFailed", i, got)
		}
	}
	if !errors.Is(c.err, errAutomationIdentity) {
		t.Errorf("sticky error = %v, want errors.Is errAutomationIdentity", c.err)
	}
	if f.loginReads != 1 {
		t.Errorf("automation login reads = %d, want exactly 1: the failure is sticky for the pass", f.loginReads)
	}
	if len(f.permReads) != 0 {
		t.Errorf("permission reads = %v, want none while the identity is unknown", f.permReads)
	}
	if got := classifyActor(c, defaultAutomationLogin, true, "alice", &actorAttestation{Actor: defaultAutomationLogin}); got != actorAccepted {
		t.Errorf("attested decision = %v, want actorAccepted: an attestation needs no identity lookup", got)
	}
}

// The login is read at most once per context however many decisions need it.
func TestClassifyActorReadsTheAutomationLoginOncePerContext(t *testing.T) {
	f := newFakeActors()
	f.perms["maintainer"] = writePermissionGranted
	c := f.ctx()

	for _, actor := range []string{"alice", "bob", "maintainer", "carol"} {
		classifyActor(c, actor, true, "alice", nil)
	}
	if f.loginReads != 1 {
		t.Errorf("automation login reads = %d, want exactly 1 for four decisions", f.loginReads)
	}
}

// Edge 47 at the rule level: a login that fails githubLoginPattern (an App or `[bot]`)
// is denied through the real permission resolver without any `gh` call.
func TestClassifyActorBotLoginIsDeniedWithoutAGhCall(t *testing.T) {
	var calls [][]string
	original := execGh
	execGh = func(args ...string) (string, string, error) {
		calls = append(calls, args)
		return "", "", errors.New("unexpected gh call")
	}
	t.Cleanup(func() { execGh = original })
	c := newActorContext(func() (string, error) { return defaultAutomationLogin, nil }, nil, permissionResolver("o/r"))

	if got := classifyActor(c, "dependabot[bot]", true, "alice", nil); got != actorDenied {
		t.Errorf("classifyActor = %v, want actorDenied", got)
	}
	if len(calls) != 0 {
		t.Errorf("gh calls = %v, want none for a login that fails the pattern", calls)
	}
}

// dismissalActor answers from one memoized timeline read: a matched event is
// (actor, known); no event, a null actor and conflicting events are all unknown.
func TestDismissalActorIsMemoizedAndKnownOnlyForAMatchedActor(t *testing.T) {
	f := newFakeActors()
	f.dismissals = map[int64]string{10: "alice", 11: ""}
	c := f.ctx()

	if actor, known := c.dismissalActor(10); actor != "alice" || !known {
		t.Errorf("dismissalActor(10) = %q, %v, want alice, true", actor, known)
	}
	if _, known := c.dismissalActor(11); known {
		t.Error("dismissalActor(11) known = true, want false for an unknown actor")
	}
	if _, known := c.dismissalActor(12); known {
		t.Error("dismissalActor(12) known = true, want false: no event for that review")
	}
	if f.dismissalReads != 1 {
		t.Errorf("dismissal timeline reads = %d, want exactly 1 for three lookups", f.dismissalReads)
	}
	if c.err != nil {
		t.Errorf("sticky error = %v, want none", c.err)
	}
}

// A failed or truncated timeline read is sticky for the pass, keeps its class
// (errors.Is) so the caller can pick the unreadable or truncated hold, and is not
// retried within the pass.
func TestDismissalActorReadFailureIsStickyAndKeepsItsClass(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"truncated", fmt.Errorf("%w: exceeded maxThreadPages", errFeedbackTruncated)},
		{"unreadable", errors.New("gh: timeline failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeActors()
			f.dismissalsErr = tc.err
			c := f.ctx()

			if _, known := c.dismissalActor(10); known {
				t.Error("dismissalActor known = true, want false on a failed read")
			}
			c.dismissalActor(11)
			if !errors.Is(c.err, tc.err) {
				t.Errorf("sticky error = %v, want it to wrap %v", c.err, tc.err)
			}
			if f.dismissalReads != 1 {
				t.Errorf("dismissal timeline reads = %d, want exactly 1: a failed read is not retried in the pass", f.dismissalReads)
			}
		})
	}
}

// -- fetchAutomationLogin ------------------------------------------------------

// stubGh serves one fixed answer for every gh call and records the calls.
func stubGh(t *testing.T, stdout string, err error) *[][]string {
	t.Helper()
	var calls [][]string
	original := execGh
	execGh = func(args ...string) (string, string, error) {
		calls = append(calls, args)
		return stdout, "", err
	}
	t.Cleanup(func() { execGh = original })
	return &calls
}

func TestFetchAutomationLoginReadsTheLoginGhReports(t *testing.T) {
	calls := stubGh(t, `{"login":"cenci-bot","id":1}`, nil)

	got, err := fetchAutomationLogin()

	if err != nil || got != "cenci-bot" {
		t.Fatalf("fetchAutomationLogin = %q, %v, want cenci-bot, nil", got, err)
	}
	if want := [][]string{{"api", "user"}}; !reflect.DeepEqual(*calls, want) {
		t.Errorf("gh calls = %v, want %v", *calls, want)
	}
}

// Edge 49: a gh error, an undecodable body, an empty login and a login failing
// githubLoginPattern are all an identity failure (errAutomationIdentity), never a
// usable login. Each case also asserts the one `gh api user` call really ran.
func TestFetchAutomationLoginRejectsAnUnusableAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stdout string
		err    error
	}{
		{"gh fails", "", errors.New("exit status 1")},
		{"an undecodable body", "not json", nil},
		{"a body with no login", `{"id":1}`, nil},
		{"an empty login", `{"login":""}`, nil},
		{"a login with a space", `{"login":"not a login"}`, nil},
		{"a bot login", `{"login":"app[bot]"}`, nil},
		{"a login starting with a hyphen", `{"login":"-bot"}`, nil},
		{"a login over 39 characters", `{"login":"` + strings.Repeat("a", 40) + `"}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubGh(t, tc.stdout, tc.err)

			got, err := fetchAutomationLogin()

			if len(*calls) != 1 {
				t.Fatalf("gh calls = %v, want exactly one `api user`", *calls)
			}
			if err == nil || !errors.Is(err, errAutomationIdentity) {
				t.Fatalf("fetchAutomationLogin = %q, %v, want an error wrapping errAutomationIdentity", got, err)
			}
			if got != "" {
				t.Errorf("login = %q, want none alongside the error", got)
			}
		})
	}
}

// -- fetchDismissalActors ------------------------------------------------------

func TestFetchDismissalActorsMapsEachReviewToItsDismissingActor(t *testing.T) {
	var calls [][]string
	scriptedGraphQLCommand(t, []string{dismissalPage(false, "", dismissalEvent(10, "alice"), dismissalEvent(11, "maintainer"))}, &calls)

	got, err := fetchDismissalActors("o/r", "42")

	if err != nil {
		t.Fatalf("fetchDismissalActors: %v", err)
	}
	if want := map[int64]string{10: "alice", 11: "maintainer"}; !reflect.DeepEqual(got, want) {
		t.Errorf("actors = %v, want %v", got, want)
	}
	if len(calls) != 1 {
		t.Fatalf("gh calls = %v, want one", calls)
	}
	joined := strings.Join(calls[0], " ")
	if !strings.Contains(joined, "REVIEW_DISMISSED_EVENT") || !strings.Contains(joined, "number=42") || strings.Contains(joined, "cursor=") {
		t.Errorf("first call = %v, want the REVIEW_DISMISSED_EVENT query for PR 42 with no cursor", calls[0])
	}
}

func TestFetchDismissalActorsTraversesEveryPage(t *testing.T) {
	var calls [][]string
	scriptedGraphQLCommand(t, []string{
		dismissalPage(true, "CURSOR1", dismissalEvent(10, "alice")),
		dismissalPage(false, "", dismissalEvent(11, "maintainer")),
	}, &calls)

	got, err := fetchDismissalActors("o/r", "42")

	if err != nil {
		t.Fatalf("fetchDismissalActors: %v", err)
	}
	if want := map[int64]string{10: "alice", 11: "maintainer"}; !reflect.DeepEqual(got, want) {
		t.Errorf("actors = %v, want %v merged across both pages", got, want)
	}
	if len(calls) != 2 || !strings.Contains(strings.Join(calls[1], " "), "cursor=CURSOR1") {
		t.Errorf("gh calls = %v, want two, the second carrying the first page's endCursor", calls)
	}
}

// A null actor (a deleted account) and several events for one review naming
// different actors are both an unknown actor: "" for that review, never one of the
// named logins. A review with no event at all is absent.
func TestFetchDismissalActorsTreatsANullOrConflictingActorAsUnknown(t *testing.T) {
	var calls [][]string
	scriptedGraphQLCommand(t, []string{dismissalPage(false, "",
		dismissalEvent(10, ""),
		dismissalEvent(11, "alice"), dismissalEvent(11, "maintainer"),
		dismissalEvent(12, "bob"),
	)}, &calls)

	got, err := fetchDismissalActors("o/r", "42")

	if err != nil {
		t.Fatalf("fetchDismissalActors: %v", err)
	}
	want := map[int64]string{10: "", 11: "", 12: "bob"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("actors = %v, want %v", got, want)
	}
	if _, ok := got[13]; ok {
		t.Error("a review with no event must be absent from the map")
	}
}

// The bounded cursor loop fails closed: an exhausted page cap with hasNextPage still
// true is errFeedbackTruncated after exactly maxThreadPages calls.
func TestFetchDismissalActorsFailsClosedWhenTheTimelineIsTruncated(t *testing.T) {
	bodies := make([]string, maxThreadPages)
	for i := range bodies {
		bodies[i] = dismissalPage(true, fmt.Sprintf("C%d", i), dismissalEvent(int64(100+i), "alice"))
	}
	var calls [][]string
	scriptedGraphQLCommand(t, bodies, &calls)

	got, err := fetchDismissalActors("o/r", "42")

	if !errors.Is(err, errFeedbackTruncated) {
		t.Fatalf("fetchDismissalActors err = %v, want errors.Is errFeedbackTruncated", err)
	}
	if got != nil {
		t.Errorf("actors = %v, want none alongside the error: a partial timeline must never be used", got)
	}
	if len(calls) != maxThreadPages {
		t.Errorf("gh calls = %d, want exactly maxThreadPages (%d)", len(calls), maxThreadPages)
	}
}

// A GraphQL errors[] envelope next to valid-looking data, malformed JSON, a gh failure
// and a malformed repo are each a distinct, non-truncated failure.
func TestFetchDismissalActorsFailsClosedOnAnUnreadableRead(t *testing.T) {
	t.Run("an errors envelope", func(t *testing.T) {
		body := `{"data":{"repository":{"pullRequest":{"timelineItems":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}},"errors":[{"message":"Something went wrong"}]}`
		var calls [][]string
		scriptedGraphQLCommand(t, []string{body}, &calls)

		_, err := fetchDismissalActors("o/r", "42")

		if err == nil || errors.Is(err, errFeedbackTruncated) {
			t.Fatalf("err = %v, want an unreadable (non-truncated) error", err)
		}
		if want := reasonFeedbackUnreadable + ": Something went wrong"; err.Error() != want {
			t.Errorf("err = %q, want exactly %q (the errors[] branch, not a raw-body echo)", err.Error(), want)
		}
	})
	t.Run("malformed JSON", func(t *testing.T) {
		var calls [][]string
		scriptedGraphQLCommand(t, []string{"not json"}, &calls)

		if _, err := fetchDismissalActors("o/r", "42"); err == nil || errors.Is(err, errFeedbackTruncated) {
			t.Fatalf("err = %v, want an unreadable (non-truncated) error", err)
		}
	})
	t.Run("gh fails", func(t *testing.T) {
		calls := stubGh(t, "", errors.New("exit status 1"))

		if _, err := fetchDismissalActors("o/r", "42"); err == nil || errors.Is(err, errFeedbackTruncated) {
			t.Fatalf("err = %v, want an unreadable (non-truncated) error", err)
		}
		if len(*calls) != 1 {
			t.Errorf("gh calls = %v, want the one failing call", *calls)
		}
	})
	t.Run("a malformed repo makes no call", func(t *testing.T) {
		calls := stubGh(t, "", nil)

		if _, err := fetchDismissalActors("norepo", "42"); err == nil {
			t.Fatal("err = nil, want a malformed-repo error")
		}
		if len(*calls) != 0 {
			t.Errorf("gh calls = %v, want none", *calls)
		}
	})
}
