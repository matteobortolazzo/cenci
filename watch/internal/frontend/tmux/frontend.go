// Package tmux implements the interactive tmux frontend: it renames and
// styles the window behind each pane-backed agent session and restores the
// window when the session ends. Sessions without a tmux pane are skipped —
// they are core-only and reach users through the read-only status frontends.
package tmux

import (
	"log"
	"sort"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/config"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	tmuxc "github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
)

// Frontend styles tmux windows to mirror agent session state.
type Frontend struct {
	cfg         config.Config
	client      tmuxc.Client
	windows     map[windowKey]*windowState // key: (socket, window target session:windowIdx)
	panes       map[paneKey]string         // key: (socket, pane ID %5) → window target on that socket
	sessionKeys map[string]windowKey       // key: core session key → window
	// headroomVars is the last @cenci-headroom-<agent> value set (#171) per
	// socket: socket → agent type → value. "" is the default server.
	headroomVars map[string]map[string]string
	// lastHeadroom is the agent → value map the latest RenderHeadroom rendered,
	// pushed to a server when its first window is tracked.
	lastHeadroom map[string]string

	// now is the clock the dead-socket rules read; tests overwrite it (#1462).
	now func() time.Time
	// socketGone reports that a non-empty socket's file is gone under a trusted
	// directory (rule (a), #1462); tests overwrite it.
	socketGone func(socket string) bool
	// failingSince is when each socket's listing started failing continuously.
	failingSince map[string]time.Time
}

var _ frontend.Frontend = (*Frontend)(nil)

// titleStopQuiescence is how long hook events must have been quiet before the
// sweep's idle-title detection may flip a Running session to stopped (#706).
// A real ESC during pure text generation fires no hook event at all, so
// silence is its signature; a title sampled while events are still fresh must
// never outrank them. 3× the default 1s SweepInterval.
const titleStopQuiescence = 3 * time.Second

// New creates a tmux frontend backed by the given client.
func New(cfg config.Config, client tmuxc.Client) *Frontend {
	return &Frontend{
		cfg:          cfg,
		client:       client,
		windows:      make(map[windowKey]*windowState),
		panes:        make(map[paneKey]string),
		sessionKeys:  make(map[string]windowKey),
		headroomVars: make(map[string]map[string]string),
		now:          time.Now,
		socketGone: func(socket string) bool {
			dir, err := tmuxc.TrustedSocketDir()
			return err == nil && tmuxc.SocketGone(dir, socket)
		},
		failingSince: make(map[string]time.Time),
	}
}

// OnEvent tracks the window behind the session's pane and applies the
// session's status to it. Paneless sessions are a no-op.
func (f *Frontend) OnEvent(sess *frontend.SessionState, event ipc.HookEvent) frontend.Observations {
	if sess.TmuxPane == "" {
		return frontend.Observations{}
	}
	pk := paneKey{sess.TmuxSocket, sess.TmuxPane}

	// Call ListPanes once for the entire event, on the pane's own server.
	panes, err := f.client.ListPanes(pk.socket)
	if err != nil {
		if f.cfg.Verbose {
			log.Printf("event: error listing panes for %s: %v", pk, err)
		}
		return frontend.Observations{}
	}

	var paneInfo *tmuxc.PaneInfo
	for i := range panes {
		if panes[i].PaneID == pk.pane {
			paneInfo = &panes[i]
			break
		}
	}
	if paneInfo == nil {
		if f.cfg.Verbose {
			log.Printf("event: pane %s not found in tmux", pk)
		}
		_, tracked := f.sessionKeys[sess.Key()]
		return frontend.Observations{UntrackedPaneMissing: !tracked}
	}

	wk := windowKey{pk.socket, paneInfo.WindowTarget()}
	f.panes[pk] = wk.target
	var obs frontend.Observations
	obs.EvictedKeys = append(obs.EvictedKeys, f.migratePaneIfRenumbered(wk, pk.pane)...)

	// Ensure window is tracked (first event or daemon restart).
	ws := f.windows[wk]
	if ws != nil && ws.PaneID != pk.pane {
		// Window index reused by a different pane — discard stale state.
		if ws.SessionKey != "" {
			obs.EvictedKeys = append(obs.EvictedKeys, ws.SessionKey)
		}
		f.discardStaleWindow(wk, ws.PaneID)
		ws = nil
	}
	if ws == nil {
		ws = f.trackWindow(wk, paneInfo, event.SessionID)
		if ws == nil {
			return obs // tracking failed
		}
	}

	key := sess.Key()
	if ws.SessionKey != "" && ws.SessionKey != key && f.sessionKeys[ws.SessionKey] == wk {
		delete(f.sessionKeys, ws.SessionKey)
	}
	ws.SessionKey = key
	f.sessionKeys[key] = wk

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
	if isGraphicsProbeTitle(paneInfo.PaneTitle) && ws.TaskName != "" {
		taskName = ws.TaskName
	}
	if sess.PromptTaskName {
		taskName = sess.TaskName
	}
	obs.TaskNameHint = taskName

	// Detect mid-session user renames.
	if !ws.ManuallyNamed && ws.LastSetName != "" && paneInfo.WindowName != ws.LastSetName {
		ws.ManuallyNamed = true
		ws.OriginalName = frontend.SanitizeName(paneInfo.WindowName)
		if f.cfg.Verbose {
			log.Printf("window %s was renamed by user (%q → %q), will keep name", wk, truncateForLog(ws.LastSetName, logMaxLen), truncateForLog(ws.OriginalName, logMaxLen))
		}
	}

	f.applyStatus(wk, ws, sess.Status, taskName)
	return obs
}

// taskNameFromPane derives the fallback display task name from the pane title.
// Codex prompt-derived labels override this fallback once available.
func taskNameFromPane(paneInfo *tmuxc.PaneInfo) string {
	if isGraphicsProbeTitle(paneInfo.PaneTitle) {
		return "Claude Code"
	}
	if fallback, ok := codexActionRequiredTitle(paneInfo.PaneTitle); ok {
		return fallback
	}
	return frontend.CompactTaskName(detect.TaskName(paneInfo.PaneTitle))
}

// Claude's unwrapped kitty capability query is interpreted by tmux as an APC
// title update (anthropics/claude-code#99005). It is protocol data, not a task.
// Also recognize the compact name older cenci versions copied into the window.
func isGraphicsProbeTitle(title string) bool {
	const probe = "Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA"
	return title == probe || title == frontend.CompactTaskName(probe)
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
	pk := paneKey{sess.TmuxSocket, sess.TmuxPane}

	panes, err := f.client.ListPanes(pk.socket)
	if err != nil {
		if f.cfg.Verbose {
			log.Printf("event: error listing panes for %s: %v", pk, err)
		}
		return
	}

	var paneInfo *tmuxc.PaneInfo
	for i := range panes {
		if panes[i].PaneID == pk.pane {
			paneInfo = &panes[i]
			break
		}
	}

	// Determine the window from fresh data or cache.
	var wk windowKey
	if paneInfo != nil {
		wk = windowKey{pk.socket, paneInfo.WindowTarget()}
		f.panes[pk] = wk.target
		f.migratePaneIfRenumbered(wk, pk.pane)
	} else {
		// Pane no longer in tmux. Use cached target for cleanup.
		wk = windowKey{pk.socket, f.panes[pk]}
		if wk.target == "" {
			if f.cfg.Verbose {
				log.Printf("event: pane %s not found in tmux", pk)
			}
			return
		}
	}

	ws := f.windows[wk]
	if ws != nil && ws.PaneID != pk.pane {
		// Late SessionEnd for a dead pane — don't touch the new window.
		delete(f.panes, pk)
		return
	}
	// When the pane is gone, check if another pane now occupies the
	// cached window target (renumber-windows). If so, discard state
	// without restoring to avoid overwriting the surviving window.
	if paneInfo == nil {
		for _, p := range panes {
			if p.WindowTarget() == wk.target {
				f.forgetWindow(wk)
				delete(f.panes, pk)
				return
			}
		}
	}
	f.restoreWindow(wk)
	delete(f.panes, pk)
}

// OnSessionHandoff releases the ended session's claim on its window while
// leaving the window tracked and styled: after a /clear (or an interactive
// /resume switch) the agent continues on the same pane under a new session
// id, and that successor's first event re-keys the window via the takeover
// in OnEvent (#707). When the pane is already gone there is no successor to
// hand the window to — fall back to the full OnSessionEnd teardown.
func (f *Frontend) OnSessionHandoff(sess *frontend.SessionState) {
	if sess.TmuxPane == "" {
		return
	}

	panes, err := f.client.ListPanes(sess.TmuxSocket)
	if err != nil {
		if f.cfg.Verbose {
			log.Printf("event: error listing panes for %s: %v", paneKey{sess.TmuxSocket, sess.TmuxPane}, err)
		}
		return
	}
	alive := false
	for i := range panes {
		if panes[i].PaneID == sess.TmuxPane {
			alive = true
			break
		}
	}
	if !alive {
		f.OnSessionEnd(sess)
		return
	}

	key := sess.Key()
	wk, ok := f.sessionKeys[key]
	if !ok {
		return
	}
	delete(f.sessionKeys, key)
	if ws := f.windows[wk]; ws != nil && ws.SessionKey == key {
		ws.SessionKey = ""
	}
	if f.cfg.Verbose {
		log.Printf("session %s handed off window %s to its successor", key, wk)
	}
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
		clear(f.failingSince)
		return nil
	}
	now := f.now()

	// One listing per distinct socket that hosts a tracked window or a
	// pane-backed session, in sorted order ("" first). A socket whose listing
	// fails is left alone for this tick: a failed listing is never read as "pane
	// gone", and the other servers are swept normally. A failed listing alone
	// still removes nothing; only the dead-socket rules below (#1462), which need
	// evidence beyond the failure, drop a non-empty socket's windows and sessions.
	socketSet := make(map[string]bool)
	for wk := range f.windows {
		socketSet[wk.socket] = true
	}
	for _, s := range sessions {
		if s.TmuxPane != "" {
			socketSet[s.TmuxSocket] = true
		}
	}
	sockets := make([]string, 0, len(socketSet))
	for socket := range socketSet {
		sockets = append(sockets, socket)
	}
	sort.Strings(sockets)

	// Build (socket, paneID) → current target maps from the listings that worked.
	paneToTarget := make(map[paneKey]string)
	existing := make(map[paneKey]bool)
	currentPaneForWindow := make(map[windowKey]string)
	paneByID := make(map[paneKey]*tmuxc.PaneInfo)
	unlisted := make(map[string]bool)
	for _, socket := range sockets {
		panes, err := f.client.ListPanes(socket)
		if err != nil {
			unlisted[socket] = true
			if _, failing := f.failingSince[socket]; !failing && socket != "" {
				f.failingSince[socket] = now
			}
			if f.cfg.Verbose {
				log.Printf("sweep: error listing panes%s: %v", socketLogSuffix(socket), err)
			}
			continue
		}
		delete(f.failingSince, socket)
		for i, p := range panes {
			pk := paneKey{socket, p.PaneID}
			paneToTarget[pk] = p.WindowTarget()
			existing[pk] = true
			currentPaneForWindow[windowKey{socket, p.WindowTarget()}] = p.PaneID
			paneByID[pk] = &panes[i]
		}
	}
	for socket := range f.failingSince {
		if !socketSet[socket] {
			delete(f.failingSince, socket)
		}
	}

	remove := make(map[string]bool)
	for _, socket := range sockets {
		if !unlisted[socket] || socket == "" {
			continue
		}
		if rule := f.deadSocketRule(socket, sessions, now); rule != "" {
			f.dropDeadSocket(socket, rule, sessions, remove)
		}
	}
	var updates []frontend.SweepAction
	displayChanged := false

	// Phase 1: Migrate renumbered panes (pane alive but at a different target).
	type migrationInfo struct {
		oldKey windowKey
		newKey windowKey
		ws     *windowState
	}
	var migrations []migrationInfo
	for wk, ws := range f.windows {
		if newTarget, ok := paneToTarget[paneKey{wk.socket, ws.PaneID}]; ok && newTarget != wk.target {
			migrations = append(migrations, migrationInfo{oldKey: wk, newKey: windowKey{wk.socket, newTarget}, ws: ws})
		}
	}
	// Remove all migrating entries from old positions first.
	for _, m := range migrations {
		delete(f.windows, m.oldKey)
	}
	// Then insert at new positions (discarding any stale occupants).
	for _, m := range migrations {
		if occupant, ok := f.windows[m.newKey]; ok && occupant.PaneID != m.ws.PaneID {
			if occupant.SessionKey != "" {
				remove[occupant.SessionKey] = true
			}
			f.discardStaleWindow(m.newKey, occupant.PaneID)
		}
		f.windows[m.newKey] = m.ws
		f.panes[paneKey{m.newKey.socket, m.ws.PaneID}] = m.newKey.target
		if m.ws.SessionKey != "" {
			f.sessionKeys[m.ws.SessionKey] = m.newKey
		}
		displayChanged = true
		if f.cfg.Verbose {
			log.Printf("sweep: pane %s renumbered from %s to %s, migrating state", paneKey{m.newKey.socket, m.ws.PaneID}, m.oldKey.target, m.newKey.target)
		}
	}

	// Phase 2: Clean up truly stale entries (pane gone from its own server).
	paneGone := false
	paneGoneKeys := make(map[string]bool)
	var stale []windowKey
	for wk, ws := range f.windows {
		if !unlisted[wk.socket] && !existing[paneKey{wk.socket, ws.PaneID}] {
			stale = append(stale, wk)
		}
	}
	for _, wk := range stale {
		ws := f.windows[wk]
		if f.cfg.Verbose {
			log.Printf("sweep: pane %s gone, cleaning up window %s", paneKey{wk.socket, ws.PaneID}, wk)
		}
		paneGone = true
		if ws.SessionKey != "" {
			remove[ws.SessionKey] = true
			paneGoneKeys[ws.SessionKey] = true
		}
		delete(f.panes, paneKey{wk.socket, ws.PaneID})
		if currentPane, ok := currentPaneForWindow[wk]; ok && currentPane != ws.PaneID {
			// Window target reused by another pane — discard without restore.
			f.forgetWindow(wk)
		} else {
			f.restoreWindow(wk)
		}
	}

	// Phase 3: Detect idle pane titles for windows still marked Running.
	// When the user presses ESC during pure text generation (no tool running),
	// there's no hook event. The pane title reverts to an idle marker (✶ ✻ ✳)
	// while we still think it's Running. Also restore Codex windows whose agent
	// process exited without sending SessionEnd (an older client or an abrupt
	// exit).
	//
	// The exit restore is Codex/OpenCode-only (#432, #488): Codex sends
	// SessionEnd from 0.166.0, but an older client or an abrupt exit does not,
	// and OpenCode's exit is not relied on to either, so a pane whose current
	// command no longer reads as that agent is our backstop exit signal. Claude
	// Code *does* fire SessionEnd (handled in daemon/event.go) and pane
	// disappearance is covered by Phase 2 above, so a finished Claude session
	// must not be swept here — its pane_current_command
	// legitimately differs from "claude" (npm/node shim reports "node"; sandbox
	// lookups fail), which would otherwise silently untrack a just-done session
	// (regression from #418 tagging Claude events with -agent claude).
	//
	// OpenCode's own JS/Bun runtime can transiently report "node"/"bun" instead
	// of "opencode" while still alive; inferAgent aliases those back to
	// "opencode" (unlike Codex, which has no such alias list), so
	// agentCommandMatches naturally treats them as "still running" here and only
	// an unambiguous shell revert triggers the restore below.
	for wk, ws := range f.windows {
		p, ok := paneByID[paneKey{wk.socket, ws.PaneID}]
		if !ok {
			continue
		}
		if (ws.Agent == "codex" || ws.Agent == "opencode") && ws.Status != detect.StatusRunning && ws.Status != detect.StatusNeedInput && !agentCommandMatches(ws.Agent, p.PaneCurrentCmd) {
			if f.cfg.Verbose {
				log.Printf("sweep: pane %s no longer running %s (current command %q), restoring window %s", paneKey{wk.socket, ws.PaneID}, ws.Agent, p.PaneCurrentCmd, wk)
			}
			if ws.SessionKey != "" {
				remove[ws.SessionKey] = true
			}
			delete(f.panes, paneKey{wk.socket, ws.PaneID})
			f.restoreWindow(wk)
			continue
		}
		if ws.Agent == "codex" {
			if fallback, actionRequired := codexActionRequiredTitle(p.PaneTitle); actionRequired {
				taskName := ws.TaskName
				if taskName == "" {
					taskName = fallback
				}
				if ws.Status != detect.StatusNeedInput || ws.TaskName != taskName {
					f.applyStatus(wk, ws, detect.StatusNeedInput, taskName)
					if ws.SessionKey != "" {
						updates = append(updates, frontend.SweepAction{SessionKey: ws.SessionKey, NewStatus: detect.StatusNeedInput, NewTask: taskName, AttentionSource: "action-required-title"})
					}
				}
				continue
			}
			// Braille specifically, not IsWorkingMarker: this branch reads
			// Codex's own spinner, and Codex emits braille only. Widening it
			// to Claude Code's half-circle marker (#1039) would let a glyph
			// Codex never writes clear a Codex need-input state.
			if ws.Status == detect.StatusNeedInput && detect.IsBraille(firstRune(p.PaneTitle)) {
				f.applyStatus(wk, ws, detect.StatusRunning, ws.TaskName)
				if ws.SessionKey != "" {
					updates = append(updates, frontend.SweepAction{SessionKey: ws.SessionKey, NewStatus: detect.StatusRunning, NewTask: ws.TaskName})
				}
				continue
			}
		}
		if ws.Status != detect.StatusRunning {
			continue
		}
		sess, tracked := sessions[ws.SessionKey]
		if tracked && sess.BackgroundHold {
			// The core is holding this session at running for background work
			// that was in flight when the main agent's Stop fired (#698). The
			// hold outranks the title backstop below. Silence cannot complete
			// explicitly reported tasks: a waiting shell or busy subagent may
			// emit no hooks for a long time. Only timer-only/legacy holds expire
			// (#1079), regardless of the title's idle or working marker.
			if sess.BackgroundTasksActive || time.Since(sess.LastEvent) < frontend.BackgroundHoldTTL {
				continue
			}
			// Status-only change: each side keeps the task name it already
			// holds (the core's may be a pinned prompt label, the window's is
			// title-derived), since nothing about the release renames anything.
			f.applyStatus(wk, ws, detect.StatusDone, ws.TaskName)
			if ws.SessionKey != "" {
				updates = append(updates, frontend.SweepAction{SessionKey: ws.SessionKey, NewStatus: detect.StatusDone, NewTask: sess.TaskName})
			}
			if f.cfg.Verbose {
				log.Printf("sweep: pane %s background hold expired after %s of silence, setting done", ws.PaneID, frontend.BackgroundHoldTTL)
			}
			continue
		}
		r := firstRune(p.PaneTitle)
		if detect.IsStatusSymbol(r) && !detect.IsWorkingMarker(r) {
			// Pane title shows an idle marker (✶ ✻ ✳) — but the title alone
			// cannot tell "user pressed ESC" from "paused at the prompt while
			// background work is in flight" (#706). Event state wins: require
			// hook-event quiescence for the ESC backstop (the background-work
			// hold is handled above). Untracked windows (no core session) keep
			// the plain backstop.
			if tracked && time.Since(sess.LastEvent) < titleStopQuiescence {
				continue
			}
			taskName := frontend.SanitizeName(detect.TaskName(p.PaneTitle))
			f.applyStatus(wk, ws, detect.StatusStopped, taskName)
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
	// A session on a socket whose listing failed is left alone.
	paneOwner := make(map[paneKey]string, len(f.windows))
	for wk, ws := range f.windows {
		if ws.SessionKey != "" {
			paneOwner[paneKey{wk.socket, ws.PaneID}] = ws.SessionKey
		}
	}
	for key, sess := range sessions {
		if sess.TmuxPane == "" || remove[key] || unlisted[sess.TmuxSocket] {
			continue
		}
		pk := paneKey{sess.TmuxSocket, sess.TmuxPane}
		if !existing[pk] {
			if f.cfg.Verbose {
				log.Printf("sweep: session %s pane %s gone, removing", key, pk)
			}
			remove[key] = true
			paneGone = true
			paneGoneKeys[key] = true
			continue
		}
		if owner, ok := paneOwner[pk]; ok && owner != key {
			if f.cfg.Verbose {
				log.Printf("sweep: session %s superseded on pane %s, removing", key, pk)
			}
			remove[key] = true
		}
	}

	actions := updates
	paneGoneEmitted := false
	for key := range remove {
		pg := paneGoneKeys[key]
		paneGoneEmitted = paneGoneEmitted || pg
		actions = append(actions, frontend.SweepAction{SessionKey: key, Remove: true, PaneGone: pg})
	}
	if len(actions) == 0 && displayChanged {
		// Display-only change (renumbering): emit a marker so the daemon rebroadcasts.
		actions = append(actions, frontend.SweepAction{})
	}
	if paneGone && !paneGoneEmitted {
		// Ensure the pane-gone signal always reaches the daemon core, even in
		// the rare case where a stale window carried no SessionKey (so no
		// remove action above already carried it).
		actions = append(actions, frontend.SweepAction{PaneGone: true})
	}
	return actions
}

// deadSocketRule names the rule that declares a non-empty socket, whose listing
// failed on this tick, dead (#1462), or returns "" when there is no evidence
// beyond the failed listing. Rule (a): the socket file is gone under a trusted
// directory. Rule (b), disabled by a non-positive -session-ttl: the listing has
// failed continuously for at least the TTL and every session on the socket has
// been silent for at least the TTL.
func (f *Frontend) deadSocketRule(socket string, sessions map[string]*frontend.SessionState, now time.Time) string {
	if f.socketGone(socket) {
		return "socket file gone"
	}
	ttl := f.cfg.SessionTTL
	if ttl <= 0 || now.Sub(f.failingSince[socket]) < ttl {
		return ""
	}
	for _, s := range sessions {
		if s.TmuxPane != "" && s.TmuxSocket == socket && now.Sub(s.LastEvent) < ttl {
			return ""
		}
	}
	return "listing failed and sessions silent for the session ttl"
}

// dropDeadSocket discards every window tracked on a dead socket without any tmux
// call (the server is gone or unreachable, so a restore would only add failing
// calls) and marks its sessions for removal without PaneGone, so the daemon drops
// them without a reap pass. The socket then leaves the listing set.
func (f *Frontend) dropDeadSocket(socket, rule string, sessions map[string]*frontend.SessionState, remove map[string]bool) {
	if f.cfg.Verbose {
		log.Printf("sweep: tmux socket %s is dead (%s), dropping its windows and sessions", socket, rule)
	}
	for key, s := range sessions {
		if s.TmuxPane != "" && s.TmuxSocket == socket {
			remove[key] = true
		}
	}
	for wk, ws := range f.windows {
		if wk.socket != socket {
			continue
		}
		if _, ok := sessions[ws.SessionKey]; ws.SessionKey != "" && !ok {
			remove[ws.SessionKey] = true
		}
		f.forgetWindow(wk)
	}
	for pk := range f.panes {
		if pk.socket == socket {
			delete(f.panes, pk)
		}
	}
	delete(f.headroomVars, socket)
	delete(f.failingSince, socket)
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

// socketLogSuffix is " on <socket>" for a non-empty socket and "" for the
// ambient server, so the ambient server's log text is unchanged.
func socketLogSuffix(socket string) string {
	if socket == "" {
		return ""
	}
	return " on " + socket
}

// Cleanup restores all tracked windows (each on its own server) on daemon
// shutdown and clears any tracked @cenci-headroom-<agent> user variables on
// every server they were set on (#171).
func (f *Frontend) Cleanup(map[string]*frontend.SessionState) {
	if f.cfg.Verbose && len(f.windows) > 0 {
		log.Printf("cleaning up %d tracked window(s)", len(f.windows))
	}
	for wk := range f.windows {
		f.restoreWindow(wk)
	}
	for socket := range f.headroomVars {
		f.renderHeadroomOn(socket, nil)
		delete(f.headroomVars, socket)
	}
}

// WindowInfo returns display fields for the window owned by the given core
// session key, or nil when no window is tracked for it.
func (f *Frontend) WindowInfo(sessionKey string) *frontend.WindowInfo {
	wk, ok := f.sessionKeys[sessionKey]
	if !ok {
		return nil
	}
	ws := f.windows[wk]
	if ws == nil {
		return nil
	}

	parts := strings.SplitN(wk.target, ":", 2)
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
		RepoDir:       ws.RepoDir,
		PaneID:        ws.PaneID,
		Socket:        wk.socket,
	}
}
