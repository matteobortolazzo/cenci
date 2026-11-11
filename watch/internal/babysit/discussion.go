package babysit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
)

// Ordinary PR discussion (#1472).
//
// `issues/<pr>/comments` is the conversation thread, distinct from the inline
// review comments (`pulls/<pr>/comments`) and reviews the supervisor already
// reads. Each human comment there is a `discussion:<id>` feedback key bound to
// a body revision (the SHA-256 of the body), so an edit reopens an addressed
// key. A key is addressed only through explicit, authorized evidence: a receipt
// comment bound to a nonce minted for one launch of the finite `babysit-review`
// worker, or a host-only `cenci babysit resolve`. This file owns the pure
// pieces: the strict read, the revision, the receipt parser and verifier, and
// the write-permission lookup. The tick wiring lives in babysit.go.

// discussionKeyPrefix is the key type of an ordinary PR conversation comment.
const discussionKeyPrefix = "discussion:"

// errDiscussionMalformed marks a comment object the strict wire decode refuses
// (a missing or null id, user.login or body). Like an undecodable page it makes
// the whole read unreadable: the caller must hold, never proceed with a partial
// set (watch/docs/error-handling.md's default-deny rule).
var errDiscussionMalformed = errors.New("malformed PR discussion comment")

// discussionComment is one validated PR conversation comment.
type discussionComment struct {
	ID        int64
	Login     string
	Body      string
	CreatedAt string
	UpdatedAt string
}

// discussionWire is the `issues/<pr>/comments` object shape. The pointer
// fields make a missing or null id, user.login or body distinguishable from a
// zero value (watch/docs/go-gotchas.md's null-vs-missing rule).
type discussionWire struct {
	ID   *int64 `json:"id"`
	User *struct {
		Login *string `json:"login"`
	} `json:"user"`
	Body      *string `json:"body"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// discussionKey renders the `discussion:<id>` feedback key of comment id.
func discussionKey(id int64) string {
	return discussionKeyPrefix + strconv.FormatInt(id, 10)
}

// fetchDiscussion reads every page of the PR's conversation comments through
// fetchPaged and validates each object. A gh failure, a page that does not
// decode or a malformed object is an error and returns no comments; complete
// is false when the page cap was reached while a page was still full-sized
// (some comments may be unread), distinct from an unreadable read. Every
// comment is returned: equal timestamps never dedup, ids do.
func fetchDiscussion(repo, pr string) (comments []discussionComment, complete bool, err error) {
	wire, complete, err := fetchPaged[discussionWire]("repos/" + repo + "/issues/" + pr + "/comments")
	if err != nil {
		return nil, false, fmt.Errorf("read PR discussion: %w", err)
	}
	comments = make([]discussionComment, 0, len(wire))
	for i, w := range wire {
		switch {
		case w.ID == nil || *w.ID <= 0:
			return nil, false, fmt.Errorf("%w: object %d has no usable id", errDiscussionMalformed, i)
		case w.User == nil || w.User.Login == nil:
			return nil, false, fmt.Errorf("%w: comment %d has no user.login", errDiscussionMalformed, *w.ID)
		case w.Body == nil:
			return nil, false, fmt.Errorf("%w: comment %d has no body", errDiscussionMalformed, *w.ID)
		}
		comments = append(comments, discussionComment{
			ID:        *w.ID,
			Login:     *w.User.Login,
			Body:      *w.Body,
			CreatedAt: w.CreatedAt,
			UpdatedAt: w.UpdatedAt,
		})
	}
	return comments, complete, nil
}

// bodyRevision is the revision of a comment body: its hex SHA-256. An edit
// yields a new revision, which is what reopens an addressed key.
func bodyRevision(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// humanDiscussion keeps the comments the supervisor treats as feedback: it
// applies only the mechanical exclusions, `[bot]` logins and a first line that
// is the cenci banner (which covers every receipt comment). The banner check
// is first-line only, so a human quoting cenci below their own first line is
// still feedback (isCommentBannerFirstLine's narrow-exclusion rule). Semantic
// actionability is the worker's call, recorded as `no-action`.
func humanDiscussion(comments []discussionComment) []discussionComment {
	var out []discussionComment
	for _, c := range comments {
		if strings.HasSuffix(c.Login, "[bot]") || isCommentBannerFirstLine(c.Body) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// discussionRevisionMap maps each human comment's `discussion:<id>` key to its
// current body revision. A key absent from the map is a deleted (or excluded)
// comment, which verifyReceipt never moves.
func discussionRevisionMap(comments []discussionComment) map[string]string {
	revisions := map[string]string{}
	for _, c := range humanDiscussion(comments) {
		revisions[discussionKey(c.ID)] = bodyRevision(c.Body)
	}
	return revisions
}

// -- receipt grammar ----------------------------------------------------------
//
// The receipt a finite `babysit-review` worker posts, one comment per launch:
//
//	> 🤖 **cenci** — review receipt posted by `/cenci:address-review` (babysit-review).
//	<!-- cenci-review-receipt:<32-hex> -->
//	head: <40|64-hex>
//	- discussion:<id> revision=<64-hex> outcome=<addressed|no-action|blocked|failed>
//
// The parser reads the body after removing blockquote lines, requires exactly
// one marker line and exactly one head line, and rejects the whole receipt on
// any line that starts with `- discussion:` and does not match the entry
// grammar, or on a duplicate key. flow/skills/address-review/SKILL.md and
// flow/docs/comment-attribution.md carry the same grammar
// (flow/tests/review-receipt-contract.test.sh keeps them in sync).

const (
	// receiptMarkerPrefix opens the nonce marker line. The registry kind is
	// `review-receipt`; this is its one sanctioned machine-read.
	receiptMarkerPrefix = "<!-- cenci-review-receipt:"
	receiptMarkerSuffix = " -->"

	receiptHeadPrefix  = "head:"
	receiptEntryPrefix = "- discussion:"
)

// Receipt outcomes. addressed and no-action resolve a key; blocked and failed
// keep it pending and need a human (`cenci babysit resolve`).
const (
	receiptOutcomeAddressed = "addressed"
	receiptOutcomeNoAction  = "no-action"
	receiptOutcomeBlocked   = "blocked"
	receiptOutcomeFailed    = "failed"
)

// Receipt sources recorded on a discussionReceipt.
const (
	receiptSourceReceipt  = "receipt"
	receiptSourceHumanCLI = "human-cli"
)

var (
	receiptMarkerLine = regexp.MustCompile(`^<!-- cenci-review-receipt:([0-9a-f]{32}) -->$`)
	receiptHeadLine   = regexp.MustCompile(`^head: ([0-9a-f]{40}|[0-9a-f]{64})$`)
	receiptEntryLine  = regexp.MustCompile(`^- discussion:([0-9]+) revision=([0-9a-f]{64}) outcome=(addressed|no-action|blocked|failed)$`)
)

var (
	// errNotReceipt means the body carries no marker line (after blockquote
	// removal): an ordinary comment, ignored without a rejection.
	errNotReceipt = errors.New("not a review receipt")
	// errReceiptMalformed means a marker line is present but the receipt breaks
	// the strict grammar; the whole receipt is rejected.
	errReceiptMalformed = errors.New("malformed review receipt")
)

// receiptEntry is one `- discussion:<id> revision=… outcome=…` line.
type receiptEntry struct {
	ID       int64
	Revision string
	Outcome  string
}

// reviewReceipt is a parsed receipt: its nonce, the head SHA the worker
// processed, and one entry per discussion key.
type reviewReceipt struct {
	Nonce   string
	HeadSHA string
	Entries []receiptEntry
}

// receiptMarker renders the marker line for nonce.
func receiptMarker(nonce string) string {
	return receiptMarkerPrefix + nonce + receiptMarkerSuffix
}

// parseReceipt parses body strictly. It returns errNotReceipt when no marker
// line exists and an error wrapping errReceiptMalformed for any grammar
// violation; a receipt may list zero keys.
func parseReceipt(body string) (reviewReceipt, error) {
	lines := strings.Split(stripBlockquoteLines(body), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	candidate := false
	for _, l := range lines {
		if strings.HasPrefix(l, receiptMarkerPrefix) {
			candidate = true
			break
		}
	}
	if !candidate {
		return reviewReceipt{}, errNotReceipt
	}

	var r reviewReceipt
	markers, heads := 0, 0
	seen := map[int64]bool{}
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, receiptMarkerPrefix):
			m := receiptMarkerLine.FindStringSubmatch(l)
			if m == nil {
				return reviewReceipt{}, fmt.Errorf("%w: marker line %q", errReceiptMalformed, l)
			}
			markers++
			r.Nonce = m[1]
		case strings.HasPrefix(l, receiptHeadPrefix):
			m := receiptHeadLine.FindStringSubmatch(l)
			if m == nil {
				return reviewReceipt{}, fmt.Errorf("%w: head line %q", errReceiptMalformed, l)
			}
			heads++
			r.HeadSHA = m[1]
		case strings.HasPrefix(l, receiptEntryPrefix):
			m := receiptEntryLine.FindStringSubmatch(l)
			if m == nil {
				return reviewReceipt{}, fmt.Errorf("%w: entry line %q", errReceiptMalformed, l)
			}
			id, err := strconv.ParseInt(m[1], 10, 64)
			if err != nil || id <= 0 {
				return reviewReceipt{}, fmt.Errorf("%w: entry id %q", errReceiptMalformed, m[1])
			}
			if seen[id] {
				return reviewReceipt{}, fmt.Errorf("%w: duplicate key discussion:%d", errReceiptMalformed, id)
			}
			seen[id] = true
			r.Entries = append(r.Entries, receiptEntry{ID: id, Revision: m[2], Outcome: m[3]})
		}
	}
	if markers != 1 {
		return reviewReceipt{}, fmt.Errorf("%w: %d marker lines", errReceiptMalformed, markers)
	}
	if heads != 1 {
		return reviewReceipt{}, fmt.Errorf("%w: %d head lines", errReceiptMalformed, heads)
	}
	return r, nil
}

// -- receipt verification -----------------------------------------------------

// reviewLaunchRecord is the outstanding `babysit-review` launch a receipt is
// verified against: the nonce minted for it, when the supervisor launched it
// and the full feedback keys it carried (receipts only act on `discussion:`
// keys inside this set). TrustedKeys is the allow-list of discussion keys in Keys
// whose comment author's permission lookup answered `admin` or `write` when the
// launch was built. Trust is default-deny: a discussion key in Keys that is not in
// TrustedKeys -- a denied author, or a record with no trust field at all (a state
// saved by an earlier build, a hand-edited or truncated file) -- is untrusted. The
// launch argument marks each untrusted key with `:untrusted`, and a receipt can
// never mark one `addressed`. The tick wiring stores it in State.
type reviewLaunchRecord struct {
	Nonce       string    `json:"nonce"`
	LaunchedAt  time.Time `json:"launchedAt"`
	Keys        []string  `json:"keys"`
	TrustedKeys []string  `json:"trustedKeys,omitempty"`
}

// discussionReceipt is the evidence recorded for one addressed or unresolved
// discussion key: the comment id, the revision the worker saw, the head SHA it
// processed, its outcome, the receipt comment that carried it (0 for a human
// resolution) and where it came from.
type discussionReceipt struct {
	CommentID        int64  `json:"commentId"`
	Revision         string `json:"revision"`
	HeadSHA          string `json:"headSha,omitempty"`
	Outcome          string `json:"outcome"`
	ReceiptCommentID int64  `json:"receiptCommentId,omitempty"`
	Source           string `json:"source"`
}

// receiptRejectReason is the closed set of reasons verifyReceipt rejects a
// receipt-shaped comment.
type receiptRejectReason string

const (
	receiptRejectMalformed         receiptRejectReason = "malformed receipt"
	receiptRejectNonceUnknown      receiptRejectReason = "nonce is not the outstanding launch's"
	receiptRejectNonceConsumed     receiptRejectReason = "nonce already consumed"
	receiptRejectEdited            receiptRejectReason = "receipt was edited"
	receiptRejectBadTimestamp      receiptRejectReason = "unusable timestamp"
	receiptRejectBeforeLaunch      receiptRejectReason = "created before the launch"
	receiptRejectNoWritePermission receiptRejectReason = "author lacks write permission"
	// receiptRejectPermissionUnknown is a failed permission lookup: the receipt
	// stays unaccepted (fail closed) but its nonce is not consumed, so the next
	// tick looks again.
	receiptRejectPermissionUnknown receiptRejectReason = "write permission lookup failed; will retry"
)

// receiptRejection records one rejected receipt-shaped comment.
type receiptRejection struct {
	CommentID int64
	Reason    receiptRejectReason
}

// String renders the rejection for an automergeDetail note.
func (r receiptRejection) String() string {
	return fmt.Sprintf("receipt comment %d rejected: %s", r.CommentID, r.Reason)
}

// receiptResult is verifyReceipt's decision. When Accepted, Nonce is consumed
// by ReceiptCommentID. Addressed holds the keys the receipt resolved (outcome
// addressed or no-action, revision still current); Unresolved holds blocked or
// failed keys whose revision is still current, plus an `addressed` entry for an
// key not in the launch's TrustedKeys, recorded as blocked (Refused names it). Keys outside the
// launch's key set, with a stale revision or with no current comment are in
// neither.
type receiptResult struct {
	Accepted         bool
	Nonce            string
	ReceiptCommentID int64
	HeadSHA          string
	Addressed        map[string]discussionReceipt
	Unresolved       map[string]discussionReceipt
	Rejections       []receiptRejection
	// Refused notes each key whose `addressed` outcome was not accepted because
	// the launch did not record its comment author as trusted.
	Refused []string
}

// verifyReceipt decides which receipt among comments, if any, is authentic for
// the outstanding launch. It is a pure function of its arguments. consumed
// maps each already-consumed nonce to the comment that consumed it (a
// superseded nonce is recorded with id 0): that comment is skipped silently,
// any other comment carrying a consumed nonce is a rejection. Comments are
// examined in id order and checks run cheapest first -- grammar, nonce,
// edited, created_at, then hasWrite, which is called only for a comment whose
// nonce is the outstanding one. A lookup that comes back unknown is rejected
// without consuming the nonce (fail closed, retried next tick). A key in the
// launch that is not in its TrustedKeys can never be moved to Addressed by an
// `addressed` outcome; `no-action`, `blocked` and `failed` are taken as
// reported. At most
// one comment is accepted per call;
// a comment without a marker line (including a quoted marker) is not a
// receipt and produces no rejection.
func verifyReceipt(launch *reviewLaunchRecord, consumed map[string]int64, comments []discussionComment, revisions map[string]string, hasWrite func(login string) writePermission) receiptResult {
	var res receiptResult
	used := make(map[string]int64, len(consumed)+1)
	for nonce, id := range consumed {
		used[nonce] = id
	}
	var launchAt time.Time
	inLaunch := map[string]bool{}
	trusted := map[string]bool{}
	if launch != nil {
		launchAt = launch.LaunchedAt.UTC().Truncate(time.Second)
		for _, k := range launch.Keys {
			inLaunch[k] = true
		}
		for _, k := range launch.TrustedKeys {
			trusted[k] = true
		}
	}
	permission := map[string]writePermission{}

	ordered := append([]discussionComment(nil), comments...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	for _, c := range ordered {
		receipt, err := parseReceipt(c.Body)
		if errors.Is(err, errNotReceipt) {
			continue
		}
		reject := func(reason receiptRejectReason) {
			res.Rejections = append(res.Rejections, receiptRejection{CommentID: c.ID, Reason: reason})
		}
		if err != nil {
			reject(receiptRejectMalformed)
			continue
		}
		if id, ok := used[receipt.Nonce]; ok {
			if id != c.ID {
				reject(receiptRejectNonceConsumed)
			}
			continue
		}
		if launch == nil || receipt.Nonce != launch.Nonce {
			reject(receiptRejectNonceUnknown)
			continue
		}
		if c.UpdatedAt != c.CreatedAt {
			reject(receiptRejectEdited)
			continue
		}
		created, err := time.Parse(time.RFC3339, c.CreatedAt)
		if err != nil || launchAt.IsZero() {
			reject(receiptRejectBadTimestamp)
			continue
		}
		if !created.After(launchAt) {
			reject(receiptRejectBeforeLaunch)
			continue
		}
		granted, asked := permission[c.Login]
		if !asked {
			granted = hasWrite(c.Login)
			permission[c.Login] = granted
		}
		switch granted {
		case writePermissionGranted:
		case writePermissionDenied:
			reject(receiptRejectNoWritePermission)
			continue
		default:
			reject(receiptRejectPermissionUnknown)
			continue
		}

		used[receipt.Nonce] = c.ID
		res.Accepted = true
		res.Nonce = receipt.Nonce
		res.ReceiptCommentID = c.ID
		res.HeadSHA = receipt.HeadSHA
		res.Addressed = map[string]discussionReceipt{}
		res.Unresolved = map[string]discussionReceipt{}
		for _, e := range receipt.Entries {
			key := discussionKey(e.ID)
			if !inLaunch[key] {
				continue
			}
			if current, ok := revisions[key]; !ok || current != e.Revision {
				continue
			}
			rec := discussionReceipt{
				CommentID:        e.ID,
				Revision:         e.Revision,
				HeadSHA:          receipt.HeadSHA,
				Outcome:          e.Outcome,
				ReceiptCommentID: c.ID,
				Source:           receiptSourceReceipt,
			}
			switch {
			case e.Outcome == receiptOutcomeAddressed && !trusted[key]:
				// The worker must never act on an author without recorded write
				// access (default-deny), so `addressed` is not evidence for this
				// key: it stays pending as blocked, for a write-access human to
				// handle and `resolve`.
				rec.Outcome = receiptOutcomeBlocked
				res.Unresolved[key] = rec
				res.Refused = append(res.Refused, key+": its comment author is not recorded as having write permission, so the receipt cannot mark it addressed")
			case e.Outcome == receiptOutcomeAddressed || e.Outcome == receiptOutcomeNoAction:
				res.Addressed[key] = rec
			default:
				res.Unresolved[key] = rec
			}
		}
	}
	return res
}

// -- write permission ---------------------------------------------------------

// githubLoginPattern is GitHub's login grammar (starts alphanumeric, at most 39
// characters, hyphens only after the first), checked before a login is spliced
// into the collaborator-permission endpoint path. It mirrors
// internal/dispatch/permission.go, reimplemented locally because babysit must
// not import dispatch.
var githubLoginPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// collaboratorPermission is GET repos/<repo>/collaborators/<login>/permission's
// response; only the top-level `permission` field is authoritative, and the
// pointer distinguishes a missing field from an empty value.
type collaboratorPermission struct {
	Permission *string `json:"permission"`
}

// writePermission is the three-way answer of a collaborator-permission lookup.
// The zero value is writePermissionUnknown, so an unset answer fails closed: a
// caller must never read "could not tell" as either a grant or a final denial.
type writePermission int

const (
	// writePermissionUnknown: the lookup failed (a gh error or timeout, an
	// undecodable body, a missing or null `permission`). Fail closed, but retry.
	writePermissionUnknown writePermission = iota
	// writePermissionDenied: GitHub answered and the login lacks write authority
	// (or the login is not a valid GitHub login and was never sent to gh).
	writePermissionDenied
	// writePermissionGranted: GitHub answered `admin` or `write`.
	writePermissionGranted
)

// fetchWritePermission resolves login's current write authority on repo: the
// top-level `permission` is exactly `admin` or `write` (granted), any other
// answer -- `read`, `triage`, `none`, `maintain` and anything unrecognized -- or
// an invalid login (never sent to gh) is denied, and a gh error, an undecodable
// body or a missing field is unknown. Only a granted answer authorizes; the
// distinction lets a transient failure be retried and reported as such rather
// than as a denial.
func fetchWritePermission(repo, login string) writePermission {
	if !githubLoginPattern.MatchString(login) {
		return writePermissionDenied
	}
	stdout, _, err := execGh("api", "repos/"+repo+"/collaborators/"+login+"/permission")
	if err != nil {
		return writePermissionUnknown
	}
	var resp collaboratorPermission
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil || resp.Permission == nil {
		return writePermissionUnknown
	}
	if *resp.Permission == "admin" || *resp.Permission == "write" {
		return writePermissionGranted
	}
	return writePermissionDenied
}

// permissionResolver caches fetchWritePermission per login for one tick, so a
// login that authored several comments (or a receipt and a comment) costs one
// lookup.
func permissionResolver(repo string) func(login string) writePermission {
	cache := map[string]writePermission{}
	return func(login string) writePermission {
		if p, ok := cache[login]; ok {
			return p
		}
		p := fetchWritePermission(repo, login)
		cache[login] = p
		return p
	}
}

// -- nonce seam ---------------------------------------------------------------

// newReviewNonce mints the per-launch receipt nonce: 32 lowercase hex
// characters (16 bytes) from crypto/rand, the form receiptMarkerLine accepts.
// sandbox.NewLaunchID already generates exactly that, so it is reused. A var so
// tests substitute a fixed nonce, like the package's other seams.
var newReviewNonce = sandbox.NewLaunchID
