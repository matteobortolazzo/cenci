package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
)

// terminalBackgroundTaskStatus lists the background-task states that cannot
// wake the session on their own. Claude Code's task status enum is
// pending|running|completed|failed|killed|paused, and a Stop event's
// background_tasks array is already documented to carry only in-flight work
// ("running/pending + backgrounded"). So this excludes exactly the three
// finished states plus paused — a paused task resumes only when the user or
// the agent resumes it, and either way that resumption fires its own events
// (#1079) — and treats every other value, including one added by a future
// Claude Code release, as still in flight: a session wrongly held at running
// now recovers on its own after frontend.BackgroundHoldTTL, while one wrongly
// marked done stays wrong until the next event (#698).
var terminalBackgroundTaskStatus = map[string]bool{
	"completed": true,
	"failed":    true,
	"killed":    true,
	"paused":    true,
}

func runNotify(args []string) {
	fs := flag.NewFlagSet("notify", flag.ExitOnError)
	socketPath := fs.String("event-socket", ipc.DefaultEventSocketPath(), "event socket path")
	agent := fs.String("agent", "", "agent name (claude, codex, or opencode)")
	_ = fs.Parse(args)

	// Read hook JSON from stdin.
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(0) // fail silently
	}

	// Parse the hook input to extract event type and relevant fields.
	var hookInput struct {
		HookEventName string `json:"hook_event_name"`
		SessionID     string `json:"session_id"`
		// Notification fields
		Notification struct {
			Type string `json:"type"`
		} `json:"notification"`
		// PreToolUse fields
		ToolName string `json:"tool_name"`
		// UserPromptSubmit field. It is reduced to a compact label before IPC.
		Prompt string `json:"prompt"`
		// PostToolUseFailure fields
		IsInterrupt bool `json:"is_interrupt"`
		// AgentID is set when the hook fires inside a subagent (Task tool) call.
		AgentID string `json:"agent_id"`
		// SessionEnd field: why the session ended. "clear" and "resume" are
		// continuations — a successor session keeps the pane — and route to
		// the window handoff instead of a full teardown (#707).
		Reason string `json:"reason"`
		// Stop field. Only each task's status is read: descriptions and shell
		// command lines stay local, like the raw prompt above.
		BackgroundTasks []struct {
			Status string `json:"status"`
		} `json:"background_tasks"`
		// Stop field. ScheduleWakeup and /loop timers are reported here, not
		// in background_tasks. Entries carry no status — presence alone means
		// the session will be woken (#705). Only the count is read: schedules
		// and prompts stay local, like the raw prompt above.
		SessionCrons []json.RawMessage `json:"session_crons"`
	}
	if err := json.Unmarshal(data, &hookInput); err != nil {
		os.Exit(0) // fail silently
	}

	// TMUX_PANE may be empty: sessions outside tmux (plain terminals,
	// sandbox) are still tracked by the daemon as paneless sessions.
	tmuxPane := os.Getenv("TMUX_PANE")

	event := ipc.HookEvent{
		EventType:        hookInput.HookEventName,
		SessionID:        hookInput.SessionID,
		Agent:            strings.ToLower(strings.TrimSpace(*agent)),
		TmuxPane:         tmuxPane,
		NotificationType: hookInput.Notification.Type,
		ToolName:         hookInput.ToolName,
		IsInterrupt:      hookInput.IsInterrupt,
		AgentID:          hookInput.AgentID,
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
	}
	if event.EventType == "SessionEnd" {
		event.SessionEndReason = hookInput.Reason
	}
	for _, task := range hookInput.BackgroundTasks {
		if !terminalBackgroundTaskStatus[task.Status] {
			event.BackgroundWork = true
			break
		}
	}
	if len(hookInput.SessionCrons) > 0 {
		event.BackgroundWork = true
	}
	if (event.Agent == "codex" || event.Agent == "opencode") && event.EventType == "UserPromptSubmit" {
		event.TaskName = frontend.PromptTaskName(hookInput.Prompt)
	}

	// Delivery is silent and non-fatal. For the default socket it starts a
	// missing daemon on demand and retries this exact event once.
	daemon.DeliverEvent(*socketPath, event)
}
