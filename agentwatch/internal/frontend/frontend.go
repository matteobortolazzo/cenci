// Package frontend defines the seam between the daemon core (session-keyed
// state, event routing) and the surfaces that present it. tmux is the one
// interactive frontend (window rename/style); waybar, noctalia, and dms are
// read-only display frontends over the shared status JSON and never implement
// this interface.
package frontend

import (
	"time"

	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/detect"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/ipc"
)

// SessionState is the daemon core's view of one agent session.
type SessionState struct {
	SessionID string
	Agent     string // claude, codex, or empty when unknown
	Status    detect.Status
	TaskName  string
	TmuxPane  string    // tmux pane ID (e.g. %5); empty = paneless session
	LastEvent time.Time // last event time, drives paneless TTL expiry
}

// Key returns the session key used in the daemon's sessions map: the agent
// session ID when known, otherwise a pane-derived fallback.
func (s *SessionState) Key() string {
	if s.SessionID != "" {
		return s.SessionID
	}
	if s.TmuxPane != "" {
		return "pane:" + s.TmuxPane
	}
	return ""
}

// Frontend is an interactive presentation layer driven by the daemon.
type Frontend interface {
	// OnEvent reacts to a hook event after the core has updated sess
	// (status, pane, agent). The tmux implementation tracks/renames/styles
	// the window; it is a no-op for paneless sessions.
	OnEvent(sess *SessionState, event ipc.HookEvent) Observations
	// OnSessionEnd releases any presentation state for the session
	// (tmux: restore the window).
	OnSessionEnd(sess *SessionState)
	// Sweep reconciles presentation state with reality (tmux: pane
	// migrate/cleanup/idle-detect) and reports core-state changes.
	Sweep(sessions map[string]*SessionState) []SweepAction
	// Cleanup restores all presentation state on daemon shutdown.
	Cleanup(sessions map[string]*SessionState)
	// WindowInfo returns display fields for the session's window, or nil
	// when the frontend has none (paneless sessions).
	WindowInfo(sessionKey string) *WindowInfo
}

// Observations carries facts a frontend learned while handling an event.
type Observations struct {
	TaskNameHint string // task name resolved from the pane (title-derived; codex path)
	AgentHint    string // agent inferred from the pane's running command
}

// SweepAction asks the core to update or remove a session after a sweep.
// A zero-valued action (only SessionKey set) records a display-only change
// (e.g. window renumbering) so the daemon still rebroadcasts.
type SweepAction struct {
	SessionKey string
	Remove     bool
	NewStatus  detect.Status
	NewTask    string
}

// WindowInfo is the tmux-specific slice of a snapshot entry.
type WindowInfo struct {
	Session       string
	WindowIndex   string
	WindowName    string
	ManuallyNamed bool
}
