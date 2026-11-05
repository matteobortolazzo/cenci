package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
)

// fleetWatchFile is the narrow slice of the fleet config that notify reads:
// the opt-in watch.excludeCwd list. Any decode error discards the whole list.
// Elements decode as *string because encoding/json turns a JSON null into a
// string as a silent no-op (""), which would let a bad element slip through.
type fleetWatchFile struct {
	Watch struct {
		ExcludeCwd []*string `json:"excludeCwd"`
	} `json:"watch"`
}

// loadExcludeCwd reads watch.excludeCwd from the fleet config at path. It
// fails open to visibility: an empty path, an unreadable file, malformed JSON
// or a wrongly typed key or element (a null included) yields nil, silently.
func loadExcludeCwd(path string) []string {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg fleetWatchFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	entries := make([]string, 0, len(cfg.Watch.ExcludeCwd))
	for _, entry := range cfg.Watch.ExcludeCwd {
		if entry == nil {
			return nil
		}
		entries = append(entries, *entry)
	}
	return entries
}

// cwdExcluded reports whether cwd equals a listed absolute entry or sits under
// one at a path-segment boundary, after filepath.Clean on both sides. It is
// deliberately narrow: an empty or relative cwd, and empty or relative
// entries, never match, so everything else stays visible. Symlinks are not
// resolved and case is not folded.
func cwdExcluded(cwd string, entries []string) bool {
	if cwd == "" || !filepath.IsAbs(cwd) {
		return false
	}
	cwd = filepath.Clean(cwd)
	sep := string(filepath.Separator)
	for _, entry := range entries {
		if entry == "" || !filepath.IsAbs(entry) {
			continue
		}
		entry = filepath.Clean(entry)
		if cwd == entry || entry == sep || strings.HasPrefix(cwd, entry+sep) {
			return true
		}
	}
	return false
}

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
		// Cwd is only compared against the opt-in watch.excludeCwd list; it
		// never reaches IPC.
		Cwd string `json:"cwd"`
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

	// Opt-in exclusion of headless probe sessions (watch.excludeCwd), decided
	// per event. Returning before DeliverEvent also keeps an excluded event
	// from autostarting a missing daemon. The config is read only for an
	// absolute cwd; every failure leaves the event visible.
	if hookInput.Cwd != "" && filepath.IsAbs(hookInput.Cwd) &&
		cwdExcluded(hookInput.Cwd, loadExcludeCwd(run.DefaultConfigPath())) {
		return
	}

	// Delivery is silent and non-fatal. For the default socket it starts a
	// missing daemon on demand and retries this exact event once.
	daemon.DeliverEvent(*socketPath, event)
}
