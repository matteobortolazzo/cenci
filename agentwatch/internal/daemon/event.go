package daemon

import (
	"log"
	"strings"
	"unicode/utf8"

	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/detect"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/ipc"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/tmux"
)

const (
	logMaxLen      = 50
	taskNameMaxLen = 30
)

// truncateForLog shortens s to maxLen runes for safe log output,
// appending "..." if truncated.
func truncateForLog(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen]) + "..."
}

// sanitizeWindowName strips control characters and limits length for safe tmux display.
func sanitizeWindowName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7F {
			continue
		}
		b.WriteRune(r)
	}
	s := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(s) > 200 {
		runes := []rune(s)
		s = string(runes[:200])
	}
	return s
}

func compactTaskName(name string) string {
	s := strings.Join(strings.Fields(sanitizeWindowName(name)), " ")
	if utf8.RuneCountInString(s) <= taskNameMaxLen {
		return s
	}

	runes := []rune(s)
	cutoff := taskNameMaxLen - 3
	if cutoff < 1 {
		cutoff = 1
	}
	short := strings.TrimRight(string(runes[:cutoff]), " \t\n\r.,;:-")
	if short == "" {
		short = string(runes[:cutoff])
	}
	return short + "..."
}

func (d *Daemon) handleEvent(event ipc.HookEvent) {
	if event.TmuxPane == "" {
		if d.cfg.Verbose {
			log.Printf("event: dropping %s event with empty tmux_pane", event.EventType)
		}
		return
	}

	// Call ListPanes once for the entire event.
	panes, err := d.client.ListPanes()
	if err != nil {
		if d.cfg.Verbose {
			log.Printf("event: error listing panes for %s: %v", event.TmuxPane, err)
		}
		return
	}

	// Find paneInfo for the event's pane.
	var paneInfo *tmux.PaneInfo
	for i := range panes {
		if panes[i].PaneID == event.TmuxPane {
			paneInfo = &panes[i]
			break
		}
	}

	// Determine window target from fresh data or cache.
	var windowTarget string
	if paneInfo != nil {
		windowTarget = paneInfo.WindowTarget()
		d.panes[event.TmuxPane] = windowTarget
		d.migratePaneIfRenumbered(windowTarget, event.TmuxPane)
	} else {
		// Pane no longer in tmux. Use cached target for SessionEnd cleanup.
		windowTarget = d.panes[event.TmuxPane]
		if windowTarget == "" {
			if d.cfg.Verbose {
				log.Printf("event: pane %s not found in tmux", event.TmuxPane)
			}
			return
		}
	}

	// Handle SessionEnd: restore and clean up.
	if event.EventType == "SessionEnd" {
		ws := d.windows[windowTarget]
		if ws != nil && ws.PaneID != event.TmuxPane {
			// Late SessionEnd for a dead pane — don't touch the new window.
			delete(d.panes, event.TmuxPane)
			d.broadcast()
			return
		}
		// When the pane is gone, check if another pane now occupies the
		// cached window target (renumber-windows). If so, discard state
		// without restoring to avoid overwriting the surviving window.
		if paneInfo == nil {
			for _, p := range panes {
				if p.WindowTarget() == windowTarget {
					delete(d.windows, windowTarget)
					delete(d.panes, event.TmuxPane)
					d.broadcast()
					return
				}
			}
		}
		d.restoreWindow(windowTarget)
		delete(d.panes, event.TmuxPane)
		d.broadcast()
		return
	}

	// For non-SessionEnd events, we need paneInfo.
	if paneInfo == nil {
		if d.cfg.Verbose {
			log.Printf("event: pane %s not found in tmux", event.TmuxPane)
		}
		return
	}

	// Ensure window is tracked (first event or daemon restart).
	ws := d.windows[windowTarget]
	if ws != nil && ws.PaneID != event.TmuxPane {
		// Window index reused by a different pane — discard stale state.
		d.discardStaleWindow(windowTarget, ws.PaneID)
		ws = nil
	}
	if ws == nil {
		ws = d.trackWindow(windowTarget, paneInfo, event.SessionID)
		if ws == nil {
			return // tracking failed
		}
	}
	ws.SessionID = event.SessionID
	if event.Agent != "" {
		ws.Agent = event.Agent
	} else if ws.Agent == "" {
		ws.Agent = inferAgent(paneInfo.PaneCurrentCmd)
	}

	// Map event to status.
	status := d.mapEventToStatus(event)
	if status == detect.StatusUnknown {
		return
	}

	// Resolve task name from hook payloads or pane title (sanitize immediately
	// to protect downstream consumers: IPC broadcast, Waybar, and tmux rename).
	taskName := d.taskNameForEvent(event, ws, paneInfo)

	// Detect mid-session user renames.
	if !ws.ManuallyNamed && ws.LastSetName != "" && paneInfo.WindowName != ws.LastSetName {
		ws.ManuallyNamed = true
		ws.OriginalName = sanitizeWindowName(paneInfo.WindowName)
		if d.cfg.Verbose {
			log.Printf("window %s was renamed by user (%q → %q), will keep name", windowTarget, truncateForLog(ws.LastSetName, logMaxLen), truncateForLog(ws.OriginalName, logMaxLen))
		}
	}

	d.applyStatus(windowTarget, ws, status, taskName)
	d.broadcast()
}

func (d *Daemon) taskNameForEvent(event ipc.HookEvent, ws *windowState, paneInfo *tmux.PaneInfo) string {
	if ws.Agent == "codex" {
		return compactTaskName(detect.TaskName(paneInfo.PaneTitle))
	}
	if event.TaskName != "" {
		return compactTaskName(event.TaskName)
	}
	return compactTaskName(detect.TaskName(paneInfo.PaneTitle))
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

// applyStatus updates the window's style, symbol variable, and name for the given status.
func (d *Daemon) applyStatus(windowTarget string, ws *windowState, status detect.Status, taskName string) {
	ws.TaskName = taskName

	if ws.Status == status {
		return
	}

	ws.Status = status
	style, symbol := d.attrsFor(status)

	// Set styles FIRST so they apply even if rename fails.
	d.setWindowOpt(windowTarget, "window-status-style", style, "error setting window-status-style")
	d.setWindowOpt(windowTarget, "window-status-current-style", style, "error setting window-status-current-style")
	// User variables for custom status-format integration.
	d.setWindowOpt(windowTarget, "@agentwatch-style", style, "error setting @agentwatch-style")
	d.setWindowOpt(windowTarget, "@agentwatch-symbol", symbol, "error setting @agentwatch-symbol")

	// Build the display name (no symbol prefix — symbol is in @agentwatch-symbol and format string).
	var displayName string
	if ws.ManuallyNamed {
		displayName = ws.OriginalName
	} else if taskName != "" {
		displayName = taskName
	} else {
		displayName = ws.OriginalName
	}

	// Defensive: inputs should already be sanitized, but guard against
	// future code paths that bypass upstream sanitization.
	displayName = sanitizeWindowName(displayName)
	if err := d.client.RenameWindow(windowTarget, displayName); err != nil {
		if d.cfg.Verbose {
			log.Printf("error renaming window %s: %v", windowTarget, err)
		}
	}
	ws.LastSetName = displayName

	if d.cfg.Verbose {
		log.Printf("window %s: %s → %q (symbol: %s, style: %s)", windowTarget, status, truncateForLog(displayName, logMaxLen), symbol, style)
	}
}

// setWindowOpt sets a tmux window option and logs on failure when verbose mode is on.
func (d *Daemon) setWindowOpt(target, key, value, errMsg string) {
	if err := d.client.SetWindowOption(target, key, value); err != nil && d.cfg.Verbose {
		log.Printf("%s for %s: %v", errMsg, target, err)
	}
}

func (d *Daemon) attrsFor(s detect.Status) (style, symbol string) {
	switch s {
	case detect.StatusIdle:
		return d.cfg.StyleIdle, d.cfg.SymbolIdle
	case detect.StatusRunning:
		return d.cfg.StyleRunning, d.cfg.SymbolRunning
	case detect.StatusDone:
		return d.cfg.StyleDone, d.cfg.SymbolDone
	case detect.StatusStopped:
		return d.cfg.StyleStopped, d.cfg.SymbolStopped
	case detect.StatusNeedInput:
		return d.cfg.StyleNeedInput, d.cfg.SymbolNeedInput
	default:
		return "default", ""
	}
}
