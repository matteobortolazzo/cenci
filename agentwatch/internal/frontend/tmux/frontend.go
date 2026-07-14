// Package tmux implements the interactive tmux frontend: it renames and
// styles the window behind each pane-backed agent session and restores the
// window when the session ends. Sessions without a tmux pane are skipped —
// they are core-only and reach users through the read-only status frontends.
package tmux

import (
	"log"
	"strings"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v3/internal/config"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v3/internal/detect"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v3/internal/frontend"
	"github.com/matteobortolazzo/agent-stack/agentwatch/v3/internal/ipc"
	tmuxc "github.com/matteobortolazzo/agent-stack/agentwatch/v3/internal/tmux"
)

// Frontend styles tmux windows to mirror agent session state.
type Frontend struct {
	cfg          config.Config
	client       tmuxc.Client
	windows      map[string]*windowState // key: window target (session:windowIdx)
	panes        map[string]string       // key: pane ID (%5) → window target
	sessionKeys  map[string]string       // key: core session key → window target
	headroomVars map[string]string       // key: agent type → last @agentwatch-headroom-<agent> value set (#171)
}

var _ frontend.Frontend = (*Frontend)(nil)

// New creates a tmux frontend backed by the given client.
func New(cfg config.Config, client tmuxc.Client) *Frontend {
	return &Frontend{
		cfg:          cfg,
		client:       client,
		windows:      make(map[string]*windowState),
		panes:        make(map[string]string),
		sessionKeys:  make(map[string]string),
		headroomVars: make(map[string]string),
	}
}

// OnEvent tracks the window behind the session's pane and applies the
// session's status to it. Paneless sessions are a no-op.
func (f *Frontend) OnEvent(sess *frontend.SessionState, event ipc.HookEvent) frontend.Observations {
	if sess.TmuxPane == "" {
		return frontend.Observations{}
	}
	pane := sess.TmuxPane

	// Call ListPanes once for the entire event.
	panes, err := f.client.ListPanes()
	if err != nil {
		if f.cfg.Verbose {
			log.Printf("event: error listing panes for %s: %v", pane, err)
		}
		return frontend.Observations{}
	}

	var paneInfo *tmuxc.PaneInfo
	for i := range panes {
		if panes[i].PaneID == pane {
			paneInfo = &panes[i]
			break
		}
	}
	if paneInfo == nil {
		if f.cfg.Verbose {
			log.Printf("event: pane %s not found in tmux", pane)
		}
		return frontend.Observations{}
	}

	windowTarget := paneInfo.WindowTarget()
	f.panes[pane] = windowTarget
	var obs frontend.Observations
	obs.EvictedKeys = append(obs.EvictedKeys, f.migratePaneIfRenumbered(windowTarget, pane)...)

	// Ensure window is tracked (first event or daemon restart).
	ws := f.windows[windowTarget]
	if ws != nil && ws.PaneID != pane {
		// Window index reused by a different pane — discard stale state.
		if ws.SessionKey != "" {
			obs.EvictedKeys = append(obs.EvictedKeys, ws.SessionKey)
		}
		f.discardStaleWindow(windowTarget, ws.PaneID)
		ws = nil
	}
	if ws == nil {
		ws = f.trackWindow(windowTarget, paneInfo, event.SessionID)
		if ws == nil {
			return obs // tracking failed
		}
	}

	key := sess.Key()
	if ws.SessionKey != "" && ws.SessionKey != key && f.sessionKeys[ws.SessionKey] == windowTarget {
		delete(f.sessionKeys, ws.SessionKey)
	}
	ws.SessionKey = key
	f.sessionKeys[key] = windowTarget

	ws.SessionID = event.SessionID
	if sess.Agent != "" {
		ws.Agent = sess.Agent
	} else if ws.Agent == "" {
		ws.Agent = inferAgent(paneInfo.PaneCurrentCmd)
	}
	obs.AgentHint = ws.Agent

	// Resolve task name from the pane title (sanitize immediately to protect
	// downstream consumers: IPC broadcast, status output, and rename).
	taskName := taskNameFromPane(paneInfo)
	if sess.PromptTaskName {
		taskName = sess.TaskName
	}
	obs.TaskNameHint = taskName

	// Detect mid-session user renames.
	if !ws.ManuallyNamed && ws.LastSetName != "" && paneInfo.WindowName != ws.LastSetName {
		ws.ManuallyNamed = true
		ws.OriginalName = frontend.SanitizeName(paneInfo.WindowName)
		if f.cfg.Verbose {
			log.Printf("window %s was renamed by user (%q → %q), will keep name", windowTarget, truncateForLog(ws.LastSetName, logMaxLen), truncateForLog(ws.OriginalName, logMaxLen))
		}
	}

	f.applyStatus(windowTarget, ws, sess.Status, taskName)
	return obs
}

// taskNameFromPane derives the fallback display task name from the pane title.
// Codex prompt-derived labels override this fallback once available.
func taskNameFromPane(paneInfo *tmuxc.PaneInfo) string {
	if fallback, ok := codexActionRequiredTitle(paneInfo.PaneTitle); ok {
		return fallback
	}
	return frontend.CompactTaskName(detect.TaskName(paneInfo.PaneTitle))
}

// codexActionRequiredTitle recognizes Codex's native question title and
// returns only its project-name fallback, never the action prefix.
func codexActionRequiredTitle(title string) (string, bool) {
	title = strings.TrimSpace(title)
	if !strings.HasPrefix(title, "[") {
		return "", false
	}
	close := strings.Index(title, "]")
	if close < 0 || strings.TrimSpace(title[1:close]) != "!" {
		return "", false
	}
	rest := strings.TrimSpace(title[close+1:])
	const action = "Action Required"
	if !strings.HasPrefix(rest, action) {
		return "", false
	}
	rest = strings.TrimSpace(strings.TrimPrefix(rest, action))
	rest = strings.TrimSpace(strings.TrimPrefix(rest, "|"))
	return frontend.CompactTaskName(rest), true
}

// OnSessionEnd restores the window owned by the ended session.
func (f *Frontend) OnSessionEnd(sess *frontend.SessionState) {
	if sess.TmuxPane == "" {
		return
	}
	pane := sess.TmuxPane

	panes, err := f.client.ListPanes()
	if err != nil {
		if f.cfg.Verbose {
			log.Printf("event: error listing panes for %s: %v", pane, err)
		}
		return
	}

	var paneInfo *tmuxc.PaneInfo
	for i := range panes {
		if panes[i].PaneID == pane {
			paneInfo = &panes[i]
			break
		}
	}

	// Determine window target from fresh data or cache.
	var windowTarget string
	if paneInfo != nil {
		windowTarget = paneInfo.WindowTarget()
		f.panes[pane] = windowTarget
		f.migratePaneIfRenumbered(windowTarget, pane)
	} else {
		// Pane no longer in tmux. Use cached target for cleanup.
		windowTarget = f.panes[pane]
		if windowTarget == "" {
			if f.cfg.Verbose {
				log.Printf("event: pane %s not found in tmux", pane)
			}
			return
		}
	}

	ws := f.windows[windowTarget]
	if ws != nil && ws.PaneID != pane {
		// Late SessionEnd for a dead pane — don't touch the new window.
		delete(f.panes, pane)
		return
	}
	// When the pane is gone, check if another pane now occupies the
	// cached window target (renumber-windows). If so, discard state
	// without restoring to avoid overwriting the surviving window.
	if paneInfo == nil {
		for _, p := range panes {
			if p.WindowTarget() == windowTarget {
				f.forgetWindow(windowTarget)
				delete(f.panes, pane)
				return
			}
		}
	}
	f.restoreWindow(windowTarget)
	delete(f.panes, pane)
}

// Sweep migrates renumbered panes, cleans up windows whose pane is gone,
// detects idle pane titles and exited agents, and reports which core sessions
// to update or remove.
func (f *Frontend) Sweep(sessions map[string]*frontend.SessionState) []frontend.SweepAction {
	paneBacked := false
	for _, s := range sessions {
		if s.TmuxPane != "" {
			paneBacked = true
			break
		}
	}
	if len(f.windows) == 0 && !paneBacked {
		return nil
	}

	panes, err := f.client.ListPanes()
	if err != nil {
		if f.cfg.Verbose {
			log.Printf("sweep: error listing panes: %v", err)
		}
		return nil
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

	remove := make(map[string]bool)
	var updates []frontend.SweepAction
	displayChanged := false

	// Phase 1: Migrate renumbered panes (pane alive but at a different target).
	type migrationInfo struct {
		oldTarget string
		newTarget string
		ws        *windowState
	}
	var migrations []migrationInfo
	for wt, ws := range f.windows {
		if newTarget, ok := paneToTarget[ws.PaneID]; ok && newTarget != wt {
			migrations = append(migrations, migrationInfo{oldTarget: wt, newTarget: newTarget, ws: ws})
		}
	}
	// Remove all migrating entries from old positions first.
	for _, m := range migrations {
		delete(f.windows, m.oldTarget)
	}
	// Then insert at new positions (discarding any stale occupants).
	for _, m := range migrations {
		if occupant, ok := f.windows[m.newTarget]; ok && occupant.PaneID != m.ws.PaneID {
			if occupant.SessionKey != "" {
				remove[occupant.SessionKey] = true
			}
			f.discardStaleWindow(m.newTarget, occupant.PaneID)
		}
		f.windows[m.newTarget] = m.ws
		f.panes[m.ws.PaneID] = m.newTarget
		if m.ws.SessionKey != "" {
			f.sessionKeys[m.ws.SessionKey] = m.newTarget
		}
		displayChanged = true
		if f.cfg.Verbose {
			log.Printf("sweep: pane %s renumbered from %s to %s, migrating state", m.ws.PaneID, m.oldTarget, m.newTarget)
		}
	}

	// Phase 2: Clean up truly stale entries (pane gone).
	var stale []string
	for wt, ws := range f.windows {
		if !existing[ws.PaneID] {
			stale = append(stale, wt)
		}
	}
	for _, wt := range stale {
		ws := f.windows[wt]
		if f.cfg.Verbose {
			log.Printf("sweep: pane %s gone, cleaning up window %s", ws.PaneID, wt)
		}
		if ws.SessionKey != "" {
			remove[ws.SessionKey] = true
		}
		delete(f.panes, ws.PaneID)
		if currentPane, ok := currentPaneForWindow[wt]; ok && currentPane != ws.PaneID {
			// Window target reused by another pane — discard without restore.
			f.forgetWindow(wt)
		} else {
			f.restoreWindow(wt)
		}
	}

	// Phase 3: Detect idle pane titles for windows still marked Running.
	// When the user presses ESC during pure text generation (no tool running),
	// there's no hook event. The pane title reverts to an idle marker (✶ ✻ ✳)
	// while we still think it's Running. Also restore windows whose agent
	// process exited without a SessionEnd hook (Codex).
	paneByID := make(map[string]*tmuxc.PaneInfo, len(panes))
	for i := range panes {
		paneByID[panes[i].PaneID] = &panes[i]
	}
	for wt, ws := range f.windows {
		p, ok := paneByID[ws.PaneID]
		if !ok {
			continue
		}
		if ws.Agent != "" && ws.Status != detect.StatusRunning && ws.Status != detect.StatusNeedInput && !agentCommandMatches(ws.Agent, p.PaneCurrentCmd) {
			if f.cfg.Verbose {
				log.Printf("sweep: pane %s no longer running %s (current command %q), restoring window %s", ws.PaneID, ws.Agent, p.PaneCurrentCmd, wt)
			}
			if ws.SessionKey != "" {
				remove[ws.SessionKey] = true
			}
			delete(f.panes, ws.PaneID)
			f.restoreWindow(wt)
			continue
		}
		if ws.Agent == "codex" {
			if fallback, actionRequired := codexActionRequiredTitle(p.PaneTitle); actionRequired {
				taskName := ws.TaskName
				if taskName == "" {
					taskName = fallback
				}
				if ws.Status != detect.StatusNeedInput || ws.TaskName != taskName {
					f.applyStatus(wt, ws, detect.StatusNeedInput, taskName)
					if ws.SessionKey != "" {
						updates = append(updates, frontend.SweepAction{SessionKey: ws.SessionKey, NewStatus: detect.StatusNeedInput, NewTask: taskName})
					}
				}
				continue
			}
			if ws.Status == detect.StatusNeedInput && detect.IsBraille(firstRune(p.PaneTitle)) {
				f.applyStatus(wt, ws, detect.StatusRunning, ws.TaskName)
				if ws.SessionKey != "" {
					updates = append(updates, frontend.SweepAction{SessionKey: ws.SessionKey, NewStatus: detect.StatusRunning, NewTask: ws.TaskName})
				}
				continue
			}
		}
		if ws.Status != detect.StatusRunning {
			continue
		}
		r := firstRune(p.PaneTitle)
		if detect.IsStatusSymbol(r) && !detect.IsBraille(r) {
			// Pane title shows an idle marker (✶ ✻ ✳) — Claude returned to prompt.
			taskName := frontend.SanitizeName(detect.TaskName(p.PaneTitle))
			f.applyStatus(wt, ws, detect.StatusStopped, taskName)
			if ws.SessionKey != "" {
				updates = append(updates, frontend.SweepAction{SessionKey: ws.SessionKey, NewStatus: detect.StatusStopped, NewTask: taskName})
			}
			if f.cfg.Verbose {
				log.Printf("sweep: pane %s idle (title %q), setting stopped", ws.PaneID, truncateForLog(p.PaneTitle, logMaxLen))
			}
		}
	}

	// Phase 4: Remove core sessions whose pane is gone (or now owned by a
	// different session) but that no tracked window pointed at — e.g. after a
	// stale-window discard, or when tracking never succeeded.
	paneOwner := make(map[string]string, len(f.windows))
	for _, ws := range f.windows {
		if ws.SessionKey != "" {
			paneOwner[ws.PaneID] = ws.SessionKey
		}
	}
	for key, sess := range sessions {
		if sess.TmuxPane == "" || remove[key] {
			continue
		}
		if !existing[sess.TmuxPane] {
			if f.cfg.Verbose {
				log.Printf("sweep: session %s pane %s gone, removing", key, sess.TmuxPane)
			}
			remove[key] = true
			continue
		}
		if owner, ok := paneOwner[sess.TmuxPane]; ok && owner != key {
			if f.cfg.Verbose {
				log.Printf("sweep: session %s superseded on pane %s, removing", key, sess.TmuxPane)
			}
			remove[key] = true
		}
	}

	actions := updates
	for key := range remove {
		actions = append(actions, frontend.SweepAction{SessionKey: key, Remove: true})
	}
	if len(actions) == 0 && displayChanged {
		// Display-only change (renumbering): emit a marker so the daemon rebroadcasts.
		actions = append(actions, frontend.SweepAction{})
	}
	return actions
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

// Cleanup restores all tracked windows on daemon shutdown and clears any
// tracked @agentwatch-headroom-<agent> global user variables (#171).
func (f *Frontend) Cleanup(map[string]*frontend.SessionState) {
	if f.cfg.Verbose && len(f.windows) > 0 {
		log.Printf("cleaning up %d tracked window(s)", len(f.windows))
	}
	for wt := range f.windows {
		f.restoreWindow(wt)
	}
	for agent := range f.headroomVars {
		f.setOpt(headroomVarName(agent), "", "error clearing @agentwatch-headroom-"+agent)
		delete(f.headroomVars, agent)
	}
}

// WindowInfo returns display fields for the window owned by the given core
// session key, or nil when no window is tracked for it.
func (f *Frontend) WindowInfo(sessionKey string) *frontend.WindowInfo {
	target, ok := f.sessionKeys[sessionKey]
	if !ok {
		return nil
	}
	ws := f.windows[target]
	if ws == nil {
		return nil
	}

	parts := strings.SplitN(target, ":", 2)
	session, winIdx := parts[0], ""
	if len(parts) == 2 {
		winIdx = parts[1]
	}

	// Use clean names (without symbol prefix) for IPC output.
	winName := ws.OriginalName
	if !ws.ManuallyNamed && ws.TaskName != "" {
		winName = ws.TaskName
	}
	return &frontend.WindowInfo{
		Session:       session,
		WindowIndex:   winIdx,
		WindowName:    winName,
		ManuallyNamed: ws.ManuallyNamed,
	}
}
