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
	// PaneID identifies the original window throughout deferred shutdown.
	// Missing or malformed identities are rejected; indexes are display only.
	PaneID      string `json:"pane_id"`
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

// babysitStopKind and babysitStatusKind are the "kind" discriminator values
// for the in-container `cenci babysit stop` / `cenci babysit status` requests
// to the host daemon (#1390). They reuse the babysit-arm envelope: one request
// line, exactly one reply line, then the daemon closes. An absent/unknown
// "kind" still routes to Events() unchanged -- see event_receiver.go.
const (
	babysitStopKind   = "babysit-stop"
	babysitStatusKind = "babysit-status"
)

// BabysitStopRequest asks the host daemon to stop the supervisor for PR in
// Repo (#1390). PR and Repo cross the container->host trust boundary and are
// validated daemon-side before any host state is read or any process is
// signalled.
type BabysitStopRequest struct {
	PR   string `json:"pr"`
	Repo string `json:"repo"`
}

// BabysitStopResponse is the daemon's single ack-or-nack reply to a
// BabysitStopRequest. Reason is only meaningful when OK is false; it carries
// no host path, PID or captured command output, and the client relays it
// verbatim.
type BabysitStopResponse struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// BabysitStatusRequest asks the host daemon for the supervisor state of PR in
// Repo (#1390); the same trust-boundary rules as BabysitStopRequest apply.
type BabysitStatusRequest struct {
	PR   string `json:"pr"`
	Repo string `json:"repo"`
}

// BabysitStatus is the machine-readable supervisor summary `cenci babysit
// status <pr>` prints as one JSON object (#1390). It deliberately carries no
// filesystem path. SupervisorLive is true only for a recorded, verified
// supervisor process (PID > 0 and owned) -- a needs-input or arming state
// with no process is not live.
type BabysitStatus struct {
	PR                  int    `json:"pr"`
	Repo                string `json:"repo"`
	Status              string `json:"status"`
	CIFixAttempts       int    `json:"ciFixAttempts"`
	ConflictFixAttempts int    `json:"conflictFixAttempts"`
	FixCap              int    `json:"fixCap"`
	SupervisorLive      bool   `json:"supervisorLive"`
}

// BabysitStatusResponse is the daemon's single reply to a
// BabysitStatusRequest: Status is set on an ack and nil on a nack.
type BabysitStatusResponse struct {
	OK     bool           `json:"ok"`
	Reason string         `json:"reason,omitempty"`
	Status *BabysitStatus `json:"status,omitempty"`
}
