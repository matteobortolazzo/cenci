package daemon

import (
	"log"

	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/detect"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/frontend"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/ipc"
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
	} else if tn := frontend.CompactTaskName(event.TaskName); tn != "" {
		// No pane-derived name — the hook payload is the only task-name
		// source. Keep the last non-empty name across events without one.
		sess.TaskName = tn
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
		if event.NotificationType == "permission_prompt" {
			return detect.StatusNeedInput
		}
		return detect.StatusUnknown
	case "PreToolUse":
		// Tools that pause for user input, same as permission prompts.
		switch event.ToolName {
		case "AskUserQuestion", "EnterPlanMode", "ExitPlanMode":
			return detect.StatusNeedInput
		}
		// Any non-input tool means the agent is actively working.
		return detect.StatusRunning
	case "PermissionRequest":
		return detect.StatusNeedInput
	case "PostToolUse":
		return detect.StatusRunning
	case "PostToolUseFailure":
		if event.IsInterrupt {
			return detect.StatusStopped
		}
		// Tool failed but the agent may retry — still running.
		return detect.StatusRunning
	case "Stop":
		return detect.StatusDone
	default:
		return detect.StatusUnknown
	}
}
