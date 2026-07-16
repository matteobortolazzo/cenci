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

type Options struct {
	PR, Agent, StateDir string
	Interval            time.Duration
	Once                bool
}
type State struct {
	SchemaVersion       int       `json:"schemaVersion"`
	PR                  string    `json:"pr"`
	Repo                string    `json:"repo"`
	Agent               string    `json:"agent"`
	IntervalSeconds     int64     `json:"intervalSeconds"`
	CurrentDelaySeconds int64     `json:"currentDelaySeconds"`
	LastHeadSHA         string    `json:"lastCiHeadSha,omitempty"`
	FixAttempts         int       `json:"ciFixAttempts"`
	RepairPending       bool      `json:"ciRepairPending"`
	LastCommentAt       string    `json:"lastCommentTimestamp,omitempty"`
	AddressedKeys       []string  `json:"addressedCommentKeys,omitempty"`
	PendingKeys         []string  `json:"pendingCommentKeys,omitempty"`
	PendingCommentAt    string    `json:"pendingCommentTimestamp,omitempty"`
	PendingHeadSHA      string    `json:"pendingCommentHeadSha,omitempty"`
	PID                 int       `json:"pid,omitempty"`
	Status              string    `json:"status"`
	UpdatedAt           time.Time `json:"updatedAt"`
}
type prView struct {
	Number                  int                    `json:"number"`
	Title                   string                 `json:"title"`
	State                   string                 `json:"state"`
	HeadRefName             string                 `json:"headRefName"`
	HeadRefOID              string                 `json:"headRefOid"`
	URL                     string                 `json:"url"`
	MergedAt                *time.Time             `json:"mergedAt"`
	ClosingIssuesReferences []struct{ Number int } `json:"closingIssuesReferences"`
}
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
		cmd := exec.Command(os.Args[0], "babysit", o.PR, "--agent", o.Agent, "--interval", o.Interval.String(), "--state-dir", dir)
		cmd.Env = append(os.Environ(), "CENCI_BABYSIT_SUPERVISOR=1")
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start supervisor: %w", err)
		}
		fmt.Printf("Babysitting PR #%s in the background (pid %d).\n", o.PR, cmd.Process.Pid)
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
	s.SchemaVersion = 1
	s.Repo = repo
	s.Agent = o.Agent
	s.IntervalSeconds = int64(o.Interval.Seconds())
	if s.CurrentDelaySeconds == 0 {
		s.CurrentDelaySeconds = s.IntervalSeconds
	}
	s.PID = os.Getpid()
	s.Status = "running"
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
	if err := ghJSON(&pr, "pr", "view", s.PR, "--repo", s.Repo, "--json", "number,title,state,headRefName,headRefOid,mergedAt,closingIssuesReferences,url"); err != nil {
		return false, 0, err
	}
	if pr.State == "MERGED" || pr.State == "CLOSED" {
		if pr.State == "MERGED" {
			_, _ = command("gh", "label", "create", "Implemented", "--repo", s.Repo, "--color", "6F42C1", "--description", "PR merged — done")
			for _, i := range pr.ClosingIssuesReferences {
				if out, err := command("gh", "issue", "edit", strconv.Itoa(i.Number), "--repo", s.Repo, "--add-label", "Implemented", "--remove-label", "In Review"); err != nil {
					return false, 0, fmt.Errorf("label issue #%d: %s: %w", i.Number, strings.TrimSpace(string(out)), err)
				}
			}
		}
		fmt.Printf("PR #%s %s: %s %s\n", s.PR, strings.ToLower(pr.State), pr.Title, pr.URL)
		return true, 0, nil
	}
	var checks []check
	if err := ghJSON(&checks, "pr", "checks", s.PR, "--repo", s.Repo, "--json", "bucket,name,state"); err != nil {
		return false, 0, err
	}
	actionable := false
	if len(s.PendingKeys) > 0 && pr.HeadRefOID != s.PendingHeadSHA {
		s.AddressedKeys = append(s.AddressedKeys, s.PendingKeys...)
		s.PendingKeys = nil
		s.LastCommentAt = s.PendingCommentAt
		s.PendingCommentAt, s.PendingHeadSHA = "", ""
	}
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
				return false, 0, err
			}
			return false, 0, errNeedsInput
		} else {
			prompt := fmt.Sprintf("PR #%s (%s) has failing CI checks: %s. Diagnose, fix, test, commit, and push without force-pushing.", s.PR, pr.HeadRefName, strings.Join(failing, ", "))
			if err := launch(s, "ci-repair", prompt); err != nil {
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
	var comments []comment
	if err := ghJSON(&comments, "api", "repos/"+s.Repo+"/pulls/"+s.PR+"/comments"); err != nil {
		return false, 0, err
	}
	seen := map[string]bool{}
	for _, key := range append(append([]string{}, s.AddressedKeys...), s.PendingKeys...) {
		seen[key] = true
	}
	newest := s.LastCommentAt
	var keys []string
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
	var reviews []review
	if err := ghJSON(&reviews, "api", "repos/"+s.Repo+"/pulls/"+s.PR+"/reviews"); err != nil {
		return false, 0, err
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
	if len(keys) > 0 {
		if err := launch(s, "address-review", s.PR); err != nil {
			return false, 0, err
		}
		s.PendingKeys = append(s.PendingKeys, keys...)
		s.PendingCommentAt = newest
		s.PendingHeadSHA = pr.HeadRefOID
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

func launch(s *State, workflow, arg string) error {
	out, err := command(os.Args[0], "run", workflow, arg, "--agent", s.Agent)
	if err != nil {
		return fmt.Errorf("launch %s: %s: %w", workflow, strings.TrimSpace(string(out)), err)
	}
	return nil
}
func ghJSON(dst any, args ...string) error {
	out, err := command("gh", args...)
	decodeErr := json.Unmarshal(out, dst)
	if decodeErr == nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("gh %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	if decodeErr != nil {
		return fmt.Errorf("decode gh output: %w", decodeErr)
	}
	return nil
}
func repository() (string, error) {
	out, err := command("gh", "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	if err != nil {
		return "", fmt.Errorf("resolve repository: %s: %w", strings.TrimSpace(string(out)), err)
	}
	r := strings.TrimSpace(string(out))
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

func processOwned(pid int, pr string) bool {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	cmdline := strings.ReplaceAll(string(b), "\x00", " ")
	return strings.Contains(cmdline, "babysit") && strings.Contains(cmdline, pr)
}
