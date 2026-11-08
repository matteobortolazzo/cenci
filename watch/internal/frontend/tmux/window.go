package tmux

import (
	"log"
	"strings"
	"unicode/utf8"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	tmuxc "github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
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

// windowKey identifies a tracked window: its tmux server socket and its
// session:index target. window targets repeat across servers, so the target
// alone is not an identity. An empty socket is the ambient (default) server.
type windowKey struct {
	socket string
	target string
}

// String is the log form: the bare target for the ambient server, so its log
// text is unchanged, and target@socket otherwise.
func (k windowKey) String() string {
	if k.socket == "" {
		return k.target
	}
	return k.target + "@" + k.socket
}

// paneKey identifies a pane: pane ids ("%5") repeat across tmux servers, so a
// pane is (socket, id).
type paneKey struct {
	socket string
	pane   string
}

// String is the log form: the bare pane id for the ambient server, and
// pane@socket otherwise.
func (k paneKey) String() string {
	if k.socket == "" {
		return k.pane
	}
	return k.pane + "@" + k.socket
}

// windowState tracks the original name and current status for a managed window.
// The window's socket is the socket of its windowKey.
type windowState struct {
	OriginalName          string
	OriginalStyle         string // saved window-status-style
	OriginalCurrentStyle  string // saved window-status-current-style
	OriginalFormat        string // saved window-status-format
	OriginalCurrentFormat string // saved window-status-current-format
	Status                detect.Status
	TaskName              string // current task name from pane title (for WindowInfo)
	ManuallyNamed         bool   // true = user set name manually, don't touch
	LastSetName           string // last name cenci set, for detecting mid-session renames
	PaneID                string // tmux pane ID (e.g. %5) for sweep validation
	SessionID             string // agent session ID for correlating events
	SessionKey            string // daemon core session key owning this window
	Agent                 string // agent name, if known
	RepoDir               string // raw @cenci-repo-dir, read once when tracking began ("" when unset)
}

const symbolPrefix = "#{@cenci-symbol} "

// trackWindow sets up tracking for a new window using pre-fetched pane info.
// Every tmux call runs on the window's own server.
func (f *Frontend) trackWindow(wk windowKey, paneInfo *tmuxc.PaneInfo, sessionID string) *windowState {
	currentName := paneInfo.WindowName
	taskName := frontend.SanitizeName(detect.TaskName(paneInfo.PaneTitle))
	if isGraphicsProbeTitle(paneInfo.PaneTitle) {
		taskName = "Claude Code"
	}

	// Strip residual cenci prefix from a previous daemon run.
	currentName = f.stripCenciPrefix(currentName)
	currentName = frontend.SanitizeName(currentName)
	probeName := isGraphicsProbeTitle(currentName)
	if probeName {
		currentName = "Claude Code"
	}

	// Check if manually named. A name still leading with an agent status
	// symbol came from a previous cenci run, not from the user.
	autoRename, err := f.client.GetWindowOption(wk.socket, wk.target, "automatic-rename")
	manuallyNamed := false
	if err == nil && autoRename == "off" && !probeName {
		r, _ := utf8.DecodeRuneInString(currentName)
		manuallyNamed = currentName != taskName && !detect.IsStatusSymbol(r)
	}

	// Drop a residual agent status marker (#1039) so it is not preserved as
	// the original name and restored on cleanup. This has to run after the
	// manual-name check above, which reads that very marker as its evidence
	// that cenci — not the user — chose the name.
	currentName = frontend.SanitizeName(detect.TaskName(currentName))

	// Save original styles.
	origStyle, err := f.client.GetWindowOption(wk.socket, wk.target, "window-status-style")
	if err != nil {
		origStyle = "default"
	}
	origCurrentStyle, err := f.client.GetWindowOption(wk.socket, wk.target, "window-status-current-style")
	if err != nil {
		origCurrentStyle = "default"
	}

	// Save original format strings.
	origFormat, err := f.client.GetWindowOption(wk.socket, wk.target, "window-status-format")
	if err != nil {
		origFormat = ""
	}
	origCurrentFormat, err := f.client.GetWindowOption(wk.socket, wk.target, "window-status-current-format")
	if err != nil {
		origCurrentFormat = ""
	}

	// Record the launch directory `cenci run` wrote. An unset or unreadable
	// option leaves it empty, which the window-cleanup pass treats as "repo
	// unknown".
	repoDir, err := f.client.GetWindowOption(wk.socket, wk.target, frontend.RepoDirOption)
	if err != nil {
		repoDir = ""
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
		RepoDir:               repoDir,
	}
	f.windows[wk] = ws

	// A server this frontend has not set headroom on yet gets the last rendered
	// values now, not at the next reconciler tick.
	f.syncHeadroom(wk.socket)

	// Disable automatic-rename for all tracked windows (we manage names now).
	f.setWindowOpt(wk.socket, wk.target, "automatic-rename", "off", "error disabling automatic-rename")

	// Prepend symbol variable to format strings for default-format users.
	if origFormat != "" {
		f.setWindowOpt(wk.socket, wk.target, "window-status-format", symbolPrefix+origFormat, "error setting window-status-format")
	}
	if origCurrentFormat != "" {
		f.setWindowOpt(wk.socket, wk.target, "window-status-current-format", symbolPrefix+origCurrentFormat, "error setting window-status-current-format")
	}

	if f.cfg.Verbose {
		if manuallyNamed {
			log.Printf("tracking manually-named window %s (%q)", wk, truncateForLog(currentName, logMaxLen))
		} else {
			log.Printf("tracking window %s (original name: %q)", wk, truncateForLog(currentName, logMaxLen))
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
	case "opencode", "bun", "node":
		return "opencode"
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

// stripCenciPrefix removes any leading cenci symbol + space from a window name.
// This handles daemon restart where a residual symbol may be embedded in the name.
func (f *Frontend) stripCenciPrefix(name string) string {
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
func (f *Frontend) forgetWindow(wk windowKey) {
	if ws, ok := f.windows[wk]; ok {
		if ws.SessionKey != "" && f.sessionKeys[ws.SessionKey] == wk {
			delete(f.sessionKeys, ws.SessionKey)
		}
	}
	delete(f.windows, wk)
}

func (f *Frontend) restoreWindow(wk windowKey) {
	ws, ok := f.windows[wk]
	if !ok {
		return
	}

	// The pane may disappear after ListPanes and its window index may then
	// belong to another session. A pane ID cannot retarget that survivor.
	target := ws.PaneID
	// Restore original window name.
	if err := f.client.RenameWindow(wk.socket, target, ws.OriginalName); err != nil {
		if f.cfg.Verbose {
			log.Printf("error restoring window %s: %v", wk, err)
		}
	}

	f.restoreWindowIndicators(wk.socket, target, ws)

	// Re-enable automatic-rename only for non-manually-named windows.
	if !ws.ManuallyNamed {
		f.setWindowOpt(wk.socket, target, "automatic-rename", "on", "error re-enabling automatic-rename")
	}

	if f.cfg.Verbose {
		log.Printf("restored window %s to %q", wk, truncateForLog(ws.OriginalName, logMaxLen))
	}

	f.forgetWindow(wk)
}

// restoreWindowIndicators restores the original styles, format strings, and
// clears cenci user variables for a tracked window.
func (f *Frontend) restoreWindowIndicators(socket, target string, ws *windowState) {
	// Restore original styles.
	f.setWindowOpt(socket, target, "window-status-style", ws.OriginalStyle, "error restoring window-status-style")
	f.setWindowOpt(socket, target, "window-status-current-style", ws.OriginalCurrentStyle, "error restoring window-status-current-style")

	// Restore original format strings.
	if ws.OriginalFormat != "" {
		f.setWindowOpt(socket, target, "window-status-format", ws.OriginalFormat, "error restoring window-status-format")
	}
	if ws.OriginalCurrentFormat != "" {
		f.setWindowOpt(socket, target, "window-status-current-format", ws.OriginalCurrentFormat, "error restoring window-status-current-format")
	}

	// Clear cenci user variables.
	f.setWindowOpt(socket, target, "@cenci-style", "", "error clearing @cenci-style")
	f.setWindowOpt(socket, target, "@cenci-symbol", "", "error clearing @cenci-symbol")
}

// discardStaleWindow removes stale state for a window without restoring
// (the old window is gone, so there's nothing to restore).
func (f *Frontend) discardStaleWindow(wk windowKey, stalePaneID string) {
	if f.cfg.Verbose {
		log.Printf("discarding stale state for window %s (pane %s gone)", wk, paneKey{wk.socket, stalePaneID})
	}
	delete(f.panes, paneKey{wk.socket, stalePaneID})
	f.forgetWindow(wk)
}

// migratePaneIfRenumbered checks if paneID is tracked at a different window
// target on newKey's own server and migrates state to newKey. This handles tmux
// renumber-windows where a pane moves to a different window index. A window on
// another server is never a candidate: the same pane id there is a different
// pane. It returns the session keys of any stale occupant states discarded along
// the way.
func (f *Frontend) migratePaneIfRenumbered(newKey windowKey, paneID string) []string {
	for oldKey, ws := range f.windows {
		if oldKey.socket == newKey.socket && ws.PaneID == paneID && oldKey != newKey {
			if f.cfg.Verbose {
				log.Printf("pane %s renumbered from %s to %s, migrating state", paneKey{newKey.socket, paneID}, oldKey.target, newKey.target)
			}
			var evicted []string
			// If newKey is occupied by a different pane's state, discard it.
			if existing, ok := f.windows[newKey]; ok && existing.PaneID != paneID {
				if existing.SessionKey != "" {
					evicted = append(evicted, existing.SessionKey)
				}
				f.discardStaleWindow(newKey, existing.PaneID)
			}
			f.windows[newKey] = ws
			delete(f.windows, oldKey)
			if ws.SessionKey != "" {
				f.sessionKeys[ws.SessionKey] = newKey
			}
			return evicted
		}
	}
	return nil
}

// applyStatus updates the window's style, symbol variable, and name for the given status.
func (f *Frontend) applyStatus(wk windowKey, ws *windowState, status detect.Status, taskName string) {
	taskChanged := ws.TaskName != taskName
	ws.TaskName = taskName

	if ws.Status == status && !taskChanged {
		return
	}

	ws.Status = status
	style, symbol := f.attrsFor(status)

	// Set styles FIRST so they apply even if rename fails.
	f.setWindowOpt(wk.socket, wk.target, "window-status-style", style, "error setting window-status-style")
	f.setWindowOpt(wk.socket, wk.target, "window-status-current-style", style, "error setting window-status-current-style")
	// User variables for custom status-format integration.
	f.setWindowOpt(wk.socket, wk.target, "@cenci-style", style, "error setting @cenci-style")
	f.setWindowOpt(wk.socket, wk.target, "@cenci-symbol", symbol, "error setting @cenci-symbol")

	// Build the display name (no symbol prefix — symbol is in @cenci-symbol and format string).
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
	if err := f.client.RenameWindow(wk.socket, wk.target, displayName); err != nil {
		if f.cfg.Verbose {
			log.Printf("error renaming window %s: %v", wk, err)
		}
	}
	ws.LastSetName = displayName

	if f.cfg.Verbose {
		log.Printf("window %s: %s → %q (symbol: %s, style: %s)", wk, status, truncateForLog(displayName, logMaxLen), symbol, style)
	}
}

// setWindowOpt sets a tmux window option on the given server and logs on
// failure when verbose mode is on.
func (f *Frontend) setWindowOpt(socket, target, key, value, errMsg string) {
	if err := f.client.SetWindowOption(socket, target, key, value); err != nil && f.cfg.Verbose {
		log.Printf("%s for %s: %v", errMsg, windowKey{socket, target}, err)
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
