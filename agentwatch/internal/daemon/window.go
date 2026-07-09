package daemon

import (
	"log"
	"strings"
	"unicode/utf8"

	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/detect"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/tmux"
)

// windowState tracks the original name and current status for a managed window.
type windowState struct {
	OriginalName          string
	OriginalStyle         string // saved window-status-style
	OriginalCurrentStyle  string // saved window-status-current-style
	OriginalFormat        string // saved window-status-format
	OriginalCurrentFormat string // saved window-status-current-format
	Status                detect.Status
	TaskName              string // current task name from pane title (for IPC export)
	ManuallyNamed         bool   // true = user set name manually, don't touch
	LastSetName           string // last name agentwatch set, for detecting mid-session renames
	PaneID                string // tmux pane ID (e.g. %5) for sweep validation
	SessionID             string // agent session ID for correlating events
	Agent                 string // agent name, if known
}

const symbolPrefix = "#{@agentwatch-symbol} "

// trackWindow sets up tracking for a new window using pre-fetched pane info.
func (d *Daemon) trackWindow(windowTarget string, paneInfo *tmux.PaneInfo, sessionID string) *windowState {
	currentName := paneInfo.WindowName
	taskName := sanitizeWindowName(detect.TaskName(paneInfo.PaneTitle))

	// Strip residual agentwatch prefix from a previous daemon run.
	currentName = d.stripAgentwatchPrefix(currentName)
	currentName = sanitizeWindowName(currentName)

	// Check if manually named.
	autoRename, err := d.client.GetWindowOption(windowTarget, "automatic-rename")
	manuallyNamed := false
	if err == nil && autoRename == "off" {
		r, _ := utf8.DecodeRuneInString(currentName)
		manuallyNamed = currentName != taskName && !detect.IsStatusSymbol(r)
	}

	// Save original styles.
	origStyle, err := d.client.GetWindowOption(windowTarget, "window-status-style")
	if err != nil {
		origStyle = "default"
	}
	origCurrentStyle, err := d.client.GetWindowOption(windowTarget, "window-status-current-style")
	if err != nil {
		origCurrentStyle = "default"
	}

	// Save original format strings.
	origFormat, err := d.client.GetWindowOption(windowTarget, "window-status-format")
	if err != nil {
		origFormat = ""
	}
	origCurrentFormat, err := d.client.GetWindowOption(windowTarget, "window-status-current-format")
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
	d.windows[windowTarget] = ws

	// Disable automatic-rename for all tracked windows (we manage names now).
	d.setWindowOpt(windowTarget, "automatic-rename", "off", "error disabling automatic-rename")

	// Prepend symbol variable to format strings for default-format users.
	if origFormat != "" {
		d.setWindowOpt(windowTarget, "window-status-format", symbolPrefix+origFormat, "error setting window-status-format")
	}
	if origCurrentFormat != "" {
		d.setWindowOpt(windowTarget, "window-status-current-format", symbolPrefix+origCurrentFormat, "error setting window-status-current-format")
	}

	if d.cfg.Verbose {
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
func (d *Daemon) stripAgentwatchPrefix(name string) string {
	symbols := []string{d.cfg.SymbolIdle, d.cfg.SymbolRunning, d.cfg.SymbolDone, d.cfg.SymbolStopped, d.cfg.SymbolNeedInput}
	for _, sym := range symbols {
		prefix := sym + " "
		for strings.HasPrefix(name, prefix) {
			name = strings.TrimPrefix(name, prefix)
		}
	}
	return name
}

func (d *Daemon) restoreWindow(windowTarget string) {
	ws, ok := d.windows[windowTarget]
	if !ok {
		return
	}

	// Restore original window name.
	if err := d.client.RenameWindow(windowTarget, ws.OriginalName); err != nil {
		if d.cfg.Verbose {
			log.Printf("error restoring window %s: %v", windowTarget, err)
		}
	}

	d.restoreWindowIndicators(windowTarget, ws)

	// Re-enable automatic-rename only for non-manually-named windows.
	if !ws.ManuallyNamed {
		d.setWindowOpt(windowTarget, "automatic-rename", "on", "error re-enabling automatic-rename")
	}

	if d.cfg.Verbose {
		log.Printf("restored window %s to %q", windowTarget, truncateForLog(ws.OriginalName, logMaxLen))
	}

	delete(d.windows, windowTarget)
}

// restoreWindowIndicators restores the original styles, format strings, and
// clears agentwatch user variables for a tracked window.
func (d *Daemon) restoreWindowIndicators(target string, ws *windowState) {
	// Restore original styles.
	d.setWindowOpt(target, "window-status-style", ws.OriginalStyle, "error restoring window-status-style")
	d.setWindowOpt(target, "window-status-current-style", ws.OriginalCurrentStyle, "error restoring window-status-current-style")

	// Restore original format strings.
	if ws.OriginalFormat != "" {
		d.setWindowOpt(target, "window-status-format", ws.OriginalFormat, "error restoring window-status-format")
	}
	if ws.OriginalCurrentFormat != "" {
		d.setWindowOpt(target, "window-status-current-format", ws.OriginalCurrentFormat, "error restoring window-status-current-format")
	}

	// Clear agentwatch user variables.
	d.setWindowOpt(target, "@agentwatch-style", "", "error clearing @agentwatch-style")
	d.setWindowOpt(target, "@agentwatch-symbol", "", "error clearing @agentwatch-symbol")
}

// sweepStale migrates renumbered panes and cleans up panes that no longer exist.
func (d *Daemon) sweepStale() {
	if len(d.windows) == 0 {
		return
	}

	panes, err := d.client.ListPanes()
	if err != nil {
		if d.cfg.Verbose {
			log.Printf("sweep: error listing panes: %v", err)
		}
		return
	}

	// Build paneID → current target map.
	paneToTarget := make(map[string]string)
	existing := make(map[string]bool)
	currentPaneForWindow := make(map[string]string)
	for _, p := range panes {
		paneToTarget[p.PaneID] = p.WindowTarget()
		existing[p.PaneID] = true
		currentPaneForWindow[p.WindowTarget()] = p.PaneID
	}

	// Phase 1: Migrate renumbered panes (pane alive but at a different target).
	type migrationInfo struct {
		oldTarget string
		newTarget string
		ws        *windowState
	}
	var migrations []migrationInfo
	for wt, ws := range d.windows {
		if newTarget, ok := paneToTarget[ws.PaneID]; ok && newTarget != wt {
			migrations = append(migrations, migrationInfo{oldTarget: wt, newTarget: newTarget, ws: ws})
		}
	}
	// Remove all migrating entries from old positions first.
	for _, m := range migrations {
		delete(d.windows, m.oldTarget)
	}
	// Then insert at new positions (discarding any stale occupants).
	for _, m := range migrations {
		if existing, ok := d.windows[m.newTarget]; ok && existing.PaneID != m.ws.PaneID {
			d.discardStaleWindow(m.newTarget, existing.PaneID)
		}
		d.windows[m.newTarget] = m.ws
		d.panes[m.ws.PaneID] = m.newTarget
		if d.cfg.Verbose {
			log.Printf("sweep: pane %s renumbered from %s to %s, migrating state", m.ws.PaneID, m.oldTarget, m.newTarget)
		}
	}

	// Phase 2: Clean up truly stale entries (pane gone).
	var stale []string
	for wt, ws := range d.windows {
		if !existing[ws.PaneID] {
			stale = append(stale, wt)
		}
	}

	for _, wt := range stale {
		ws := d.windows[wt]
		if d.cfg.Verbose {
			log.Printf("sweep: pane %s gone, cleaning up window %s", ws.PaneID, wt)
		}
		delete(d.panes, ws.PaneID)
		if currentPane, ok := currentPaneForWindow[wt]; ok && currentPane != ws.PaneID {
			// Window target reused by another pane — discard without restore.
			delete(d.windows, wt)
		} else {
			d.restoreWindow(wt)
		}
	}

	// Phase 3: Detect idle pane titles for windows still marked Running.
	// When the user presses ESC during pure text generation (no tool running),
	// there's no hook event. The pane title reverts to an idle marker (✶ ✻ ✳)
	// while we still think it's Running.
	paneByID := make(map[string]*tmux.PaneInfo, len(panes))
	for i := range panes {
		paneByID[panes[i].PaneID] = &panes[i]
	}
	var idleDetected int
	var exitedDetected int
	for wt, ws := range d.windows {
		p, ok := paneByID[ws.PaneID]
		if !ok {
			continue
		}
		if ws.Agent != "" && ws.Status != detect.StatusRunning && ws.Status != detect.StatusNeedInput && !agentCommandMatches(ws.Agent, p.PaneCurrentCmd) {
			if d.cfg.Verbose {
				log.Printf("sweep: pane %s no longer running %s (current command %q), restoring window %s", ws.PaneID, ws.Agent, p.PaneCurrentCmd, wt)
			}
			delete(d.panes, ws.PaneID)
			d.restoreWindow(wt)
			exitedDetected++
			continue
		}
		if ws.Status != detect.StatusRunning {
			continue
		}
		r, _ := utf8.DecodeRuneInString(p.PaneTitle)
		if detect.IsStatusSymbol(r) && !detect.IsBraille(r) {
			// Pane title shows an idle marker (✶ ✻ ✳) — Claude returned to prompt.
			taskName := sanitizeWindowName(detect.TaskName(p.PaneTitle))
			d.applyStatus(wt, ws, detect.StatusStopped, taskName)
			idleDetected++
			if d.cfg.Verbose {
				log.Printf("sweep: pane %s idle (title %q), setting stopped", ws.PaneID, truncateForLog(p.PaneTitle, logMaxLen))
			}
		}
	}

	if len(migrations) > 0 || len(stale) > 0 || idleDetected > 0 || exitedDetected > 0 {
		d.broadcast()
	}
}

// discardStaleWindow removes stale state for a window target without restoring
// (the old window is gone, so there's nothing to restore).
func (d *Daemon) discardStaleWindow(windowTarget, stalePaneID string) {
	if d.cfg.Verbose {
		log.Printf("discarding stale state for window %s (pane %s gone)", windowTarget, stalePaneID)
	}
	delete(d.panes, stalePaneID)
	delete(d.windows, windowTarget)
}

// migratePaneIfRenumbered checks if paneID is tracked at a different window target
// and migrates state to newTarget. This handles tmux renumber-windows where a pane
// moves to a different window index.
func (d *Daemon) migratePaneIfRenumbered(newTarget, paneID string) {
	for oldTarget, ws := range d.windows {
		if ws.PaneID == paneID && oldTarget != newTarget {
			if d.cfg.Verbose {
				log.Printf("pane %s renumbered from %s to %s, migrating state", paneID, oldTarget, newTarget)
			}
			// If newTarget is occupied by a different pane's state, discard it.
			if existing, ok := d.windows[newTarget]; ok && existing.PaneID != paneID {
				d.discardStaleWindow(newTarget, existing.PaneID)
			}
			d.windows[newTarget] = ws
			delete(d.windows, oldTarget)
			return
		}
	}
}
