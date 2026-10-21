package pipeline

// The `cenci pipeline followup` engine (#1339): the deterministic bookkeeping
// behind implement Phase 9 and address-review for deferred items. It resolves
// one run end to end through the package `command` seam -- the origin read,
// the generation limit, exact lineage / `PR:` lookup, the guarded idempotent
// append, the cross-ticket eligibility gates and the two-step needs-judgment
// interface, and the create with inheritance, title verification and
// lookup-gated retry. It is stateless (no pipeline state file is read or
// written) and never removes a label, grants Refined or automerge:ok, closes
// or merges an issue. Fetched issue bodies and titles are untrusted data:
// they are only matched against whole lines and only checklist lines built
// from validated items are ever inserted.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Source values accepted by FollowupOpts.Source. Each caller keeps its own
// semantics: implement fails closed on an unreadable origin and skips a
// Followup origin; address-review appends to a Followup origin and degrades
// to a bare Followup create when the origin is unreadable.
const (
	FollowupSourceImplement     = "implement"
	FollowupSourceAddressReview = "address-review"
)

// FollowupResult.Action values: a closed set. A failed run reports
// FollowupActionSkipped with a failure reason and a non-empty Errors.
const (
	FollowupActionCreated       = "created"
	FollowupActionAppended      = "appended"
	FollowupActionNoop          = "noop"
	FollowupActionSkipped       = "skipped"
	FollowupActionNeedsJudgment = "needs-judgment"
)

// FollowupResult.Reason values: a closed set. created/appended/noop carry the
// route that was taken; needs-judgment carries eligible-candidates; skipped
// carries why nothing was written.
const (
	FollowupReasonLineageMatch           = "lineage-match"
	FollowupReasonOriginFollowup         = "origin-followup"
	FollowupReasonAppendTo               = "append-to"
	FollowupReasonNoLineageMatch         = "no-lineage-match"
	FollowupReasonCreateFlag             = "create-flag"
	FollowupReasonRecovered              = "recovered"
	FollowupReasonEligibleCandidates     = "eligible-candidates"
	FollowupReasonOriginIsFollowup       = "origin-is-followup"
	FollowupReasonOriginUnreadable       = "origin-unreadable"
	FollowupReasonTitleMismatch          = "title-mismatch"
	FollowupReasonMissingTitle           = "missing-title"
	FollowupReasonInvalidInput           = "invalid-input"
	FollowupReasonNoDeferredItems        = "no-deferred-items"
	FollowupReasonLookupFailed           = "lookup-failed"
	FollowupReasonAppendTargetIneligible = "append-target-ineligible"
	FollowupReasonAppendFailed           = "append-failed"
	FollowupReasonLabelCreateFailed      = "label-create-failed"
	FollowupReasonCreateFailed           = "create-failed"
)

// Sentinel errors, detectable via errors.Is at the package boundary (rule
// #412), each a distinct failure class (rule #446).
var (
	// ErrNoDeferredItems is returned for an empty items file or `[]`.
	ErrNoDeferredItems = errors.New("no deferred items")

	// ErrInvalidFollowupInput is returned for malformed options, an
	// unreadable or malformed items/title file, an item or title containing a
	// line break, or a create without a title. No gh call precedes it.
	ErrInvalidFollowupInput = errors.New("invalid followup input")

	// ErrOriginUnreadable is returned when --source implement cannot read the
	// origin issue (fail closed).
	ErrOriginUnreadable = errors.New("origin issue unreadable")

	// ErrAppendTargetIneligible is returned when --append-to names an issue
	// that is absent from the eligible set or fails a gate on the fresh read.
	ErrAppendTargetIneligible = errors.New("append target is not an eligible followup")

	// ErrFollowupTitleMismatch is returned when the created issue's title
	// differs from the title file.
	ErrFollowupTitleMismatch = errors.New("created followup title does not match the title file")

	// ErrFollowupCreateFailed is returned when the label self-heal, the create
	// POST (after its single lookup-gated retry) or the post-create title
	// re-fetch fails.
	ErrFollowupCreateFailed = errors.New("followup create failed")
)

const (
	followupLabel            = "Followup"
	followupLabelColor       = "C5DEF5"
	followupLabelDescription = "Deferred/out-of-scope item captured from a session — triage before working"
	followupSearchLimit      = 200
	followupDeferredHeading  = "## Deferred Items"
)

// followupExcludedLabels are never inherited from the origin: lifecycle,
// refine-applied, grant and reconciler-transient labels (14).
var followupExcludedLabels = []string{
	"Refined", "Working", "Planned", "In Review", "Implemented", "Input Needed",
	"Design", "Designed", "automerge:ok", "Browser", "ui:visual-check",
	"dispatch-failed", "plan-invalid", "reconcile-stuck",
}

// followupUnstartedLabels disqualify a cross-ticket candidate: any of them
// means the issue is no longer an unstarted capture (8).
var followupUnstartedLabels = []string{
	"Refined", "Planned", "Working", "In Review", "Implemented", "Input Needed", "Design", "Designed",
}

var (
	followupPRURLRe    = regexp.MustCompile(`^https://\S+/pull/[0-9]+$`)
	followupRepoSlugRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	followupRelatedRe  = regexp.MustCompile(`^[0-9]+(,[0-9]+)*$`)
	followupHeadingRe  = regexp.MustCompile(`^#{1,6} `)
)

// FollowupOpts are the inputs to one Followup run. The CLI layer validates
// shape and maps violations to exit 2; the engine re-validates and reports
// ErrInvalidFollowupInput so a library caller cannot bypass the checks.
type FollowupOpts struct {
	// ID is the origin ticket number; empty exactly when Ticketless.
	ID string
	// Ticketless runs with no origin: no origin read, no Related to line,
	// labels exactly ["Followup"], no milestone.
	Ticketless bool
	// Source is FollowupSourceImplement or FollowupSourceAddressReview.
	Source string
	// ItemsFile holds a JSON array of {"context": "...", "area": "..."}.
	ItemsFile string
	// PRURL is the pull request URL (`https://.../pull/<n>`).
	PRURL string
	// TitleFile holds the title of a created issue; required to create.
	TitleFile string
	// Area is the project token used by the cross-ticket gates; empty skips
	// cross-ticket candidates.
	Area string
	// AppendTo, when > 0, appends to that eligible open Followup.
	AppendTo int
	// Create forces a create instead of the needs-judgment stop.
	Create bool
	// PossiblyRelated is a comma list of issue numbers ("12,15"), meaningful
	// only with Create.
	PossiblyRelated string
	// RepoSlug is the "<owner>/<repo>" gh --repo target; empty targets the
	// current repository.
	RepoSlug string
}

// FollowupItem is one deferred item.
type FollowupItem struct {
	Context string `json:"context"`
	Area    string `json:"area"`
}

// FollowupCandidate is one eligible cross-ticket append candidate.
type FollowupCandidate struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
}

// FollowupResult is the `followup` object of the JSON result. Number is set
// only for created, appended and noop; every slice is non-nil.
type FollowupResult struct {
	Action      string              `json:"action"`
	Number      int                 `json:"number,omitempty"`
	Reason      string              `json:"reason"`
	Items       []FollowupItem      `json:"items"`
	Candidates  []FollowupCandidate `json:"candidates"`
	AlsoMatched []int               `json:"also_matched"`
}

// FollowupOutput is the JSON result of one run: it is returned even when the
// run fails. The CLI exits 1 exactly when Followup returned a non-nil error
// (equivalently, Failed reports true); needs-judgment and
// skipped/origin-is-followup are successes (exit 0).
type FollowupOutput struct {
	Followup FollowupResult `json:"followup"`
	Warnings []string       `json:"warnings"`
	Errors   []string       `json:"errors"`
}

// Failed reports whether the run failed (non-empty Errors).
func (o FollowupOutput) Failed() bool { return len(o.Errors) > 0 }

// followupIssue is the subset of an issue the engine reads.
type followupIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
}

func (i followupIssue) labelNames() []string {
	names := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		names = append(names, l.Name)
	}
	return names
}

// Followup runs one deferred-item bookkeeping operation. The returned output
// is always populated; a non-nil error means the run failed and the output
// carries the matching Errors entry, the closed-set skipped reason and no
// Number.
func Followup(o FollowupOpts) (FollowupOutput, error) {
	r := &followupRun{o: o, cfg: defaultRetryConfig()}
	err := r.run()
	return r.finish(), err
}

type followupRun struct {
	o     FollowupOpts
	cfg   retryConfig
	out   FollowupOutput
	items []FollowupItem
	title string
	idNum int

	originRead       bool
	originLabels     []string
	originMilestone  int
	originIsFollowup bool
}

func (r *followupRun) run() error {
	if err := r.readInput(); err != nil {
		return err
	}
	if !r.o.Ticketless {
		if done, err := r.readOrigin(); done || err != nil {
			return err
		}
	}
	if r.originIsFollowup {
		r.ignoreFlags(fmt.Sprintf("origin #%d is a Followup issue", r.idNum))
		return r.guardedAppend(r.idNum, FollowupReasonOriginFollowup, nil)
	}

	issues, err := r.search()
	if err != nil {
		return r.fail(FollowupReasonLookupFailed, err)
	}
	if target, also := r.lineage(issues); target != 0 {
		r.out.Followup.AlsoMatched = also
		r.ignoreFlags(fmt.Sprintf("lineage match #%d takes precedence", target))
		return r.guardedAppend(target, FollowupReasonLineageMatch, nil)
	}

	if r.o.Area == "" {
		r.warn("cross-ticket candidates skipped: no --area")
	} else {
		eligible := r.eligible(issues)
		switch {
		case r.o.AppendTo > 0:
			return r.appendTo(eligible)
		case r.o.Create:
			// --create bypasses the needs-judgment stop and falls through to create.
		case len(eligible) > 0:
			r.needsJudgment(eligible)
			return nil
		}
	}
	return r.create()
}

// finish normalizes the output: every slice non-nil (nil marshals as null,
// watch/docs/go-gotchas.md) and Number only on a successful mutation.
func (r *followupRun) finish() FollowupOutput {
	out := r.out
	f := &out.Followup
	if f.Action == "" {
		f.Action = FollowupActionSkipped
	}
	switch f.Action {
	case FollowupActionCreated, FollowupActionAppended, FollowupActionNoop:
	default:
		f.Number = 0
	}
	if f.Items == nil {
		f.Items = []FollowupItem{}
	}
	if f.Candidates == nil {
		f.Candidates = []FollowupCandidate{}
	}
	if f.AlsoMatched == nil {
		f.AlsoMatched = []int{}
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	if out.Errors == nil {
		out.Errors = []string{}
	}
	return out
}

// fail records a failed run: skipped, the closed-set reason, no number, and
// the error text in Errors. It returns err so callers can `return r.fail(...)`.
func (r *followupRun) fail(reason string, err error) error {
	f := &r.out.Followup
	f.Action = FollowupActionSkipped
	f.Reason = reason
	f.Number = 0
	r.out.Errors = append(r.out.Errors, err.Error())
	return err
}

func (r *followupRun) warn(msg string) {
	if !slices.Contains(r.out.Warnings, msg) {
		r.out.Warnings = append(r.out.Warnings, msg)
	}
}

// ignoreFlags warns for each explicit routing flag a lineage or origin route
// overrides: those routes win so a retry can never silently duplicate.
func (r *followupRun) ignoreFlags(why string) {
	if r.o.AppendTo > 0 {
		r.warn("--append-to ignored: " + why)
	}
	if r.o.Create {
		r.warn("--create ignored: " + why)
	}
}

func (r *followupRun) withRepo(args ...string) []string {
	if r.o.RepoSlug != "" {
		args = append(args, "--repo", r.o.RepoSlug)
	}
	return args
}

// -- input phase (zero gh calls) -------------------------------------------

func invalidFollowup(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidFollowupInput, fmt.Sprintf(format, args...))
}

func (r *followupRun) readInput() error {
	if err := r.validateOpts(); err != nil {
		return r.fail(FollowupReasonInvalidInput, err)
	}
	items, err := readFollowupItems(r.o.ItemsFile)
	if err != nil {
		reason := FollowupReasonInvalidInput
		if errors.Is(err, ErrNoDeferredItems) {
			reason = FollowupReasonNoDeferredItems
		}
		return r.fail(reason, err)
	}
	r.items = items
	r.out.Followup.Items = items
	if r.o.TitleFile != "" {
		title, err := readFollowupTitle(r.o.TitleFile)
		if err != nil {
			return r.fail(FollowupReasonInvalidInput, err)
		}
		r.title = title
	}
	return nil
}

func (r *followupRun) validateOpts() error {
	o := r.o
	if o.Source != FollowupSourceImplement && o.Source != FollowupSourceAddressReview {
		return invalidFollowup("unknown source %q", o.Source)
	}
	if o.Ticketless {
		if o.ID != "" {
			return invalidFollowup("a ticket id cannot be combined with ticketless")
		}
	} else {
		n, err := strconv.Atoi(o.ID)
		if err != nil || n <= 0 || strconv.Itoa(n) != o.ID {
			return invalidFollowup("ticket id %q is not a positive integer", o.ID)
		}
		r.idNum = n
	}
	if o.ItemsFile == "" {
		return invalidFollowup("no items file")
	}
	if !followupPRURLRe.MatchString(o.PRURL) {
		return invalidFollowup("pr url %q is not https://.../pull/<n>", o.PRURL)
	}
	if o.RepoSlug != "" && !followupRepoSlugRe.MatchString(o.RepoSlug) {
		return invalidFollowup("repo slug %q is not OWNER/REPO", o.RepoSlug)
	}
	if o.AppendTo < 0 {
		return invalidFollowup("append-to %d is not a positive integer", o.AppendTo)
	}
	if o.AppendTo > 0 && o.Create {
		return invalidFollowup("append-to and create are mutually exclusive")
	}
	if o.AppendTo > 0 && o.Area == "" {
		return invalidFollowup("append-to requires an area")
	}
	if o.PossiblyRelated != "" {
		if !o.Create {
			return invalidFollowup("possibly-related requires create")
		}
		if !followupRelatedRe.MatchString(o.PossiblyRelated) {
			return invalidFollowup("possibly-related %q is not a comma list of numbers", o.PossiblyRelated)
		}
	}
	return nil
}

func readFollowupItems(path string) ([]FollowupItem, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, invalidFollowup("read items file: %v", err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return nil, fmt.Errorf("items file %q is empty: %w", path, ErrNoDeferredItems)
	}
	var raw []FollowupItem
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, invalidFollowup("items file %q is not a JSON array of {context, area}: %v", path, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("items file %q: %w", path, ErrNoDeferredItems)
	}
	items := make([]FollowupItem, 0, len(raw))
	for i, it := range raw {
		if strings.TrimSpace(it.Context) == "" || strings.TrimSpace(it.Area) == "" {
			return nil, invalidFollowup("item %d has an empty context or area", i)
		}
		// A line break would let an item inject a `Related to #` or `PR:` line.
		if strings.ContainsAny(it.Context, "\r\n") || strings.ContainsAny(it.Area, "\r\n") {
			return nil, invalidFollowup("item %d contains a line break", i)
		}
		if !slices.Contains(items, it) {
			items = append(items, it)
		}
	}
	return items, nil
}

func readFollowupTitle(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", invalidFollowup("read title file: %v", err)
	}
	title := strings.TrimRight(string(b), "\r\n")
	if strings.TrimSpace(title) == "" || strings.ContainsAny(title, "\r\n") {
		return "", invalidFollowup("title file %q must hold one non-empty line", path)
	}
	return title, nil
}

// -- origin phase -----------------------------------------------------------

// readOrigin reads the origin's milestone and labels. done reports that the
// run already has its final result (implement + Followup origin).
func (r *followupRun) readOrigin() (done bool, err error) {
	implement := r.o.Source == FollowupSourceImplement
	out, rerr := ghRetry(r.cfg, r.withRepo("issue", "view", r.o.ID, "--json", "milestone,labels")...)
	var meta struct {
		Milestone *struct {
			Number int `json:"number"`
		} `json:"milestone"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if rerr == nil {
		if jerr := json.Unmarshal(out, &meta); jerr != nil {
			rerr = fmt.Errorf("decode origin #%d: %w", r.idNum, jerr)
		}
	}
	if rerr != nil {
		if implement {
			return true, r.fail(FollowupReasonOriginUnreadable, fmt.Errorf("%w: %w", ErrOriginUnreadable, rerr))
		}
		r.warn("milestone/label inheritance skipped: origin metadata unreadable")
		return false, nil
	}
	r.originRead = true
	for _, l := range meta.Labels {
		r.originLabels = append(r.originLabels, l.Name)
	}
	if meta.Milestone != nil {
		r.originMilestone = meta.Milestone.Number
	}
	if slices.Contains(r.originLabels, followupLabel) {
		if implement {
			r.out.Followup.Action = FollowupActionSkipped
			r.out.Followup.Reason = FollowupReasonOriginIsFollowup
			return true, nil
		}
		r.originIsFollowup = true
	}
	return false, nil
}

// -- lookup phase -----------------------------------------------------------

// search runs the single open-Followup list; eligibility is read from the same
// response (no extra gh call).
func (r *followupRun) search() ([]followupIssue, error) {
	out, err := ghRetry(r.cfg, r.withRepo("issue", "list", "--label", followupLabel, "--state", "open",
		"--limit", strconv.Itoa(followupSearchLimit), "--json", "number,title,body,labels,assignees")...)
	if err != nil {
		return nil, fmt.Errorf("search open Followup issues: %w", err)
	}
	var issues []followupIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("decode open Followup issues: %w", err)
	}
	if len(issues) >= followupSearchLimit {
		r.warn("followup search may be truncated at 200 results")
	}
	return issues, nil
}

// bodyLines is the set of whole lines of body, each with one trailing "\r"
// removed -- never TrimSpace or a substring match.
func bodyLines(body string) map[string]bool {
	set := map[string]bool{}
	for _, l := range strings.Split(body, "\n") {
		set[strings.TrimSuffix(l, "\r")] = true
	}
	return set
}

// lineage returns the append target (0 when none) and every other match. The
// address-review PR line is preferred over the Related to fallback (ticket
// mode only); implement uses Related to only. The lowest number in the
// preferred tier is the target.
func (r *followupRun) lineage(issues []followupIssue) (int, []int) {
	var byPR, byRelated []int
	for _, is := range issues {
		lines := bodyLines(is.Body)
		if r.o.Source == FollowupSourceAddressReview && lines["PR: "+r.o.PRURL] {
			byPR = append(byPR, is.Number)
		}
		if !r.o.Ticketless && lines["Related to #"+r.o.ID] {
			byRelated = append(byRelated, is.Number)
		}
	}
	slices.Sort(byPR)
	slices.Sort(byRelated)
	preferred := byRelated
	if len(byPR) > 0 {
		preferred = byPR
	}
	if len(preferred) == 0 {
		return 0, nil
	}
	target := preferred[0]
	also := []int{}
	for _, n := range slices.Concat(byPR, byRelated) {
		if n != target && !slices.Contains(also, n) {
			also = append(also, n)
		}
	}
	slices.Sort(also)
	return target, also
}

// -- cross-ticket phase -----------------------------------------------------

// leadingAreaToken is the title text before the first ':' or '('.
func leadingAreaToken(title string) (string, bool) {
	i := strings.IndexAny(title, ":(")
	if i < 0 {
		return "", false
	}
	tok := strings.TrimSpace(title[:i])
	return tok, tok != ""
}

// followupEligible applies #778's gates: unstarted (no assignee, none of the
// 8 lifecycle labels) and same area (title token equal case-insensitively, or
// a label exactly equal to the area). No readable area means ineligible.
func followupEligible(is followupIssue, area string) bool {
	if len(is.Assignees) > 0 {
		return false
	}
	labels := is.labelNames()
	for _, l := range labels {
		if slices.Contains(followupUnstartedLabels, l) {
			return false
		}
	}
	if tok, ok := leadingAreaToken(is.Title); ok && strings.EqualFold(tok, area) {
		return true
	}
	return slices.Contains(labels, area)
}

// eligible is the set of cross-ticket candidates, ascending by number. No
// lineage match exists when this runs, so excluding the origin is the whole of
// the "origin and lineage never appear" rule.
func (r *followupRun) eligible(issues []followupIssue) []followupIssue {
	var out []followupIssue
	for _, is := range issues {
		if !r.o.Ticketless && is.Number == r.idNum {
			continue
		}
		if followupEligible(is, r.o.Area) {
			out = append(out, is)
		}
	}
	slices.SortFunc(out, func(a, b followupIssue) int { return a.Number - b.Number })
	return out
}

func (r *followupRun) needsJudgment(eligible []followupIssue) {
	f := &r.out.Followup
	f.Action = FollowupActionNeedsJudgment
	f.Reason = FollowupReasonEligibleCandidates
	f.Candidates = make([]FollowupCandidate, 0, len(eligible))
	for _, is := range eligible {
		f.Candidates = append(f.Candidates, FollowupCandidate{Number: is.Number, Title: is.Title})
	}
}

// appendTo re-reads the named target and re-applies every gate before the
// guarded append, closing the gap between the two calls of the two-step
// interface.
func (r *followupRun) appendTo(eligible []followupIssue) error {
	n := r.o.AppendTo
	ineligible := func(why string) error {
		return r.fail(FollowupReasonAppendTargetIneligible,
			fmt.Errorf("#%d %s: %w", n, why, ErrAppendTargetIneligible))
	}
	if !slices.ContainsFunc(eligible, func(is followupIssue) bool { return is.Number == n }) {
		return ineligible("is not an eligible open Followup for area " + strconv.Quote(r.o.Area))
	}
	snap, err := r.readIssue(n)
	if err != nil {
		return r.fail(FollowupReasonAppendFailed, fmt.Errorf("re-read #%d before append: %w", n, err))
	}
	if !strings.EqualFold(snap.State, "OPEN") || !slices.Contains(snap.labelNames(), followupLabel) ||
		!followupEligible(*snap, r.o.Area) {
		return ineligible("no longer passes the eligibility gates on a fresh read")
	}
	return r.guardedAppend(n, FollowupReasonAppendTo, snap)
}

// -- guarded append ---------------------------------------------------------

func (r *followupRun) readIssue(n int) (*followupIssue, error) {
	out, err := ghRetry(r.cfg, r.withRepo("issue", "view", strconv.Itoa(n), "--json", "body,title,labels,assignees,state")...)
	if err != nil {
		return nil, err
	}
	var is followupIssue
	if err := json.Unmarshal(out, &is); err != nil {
		return nil, fmt.Errorf("decode #%d: %w", n, err)
	}
	is.Number = n
	return &is, nil
}

func followupItemLine(it FollowupItem) string {
	return "- [ ] " + it.Context + " — `" + it.Area + "`"
}

// missingItemLines are the checklist lines not yet present in body. An item is
// present when a whole line equals its unchecked or checked form.
func (r *followupRun) missingItemLines(body string) []string {
	lines := bodyLines(body)
	var missing []string
	for _, it := range r.items {
		line := followupItemLine(it)
		rest := strings.TrimPrefix(line, "- [ ] ")
		if lines[line] || lines["- [x] "+rest] || lines["- [X] "+rest] {
			continue
		}
		missing = append(missing, line)
	}
	return missing
}

func isChecklistLine(l string) bool {
	return strings.HasPrefix(l, "- [ ] ") || strings.HasPrefix(l, "- [x] ") || strings.HasPrefix(l, "- [X] ")
}

// insertItemLines adds lines after the last checklist line of the Deferred
// Items section, or appends a new section when the heading is absent. Every
// pre-existing line is kept byte for byte; inserted lines use the body's line
// ending.
func insertItemLines(body string, lines []string) string {
	cr := ""
	if strings.Contains(body, "\r\n") {
		cr = "\r"
	}
	parts := strings.Split(body, "\n")
	heading := slices.IndexFunc(parts, func(p string) bool {
		return strings.TrimSuffix(p, "\r") == followupDeferredHeading
	})
	if heading < 0 {
		var b strings.Builder
		b.WriteString(body)
		if body != "" {
			if !strings.HasSuffix(body, "\n") {
				b.WriteString(cr + "\n")
			}
			b.WriteString(cr + "\n")
		}
		b.WriteString(followupDeferredHeading)
		for _, l := range lines {
			b.WriteString(cr + "\n" + l)
		}
		return b.String()
	}
	at := heading
	for i := heading + 1; i < len(parts); i++ {
		l := strings.TrimSuffix(parts[i], "\r")
		if followupHeadingRe.MatchString(l) {
			break
		}
		if isChecklistLine(l) {
			at = i
		}
	}
	inserted := make([]string, 0, len(lines))
	for _, l := range lines {
		inserted = append(inserted, l+cr)
	}
	return strings.Join(slices.Insert(parts, at+1, inserted...), "\n")
}

// guardedAppend re-reads issue n (unless snap is the fresh read the caller
// just gated on), adds only the missing items, and edits with --body-file
// only: no label or milestone flag. Zero missing items is a noop with zero
// edits.
func (r *followupRun) guardedAppend(n int, reason string, snap *followupIssue) error {
	if snap == nil {
		var err error
		if snap, err = r.readIssue(n); err != nil {
			return r.fail(FollowupReasonAppendFailed, fmt.Errorf("read #%d for append: %w", n, err))
		}
	}
	f := &r.out.Followup
	f.Reason = reason
	missing := r.missingItemLines(snap.Body)
	if len(missing) == 0 {
		f.Action = FollowupActionNoop
		f.Number = n
		return nil
	}
	if err := r.editBody(n, insertItemLines(snap.Body, missing)); err != nil {
		return r.fail(FollowupReasonAppendFailed, fmt.Errorf("append to #%d: %w", n, err))
	}
	f.Action = FollowupActionAppended
	f.Number = n
	return nil
}

// writeFollowupTemp writes content to a fresh temp file; the command seam has
// no stdin. The returned func removes it.
func writeFollowupTemp(pattern, content string) (string, func(), error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", func() {}, err
	}
	remove := func() { _ = os.Remove(f.Name()) }
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		remove()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		remove()
		return "", func() {}, err
	}
	return f.Name(), remove, nil
}

func (r *followupRun) editBody(n int, body string) error {
	path, remove, err := writeFollowupTemp("cenci-followup-body-*.md", body)
	if err != nil {
		return fmt.Errorf("write body file: %w", err)
	}
	defer remove()
	_, err = ghRetry(r.cfg, r.withRepo("issue", "edit", strconv.Itoa(n), "--body-file", path)...)
	return err
}

// -- create -----------------------------------------------------------------

type followupPayload struct {
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Labels    []string `json:"labels"`
	Milestone int      `json:"milestone,omitempty"`
}

func (r *followupRun) inheritedLabels() []string {
	labels := []string{followupLabel}
	for _, l := range r.originLabels {
		if !slices.Contains(followupExcludedLabels, l) && !slices.Contains(labels, l) {
			labels = append(labels, l)
		}
	}
	return labels
}

func (r *followupRun) createBody() string {
	var b strings.Builder
	b.WriteString(followupDeferredHeading)
	for _, it := range r.items {
		b.WriteString("\n" + followupItemLine(it))
	}
	var middle []string
	if r.o.PossiblyRelated != "" {
		refs := strings.Split(r.o.PossiblyRelated, ",")
		for i := range refs {
			refs[i] = "#" + refs[i]
		}
		middle = append(middle, "Possibly related: "+strings.Join(refs, ", "))
	}
	if !r.o.Ticketless {
		middle = append(middle, "Related to #"+r.o.ID)
	}
	if len(middle) > 0 {
		b.WriteString("\n\n" + strings.Join(middle, "\n"))
	}
	b.WriteString("\n\nPR: " + r.o.PRURL)
	return b.String()
}

func (r *followupRun) create() error {
	if r.title == "" {
		return r.fail(FollowupReasonMissingTitle, invalidFollowup("a title file is required to create a followup"))
	}
	if err := ghLabelCreate(followupLabel, r.o.RepoSlug, r.cfg,
		"--color", followupLabelColor, "--description", followupLabelDescription); err != nil {
		return r.fail(FollowupReasonLabelCreateFailed, fmt.Errorf("%w: %w", ErrFollowupCreateFailed, err))
	}

	payload := followupPayload{Title: r.title, Body: r.createBody(), Labels: r.inheritedLabels(), Milestone: r.originMilestone}
	if r.originRead && r.originMilestone == 0 && len(payload.Labels) == 1 {
		r.warn("milestone/label inheritance empty")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return r.fail(FollowupReasonCreateFailed, fmt.Errorf("%w: encode payload: %w", ErrFollowupCreateFailed, err))
	}
	path, remove, err := writeFollowupTemp("cenci-followup-payload-*.json", string(raw))
	if err != nil {
		return r.fail(FollowupReasonCreateFailed, fmt.Errorf("%w: write payload file: %w", ErrFollowupCreateFailed, err))
	}
	defer remove()

	n, postErr := r.post(path)
	if postErr != nil {
		// The POST may have succeeded server-side: look the issue up again
		// before any retry, and never retry blind.
		found, lerr := r.recoveryMatch()
		switch {
		case lerr != nil:
			return r.fail(FollowupReasonCreateFailed,
				fmt.Errorf("%w: create failed (%v) and the recovery lookup failed: %w", ErrFollowupCreateFailed, postErr, lerr))
		case found != nil:
			return r.finishRecovered(found)
		}
		if n, postErr = r.post(path); postErr != nil {
			return r.fail(FollowupReasonCreateFailed, fmt.Errorf("%w: %w", ErrFollowupCreateFailed, postErr))
		}
	}
	return r.verifyTitle(n)
}

// post is one create POST: a single command call, not ghRetry's transient
// retry, so a failure is never blindly repeated.
func (r *followupRun) post(payloadPath string) (int, error) {
	endpoint := "repos/{owner}/{repo}/issues"
	if r.o.RepoSlug != "" {
		endpoint = "repos/" + r.o.RepoSlug + "/issues"
	}
	out, err := command("gh", "api", endpoint, "-X", "POST", "--input", payloadPath)
	if err != nil {
		return 0, fmt.Errorf("gh api %s: %s: %w", endpoint, strings.TrimSpace(string(out)), err)
	}
	var resp struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(out, &resp); err != nil || resp.Number <= 0 {
		return 0, fmt.Errorf("gh api %s: response carries no issue number", endpoint)
	}
	return resp.Number, nil
}

// recoveryMatch re-runs the lookup after a failed POST: Related to #<id> in
// ticket mode, the PR line otherwise. The lowest match is returned.
func (r *followupRun) recoveryMatch() (*followupIssue, error) {
	issues, err := r.search()
	if err != nil {
		return nil, err
	}
	want := "PR: " + r.o.PRURL
	if !r.o.Ticketless {
		want = "Related to #" + r.o.ID
	}
	var found *followupIssue
	for i := range issues {
		if bodyLines(issues[i].Body)[want] && (found == nil || issues[i].Number < found.Number) {
			found = &issues[i]
		}
	}
	return found, nil
}

// finishRecovered reports a create that had already succeeded. Items the
// recovered issue lacks are added through the guarded append; when all are
// present this costs no extra gh call.
func (r *followupRun) finishRecovered(found *followupIssue) error {
	r.warn("recovered: create had already succeeded")
	if len(r.missingItemLines(found.Body)) > 0 {
		if err := r.guardedAppend(found.Number, FollowupReasonRecovered, nil); err != nil {
			return err
		}
	}
	f := &r.out.Followup
	f.Action = FollowupActionCreated
	f.Reason = FollowupReasonRecovered
	f.Number = found.Number
	return nil
}

// verifyTitle re-fetches the created issue's title and compares it with the
// title file; a mismatch is a failure that names the issue.
func (r *followupRun) verifyTitle(n int) error {
	out, err := ghRetry(r.cfg, r.withRepo("issue", "view", strconv.Itoa(n), "--json", "title")...)
	var got struct {
		Title string `json:"title"`
	}
	if err == nil {
		err = json.Unmarshal(out, &got)
	}
	if err != nil {
		return r.fail(FollowupReasonCreateFailed,
			fmt.Errorf("%w: created #%d but could not verify its title: %w", ErrFollowupCreateFailed, n, err))
	}
	if got.Title != r.title {
		return r.fail(FollowupReasonTitleMismatch,
			fmt.Errorf("created #%d has title %q, want %q: %w", n, got.Title, r.title, ErrFollowupTitleMismatch))
	}
	f := &r.out.Followup
	f.Action = FollowupActionCreated
	f.Reason = FollowupReasonNoLineageMatch
	if r.o.Create {
		f.Reason = FollowupReasonCreateFlag
	}
	f.Number = n
	return nil
}
