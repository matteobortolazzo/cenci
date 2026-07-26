package daemon

import (
	"log"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
)

// sessionKeyForEvent returns the key for the daemon's sessions map: the agent
// session ID when present, otherwise a pane-derived fallback. Events carrying
// neither are dropped.
func sessionKeyForEvent(event ipc.HookEvent) string {
	if event.SessionID != "" {
		return event.SessionID
	}
	if event.TmuxPane != "" {
		return "pane:" + event.TmuxPane
	}
	return ""
}

func (d *Daemon) handleEvent(event ipc.HookEvent) {
	key := sessionKeyForEvent(event)
	if key == "" {
		if d.cfg.Verbose {
			log.Printf("event: dropping %s event with empty session_id and tmux_pane", event.EventType)
		}
		return
	}

	sess := d.sessions[key]
	if sess == nil {
		sess = &frontend.SessionState{SessionID: event.SessionID}
		d.sessions[key] = sess
	}
	sess.LastEvent = d.now()
	// Upgrade paneless→pane when a later event carries a pane; never clear a
	// known pane on an empty-pane event.
	if event.TmuxPane != "" {
		sess.TmuxPane = event.TmuxPane
	}
	if event.Agent != "" {
		sess.Agent = event.Agent
	}
	if event.EventType == "UserPromptSubmit" && event.TaskName != "" && !sess.PromptTaskName {
		sess.TaskName = event.TaskName
		sess.PromptTaskName = true
	}

	// A subagent's own SessionEnd (non-empty AgentID) must not tear down the
	// main session's window — only the main agent's own SessionEnd does.
	if event.EventType == "SessionEnd" && event.AgentID == "" {
		// Resolve the window's "session:index" target before teardown:
		// OnSessionEnd releases the frontend's tracking state for this
		// session key, so WindowInfo would return nil afterward (#522).
		wi := d.frontend.WindowInfo(key)
		delete(d.sessions, key)
		// "clear" and an interactive "resume" switch end the session id, not
		// the pane: a successor session continues in the same window
		// immediately, so hand the window over instead of restoring it
		// (#707). Every other reason — including an absent one from an older
		// Claude Code — keeps the full teardown: the handoff is narrowed to
		// the exact continuation reasons, never a catch-all.
		if event.SessionEndReason == "clear" || event.SessionEndReason == "resume" {
			// A pending-close means "close once the agent's work ends" — and on
			// a handoff the work continues in this window under a successor
			// session. Keep the registry entry (keyed by session:index, which
			// survives the handoff) so the kill fires on the successor's own
			// SessionEnd instead of destroying the window mid-continuation. If
			// the handoff fell back to teardown because the pane is already
			// gone, the sweep's prunePendingCloseForWindow reclaims the entry.
			d.frontend.OnSessionHandoff(sess)
		} else {
			d.frontend.OnSessionEnd(sess)
			d.killPendingClose(wi)
		}
		d.broadcast()
		return
	}

	status := d.mapEventToStatus(event)
	if status == detect.StatusUnknown {
		return
	}
	if status == detect.StatusRunning && sess.Status == detect.StatusNeedInput && suppressNeedInputClear(event, sess) {
		if d.cfg.Verbose {
			log.Printf("attention: suppressing %s (tool=%s agent=%s) clearing need-input on session %s", event.EventType, event.ToolName, event.AgentID, key)
		}
		return
	}
	// A subagent's running event must not clear a terminal status set by the
	// main agent (#656). Each `cenci notify` is an independent process racing
	// to the daemon's socket, so a subagent PreToolUse/PostToolUse can be
	// delivered after the main agent's Stop already set done — and because the
	// subagent's own Stop is suppressed above, nothing would restore done.
	// Only a main-agent event (empty AgentID) may resume a finished session.
	if status == detect.StatusRunning && event.AgentID != "" &&
		(sess.Status == detect.StatusDone || sess.Status == detect.StatusStopped) {
		if d.cfg.Verbose {
			log.Printf("event: suppressing %s (tool=%s agent=%s) clearing %v on session %s", event.EventType, event.ToolName, event.AgentID, sess.Status, key)
		}
		return
	}
	sess.Status = status
	switch status {
	case detect.StatusNeedInput:
		sess.AttentionSource = attentionSource(event)
		if d.cfg.Verbose {
			log.Printf("attention: session=%s source=%s event=%s tool=%s", key, sess.AttentionSource, event.EventType, event.ToolName)
		}
	case detect.StatusRunning:
		sess.AttentionSource = ""
	}

	obs := d.frontend.OnEvent(sess, event)
	for _, evicted := range obs.EvictedKeys {
		if evicted != key {
			delete(d.sessions, evicted)
		}
	}
	if sess.Agent == "" {
		sess.Agent = obs.AgentHint
	}
	if obs.TaskNameHint != "" {
		sess.TaskName = obs.TaskNameHint
	}

	d.broadcast()
}

// suppressNeedInputClear reports whether a Running-mapping event must be
// ignored while a session is waiting on user input (NeedInput), rather than
// clearing it back to running (#544). Two cases:
//
//   - The event belongs to a subagent (Task tool delegation, AgentID set).
//     A backgrounded subagent fires its own PreToolUse/PostToolUse on the
//     same session_id as the main agent; those must never affect the main
//     session's attention state, matching the existing Done/Stopped guard
//     in mapEventToStatus (#277).
//   - The event is a PostToolUse for a tool call that started before the
//     input-blocking tool and is only now completing (its tool name doesn't
//     match the tool recorded in AttentionSource). Each `cenci notify`
//     invocation is an independent process racing to the daemon's socket,
//     so a prior tool's completion can be delivered after the
//     PreToolUse(AskUserQuestion) that set NeedInput.
//
// A PreToolUse from the main agent itself is never suppressed — it always
// means the agent has resumed and is doing new work (see
// TestDaemon_AskUserQuestionSetsNeedInput).
func suppressNeedInputClear(event ipc.HookEvent, sess *frontend.SessionState) bool {
	if event.AgentID != "" {
		return true
	}
	if event.EventType != "PostToolUse" {
		return false
	}
	expectedTool, ok := strings.CutPrefix(sess.AttentionSource, "input-tool:")
	return ok && expectedTool != "" && expectedTool != event.ToolName
}

func attentionSource(event ipc.HookEvent) string {
	if event.EventType == "PermissionRequest" {
		return "permission-request"
	}
	if event.EventType == "PreToolUse" {
		return "input-tool:" + event.ToolName
	}
	if event.EventType == "Notification" {
		return "notification:" + event.NotificationType
	}
	return "native-alert"
}

// mapEventToStatus converts a hook event to a detect.Status.
func (d *Daemon) mapEventToStatus(event ipc.HookEvent) detect.Status {
	status := d.mapEventToStatusRaw(event)
	// A subagent (Task tool delegation) fires its own terminal events on the
	// same session_id as the main agent. Only the main agent's own terminal
	// events (empty AgentID) may flip the session to done/stopped — a
	// subagent's Stop/agent_completed must not interrupt the main
	// delegation's running status.
	if event.AgentID != "" && (status == detect.StatusDone || status == detect.StatusStopped) {
		if d.cfg.Verbose {
			log.Printf("event: suppressing %s for subagent %s on session %s (status=%v)", event.EventType, event.AgentID, sessionKeyForEvent(event), status)
		}
		return detect.StatusUnknown
	}
	return status
}

func (d *Daemon) mapEventToStatusRaw(event ipc.HookEvent) detect.Status {
	switch event.EventType {
	case "SessionStart":
		return detect.StatusIdle
	case "UserPromptSubmit":
		return detect.StatusRunning
	case "Notification":
		switch event.NotificationType {
		case "permission_prompt", "agent_needs_input", "elicitation_dialog":
			return detect.StatusNeedInput
		case "agent_completed":
			return detect.StatusDone
		}
		// Unmapped notification subtypes are dropped.
		return detect.StatusUnknown
	case "PreToolUse":
		// Tools that pause for user input, same as permission prompts.
		switch event.ToolName {
		case "AskUserQuestion", "request_user_input", "EnterPlanMode", "ExitPlanMode":
			return detect.StatusNeedInput
		}
		// Any non-input tool means the agent is actively working.
		return detect.StatusRunning
	case "PermissionRequest":
		return detect.StatusNeedInput
	case "PermissionDenied":
		// Tool call denied by the auto-mode classifier; the agent proceeds.
		return detect.StatusRunning
	case "PostToolUse":
		return detect.StatusRunning
	case "PostToolUseFailure":
		// is_interrupt (user pressed ESC) is undocumented in the Claude hook
		// spec — it works today but is fragile. The pane-title sweep in
		// internal/frontend/tmux/frontend.go (Phase 3) remains the backstop.
		if event.IsInterrupt {
			return detect.StatusStopped
		}
		// Tool failed but the agent may retry — still running.
		return detect.StatusRunning
	case "Stop":
		// The main agent's turn can end while work it backgrounded (a
		// subagent/fork, a workflow, a background shell command, a monitor) is
		// still in flight. Claude Code reports that work on the Stop event, and
		// the session is paused waiting to be woken by it — not done (#698).
		// Restricted to main-agent events so a subagent-scoped Stop stays the
		// no-op the guard in mapEventToStatus makes it (#277).
		if event.BackgroundWork && event.AgentID == "" {
			if d.cfg.Verbose {
				log.Printf("event: Stop on session %s reports in-flight background work, holding running instead of done", sessionKeyForEvent(event))
			}
			return detect.StatusRunning
		}
		return detect.StatusDone
	case "StopFailure":
		// Turn died on an API error (rate_limit, overloaded, billing). Reuse
		// the Stopped state — there is no separate Attention status.
		return detect.StatusStopped
	default:
		return detect.StatusUnknown
	}
}
