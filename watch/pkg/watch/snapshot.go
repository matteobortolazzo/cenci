package watch

// StateSnapshot is the top-level message broadcast to subscribers. The daemon
// emits one StateSnapshot per NDJSON line whenever tracked state changes.
type StateSnapshot struct {
	// Timestamp is the RFC 3339 time at which the daemon built this snapshot.
	Timestamp string `json:"timestamp"`
	// Windows is the current set of tracked windows, one entry per window.
	Windows []WindowState `json:"windows"`
	// Summary holds aggregate counts across Windows.
	Summary StatusSummary `json:"summary"`
	// Headroom reports per-agent-type token-budget headroom (0.0-1.0) computed
	// by the embedded dispatch loop. Omitted when the embedded loop is disabled
	// or no AgentLimits are configured, leaving downstream frontends unchanged.
	Headroom map[string]float64 `json:"headroom,omitempty"`
	// Dispatch reports the embedded fleet dispatch loop's live state (#219).
	// Nil until a daemon populates it (#220), in which case the "dispatch" key
	// is omitted entirely so pre-#219 NDJSON consumers are unaffected.
	Dispatch *DispatchState `json:"dispatch,omitempty"`
}

// AttentionUpdate is the reconciler's per-tick push onto the daemon's
// attention channel: the current set of synthetic "failed"/"escalated"
// windows (#46, extended by #826) alongside the current per-agent-type
// headroom snapshot (#169).
type AttentionUpdate struct {
	// Windows is the current set of synthetic "failed"/"escalated" window
	// entries.
	Windows []WindowState
	// Headroom reports per-agent-type token-budget headroom (0.0-1.0). Empty
	// when the embedded loop's BudgetProvider is not a *UsageProvider or no
	// AgentLimits are configured.
	Headroom map[string]float64
	// Dispatch reports the embedded fleet dispatch loop's live state (#219).
	// Nil until a daemon populates it (#220).
	Dispatch *DispatchState `json:"dispatch,omitempty"`
}

// WindowState describes a single tracked window.
type WindowState struct {
	// Session is the tmux session name the window belongs to.
	Session string `json:"session"`
	// WindowIndex is the window's index within its session, as a string.
	WindowIndex string `json:"window_index"`
	// PaneID is the tracked agent's stable tmux pane identity ("%N"). Close
	// operations use it throughout shutdown; session indexes can be reused.
	// Empty for paneless sessions and synthetic attention entries.
	PaneID string `json:"pane_id,omitempty"`
	// Socket is the tmux server socket the window lives on. Empty means the
	// default server, which is also what a snapshot from an older daemon (one
	// that predates this field) means.
	Socket string `json:"socket,omitempty"`
	// WindowName is the "<number>-<skill>" window name (e.g. "42-implement"),
	// where skill is the running workflow. The leading ticket number is the
	// stable join key external tools use to associate cenci state with
	// their own records (for example, badging a kanban card by number prefix).
	// A free-text run with no ticket number keeps a descriptive slug instead.
	WindowName string `json:"window_name"`
	// TaskName is the human-readable task extracted for the window, if any.
	TaskName string `json:"task_name"`
	// Status is the window's current status (for example "idle", "running",
	// "done", "stopped", "need_input", "failed", or "escalated"). "failed"
	// marks a synthetic entry for a dispatch-failed or plan-invalid ticket
	// that has no live window. "escalated" (#826) marks a synthetic entry
	// for a ticket the unattended planner escalated (Input Needed) -- also
	// no live window, but distinct from "failed": see StatusSummary.Escalated.
	Status string `json:"status"`
	// Agent identifies the coding agent detected in the window, if known.
	// Omitted from the JSON when empty.
	Agent string `json:"agent,omitempty"`
	// ManuallyNamed reports whether the window name was set by the user rather
	// than derived by cenci.
	ManuallyNamed bool `json:"manually_named"`
	// ModelSwitches lists the model switches Claude Code reported for the
	// session (and its subagents), oldest first, capped by the daemon. It never
	// influences Status. Omitted from the JSON when empty, so older NDJSON
	// consumers are unaffected.
	ModelSwitches []ModelSwitch `json:"model_switches,omitempty"`
	// ModelSwitchesMore counts switches past the cap that are not listed.
	// Omitted from the JSON when zero.
	ModelSwitchesMore int `json:"model_switches_more,omitempty"`
	// NativeMeasurement is the latest validated native Claude measurement for
	// the session (#1450). Display only: it never feeds Status.
	NativeMeasurement *NativeMeasurement `json:"native_measurement,omitempty"`
	// Association is how a Codex session's tmux window was established
	// (#1529): "bound" (a `cenci bind` binding with a window), "proven" (a
	// window from a hook pane, server pid matched or no socket sent) or
	// "unproven" (hooks arrived but a pane-carrying event was refused, so the
	// session is paneless). Omitted for every other entry, including a Codex
	// session that never reported a pane.
	Association string `json:"association,omitempty"`
	// SessionID is the agent session id, set only on "unproven" entries, so
	// `cenci status` can name it in the `cenci bind` hint. Proven and bound
	// entries omit it: the broadcast socket is mounted into sandboxes, and host
	// session ids stay off it wherever the hint does not need them.
	SessionID string `json:"session_id,omitempty"`
}

// Association values of WindowState.Association (#1529).
const (
	AssociationBound    = "bound"
	AssociationProven   = "proven"
	AssociationUnproven = "unproven"
)

// NativeMeasurement is the validated snapshot of one native Claude
// measurement sample (#1450), stored per session. Freshness ("fresh|stale") and
// quota state ("current|expired (reset passed)") are not stored: they depend on
// the reader's clock, so consumers derive them from MeasuredAt and ResetsAt.
type NativeMeasurement struct {
	// MeasuredAt is the sample's RFC 3339 timestamp.
	MeasuredAt    string `json:"measured_at"`
	Source        string `json:"source,omitempty"`
	ClaudeVersion string `json:"claude_version,omitempty"`
	// Context is nil when the context object was absent or not an object.
	Context *NativeContext `json:"context,omitempty"`
	// Quota is nil (encodes null) when rate_limits was absent or rejected, and
	// an empty slice (encodes []) when it was an empty array.
	Quota []NativeQuotaWindow `json:"quota"`
	// Missing names every absent or rejected field (dotted JSON paths with
	// indexes); Invalid names only the rejected subset. Both are non-nil so an
	// empty list encodes as [], never null. A "rate_limits[<i>]..." index is the
	// position in the input array, not in Quota: a rejected non-object entry is
	// skipped in Quota, so the two can differ.
	Missing []string `json:"missing"`
	Invalid []string `json:"invalid"`
}

// NativeContext is the context-window part of a NativeMeasurement. A rejected
// value is a nil pointer, never zero.
type NativeContext struct {
	Tokens  *int64   `json:"tokens,omitempty"`
	Window  *int64   `json:"window,omitempty"`
	Percent *float64 `json:"percent,omitempty"`
	// OverWindow is true when Percent is above 100 (tokens over the window).
	OverWindow bool `json:"over_window,omitempty"`
}

// NativeQuotaWindow is one rate-limit window of a NativeMeasurement. Kind is
// "five_hour", "weekly" or "unknown:<sanitized kind>".
type NativeQuotaWindow struct {
	Kind        string   `json:"kind"`
	PercentUsed *float64 `json:"percent_used,omitempty"`
	ResetsAt    string   `json:"resets_at,omitempty"`
}

// ModelSwitch is one model change reported by a Claude Code PostModelSwitch
// hook. FromModel and ToModel hold a validated model id or the placeholder
// "(invalid model id)"; AgentType is set only when Subagent is true.
type ModelSwitch struct {
	FromModel string `json:"from_model"`
	ToModel   string `json:"to_model"`
	// Source is the hook's source value ("auto" for a change Claude Code made on
	// its own, such as the fallback after a flagged request), or "unknown".
	Source    string `json:"source"`
	Subagent  bool   `json:"subagent"`
	AgentType string `json:"agent_type,omitempty"`
	// Fallback is true only for Source "auto"; any other source keeps the
	// neutral label.
	Fallback bool `json:"fallback"`
}

// DispatchState describes the daemon-embedded fleet dispatch loop.
type DispatchState struct {
	// Enabled reports whether the embedded loop is turned on, resolved from
	// dispatch.Config's LoopEnabled.
	Enabled bool `json:"enabled"`
	// DaemonRunning reports whether a daemon was reached over its socket to
	// answer this query. False means every field below falls back to the
	// resolved config and does not reflect a live pass.
	DaemonRunning bool `json:"daemon_running"`
	// Interval is the configured loop interval (e.g. "5m"). Empty when no
	// positive interval is configured. Omitted from JSON when empty.
	Interval string `json:"interval,omitempty"`
	// PassRunning reports whether a dispatch pass is in flight right now.
	// Only meaningful when DaemonRunning is true.
	PassRunning bool `json:"pass_running"`
	// LastRunAt is the RFC 3339 time of the most recently completed pass.
	// Omitted from JSON when empty.
	LastRunAt string `json:"last_run_at,omitempty"`
	// LastDispatched is the number of tickets dispatched in the most recent
	// pass.
	LastDispatched int `json:"last_dispatched"`
	// LastSkipped is the number of tickets skipped (not dispatched) in the
	// most recent pass.
	LastSkipped int `json:"last_skipped"`
	// LastError is the most recent pass's error message, if any. Omitted
	// from JSON when empty.
	LastError string `json:"last_error,omitempty"`
	// ResolveError is set only when ResolveDispatchState's fallback path was
	// triggered by a non-daemon-unreachable ReadSnapshot error (for example a
	// corrupt/truncated snapshot or a permission-denied socket) -- a daemon
	// that was reached but then failed, distinct from no daemon listening at
	// all. Empty in every other case, including the live-daemon-broadcast
	// path (StateSnapshot.Dispatch) which never sets it. Omitted from JSON
	// when empty.
	ResolveError string `json:"resolve_error,omitempty"`
}

// StatusSummary counts tracked windows by status.
type StatusSummary struct {
	// Total is the number of tracked windows.
	Total int `json:"total"`
	// Idle counts windows in the idle status.
	Idle int `json:"idle"`
	// Running counts windows in the running status.
	Running int `json:"running"`
	// Done counts windows in the done status.
	Done int `json:"done"`
	// Stopped counts windows in the stopped status.
	Stopped int `json:"stopped"`
	// NeedInput counts windows awaiting user input from a live, running
	// agent session (a hook-reported detect.StatusNeedInput). This is the
	// counter internal/dispatch/decide.go's fleet-wide "need-input pause"
	// gate reads (Summary.NeedInput >= NeedInputThreshold) -- it is about a
	// live session blocked mid-turn, not about the reconciler's Input
	// Needed board label. See Escalated below: the two must never be
	// conflated, and only NeedInput feeds that pause.
	NeedInput int `json:"need_input"`
	// Failed counts synthetic entries for dispatch-failed or plan-invalid
	// tickets surfaced by the reconciler (they have no live window).
	Failed int `json:"failed"`
	// Escalated (#826) counts synthetic entries for tickets the unattended
	// planner escalated (the reconciler's Input Needed label -> Escalated
	// window, status "escalated"). This is a DIFFERENT concept from
	// NeedInput above -- a live session waiting mid-turn versus a stopped
	// run blocked on a ticket-comment answer -- and deliberately does not
	// feed dispatch's fleet-wide NeedInput pause: a single escalation must
	// never freeze the whole fleet.
	Escalated int `json:"escalated"`
}
