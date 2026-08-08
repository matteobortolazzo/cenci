package babysit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const fixCap = 3

// stateSchemaVersion is State's persisted schema version. Version 2 (#885)
// adds LaunchedKeys, split out of AddressedKeys' old dual role as both
// resolution truth and launch-dedup marker -- a dedup key must never double
// as merge authorization. load()'s migration seeds LaunchedKeys from
// PendingKeys for any state below this version, so a supervisor upgraded
// mid-episode does not fire one spurious address-review relaunch for work
// already dispatched under the old schema.
const stateSchemaVersion = 2

type Options struct {
	PR, Agent, StateDir string
	Interval            time.Duration
	Once                bool
	// Session, when set, is the tmux session babysit's launch() calls
	// target explicitly (#975) -- flag precedence over the arm-time local
	// tmux resolution, mirroring run.Opts.Session's own "flag > current
	// tmux session" precedence. The arming parent threads this onto the
	// detached supervisor child's argv (`cenci babysit ... --session
	// <name>`); the daemon-spawned path (#977) will set it directly.
	Session string
	// Dir, when set, is the working directory babysit's launch() calls
	// start their windows in (#975), mirroring run.Opts.Dir.
	Dir string
}
type State struct {
	SchemaVersion       int      `json:"schemaVersion"`
	PR                  string   `json:"pr"`
	Repo                string   `json:"repo"`
	Agent               string   `json:"agent"`
	IntervalSeconds     int64    `json:"intervalSeconds"`
	CurrentDelaySeconds int64    `json:"currentDelaySeconds"`
	LastHeadSHA         string   `json:"lastCiHeadSha,omitempty"`
	FixAttempts         int      `json:"ciFixAttempts"`
	RepairPending       bool     `json:"ciRepairPending"`
	LastCommentAt       string   `json:"lastCommentTimestamp,omitempty"`
	AddressedKeys       []string `json:"addressedCommentKeys,omitempty"`
	PendingKeys         []string `json:"pendingCommentKeys,omitempty"`
	// LaunchedKeys records which currently-pending keys already had an
	// address-review workflow dispatched for their *current* resolution
	// episode (#885). It is launch-dedup bookkeeping ONLY -- a key's
	// presence here means nothing more than "don't relaunch this episode's
	// workflow again"; it is never merge authorization, and resolution truth
	// stays entirely AddressedKeys/PendingKeys' job. A key drops out of
	// LaunchedKeys the moment reconcileFeedback resolves it, so a later
	// reopen of the same key starts its new episode unlaunched and is picked
	// up by tick's PendingKeys-\-LaunchedKeys launch trigger again.
	LaunchedKeys     []string `json:"launchedFeedbackKeys,omitempty"`
	PendingCommentAt string   `json:"pendingCommentTimestamp,omitempty"`
	// PendingHeadSHA is repair-attempt/deduplication metadata only -- it
	// records the head commit SHA at the moment new feedback was detected --
	// and is never proof of resolution (#850). A push landing after this SHA
	// (repair or otherwise) does not, by itself, mean a reviewer accepted
	// anything; only reconcileFeedback's GitHub-authoritative check (resolved
	// review thread, or a dismissed/superseded CHANGES_REQUESTED review)
	// clears a PendingKeys entry. Still written at detection time (see the
	// new-key append below), just no longer read to decide resolution.
	PendingHeadSHA string    `json:"pendingCommentHeadSha,omitempty"`
	PID            int       `json:"pid,omitempty"`
	Status         string    `json:"status"`
	UpdatedAt      time.Time `json:"updatedAt"`

	// RepoRoot is the supervised repository's local checkout root, resolved
	// once at startup. It is the repo half of BlocksClose's join key; a
	// network-free `git rev-parse` rather than repository()'s `gh` call,
	// because the close path must make no network calls (#787).
	RepoRoot string `json:"repoRoot,omitempty"`
	// LaunchSession is the tmux session every launch() call targets,
	// resolved once at arm time (#975) rather than inherited from whatever
	// $TMUX_PANE happens to be live when a much-later tick actually
	// launches a repair/attention/address-review workflow -- the arming
	// pane can be long gone by then. Additive; re-resolved on every arm, no
	// stateSchemaVersion bump needed (contrast #885's LaunchedKeys
	// migration).
	LaunchSession string `json:"launchSession,omitempty"`
	// LaunchDir is the working directory every launch() call's spawned
	// window starts in, resolved alongside LaunchSession at arm time
	// (#975).
	LaunchDir string `json:"launchDir,omitempty"`
	// ClosingIssues are the issue numbers the supervised PR closes — the
	// ticket half of BlocksClose's join key (#787).
	ClosingIssues []int `json:"closingIssues,omitempty"`
	// CIStatus is the collapsed CI verdict for the supervised PR: "green",
	// "failing", "pending", or "" when unknown (no checks at all, or no tick
	// has completed yet). Only "failing"/"pending" hold a window open (#787).
	CIStatus string `json:"ciStatus,omitempty"`

	// Automerge fields (#824). The supervisor's detached mode sets
	// cmd.Stdout = nil, so the automerge decision log line only reaches a
	// terminal under --once -- these persist the decision into the state
	// file so it survives that, for both `cenci babysit status` and
	// debugging.
	AutomergeDecision string `json:"automergeDecision,omitempty"`
	AutomergeReason   string `json:"automergeReason,omitempty"`
	// AutomergeDetail is optional, purely diagnostic context layered onto
	// AutomergeReason -- e.g. a rejected merge's captured `gh` output, or a
	// wrapped policy/labels/allowed-methods fetch error's message. It never
	// replaces AutomergeReason's stable reason-constant contract.
	AutomergeDetail     string            `json:"automergeDetail,omitempty"`
	AutomergeConditions []conditionResult `json:"automergeConditions,omitempty"`
	AutomergeCheckedAt  time.Time         `json:"automergeCheckedAt,omitempty"`
	// AutomergeFailureClass is the orthogonal "cause" axis to
	// AutomergeReason's "site" axis (#886): AutomergeReason says which stage
	// of the condition chain (or which upstream read) held or failed,
	// AutomergeFailureClass says what kind of underlying `gh` failure
	// produced it (command/timeout/cancelled/truncated/parse), when the hold
	// stemmed from a `gh` failure at all. recordDecision assigns it
	// unconditionally on every call so a stale class from a previous failed
	// tick never survives into a later clean tick's persisted state.
	AutomergeFailureClass string `json:"automergeFailureClass,omitempty"`
}
type prFile struct {
	Path string `json:"path"`
}
type prView struct {
	Number                  int                    `json:"number"`
	Title                   string                 `json:"title"`
	State                   string                 `json:"state"`
	HeadRefName             string                 `json:"headRefName"`
	HeadRefOID              string                 `json:"headRefOid"`
	BaseRefName             string                 `json:"baseRefName"`
	URL                     string                 `json:"url"`
	MergedAt                *time.Time             `json:"mergedAt"`
	ClosingIssuesReferences []struct{ Number int } `json:"closingIssuesReferences"`
	Mergeable               string                 `json:"mergeable"`
	IsDraft                 bool                   `json:"isDraft"`
	ChangedFiles            int                    `json:"changedFiles"`
	Additions               int                    `json:"additions"`
	Deletions               int                    `json:"deletions"`
	Files                   []prFile               `json:"files"`
}

// prViewFields is the --json field set every `gh pr view` call in this
// package requests -- shared by tick's own fetch and merge.go's post-merge
// verification refetch so both decode the identical prView shape.
const prViewFields = "number,title,state,headRefName,headRefOid,mergedAt,closingIssuesReferences,url,baseRefName,mergeable,isDraft,changedFiles,additions,deletions,files"

type check struct{ Bucket, Name, State string }
type comment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	User      struct{ Login string }
}

type review struct {
	ID          int64  `json:"id"`
	State       string `json:"state"`
	SubmittedAt string `json:"submitted_at"`
	User        struct{ Login string }
}

var errNeedsInput = errors.New("human input required")

var command = func(name string, args ...string) ([]byte, error) { return exec.Command(name, args...).CombinedOutput() }

// startSupervisor is a test seam over the detached supervisor child's
// process start (#975), mirroring the package's existing command/
// processOwned seam shape (a var pointing at a default func, restorable via
// t.Cleanup).
var startSupervisor = defaultStartSupervisor

func defaultStartSupervisor(cmd *exec.Cmd) error {
	return cmd.Start()
}

// resolveLaunchTarget resolves the tmux session and start directory every
// launch() call will target, at arm time (#975): an explicit Options field
// wins over ambient resolution, mirroring run.Opts's own "flag > current
// tmux session" precedence -- so a detached child or a `--once` invocation
// that already carries --session/--dir never re-resolves. Dir falls back
// from an explicit flag to `git rev-parse --show-toplevel` to os.Getwd(),
// computed independently of (and stored separately from) RepoRoot. When no
// session can be resolved at all (armed outside tmux), arming must still
// succeed -- this only warns to stderr and returns an empty session, which
// launch() later gates on (AC 6).
func resolveLaunchTarget(o Options) (session, dir string) {
	session = strings.TrimSpace(o.Session)
	if session == "" {
		if s, err := currentTmuxSession(); err == nil {
			session = s
		} else {
			fmt.Fprintf(os.Stderr, "cenci babysit: %v -- no repair window can be opened until you re-arm from inside a tmux pane\n", err)
		}
	}
	dir = strings.TrimSpace(o.Dir)
	if dir == "" {
		dir = gitToplevel()
		if dir == "" {
			if wd, err := os.Getwd(); err == nil {
				dir = wd
			}
		}
	}
	return session, dir
}

// gitToplevel runs `git rev-parse --show-toplevel` via the package's command
// seam, returning "" on any failure -- the shared implementation behind both
// localRepoRoot and resolveLaunchTarget's dir fallback, which both need the
// checkout root and both already tolerate a "" result (localRepoRoot's own
// documented fail-open contract, and resolveLaunchTarget's further
// os.Getwd() fallback).
func gitToplevel() string {
	out, err := command("git", "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// logPath is the detached supervisor's per-repo/PR stdout/stderr log path
// (#975), sharing statePath's hash-prefix convention: one per repo/PR,
// keeping the repo name off disk, and staying outside BlocksClose's *.json
// glob (auto-adopted answer #6).
func logPath(dir, repo, pr string) string {
	sum := sha256.Sum256([]byte(repo))
	return filepath.Join(dir, hex.EncodeToString(sum[:6])+"-"+pr+".log")
}

// openSupervisorLog opens (creating if needed) the detached supervisor's
// append-mode, 0600 log file. O_CREATE's mode argument only applies at
// creation, so an explicit Chmod normalizes a pre-existing looser-permission
// file to 0600 too (AC 5).
func openSupervisorLog(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func Run(o Options) error {
	if o.Interval < time.Minute {
		o.Interval = time.Minute
	}
	if o.Interval > time.Hour {
		o.Interval = time.Hour
	}
	if _, err := strconv.Atoi(o.PR); err != nil {
		return fmt.Errorf("PR must be a number")
	}
	repo, err := repository()
	if err != nil {
		return err
	}
	dir, err := stateDir(o.StateDir)
	if err != nil {
		return err
	}
	path := statePath(dir, repo, o.PR)
	lockPath := path + ".lock"
	if !o.Once && os.Getenv("CENCI_BABYSIT_SUPERVISOR") == "" {
		if owner, err := os.ReadFile(lockPath); err == nil {
			return fmt.Errorf("supervisor already running for PR #%s (%s)", o.PR, strings.TrimSpace(string(owner)))
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return err
		}
		session, launchDir := resolveLaunchTarget(o)
		lp := logPath(dir, repo, o.PR)
		logFile, err := openSupervisorLog(lp)
		if err != nil {
			return fmt.Errorf("open supervisor log %s: %w", lp, err)
		}
		cmd := exec.Command(os.Args[0], "babysit", o.PR, "--agent", o.Agent, "--interval", o.Interval.String(), "--state-dir", dir, "--session", session, "--dir", launchDir)
		cmd.Env = append(os.Environ(), "CENCI_BABYSIT_SUPERVISOR=1")
		cmd.Stdin = nil
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := startSupervisor(cmd); err != nil {
			_ = logFile.Close()
			return fmt.Errorf("start supervisor: %w", err)
		}
		// The parent's own fd is no longer needed once the child inherits it
		// across Start(); the child keeps writing to the same underlying file
		// via its own inherited descriptor.
		_ = logFile.Close()
		pid := 0
		if cmd.Process != nil {
			pid = cmd.Process.Pid
		}
		fmt.Printf("Babysitting PR #%s in the background (pid %d). Supervisor log: %s\n", o.PR, pid, lp)
		return nil
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("supervisor already owns PR #%s; stop it before using --once", o.PR)
		}
		return err
	}
	_, _ = fmt.Fprintf(lock, "%d\n", os.Getpid())
	_ = lock.Close()
	defer func() {
		_ = os.Remove(lockPath)
	}()
	s := load(path)
	if s.Repo != "" && (s.Repo != repo || s.Agent != o.Agent) {
		return errors.New("existing supervisor state belongs to different repository or agent")
	}
	s.PR = o.PR
	s.SchemaVersion = stateSchemaVersion
	s.Repo = repo
	s.Agent = o.Agent
	s.IntervalSeconds = int64(o.Interval.Seconds())
	if s.CurrentDelaySeconds == 0 {
		s.CurrentDelaySeconds = s.IntervalSeconds
	}
	s.PID = os.Getpid()
	s.Status = "running"
	s.RepoRoot = localRepoRoot()
	s.LaunchSession, s.LaunchDir = resolveLaunchTarget(o)
	// Persist once *before* the first poll: `cenci close` reads this file to
	// decide whether a supervisor still owns the ticket, and without an eager
	// save there is an arm-to-first-poll window (a full interval wide) in
	// which the supervisor is live but invisible to the guard (#787).
	s.UpdatedAt = time.Now().UTC()
	if err := save(path, s); err != nil {
		return err
	}
	for {
		terminal, delay, err := tick(&s)
		s.UpdatedAt = time.Now().UTC()
		if errors.Is(err, errNeedsInput) {
			s.PID = 0
			return save(path, s)
		}
		if err != nil {
			s.Status = "retrying"
			s.CurrentDelaySeconds *= 2
			if s.CurrentDelaySeconds < 60 {
				s.CurrentDelaySeconds = 60
			}
			if s.CurrentDelaySeconds > 3600 {
				s.CurrentDelaySeconds = 3600
			}
			_ = save(path, s)
			if o.Once {
				s.PID = 0
				return err
			}
			time.Sleep(time.Duration(s.CurrentDelaySeconds) * time.Second)
			continue
		}
		if terminal {
			_ = os.Remove(path)
			return nil
		}
		if err := save(path, s); err != nil {
			return err
		}
		if o.Once {
			return nil
		}
		time.Sleep(delay)
	}
}

func tick(s *State) (bool, time.Duration, error) {
	var pr prView
	if err := ghJSON(&pr, "pr", "view", s.PR, "--repo", s.Repo, "--json", prViewFields); err != nil {
		recordUpstreamReadFailure(s, reasonUpstreamPRUnreadable, err)
		return false, 0, err
	}
	if pr.State == "MERGED" || pr.State == "CLOSED" {
		if pr.State == "MERGED" {
			_, _, _ = execGh("label", "create", "Implemented", "--repo", s.Repo, "--color", "6F42C1", "--description", "PR merged — done")
			for _, i := range pr.ClosingIssuesReferences {
				if _, stderr, err := execGh("issue", "edit", strconv.Itoa(i.Number), "--repo", s.Repo, "--add-label", "Implemented", "--remove-label", "In Review"); err != nil {
					return false, 0, fmt.Errorf("label issue #%d: %s: %w", i.Number, strings.TrimSpace(stderr), err)
				}
			}
		}
		fmt.Printf("PR #%s %s: %s %s\n", s.PR, strings.ToLower(pr.State), pr.Title, pr.URL)
		return true, 0, nil
	}
	var checks []check
	if err := ghJSON(&checks, "pr", "checks", s.PR, "--repo", s.Repo, "--json", "bucket,name,state"); err != nil {
		recordUpstreamReadFailure(s, reasonUpstreamChecksUnreadable, err)
		return false, 0, err
	}
	// Publish the close guard's join key and verdict from data this tick
	// already fetched — no extra API calls (#787).
	s.ClosingIssues = nil
	for _, i := range pr.ClosingIssuesReferences {
		s.ClosingIssues = append(s.ClosingIssues, i.Number)
	}
	s.CIStatus = ciStatus(checks)
	actionable := false
	var failing []string
	for _, c := range checks {
		if c.Bucket == "fail" {
			failing = append(failing, c.Name)
		}
	}
	if len(failing) > 0 && pr.HeadRefOID != s.LastHeadSHA {
		if s.FixAttempts >= fixCap {
			s.Status = "needs-input"
			if err := launch(s, "babysit-attention", s.PR+" CI retry cap reached; decide whether to retry, pause, or stop"); err != nil {
				// One-decision-per-tick (Decision 7, #854): without this, a
				// failed workflow dispatch on an enabled automerge tick
				// returned tick's error with no automerge decision recorded
				// at all, leaving a stale decision from the previous tick
				// displayed.
				recordUpstreamReadFailure(s, reasonWorkflowLaunchFailed, err)
				return false, 0, err
			}
			return false, 0, errNeedsInput
		} else {
			prompt := fmt.Sprintf("PR #%s (%s) has failing CI checks: %s. Diagnose, fix, test, commit, and push without force-pushing.", s.PR, pr.HeadRefName, strings.Join(failing, ", "))
			if err := launch(s, "ci-repair", prompt); err != nil {
				recordUpstreamReadFailure(s, reasonWorkflowLaunchFailed, err)
				return false, 0, err
			}
			s.FixAttempts++
			s.RepairPending = true
		}
		s.LastHeadSHA = pr.HeadRefOID
		actionable = true
	} else if pr.HeadRefOID != s.LastHeadSHA {
		s.FixAttempts = 0
		s.RepairPending = false
		s.LastHeadSHA = pr.HeadRefOID
	}
	// Fully paginated (#854): fetchPaged follows every page up to
	// maxFeedbackPages, so a PR with more comments than one page no longer
	// silently misses them; commentsComplete records whether the traversal
	// actually proved completeness (a short/empty terminating page) or hit
	// the page cap while still full-sized.
	comments, commentsComplete, err := fetchPaged[comment]("repos/" + s.Repo + "/pulls/" + s.PR + "/comments")
	if err != nil {
		recordUpstreamReadFailure(s, reasonUpstreamCommentsUnreadable, err)
		return false, 0, err
	}
	// Fully paginated (#854): reviewsComplete feeds feedbackState.ReviewsComplete
	// (feedback.go), replacing #850's reviewsPageSize length tripwire with the
	// actual completeness signal from the traversal.
	reviews, reviewsComplete, err := fetchPaged[review]("repos/" + s.Repo + "/pulls/" + s.PR + "/reviews")
	if err != nil {
		recordUpstreamReadFailure(s, reasonUpstreamReviewsUnreadable, err)
		return false, 0, err
	}
	keys, newest := detectNewFeedbackKeys(s, comments, reviews)
	if len(keys) > 0 {
		s.PendingKeys = append(s.PendingKeys, keys...)
		s.PendingCommentAt = newest
		s.PendingHeadSHA = pr.HeadRefOID
		actionable = true
	}
	// #850/#885: re-fetch authoritative review-feedback state at the end of
	// tick, after this tick's new keys are recorded, and immediately before
	// deciding what (if anything) to (re)launch -- runs unconditionally,
	// regardless of whether automerge itself is enabled, since the
	// launch-dedup/AddressedKeys bookkeeping it performs matters independent
	// of automerge. reconcileFeedback also reclassifies every previously-
	// addressed key against fresh GitHub state (#885), so a reopened thread
	// or review is caught here even though its key already lives in
	// AddressedKeys; verdict.Reopened names any such key.
	verdict := reconcileFeedback(s, reviews, reviewsComplete)
	if len(verdict.Reopened) > 0 {
		actionable = true
	}
	// #885: the single per-tick address-review launch trigger, driven by
	// PendingKeys \ LaunchedKeys -- covers both this tick's brand-new keys
	// and any key reconcileFeedback just reopened above, exactly once per
	// resolution episode (Decision 3). A launch failure still leaves the
	// keys recorded as pending (merge safety must not depend on launch
	// success) but out of LaunchedKeys, so the very next tick retries --
	// matching today's effectively-unbounded retry behavior, no
	// fixCap-style cap.
	if toLaunch := removeKeys(s.PendingKeys, s.LaunchedKeys); len(toLaunch) > 0 {
		if err := launch(s, "address-review", s.PR); err != nil {
			recordUpstreamReadFailure(s, reasonWorkflowLaunchFailed, err)
			return false, 0, err
		}
		s.LaunchedKeys = append(s.LaunchedKeys, toLaunch...)
		actionable = true
	}
	if runAutomerge(s, pr, checks, verdict, commentsComplete, reviewsComplete) {
		actionable = true
	}
	if actionable {
		s.CurrentDelaySeconds = s.IntervalSeconds
	} else {
		s.CurrentDelaySeconds *= 2
		if s.CurrentDelaySeconds > 3600 {
			s.CurrentDelaySeconds = 3600
		}
		fmt.Printf("PR #%s quiet — no new actionable work. Next check in ~%dm (backing off).\n", s.PR, s.CurrentDelaySeconds/60)
	}
	return false, time.Duration(s.CurrentDelaySeconds) * time.Second, nil
}

// launch dispatches workflow via a self-exec of `cenci run`, targeting the
// tmux session recorded at arm time (#975) rather than inheriting whatever
// $TMUX_PANE/cwd happen to be live when this tick's launch actually fires --
// the arming pane can be long gone by then. An empty recorded session (armed
// outside tmux) fails immediately with no probe and no `cenci run` call; a
// recorded session that no longer exists fails with an error naming it,
// again issuing zero `cenci run` calls -- never falling back to another
// session, never creating one (ticket Decision). The probe-error and
// session-absent branches are kept as separate returns
// (watch/docs/error-handling.md's rule against collapsing "probe errored"
// into "condition false").
func launch(s *State, workflow, arg string) error {
	if s.LaunchSession == "" {
		return fmt.Errorf("launch %s: no tmux session was recorded at arm time; re-arm from a host tmux pane", workflow)
	}
	exists, err := tmuxHasSession(s.LaunchSession)
	if err != nil {
		return fmt.Errorf("launch %s: checking tmux session %q: %w", workflow, s.LaunchSession, err)
	}
	if !exists {
		return fmt.Errorf("launch %s: recorded tmux session %q no longer exists", workflow, s.LaunchSession)
	}
	args := []string{"run", workflow, arg, "--agent", s.Agent, "--session", s.LaunchSession}
	if s.LaunchDir != "" {
		args = append(args, "--dir", s.LaunchDir)
	}
	out, err := command(os.Args[0], args...)
	if err != nil {
		return fmt.Errorf("launch %s: %s: %w", workflow, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// detectNewFeedbackKeys is the pure, read-only new-feedback detection
// predicate, extracted out of tick's own inline loop so both tick and the
// pre-merge re-check (merge.go's recheckAutomergeInputs) share exactly one
// implementation (#854's rejected "reuse reconcileFeedback" alternative:
// that function mutates State and re-fetches GraphQL thread
// resolution, risking double-bookkeeping and unable to detect feedback that
// landed strictly between this tick's own fetch and the merge attempt).
// Given s's already-recorded AddressedKeys/PendingKeys and LastCommentAt, it
// reports every comment/review key in comments/reviews not already seen,
// ignoring bot authors, plus the newest timestamp found (s.LastCommentAt
// when nothing new was found) -- identical to tick's pre-#854 inline
// computation, just factored out; it mutates nothing.
func detectNewFeedbackKeys(s *State, comments []comment, reviews []review) (keys []string, newest string) {
	seen := map[string]bool{}
	for _, key := range append(append([]string{}, s.AddressedKeys...), s.PendingKeys...) {
		seen[key] = true
	}
	newest = s.LastCommentAt
	for _, c := range comments {
		ts := c.UpdatedAt
		if ts == "" {
			ts = c.CreatedAt
		}
		key := "comment:" + strconv.FormatInt(c.ID, 10)
		if !seen[key] && ts > s.LastCommentAt && !strings.HasSuffix(c.User.Login, "[bot]") {
			keys = append(keys, key)
			if ts > newest {
				newest = ts
			}
		}
	}
	for _, r := range reviews {
		key := "review:" + strconv.FormatInt(r.ID, 10)
		if r.State == "CHANGES_REQUESTED" && !seen[key] && r.SubmittedAt > s.LastCommentAt && !strings.HasSuffix(r.User.Login, "[bot]") {
			keys = append(keys, key)
			if r.SubmittedAt > newest {
				newest = r.SubmittedAt
			}
		}
	}
	return keys, newest
}

// ghJSON decodes dst from `gh <args...>`'s stdout. Strict (#886): any
// non-nil execGh error -- a nonzero exit, a bounded-output truncation
// (errGhOutputTruncated), a timeout, a cancellation -- fails closed
// unconditionally, before dst is ever decoded, even when stdout still
// happens to be complete, valid JSON. This removes the previous "gh pr
// checks exits 8 while checks are pending, but stdout still decodes, so
// treat it as success" carve-out entirely: a caller that wants to
// distinguish a genuine `gh` failure from a checks-pending exit must do so
// itself, not rely on ghJSON to paper over it (watch/docs/error-handling.md's
// default-deny rule). Only once execGh itself reported success does ghJSON
// attempt to decode; a decode failure there is wrapped with errGhDecode so
// classifyGhFailure resolves it to failureClassParse.
func ghJSON(dst any, args ...string) error {
	stdout, stderr, err := execGh(args...)
	if err != nil {
		return fmt.Errorf("gh %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(stderr), err)
	}
	if decodeErr := json.Unmarshal([]byte(stdout), dst); decodeErr != nil {
		return fmt.Errorf("gh %s: decode: %w", strings.Join(args, " "), errors.Join(decodeErr, errGhDecode))
	}
	return nil
}
func repository() (string, error) {
	stdout, stderr, err := execGh("repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	if err != nil {
		return "", fmt.Errorf("resolve repository: %s: %w", strings.TrimSpace(stderr), err)
	}
	r := strings.TrimSpace(stdout)
	if !strings.Contains(r, "/") {
		return "", errors.New("could not resolve owner/repository")
	}
	return r, nil
}
func stateDir(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		h, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		base = filepath.Join(h, ".local", "state")
	}
	return filepath.Join(base, "cenci", "babysit"), nil
}
func statePath(dir, repo, pr string) string {
	sum := sha256.Sum256([]byte(repo))
	return filepath.Join(dir, hex.EncodeToString(sum[:6])+"-"+pr+".json")
}
func load(path string) State {
	var s State
	b, e := os.ReadFile(path)
	if e == nil {
		_ = json.Unmarshal(b, &s)
	}
	// Schema migration (#885): a state persisted below stateSchemaVersion
	// predates LaunchedKeys -- its PendingKeys already represents in-flight
	// feedback dispatched under the old AddressedKeys-as-dedup scheme, so
	// seed LaunchedKeys from it here, before Run() (or any other caller)
	// ever overwrites s.SchemaVersion, so an upgraded supervisor with
	// in-flight feedback does not fire one spurious address-review relaunch
	// for work already dispatched. Both the standalone `load()` callers
	// (BlocksClose, `cenci babysit status`) and Run()'s own startup load see
	// the migrated value.
	if s.SchemaVersion < stateSchemaVersion {
		if len(s.PendingKeys) > 0 {
			s.LaunchedKeys = append(append([]string{}, s.LaunchedKeys...), s.PendingKeys...)
		}
		s.SchemaVersion = stateSchemaVersion
	}
	return s
}
func save(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func Stop(pr, explicit string) error {
	dir, err := stateDir(explicit)
	if err != nil {
		return err
	}
	repo, err := repository()
	if err != nil {
		return err
	}
	cleanPR := strings.TrimPrefix(pr, "#")
	path := statePath(dir, repo, cleanPR)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("no supervisor found for PR #%s", pr)
	}
	s := load(path)
	if s.PID > 0 {
		if !processOwned(s.PID, cleanPR) {
			return fmt.Errorf("refusing to signal pid %d: it is not the recorded cenci babysit process", s.PID)
		}
		proc, e := os.FindProcess(s.PID)
		if e != nil {
			return e
		}
		if e = proc.Signal(syscall.SIGTERM); e != nil {
			return fmt.Errorf("stop pid %d: %w", s.PID, e)
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Printf("Stopped babysitting PR #%s.\n", cleanPR)
	return nil
}

// processOwned is a test seam over defaultProcessOwned, mirroring the
// package's existing `command` seam: BlocksClose's decision matrix must be
// testable without spawning real supervisor processes.
var processOwned = defaultProcessOwned

func defaultProcessOwned(pid int, pr string) bool {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		// Where procfs is readable (Linux), an unreadable cmdline means the
		// pid is gone or hidden — stay strict, since Stop signals on this
		// answer and a recycled pid must never be signalled. Where procfs is
		// unavailable *entirely* (no /proc mount, non-Linux), ownership
		// cannot be established at all and an unconditional false would make
		// BlocksClose a silent no-op, so fall back to a liveness-only probe
		// there (#787).
		if procfsReadable() {
			return false
		}
		return syscall.Kill(pid, 0) == nil
	}
	cmdline := strings.ReplaceAll(string(b), "\x00", " ")
	return strings.Contains(cmdline, "babysit") && strings.Contains(cmdline, pr)
}

// procfsReadable reports whether this process can read its own procfs cmdline
// — i.e. whether /proc is mounted and readable for the caller at all.
func procfsReadable() bool {
	_, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(os.Getpid()), "cmdline"))
	return err == nil
}

// localRepoRoot resolves the supervised checkout's root without touching the
// network. "" when it cannot be resolved, which makes the guard's repo
// comparison fail open rather than block on an unknown repo (#787).
func localRepoRoot() string {
	return gitToplevel()
}

// ciStatus collapses a PR's check buckets into the guard's verdict: any
// failing check wins, then any pending one, otherwise green. A PR with no
// checks at all reports "" (unknown), which never holds a window open — a
// repo without CI must not wedge its windows open forever (#787).
func ciStatus(checks []check) string {
	if len(checks) == 0 {
		return ""
	}
	pending := false
	for _, c := range checks {
		switch c.Bucket {
		case "fail":
			return "failing"
		case "pending":
			pending = true
		}
	}
	if pending {
		return "pending"
	}
	return "green"
}

// BlocksClose reports whether a live supervisor owns a PR that closes the
// given ticket with CI not yet green, plus a human-readable reason for the
// skip line. It is the read side of the `cenci close` × `cenci babysit` join
// (#787): closing an agent's window while its PR is still being supervised
// destroys work in progress, since the ticket is "In Review", not
// "Implemented", until babysit says so.
//
// repoRoot scopes the answer to one checkout; an empty repoRoot on either
// side (caller or state file) skips the comparison rather than rejecting the
// match, so an unresolvable repo root degrades to a ticket-only match instead
// of silently disabling the guard. stateDirOverride mirrors babysit's
// --state-dir; "" uses the standard location.
//
// Every failure — missing directory, unreadable or corrupt state file, no
// procfs — fails *open* (returns false), and nothing is ever written to
// stdout: this runs on every lazyboards board refresh, and a guard that
// errored into "never close anything" would be worse than the bug it fixes.
// It makes no network calls for the same reason.
func BlocksClose(ticket, repoRoot, stateDirOverride string) (bool, string) {
	number, err := strconv.Atoi(strings.TrimPrefix(ticket, "#"))
	if err != nil {
		return false, ""
	}
	dir, err := stateDir(stateDirOverride)
	if err != nil {
		return false, ""
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return false, ""
	}
	for _, p := range paths {
		s := load(p) // corrupt/unreadable decodes to the zero State: no match
		if !closesIssue(s, number) {
			continue
		}
		if repoRoot != "" && s.RepoRoot != "" && repoRoot != s.RepoRoot {
			continue
		}
		if s.CIStatus != "failing" && s.CIStatus != "pending" {
			continue
		}
		if !supervisorLive(s) {
			continue
		}
		return true, fmt.Sprintf("babysit supervising PR #%s, CI not green", s.PR)
	}
	return false, ""
}

// closesIssue reports whether s's supervised PR closes issue number.
func closesIssue(s State, number int) bool {
	for _, i := range s.ClosingIssues {
		if i == number {
			return true
		}
	}
	return false
}

// supervisorLive reports whether the supervisor described by s is still on
// the hook for its PR. A running supervisor is identified by its own pid; a
// supervisor paused for human input deliberately zeroes its pid (see Run's
// errNeedsInput branch) but has *not* finished the work, so its window must
// stay open too (#787).
func supervisorLive(s State) bool {
	if s.PID > 0 && processOwned(s.PID, s.PR) {
		return true
	}
	return s.Status == "needs-input"
}
