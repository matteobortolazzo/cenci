package pipeline

// The plan-authorization record (#1464): a SHA-256-bound `authorization` field
// in .cenci/pipeline/<id>.json that says who released a saved plan for
// execution, and for which exact bytes.
//
//   - `cenci pipeline plan <id> --authorization <kind>` writes it (persist
//     rules, applyPersistRule);
//   - `cenci pipeline plan <id> --approve` checks it at the execution boundary
//     (approve rules, applyApproveGate);
//   - `cenci dispatch approve <id>` releases a held plan (ApproveHeldPlan);
//   - internal/dispatch reads it at admission through ParseAuthorization and
//     PlanFingerprint.
//
// A plan is `held` until a human releases it: a `human` record is written only
// by ApproveHeldPlan (source `cli`) or by an attended `plan --approve` (source
// `attended-launch`). No username or hostname is ever stored.
//
// The parser is default-deny: anything that is not a well-formed record of a
// known kind is `unreadable`, never absent and never a kind. The state file
// lives in the agent-writable repo, so the field is kept as raw bytes
// (State.Authorization) and round-trips through every other state writer
// untouched even when this binary cannot parse it.
//
// Unattended() decides which rule set applies: CENCI_ATTENDED=0 AND the
// launcher's origin marker not saying host-config. The sandbox launcher sets
// CENCI_ATTENDED=0 for every sandbox launch while the host's planning.attended
// is off, human `cenci open` sessions included, so the value alone cannot tell
// a dispatcher-pinned session from a human one. The launcher therefore also
// forwards AttendedOriginEnv: host-config when it read the flag from the host
// config, pinned when CENCI_ATTENDED was set in its own environment (the
// dispatcher's pin), and config-unreadable when the host config could not be
// read. Only host-config makes CENCI_ATTENDED=0 a human session; any other
// value (pinned, config-unreadable) or none fails closed as unattended.
//
// Enforcement limit (documented in watch/docs/dispatch-reconcile.md): the
// record closes the bypass by dispatcher launch and by launch argument. It does
// not defend against a session that deliberately rewrites the state file or
// sets the marker itself.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/planfile"
)

// The environment contract of the unattended predicate. The launcher
// (internal/sandbox/launcher) mirrors AttendedOriginEnv and
// AttendedOriginHostConfig; keep the three in sync.
const (
	// AttendedEnv is the planning-posture variable: "0" pins a session
	// unattended, anything else (or unset) is a human at the keyboard.
	AttendedEnv = "CENCI_ATTENDED"
	// AttendedOriginEnv records where a forwarded AttendedEnv value came from.
	AttendedOriginEnv = "CENCI_ATTENDED_ORIGIN"
	// AttendedOriginHostConfig is the one origin value that exempts
	// CENCI_ATTENDED=0 from counting as unattended: the launcher resolved the
	// flag from the host's planning.attended config (a human `cenci open` or
	// `cenci run` session). Any other value (pinned, config-unreadable), or
	// none, counts as unattended.
	AttendedOriginHostConfig = "host-config"
)

// AuthorizationKind is the closed set of record kinds.
type AuthorizationKind string

const (
	// AuthorizationHeld: saved for human review; releases only through
	// `cenci dispatch approve` or an attended `plan --approve`.
	AuthorizationHeld AuthorizationKind = "held"
	// AuthorizationLean, Trivial, LeanResumed and LeanDecided are the
	// autonomous approval paths of flow's Phase 1.
	AuthorizationLean        AuthorizationKind = "lean"
	AuthorizationTrivial     AuthorizationKind = "trivial"
	AuthorizationLeanResumed AuthorizationKind = "lean-resumed"
	AuthorizationLeanDecided AuthorizationKind = "lean-decided"
	// AuthorizationHuman: a human approved these exact plan bytes. It is never
	// accepted from `plan --authorization`.
	AuthorizationHuman AuthorizationKind = "human"
)

// The `source` of a human record: which release channel wrote it.
const (
	AuthorizationSourceCLI            = "cli"
	AuthorizationSourceAttendedLaunch = "attended-launch"
)

// Authorization is the record persisted in State.Authorization.
type Authorization struct {
	Kind       AuthorizationKind `json:"kind"`
	PlanSHA256 string            `json:"planSha256"`
	RecordedAt time.Time         `json:"recordedAt"`
	// Source is set on human records only.
	Source string `json:"source,omitempty"`
}

// AuthorizationClass is ParseAuthorization's closed verdict.
type AuthorizationClass string

const (
	// AuthorizationAbsent: the state carries no authorization field.
	AuthorizationAbsent AuthorizationClass = "absent"
	// AuthorizationRecorded: a well-formed record of a known kind.
	AuthorizationRecorded AuthorizationClass = "recorded"
	// AuthorizationUnreadable: anything else.
	AuthorizationUnreadable AuthorizationClass = "unreadable"
)

// Sentinel errors, detectable via errors.Is (rule #412). No CENCI-* error code
// is registered for them (watch/docs/error-handling.md).
var (
	// ErrAuthorizationHeld: the plan is held for human review.
	ErrAuthorizationHeld = errors.New("awaiting human plan review")
	// ErrAuthorizationChanged: the plan's bytes differ from the ones approved.
	ErrAuthorizationChanged = errors.New("plan changed since approval")
	// ErrAuthorizationUnrecorded: no authorization record exists for a plan
	// that has not yet been approved for execution.
	ErrAuthorizationUnrecorded = errors.New("plan authorization unrecorded (legacy plan; run cenci dispatch approve)")
	// ErrAuthorizationUnreadable: the record cannot be classified.
	ErrAuthorizationUnreadable = errors.New("plan authorization unreadable")
	// ErrAuthorizationDowngrade: an unattended autonomous kind tried to
	// replace a held record for the same plan bytes.
	ErrAuthorizationDowngrade = errors.New("plan authorization downgrade refused")
	// ErrAuthorizationUsage: `--authorization` was given an unusable value or
	// combination (human, an unknown kind, with --approve, off the plan stage).
	ErrAuthorizationUsage = errors.New("invalid plan authorization request")
	// ErrAttendedRequired: approval needs an attended session.
	ErrAttendedRequired = errors.New("approval requires an attended session")
	// ErrPlanAwaitingInput: the plan is an awaiting-input draft, not a plan.
	ErrPlanAwaitingInput = errors.New("plan is an awaiting-input draft")
)

var planSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// rawKindPattern gates what ApproveHeldPlan echoes of a replaced record's
// kind: the record is agent-writable, so only a short plain token is ever
// printed.
var rawKindPattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// Unattended reports whether this process is an unattended session:
// CENCI_ATTENDED is exactly "0" and the origin marker does not say the value
// came from the host config. Default-deny: only an explicit host-config marker
// makes CENCI_ATTENDED=0 a human session.
func Unattended() bool {
	return os.Getenv(AttendedEnv) == "0" && os.Getenv(AttendedOriginEnv) != AttendedOriginHostConfig
}

// PlanFingerprint is the lowercase hex SHA-256 of a plan file's exact bytes.
// Dispatch and pipeline both hash planfile.Entry.Content through it, so the
// two always agree.
func PlanFingerprint(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// IsPlanFingerprint reports whether s is a lowercase hex SHA-256, the only
// form PlanFingerprint produces.
func IsPlanFingerprint(s string) bool {
	return planSHA256Pattern.MatchString(s)
}

// IsPersistAuthorizationKind reports whether kind is accepted by
// `plan --authorization`: every kind except human.
func IsPersistAuthorizationKind(kind string) bool {
	switch AuthorizationKind(kind) {
	case AuthorizationHeld, AuthorizationLean, AuthorizationTrivial, AuthorizationLeanResumed, AuthorizationLeanDecided:
		return true
	default:
		return false
	}
}

// ParseAuthorization classifies the raw State.Authorization bytes. Absent means
// no field at all; a well-formed record of a known kind is recorded; everything
// else (undecodable, an unknown kind, a malformed planSha256, a human record
// without a known source, a source on a non-human record) is unreadable.
func ParseAuthorization(raw json.RawMessage) (Authorization, AuthorizationClass) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Authorization{}, AuthorizationAbsent
	}
	var a Authorization
	if err := json.Unmarshal(raw, &a); err != nil {
		return Authorization{}, AuthorizationUnreadable
	}
	if !IsPlanFingerprint(a.PlanSHA256) {
		return Authorization{}, AuthorizationUnreadable
	}
	switch a.Kind {
	case AuthorizationHeld, AuthorizationLean, AuthorizationTrivial, AuthorizationLeanResumed, AuthorizationLeanDecided:
		if a.Source != "" {
			return Authorization{}, AuthorizationUnreadable
		}
	case AuthorizationHuman:
		if a.Source != AuthorizationSourceCLI && a.Source != AuthorizationSourceAttendedLaunch {
			return Authorization{}, AuthorizationUnreadable
		}
	default:
		return Authorization{}, AuthorizationUnreadable
	}
	return a, AuthorizationRecorded
}

// rawAuthorizationKind is the best-effort kind of an unparseable record, for
// the one line that tells the human what was replaced. Anything that is not a
// short plain token reads "unreadable", so hostile record text never reaches
// the terminal.
func rawAuthorizationKind(raw json.RawMessage) string {
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || !rawKindPattern.MatchString(probe.Kind) {
		return "unreadable"
	}
	return probe.Kind
}

// newRecord builds a record bound to hash, stamped now.
func newRecord(kind AuthorizationKind, hash, source string) json.RawMessage {
	raw, err := json.Marshal(Authorization{Kind: kind, PlanSHA256: hash, RecordedAt: time.Now().UTC(), Source: source})
	if err != nil {
		// Authorization holds only strings and a time.Time: Marshal cannot fail.
		panic("marshal authorization: " + err.Error())
	}
	return raw
}

// planLookupError is a plan-resolution failure whose message is shown to a
// human verbatim while still matching its sentinel through errors.Is.
type planLookupError struct {
	msg string
	err error
}

func (e *planLookupError) Error() string { return e.msg }
func (e *planLookupError) Unwrap() error { return e.err }

// selectPlan resolves the one healthy plan file for ticket id under repoRoot.
// Every other outcome is an error: an incomplete inventory, no plan, several
// plans (all named), or a broken claim.
func selectPlan(repoRoot string, id int) (*planfile.Entry, error) {
	inv := planfile.Read(repoRoot)
	if !inv.Complete() {
		return nil, fmt.Errorf("plan inventory for ticket %d: %s could not be fully read: %v: %w", id, inv.Dir, inv.Err, ErrPlanInventoryUnreadable)
	}
	sel := inv.Select(id)
	switch sel.Result {
	case planfile.SelectSingle:
		return sel.Entry, nil
	case planfile.SelectAbsent:
		return nil, &planLookupError{msg: fmt.Sprintf("no plan file for #%d", id), err: ErrPlanNotFound}
	case planfile.SelectAmbiguous:
		return nil, &planLookupError{msg: fmt.Sprintf("multiple plan files for #%d: %s", id, strings.Join(entryPaths(sel.Entries), ", ")), err: ErrMultiplePlans}
	case planfile.SelectBroken:
		if len(sel.Entries) == 0 {
			return nil, fmt.Errorf("plan inventory for ticket %d: broken selection with no claiming entries: %w", id, ErrPlanInventoryUnreadable)
		}
		e := sel.Entries[0]
		switch e.Health {
		case planfile.HealthIDMismatch:
			return nil, fmt.Errorf("plan file %s: %s: %w", e.Path, e.Reason, ErrPlanIdentityMismatch)
		case planfile.HealthMalformed:
			return nil, fmt.Errorf("plan file %s: missing or unterminated front matter: %w", e.Path, ErrPlanMalformed)
		default:
			return nil, fmt.Errorf("plan file %s: %s", e.Path, e.Reason)
		}
	default:
		return nil, fmt.Errorf("plan inventory for ticket %d: unrecognized selection result %q", id, sel.Result)
	}
}

// currentPlan resolves the one healthy plan file for ticket id, with the plan
// repo root resolved from repoRootOverride (empty: the git repo root).
func currentPlan(id, repoRootOverride string) (*planfile.Entry, error) {
	n, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("invalid ticket id %q: %w", id, err)
	}
	repoRoot, err := resolvePlanRepoRoot(repoRootOverride)
	if err != nil {
		return nil, err
	}
	return selectPlan(repoRoot, n)
}

// currentPlanHash resolves the plan for o's ticket and fingerprints its exact
// bytes.
func currentPlanHash(o Opts) (string, error) {
	entry, err := currentPlan(o.ID, o.RepoRoot)
	if err != nil {
		return "", err
	}
	return PlanFingerprint(entry.Content), nil
}

// checkAuthorizationOpts rejects an unusable --authorization request before
// anything else: only the closed set of persist kinds, only on bare `plan`.
func checkAuthorizationOpts(o Opts) error {
	switch {
	case o.Authorization == "":
		return nil
	case o.Stage != "plan":
		return fmt.Errorf("--authorization applies to the plan stage only, not %q: %w", o.Stage, ErrAuthorizationUsage)
	case o.Approve:
		return fmt.Errorf("--authorization cannot be combined with --approve: %w", ErrAuthorizationUsage)
	case !IsPersistAuthorizationKind(o.Authorization):
		return fmt.Errorf("--authorization %q is not one of held, lean, trivial, lean-resumed, lean-decided: %w", o.Authorization, ErrAuthorizationUsage)
	}
	return nil
}

// withRelease appends the way out of a refusal.
func withRelease(err error, id string) error {
	return fmt.Errorf("%w; run `cenci dispatch approve %s` from an attended session", err, id)
}

// applyAuthorization runs the rule set that applies to this call after its
// stage transition was accepted: the persist rules for bare `plan
// --authorization K`, the approve gate for `plan --approve`, nothing for every
// other call. It may rewrite s.Authorization in memory (the caller saves) and
// returns warnings to surface; a non-nil error means the call is refused and
// nothing may be saved.
func applyAuthorization(o Opts, s *State, noop bool) ([]string, error) {
	switch {
	case o.Authorization != "":
		return applyPersistRule(o, s)
	case o.Stage == "plan" && o.Approve:
		return nil, applyApproveGate(o, s, noop)
	default:
		return nil, nil
	}
}

// applyPersistRule implements bare `plan --authorization K`. It records even
// when the stage step was a no-op (a re-plan over an executed ticket still
// replaces the record).
//
//   - held always overwrites;
//   - an autonomous K, unattended, over a held record for the same plan bytes
//     is refused (no downgrade);
//   - an autonomous K over a human record for the same plan bytes leaves the
//     record and warns;
//   - an autonomous K, unattended, over an unreadable record is refused (it
//     may be a held record this binary cannot parse — default-deny); attended,
//     it overwrites and warns with the replaced raw kind;
//   - anything else is written bound to the plan's current bytes.
func applyPersistRule(o Opts, s *State) ([]string, error) {
	hash, err := currentPlanHash(o)
	if err != nil {
		return nil, fmt.Errorf("plan --authorization %s for #%s: %w", o.Authorization, o.ID, err)
	}
	kind := AuthorizationKind(o.Authorization)
	var warnings []string
	if kind != AuthorizationHeld {
		cur, class := ParseAuthorization(s.Authorization)
		switch {
		case class == AuthorizationUnreadable:
			if Unattended() {
				return nil, withRelease(fmt.Errorf("plan --authorization %s for #%s: %w", kind, o.ID, ErrAuthorizationUnreadable), o.ID)
			}
			warnings = append(warnings, fmt.Sprintf("replaced an unreadable plan authorization record (kind %s) with --authorization %s", rawAuthorizationKind(s.Authorization), kind))
		case class == AuthorizationRecorded && cur.PlanSHA256 == hash:
			switch cur.Kind {
			case AuthorizationHeld:
				if Unattended() {
					return nil, withRelease(fmt.Errorf("plan --authorization %s for #%s: %w: the plan is held for human review", kind, o.ID, ErrAuthorizationDowngrade), o.ID)
				}
			case AuthorizationHuman:
				return []string{fmt.Sprintf("plan already approved by a human for these exact bytes; --authorization %s ignored", kind)}, nil
			}
		}
	}
	s.Authorization = newRecord(kind, hash, "")
	return warnings, nil
}

// applyApproveGate implements the `plan --approve` authorization gate. noop is
// true when the stage is already at or past plan_approved.
//
// A real transition (from waiting_for_plan_approval, adoption included):
//   - unattended: admitted only for a recorded, non-held kind whose hash
//     matches the plan's current bytes; held, changed, unrecorded and
//     unreadable are refused;
//   - attended: a held, absent or non-matching record becomes human
//     (attended-launch) for the current bytes, a matching autonomous or human
//     record is kept, an unreadable one is refused.
//
// A no-op: no hash re-check. Unattended refuses only a held or unreadable
// record (a missing one is a legacy in-flight execution). Attended refuses an
// unreadable record and releases a held one to human.
func applyApproveGate(o Opts, s *State, noop bool) error {
	cur, class := ParseAuthorization(s.Authorization)
	unattended := Unattended()
	// refuse is a refusal that names the release command as the way out.
	refuse := func(sentinel error) error {
		return withRelease(fmt.Errorf("plan --approve for #%s: %w", o.ID, sentinel), o.ID)
	}

	if noop {
		switch {
		case class == AuthorizationUnreadable:
			return refuse(ErrAuthorizationUnreadable)
		case class == AuthorizationRecorded && cur.Kind == AuthorizationHeld:
			if unattended {
				return refuse(ErrAuthorizationHeld)
			}
			hash, err := currentPlanHash(o)
			if err != nil {
				return fmt.Errorf("plan --approve for #%s: %w", o.ID, err)
			}
			s.Authorization = newRecord(AuthorizationHuman, hash, AuthorizationSourceAttendedLaunch)
		}
		return nil
	}

	hash, err := currentPlanHash(o)
	if err != nil {
		return fmt.Errorf("plan --approve for #%s: %w", o.ID, err)
	}
	if unattended {
		switch {
		case class == AuthorizationAbsent:
			return fmt.Errorf("plan --approve for #%s: %w", o.ID, ErrAuthorizationUnrecorded)
		case class == AuthorizationUnreadable:
			return refuse(ErrAuthorizationUnreadable)
		case cur.Kind == AuthorizationHeld:
			return refuse(ErrAuthorizationHeld)
		case cur.PlanSHA256 != hash:
			return refuse(ErrAuthorizationChanged)
		}
		return nil
	}

	switch {
	case class == AuthorizationUnreadable:
		return refuse(ErrAuthorizationUnreadable)
	case class == AuthorizationRecorded && cur.Kind != AuthorizationHeld && cur.PlanSHA256 == hash:
		return nil
	}
	s.Authorization = newRecord(AuthorizationHuman, hash, AuthorizationSourceAttendedLaunch)
	return nil
}

// ApproveOpts are the resolved inputs to ApproveHeldPlan; RepoRoot and
// StateDir follow Opts' precedence.
type ApproveOpts struct {
	ID       string
	RepoRoot string
	StateDir string
}

// ApproveResult describes what ApproveHeldPlan did.
type ApproveResult struct {
	// PlanPath and Fingerprint identify the plan: its path and the full hex
	// SHA-256 of its bytes.
	PlanPath    string
	Fingerprint string
	// StatePath is the state file consulted.
	StatePath string
	// Released is true when a human record was written. When false the plan
	// was not awaiting review and Kind is the record's kind; nothing was
	// written.
	Released bool
	Kind     AuthorizationKind
	// ReplacedKind is the kind of the record a release replaced (the raw kind
	// of an unreadable one, "unreadable" when it has none), or "" when there
	// was no record.
	ReplacedKind string
}

// ApproveHeldPlan is `cenci dispatch approve <id>`: it records a human
// authorization (source cli) bound to the plan's current bytes when the record
// is held, missing, bound to other bytes, or unreadable, and otherwise leaves
// the state file untouched.
//
// It refuses an unattended session before touching anything. The plan checks
// (none, several, an awaiting-input draft) come before any state access, so a
// refused call writes nothing; a state file that cannot be read or reached
// through the os.Root-scoped access fails the call the same way.
func ApproveHeldPlan(o ApproveOpts) (ApproveResult, error) {
	if Unattended() {
		return ApproveResult{}, fmt.Errorf("%w: %s=0 marks this session as unattended", ErrAttendedRequired, AttendedEnv)
	}
	loc, err := resolveStateLocation(Opts{ID: o.ID, RepoRoot: o.RepoRoot, StateDir: o.StateDir})
	if err != nil {
		return ApproveResult{}, err
	}
	entry, err := currentPlan(o.ID, o.RepoRoot)
	if err != nil {
		return ApproveResult{}, err
	}
	if entry.FrontMatter["status"] == "awaiting-input" {
		return ApproveResult{}, fmt.Errorf("plan file %s is an awaiting-input draft: answer the escalation on the ticket first: %w", entry.Path, ErrPlanAwaitingInput)
	}
	res := ApproveResult{PlanPath: entry.Path, Fingerprint: PlanFingerprint(entry.Content)}

	d, err := openStateDir(loc, true)
	if err != nil {
		return ApproveResult{}, err
	}
	defer func() { _ = d.Close() }()
	res.StatePath = d.displayPath()

	err = withLock(d, defaultRetryConfig(), func() error {
		s, lerr := d.load()
		if lerr != nil {
			return lerr
		}
		cur, class := ParseAuthorization(s.Authorization)
		switch class {
		case AuthorizationRecorded:
			if cur.Kind != AuthorizationHeld && cur.PlanSHA256 == res.Fingerprint {
				res.Kind = cur.Kind
				return nil
			}
			res.ReplacedKind = string(cur.Kind)
		case AuthorizationUnreadable:
			res.ReplacedKind = rawAuthorizationKind(s.Authorization)
		}
		s.ID = o.ID
		s.SchemaVersion = CurrentSchemaVersion
		s.Authorization = newRecord(AuthorizationHuman, res.Fingerprint, AuthorizationSourceCLI)
		s.UpdatedAt = time.Now().UTC()
		if serr := d.save(s); serr != nil {
			return serr
		}
		res.Released = true
		return nil
	})
	if err != nil {
		return ApproveResult{}, err
	}
	return res, nil
}
