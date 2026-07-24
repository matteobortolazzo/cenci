package ipc

// HookEvent represents an agent hook event delivered via cenci notify.
type HookEvent struct {
	EventType        string `json:"event_type"`                  // hook_event_name from stdin
	SessionID        string `json:"session_id"`                  // agent session ID
	Agent            string `json:"agent,omitempty"`             // claude, codex, opencode, or empty when unknown
	TmuxPane         string `json:"tmux_pane"`                   // $TMUX_PANE (e.g. %5)
	NotificationType string `json:"notification_type,omitempty"` // Notification events only
	ToolName         string `json:"tool_name,omitempty"`         // PreToolUse, PermissionRequest, PostToolUse events
	TaskName         string `json:"task_name,omitempty"`         // compact first-prompt label; raw prompt is never sent
	IsInterrupt      bool   `json:"is_interrupt,omitempty"`      // PostToolUseFailure: true if user pressed ESC
	AgentID          string `json:"agent_id,omitempty"`          // set when the hook fires inside a subagent (Task tool) call; empty for the main agent
	BackgroundWork   bool   `json:"background_work,omitempty"`   // Stop events: the turn ended with in-flight background work still registered (#698)
	Timestamp        string `json:"timestamp"`
}

// pendingCloseKind is the "kind" discriminator value for a PendingClose
// message sent over the event socket (#522). Plain HookEvent lines carry no
// "kind" field at all, so an envelope with an empty/absent "kind" always
// routes to the pre-existing Events() channel — see event_receiver.go.
const pendingCloseKind = "pending-close"

// PendingClose is a fire-and-forget message `cenci close` sends to the
// daemon's event socket when it skips a matched window as busy
// (running/need-input, no --force). The daemon records it and kills the
// window itself once it observes the owning session's SessionEnd (#522).
type PendingClose struct {
	Session     string `json:"session"`
	WindowIndex string `json:"window_index"`
	WindowName  string `json:"window_name"`
}
