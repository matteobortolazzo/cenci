package daemon

import (
	"errors"
	"log"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/modelswitch"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// sessionKeyForEvent returns the key for the daemon's sessions map: the agent
// session ID when present, otherwise a pane-derived fallback. Events carrying
// neither are dropped. The fallback carries the (already sanitized) socket when
// it is non-empty, because pane ids repeat across tmux servers; an empty socket
// keeps "pane:<id>". It must stay in sync with frontend.SessionState.Key.
func sessionKeyForEvent(event ipc.HookEvent) string {
	if event.SessionID != "" {
		return event.SessionID
	}
	if event.TmuxPane != "" {
		if event.TmuxSocket != "" {
			return "pane:" + event.TmuxPane + "@" + event.TmuxSocket
		}
		return "pane:" + event.TmuxPane
	}
	return ""
}

// sanitizeTmuxSocket validates a hook event's tmux_socket at ingest. The event
// socket also receives events from containers, so any in-container process can
// write a raw event with an arbitrary value: notify's pre-sending is not a
// check. A value passes only when it has the socket shape, is valid UTF-8 made
// of printable runes only (it is later printed in verbose logs, where a raw ESC,
// CR, C1 control or bidi override would forge log output), the trusted socket
// directory resolves, the value is exactly one name directly under it, and that
// path exists as a Unix socket owned by the daemon's uid (an Lstat check that
// never connects, #1462). Any failure (an error resolving the directory
// included) yields an empty socket, i.e. the default server, plus a rejection
// description for the verbose log. A malformed value is never echoed into that
// description.
func (d *Daemon) sanitizeTmuxSocket(socket string) (clean, rejection string) {
	if socket == "" {
		return "", ""
	}
	if !tmux.ValidSocket(socket) || hasUnprintable(socket) {
		return "", "malformed"
	}
	dir, err := d.tmuxSocketDir()
	if err == nil {
		err = tmux.CheckTrustedSocket(dir, socket)
	}
	if err == nil {
		err = d.tmuxSocketFile(socket)
	}
	if err != nil {
		return "", "untrusted: " + err.Error()
	}
	return socket, ""
}

// hasUnprintable reports whether s is not valid UTF-8 or holds a rune that is
// not printable: C0 and C1 controls, DEL, and format characters such as the
// bidi overrides. A printable non-ASCII name is not flagged.
func hasUnprintable(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return true
		}
	}
	return false
}

// serverPIDPattern is the shape of a tmux server pid (#1529): a positive decimal
// without a leading zero, at most 10 digits.
var serverPIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)

// validServerPID reports whether pid has the tmux server pid shape.
func validServerPID(pid string) bool {
	return serverPIDPattern.MatchString(pid)
}

func (d *Daemon) handleEvent(event ipc.HookEvent) {
	// The pid rule applies only to events that sent a socket at all (#1529):
	// read before sanitizing, since an untrusted socket falls back to the
	// default server and its pid is then compared against that server's.
	sentSocket := event.TmuxSocket != ""
	var rejection string
	event.TmuxSocket, rejection = d.sanitizeTmuxSocket(event.TmuxSocket)
	malformedPID := event.TmuxServerPID != "" && !validServerPID(event.TmuxServerPID)
	if malformedPID {
		event.TmuxServerPID = ""
	}
	key := sessionKeyForEvent(event)
	if key == "" {
		if d.cfg.Verbose {
			log.Printf("event: dropping %s event with empty session_id and tmux_pane", event.EventType)
		}
		return
	}
	if rejection != "" && d.cfg.Verbose {
		log.Printf("event: %s event on session %s: tmux_socket %s, using the default server", event.EventType, key, rejection)
	}
	if malformedPID && d.cfg.Verbose {
		log.Printf("event: %q event on session %q: tmux_server_pid malformed, treating it as absent", event.EventType, key)
	}

	sess := d.sessions[key]
	if sess == nil {
		sess = &frontend.SessionState{SessionID: event.SessionID}
		d.sessions[key] = sess
	}
	sess.LastEvent = d.now()
	if event.Agent != "" {
		sess.Agent = event.Agent
	}
	// Upgrade paneless→pane when a later event carries a pane; never clear a
	// known pane on an empty-pane event. The socket travels with the pane: they
	// are set together, and an event without a pane changes neither.
	adopt := d.paneAdoptable(key, sess, event, sentSocket)
	recheckMissingPane := adopt && sess.TmuxPane == "" &&
		event.TmuxPane == sess.MissingTmuxPane && event.TmuxSocket == sess.MissingTmuxSocket
	requirePID := sentSocket && sess.Agent == "codex" && sess.SessionID != ""
	if adopt && !recheckMissingPane {
		adoptEventPane(sess, event, requirePID)
	}
	if event.EventType == "UserPromptSubmit" && event.TaskName != "" && !sess.PromptTaskName {
		sess.TaskName = event.TaskName
		sess.PromptTaskName = true
	}

	// A subagent's own SessionEnd (non-empty AgentID) must not tear down the
	// main session's window — only the main agent's own SessionEnd does.
	// Codex's SessionEnd always carries the constant reason "other", so it
	// always takes the full teardown below, never the clear/resume handoff.
	if event.EventType == "SessionEnd" && event.AgentID == "" {
		// Resolve the window's stable pane identity before teardown:
		// OnSessionEnd releases the frontend's tracking state for this
		// session key, so WindowInfo would return nil afterward (#522).
		wi := d.frontend.WindowInfo(key)
		delete(d.sessions, key)
		// SessionEnd never reaches frontend.OnEvent, so the pid proof runs here
		// (#1529): a pane listed under another server pid is a reused id that
		// names an unrelated window. The frontend's teardown forgets the window
		// without a tmux write; the pending-close entry on that id is dropped,
		// never fired. A failed listing proves nothing and changes nothing extra.
		if sess.RequireServerPID && sess.TmuxPane != "" {
			if _, err := d.frontend.CheckPane(sess.TmuxSocket, sess.TmuxPane, sess.TmuxServerPID); errors.Is(err, frontend.ErrServerPIDMismatch) {
				if d.cfg.Verbose {
					log.Printf("event: SessionEnd on Codex session %q: pane %q is listed under another tmux server pid; forgetting its window without a tmux write", key, sess.TmuxPane)
				}
				d.frontend.OnSessionEnd(sess)
				if wi != nil {
					delete(d.pending, pendingKey{socket: wi.Socket, pane: wi.PaneID})
				}
				if event.SessionEndReason != "clear" && event.SessionEndReason != "resume" {
					d.triggerReap()
				}
				d.broadcast()
				return
			}
		}
		// "clear" and an interactive "resume" switch end the session id, not
		// the pane: a successor session continues in the same window
		// immediately, so hand the window over instead of restoring it
		// (#707). Every other reason — including an absent one from an older
		// Claude Code — keeps the full teardown: the handoff is narrowed to
		// the exact continuation reasons, never a catch-all.
		if event.SessionEndReason == "clear" || event.SessionEndReason == "resume" {
			// A pending-close means "close once the agent's work ends" — and on
			// a handoff the work continues in this window under a successor
			// session. Keep the registry entry (keyed by (socket, pane ID),
			// both of which survive the handoff) so the kill fires on the successor's own
			// SessionEnd instead of destroying the window mid-continuation. If
			// the handoff fell back to teardown because the pane is already
			// gone, the sweep's prunePendingCloseForWindow reclaims the entry.
			d.frontend.OnSessionHandoff(sess)
		} else {
			d.frontend.OnSessionEnd(sess)
			d.killPendingClose(wi)
			// Teardown drops the daemon's last binding to this pane:
			// OnSessionEnd forgets the frontend's tracking entry and the
			// session is already deleted above, so the sweep can never flag
			// this pane gone afterward. Any container process still holding
			// the pane -- including one stranded by the pending-close kill
			// that just destroyed the window -- would otherwise survive until
			// the next daemon restart (#1171). Narrowed to pane-bound
			// sessions: a paneless one never had a (socket, pane) pair for
			// the reaper to match on.
			if sess.TmuxPane != "" {
				d.triggerReap()
			}
		}
		d.broadcast()
		return
	}

	// A model switch is display-only state (#1179): it must never change the
	// session's status, so it skips the status mapping and frontend.OnEvent
	// entirely. A subagent's switch (AgentID set) lands on the parent session.
	if event.EventType == "PostModelSwitch" {
		d.recordModelSwitch(key, sess, event)
		d.broadcast()
		return
	}

	status := d.mapEventToStatus(event)
	if status == detect.StatusUnknown {
		return
	}
	if status == detect.StatusRunning && sess.Status == detect.StatusNeedInput && suppressNeedInputClear(event, sess) {
		if d.cfg.Verbose {
			log.Printf("attention: suppressing %s (tool=%s agent=%s) clearing need-input on session %s", event.EventType, event.ToolName, event.AgentID, key)
		}
		return
	}
	// A subagent's running event must not clear a terminal status set by the
	// main agent (#656). Each `cenci notify` is an independent process racing
	// to the daemon's socket, so a subagent PreToolUse/PostToolUse can be
	// delivered after the main agent's Stop already set done — and because the
	// subagent's own Stop is suppressed above, nothing would restore done.
	// Only a main-agent event (empty AgentID) may resume a finished session.
	if status == detect.StatusRunning && event.AgentID != "" &&
		(sess.Status == detect.StatusDone || sess.Status == detect.StatusStopped) {
		if d.cfg.Verbose {
			log.Printf("event: suppressing %s (tool=%s agent=%s) clearing %v on session %s", event.EventType, event.ToolName, event.AgentID, sess.Status, key)
		}
		return
	}
	sess.Status = status
	// Track the #698 background-work hold for the sweep (#706): set exactly
	// when a held main-agent Stop is applied, cleared by the main agent's next
	// applied event. Subagent events (AgentID set) must not touch it — the
	// backgrounded subagent's own PreToolUse/PostToolUse land on the same
	// session key, and they are the very in-flight work the hold protects;
	// letting them clear it would re-expose the session to the title sweep in
	// the gaps between the subagent's tool calls. Suppressed/dropped events
	// return above and never clear it either.
	if event.AgentID == "" {
		sess.BackgroundHold = event.EventType == "Stop" && (event.BackgroundWork || event.BackgroundTasksActive)
		sess.BackgroundTasksActive = sess.BackgroundHold && event.BackgroundTasksActive
	}
	switch status {
	case detect.StatusNeedInput:
		sess.AttentionSource = attentionSource(event)
		if d.cfg.Verbose {
			log.Printf("attention: session=%s source=%s event=%s tool=%s", key, sess.AttentionSource, event.EventType, event.ToolName)
		}
	case detect.StatusRunning:
		sess.AttentionSource = ""
	}

	// Ignored events above must leave a rejected pane detached. Only applied
	// events recheck it through the frontend, including a reused pane ID after
	// a tmux server restart.
	if recheckMissingPane {
		adoptEventPane(sess, event, requirePID)
	}
	obs := d.frontend.OnEvent(sess, event)
	d.applyObservations(key, sess, obs)
	d.broadcast()
}

// paneAdoptable decides whether the event's pane may become the session's
// association, before the #1500 missing-pair recheck (#1529). A binding outranks
// every hook pane. A Codex event that sent a socket must carry a server pid, and
// may not take a pane another session is bound to. Every other event (Claude,
// sandboxed Codex without a socket, a pane-only event) keeps the earlier rule:
// any non-empty pane is adopted.
func (d *Daemon) paneAdoptable(key string, sess *frontend.SessionState, event ipc.HookEvent, sentSocket bool) bool {
	if event.TmuxPane == "" {
		return false
	}
	if b := sess.Binding; b != nil {
		if d.cfg.Verbose && (event.TmuxPane != b.Pane || event.TmuxSocket != b.Socket) {
			log.Printf("event: Codex session %q is bound to pane %q; ignoring the hook pane %q", key, b.Pane, event.TmuxPane)
		}
		return false
	}
	if !sentSocket || sess.Agent != "codex" || sess.SessionID == "" {
		return true
	}
	refuse := ""
	switch {
	case event.TmuxServerPID == "":
		refuse = "carries no tmux server pid"
	case d.boundElsewhere(key, event.TmuxSocket, event.TmuxPane):
		refuse = "names a pane another session is bound to"
	default:
		return true
	}
	if sess.TmuxPane == "" {
		sess.Unproven = true
	}
	if d.cfg.Verbose {
		log.Printf("event: Codex %q event on session %q %s; not attaching pane %q", event.EventType, key, refuse, event.TmuxPane)
	}
	return false
}

// boundElsewhere reports whether a session other than key is bound to the pane.
func (d *Daemon) boundElsewhere(key, socket, pane string) bool {
	for k, s := range d.sessions {
		if k != key && s.Binding != nil && s.Binding.Socket == socket && s.Binding.Pane == pane {
			return true
		}
	}
	return false
}

// adoptEventPane makes the event's pane the session's association. The socket
// and the claimed server pid travel with the pane.
func adoptEventPane(sess *frontend.SessionState, event ipc.HookEvent, requirePID bool) {
	sess.TmuxPane = event.TmuxPane
	sess.TmuxSocket = event.TmuxSocket
	sess.RequireServerPID = requirePID
	sess.TmuxServerPID = ""
	if requirePID {
		sess.TmuxServerPID = event.TmuxServerPID
	}
}

// detachPaneless drops the session's pane association and binding, keeping the
// session itself, and marks it unproven. With remember it records the refused
// (pane, socket) pair so ignored events cannot re-attach it (#1500).
func detachPaneless(sess *frontend.SessionState, remember bool) {
	if remember {
		sess.MissingTmuxPane = sess.TmuxPane
		sess.MissingTmuxSocket = sess.TmuxSocket
	}
	sess.TmuxPane = ""
	sess.TmuxSocket = ""
	sess.TmuxServerPID = ""
	sess.RequireServerPID = false
	sess.Binding = nil
	sess.Unproven = true
}

// applyObservations applies what the frontend learned while handling an event
// (or a bind's restyle) to the session.
func (d *Daemon) applyObservations(key string, sess *frontend.SessionState, obs frontend.Observations) {
	switch {
	case obs.ServerPIDMismatch:
		// The pane is listed under another server pid (#1529): a different
		// server lifetime, or a reused pane id. The frontend wrote nothing;
		// the session stays listed, paneless, and any binding is void.
		if d.cfg.Verbose {
			log.Printf("event: Codex session %q pane %q is listed under another tmux server pid; retaining as paneless", key, sess.TmuxPane)
		}
		detachPaneless(sess, true)
	case obs.UntrackedPaneMissing && sess.Agent == "codex" && sess.SessionID != "":
		// Codex's shared app-server can inherit the tmux environment of a
		// client that has since closed. A new thread's events still carry that
		// dead pane. Keep the independently identified session visible in
		// status/counts rather than letting the pane-gone sweep immediately
		// delete it. Previously tracked windows retain normal pane-gone
		// cleanup; a listing error is not evidence.
		if d.cfg.Verbose {
			log.Printf("event: Codex session %s supplied an untracked missing pane; retaining as paneless", key)
		}
		detachPaneless(sess, true)
	}
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
}

// recordModelSwitch appends a PostModelSwitch event to the session's display
// list, or counts it as overflow past modelswitch.MaxSwitches. The event socket
// also receives events from containers, so every free-text field is validated
// again here and an invalid value becomes a placeholder, never a snapshot value.
func (d *Daemon) recordModelSwitch(key string, sess *frontend.SessionState, event ipc.HookEvent) {
	sw := watch.ModelSwitch{
		FromModel: modelswitch.SanitizeModel(event.FromModel),
		ToModel:   modelswitch.SanitizeModel(event.ToModel),
		Source:    modelswitch.SanitizeSource(event.ModelSwitchSource),
		Subagent:  event.AgentID != "",
	}
	if sw.Subagent {
		sw.AgentType = modelswitch.SanitizeAgentType(event.AgentType)
	}
	sw.Fallback = modelswitch.Fallback(sw.Source)

	if d.cfg.Verbose {
		if sw.FromModel != event.FromModel || sw.ToModel != event.ToModel ||
			(sw.Subagent && sw.AgentType != event.AgentType) ||
			(event.ModelSwitchSource != "" && sw.Source != event.ModelSwitchSource) {
			log.Printf("model-switch: replaced invalid field(s) with placeholders on session %s", key)
		}
	}
	if len(sess.ModelSwitches) >= modelswitch.MaxSwitches {
		sess.ModelSwitchesMore++
		if d.cfg.Verbose {
			log.Printf("model-switch: session %s is past the %d switch cap, counted %d more", key, modelswitch.MaxSwitches, sess.ModelSwitchesMore)
		}
		return
	}
	sess.ModelSwitches = append(sess.ModelSwitches, sw)
	if d.cfg.Verbose {
		log.Printf("model-switch: session=%s %s -> %s source=%s subagent=%t status unchanged", key, sw.FromModel, sw.ToModel, sw.Source, sw.Subagent)
	}
}

// suppressNeedInputClear reports whether a Running-mapping event must be
// ignored while a session is waiting on user input (NeedInput), rather than
// clearing it back to running (#544). Two cases:
//
//   - The event belongs to a subagent (Task tool delegation, AgentID set).
//     A backgrounded subagent fires its own PreToolUse/PostToolUse on the
//     same session_id as the main agent; those must never affect the main
//     session's attention state, matching the existing Done/Stopped guard
//     in mapEventToStatus (#277).
//   - The event is a PostToolUse for a tool call that started before the
//     input-blocking tool and is only now completing (its tool name doesn't
//     match the tool recorded in AttentionSource). Each `cenci notify`
//     invocation is an independent process racing to the daemon's socket,
//     so a prior tool's completion can be delivered after the
//     PreToolUse(AskUserQuestion) that set NeedInput.
//
// A PreToolUse from the main agent itself is never suppressed — it always
// means the agent has resumed and is doing new work (see
// TestDaemon_AskUserQuestionSetsNeedInput).
func suppressNeedInputClear(event ipc.HookEvent, sess *frontend.SessionState) bool {
	if event.AgentID != "" {
		return true
	}
	if event.EventType != "PostToolUse" {
		return false
	}
	expectedTool, ok := strings.CutPrefix(sess.AttentionSource, "input-tool:")
	return ok && expectedTool != "" && expectedTool != event.ToolName
}

func attentionSource(event ipc.HookEvent) string {
	if event.EventType == "PermissionRequest" {
		return "permission-request"
	}
	if event.EventType == "PreToolUse" {
		return "input-tool:" + event.ToolName
	}
	if event.EventType == "Notification" {
		return "notification:" + event.NotificationType
	}
	return "native-alert"
}

// mapEventToStatus converts a hook event to a detect.Status.
func (d *Daemon) mapEventToStatus(event ipc.HookEvent) detect.Status {
	status := d.mapEventToStatusRaw(event)
	// A subagent (Task tool delegation) fires its own terminal events on the
	// same session_id as the main agent. Only the main agent's own terminal
	// events (empty AgentID) may flip the session to done/stopped — a
	// subagent's Stop/agent_completed must not interrupt the main
	// delegation's running status.
	if event.AgentID != "" && (status == detect.StatusDone || status == detect.StatusStopped) {
		if d.cfg.Verbose {
			log.Printf("event: suppressing %s for subagent %s on session %s (status=%v)", event.EventType, event.AgentID, sessionKeyForEvent(event), status)
		}
		return detect.StatusUnknown
	}
	return status
}

func (d *Daemon) mapEventToStatusRaw(event ipc.HookEvent) detect.Status {
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
		// The main agent's turn can end while work it backgrounded (a
		// subagent/fork, a workflow, a background shell command, a monitor) is
		// still in flight. Claude Code reports that work on the Stop event, and
		// the session is paused waiting to be woken by it — not done (#698).
		// Restricted to main-agent events so a subagent-scoped Stop stays the
		// no-op the guard in mapEventToStatus makes it (#277).
		if (event.BackgroundWork || event.BackgroundTasksActive) && event.AgentID == "" {
			if d.cfg.Verbose {
				log.Printf("event: Stop on session %s reports in-flight background work, holding running instead of done", sessionKeyForEvent(event))
			}
			return detect.StatusRunning
		}
		return detect.StatusDone
	case "Interrupt":
		// Codex's native turn-interrupt hook (the user pressed ESC). A
		// subagent-tagged Interrupt is suppressed by mapEventToStatus, like any
		// other terminal event.
		return detect.StatusStopped
	case "StopFailure":
		// Turn died on an API error (rate_limit, overloaded, billing). Reuse
		// the Stopped state — there is no separate Attention status.
		return detect.StatusStopped
	default:
		return detect.StatusUnknown
	}
}
