package pipeline

// Label lifecycle transitions (ticket #559): working / planned / in-review,
// each stage-gated against the ticket's persisted pipeline stage, with the
// "working" transition additionally enforcing exclusive gh-assignee
// ownership (mirrors flow/skills/ticket-ownership/SKILL.md's full contract:
// resolve the current login, compare against the ticket's assignees,
// auto-claim only when unassigned, never replace an existing assignee).
// Label application self-heals via `gh label create` (treating "already
// exists" as success) before `gh issue edit --add-label`/`--remove-label`.
// All gh calls are retry-wrapped through retry.go's existing
// retryDo/command machinery (ghIssueView's sibling helpers) -- never
// reinvented. Backs the `cenci pipeline label <id> --transition ...`
// mechanics verb.

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Sentinel errors, detectable via errors.Is at the package boundary (rule
// #412). Each remains a distinct value so callers can distinguish failure
// classes (rule #446).
var (
	// ErrWrongStageForLabel is returned when a label transition is invoked
	// while the ticket's persisted pipeline stage does not match that
	// transition's required stage precondition. The error's message names
	// both the required and the actual stage.
	ErrWrongStageForLabel = errors.New("wrong pipeline stage for label transition")

	// ErrForeignAssignee is returned by the "working" transition's
	// ownership check when the ticket is already assigned to exactly one
	// user other than the resolved current gh login.
	ErrForeignAssignee = errors.New("ticket assigned to a different user")

	// ErrMultipleAssignees is returned by the "working" transition's
	// ownership check when the ticket has more than one assignee.
	ErrMultipleAssignees = errors.New("ticket has multiple assignees")
)

// labelStagePrecondition maps each label transition to the minimum pipeline
// stage it requires (ticket #636: a minimum gate, not an exact-stage
// whitelist): "working" requires at least StagePrepared (so a saved-plan
// pickup can re-apply Working before Phase 2's `plan --approve` runs, #668,
// and so can a resume or re-plan from any later stage), "planned" requires
// at least StageWaitingForPlanApproval (so a re-plan over an
// already-executed ticket can still re-apply Planned), "in-review" requires
// at least StageFinalized (the maximum rank in the total order, so
// "at or past" only ever means "at").
var labelStagePrecondition = map[string]Stage{
	"working":   StagePrepared,
	"planned":   StageWaitingForPlanApproval,
	"in-review": StageFinalized,
}

// labelName is the gh label applied by each transition.
var labelName = map[string]string{
	"working":   "Working",
	"planned":   "Planned",
	"in-review": "In Review",
}

// LabelOpts are the resolved inputs to one label-transition invocation.
// RepoRoot/StateDir mirror pipeline.Opts' own precedence for locating the
// persisted state file. RepoSlug is the "<owner>/<repo>" gh --repo target
// (a test-override hook mirroring pipeline.Opts.RepoRoot/StateDir).
type LabelOpts struct {
	ID       string
	RepoRoot string
	StateDir string
	RepoSlug string

	// Transition is one of "working", "planned", "in-review".
	Transition string

	// Trivial, meaningful only for the "planned" transition, keeps
	// "Working" instead of removing it (the Trivial Fast Path continues
	// straight into implementation rather than stopping for plan review --
	// see flow/skills/implement/SKILL.md's Label "Working" section).
	Trivial bool

	// ParentID, meaningful only for the "in-review" transition, cascades
	// "In Review" onto the named parent ticket alongside this ticket (the
	// last-child PR-open case -- see
	// flow/skills/implement/phases/phase-9-pr.md's Labels section).
	ParentID string
}

// ApplyLabelTransition executes one of the three label-lifecycle
// transitions (working/planned/in-review): stage-gates against the
// persisted pipeline stage (ErrWrongStageForLabel on mismatch), for
// "working" also verifies/claims exclusive gh assignee ownership
// (ErrForeignAssignee / ErrMultipleAssignees on conflict, auto-claim when
// unassigned), then self-healingly creates and applies the corresponding gh
// label(s), returning the resulting State.
func ApplyLabelTransition(o LabelOpts) (State, error) {
	minimum, ok := labelStagePrecondition[o.Transition]
	if !ok {
		return State{}, fmt.Errorf("unknown label transition %q", o.Transition)
	}
	name := labelName[o.Transition]

	path, err := resolveStatePath(Opts{ID: o.ID, RepoRoot: o.RepoRoot, StateDir: o.StateDir})
	if err != nil {
		return State{}, err
	}

	current, err := loadState(path)
	if err != nil {
		return State{}, err
	}
	currentRank, currentOk := stageRank(current.Stage)
	if !currentOk {
		return State{}, fmt.Errorf("label transition %q: unknown persisted stage %q for ticket %s: %w",
			o.Transition, current.Stage, o.ID, ErrWrongStageForLabel)
	}
	// Defensive invariant, not reachable via any external input today:
	// labelStagePrecondition only ever maps a transition to a Stage
	// registered in stageOrder, so minimumOk is always true in practice.
	// Checked anyway (default-deny per watch/docs/go-gotchas.md #598,
	// watch/docs/error-handling.md #628) so that a future
	// labelStagePrecondition entry referencing an unregistered Stage
	// constant hard-fails loudly instead of silently ranking as 0 and
	// turning currentRank < minimumRank into an unconditional pass.
	minimumRank, minimumOk := stageRank(minimum)
	if !minimumOk {
		return State{}, fmt.Errorf("label transition %q: unknown minimum stage %q: %w",
			o.Transition, minimum, ErrWrongStageForLabel)
	}
	if currentRank < minimumRank {
		return State{}, fmt.Errorf("label transition %q requires stage %q or later, ticket %s is at stage %q: %w",
			o.Transition, minimum, o.ID, current.Stage, ErrWrongStageForLabel)
	}

	cfg := defaultRetryConfig()

	if o.Transition == "working" {
		if err := verifyAndClaimOwnership(o.ID, o.RepoSlug, cfg); err != nil {
			return State{}, err
		}
	}

	removeLabel := ""
	if o.Transition == "planned" && !o.Trivial {
		removeLabel = labelName["working"]
	}
	if o.Transition == "in-review" {
		removeLabel = labelName["working"]
	}

	if err := ghLabelCreate(name, o.RepoSlug, cfg); err != nil {
		return State{}, err
	}
	if err := ghApplyLabel(o.ID, name, removeLabel, o.RepoSlug, cfg); err != nil {
		return State{}, err
	}

	if o.Transition == "in-review" && o.ParentID != "" {
		if err := ghApplyLabel(o.ParentID, name, "", o.RepoSlug, cfg); err != nil {
			return State{}, err
		}
	}

	// Record the post-edit updatedAt as the plan-freshness baseline (#669):
	// the label edit above just bumped the ticket's updatedAt, and without
	// this baseline CheckPlan would classify the pipeline's own edit as
	// user drift and mark the plan stale. Must run AFTER every gh edit of
	// this ticket, and a fetch failure propagates rather than silently
	// dropping the baseline (#560 item 1).
	ticketUpdatedAt, err := ghIssueUpdatedAt(o.ID, o.RepoSlug, cfg)
	if err != nil {
		return State{}, err
	}

	return recordLabelState(path, name, removeLabel, ticketUpdatedAt)
}

// verifyAndClaimOwnership mirrors flow/skills/ticket-ownership/SKILL.md's
// full contract: resolve the current gh login, compare it against the
// ticket's assignees, auto-claim only when unassigned (then re-verify), and
// never replace an existing assignee.
func verifyAndClaimOwnership(id, repoSlug string, cfg retryConfig) error {
	login, err := ghCurrentLogin(cfg)
	if err != nil {
		return err
	}

	assignees, err := ghIssueAssignees(id, repoSlug, cfg)
	if err != nil {
		return err
	}

	if len(assignees) == 0 {
		if err := ghClaimAssignee(id, login, repoSlug, cfg); err != nil {
			return err
		}
		assignees, err = ghIssueAssignees(id, repoSlug, cfg)
		if err != nil {
			return err
		}
	}

	return checkSoleAssignee(id, assignees, login)
}

// checkSoleAssignee reports whether login is the ticket's sole assignee,
// returning the appropriate sentinel error otherwise.
func checkSoleAssignee(id string, assignees []string, login string) error {
	switch len(assignees) {
	case 0:
		return fmt.Errorf("ticket %s has no assignee even after a claim attempt", id)
	case 1:
		if strings.EqualFold(assignees[0], login) {
			return nil
		}
		return fmt.Errorf("ticket %s is assigned to %q, not the current gh login %q: %w", id, assignees[0], login, ErrForeignAssignee)
	default:
		return fmt.Errorf("ticket %s has multiple assignees %v: %w", id, assignees, ErrMultipleAssignees)
	}
}

// recordLabelState persists the applied/removed label into the ticket's
// Labels field — plus the post-edit ticketUpdatedAt freshness baseline —
// under the same per-ticket flock lock pipeline.Run and SetArtifacts use.
func recordLabelState(path, add, remove, ticketUpdatedAt string) (State, error) {
	var result State
	lockErr := withLock(path+".lock", defaultRetryConfig(), func() error {
		s, lerr := loadState(path)
		if lerr != nil {
			return lerr
		}
		labels := s.Labels
		if remove != "" {
			labels = removeLabelFromSlice(labels, remove)
		}
		labels = addLabelToSlice(labels, add)
		s.Labels = labels
		s.TicketUpdatedAt = ticketUpdatedAt
		s.SchemaVersion = CurrentSchemaVersion
		s.UpdatedAt = time.Now().UTC()
		if serr := saveState(path, s); serr != nil {
			return serr
		}
		result = s
		return nil
	})
	if lockErr != nil {
		return State{}, lockErr
	}
	return result, nil
}

func addLabelToSlice(labels []string, name string) []string {
	for _, l := range labels {
		if l == name {
			return labels
		}
	}
	return append(labels, name)
}

func removeLabelFromSlice(labels []string, name string) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if l != name {
			out = append(out, l)
		}
	}
	return out
}

// -- gh exec helpers, retry-wrapped via retry.go's retryDo/command ----------

// ghRetry calls `gh <args...>` through the shared command seam, retrying
// transient failures via retryDo/isTransientGhError (retry.go), the same
// classification ghIssueView uses.
func ghRetry(cfg retryConfig, args ...string) ([]byte, error) {
	var out []byte
	err := retryDo(func() error {
		var cmdErr error
		out, cmdErr = command("gh", args...)
		if cmdErr == nil {
			return nil
		}
		return fmt.Errorf("gh %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), cmdErr)
	}, isTransientGhError, cfg)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// loginPattern is the validation gate every resolved gh login must pass
// before it is used for ownership comparison/claiming, mirroring
// flow/skills/ticket-ownership/SKILL.md's full contract.
var loginPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// ghCurrentLogin resolves the active gh user's login (mirrors
// ticket-ownership's `gh api user --jq .login`).
func ghCurrentLogin(cfg retryConfig) (string, error) {
	out, err := ghRetry(cfg, "api", "user", "--jq", ".login")
	if err != nil {
		return "", fmt.Errorf("resolve current gh login: %w", err)
	}
	login := strings.TrimSpace(string(out))
	if login == "" {
		return "", fmt.Errorf("resolve current gh login: gh returned an empty login")
	}
	if !loginPattern.MatchString(login) {
		return "", fmt.Errorf("resolve current gh login: %q does not match ^[A-Za-z0-9-]+$", login)
	}
	return login, nil
}

// ghIssueAssignees fetches the ticket's current assignee logins (mirrors
// ticket-ownership's `gh issue view <id> --json assignees`).
func ghIssueAssignees(id, repoSlug string, cfg retryConfig) ([]string, error) {
	args := []string{"issue", "view", id, "--json", "assignees"}
	if repoSlug != "" {
		args = append(args, "--repo", repoSlug)
	}
	out, err := ghRetry(cfg, args...)
	if err != nil {
		return nil, fmt.Errorf("fetch assignees for ticket %s: %w", id, err)
	}
	var payload struct {
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
	}
	if jerr := json.Unmarshal(out, &payload); jerr != nil {
		return nil, fmt.Errorf("decode assignees for ticket %s: %w", id, jerr)
	}
	logins := make([]string, 0, len(payload.Assignees))
	for _, a := range payload.Assignees {
		logins = append(logins, a.Login)
	}
	return logins, nil
}

// ghIssueUpdatedAt fetches the ticket's current updatedAt (RFC3339,
// verbatim) for the post-label-edit freshness baseline (#669), validating
// it parses before it is persisted — a malformed value stored now would
// surface as a confusing plan-check failure much later.
func ghIssueUpdatedAt(id, repoSlug string, cfg retryConfig) (string, error) {
	args := []string{"issue", "view", id, "--json", "updatedAt"}
	if repoSlug != "" {
		args = append(args, "--repo", repoSlug)
	}
	out, err := ghRetry(cfg, args...)
	if err != nil {
		return "", fmt.Errorf("fetch post-edit updatedAt baseline for ticket %s: %w", id, err)
	}
	var payload struct {
		UpdatedAt string `json:"updatedAt"`
	}
	if jerr := json.Unmarshal(out, &payload); jerr != nil {
		return "", fmt.Errorf("decode post-edit updatedAt baseline for ticket %s: %w", id, jerr)
	}
	if _, perr := time.Parse(time.RFC3339, payload.UpdatedAt); perr != nil {
		return "", fmt.Errorf("parse post-edit updatedAt baseline %q for ticket %s: %w", payload.UpdatedAt, id, perr)
	}
	return payload.UpdatedAt, nil
}

// ghClaimAssignee claims ticket id for login (mirrors ticket-ownership's
// `gh issue edit <id> --add-assignee <login>`). Never removes an existing
// assignee -- only called on the "no assignees" branch.
func ghClaimAssignee(id, login, repoSlug string, cfg retryConfig) error {
	args := []string{"issue", "edit", id, "--add-assignee", login}
	if repoSlug != "" {
		args = append(args, "--repo", repoSlug)
	}
	if _, err := ghRetry(cfg, args...); err != nil {
		return fmt.Errorf("claim ticket %s for %s: %w", id, login, err)
	}
	return nil
}

// ghLabelCreate self-healingly creates label name: "already exists" is
// treated as success (rule: content-specific classification, #446), any
// other failure propagates as a genuine error.
func ghLabelCreate(name, repoSlug string, cfg retryConfig) error {
	args := []string{"label", "create", name}
	if repoSlug != "" {
		args = append(args, "--repo", repoSlug)
	}
	err := retryDo(func() error {
		out, cmdErr := command("gh", args...)
		if cmdErr == nil {
			return nil
		}
		text := strings.TrimSpace(string(out))
		if strings.Contains(strings.ToLower(text), "already exists") {
			return nil
		}
		return fmt.Errorf("gh label create %q: %s: %w", name, text, cmdErr)
	}, isTransientGhError, cfg)
	if err != nil {
		return fmt.Errorf("create label %q: %w", name, err)
	}
	return nil
}

// ghApplyLabel applies `--add-label add` (and, when remove is non-empty,
// `--remove-label remove`) to ticket id.
func ghApplyLabel(id, add, remove, repoSlug string, cfg retryConfig) error {
	args := []string{"issue", "edit", id, "--add-label", add}
	if remove != "" {
		args = append(args, "--remove-label", remove)
	}
	if repoSlug != "" {
		args = append(args, "--repo", repoSlug)
	}
	if _, err := ghRetry(cfg, args...); err != nil {
		return fmt.Errorf("apply label %q to ticket %s: %w", add, id, err)
	}
	return nil
}
