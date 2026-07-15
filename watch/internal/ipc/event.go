package ipc

// HookEvent represents an agent hook event delivered via cenci notify.
type HookEvent struct {
	EventType        string `json:"event_type"`                  // hook_event_name from stdin
	SessionID        string `json:"session_id"`                  // agent session ID
	Agent            string `json:"agent,omitempty"`             // claude, codex, or empty when unknown
	TmuxPane         string `json:"tmux_pane"`                   // $TMUX_PANE (e.g. %5)
	NotificationType string `json:"notification_type,omitempty"` // Notification events only
	ToolName         string `json:"tool_name,omitempty"`         // PreToolUse, PermissionRequest, PostToolUse events
	TaskName         string `json:"task_name,omitempty"`         // compact first-prompt label; raw prompt is never sent
	IsInterrupt      bool   `json:"is_interrupt,omitempty"`      // PostToolUseFailure: true if user pressed ESC
	AgentID          string `json:"agent_id,omitempty"`          // set when the hook fires inside a subagent (Task tool) call; empty for the main agent
	Timestamp        string `json:"timestamp"`
}
