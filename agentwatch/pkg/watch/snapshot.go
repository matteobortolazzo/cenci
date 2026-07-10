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
}

// WindowState describes a single tracked window.
type WindowState struct {
	// Session is the tmux session name the window belongs to.
	Session string `json:"session"`
	// WindowIndex is the window's index within its session, as a string.
	WindowIndex string `json:"window_index"`
	// WindowName is the "<number>-<slug>" window name. It is the stable join
	// key external tools use to associate agentwatch state with their own
	// records (for example, badging a kanban card that shares the name).
	WindowName string `json:"window_name"`
	// TaskName is the human-readable task extracted for the window, if any.
	TaskName string `json:"task_name"`
	// Status is the window's current status (for example "idle", "running",
	// "done", "stopped", "need_input", or "failed"). "failed" marks a synthetic
	// entry for a dispatch-failed or plan-invalid ticket that has no live window.
	Status string `json:"status"`
	// Agent identifies the coding agent detected in the window, if known.
	// Omitted from the JSON when empty.
	Agent string `json:"agent,omitempty"`
	// ManuallyNamed reports whether the window name was set by the user rather
	// than derived by agentwatch.
	ManuallyNamed bool `json:"manually_named"`
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
	// NeedInput counts windows awaiting user input.
	NeedInput int `json:"need_input"`
	// Failed counts synthetic entries for dispatch-failed or plan-invalid
	// tickets surfaced by the reconciler (they have no live window).
	Failed int `json:"failed"`
}
