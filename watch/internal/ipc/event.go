package ipc

import "time"

// HookEvent represents an agent hook event delivered via cenci notify.
type HookEvent struct {
	EventType        string `json:"event_type"`                   // hook_event_name from stdin
	SessionID        string `json:"session_id"`                   // agent session ID
	Agent            string `json:"agent,omitempty"`              // claude, codex, opencode, or empty when unknown
	TmuxPane         string `json:"tmux_pane"`                    // $TMUX_PANE (e.g. %5)
	NotificationType string `json:"notification_type,omitempty"`  // Notification events only
	ToolName         string `json:"tool_name,omitempty"`          // PreToolUse, PermissionRequest, PostToolUse events
	TaskName         string `json:"task_name,omitempty"`          // compact first-prompt label; raw prompt is never sent
	IsInterrupt      bool   `json:"is_interrupt,omitempty"`       // PostToolUseFailure: true if user pressed ESC
	AgentID          string `json:"agent_id,omitempty"`           // set when the hook fires inside a subagent (Task tool) call; empty for the main agent
	BackgroundWork   bool   `json:"background_work,omitempty"`    // Stop events: the turn ended with in-flight background work still registered (#698)
	SessionEndReason string `json:"session_end_reason,omitempty"` // SessionEnd events: reason enum (clear, resume, logout, prompt_input_exit, bypass_permissions_disabled, other) (#707)
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

// armRequestKind is the "kind" discriminator value for a babysit-arm request
// sent over the event socket (#1094). It follows the pending-close precedent
// exactly: an absent/unknown "kind" still routes to the pre-existing
// Events() channel unchanged — see event_receiver.go.
const armRequestKind = "babysit-arm"

// ArmRequest is the in-container `cenci babysit`/`babysit stop` client's
// request to have the host daemon spawn (or verify) a supervisor on its
// behalf (#1094). PR/Repo are validated daemon-side before the request ever
// reaches the injectable spawn seam; Agent is validated against the same
// closed set `parseBabysitArgs` accepts. Interval and TmuxPane carry the
// arguments the host-side spawn (#1095's hostArmSpawn) needs.
type ArmRequest struct {
	PR       string        `json:"pr"`
	Repo     string        `json:"repo"`
	Agent    string        `json:"agent"`
	Interval time.Duration `json:"interval"`
	TmuxPane string        `json:"tmux_pane"`
}

// ArmResponse is the daemon's single ack-or-nack reply to an ArmRequest,
// written on the same connection before it closes (#1094). Reason is only
// meaningful when OK is false; the client relays it verbatim and never
// re-derives or re-words it.
type ArmResponse struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}
