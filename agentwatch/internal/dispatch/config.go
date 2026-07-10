package dispatch

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/run"
)

// QuietHours is a local-clock window in which no dispatch happens. StartHour ==
// EndHour disables it; StartHour > EndHour (e.g. 22..7) wraps past midnight.
type QuietHours struct {
	StartHour int `json:"startHour"`
	EndHour   int `json:"endHour"`
}

// Contains reports whether now (in its own location) falls inside the window.
func (q QuietHours) Contains(now time.Time) bool {
	if q.StartHour == q.EndHour {
		return false
	}
	h := now.Hour()
	if q.StartHour < q.EndHour {
		return h >= q.StartHour && h < q.EndHour
	}
	return h >= q.StartHour || h < q.EndHour
}

// RepoConfig binds an owner/repo to the local directory holding its .plans/ and
// git tree — the directory a dispatched session cd's into.
type RepoConfig struct {
	Repo string `json:"repo"`
	Dir  string `json:"dir"`
}

// Config is the resolved dispatch policy: built-in defaults with a config.json
// "dispatch" block merged over them.
type Config struct {
	Repos                  []RepoConfig
	ConcurrencyCap         int
	NeedInputThreshold     int
	DailyQuota             int
	QuietHours             *QuietHours
	PlanStalenessTolerance int // commits
	DefaultAgent           string
	AgentBudgetFloors      map[string]float64
	AgentLimits            map[string]AgentLimit
	AgentPreference        []string
	ClaudeSessionDir       string
	CodexDBPath            string
	Session                string // target tmux session for dispatched windows
}

// DefaultConfig returns the built-in policy used when no config file (or no
// "dispatch" block) is present.
func DefaultConfig() Config {
	return Config{
		ConcurrencyCap:         3,
		NeedInputThreshold:     1,
		DailyQuota:             20,
		PlanStalenessTolerance: 5,
		DefaultAgent:           "claude",
	}
}

// dispatchFile is the on-disk "dispatch" block. Numeric fields are pointers so
// an explicit 0 (e.g. concurrencyCap: 0 to pause) is distinguishable from unset.
type dispatchFile struct {
	Repos                  []RepoConfig          `json:"repos"`
	ConcurrencyCap         *int                  `json:"concurrencyCap"`
	NeedInputThreshold     *int                  `json:"needInputThreshold"`
	DailyQuota             *int                  `json:"dailyQuota"`
	QuietHours             *QuietHours           `json:"quietHours"`
	PlanStalenessTolerance *int                  `json:"planStalenessTolerance"`
	DefaultAgent           string                `json:"defaultAgent"`
	AgentBudgetFloors      map[string]float64    `json:"agentBudgetFloors"`
	AgentLimits            map[string]AgentLimit `json:"agentLimits"`
	AgentPreference        []string              `json:"agentPreference"`
	ClaudeSessionDir       string                `json:"claudeSessionDir"`
	CodexDBPath            string                `json:"codexDBPath"`
	Session                string                `json:"session"`
}

// LoadConfig returns the default policy with the config.json "dispatch" block
// merged over it. It reads the same file as internal/run (unknown keys ignored).
// A missing file is not an error. An empty path resolves the XDG default.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	if path == "" {
		path = run.DefaultConfigPath()
	}
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("reading config %s: %w", path, err)
	}
	var f struct {
		Dispatch *dispatchFile `json:"dispatch"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return cfg, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if f.Dispatch != nil {
		cfg = mergeConfig(cfg, *f.Dispatch)
	}
	return cfg, nil
}

// mergeConfig overlays the file block over base: scalars override when set,
// slices/maps override when non-nil.
func mergeConfig(base Config, o dispatchFile) Config {
	if o.Repos != nil {
		base.Repos = o.Repos
	}
	if o.ConcurrencyCap != nil {
		base.ConcurrencyCap = *o.ConcurrencyCap
	}
	if o.NeedInputThreshold != nil {
		base.NeedInputThreshold = *o.NeedInputThreshold
	}
	if o.DailyQuota != nil {
		base.DailyQuota = *o.DailyQuota
	}
	if o.QuietHours != nil {
		base.QuietHours = o.QuietHours
	}
	if o.PlanStalenessTolerance != nil {
		base.PlanStalenessTolerance = *o.PlanStalenessTolerance
	}
	if o.DefaultAgent != "" {
		base.DefaultAgent = o.DefaultAgent
	}
	if o.AgentBudgetFloors != nil {
		base.AgentBudgetFloors = o.AgentBudgetFloors
	}
	if o.AgentLimits != nil {
		base.AgentLimits = o.AgentLimits
	}
	if o.AgentPreference != nil {
		base.AgentPreference = o.AgentPreference
	}
	if o.ClaudeSessionDir != "" {
		base.ClaudeSessionDir = o.ClaudeSessionDir
	}
	if o.CodexDBPath != "" {
		base.CodexDBPath = o.CodexDBPath
	}
	if o.Session != "" {
		base.Session = o.Session
	}
	return base
}
