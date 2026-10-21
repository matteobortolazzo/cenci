package pipeline

// Behavior tests for the `cenci pipeline followup` engine (#1339). They drive
// Followup through the package-internal `command` seam against a stateful
// fake GitHub (fakeFollowupGh), recording every call. Every install registers
// a cleanup audit of the P44 invariants (no label removal or grant, no close,
// no PATCH, `issue edit` carries only --body-file/--repo) over all recorded
// calls. Failure texts are deliberately non-transient so ghRetry's real
// sleeps never trigger.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	fuPR               = "https://github.com/o/r/pull/9"
	fuSlug             = "o/r"
	fuTitle            = "watch: tidy the followup docs"
	fuLabelDescription = "Deferred/out-of-scope item captured from a session — triage before working"
	fuLine1            = "- [ ] Add retry docs — `watch`"
	fuLine2            = "- [ ] Cover edge case — `flow`"
	fuServerError      = "HTTP 500: internal server error"
)

var fuItems = []FollowupItem{
	{Context: "Add retry docs", Area: "watch"},
	{Context: "Cover edge case", Area: "flow"},
}

var fuExcludedLabels = []string{
	"Refined", "Working", "Planned", "In Review", "Implemented", "Input Needed",
	"Design", "Designed", "automerge:ok", "Browser", "ui:visual-check",
	"dispatch-failed", "plan-invalid", "reconcile-stuck",
}

var fuUnstartedGateLabels = []string{
	"Refined", "Planned", "Working", "In Review", "Implemented", "Input Needed", "Design", "Designed",
}

// -- fake GitHub ----------------------------------------------------------

type fuIssue struct {
	Number    int
	Title     string
	Body      string
	Labels    []string
	Assignees []string
	Milestone int
	State     string
}

type fuFailRule struct {
	tokens      []string
	text        string
	skip, times int
}

type fuPost struct {
	raw     string
	payload map[string]json.RawMessage
	labels  []string
}

type fuEdit struct {
	number int
	body   string
	call   ghCall
}

type fakeFollowupGh struct {
	t          *testing.T
	issues     map[int]*fuIssue
	nextNumber int
	calls      []ghCall
	rules      []*fuFailRule
	edits      []fuEdit
	posts      []fuPost
	tempFiles  []string

	// labelCreate is "ok", "already-exists" or "fail".
	labelCreate string
	// postStoredTitle/postStoredBody override what the server stores for a
	// created issue, to model a title mismatch or a partially written body.
	postStoredTitle string
	postStoredBody  string
	// postCreatesThenErrors makes the next N POSTs create the issue and still
	// return an error (the ambiguous failure P14 guards against).
	postCreatesThenErrors int
	// onList runs after each `issue list` response is built.
	onList func()
}

func newFuWorld(t *testing.T) *fakeFollowupGh {
	t.Helper()
	w := &fakeFollowupGh{t: t, issues: map[int]*fuIssue{}, nextNumber: 100, labelCreate: "ok"}
	w.add(fuIssue{Number: 7, Title: "watch: origin ticket", Labels: []string{"enhancement"}})
	original := command
	command = func(name string, args ...string) ([]byte, error) {
		call := ghCall{name: name, args: args}
		w.calls = append(w.calls, call)
		return w.respond(call)
	}
	t.Cleanup(func() { command = original })
	t.Cleanup(w.audit)
	return w
}

func (w *fakeFollowupGh) add(i fuIssue) *fuIssue {
	if i.State == "" {
		i.State = "OPEN"
	}
	w.issues[i.Number] = &i
	return &i
}

func (w *fakeFollowupGh) inject(skip, times int, text string, tokens ...string) {
	w.rules = append(w.rules, &fuFailRule{tokens: tokens, text: text, skip: skip, times: times})
}

func (w *fakeFollowupGh) callsIs(tokens ...string) []ghCall {
	var out []ghCall
	for _, c := range w.calls {
		if c.is(tokens...) {
			out = append(out, c)
		}
	}
	return out
}

func (w *fakeFollowupGh) mutations() []ghCall {
	var out []ghCall
	for _, c := range w.calls {
		if c.is("issue", "edit") || c.is("api") || c.is("label", "create") {
			out = append(out, c)
		}
	}
	return out
}

func (w *fakeFollowupGh) callStrings() []string {
	out := make([]string, 0, len(w.calls))
	for _, c := range w.calls {
		out = append(out, c.String())
	}
	return out
}

func (w *fakeFollowupGh) respond(call ghCall) ([]byte, error) {
	if call.name != "gh" {
		w.t.Fatalf("unexpected command %s", call)
	}
	for _, r := range w.rules {
		if !call.is(r.tokens...) {
			continue
		}
		if r.skip > 0 {
			r.skip--
			continue
		}
		if r.times > 0 {
			r.times--
			return []byte(r.text), fmt.Errorf("exit status 1")
		}
	}
	switch {
	case call.is("issue", "view"):
		return w.view(call)
	case call.is("issue", "list"):
		return w.list(call)
	case call.is("issue", "edit"):
		return w.edit(call)
	case call.is("label", "create"):
		switch w.labelCreate {
		case "already-exists":
			return []byte("label with name \"Followup\" already exists"), fmt.Errorf("exit status 1")
		case "fail":
			return []byte("HTTP 401: Bad credentials"), fmt.Errorf("exit status 1")
		}
		return nil, nil
	case call.is("api"):
		return w.post(call)
	}
	w.t.Fatalf("unexpected gh invocation: %s", call)
	return nil, nil
}

func (w *fakeFollowupGh) fields(i *fuIssue, names []string) map[string]any {
	out := map[string]any{}
	for _, n := range names {
		switch n {
		case "number":
			out[n] = i.Number
		case "title":
			out[n] = i.Title
		case "body":
			out[n] = i.Body
		case "state":
			out[n] = i.State
		case "labels":
			ls := []map[string]string{}
			for _, l := range i.Labels {
				ls = append(ls, map[string]string{"name": l})
			}
			out[n] = ls
		case "assignees":
			as := []map[string]string{}
			for _, a := range i.Assignees {
				as = append(as, map[string]string{"login": a})
			}
			out[n] = as
		case "milestone":
			if i.Milestone == 0 {
				out[n] = nil
			} else {
				out[n] = map[string]any{"number": i.Milestone, "title": "m"}
			}
		default:
			w.t.Fatalf("fake gh: unsupported --json field %q", n)
		}
	}
	return out
}

func (w *fakeFollowupGh) view(call ghCall) ([]byte, error) {
	n, _ := strconv.Atoi(call.args[2])
	i, ok := w.issues[n]
	if !ok {
		return []byte("GraphQL: Could not resolve to an Issue with the number of " + call.args[2]), fmt.Errorf("exit status 1")
	}
	b, err := json.Marshal(w.fields(i, strings.Split(call.flagValues("--json")[0], ",")))
	if err != nil {
		w.t.Fatalf("marshal view: %v", err)
	}
	return b, nil
}

func (w *fakeFollowupGh) list(call ghCall) ([]byte, error) {
	limit, err := strconv.Atoi(call.flagValues("--limit")[0])
	if err != nil {
		w.t.Fatalf("fake gh: bad --limit: %v", err)
	}
	var nums []int
	for n, i := range w.issues {
		if i.State == "OPEN" && slices.Contains(i.Labels, "Followup") {
			nums = append(nums, n)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(nums)))
	if len(nums) > limit {
		nums = nums[:limit]
	}
	fields := strings.Split(call.flagValues("--json")[0], ",")
	rows := []map[string]any{}
	for _, n := range nums {
		rows = append(rows, w.fields(w.issues[n], fields))
	}
	b, err := json.Marshal(rows)
	if err != nil {
		w.t.Fatalf("marshal list: %v", err)
	}
	if w.onList != nil {
		w.onList()
	}
	return b, nil
}

func (w *fakeFollowupGh) edit(call ghCall) ([]byte, error) {
	n, _ := strconv.Atoi(call.args[2])
	i, ok := w.issues[n]
	if !ok {
		w.t.Fatalf("fake gh: edit of unknown issue %d", n)
	}
	path := call.flagValues("--body-file")[0]
	w.tempFiles = append(w.tempFiles, path)
	b, err := os.ReadFile(path)
	if err != nil {
		w.t.Fatalf("fake gh: read body file: %v", err)
	}
	i.Body = string(b)
	w.edits = append(w.edits, fuEdit{number: n, body: string(b), call: call})
	return nil, nil
}

func (w *fakeFollowupGh) post(call ghCall) ([]byte, error) {
	path := call.flagValues("--input")[0]
	w.tempFiles = append(w.tempFiles, path)
	b, err := os.ReadFile(path)
	if err != nil {
		w.t.Fatalf("fake gh: read payload: %v", err)
	}
	p := fuPost{raw: string(b), payload: map[string]json.RawMessage{}}
	if err := json.Unmarshal(b, &p.payload); err != nil {
		w.t.Fatalf("fake gh: payload is not a JSON object: %v", err)
	}
	if raw, ok := p.payload["labels"]; ok {
		if err := json.Unmarshal(raw, &p.labels); err != nil {
			w.t.Fatalf("fake gh: labels: %v", err)
		}
	}
	w.posts = append(w.posts, p)

	issue := fuIssue{Number: w.nextNumber, State: "OPEN", Labels: p.labels}
	w.nextNumber++
	_ = json.Unmarshal(p.payload["title"], &issue.Title)
	_ = json.Unmarshal(p.payload["body"], &issue.Body)
	if raw, ok := p.payload["milestone"]; ok {
		_ = json.Unmarshal(raw, &issue.Milestone)
	}
	if w.postStoredTitle != "" {
		issue.Title = w.postStoredTitle
	}
	if w.postStoredBody != "" {
		issue.Body = w.postStoredBody
	}
	w.add(issue)
	if w.postCreatesThenErrors > 0 {
		w.postCreatesThenErrors--
		return []byte(fuServerError), fmt.Errorf("exit status 1")
	}
	return []byte(fmt.Sprintf(`{"number":%d}`, issue.Number)), nil
}

// audit is the P44 invariant over every recorded call.
func (w *fakeFollowupGh) audit() {
	w.t.Helper()
	for _, c := range w.calls {
		for _, bad := range []string{"--remove-label", "--add-label", "--add-assignee", "--milestone", "--add-project"} {
			if slices.Contains(c.args, bad) {
				w.t.Errorf("P44: %s carries forbidden flag %s", c, bad)
			}
		}
		if c.is("issue", "close") || c.is("issue", "reopen") || c.is("issue", "delete") || c.is("pr") {
			w.t.Errorf("P44: forbidden command %s", c)
		}
		for i, a := range c.args {
			if a == "-X" && (i+1 >= len(c.args) || c.args[i+1] != "POST") {
				w.t.Errorf("P44: %s uses a method other than POST", c)
			}
		}
		if c.is("issue", "edit") {
			for _, a := range c.args[3:] {
				if strings.HasPrefix(a, "-") && a != "--body-file" && a != "--repo" {
					w.t.Errorf("P44: issue edit carries flag %s: %s", a, c)
				}
			}
		}
	}
	for _, p := range w.posts {
		for _, bad := range []string{"Refined", "automerge:ok"} {
			if strings.Contains(p.raw, bad) {
				w.t.Errorf("P44: POST payload grants %q: %s", bad, p.raw)
			}
		}
	}
	for _, f := range w.tempFiles {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			w.t.Errorf("temp file %s was not removed (stat err: %v)", f, err)
		}
	}
}

// -- test helpers ---------------------------------------------------------

func fuWriteFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func fuWriteItems(t *testing.T, items []FollowupItem) string {
	t.Helper()
	b, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}
	return fuWriteFile(t, "items.json", string(b))
}

func fuOpts(t *testing.T, muts ...func(*FollowupOpts)) FollowupOpts {
	t.Helper()
	o := FollowupOpts{
		ID:        "7",
		Source:    FollowupSourceImplement,
		ItemsFile: fuWriteItems(t, fuItems),
		TitleFile: fuWriteFile(t, "title.txt", fuTitle+"\n"),
		PRURL:     fuPR,
		Area:      "watch",
		RepoSlug:  fuSlug,
	}
	for _, m := range muts {
		m(&o)
	}
	return o
}

func fuRun(t *testing.T, muts ...func(*FollowupOpts)) (FollowupOutput, error) {
	t.Helper()
	return Followup(fuOpts(t, muts...))
}

func fuAddressReview(o *FollowupOpts) { o.Source = FollowupSourceAddressReview }
func fuTicketless(o *FollowupOpts)    { o.Ticketless = true; o.ID = "" }

func fuExpect(t *testing.T, out FollowupOutput, action, reason string, number int) {
	t.Helper()
	f := out.Followup
	if f.Action != action || f.Reason != reason || f.Number != number {
		t.Fatalf("followup = {action %q reason %q number %d}, want {action %q reason %q number %d}; errors=%v warnings=%v",
			f.Action, f.Reason, f.Number, action, reason, number, out.Errors, out.Warnings)
	}
}

func fuStrings(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if got == nil {
		t.Errorf("%s is nil, want a non-nil slice", what)
	}
	if len(got) != len(want) || (len(got) > 0 && !slices.Equal(got, want)) {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

func fuNoMutations(t *testing.T, w *fakeFollowupGh) {
	t.Helper()
	if m := w.mutations(); len(m) != 0 {
		t.Errorf("want zero mutating gh calls, got %v", m)
	}
}

func fuLineage(number int, body string) fuIssue {
	return fuIssue{Number: number, Title: "other: lineage target", Labels: []string{"Followup"}, Body: body}
}

func fuCandidate(number int, title string, labels ...string) fuIssue {
	return fuIssue{Number: number, Title: title, Labels: append([]string{"Followup"}, labels...), Body: "## Deferred Items\n- [ ] x — `watch`"}
}

// -- P3: label self-heal ----------------------------------------------------

func TestFollowup_LabelCreate_ColorAndDescriptionLiteral(t *testing.T) {
	w := newFuWorld(t)
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
	creates := w.callsIs("label", "create")
	if len(creates) != 1 {
		t.Fatalf("label create calls = %d, want 1: %v", len(creates), creates)
	}
	want := []string{"label", "create", "Followup", "--repo", fuSlug, "--color", "C5DEF5", "--description", fuLabelDescription}
	if !slices.Equal(creates[0].args, want) {
		t.Errorf("label create args = %q, want %q", creates[0].args, want)
	}
	createIdx, postIdx := -1, -1
	for i, c := range w.calls {
		if c.is("label", "create") {
			createIdx = i
		}
		if c.is("api") {
			postIdx = i
		}
	}
	if createIdx < 0 || postIdx < 0 || createIdx > postIdx {
		t.Errorf("label create (call %d) must precede the POST (call %d)", createIdx, postIdx)
	}
}

func TestFollowup_LabelCreate_AlreadyExistsIsSuccess(t *testing.T) {
	w := newFuWorld(t)
	w.labelCreate = "already-exists"
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("already-exists must count as success, got: %v", err)
	}
	fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
	if len(w.posts) != 1 {
		t.Errorf("POST count = %d, want 1", len(w.posts))
	}
}

func TestFollowup_LabelCreate_GenuineFailure_ZeroCreates(t *testing.T) {
	w := newFuWorld(t)
	w.labelCreate = "fail"
	out, err := fuRun(t)
	if !errors.Is(err, ErrFollowupCreateFailed) {
		t.Fatalf("err = %v, want ErrFollowupCreateFailed", err)
	}
	fuExpect(t, out, FollowupActionSkipped, FollowupReasonLabelCreateFailed, 0)
	if len(out.Errors) != 1 || !strings.Contains(out.Errors[0], "Bad credentials") {
		t.Errorf("errors = %q, want one entry naming the label failure", out.Errors)
	}
	if len(w.posts) != 0 {
		t.Errorf("POST count = %d, want 0 after a genuine label-create failure", len(w.posts))
	}
}

// -- P4/P5/P6: origin phase -------------------------------------------------

func TestFollowup_Implement_OriginIsFollowup_Skipped(t *testing.T) {
	w := newFuWorld(t)
	w.issues[7].Labels = []string{"Followup", "enhancement"}
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("origin-is-followup must exit 0 (err == nil), got: %v", err)
	}
	fuExpect(t, out, FollowupActionSkipped, FollowupReasonOriginIsFollowup, 0)
	fuNoMutations(t, w)
	if len(out.Errors) != 0 {
		t.Errorf("errors = %q, want none", out.Errors)
	}
	want := []string{"gh issue view 7 --json milestone,labels --repo o/r"}
	if got := w.callStrings(); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

func TestFollowup_Implement_OriginUnreadable_FailClosed(t *testing.T) {
	w := newFuWorld(t)
	w.inject(0, 1, fuServerError, "issue", "view", "7")
	out, err := fuRun(t)
	if !errors.Is(err, ErrOriginUnreadable) {
		t.Fatalf("err = %v, want ErrOriginUnreadable", err)
	}
	fuExpect(t, out, FollowupActionSkipped, FollowupReasonOriginUnreadable, 0)
	if !slices.Equal(out.Followup.Items, fuItems) {
		t.Errorf("items = %v, want the deferred items echoed: %v", out.Followup.Items, fuItems)
	}
	if len(out.Errors) != 1 || !strings.Contains(out.Errors[0], "internal server error") {
		t.Errorf("errors = %q, want one entry carrying the gh failure", out.Errors)
	}
	fuNoMutations(t, w)
	if n := len(w.callsIs("issue", "list")); n != 0 {
		t.Errorf("issue list calls = %d, want 0 after a fail-closed origin read", n)
	}
}

func TestFollowup_AddressReview_FollowupOrigin_AppendsToOrigin(t *testing.T) {
	w := newFuWorld(t)
	w.issues[7].Labels = []string{"Followup"}
	w.issues[7].Body = "## Deferred Items\n- [ ] Add retry docs — `watch`\n\nRelated to #3\n\nPR: https://github.com/o/r/pull/1"
	out, err := fuRun(t, fuAddressReview)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionAppended, FollowupReasonOriginFollowup, 7)
	if len(w.edits) != 1 || w.edits[0].number != 7 {
		t.Fatalf("edits = %v, want exactly one edit of #7", w.edits)
	}
	wantBody := "## Deferred Items\n- [ ] Add retry docs — `watch`\n" + fuLine2 + "\n\nRelated to #3\n\nPR: https://github.com/o/r/pull/1"
	if w.edits[0].body != wantBody {
		t.Errorf("edited body = %q, want %q", w.edits[0].body, wantBody)
	}
	if n := len(w.callsIs("issue", "list")); n != 0 {
		t.Errorf("issue list calls = %d, want 0 (the origin is the target)", n)
	}
	if n := len(w.callsIs("api")) + len(w.callsIs("label", "create")); n != 0 {
		t.Errorf("create-side calls = %d, want 0", n)
	}
}

func TestFollowup_AddressReview_OriginUnreadable_CreatesFollowupOnly(t *testing.T) {
	w := newFuWorld(t)
	w.issues[7].Labels = []string{"enhancement"}
	w.issues[7].Milestone = 14
	w.inject(0, 1, fuServerError, "issue", "view", "7")
	out, err := fuRun(t, fuAddressReview)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
	fuStrings(t, "warnings", out.Warnings, "milestone/label inheritance skipped: origin metadata unreadable")
	if len(w.posts) != 1 {
		t.Fatalf("POST count = %d, want 1", len(w.posts))
	}
	fuStrings(t, "payload labels", w.posts[0].labels, "Followup")
	if _, ok := w.posts[0].payload["milestone"]; ok {
		t.Errorf("payload has a milestone key although origin metadata was unreadable: %s", w.posts[0].raw)
	}
}

// -- P7/P8/P28: lineage -----------------------------------------------------

func TestFollowup_Lookup_SingleListCallExactArgs(t *testing.T) {
	w := newFuWorld(t)
	if _, err := fuRun(t); err != nil {
		t.Fatalf("Followup: %v", err)
	}
	lists := w.callsIs("issue", "list")
	if len(lists) != 1 {
		t.Fatalf("issue list calls = %d, want 1", len(lists))
	}
	want := []string{"issue", "list", "--label", "Followup", "--state", "open", "--limit", "200",
		"--json", "number,title,body,labels,assignees", "--repo", fuSlug}
	if !slices.Equal(lists[0].args, want) {
		t.Errorf("issue list args = %q, want %q", lists[0].args, want)
	}
}

func TestFollowup_Lineage_NearMissesDoNotMatch(t *testing.T) {
	for _, body := range []string{
		"Related to #70",
		"Related to #7 ",
		" Related to #7",
		"See Related to #7",
	} {
		t.Run(body, func(t *testing.T) {
			w := newFuWorld(t)
			w.add(fuLineage(20, "## Deferred Items\n- [ ] x — `watch`\n\n"+body+"\n\nPR: https://github.com/o/r/pull/1"))
			out, err := fuRun(t)
			if err != nil {
				t.Fatalf("Followup: %v", err)
			}
			fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
			if len(w.edits) != 0 {
				t.Errorf("edits = %v, a near miss must not be treated as lineage", w.edits)
			}
			if len(out.Followup.AlsoMatched) != 0 {
				t.Errorf("also_matched = %v, want none", out.Followup.AlsoMatched)
			}
		})
	}
}

func TestFollowup_Lineage_LowestWins_RestAlsoMatched(t *testing.T) {
	w := newFuWorld(t)
	for _, n := range []int{30, 12, 21} {
		w.add(fuLineage(n, "## Deferred Items\n- [ ] x — `watch`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"))
	}
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionAppended, FollowupReasonLineageMatch, 12)
	if !slices.Equal(out.Followup.AlsoMatched, []int{21, 30}) {
		t.Errorf("also_matched = %v, want [21 30]", out.Followup.AlsoMatched)
	}
	if len(w.edits) != 1 || w.edits[0].number != 12 {
		t.Errorf("edits = %v, want exactly one edit of #12", w.edits)
	}
}

func TestFollowup_Lineage_CRLFBodyMatches(t *testing.T) {
	w := newFuWorld(t)
	w.add(fuLineage(12, "## Deferred Items\r\n- [ ] Add retry docs — `watch`\r\n\r\nRelated to #7\r\n\r\nPR: https://github.com/o/r/pull/1"))
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionAppended, FollowupReasonLineageMatch, 12)
	want := "## Deferred Items\r\n- [ ] Add retry docs — `watch`\r\n" + fuLine2 + "\r\n\r\nRelated to #7\r\n\r\nPR: https://github.com/o/r/pull/1"
	if len(w.edits) != 1 || w.edits[0].body != want {
		t.Errorf("edits = %+v, want one edit with CRLF-preserving body %q", w.edits, want)
	}
}

func TestFollowup_Lineage_NotGatedByEligibility(t *testing.T) {
	w := newFuWorld(t)
	target := fuLineage(12, "## Deferred Items\n- [ ] x — `watch`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1")
	target.Labels = []string{"Followup", "Refined"}
	target.Assignees = []string{"alice"}
	w.add(target)
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionAppended, FollowupReasonLineageMatch, 12)
	if len(w.edits) != 1 || w.edits[0].number != 12 {
		t.Errorf("edits = %v, want exactly one edit of #12", w.edits)
	}
}

func TestFollowup_Lineage_IgnoresAppendToAndCreateWithWarning(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*FollowupOpts)
		want string
	}{
		{"append-to", func(o *FollowupOpts) { o.AppendTo = 99 }, "--append-to ignored: lineage match #12 takes precedence"},
		{"create", func(o *FollowupOpts) { o.Create = true }, "--create ignored: lineage match #12 takes precedence"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFuWorld(t)
			w.add(fuLineage(12, "## Deferred Items\n- [ ] x — `watch`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"))
			out, err := fuRun(t, tc.mut)
			if err != nil {
				t.Fatalf("Followup: %v", err)
			}
			fuExpect(t, out, FollowupActionAppended, FollowupReasonLineageMatch, 12)
			fuStrings(t, "warnings", out.Warnings, tc.want)
			if len(w.posts) != 0 {
				t.Errorf("POST count = %d, want 0", len(w.posts))
			}
		})
	}
}

func TestFollowup_AddressReview_FollowupOrigin_IgnoresCreateWithWarning(t *testing.T) {
	w := newFuWorld(t)
	w.issues[7].Labels = []string{"Followup"}
	out, err := fuRun(t, fuAddressReview, func(o *FollowupOpts) { o.Create = true })
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionAppended, FollowupReasonOriginFollowup, 7)
	fuStrings(t, "warnings", out.Warnings, "--create ignored: origin #7 is a Followup issue")
	if len(w.posts) != 0 {
		t.Errorf("POST count = %d, want 0", len(w.posts))
	}
}

func TestFollowup_AddressReview_PRLinePreferred(t *testing.T) {
	prBody := "## Deferred Items\n- [ ] x — `watch`\n\nPR: " + fuPR
	relBody := "## Deferred Items\n- [ ] x — `watch`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"
	t.Run("address-review prefers the PR line over a lower-numbered Related to", func(t *testing.T) {
		w := newFuWorld(t)
		w.add(fuLineage(40, prBody))
		w.add(fuLineage(12, relBody))
		out, err := fuRun(t, fuAddressReview)
		if err != nil {
			t.Fatalf("Followup: %v", err)
		}
		fuExpect(t, out, FollowupActionAppended, FollowupReasonLineageMatch, 40)
		if !slices.Equal(out.Followup.AlsoMatched, []int{12}) {
			t.Errorf("also_matched = %v, want [12]", out.Followup.AlsoMatched)
		}
	})
	t.Run("address-review falls back to Related to in ticket mode", func(t *testing.T) {
		w := newFuWorld(t)
		w.add(fuLineage(12, relBody))
		out, err := fuRun(t, fuAddressReview)
		if err != nil {
			t.Fatalf("Followup: %v", err)
		}
		fuExpect(t, out, FollowupActionAppended, FollowupReasonLineageMatch, 12)
	})
	t.Run("implement ignores the PR line", func(t *testing.T) {
		w := newFuWorld(t)
		w.add(fuLineage(40, prBody))
		w.add(fuLineage(12, relBody))
		out, err := fuRun(t)
		if err != nil {
			t.Fatalf("Followup: %v", err)
		}
		fuExpect(t, out, FollowupActionAppended, FollowupReasonLineageMatch, 12)
		if len(out.Followup.AlsoMatched) != 0 {
			t.Errorf("also_matched = %v, want none (PR-line issue is not a match for implement)", out.Followup.AlsoMatched)
		}
	})
}

func TestFollowup_AddressReview_PRLineNearMissesDoNotMatch(t *testing.T) {
	for _, line := range []string{
		"PR: https://github.com/o/r/pull/90",
		"PR: " + fuPR + " ",
		" PR: " + fuPR,
		"See PR: " + fuPR,
		"PR: " + fuPR + "/files",
	} {
		t.Run(line, func(t *testing.T) {
			w := newFuWorld(t)
			w.add(fuLineage(20, "## Deferred Items\n- [ ] x — `watch`\n\n"+line))
			out, err := fuRun(t, fuAddressReview, fuTicketless)
			if err != nil {
				t.Fatalf("Followup: %v", err)
			}
			fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
			if len(w.edits) != 0 {
				t.Errorf("edits = %v, a near miss must not be treated as lineage", w.edits)
			}
			if len(out.Followup.AlsoMatched) != 0 {
				t.Errorf("also_matched = %v, want none", out.Followup.AlsoMatched)
			}
		})
	}
}

func TestFollowup_AddressReview_Ticketless_NoRelatedToFallback(t *testing.T) {
	relBody := "## Deferred Items\n- [ ] x — `watch`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"
	t.Run("ticketless address-review matches the PR line", func(t *testing.T) {
		w := newFuWorld(t)
		w.add(fuLineage(40, "## Deferred Items\n- [ ] x — `watch`\n\nPR: "+fuPR))
		out, err := fuRun(t, fuAddressReview, fuTicketless)
		if err != nil {
			t.Fatalf("Followup: %v", err)
		}
		fuExpect(t, out, FollowupActionAppended, FollowupReasonLineageMatch, 40)
	})
	t.Run("ticketless address-review never falls back to Related to", func(t *testing.T) {
		w := newFuWorld(t)
		w.add(fuLineage(12, relBody))
		out, err := fuRun(t, fuAddressReview, fuTicketless)
		if err != nil {
			t.Fatalf("Followup: %v", err)
		}
		fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
		if len(w.edits) != 0 {
			t.Errorf("edits = %v, want none", w.edits)
		}
	})
	t.Run("ticketless implement does no lineage matching", func(t *testing.T) {
		w := newFuWorld(t)
		w.add(fuLineage(40, "## Deferred Items\n- [ ] x — `watch`\n\nPR: "+fuPR))
		out, err := fuRun(t, fuTicketless)
		if err != nil {
			t.Fatalf("Followup: %v", err)
		}
		fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
		if len(w.edits) != 0 {
			t.Errorf("edits = %v, want none", w.edits)
		}
	})
}

// -- P9/P30/P31: guarded append ---------------------------------------------

func TestFollowup_Append_InsertAfterLastChecklistLine(t *testing.T) {
	w := newFuWorld(t)
	w.add(fuLineage(12, "## Deferred Items\n- [ ] Add retry docs — `watch`\n- [x] Old done — `flow`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"))
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionAppended, FollowupReasonLineageMatch, 12)
	want := "## Deferred Items\n- [ ] Add retry docs — `watch`\n- [x] Old done — `flow`\n" + fuLine2 + "\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"
	if len(w.edits) != 1 || w.edits[0].body != want {
		t.Fatalf("edits = %+v, want one edit with body %q", w.edits, want)
	}
	call := w.edits[0].call
	if len(call.args) != 7 || !call.is("issue", "edit", "12", "--body-file") || call.args[4] == "" || !call.hasFlag("--repo", fuSlug) {
		t.Errorf("edit args = %q, want issue edit 12 --body-file <file> --repo %s and nothing else", call.args, fuSlug)
	}
}

func TestFollowup_Append_OnlyMissingItems(t *testing.T) {
	w := newFuWorld(t)
	w.add(fuLineage(12, "## Deferred Items\n- [ ] A — `watch`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"))
	items := []FollowupItem{{"A", "watch"}, {"B", "watch"}, {"C", "flow"}}
	out, err := fuRun(t, func(o *FollowupOpts) { o.ItemsFile = fuWriteItems(t, items) })
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionAppended, FollowupReasonLineageMatch, 12)
	want := "## Deferred Items\n- [ ] A — `watch`\n- [ ] B — `watch`\n- [ ] C — `flow`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"
	if len(w.edits) != 1 || w.edits[0].body != want {
		t.Errorf("edits = %+v, want one edit with body %q", w.edits, want)
	}
}

func TestFollowup_Append_StopsAtNextHeading(t *testing.T) {
	w := newFuWorld(t)
	w.add(fuLineage(12, "## Deferred Items\n- [ ] Add retry docs — `watch`\n\nRelated to #7\n\n## Notes\n- [ ] unrelated checklist\n\nPR: https://github.com/o/r/pull/1"))
	if _, err := fuRun(t); err != nil {
		t.Fatalf("Followup: %v", err)
	}
	want := "## Deferred Items\n- [ ] Add retry docs — `watch`\n" + fuLine2 + "\n\nRelated to #7\n\n## Notes\n- [ ] unrelated checklist\n\nPR: https://github.com/o/r/pull/1"
	if len(w.edits) != 1 || w.edits[0].body != want {
		t.Errorf("edits = %+v, want the new line inside Deferred Items only: %q", w.edits, want)
	}
}

func TestFollowup_Append_SecondRunNoop_ZeroEdits(t *testing.T) {
	w := newFuWorld(t)
	w.add(fuLineage(12, "## Deferred Items\n- [ ] Add retry docs — `watch`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"))
	first, err := fuRun(t)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	fuExpect(t, first, FollowupActionAppended, FollowupReasonLineageMatch, 12)
	if len(w.edits) != 1 {
		t.Fatalf("edits after first run = %d, want 1", len(w.edits))
	}
	second, err := fuRun(t)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	fuExpect(t, second, FollowupActionNoop, FollowupReasonLineageMatch, 12)
	if len(w.edits) != 1 {
		t.Errorf("edits after second run = %d, want still 1 (zero new gh issue edit calls)", len(w.edits))
	}
}

func TestFollowup_Append_NoDeferredHeading_AppendsSection(t *testing.T) {
	w := newFuWorld(t)
	body := "Notes from the author.\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"
	w.add(fuLineage(12, body))
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionAppended, FollowupReasonLineageMatch, 12)
	want := body + "\n\n## Deferred Items\n" + fuLine1 + "\n" + fuLine2
	if len(w.edits) != 1 || w.edits[0].body != want {
		t.Errorf("edits = %+v, want one edit with body %q", w.edits, want)
	}
	if !strings.HasPrefix(w.edits[0].body, body) {
		t.Errorf("prior content changed: %q", w.edits[0].body)
	}
}

func TestFollowup_Append_CheckedLineCountsAsPresent(t *testing.T) {
	w := newFuWorld(t)
	w.add(fuLineage(12, "## Deferred Items\n- [x] Add retry docs — `watch`\n- [X] Cover edge case — `flow`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"))
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionNoop, FollowupReasonLineageMatch, 12)
	if len(w.edits) != 0 {
		t.Errorf("edits = %v, want none", w.edits)
	}
}

func TestFollowup_Append_EditFails_NoNumber(t *testing.T) {
	w := newFuWorld(t)
	w.add(fuLineage(12, "## Deferred Items\n- [ ] x — `watch`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"))
	w.inject(0, 1, fuServerError, "issue", "edit")
	out, err := fuRun(t)
	if err == nil {
		t.Fatal("want an error when the append edit fails")
	}
	fuExpect(t, out, FollowupActionSkipped, FollowupReasonAppendFailed, 0)
	if len(out.Errors) != 1 || !strings.Contains(out.Errors[0], "internal server error") {
		t.Errorf("errors = %q, want one entry carrying the gh failure", out.Errors)
	}
}

func TestFollowup_Items_RepeatedAddedOnce(t *testing.T) {
	w := newFuWorld(t)
	items := []FollowupItem{{"A", "watch"}, {"A", "watch"}, {"B", "flow"}}
	out, err := fuRun(t, func(o *FollowupOpts) { o.ItemsFile = fuWriteItems(t, items) })
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	if want := []FollowupItem{{"A", "watch"}, {"B", "flow"}}; !slices.Equal(out.Followup.Items, want) {
		t.Errorf("items = %v, want %v", out.Followup.Items, want)
	}
	var body string
	_ = json.Unmarshal(w.posts[0].payload["body"], &body)
	if strings.Count(body, "- [ ] A — `watch`") != 1 {
		t.Errorf("body lists the repeated item more than once: %q", body)
	}
}

// -- P10/P11/P12/P32/P44: create --------------------------------------------

func TestFollowup_Create_ExcludedLabelsAbsent(t *testing.T) {
	for _, excluded := range fuExcludedLabels {
		t.Run(excluded, func(t *testing.T) {
			w := newFuWorld(t)
			w.issues[7].Labels = []string{excluded, "enhancement"}
			out, err := fuRun(t)
			if err != nil {
				t.Fatalf("Followup: %v", err)
			}
			fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
			labels := w.posts[0].labels
			if slices.Contains(labels, excluded) {
				t.Errorf("payload labels %q contain the excluded label %q", labels, excluded)
			}
			fuStrings(t, "payload labels", labels, "Followup", "enhancement")
			if c := strings.Count(strings.Join(labels, "\n"), "Followup"); c != 1 {
				t.Errorf("Followup appears %d times, want exactly once", c)
			}
		})
	}
}

func TestFollowup_Create_InheritsMilestone(t *testing.T) {
	w := newFuWorld(t)
	w.issues[7].Milestone = 14
	if _, err := fuRun(t); err != nil {
		t.Fatalf("Followup: %v", err)
	}
	if got := string(w.posts[0].payload["milestone"]); got != "14" {
		t.Errorf("payload milestone = %q, want 14: %s", got, w.posts[0].raw)
	}
}

func TestFollowup_Create_NoMilestone_KeyOmitted(t *testing.T) {
	w := newFuWorld(t)
	if _, err := fuRun(t); err != nil {
		t.Fatalf("Followup: %v", err)
	}
	if _, ok := w.posts[0].payload["milestone"]; ok {
		t.Errorf("payload has a milestone key although the origin has none: %s", w.posts[0].raw)
	}
}

func TestFollowup_Create_EmptyInheritanceWarning(t *testing.T) {
	w := newFuWorld(t)
	w.issues[7].Labels = []string{"Refined", "Working", "Planned"}
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuStrings(t, "payload labels", w.posts[0].labels, "Followup")
	fuStrings(t, "warnings", out.Warnings, "milestone/label inheritance empty")
}

func TestFollowup_NeverGrantsRefinedOrAutomerge(t *testing.T) {
	w := newFuWorld(t)
	w.issues[7].Labels = []string{"Refined", "automerge:ok", "Working", "enhancement"}
	w.issues[7].Milestone = 14
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
	for _, bad := range []string{"Refined", "automerge:ok", "Working"} {
		if strings.Contains(w.posts[0].raw, bad) {
			t.Errorf("payload carries %q: %s", bad, w.posts[0].raw)
		}
	}
	fuStrings(t, "payload labels", w.posts[0].labels, "Followup", "enhancement")
}

func TestFollowup_Ticketless_BareCreate_NoOriginRead(t *testing.T) {
	w := newFuWorld(t)
	w.issues[7].Labels = []string{"enhancement"}
	w.issues[7].Milestone = 14
	out, err := fuRun(t, fuTicketless)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
	fuStrings(t, "warnings", out.Warnings)
	fuStrings(t, "payload labels", w.posts[0].labels, "Followup")
	if _, ok := w.posts[0].payload["milestone"]; ok {
		t.Errorf("ticketless payload has a milestone key: %s", w.posts[0].raw)
	}
	if strings.Contains(w.posts[0].raw, "Related to #") {
		t.Errorf("ticketless payload contains a Related to line: %s", w.posts[0].raw)
	}
	for _, c := range w.callsIs("issue", "view") {
		if c.args[2] == "7" {
			t.Errorf("ticketless run read an origin: %s", c)
		}
	}
}

func TestFollowup_CreateBody_ExactTicketMode(t *testing.T) {
	w := newFuWorld(t)
	if _, err := fuRun(t); err != nil {
		t.Fatalf("Followup: %v", err)
	}
	var body string
	_ = json.Unmarshal(w.posts[0].payload["body"], &body)
	want := "## Deferred Items\n" + fuLine1 + "\n" + fuLine2 + "\n\nRelated to #7\n\nPR: " + fuPR
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	var title string
	_ = json.Unmarshal(w.posts[0].payload["title"], &title)
	if title != fuTitle {
		t.Errorf("payload title = %q, want %q (title file's trailing newline stripped)", title, fuTitle)
	}
}

func TestFollowup_CreateBody_ExactTicketless(t *testing.T) {
	w := newFuWorld(t)
	if _, err := fuRun(t, fuTicketless); err != nil {
		t.Fatalf("Followup: %v", err)
	}
	var body string
	_ = json.Unmarshal(w.posts[0].payload["body"], &body)
	want := "## Deferred Items\n" + fuLine1 + "\n" + fuLine2 + "\n\nPR: " + fuPR
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestFollowup_Create_CallSequence(t *testing.T) {
	w := newFuWorld(t)
	if _, err := fuRun(t); err != nil {
		t.Fatalf("Followup: %v", err)
	}
	got := w.callStrings()
	if len(got) != 5 {
		t.Fatalf("calls = %q, want 5", got)
	}
	want := []string{
		"gh issue view 7 --json milestone,labels --repo o/r",
		"gh issue list --label Followup --state open --limit 200 --json number,title,body,labels,assignees --repo o/r",
		"gh label create Followup --repo o/r --color C5DEF5 --description " + fuLabelDescription,
	}
	if !slices.Equal(got[:3], want) {
		t.Errorf("first calls = %q, want %q", got[:3], want)
	}
	post := w.calls[3]
	if len(post.args) != 6 || !post.is("api", "repos/o/r/issues", "-X", "POST", "--input") {
		t.Errorf("POST args = %q, want api repos/o/r/issues -X POST --input <file>", post.args)
	}
	if got[4] != "gh issue view 100 --json title --repo o/r" {
		t.Errorf("last call = %q, want the title re-fetch of #100", got[4])
	}
}

func TestFollowup_Create_NoRepoSlug_Endpoint(t *testing.T) {
	w := newFuWorld(t)
	if _, err := fuRun(t, func(o *FollowupOpts) { o.RepoSlug = "" }); err != nil {
		t.Fatalf("Followup: %v", err)
	}
	posts := w.callsIs("api")
	if len(posts) != 1 || posts[0].args[1] != "repos/{owner}/{repo}/issues" {
		t.Errorf("POST calls = %v, want one against repos/{owner}/{repo}/issues", posts)
	}
	for _, c := range w.calls {
		if slices.Contains(c.args, "--repo") {
			t.Errorf("%s carries --repo although no slug was given", c)
		}
	}
}

func TestFollowup_Create_MissingTitle_ZeroPosts(t *testing.T) {
	w := newFuWorld(t)
	out, err := fuRun(t, func(o *FollowupOpts) { o.TitleFile = "" })
	if !errors.Is(err, ErrInvalidFollowupInput) {
		t.Fatalf("err = %v, want ErrInvalidFollowupInput", err)
	}
	fuExpect(t, out, FollowupActionSkipped, FollowupReasonMissingTitle, 0)
	fuNoMutations(t, w)
}

// -- P13/P14: title check and retry -----------------------------------------

func TestFollowup_Create_TitleMismatch(t *testing.T) {
	w := newFuWorld(t)
	w.postStoredTitle = "something else entirely"
	out, err := fuRun(t)
	if !errors.Is(err, ErrFollowupTitleMismatch) {
		t.Fatalf("err = %v, want ErrFollowupTitleMismatch", err)
	}
	fuExpect(t, out, FollowupActionSkipped, FollowupReasonTitleMismatch, 0)
	if len(out.Errors) != 1 || !strings.Contains(out.Errors[0], "#100") {
		t.Errorf("errors = %q, want one entry naming #100", out.Errors)
	}
	if !strings.Contains(err.Error(), "#100") {
		t.Errorf("err = %q, want it to name #100", err)
	}
}

func TestFollowup_Create_PostFails_RecoveredNoSecondPost(t *testing.T) {
	w := newFuWorld(t)
	w.postCreatesThenErrors = 1
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionCreated, FollowupReasonRecovered, 100)
	fuStrings(t, "warnings", out.Warnings, "recovered: create had already succeeded")
	if len(w.posts) != 1 {
		t.Errorf("POST count = %d, want 1 (no second POST)", len(w.posts))
	}
	if n := len(w.callsIs("issue", "list")); n != 2 {
		t.Errorf("issue list calls = %d, want 2 (lookup + recovery lookup)", n)
	}
	if len(w.edits) != 0 {
		t.Errorf("edits = %v, want none when every item is already present", w.edits)
	}
}

func TestFollowup_Create_PostFails_Ticketless_RecoveredByPRLine(t *testing.T) {
	w := newFuWorld(t)
	w.postCreatesThenErrors = 1
	// A Related-to-only issue must not satisfy the ticketless (PR line) lookup.
	w.add(fuLineage(50, "## Deferred Items\n- [ ] x — `watch`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"))
	out, err := fuRun(t, fuTicketless)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionCreated, FollowupReasonRecovered, 100)
	fuStrings(t, "warnings", out.Warnings, "recovered: create had already succeeded")
	if len(w.posts) != 1 {
		t.Errorf("POST count = %d, want 1 (no second POST)", len(w.posts))
	}
	post := slices.IndexFunc(w.calls, func(c ghCall) bool { return c.is("api") })
	lists := 0
	for i, c := range w.calls {
		if c.is("issue", "list") {
			lists++
			if lists == 2 && i < post {
				t.Errorf("recovery issue list ran before the failed POST: %q", w.callStrings())
			}
		}
	}
	if lists != 2 {
		t.Errorf("issue list calls = %d, want 2 (lookup + recovery lookup): %q", lists, w.callStrings())
	}
}

func TestFollowup_Create_PostFails_RecoveredMissingItemsAppended(t *testing.T) {
	w := newFuWorld(t)
	w.postCreatesThenErrors = 1
	w.postStoredBody = "## Deferred Items\n" + fuLine1 + "\n\nRelated to #7\n\nPR: " + fuPR
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionCreated, FollowupReasonRecovered, 100)
	want := "## Deferred Items\n" + fuLine1 + "\n" + fuLine2 + "\n\nRelated to #7\n\nPR: " + fuPR
	if len(w.edits) != 1 || w.edits[0].number != 100 || w.edits[0].body != want {
		t.Errorf("edits = %+v, want one edit of #100 adding the missing item: %q", w.edits, want)
	}
	if len(w.posts) != 1 {
		t.Errorf("POST count = %d, want 1", len(w.posts))
	}
}

func TestFollowup_Create_PostFails_NoMatch_SingleRetry(t *testing.T) {
	w := newFuWorld(t)
	w.inject(0, 1, fuServerError, "api")
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
	fuStrings(t, "warnings", out.Warnings)
	if n := len(w.callsIs("api")); n != 2 {
		t.Errorf("POST calls = %d, want 2 (failure + one retry)", n)
	}
	if n := len(w.callsIs("issue", "list")); n != 2 {
		t.Errorf("issue list calls = %d, want 2 (the retry is gated by a fresh lookup)", n)
	}
}

func TestFollowup_Create_PostFails_NoMatch_RetryFails(t *testing.T) {
	w := newFuWorld(t)
	w.inject(0, 5, fuServerError, "api")
	out, err := fuRun(t)
	if !errors.Is(err, ErrFollowupCreateFailed) {
		t.Fatalf("err = %v, want ErrFollowupCreateFailed", err)
	}
	fuExpect(t, out, FollowupActionSkipped, FollowupReasonCreateFailed, 0)
	if n := len(w.callsIs("api")); n != 2 {
		t.Errorf("POST calls = %d, want exactly 2 (never a third)", n)
	}
}

func TestFollowup_Create_PostFails_LookupFails_NoRetry(t *testing.T) {
	w := newFuWorld(t)
	w.inject(0, 1, fuServerError, "api")
	w.inject(1, 1, "HTTP 500: lookup broke", "issue", "list")
	out, err := fuRun(t)
	if !errors.Is(err, ErrFollowupCreateFailed) {
		t.Fatalf("err = %v, want ErrFollowupCreateFailed", err)
	}
	fuExpect(t, out, FollowupActionSkipped, FollowupReasonCreateFailed, 0)
	if n := len(w.callsIs("api")); n != 1 {
		t.Errorf("POST calls = %d, want 1 (no retry when the recovery lookup fails)", n)
	}
	if len(out.Errors) != 1 || !strings.Contains(out.Errors[0], "lookup broke") {
		t.Errorf("errors = %q, want one entry carrying the lookup failure", out.Errors)
	}
}

// -- P15-P18, P34-P36: cross-ticket phase -----------------------------------

func TestFollowup_CrossTicket_Eligible_NeedsJudgment_ZeroMutations(t *testing.T) {
	w := newFuWorld(t)
	w.add(fuCandidate(21, "watch: refactor the daemon"))
	w.add(fuCandidate(20, "watch: older candidate"))
	assigned := fuCandidate(22, "watch: taken")
	assigned.Assignees = []string{"alice"}
	w.add(assigned)
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("needs-judgment must exit 0 (err == nil), got: %v", err)
	}
	fuExpect(t, out, FollowupActionNeedsJudgment, FollowupReasonEligibleCandidates, 0)
	want := []FollowupCandidate{{20, "watch: older candidate"}, {21, "watch: refactor the daemon"}}
	if !slices.Equal(out.Followup.Candidates, want) {
		t.Errorf("candidates = %v, want %v", out.Followup.Candidates, want)
	}
	fuNoMutations(t, w)
}

func TestFollowup_CrossTicket_EachGateFailsAlone(t *testing.T) {
	control := fuCandidate(20, "watch: refactor")
	cases := []struct {
		name string
		mod  func(*fuIssue)
	}{
		{"assignee", func(i *fuIssue) { i.Assignees = []string{"alice"} }},
		{"title token mismatch", func(i *fuIssue) { i.Title = "flow: refactor" }},
	}
	for _, l := range fuUnstartedGateLabels {
		cases = append(cases, struct {
			name string
			mod  func(*fuIssue)
		}{"label " + l, func(i *fuIssue) { i.Labels = append(i.Labels, l) }})
	}
	t.Run("control passes every gate", func(t *testing.T) {
		w := newFuWorld(t)
		w.add(control)
		out, err := fuRun(t)
		if err != nil {
			t.Fatalf("Followup: %v", err)
		}
		fuExpect(t, out, FollowupActionNeedsJudgment, FollowupReasonEligibleCandidates, 0)
	})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFuWorld(t)
			c := control
			c.Labels = slices.Clone(control.Labels)
			tc.mod(&c)
			w.add(c)
			out, err := fuRun(t)
			if err != nil {
				t.Fatalf("Followup: %v", err)
			}
			fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
			if len(out.Followup.Candidates) != 0 {
				t.Errorf("candidates = %v, want none", out.Followup.Candidates)
			}
			if len(w.posts) != 1 || len(w.edits) != 0 {
				t.Errorf("posts=%d edits=%d, want a direct create and no edit", len(w.posts), len(w.edits))
			}
		})
	}
}

func TestFollowup_CrossTicket_AreaMatch(t *testing.T) {
	cases := []struct {
		name     string
		title    string
		labels   []string
		eligible bool
	}{
		{"case-insensitive token before colon", "Watch: tidy", nil, true},
		{"token before paren", "watch(cmd): tidy", nil, true},
		{"label equal to area", "tidy the docs", []string{"watch"}, true},
		{"label differing only by case", "tidy the docs", []string{"Watch"}, false},
		{"token is a prefix of the area", "wat: tidy", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFuWorld(t)
			w.add(fuCandidate(20, tc.title, tc.labels...))
			out, err := fuRun(t)
			if err != nil {
				t.Fatalf("Followup: %v", err)
			}
			if tc.eligible {
				fuExpect(t, out, FollowupActionNeedsJudgment, FollowupReasonEligibleCandidates, 0)
			} else {
				fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
			}
		})
	}
}

func TestFollowup_Candidate_NoTokenNoLabel_Ineligible(t *testing.T) {
	w := newFuWorld(t)
	w.add(fuCandidate(20, "tidy the docs"))
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
	if len(out.Followup.Candidates) != 0 {
		t.Errorf("candidates = %v, want none", out.Followup.Candidates)
	}
}

func TestFollowup_CrossTicket_NoExtraGhCallForEligibility(t *testing.T) {
	w := newFuWorld(t)
	w.add(fuCandidate(20, "watch: refactor"))
	out, err := fuRun(t)
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionNeedsJudgment, FollowupReasonEligibleCandidates, 0)
	want := []string{
		"gh issue view 7 --json milestone,labels --repo o/r",
		"gh issue list --label Followup --state open --limit 200 --json number,title,body,labels,assignees --repo o/r",
	}
	if got := w.callStrings(); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want exactly %q", got, want)
	}
}

func TestFollowup_Candidates_ExcludeOriginAndLineage(t *testing.T) {
	t.Run("origin is never a candidate", func(t *testing.T) {
		w := newFuWorld(t)
		w.issues[7].Labels = []string{"Followup"}
		w.issues[7].Title = "watch: origin followup"
		w.inject(0, 1, fuServerError, "issue", "view", "7")
		out, err := fuRun(t, fuAddressReview)
		if err != nil {
			t.Fatalf("Followup: %v", err)
		}
		fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
		if len(out.Followup.Candidates) != 0 {
			t.Errorf("candidates = %v, want the origin excluded", out.Followup.Candidates)
		}
	})
	t.Run("control: the same issue is a candidate for another origin", func(t *testing.T) {
		w := newFuWorld(t)
		w.add(fuIssue{Number: 8, Title: "watch: other origin", Labels: []string{"enhancement"}})
		w.add(fuCandidate(7, "watch: origin followup"))
		out, err := fuRun(t, func(o *FollowupOpts) { o.ID = "8" })
		if err != nil {
			t.Fatalf("Followup: %v", err)
		}
		fuExpect(t, out, FollowupActionNeedsJudgment, FollowupReasonEligibleCandidates, 0)
		if !slices.Equal(out.Followup.Candidates, []FollowupCandidate{{7, "watch: origin followup"}}) {
			t.Errorf("candidates = %v, want [#7]", out.Followup.Candidates)
		}
	})
	t.Run("a lineage match is appended to and no candidates are evaluated", func(t *testing.T) {
		w := newFuWorld(t)
		lineage := fuCandidate(12, "watch: lineage target")
		lineage.Body = "## Deferred Items\n- [ ] x — `watch`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"
		w.add(lineage)
		out, err := fuRun(t)
		if err != nil {
			t.Fatalf("Followup: %v", err)
		}
		fuExpect(t, out, FollowupActionAppended, FollowupReasonLineageMatch, 12)
		if len(out.Followup.Candidates) != 0 {
			t.Errorf("candidates = %v, want none", out.Followup.Candidates)
		}
	})
}

func TestFollowup_NoArea_SkipsCandidatesWithWarning(t *testing.T) {
	w := newFuWorld(t)
	w.add(fuCandidate(20, "watch: refactor"))
	out, err := fuRun(t, func(o *FollowupOpts) { o.Area = "" })
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionCreated, FollowupReasonNoLineageMatch, 100)
	fuStrings(t, "warnings", out.Warnings, "cross-ticket candidates skipped: no --area")
	if len(out.Followup.Candidates) != 0 {
		t.Errorf("candidates = %v, want none", out.Followup.Candidates)
	}
}

func TestFollowup_AppendTo_Eligible_Appends(t *testing.T) {
	w := newFuWorld(t)
	w.add(fuCandidate(20, "watch: refactor"))
	out, err := fuRun(t, func(o *FollowupOpts) { o.AppendTo = 20 })
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionAppended, FollowupReasonAppendTo, 20)
	want := "## Deferred Items\n- [ ] x — `watch`\n" + fuLine1 + "\n" + fuLine2
	if len(w.edits) != 1 || w.edits[0].number != 20 || w.edits[0].body != want {
		t.Errorf("edits = %+v, want one edit of #20 with body %q", w.edits, want)
	}
	if n := len(w.callsIs("issue", "view", "20")); n != 1 {
		t.Errorf("reads of #20 = %d, want 1 (the fresh gate read is reused for the append)", n)
	}
}

func TestFollowup_AppendTo_FailsGateOnFreshRead_ZeroEdits(t *testing.T) {
	w := newFuWorld(t)
	w.add(fuCandidate(20, "watch: refactor"))
	w.onList = func() { w.issues[20].Assignees = []string{"alice"} }
	out, err := fuRun(t, func(o *FollowupOpts) { o.AppendTo = 20 })
	if !errors.Is(err, ErrAppendTargetIneligible) {
		t.Fatalf("err = %v, want ErrAppendTargetIneligible", err)
	}
	fuExpect(t, out, FollowupActionSkipped, FollowupReasonAppendTargetIneligible, 0)
	if len(out.Errors) != 1 || !strings.Contains(out.Errors[0], "#20") {
		t.Errorf("errors = %q, want one entry naming #20", out.Errors)
	}
	fuNoMutations(t, w)
}

func TestFollowup_AppendTo_NotInEligibleSet_ZeroEdits(t *testing.T) {
	cases := []struct {
		name string
		n    int
	}{
		{"absent from the list", 99},
		{"area mismatch", 21},
		{"the origin itself", 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFuWorld(t)
			w.add(fuIssue{Number: 99, Title: "watch: not a followup", Labels: []string{"enhancement"}})
			w.add(fuCandidate(21, "flow: other area"))
			out, err := fuRun(t, func(o *FollowupOpts) { o.AppendTo = tc.n })
			if !errors.Is(err, ErrAppendTargetIneligible) {
				t.Fatalf("err = %v, want ErrAppendTargetIneligible", err)
			}
			fuExpect(t, out, FollowupActionSkipped, FollowupReasonAppendTargetIneligible, 0)
			fuNoMutations(t, w)
		})
	}
}

func TestFollowup_Create_PossiblyRelatedLine(t *testing.T) {
	w := newFuWorld(t)
	out, err := fuRun(t, func(o *FollowupOpts) { o.Create = true; o.PossiblyRelated = "12,15" })
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionCreated, FollowupReasonCreateFlag, 100)
	var body string
	_ = json.Unmarshal(w.posts[0].payload["body"], &body)
	want := "## Deferred Items\n" + fuLine1 + "\n" + fuLine2 + "\n\nPossibly related: #12, #15\nRelated to #7\n\nPR: " + fuPR
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestFollowup_CreateFlag_NeverAppendsToCandidate(t *testing.T) {
	w := newFuWorld(t)
	w.add(fuCandidate(20, "watch: refactor"))
	out, err := fuRun(t, func(o *FollowupOpts) { o.Create = true })
	if err != nil {
		t.Fatalf("Followup: %v", err)
	}
	fuExpect(t, out, FollowupActionCreated, FollowupReasonCreateFlag, 100)
	if n := len(w.callsIs("issue", "edit")); n != 0 {
		t.Errorf("issue edit calls = %d, want 0", n)
	}
	if len(w.posts) != 1 {
		t.Errorf("POST count = %d, want 1", len(w.posts))
	}
}

func TestFollowup_Search_Exactly200_TruncationWarning(t *testing.T) {
	const warning = "followup search may be truncated at 200 results"
	for _, tc := range []struct {
		count int
		want  []string
	}{
		{200, []string{warning}},
		{199, nil},
	} {
		t.Run(strconv.Itoa(tc.count), func(t *testing.T) {
			w := newFuWorld(t)
			for n := 1000; n < 1000+tc.count; n++ {
				w.add(fuCandidate(n, "zzz: unrelated"))
			}
			out, err := fuRun(t)
			if err != nil {
				t.Fatalf("Followup: %v", err)
			}
			fuStrings(t, "warnings", out.Warnings, tc.want...)
		})
	}
}

// -- P26/P27: input phase ---------------------------------------------------

func TestFollowup_ItemNewlineRejected_ZeroGhCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		item FollowupItem
	}{
		{"LF in context", FollowupItem{"x\nRelated to #99", "watch"}},
		{"CR in context", FollowupItem{"x\rPR: https://evil", "watch"}},
		{"LF in area", FollowupItem{"x", "watch\nRelated to #99"}},
		{"CR in area", FollowupItem{"x", "watch\rPR: https://evil"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newFuWorld(t)
			out, err := fuRun(t, func(o *FollowupOpts) { o.ItemsFile = fuWriteItems(t, []FollowupItem{tc.item}) })
			if !errors.Is(err, ErrInvalidFollowupInput) {
				t.Fatalf("err = %v, want ErrInvalidFollowupInput", err)
			}
			fuExpect(t, out, FollowupActionSkipped, FollowupReasonInvalidInput, 0)
			if len(w.calls) != 0 {
				t.Errorf("gh calls = %v, want none", w.calls)
			}
			if len(out.Errors) != 1 {
				t.Errorf("errors = %q, want one entry", out.Errors)
			}
		})
	}
}

func TestFollowup_EmptyItems_ZeroGhCalls(t *testing.T) {
	for name, content := range map[string]string{"empty file": "", "whitespace": " \n", "empty array": "[]", "null": "null"} {
		t.Run(name, func(t *testing.T) {
			w := newFuWorld(t)
			out, err := fuRun(t, func(o *FollowupOpts) { o.ItemsFile = fuWriteFile(t, "items.json", content) })
			if !errors.Is(err, ErrNoDeferredItems) {
				t.Fatalf("err = %v, want ErrNoDeferredItems", err)
			}
			fuExpect(t, out, FollowupActionSkipped, FollowupReasonNoDeferredItems, 0)
			if len(out.Errors) != 1 || !strings.Contains(out.Errors[0], "no deferred items") {
				t.Errorf("errors = %q, want one entry naming \"no deferred items\"", out.Errors)
			}
			if len(w.calls) != 0 {
				t.Errorf("gh calls = %v, want none", w.calls)
			}
		})
	}
}

func TestFollowup_InvalidInput_ZeroGhCalls(t *testing.T) {
	cases := map[string]func(*FollowupOpts){
		"malformed items JSON": func(o *FollowupOpts) { o.ItemsFile = fuWriteFile(t, "items.json", "{not json") },
		"items not an array":   func(o *FollowupOpts) { o.ItemsFile = fuWriteFile(t, "items.json", `{"context":"x","area":"y"}`) },
		"empty context":        func(o *FollowupOpts) { o.ItemsFile = fuWriteItems(t, []FollowupItem{{"", "watch"}}) },
		"empty area field":     func(o *FollowupOpts) { o.ItemsFile = fuWriteItems(t, []FollowupItem{{"x", " "}}) },
		"missing items file":   func(o *FollowupOpts) { o.ItemsFile = filepath.Join(t.TempDir(), "absent.json") },
		"missing title file":   func(o *FollowupOpts) { o.TitleFile = filepath.Join(t.TempDir(), "absent.txt") },
		"newline in title":     func(o *FollowupOpts) { o.TitleFile = fuWriteFile(t, "title.txt", "a\nb") },
		"empty title":          func(o *FollowupOpts) { o.TitleFile = fuWriteFile(t, "title.txt", "\n") },
		"unknown source":       func(o *FollowupOpts) { o.Source = "other" },
		"ticketless with id":   func(o *FollowupOpts) { o.Ticketless = true },
		"missing id":           func(o *FollowupOpts) { o.ID = "" },
		"non-numeric id":       func(o *FollowupOpts) { o.ID = "7a" },
		"bad pr url":           func(o *FollowupOpts) { o.PRURL = "http://example.com/x" },
		"bad repo slug":        func(o *FollowupOpts) { o.RepoSlug = "o/r/x" },
		"bad possibly related": func(o *FollowupOpts) { o.Create = true; o.PossiblyRelated = "1,,2" },
		"possibly w/o create":  func(o *FollowupOpts) { o.PossiblyRelated = "1" },
		"append-to and create": func(o *FollowupOpts) { o.AppendTo = 3; o.Create = true },
		"append-to w/o area":   func(o *FollowupOpts) { o.AppendTo = 3; o.Area = "" },
		"negative append-to":   func(o *FollowupOpts) { o.AppendTo = -1 },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			w := newFuWorld(t)
			out, err := fuRun(t, mut)
			if !errors.Is(err, ErrInvalidFollowupInput) {
				t.Fatalf("err = %v, want ErrInvalidFollowupInput", err)
			}
			fuExpect(t, out, FollowupActionSkipped, FollowupReasonInvalidInput, 0)
			if len(w.calls) != 0 {
				t.Errorf("gh calls = %v, want none", w.calls)
			}
		})
	}
}

// -- P2: output invariants --------------------------------------------------

func TestFollowup_NumberOnlyOnSuccessfulMutation(t *testing.T) {
	lineageBody := "## Deferred Items\n- [ ] x — `watch`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"
	cases := []struct {
		name    string
		setup   func(w *fakeFollowupGh)
		wantErr bool
	}{
		{"skipped", func(w *fakeFollowupGh) { w.issues[7].Labels = []string{"Followup"} }, false},
		{"needs-judgment", func(w *fakeFollowupGh) { w.add(fuCandidate(20, "watch: refactor")) }, false},
		{"edit fails", func(w *fakeFollowupGh) {
			w.add(fuLineage(12, lineageBody))
			w.inject(0, 1, fuServerError, "issue", "edit")
		}, true},
		{"POST fails", func(w *fakeFollowupGh) { w.inject(0, 5, fuServerError, "api") }, true},
		{"title mismatch", func(w *fakeFollowupGh) { w.postStoredTitle = "other" }, true},
		{"label create fails", func(w *fakeFollowupGh) { w.labelCreate = "fail" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFuWorld(t)
			tc.setup(w)
			out, err := fuRun(t)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && len(out.Errors) == 0 {
				t.Error("errors is empty on a failed run")
			}
			if !tc.wantErr && len(out.Errors) != 0 {
				t.Errorf("errors = %q on a successful run", out.Errors)
			}
			if out.Followup.Number != 0 {
				t.Errorf("number = %d, want 0 for %q", out.Followup.Number, out.Followup.Action)
			}
			raw, merr := json.Marshal(out)
			if merr != nil {
				t.Fatalf("marshal: %v", merr)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(raw, &top); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			var f map[string]json.RawMessage
			if err := json.Unmarshal(top["followup"], &f); err != nil {
				t.Fatalf("unmarshal followup: %v", err)
			}
			if _, ok := f["number"]; ok {
				t.Errorf("followup has a number key: %s", raw)
			}
			for _, key := range []string{"items", "candidates", "also_matched"} {
				if s := string(f[key]); !strings.HasPrefix(s, "[") {
					t.Errorf("followup.%s = %s, want an array", key, s)
				}
			}
			for _, key := range []string{"warnings", "errors"} {
				if s := string(top[key]); !strings.HasPrefix(s, "[") {
					t.Errorf("%s = %s, want an array", key, s)
				}
			}
			if strings.Contains(string(raw), "null") {
				t.Errorf("output contains null: %s", raw)
			}
		})
	}
}

// A GitHub read that fails must exit non-zero with a named error, the
// closed-set reason and no number; a read that precedes any mutation leaves
// zero mutating calls, and the post-create re-fetch fails after the POST.
func TestFollowup_GitHubReadFailure_ErrorsNoNumber(t *testing.T) {
	lineageBody := "## Deferred Items\n- [ ] x — `watch`\n\nRelated to #7\n\nPR: https://github.com/o/r/pull/1"
	cases := []struct {
		name      string
		setup     func(w *fakeFollowupGh)
		mut       func(*FollowupOpts)
		reason    string
		sentinel  error
		errSub    string
		lastCall  []string
		mutations int
	}{
		{"lookup list", func(w *fakeFollowupGh) { w.inject(0, 1, fuServerError, "issue", "list") }, func(*FollowupOpts) {},
			FollowupReasonLookupFailed, nil, "search open Followup issues", []string{"issue", "list"}, 0},
		{"guarded-append target re-read", func(w *fakeFollowupGh) {
			w.add(fuLineage(12, lineageBody))
			w.inject(0, 1, fuServerError, "issue", "view", "12")
		}, func(*FollowupOpts) {},
			FollowupReasonAppendFailed, nil, "read #12 for append", []string{"issue", "view", "12"}, 0},
		{"append-to fresh re-read", func(w *fakeFollowupGh) {
			w.add(fuCandidate(20, "watch: refactor"))
			w.inject(0, 1, fuServerError, "issue", "view", "20")
		}, func(o *FollowupOpts) { o.AppendTo = 20 },
			FollowupReasonAppendFailed, nil, "re-read #20 before append", []string{"issue", "view", "20"}, 0},
		{"post-create title re-fetch", func(w *fakeFollowupGh) { w.inject(0, 1, fuServerError, "issue", "view", "100") }, func(*FollowupOpts) {},
			FollowupReasonCreateFailed, ErrFollowupCreateFailed, "created #100 but could not verify its title", []string{"issue", "view", "100"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFuWorld(t)
			tc.setup(w)
			out, err := fuRun(t, tc.mut)
			if err == nil {
				t.Fatalf("want an error, got output %+v", out)
			}
			if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Errorf("err = %v, want it to wrap %v", err, tc.sentinel)
			}
			fuExpect(t, out, FollowupActionSkipped, tc.reason, 0)
			if len(out.Errors) != 1 || !strings.Contains(out.Errors[0], tc.errSub) || !strings.Contains(out.Errors[0], "internal server error") {
				t.Errorf("errors = %q, want one entry containing %q and the gh failure", out.Errors, tc.errSub)
			}
			f := out.Followup
			if out.Warnings == nil || f.Items == nil || f.Candidates == nil || f.AlsoMatched == nil {
				t.Errorf("a slice is nil: %+v", out)
			}
			if got := w.calls[len(w.calls)-1]; !got.is(tc.lastCall...) {
				t.Errorf("last call = %s, want the failing call %q (the injected failure was not the last step)", got, tc.lastCall)
			}
			if got := len(w.mutations()); got != tc.mutations {
				t.Errorf("mutating calls = %d (%v), want %d", got, w.mutations(), tc.mutations)
			}
		})
	}
}

func TestFollowup_InputFailureOutput_ArraysNeverNull(t *testing.T) {
	newFuWorld(t)
	out, err := fuRun(t, func(o *FollowupOpts) { o.ItemsFile = fuWriteFile(t, "items.json", "[]") })
	if err == nil {
		t.Fatal("want an error")
	}
	raw, merr := json.Marshal(out)
	if merr != nil {
		t.Fatalf("marshal: %v", merr)
	}
	if strings.Contains(string(raw), "null") {
		t.Errorf("output contains null: %s", raw)
	}
}
