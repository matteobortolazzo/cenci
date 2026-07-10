package dispatch

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/claude-tools/agentwatch/pkg/watch"
)

// TicketMutator applies the reconciler's gh side effects. The seam keeps
// RunReconcileOnce testable without shelling out.
type TicketMutator interface {
	EditLabels(repo string, number int, add, remove []string) error
	Comment(repo string, number int, body string) error
}

// ObservationStore persists the grace-observation map between passes so grace
// survives cron invocations and daemon restarts.
type ObservationStore interface {
	Load() (map[string]time.Time, error)
	Save(map[string]time.Time) error
}

// GHMutator is the real TicketMutator; it shells out to gh, mirroring collect.go.
type GHMutator struct{}

// EditLabels adds and removes labels on an issue. A no-op call (no labels) does
// not invoke gh.
func (GHMutator) EditLabels(repo string, number int, add, remove []string) error {
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}
	args := []string{"issue", "edit", strconv.Itoa(number), "--repo", repo}
	for _, l := range add {
		args = append(args, "--add-label", l)
	}
	for _, l := range remove {
		args = append(args, "--remove-label", l)
	}
	if out, err := exec.Command("gh", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("gh issue edit #%d in %s: %w: %s", number, repo, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Comment posts a comment on an issue. The body is passed as an argv element (no
// shell), so newlines and markup need no escaping.
func (GHMutator) Comment(repo string, number int, body string) error {
	if out, err := exec.Command("gh", "issue", "comment", strconv.Itoa(number),
		"--repo", repo, "--body", body).CombinedOutput(); err != nil {
		return fmt.Errorf("gh issue comment #%d in %s: %w: %s", number, repo, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// countAttempts tallies durable attempt markers on a ticket's comment thread.
// It is called only for Working candidate tickets (few at a time), so the extra
// gh call per candidate is cheap. An error is returned rather than swallowed to
// 0: the caller must not confuse "no attempts yet" with "could not read", since
// the count gates the retry-vs-fail decision.
func countAttempts(repo string, number int) (int, error) {
	out, err := exec.Command("gh", "issue", "view", strconv.Itoa(number),
		"--repo", repo, "--json", "comments").Output()
	if err != nil {
		return 0, fmt.Errorf("gh issue view #%d in %s: %w", number, repo, err)
	}
	var v struct {
		Comments []struct {
			Body string `json:"body"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return 0, fmt.Errorf("parsing comments for #%d in %s: %w", number, repo, err)
	}
	n := 0
	for _, c := range v.Comments {
		if strings.Contains(c.Body, attemptMarker) {
			n++
		}
	}
	return n, nil
}

// stateStore persists the grace-observation map as JSON on disk.
type stateStore struct {
	path string
}

type reconcileState struct {
	Observations map[string]time.Time `json:"observations"`
}

// DefaultStatePath resolves $XDG_STATE_HOME/agentwatch/reconcile.json, falling
// back to ~/.local/state when XDG_STATE_HOME is unset.
func DefaultStatePath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "agentwatch", "reconcile.json")
}

// NewStateStore returns a disk-backed ObservationStore. An empty path resolves
// the XDG default.
func NewStateStore(path string) ObservationStore {
	if path == "" {
		path = DefaultStatePath()
	}
	return &stateStore{path: path}
}

func (s *stateStore) Load() (map[string]time.Time, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]time.Time{}, nil
		}
		return map[string]time.Time{}, err
	}
	var st reconcileState
	if err := json.Unmarshal(data, &st); err != nil {
		return map[string]time.Time{}, err
	}
	if st.Observations == nil {
		st.Observations = map[string]time.Time{}
	}
	return st.Observations, nil
}

func (s *stateStore) Save(obs map[string]time.Time) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(reconcileState{Observations: obs}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

// reconcileDeps are the collected, impure inputs to a reconcile pass. Separating
// them from RunReconcileOnce lets applyReconcile be exercised without gh.
type reconcileDeps struct {
	Tickets         []Ticket
	Plans           []Plan
	Snapshot        *watch.StateSnapshot
	Attempts        map[string]int
	AttemptsUnknown map[string]bool
	Now             time.Time
}

// RunReconcileOnce collects tickets, plans, the daemon snapshot, and durable
// attempt counts, runs the pure Reconcile engine, logs every recovery, applies
// the gh mutations (unless dryRun), and persists the grace map. The daemon
// consumes result.Failed to badge the snapshot.
func RunReconcileOnce(cfg Config, mut TicketMutator, dryRun bool, out io.Writer, store ObservationStore) ReconcileResult {
	if out == nil {
		out = os.Stdout
	}

	tickets, err := CollectTickets(cfg.Repos)
	if err != nil {
		logf(out, "reconcile: collecting tickets: %v\n", err)
	}

	var plans []Plan
	for _, rc := range cfg.Repos {
		ps, err := ReadPlans(rc.Repo, rc.Dir, nil)
		if err != nil {
			logf(out, "reconcile: reading plans in %s: %v\n", rc.Dir, err)
			continue
		}
		plans = append(plans, ps...)
	}

	snap, _ := ReadSnapshot(watch.DefaultSocketPath()) // nil on error ⇒ Reconcile never reconciles blind

	// Count durable attempts only for Working candidate tickets — the only ones
	// the failure path can act on. A gh read error marks the ticket unknown so
	// Reconcile defers rather than acting on an assumed-zero count.
	attempts := make(map[string]int)
	attemptsUnknown := make(map[string]bool)
	for _, t := range tickets {
		if !hasLabel(t.Labels, labelWorking) {
			continue
		}
		n, err := countAttempts(t.Repo, t.Number)
		if err != nil {
			logf(out, "reconcile: #%d counting attempts: %v\n", t.Number, err)
			attemptsUnknown[planKey(t.Repo, t.Number)] = true
			continue
		}
		attempts[planKey(t.Repo, t.Number)] = n
	}

	return applyReconcile(cfg, reconcileDeps{
		Tickets:         tickets,
		Plans:           plans,
		Snapshot:        snap,
		Attempts:        attempts,
		AttemptsUnknown: attemptsUnknown,
		Now:             time.Now(),
	}, mut, dryRun, out, store)
}

// applyReconcile runs the pure engine over already-collected deps, logs, applies
// (unless dryRun), and persists the next grace map. It is the testable core of
// RunReconcileOnce.
func applyReconcile(cfg Config, deps reconcileDeps, mut TicketMutator, dryRun bool, out io.Writer, store ObservationStore) ReconcileResult {
	if out == nil {
		out = os.Stdout
	}

	obs, err := store.Load()
	if err != nil {
		logf(out, "reconcile: loading state: %v\n", err)
		obs = map[string]time.Time{}
	}

	result := Reconcile(ReconcileInputs{
		Tickets:         deps.Tickets,
		Plans:           deps.Plans,
		Snapshot:        deps.Snapshot,
		Now:             deps.Now,
		Observations:    obs,
		Attempts:        deps.Attempts,
		AttemptsUnknown: deps.AttemptsUnknown,
		Config:          cfg,
	})

	for _, rec := range result.Recoveries {
		logf(out, "%s\n", formatRecovery(rec))
	}

	if dryRun {
		return result
	}

	for _, rec := range result.Recoveries {
		applyRecovery(mut, rec, out)
	}

	if err := store.Save(result.NextObservations); err != nil {
		logf(out, "reconcile: saving state: %v\n", err)
	}
	return result
}

// applyRecovery applies one recovery's gh side effects. Report-only kinds
// (orphan-plan) do nothing. If the label swap fails, the comment is skipped so a
// ticket is never annotated without the state change that explains it.
func applyRecovery(mut TicketMutator, rec Recovery, out io.Writer) {
	if len(rec.AddLabels) == 0 && len(rec.RemoveLabels) == 0 {
		return
	}
	if err := mut.EditLabels(rec.Ticket.Repo, rec.Ticket.Number, rec.AddLabels, rec.RemoveLabels); err != nil {
		logf(out, "reconcile: #%d edit labels: %v\n", rec.Ticket.Number, err)
		return
	}
	if rec.Comment != "" {
		if err := mut.Comment(rec.Ticket.Repo, rec.Ticket.Number, rec.Comment); err != nil {
			logf(out, "reconcile: #%d comment: %v\n", rec.Ticket.Number, err)
		}
	}
}

// formatRecovery renders one recovery as a single log line.
func formatRecovery(rec Recovery) string {
	switch rec.Kind {
	case RecoveryRetry:
		return fmt.Sprintf("#%d retry: %s", rec.Ticket.Number, rec.Detail)
	case RecoveryFailed:
		return fmt.Sprintf("#%d dispatch-failed: %s", rec.Ticket.Number, rec.Detail)
	case RecoveryPlanInvalid:
		return fmt.Sprintf("#%d plan-invalid: %s", rec.Ticket.Number, rec.Detail)
	case RecoveryOrphanPlan:
		return fmt.Sprintf("#%d orphan-plan (report only): %s", rec.Ticket.Number, rec.Detail)
	default:
		return fmt.Sprintf("#%d %s: %s", rec.Ticket.Number, rec.Kind, rec.Detail)
	}
}
