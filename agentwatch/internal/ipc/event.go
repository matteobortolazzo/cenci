package ipc

// HookEvent represents an agent hook event delivered via agentwatch notify.
type HookEvent struct {
	EventType        string `json:"event_type"`                  // hook_event_name from stdin
	SessionID        string `json:"session_id"`                  // agent session ID
	Agent            string `json:"agent,omitempty"`             // claude, codex, or empty when unknown
	TmuxPane         string `json:"tmux_pane"`                   // $TMUX_PANE (e.g. %5)
	NotificationType string `json:"notification_type,omitempty"` // Notification events only
	ToolName         string `json:"tool_name,omitempty"`         // PreToolUse, PermissionRequest, PostToolUse events
	IsInterrupt      bool   `json:"is_interrupt,omitempty"`      // PostToolUseFailure: true if user pressed ESC
	Timestamp        string `json:"timestamp"`
}
