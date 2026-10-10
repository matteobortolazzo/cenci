// Package incident owns durable incident scheduling. Broker transport and coding
// agents are boundaries; neither controls the job ledger or publication policy.
package incident

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	Enabled        bool            `json:"enabled"`
	StateDir       string          `json:"stateDir"`
	Namespace      string          `json:"namespace"`
	Queue          string          `json:"queue"`
	Topic          string          `json:"topic"`
	Subscription   string          `json:"subscription"`
	Concurrency    int             `json:"concurrency"`
	TimeoutSeconds int             `json:"timeoutSeconds"`
	MaxAttempts    int             `json:"maxAttempts"`
	BudgetUSD      float64         `json:"budgetUsd"` // reserved per attempt; never refunded after a crash
	Agent          ContainerConfig `json:"agent"`
	Resources      []Resource      `json:"resources"`
}

type ContainerConfig struct {
	Runtime   string `json:"runtime"`   // docker or podman, never an arbitrary shell
	Image     string `json:"image"`     // prebuilt image with Claude CLI and project tools
	APIKeyEnv string `json:"apiKeyEnv"` // ANTHROPIC_API_KEY only
	Network   string `json:"network"`   // operator-provisioned internal network with restricted egress proxy
	ProxyURL  string `json:"proxyUrl"`
}

type Deployment struct {
	Commit string    `json:"commit"`
	From   time.Time `json:"from"`
	Until  time.Time `json:"until"` // exclusive; zero denotes the current deployment
}

type Resource struct {
	ID          string       `json:"id"`
	Repo        string       `json:"repo"`
	Dir         string       `json:"dir"`
	Base        string       `json:"base"`
	Environment string       `json:"environment"`
	Deployments []Deployment `json:"deployments"`
	Runbooks    []string     `json:"runbooks"`
	Workspace   string       `json:"workspace"` // Log Analytics workspace UUID
	Query       string       `json:"query"`     // trusted, operator-authored KQL; never taken from an alert
	Regression  []string     `json:"regression"`
	Checks      [][]string   `json:"checks"`
	TestPaths   []string     `json:"testPaths"` // filepath.Match patterns, repository relative
	Workflow    string       `json:"workflow"`  // configured cenci instructions for this repo
}

var shaPattern = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var uuidPattern = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)
var basePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_-]*$`)
var containerID = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer func() { _ = f.Close() }()
	var c Config
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err := d.Decode(&extra); err == nil {
		return c, fmt.Errorf("extra config value")
	} else if err != io.EOF {
		return c, err
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if !c.Enabled {
		return fmt.Errorf("incident worker is disabled; set enabled explicitly")
	}
	if !filepath.IsAbs(c.StateDir) || c.Concurrency < 1 || c.Concurrency > 16 || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 86400 || c.MaxAttempts < 1 || c.MaxAttempts > 10 || c.BudgetUSD < 0.03 || c.BudgetUSD > 10000 || math.IsInf(c.BudgetUSD, 0) || math.IsNaN(c.BudgetUSD) {
		return fmt.Errorf("invalid state directory or execution limits")
	}
	if !strings.HasSuffix(c.Namespace, ".servicebus.windows.net") || strings.ContainsAny(c.Namespace, "/:@\\") {
		return fmt.Errorf("invalid Service Bus namespace")
	}
	if (c.Queue != "") == (c.Topic != "") || (c.Topic != "" && c.Subscription == "") || (c.Queue != "" && c.Subscription != "") {
		return fmt.Errorf("configure one queue or topic/subscription")
	}
	if c.Agent.Runtime != "docker" && c.Agent.Runtime != "podman" {
		return fmt.Errorf("agent runtime must be docker or podman")
	}
	if c.Agent.Image == "" || strings.HasPrefix(c.Agent.Image, "-") || c.Agent.APIKeyEnv != "ANTHROPIC_API_KEY" {
		return fmt.Errorf("agent needs a prebuilt image and ANTHROPIC_API_KEY")
	}
	if !basePattern.MatchString(c.Agent.Network) || !strings.HasPrefix(c.Agent.ProxyURL, "http://") || strings.ContainsAny(c.Agent.ProxyURL, "@\r\n") {
		return fmt.Errorf("agent needs an internal network and credential-free HTTP egress proxy")
	}
	if len(c.Resources) == 0 {
		return fmt.Errorf("resource allowlist is empty")
	}
	seen := map[string]bool{}
	dirs := map[string]string{}
	repos := map[string]string{}
	for _, r := range c.Resources {
		id := strings.ToLower(r.ID)
		if !strings.HasPrefix(id, "/subscriptions/") || seen[id] {
			return fmt.Errorf("invalid or duplicate resource %q", r.ID)
		}
		seen[id] = true
		if !repoPattern.MatchString(r.Repo) || !filepath.IsAbs(r.Dir) || !basePattern.MatchString(r.Base) || strings.Contains(r.Base, "..") || r.Environment == "" || r.Workflow == "" {
			return fmt.Errorf("resource %s needs repository, absolute checkout, base, environment and workflow", r.ID)
		}
		canonical, err := filepath.EvalSymlinks(r.Dir)
		if err != nil {
			return fmt.Errorf("repository %s: %w", r.Dir, err)
		}
		if canonical != filepath.Clean(r.Dir) {
			return fmt.Errorf("repository path must be canonical: %s", r.Dir)
		}
		if other, ok := dirs[canonical]; ok && other != r.Repo {
			return fmt.Errorf("checkout cannot map to multiple repositories")
		}
		dirs[canonical] = r.Repo
		if old, ok := repos[strings.ToLower(r.Repo)]; ok && old != canonical {
			return fmt.Errorf("repository must use one canonical checkout")
		}
		repos[strings.ToLower(r.Repo)] = canonical
		if !uuidPattern.MatchString(r.Workspace) || r.Query == "" || len(r.Runbooks) == 0 || len(r.Regression) == 0 || len(r.Checks) == 0 || len(r.TestPaths) == 0 {
			return fmt.Errorf("resource %s needs telemetry, runbooks, regression command, checks and test paths", r.ID)
		}
		for _, p := range r.Runbooks {
			if !safeRelative(p) {
				return fmt.Errorf("invalid runbook path %q", p)
			}
		}
		for _, p := range r.TestPaths {
			if !safeRelative(p) {
				return fmt.Errorf("invalid test pattern %q", p)
			}
			if _, err := filepath.Match(p, "test"); err != nil {
				return err
			}
		}
		for _, cmd := range append([][]string{r.Regression}, r.Checks...) {
			if len(cmd) == 0 || cmd[0] == "" || strings.HasPrefix(cmd[0], "-") {
				return fmt.Errorf("invalid check command")
			}
		}
		for i, d := range r.Deployments {
			if !shaPattern.MatchString(d.Commit) || d.From.IsZero() || (!d.Until.IsZero() && !d.Until.After(d.From)) {
				return fmt.Errorf("invalid deployment for %s", r.ID)
			}
			for _, old := range r.Deployments[:i] {
				if (d.Until.IsZero() || old.From.Before(d.Until)) && (old.Until.IsZero() || d.From.Before(old.Until)) {
					return fmt.Errorf("overlapping deployments for %s", r.ID)
				}
			}
		}
	}
	return nil
}

func safeRelative(p string) bool {
	return p != "" && !filepath.IsAbs(p) && p != "." && filepath.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\x00\r\n")
}

func (c Config) resource(id string) (Resource, bool) {
	for _, r := range c.Resources {
		if strings.EqualFold(r.ID, id) {
			return r, true
		}
	}
	return Resource{}, false
}
func (r Resource) deployed(at time.Time) string {
	for _, d := range r.Deployments {
		if !at.Before(d.From) && (d.Until.IsZero() || at.Before(d.Until)) {
			return d.Commit
		}
	}
	return ""
}
