package incident

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ContainerAgent runs one fresh CLI per phase. History is a read-only bare clone;
// neither the main checkout, Git credential store, Azure credentials, runtime
// socket nor user's home are mounted. Agent network access requires a dedicated
// internal network whose proxy permits only the model API.
type ContainerAgent struct {
	Config  Config
	ownerID string // captured for this runtime invocation, including deferred cleanup
}

// Public runtime entry points resolve the directory after WorkerLock has created
// it. Keep the canonical copy local: concurrent jobs must not mutate Config.
func (c *ContainerAgent) canonicalState() (*ContainerAgent, error) {
	if !filepath.IsAbs(c.Config.StateDir) {
		return nil, fmt.Errorf("incident state directory must be absolute")
	}
	dir, err := filepath.EvalSymlinks(c.Config.StateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve incident state directory: %w", err)
	}
	copy := *c
	copy.Config.StateDir = dir
	copy.ownerID, err = persistentOwner(dir)
	if err != nil {
		return nil, err
	}
	return &copy, nil
}

// A durable identity follows the actual directory across all filesystem aliases,
// including case-insensitive aliases that EvalSymlinks cannot canonicalize.
// Serialize initialization with the ledger lock so simultaneous callers cannot
// launch containers under different IDs. Never replace an unreadable/invalid ID.
func persistentOwner(dir string) (string, error) {
	path := filepath.Join(dir, "owner-id")
	// Existing identities are atomically written and never updated. Reading one
	// must not depend on ledger health: startup stops orphans before recovery.
	owner, err := readOwner(path)
	if err == nil {
		return owner, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("incident owner identity: %w", err)
	}
	err = NewStore(dir).transaction(false, func(_ map[string]*Incident) error {
		// Another caller may have initialized it while we waited for the lock.
		var err error
		owner, err = readOwner(path)
		if !os.IsNotExist(err) {
			return err
		}
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return err
		}
		owner = hex.EncodeToString(random[:])
		return durableFile(path, []byte(owner))
	})
	if err != nil {
		return "", fmt.Errorf("incident owner identity: %w", err)
	}
	return owner, nil
}

func readOwner(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	id := string(b)
	if _, err := hex.DecodeString(id); err != nil || len(id) != 24 || id != strings.ToLower(id) {
		return "", fmt.Errorf("invalid persisted value")
	}
	return id, nil
}

func (c *ContainerAgent) owner() string { return c.ownerID }
func (c *ContainerAgent) args(name, dir, history string, readonly bool) []string {
	mode := ":rw"
	if readonly {
		mode = ":ro"
	}
	metadata := filepath.Dir(history)
	args := []string{"run", "--rm", "-i", "--name", name, "--label", "cenci.incident.owner=" + c.owner(), "--label", "cenci.incident.job=" + name, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=128", "--memory=2g", "--cpus=2", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()), "--tmpfs", "/tmp:rw,nosuid,size=512m,mode=1777", "--env", "HOME=/tmp", "--workdir", dir, "--volume", dir + ":" + dir + mode, "--volume", filepath.Join(metadata, "container.git") + ":" + dir + "/.git:ro", "--volume", history + ":/history:ro"}
	if c.Config.Agent.Runtime == "podman" {
		args = append(args, "--userns=keep-id")
	}
	return args
}

type CleanupFailure struct{ Err error }

func (e *CleanupFailure) Error() string { return "incident container cleanup failed: " + e.Err.Error() }
func (e *CleanupFailure) Unwrap() error { return e.Err }

func (c *ContainerAgent) remove(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ids, err := commandDeadline(ctx, "", nil, nil, c.Config.Agent.Runtime, "ps", "-aq", "--filter", "label=cenci.incident.owner="+c.owner(), "--filter", "label=cenci.incident.job="+name)
	if err != nil {
		return &CleanupFailure{err}
	}
	for _, id := range strings.Fields(ids) {
		if !containerID.MatchString(id) {
			return &CleanupFailure{fmt.Errorf("invalid container id")}
		}
		if _, err := commandDeadline(ctx, "", nil, nil, c.Config.Agent.Runtime, "rm", "-f", id); err != nil {
			return &CleanupFailure{err}
		}
	}
	return nil
}

// Cleanup is only invoked after acquiring the lifetime worker lock. A crashed
// worker may leave a container running; it must be stopped before reclaiming jobs.
func (c *ContainerAgent) Cleanup(ctx context.Context) error {
	canonical, err := c.canonicalState()
	if err != nil {
		return err
	}
	c = canonical
	ids, err := command(ctx, "", nil, nil, c.Config.Agent.Runtime, "ps", "-aq", "--filter", "label=cenci.incident.owner="+c.owner())
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(ids) {
		if !containerID.MatchString(id) {
			return fmt.Errorf("invalid container id")
		}
		if _, err := command(ctx, "", nil, nil, c.Config.Agent.Runtime, "rm", "-f", id); err != nil {
			return err
		}
	}
	return nil
}

const resultSchema = `{"type":"object","properties":{"verdict":{"type":"string","enum":["fix","review"]},"report":{"type":"string","minLength":1}},"required":["verdict","report"],"additionalProperties":false}`

func (c *ContainerAgent) Run(ctx context.Context, req AgentRequest) (result AgentResult, resultErr error) {
	canonical, err := c.canonicalState()
	if err != nil {
		return AgentResult{}, err
	}
	c = canonical
	network, err := command(ctx, "", nil, nil, c.Config.Agent.Runtime, "network", "inspect", "--format", "{{.Internal}}", c.Config.Agent.Network)
	if err != nil {
		return AgentResult{}, err
	}
	if network != "true" {
		return AgentResult{}, fmt.Errorf("agent network must be internal; production metadata endpoints must be unreachable")
	}
	if os.Getenv(c.Config.Agent.APIKeyEnv) == "" {
		return AgentResult{}, fmt.Errorf("agent API key is unavailable")
	}
	name := "cenci-incident-" + c.owner() + "-" + req.Incident.Key[:12] + "-" + req.Phase
	args := c.args(name, req.Worktree, req.History, req.Phase == "investigate")
	defer func() { resultErr = errors.Join(resultErr, c.remove(name)) }()
	args = append(args, "--network", c.Config.Agent.Network, "--env", "HTTPS_PROXY="+c.Config.Agent.ProxyURL, "--env", "HTTP_PROXY="+c.Config.Agent.ProxyURL, "--env", c.Config.Agent.APIKeyEnv, "--volume", req.DeployedTree+":/deployed:ro", "--entrypoint", "claude", c.Config.Agent.Image, "-p", "--restricted", "--add-dir", "/history", "/deployed", "--output-format", "json", "--no-session-persistence", "--setting-sources", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--tools", "Read,Glob,Grep,Edit,Write", "--allowedTools", "Read,Glob,Grep,Edit,Write", "--max-budget-usd", strconv.FormatFloat(req.BudgetUSD, 'f', 6, 64), "--max-turns", "40", "--json-schema", resultSchema, "--append-system-prompt", req.Instructions)
	payload, err := json.Marshal(req)
	if err != nil {
		return AgentResult{}, err
	}
	out, err := commandDeadline(ctx, "", nil, strings.NewReader(string(payload)), c.Config.Agent.Runtime, args...)
	if err != nil {
		return AgentResult{}, err
	}
	var envelope struct {
		Subtype string      `json:"subtype"`
		Error   bool        `json:"is_error"`
		Cost    *float64    `json:"total_cost_usd"`
		Result  AgentResult `json:"structured_output"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		return AgentResult{}, fmt.Errorf("agent returned invalid result: %w", err)
	}
	if envelope.Error || envelope.Subtype != "success" || envelope.Cost == nil || *envelope.Cost < 0 || *envelope.Cost > req.BudgetUSD || (envelope.Result.Verdict != "fix" && envelope.Result.Verdict != "review") || envelope.Result.Report == "" {
		return AgentResult{}, fmt.Errorf("agent failed, exceeded budget, or returned incomplete result")
	}
	return envelope.Result, nil
}

// Required checks execute without network or model credentials. All dependencies
// and caches must be present in the operator's prebuilt image.
func (c *ContainerAgent) RunCheck(ctx context.Context, dir string, argv []string) (output string, resultErr error) {
	canonical, err := c.canonicalState()
	if err != nil {
		return "", &CheckUnavailable{err}
	}
	c = canonical
	name := "cenci-incident-" + c.owner() + "-check-" + Key(dir)[:12]
	defer func() { resultErr = errors.Join(resultErr, c.remove(name)) }()
	key := strings.TrimPrefix(filepath.Base(dir), "incident-")
	history := filepath.Join(c.Config.StateDir, "artifacts", key, "history.git")
	args := c.args(name, dir, history, false)
	args = append(args, "--network", "none", "--entrypoint", argv[0], c.Config.Agent.Image)
	args = append(args, argv[1:]...)
	output, resultErr = commandDeadline(ctx, "", nil, nil, c.Config.Agent.Runtime, args...)
	var exit *exec.ExitError
	if errors.As(resultErr, &exit) && (exit.ExitCode() == 125 || exit.ExitCode() == 126 || exit.ExitCode() == 127) {
		resultErr = &CheckUnavailable{resultErr}
	}
	return
}

type CheckUnavailable struct{ Err error }

func (e *CheckUnavailable) Error() string { return "check runtime unavailable: " + e.Err.Error() }
func (e *CheckUnavailable) Unwrap() error { return e.Err }

type ContainerChecks struct{ Agent *ContainerAgent }

func (c ContainerChecks) Run(ctx context.Context, dir string, argv []string) (string, error) {
	return c.Agent.RunCheck(ctx, dir, argv)
}
