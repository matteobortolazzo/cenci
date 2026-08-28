package dispatch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
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
// git tree — the directory a dispatched session cd's into — and the tmux
// session its dispatches target (#927). Session has no `omitempty` so an
// enrolled entry always advertises the required key on write-back. An empty
// Session (or one whose configured tmux session is absent) skips every
// ActionDispatch decision for this repo -- see the per-repo gate in
// session.go.
type RepoConfig struct {
	Repo    string `json:"repo"`
	Dir     string `json:"dir"`
	Session string `json:"session"`
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
	// Model, when set, overrides agents.*.model for every session this
	// dispatch pass spawns, so dispatched sessions don't silently inherit
	// whatever ambient/account-level default model is active at spawn time.
	Model             string
	AgentBudgetFloors map[string]float64
	AgentLimits       map[string]AgentLimit
	AgentPreference   []string
	ClaudeSessionDir  string
	CodexDBPath       string

	// LegacyTopLevelSession (#927) reports whether the config file still
	// carries a leftover top-level dispatch.session key with a non-empty
	// (post-trim) value -- the pre-#927 fleet-wide session, now replaced by
	// per-repo repos[].session. It is read-only detection: the legacy value
	// is NEVER back-filled onto any repos[] entry, and this field never
	// gates a pass on its own -- it only drives a once-per-pass deprecation
	// log line (RunOnce) naming the repos with no session and the resolved
	// config path. A present-but-empty/whitespace-only legacy value dropped
	// no real configuration, so it does not set this true (Q&A #2).
	LegacyTopLevelSession bool

	// Path is the config file LoadConfig actually resolved and read from
	// (after the run.DefaultConfigPath() fallback), so log lines that name
	// "the resolved config path" (the per-repo session gate, the legacy
	// deprecation line) are naming the file that was actually loaded rather
	// than re-deriving the XDG default and mis-naming it when --config was
	// passed. Empty when no path could be resolved at all.
	Path string

	// Reconciler (#46) policy.
	GracePeriod    time.Duration // how long the failure signal must hold before recovery (default 5m)
	RetryBudget    int           // retries before a ticket is marked dispatch-failed (default 2)
	DaemonInterval time.Duration // embedded dispatch+reconcile loop interval; 0 disables it

	// ApplyRetryBudget (#265) bounds consecutive failed apply-mutation passes
	// (e.g. a gh call failing outright) before a ticket is escalated to the
	// reconcile-stuck terminal label. Separate from RetryBudget, which bounds
	// dispatch attempts, not apply-mutation failures.
	ApplyRetryBudget int

	// LoopEnabled reports whether the embedded fleet dispatch loop (#219) is
	// on. Resolved from an explicit "loopEnabled" when present, else absent
	// means disabled.
	LoopEnabled bool

	// PipelineStageGate is the #732 kill switch for the persisted-pipeline-
	// stage gate: when true (the default), a ticket whose persisted
	// `cenci pipeline` stage is "finalized" (or unreadable) is skipped even
	// if its board state otherwise permits dispatch. It can only ever
	// reduce dispatches, so it defaults on; set false to disable the gate
	// entirely during rollout.
	PipelineStageGate bool

	// PlanRefined (#828) gates two stage-aware dispatch behaviors in
	// lean-planning repos: a Refined ticket with no plan becomes a planning
	// dispatch (`cenci run implement <n>`), and a terminal "plan stale,
	// re-plan" skip becomes an autonomous re-plan dispatch
	// (`cenci run implement "<n> replan"`). Defaults false (default-deny) --
	// a fleet-wide kill switch, not sufficient authorization on its own
	// (#851): Refined pickup and autonomous re-plan additionally require the
	// repo's own committed `planning.autonomy == "lean"`, read from
	// `.cenci/config.json` via the probeRepoAutonomy gate (autonomy.go) --
	// fleet configuration can disable lean planning but can no longer
	// independently authorize it for a repo that hasn't opted in itself.
	PlanRefined bool

	// PlanningAttended (#1086) is the fleet-wide "a human is at the keyboard
	// on this machine" narrowing switch: when true, RunOnce's
	// narrowAutonomiesAttended step maps every RepoAutonomyLean entry in the
	// resolved autonomy map to RepoAutonomyAttended before Decide ever runs,
	// suppressing unattended planning pickups/re-plans for lean repos on
	// this machine specifically. Resolved leniently by LoadConfig (see
	// resolvePlanningAttended below): an unparseable planning.attended value
	// folds to true (the restrictive, safe direction) rather than aborting
	// the whole config load. Default false -- additive only, so a config
	// with no "planning" block is byte-identical to today's behavior.
	PlanningAttended bool

	// PlanningAttendedUnparseable (#1086) reports whether the raw
	// planning.attended value in the loaded config could not be trusted (a
	// non-bool value, or "planning" itself not a JSON object) -- set
	// whenever resolvePlanningAttended had to fold to the restrictive
	// PlanningAttended=true default rather than reading an explicit value.
	// RunOnce logs exactly one line naming planning.attended when this is
	// true, so a broken fleet config is discoverable without ever aborting
	// the dispatch pass (contrast reloadConfig's whole-file-JSON-corruption
	// abort path, which this field must never widen).
	PlanningAttendedUnparseable bool
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
		GracePeriod:            5 * time.Minute,
		RetryBudget:            2,
		ApplyRetryBudget:       3,
		PipelineStageGate:      true,
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
	Model                  string                `json:"model"`
	AgentBudgetFloors      map[string]float64    `json:"agentBudgetFloors"`
	AgentLimits            map[string]AgentLimit `json:"agentLimits"`
	AgentPreference        []string              `json:"agentPreference"`
	ClaudeSessionDir       string                `json:"claudeSessionDir"`
	CodexDBPath            string                `json:"codexDBPath"`
	// LegacySession is detection-only (#927): the old fleet-wide
	// dispatch.session key, replaced by per-repo repos[].session. It is
	// never merged into Config -- no field reads its decoded value, only
	// its presence and (post-trim) emptiness are inspected in LoadConfig --
	// so a legacy config whose value isn't even a JSON string (a shape a
	// typed `string` field would fail the whole parse on) is still
	// tolerated for detection purposes.
	LegacySession     *json.RawMessage `json:"session"`
	GracePeriod       string           `json:"gracePeriod"`       // Go duration string, e.g. "5m"
	RetryBudget       *int             `json:"retryBudget"`       // pointer so an explicit 0 (no retries) is distinguishable from unset
	DaemonInterval    string           `json:"daemonInterval"`    // Go duration string, e.g. "5m"; empty/0 disables the embedded loop
	LoopEnabled       *bool            `json:"loopEnabled"`       // pointer so absence resolves to disabled (not inferred from DaemonInterval)
	ApplyRetryBudget  *int             `json:"applyRetryBudget"`  // pointer so an explicit 0 is distinguishable from unset
	PipelineStageGate *bool            `json:"pipelineStageGate"` // pointer so an explicit false is distinguishable from unset
	PlanRefined       *bool            `json:"planRefined"`       // pointer so an explicit false is distinguishable from unset
}

// LoadConfig returns the default policy with the config.json "dispatch" block
// merged over it. It reads the same file as internal/run (unknown keys ignored).
// A missing file is not an error. An empty path resolves the XDG default.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	if path == "" {
		path = run.DefaultConfigPath()
	}
	cfg.Path = path
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
		Dispatch *dispatchFile    `json:"dispatch"`
		Planning *json.RawMessage `json:"planning"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return cfg, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if f.Dispatch != nil {
		cfg = mergeConfig(cfg, *f.Dispatch)
		cfg.LegacyTopLevelSession = legacyTopLevelSessionSet(f.Dispatch.LegacySession)
	}
	cfg.PlanningAttended, cfg.PlanningAttendedUnparseable = resolvePlanningAttended(f.Planning)
	return cfg, nil
}

// resolvePlanningAttended decodes the fleet config's top-level "planning"
// block leniently (#1086): unlike dispatchFile above, this is deliberately
// NOT a typed struct, so a malformed value (a non-bool attended, or
// "planning" itself not even a JSON object) can never fail the whole
// json.Unmarshal in LoadConfig and abort the pass via reloadConfig
// (dispatch.go's whole-file-corruption tick-skip path) -- which would also
// stop ordinary Planned dispatch, an unrelated behavior this key must never
// perturb. raw is nil when the "planning" key is absent entirely, resolving
// to (false, false): additive-only, byte-identical to every pre-#1086
// config. Any decode failure folds to (true, true) -- the restrictive,
// fail-closed direction -- so an operator-visible mistake in this block
// narrows planning rather than silently doing nothing.
func resolvePlanningAttended(raw *json.RawMessage) (attended bool, unparseable bool) {
	if raw == nil {
		return false, false
	}
	var block map[string]json.RawMessage
	if err := json.Unmarshal(*raw, &block); err != nil {
		return true, true
	}
	attendedRaw, ok := block["attended"]
	if !ok {
		return false, false
	}
	// A literal JSON null unmarshals into a non-pointer bool as a silent
	// no-op (err == nil, b left at its zero value false) -- without this
	// explicit check that would resolve to (false, false), identical to no
	// "planning" block at all, contradicting this function's fold-to-true
	// contract. Detected the same way QueryPlanningAttended (fleet.go)
	// detects it for its own strict-error path.
	if bytes.Equal(bytes.TrimSpace(attendedRaw), []byte("null")) {
		return true, true
	}
	var b bool
	if err := json.Unmarshal(attendedRaw, &b); err != nil {
		return true, true
	}
	return b, false
}

// legacyTopLevelSessionSet reports whether raw decodes to a non-empty
// (post-trim) string, per Q&A #2: a present-but-empty or whitespace-only
// legacy dispatch.session dropped no real configuration, so it must not
// trigger the deprecation detection. A non-string legacy value (or one that
// fails to decode) is tolerated as "not set" rather than failing the whole
// config parse -- detection is best-effort, never load-bearing.
func legacyTopLevelSessionSet(raw *json.RawMessage) bool {
	if raw == nil {
		return false
	}
	var s string
	if err := json.Unmarshal(*raw, &s); err != nil {
		return false
	}
	return strings.TrimSpace(s) != ""
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
	if o.Model != "" {
		base.Model = o.Model
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
	// No back-fill of the legacy top-level dispatch.session, ever (#927):
	// see legacyTopLevelSessionSet for the read-only detection that feeds
	// Config.LegacyTopLevelSession instead.
	if d, err := time.ParseDuration(o.GracePeriod); o.GracePeriod != "" && err == nil {
		base.GracePeriod = d
	}
	if o.RetryBudget != nil {
		base.RetryBudget = *o.RetryBudget
	}
	if d, err := time.ParseDuration(o.DaemonInterval); o.DaemonInterval != "" && err == nil {
		base.DaemonInterval = d
	}
	if o.LoopEnabled != nil {
		base.LoopEnabled = *o.LoopEnabled
	}
	if o.ApplyRetryBudget != nil {
		base.ApplyRetryBudget = *o.ApplyRetryBudget
	}
	if o.PipelineStageGate != nil {
		base.PipelineStageGate = *o.PipelineStageGate
	}
	if o.PlanRefined != nil {
		base.PlanRefined = *o.PlanRefined
	}
	return base
}
