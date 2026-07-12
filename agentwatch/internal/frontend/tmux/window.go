package tmux

import (
	"log"
	"strings"
	"unicode/utf8"

	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/detect"
	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/frontend"
	tmuxc "github.com/matteobortolazzo/agent-stack/agentwatch/internal/tmux"
)

const logMaxLen = 50

// truncateForLog shortens s to maxLen runes for safe log output,
// appending "..." if truncated.
func truncateForLog(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen]) + "..."
}

// windowState tracks the original name and current status for a managed window.
type windowState struct {
	OriginalName          string
	OriginalStyle         string // saved window-status-style
	OriginalCurrentStyle  string // saved window-status-current-style
	OriginalFormat        string // saved window-status-format
	OriginalCurrentFormat string // saved window-status-current-format
	Status                detect.Status
	TaskName              string // current task name from pane title (for WindowInfo)
	ManuallyNamed         bool   // true = user set name manually, don't touch
	LastSetName           string // last name agentwatch set, for detecting mid-session renames
	PaneID                string // tmux pane ID (e.g. %5) for sweep validation
	SessionID             string // agent session ID for correlating events
	SessionKey            string // daemon core session key owning this window
	Agent                 string // agent name, if known
}

const symbolPrefix = "#{@agentwatch-symbol} "

// trackWindow sets up tracking for a new window using pre-fetched pane info.
func (f *Frontend) trackWindow(windowTarget string, paneInfo *tmuxc.PaneInfo, sessionID string) *windowState {
	currentName := paneInfo.WindowName
	taskName := frontend.SanitizeName(detect.TaskName(paneInfo.PaneTitle))

	// Strip residual agentwatch prefix from a previous daemon run.
	currentName = f.stripAgentwatchPrefix(currentName)
	currentName = frontend.SanitizeName(currentName)

	// Check if manually named.
	autoRename, err := f.client.GetWindowOption(windowTarget, "automatic-rename")
	manuallyNamed := false
	if err == nil && autoRename == "off" {
		r, _ := utf8.DecodeRuneInString(currentName)
		manuallyNamed = currentName != taskName && !detect.IsStatusSymbol(r)
	}

	// Save original styles.
	origStyle, err := f.client.GetWindowOption(windowTarget, "window-status-style")
	if err != nil {
		origStyle = "default"
	}
	origCurrentStyle, err := f.client.GetWindowOption(windowTarget, "window-status-current-style")
	if err != nil {
		origCurrentStyle = "default"
	}

	// Save original format strings.
	origFormat, err := f.client.GetWindowOption(windowTarget, "window-status-format")
	if err != nil {
		origFormat = ""
	}
	origCurrentFormat, err := f.client.GetWindowOption(windowTarget, "window-status-current-format")
	if err != nil {
		origCurrentFormat = ""
	}

	ws := &windowState{
		OriginalName:          currentName,
		OriginalStyle:         origStyle,
		OriginalCurrentStyle:  origCurrentStyle,
		OriginalFormat:        origFormat,
		OriginalCurrentFormat: origCurrentFormat,
		ManuallyNamed:         manuallyNamed,
		PaneID:                paneInfo.PaneID,
		SessionID:             sessionID,
		Agent:                 inferAgent(paneInfo.PaneCurrentCmd),
	}
	f.windows[windowTarget] = ws

	// Disable automatic-rename for all tracked windows (we manage names now).
	f.setWindowOpt(windowTarget, "automatic-rename", "off", "error disabling automatic-rename")

	// Prepend symbol variable to format strings for default-format users.
	if origFormat != "" {
		f.setWindowOpt(windowTarget, "window-status-format", symbolPrefix+origFormat, "error setting window-status-format")
	}
	if origCurrentFormat != "" {
		f.setWindowOpt(windowTarget, "window-status-current-format", symbolPrefix+origCurrentFormat, "error setting window-status-current-format")
	}

	if f.cfg.Verbose {
		if manuallyNamed {
			log.Printf("tracking manually-named window %s (%q)", windowTarget, truncateForLog(currentName, logMaxLen))
		} else {
			log.Printf("tracking window %s (original name: %q)", windowTarget, truncateForLog(currentName, logMaxLen))
		}
	}

	return ws
}

func inferAgent(cmd string) string {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "claude", "claude-code":
		return "claude"
	case "codex", "codex-cli":
		return "codex"
	default:
		return ""
	}
}

func agentCommandMatches(agent, cmd string) bool {
	inferred := inferAgent(cmd)
	if agent == "" {
		return inferred != ""
	}
	return inferred == agent
}

// stripAgentwatchPrefix removes any leading agentwatch symbol + space from a window name.
// This handles daemon restart where a residual symbol may be embedded in the name.
func (f *Frontend) stripAgentwatchPrefix(name string) string {
	symbols := []string{f.cfg.SymbolIdle, f.cfg.SymbolRunning, f.cfg.SymbolDone, f.cfg.SymbolStopped, f.cfg.SymbolNeedInput}
	for _, sym := range symbols {
		prefix := sym + " "
		for strings.HasPrefix(name, prefix) {
			name = strings.TrimPrefix(name, prefix)
		}
	}
	return name
}

// forgetWindow removes a window's tracking state and its session-key mapping.
func (f *Frontend) forgetWindow(windowTarget string) {
	if ws, ok := f.windows[windowTarget]; ok {
		if ws.SessionKey != "" && f.sessionKeys[ws.SessionKey] == windowTarget {
			delete(f.sessionKeys, ws.SessionKey)
		}
	}
	delete(f.windows, windowTarget)
}

func (f *Frontend) restoreWindow(windowTarget string) {
	ws, ok := f.windows[windowTarget]
	if !ok {
		return
	}

	// Restore original window name.
	if err := f.client.RenameWindow(windowTarget, ws.OriginalName); err != nil {
		if f.cfg.Verbose {
			log.Printf("error restoring window %s: %v", windowTarget, err)
		}
	}

	f.restoreWindowIndicators(windowTarget, ws)

	// Re-enable automatic-rename only for non-manually-named windows.
	if !ws.ManuallyNamed {
		f.setWindowOpt(windowTarget, "automatic-rename", "on", "error re-enabling automatic-rename")
	}

	if f.cfg.Verbose {
		log.Printf("restored window %s to %q", windowTarget, truncateForLog(ws.OriginalName, logMaxLen))
	}

	f.forgetWindow(windowTarget)
}

// restoreWindowIndicators restores the original styles, format strings, and
// clears agentwatch user variables for a tracked window.
func (f *Frontend) restoreWindowIndicators(target string, ws *windowState) {
	// Restore original styles.
	f.setWindowOpt(target, "window-status-style", ws.OriginalStyle, "error restoring window-status-style")
	f.setWindowOpt(target, "window-status-current-style", ws.OriginalCurrentStyle, "error restoring window-status-current-style")

	// Restore original format strings.
	if ws.OriginalFormat != "" {
		f.setWindowOpt(target, "window-status-format", ws.OriginalFormat, "error restoring window-status-format")
	}
	if ws.OriginalCurrentFormat != "" {
		f.setWindowOpt(target, "window-status-current-format", ws.OriginalCurrentFormat, "error restoring window-status-current-format")
	}

	// Clear agentwatch user variables.
	f.setWindowOpt(target, "@agentwatch-style", "", "error clearing @agentwatch-style")
	f.setWindowOpt(target, "@agentwatch-symbol", "", "error clearing @agentwatch-symbol")
}

// discardStaleWindow removes stale state for a window target without restoring
// (the old window is gone, so there's nothing to restore).
func (f *Frontend) discardStaleWindow(windowTarget, stalePaneID string) {
	if f.cfg.Verbose {
		log.Printf("discarding stale state for window %s (pane %s gone)", windowTarget, stalePaneID)
	}
	delete(f.panes, stalePaneID)
	f.forgetWindow(windowTarget)
}

// migratePaneIfRenumbered checks if paneID is tracked at a different window target
// and migrates state to newTarget. This handles tmux renumber-windows where a pane
// moves to a different window index. It returns the session keys of any stale
// occupant states discarded along the way.
func (f *Frontend) migratePaneIfRenumbered(newTarget, paneID string) []string {
	for oldTarget, ws := range f.windows {
		if ws.PaneID == paneID && oldTarget != newTarget {
			if f.cfg.Verbose {
				log.Printf("pane %s renumbered from %s to %s, migrating state", paneID, oldTarget, newTarget)
			}
			var evicted []string
			// If newTarget is occupied by a different pane's state, discard it.
			if existing, ok := f.windows[newTarget]; ok && existing.PaneID != paneID {
				if existing.SessionKey != "" {
					evicted = append(evicted, existing.SessionKey)
				}
				f.discardStaleWindow(newTarget, existing.PaneID)
			}
			f.windows[newTarget] = ws
			delete(f.windows, oldTarget)
			if ws.SessionKey != "" {
				f.sessionKeys[ws.SessionKey] = newTarget
			}
			return evicted
		}
	}
	return nil
}

// applyStatus updates the window's style, symbol variable, and name for the given status.
func (f *Frontend) applyStatus(windowTarget string, ws *windowState, status detect.Status, taskName string) {
	taskChanged := ws.TaskName != taskName
	ws.TaskName = taskName

	if ws.Status == status && !taskChanged {
		return
	}

	ws.Status = status
	style, symbol := f.attrsFor(status)

	// Set styles FIRST so they apply even if rename fails.
	f.setWindowOpt(windowTarget, "window-status-style", style, "error setting window-status-style")
	f.setWindowOpt(windowTarget, "window-status-current-style", style, "error setting window-status-current-style")
	// User variables for custom status-format integration.
	f.setWindowOpt(windowTarget, "@agentwatch-style", style, "error setting @agentwatch-style")
	f.setWindowOpt(windowTarget, "@agentwatch-symbol", symbol, "error setting @agentwatch-symbol")

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
	displayName = frontend.SanitizeName(displayName)
	if err := f.client.RenameWindow(windowTarget, displayName); err != nil {
		if f.cfg.Verbose {
			log.Printf("error renaming window %s: %v", windowTarget, err)
		}
	}
	ws.LastSetName = displayName

	if f.cfg.Verbose {
		log.Printf("window %s: %s → %q (symbol: %s, style: %s)", windowTarget, status, truncateForLog(displayName, logMaxLen), symbol, style)
	}
}

// setWindowOpt sets a tmux window option and logs on failure when verbose mode is on.
func (f *Frontend) setWindowOpt(target, key, value, errMsg string) {
	if err := f.client.SetWindowOption(target, key, value); err != nil && f.cfg.Verbose {
		log.Printf("%s for %s: %v", errMsg, target, err)
	}
}

func (f *Frontend) attrsFor(s detect.Status) (style, symbol string) {
	switch s {
	case detect.StatusIdle:
		return f.cfg.StyleIdle, f.cfg.SymbolIdle
	case detect.StatusRunning:
		return f.cfg.StyleRunning, f.cfg.SymbolRunning
	case detect.StatusDone:
		return f.cfg.StyleDone, f.cfg.SymbolDone
	case detect.StatusStopped:
		return f.cfg.StyleStopped, f.cfg.SymbolStopped
	case detect.StatusNeedInput:
		return f.cfg.StyleNeedInput, f.cfg.SymbolNeedInput
	default:
		return "default", ""
	}
}
