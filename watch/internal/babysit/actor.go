package babysit

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// #862: resolution-actor authorization. A GitHub-side resolution signal (a
// resolved thread, a dismissed review, a superseding approval) counts as
// reviewer acceptance only when its actor is not the automation login and is
// either the thread's or review's own author or has admin/write permission.
// Anything else -- the automation login, an unknown actor, a login without write
// access -- leaves the key pending and held (State.ActorHeldKeys) until an
// attended host `cenci babysit resolve` attests the actor.

// errAutomationIdentity is the sentinel a failed automation-login lookup wraps
// (a gh error, an undecodable body, an empty login or one failing
// githubLoginPattern): the whole comment/review pass then fails closed under
// reasonFeedbackUnreadable with detail `automation identity lookup failed`.
var errAutomationIdentity = errors.New("automation identity lookup failed")

// attestationSourceHumanCLI is actorAttestation.Source for an attestation
// recorded by an attended host `cenci babysit resolve`.
const attestationSourceHumanCLI = "human-cli"

// actorAttestation is State.ActorAttestations' value: a human's attestation,
// recorded by `cenci babysit resolve <pr> comment:<id>|review:<id>`, that Actor
// (the login the supervisor saw resolve or dismiss the key; "" for a null or
// unknown actor) is acceptable for that key.
type actorAttestation struct {
	Actor      string    `json:"actor"`
	AttestedAt time.Time `json:"attestedAt"`
	Source     string    `json:"source"`
}

// fetchAutomationLogin reads the login the supervisor's own `gh` reports
// (`gh api user`), rejecting a failed call, an undecodable body, an empty login
// and a login that fails githubLoginPattern, all wrapped in errAutomationIdentity.
func fetchAutomationLogin() (string, error) {
	var user struct {
		Login string `json:"login"`
	}
	if err := ghJSON(&user, "api", "user"); err != nil {
		return "", fmt.Errorf("%w: %w", errAutomationIdentity, err)
	}
	if !githubLoginPattern.MatchString(user.Login) {
		return "", fmt.Errorf("%w: gh api user reported an unusable login", errAutomationIdentity)
	}
	return user.Login, nil
}

// dismissalEventsQuery reads the PR's ReviewDismissedEvent timeline items, one
// page at a time, for fetchDismissalActors' cursor loop.
const dismissalEventsQuery = `query($owner:String!,$name:String!,$number:Int!,$cursor:String){repository(owner:$owner,name:$name){pullRequest(number:$number){timelineItems(first:100,after:$cursor,itemTypes:[REVIEW_DISMISSED_EVENT]){pageInfo{hasNextPage endCursor}nodes{... on ReviewDismissedEvent{actor{login} review{databaseId}}}}}}}`

// graphQLDismissalsResponse is the response envelope for dismissalEventsQuery.
// Errors is checked separately from Data for the same reason as
// graphQLThreadsResponse's (a 200 with partial data is not a clean read); a null
// actor (a deleted account) is a nil pointer.
type graphQLDismissalsResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				TimelineItems struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						Actor *struct {
							Login string `json:"login"`
						} `json:"actor"`
						Review *struct {
							DatabaseID int64 `json:"databaseId"`
						} `json:"review"`
					} `json:"nodes"`
				} `json:"timelineItems"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// fetchDismissalActors reads the PR's ReviewDismissedEvent timeline items with
// a bounded cursor loop (maxThreadPages, an errors[] check, errFeedbackTruncated
// on an exhausted cap). It maps a dismissed review's databaseId to the login of
// the event's actor; "" means the actor is unknown (a null actor, or several
// events for one review naming different actors). A review with no event is
// absent from the map. Any failure returns no map at all: a partial timeline is
// never used.
func fetchDismissalActors(repo, pr string) (map[int64]string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("%s: malformed repo %q, want owner/name", reasonFeedbackUnreadable, repo)
	}
	result := map[int64]string{}
	cursor := ""
	for page := 0; page < maxThreadPages; page++ {
		args := []string{"api", "graphql",
			"-f", "query=" + dismissalEventsQuery,
			"-f", "owner=" + owner,
			"-f", "name=" + name,
			"-F", "number=" + pr,
		}
		if cursor != "" {
			args = append(args, "-f", "cursor="+cursor)
		}
		stdout, stderr, err := execGh(args...)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", reasonFeedbackUnreadable, strings.TrimSpace(stderr), err)
		}
		var resp graphQLDismissalsResponse
		if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
			return nil, fmt.Errorf("%s: %s", reasonFeedbackUnreadable, strings.TrimSpace(stdout))
		}
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("%s: %s", reasonFeedbackUnreadable, resp.Errors[0].Message)
		}
		items := resp.Data.Repository.PullRequest.TimelineItems
		for _, node := range items.Nodes {
			if node.Review == nil {
				continue
			}
			actor := ""
			if node.Actor != nil {
				actor = node.Actor.Login
			}
			if prev, seen := result[node.Review.DatabaseID]; seen && prev != actor {
				actor = ""
			}
			result[node.Review.DatabaseID] = actor
		}
		if !items.PageInfo.HasNextPage {
			return result, nil
		}
		cursor = items.PageInfo.EndCursor
	}
	return nil, fmt.Errorf("%w: exceeded maxThreadPages (%d) dismissal pages with more still reported", errFeedbackTruncated, maxThreadPages)
}

// actorDecision is classifyActor's three-way answer. The zero value is a denial,
// so an unset answer fails closed.
type actorDecision int

const (
	// actorDenied: the actor does not count (the automation login, an unknown
	// actor, or a login without admin/write that is not the author).
	actorDenied actorDecision = iota
	// actorAccepted: the actor counts.
	actorAccepted
	// actorLookupFailed: a lookup needed for the decision failed (the automation
	// login, the dismissal timeline or a permission). Not a denial: the key stays
	// where it was and the next tick retries.
	actorLookupFailed
)

// actorContext is the lazily-resolved, per-pass (one tick, or one pre-merge
// recheck) input of the actor rule: the automation login and the dismissal
// events are each read at most once, and only when a classification needs them.
// err is sticky: once the login or the timeline read failed, every later
// decision in the pass is actorLookupFailed and the caller holds the whole pass.
type actorContext struct {
	loginFn      func() (string, error)
	dismissalsFn func() (map[int64]string, error)
	permission   func(login string) writePermission
	err          error

	loginRead      bool
	loginValue     string
	loginErr       error
	dismissalsRead bool
	dismissals     map[int64]string
}

// newActorContext builds a context over the three lookups. Production goes
// through newLiveActorContext; tests pass fakes.
func newActorContext(login func() (string, error), dismissals func() (map[int64]string, error), permission func(login string) writePermission) *actorContext {
	return &actorContext{loginFn: login, dismissalsFn: dismissals, permission: permission}
}

// newLiveActorContext is newActorContext over the real lookups for s's PR, the
// one a tick and a pre-merge recheck each build: permission is that pass's own
// permissionResolver.
func newLiveActorContext(s *State, permission func(login string) writePermission) *actorContext {
	return newActorContext(fetchAutomationLogin, func() (map[int64]string, error) { return fetchDismissalActors(s.Repo, s.PR) }, permission)
}

// login returns the automation login, reading it at most once per context. A
// failed read is recorded as the context's sticky error, wrapping
// errAutomationIdentity.
func (c *actorContext) login() (string, error) {
	if !c.loginRead {
		c.loginRead = true
		login, err := c.loginFn()
		if err != nil {
			if !errors.Is(err, errAutomationIdentity) {
				err = fmt.Errorf("%w: %w", errAutomationIdentity, err)
			}
			c.loginErr = err
			c.setErr(err)
		}
		c.loginValue = login
	}
	return c.loginValue, c.loginErr
}

// dismissalActor returns the actor that dismissed the review with reviewID.
// known is false when no event matches or the actor is unknown; a failed
// timeline read also returns known=false and sets c.err.
func (c *actorContext) dismissalActor(reviewID int64) (actor string, known bool) {
	if !c.dismissalsRead {
		c.dismissalsRead = true
		events, err := c.dismissalsFn()
		if err != nil {
			c.setErr(err)
		}
		c.dismissals = events
	}
	actor = c.dismissals[reviewID]
	return actor, actor != ""
}

// setErr records the first failure of the pass; later ones are not kept.
func (c *actorContext) setErr(err error) {
	if c.err == nil {
		c.err = err
	}
}

// classifyActor applies the actor rule to one resolution: an attestation for
// exactly this actor accepts; otherwise an unknown actor is denied, an actor that
// EqualFold-matches the automation login is denied (even when it is the author),
// an actor equal to the non-empty author is accepted, and anyone else needs
// admin/write permission (denied or lookup failed when it does not).
func classifyActor(c *actorContext, actor string, known bool, author string, attestation *actorAttestation) actorDecision {
	if attestation != nil && attestation.Actor == actor {
		return actorAccepted
	}
	if !known {
		return actorDenied
	}
	if c.err != nil {
		return actorLookupFailed
	}
	login, err := c.login()
	if err != nil {
		return actorLookupFailed
	}
	if strings.EqualFold(actor, login) {
		return actorDenied
	}
	if author != "" && actor == author {
		return actorAccepted
	}
	switch c.permission(actor) {
	case writePermissionGranted:
		return actorAccepted
	case writePermissionDenied:
		return actorDenied
	default:
		return actorLookupFailed
	}
}
