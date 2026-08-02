package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// githubLoginPattern is the login-shape guard (#882 plan Assumptions) run
// before any login is interpolated into the collaborator-permission endpoint
// path -- a path-injection defense mirroring go-gotchas #855's
// splicing-validation rule, even though every current call site's login
// comes from GitHub's own REST response. Matches GitHub's own login grammar:
// starts with an alphanumeric, at most 39 characters total, hyphens allowed
// only after the first character.
var githubLoginPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// permissionProbeTimeout bounds every individual write-permission `gh api
// .../collaborators/<login>/permission` call (#882), mirroring
// ghIssueCommentsTimeout's rationale (resume.go): a hung network call must
// never stall a dispatch pass indefinitely. A var (not a const), like
// maxOpenPRProbeDuration (openpr.go): permission_test.go overrides it to a
// small value for the duration of one test so proving the timeout
// classification does not require waiting out the real bound.
var permissionProbeTimeout = 60 * time.Second

// maxPermissionStdoutBytes bounds the write-permission probe's stdout/stderr
// via execGhBoundedCtx -- generous for the tiny `{"permission":"..."}`
// payload this endpoint returns, but exceeding it is an explicit
// WritePermissionTruncated classification, never a silently-short JSON body.
const maxPermissionStdoutBytes = 1 << 16 // 64 KiB

// maxPermissionLookupsPerRepo bounds how many write-permission gh calls one
// resolveAnswerProbes pass may make for a single repository (#882 plan's
// Assumptions: "bound permission lookups per repository"), mirroring
// maxDependencyResolutions (dependency.go)'s per-pass budget shape. Once the
// cap is reached, any further cache-miss login for that repo resolves
// directly to WritePermissionCapExhausted without shelling out -- still
// fails closed (never grants), it just stops making gh calls for that repo.
const maxPermissionLookupsPerRepo = 50

// writePermissionGrantingValues is the closed, accepted set of top-level
// `permission` values that grant CURRENT repository write authority (#882
// plan Q1): exactly "admin" and "write" -- `role_name` is deliberately never
// consulted, since custom org roles make it an open set that would break
// closed-set discipline. Documented verbatim (byte-identical value list) in
// flow/skills/implement/phases/phase-1-plan.md's `## Resume From Draft`
// section and pinned there by resume_crosslane_test.go.
var writePermissionGrantingValues = []string{"admin", "write"}

// writePermissionDeniedValues is the closed set of top-level `permission`
// values that are recognized but positively insufficient (#882 plan's
// Decisions: "read/triage permission ... deny distinctly"): a value here
// classifies as WritePermissionDenied, distinct from
// WritePermissionUnknownValue (a value outside both sets).
var writePermissionDeniedValues = []string{"read", "triage", "none"}

// collaboratorPermissionResponse mirrors GET
// repos/<owner>/<repo>/collaborators/<login>/permission's response shape
// (#882 plan Q1): only the top-level `permission` field is the authoritative
// signal. A pointer field (go-gotchas #881's null-vs-missing rule) so a
// well-formed JSON object that simply omits `permission` (e.g. one carrying
// only `role_name`) is distinguishable from an empty-string value.
type collaboratorPermissionResponse struct {
	Permission *string `json:"permission"`
}

// fetchWritePermission resolves login's CURRENT write permission on repo via
// the authoritative `gh api repos/<owner>/<repo>/collaborators/<login>/permission`
// endpoint (#882 plan Q1), classifying the outcome into the WritePermission
// closed set. The login-shape guard (githubLoginPattern) runs first and
// never spends a gh call on an invalid login -- a path-injection defense
// mirroring probeEscalationAnswer's own validAnchor-before-budget
// short-circuit (resume.go). Bounded by permissionProbeTimeout and
// maxPermissionStdoutBytes via execGhBoundedCtx (watch/AGENTS.md #825/#852/
// #854: never bare exec.Command). Log lines avoid the " skip:" / " dispatch "
// substrings lazyboards classifies decision lines by (autonomy.go:148-150).
func fetchWritePermission(repo, login string, out io.Writer) WritePermission {
	if !githubLoginPattern.MatchString(login) {
		return WritePermissionLoginInvalid
	}

	ctx, cancel := context.WithTimeout(context.Background(), permissionProbeTimeout)
	defer cancel()
	endpoint := fmt.Sprintf("repos/%s/collaborators/%s/permission", repo, login)
	stdout, stderr, err := execGhBoundedCtx(ctx, maxPermissionStdoutBytes, "api", endpoint)
	if err != nil {
		switch {
		case errors.Is(err, errGhTimeout):
			logf(out, "dispatch: write-permission probe for %s login %s timed out: %s\n",
				repo, login, truncateDetail(collapseLines(stdout+stderr), maxProbeLogDetailBytes))
			return WritePermissionTimeout
		case errors.Is(err, errGhOutputTruncated):
			logf(out, "dispatch: write-permission probe for %s login %s exceeded the %d-byte stdout cap\n",
				repo, login, maxPermissionStdoutBytes)
			return WritePermissionTruncated
		default:
			logf(out, "dispatch: write-permission probe for %s login %s failed: %v (%s)\n",
				repo, login, err, truncateDetail(collapseLines(stdout+stderr), maxProbeLogDetailBytes))
			return WritePermissionAPIError
		}
	}

	var resp collaboratorPermissionResponse
	if uerr := json.Unmarshal([]byte(stdout), &resp); uerr != nil {
		logf(out, "dispatch: write-permission probe for %s login %s returned malformed JSON: %s\n",
			repo, login, truncateDetail(collapseLines(stdout), maxProbeLogDetailBytes))
		return WritePermissionMalformed
	}
	if resp.Permission == nil {
		logf(out, "dispatch: write-permission probe for %s login %s response carries no permission field\n", repo, login)
		return WritePermissionMissingField
	}

	value := *resp.Permission
	for _, v := range writePermissionGrantingValues {
		if value == v {
			return WritePermissionGranted
		}
	}
	for _, v := range writePermissionDeniedValues {
		if value == v {
			return WritePermissionDenied
		}
	}
	logf(out, "dispatch: write-permission probe for %s login %s returned unrecognized permission value %q\n", repo, login, value)
	return WritePermissionUnknownValue
}

// writePermissionAnswerProbe is the pure WritePermission -> AnswerProbe
// mapping (#882), with a default-deny branch: a zero-value or unrecognized
// WritePermission never silently reproduces an existing class, it maps to
// AnswerProbePermissionUnknownValue (watch/docs/go-gotchas.md #598).
func writePermissionAnswerProbe(perm WritePermission) AnswerProbe {
	switch perm {
	case WritePermissionGranted:
		return AnswerProbeAnswered
	case WritePermissionDenied:
		return AnswerProbeUnauthorized
	case WritePermissionLoginInvalid:
		return AnswerProbeLoginInvalid
	case WritePermissionAPIError:
		return AnswerProbePermissionAPIError
	case WritePermissionTimeout:
		return AnswerProbePermissionTimeout
	case WritePermissionTruncated:
		return AnswerProbePermissionTruncated
	case WritePermissionMalformed:
		return AnswerProbePermissionMalformed
	case WritePermissionMissingField:
		return AnswerProbePermissionMissingField
	case WritePermissionCapExhausted:
		return AnswerProbePermissionCapExhausted
	case WritePermissionUnknownValue:
		return AnswerProbePermissionUnknownValue
	default:
		return AnswerProbePermissionUnknownValue
	}
}

// permissionCache is the per-pass, per-(repo, lowercased login) cache and
// per-repo lookup budget for the write-permission probe (#882 plan's
// Assumptions): constructed once inside resolveAnswerProbes and dies with
// the pass -- never persisted, never carried across passes (a prior pass's
// authorization is never treated as current). Login is lowercased before
// keying (GitHub logins are themselves case-insensitive), so answering as
// "octocat" and "Octocat" in the same pass costs exactly one gh call.
type permissionCache struct {
	verdicts      map[string]WritePermission
	repoCalls     map[string]int
	repoCapLogged map[string]bool
}

// newPermissionCache constructs an empty permissionCache.
func newPermissionCache() *permissionCache {
	return &permissionCache{
		verdicts:      make(map[string]WritePermission),
		repoCalls:     make(map[string]int),
		repoCapLogged: make(map[string]bool),
	}
}

// permissionCacheKey namespaces a lowercased login by its repo so the same
// login answering in two different repos is never conflated (mirrors
// planKey's repo-namespacing rationale, decide.go).
func permissionCacheKey(repo, login string) string {
	return repo + "\x00" + strings.ToLower(login)
}

// resolveWritePermission resolves login's current write permission on repo,
// consulting the cache first, then the per-repo budget, falling back to
// fetchWritePermission only on a genuine cache miss under budget (#882).
// Once a repo's budget is exhausted, every further cache-miss login for that
// repo resolves directly to WritePermissionCapExhausted without shelling
// out, and the cap-hit line is logged exactly once for that repo this pass.
//
// A login that fails githubLoginPattern's shape check is resolved (and
// cached) directly via fetchWritePermission without ever touching the
// per-repo budget counter: fetchWritePermission's own doc comment states
// such a login "never spends a gh call", so it must never be charged against
// the budget that exists to bound actual gh invocations (review fix,
// #882 follow-up).
func (c *permissionCache) resolveWritePermission(repo, login string, out io.Writer) WritePermission {
	key := permissionCacheKey(repo, login)
	if v, ok := c.verdicts[key]; ok {
		return v
	}
	if !githubLoginPattern.MatchString(login) {
		v := fetchWritePermission(repo, login, out)
		c.verdicts[key] = v
		return v
	}
	if c.repoCalls[repo] >= maxPermissionLookupsPerRepo {
		if !c.repoCapLogged[repo] {
			logf(out, "dispatch: write-permission lookup cap (%d) reached for %s, remaining unresolved\n", maxPermissionLookupsPerRepo, repo)
			c.repoCapLogged[repo] = true
		}
		return WritePermissionCapExhausted
	}
	c.repoCalls[repo]++
	v := fetchWritePermission(repo, login, out)
	c.verdicts[key] = v
	return v
}
