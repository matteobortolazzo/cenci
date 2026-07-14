package daemon

import (
	"log"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/detect"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/frontend"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/ipc"
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

	if event.EventType == "SessionEnd" {
		delete(d.sessions, key)
		d.frontend.OnSessionEnd(sess)
		d.broadcast()
		return
	}

	status := d.mapEventToStatus(event)
	if status == detect.StatusUnknown {
		return
	}
	sess.Status = status

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

// mapEventToStatus converts a hook event to a detect.Status.
func (d *Daemon) mapEventToStatus(event ipc.HookEvent) detect.Status {
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
		return detect.StatusDone
	case "StopFailure":
		// Turn died on an API error (rate_limit, overloaded, billing). Reuse
		// the Stopped state — there is no separate Attention status.
		return detect.StatusStopped
	default:
		return detect.StatusUnknown
	}
}
