// Package frontend defines the seam between the daemon core (session-keyed
// state, event routing) and the surfaces that present it. tmux is the one
// interactive frontend (window rename/style); waybar, noctalia, and dms are
// read-only display frontends over the shared status JSON and never implement
// this interface.
package frontend

import (
	"errors"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// SessionState is the daemon core's view of one agent session.
type SessionState struct {
	SessionID       string
	Agent           string // claude, codex, opencode, or empty when unknown
	Status          detect.Status
	AttentionSource string // permission-request, input-tool, action-required-title, native-alert
	TaskName        string
	// PromptTaskName is true after the first non-empty prompt label is pinned.
	PromptTaskName bool
	TmuxPane       string    // tmux pane ID (e.g. %5); empty = paneless session
	TmuxSocket     string    // tmux server socket of TmuxPane; empty = the ambient server
	LastEvent      time.Time // last event time, drives paneless TTL expiry
	// MissingTmuxPane/Socket remember the Codex association rejected by a
	// successful listing. Ignored events must not reattach that same pair;
	// applied events still check it again so a live pane can attach normally.
	MissingTmuxPane   string
	MissingTmuxSocket string
	// TmuxServerPID is the tmux server pid the association with TmuxPane was
	// claimed under: the event's $TMUX pid, or the binding's. RequireServerPID
	// makes OnEvent compare it with the live pid from the same listing before
	// any tmux write (#1529). It is set only for Codex sessions whose event
	// carried a tmux socket, and for bound sessions; every other association
	// keeps the pre-#1529 path.
	TmuxServerPID    string
	RequireServerPID bool
	// Binding is the pane `cenci bind` pinned this session to (#1529). While
	// set it outranks the pane the session's hook events carry. In memory
	// only: a daemon restart drops it.
	Binding *PaneBinding
	// Unproven is true after a pane-carrying Codex event was refused (missing
	// pane, an empty, malformed or mismatched server pid) or a bind elsewhere
	// detached the session. The snapshot reports it only while the session has
	// no window.
	Unproven bool
	// BackgroundHold is true while the session's latest main-agent Stop was
	// held at running because in-flight background work will wake it (#698).
	// While set, the tmux sweep's idle-title detection must not flip the
	// session to stopped — the pane legitimately sits at the prompt with an
	// idle-marker title until the background work wakes it (#706). The hold
	// expires after BackgroundHoldTTL of event silence only when no active
	// tasks were explicitly reported (#1079).
	BackgroundHold bool
	// BackgroundTasksActive distinguishes actual tasks from scheduled wakeups
	// and legacy BackgroundWork-only producers. It is meaningful only during
	// BackgroundHold and clears with the next applied main-agent event.
	BackgroundTasksActive bool
	// ModelSwitches are the PostModelSwitch records of this session and its
	// subagents, oldest first and capped by the daemon; ModelSwitchesMore counts
	// those past the cap (#1179). Display only: they never feed Status.
	ModelSwitches     []watch.ModelSwitch
	ModelSwitchesMore int
	// NativeMeasurement is the latest validated native Claude measurement for
	// this session and NativeMeasuredAt its measured_at, which orders later
	// samples (#1450). Display only: they never feed Status or LastEvent.
	NativeMeasurement *watch.NativeMeasurement
	NativeMeasuredAt  time.Time
}

// PaneBinding is the (socket, pane, server pid) a `cenci bind` recorded for a
// Codex session after the frontend proved the pane is listed on that socket
// under that pid (#1529).
type PaneBinding struct {
	Socket    string
	Pane      string
	ServerPID string
}

// CheckPane's failure classes (#1529). ErrPaneListing is a failed listing,
// which proves nothing about the pane; the other two are proof from a
// successful listing.
var (
	ErrPaneListing       = errors.New("tmux pane listing failed")
	ErrPaneNotListed     = errors.New("pane is not listed on that tmux server")
	ErrServerPIDMismatch = errors.New("tmux server pid does not match the live server")
)

// BackgroundHoldTTL releases timer-only and legacy background holds after
// event silence (#1079). Explicitly reported active tasks are exempt: shells
// and subagents can be quiet for longer than this without having completed.
// Their next applied main-agent event resolves the hold; session teardown and
// the normal pane-gone/paneless session TTL cleanup still apply.
const BackgroundHoldTTL = 2 * time.Minute

// RepoDirOption is the tmux window user option `cenci run` writes on every
// window it spawns (#1250): the absolute directory the window was launched in.
// The tmux frontend reads it once, when the window is first tracked, and
// exposes it as WindowInfo.RepoDir; the window-cleanup pass (#1251) uses it to
// resolve the window's repository. A reader must trust the value only when it
// is an absolute path to an existing directory; anything else is treated as
// "repo unknown" and the window is left alone.
const RepoDirOption = "@cenci-repo-dir"

// Key returns the session key used in the daemon's sessions map: the agent
// session ID when known, otherwise a pane-derived fallback. The fallback
// carries the socket when it is non-empty ("pane:<id>@<socket>"), because pane
// ids repeat across tmux servers; an empty socket keeps "pane:<id>". It must
// stay in sync with the daemon's sessionKeyForEvent.
func (s *SessionState) Key() string {
	if s.SessionID != "" {
		return s.SessionID
	}
	if s.TmuxPane != "" {
		if s.TmuxSocket != "" {
			return "pane:" + s.TmuxPane + "@" + s.TmuxSocket
		}
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
	// OnSessionHandoff releases the ended session's claim on its
	// presentation state while keeping that state alive for a successor
	// session on the same pane — SessionEnd reason "clear" or "resume",
	// where the agent continues under a new session id (tmux: keep the
	// window tracked and styled; the successor's first event re-keys it).
	// Implementations fall back to OnSessionEnd when the pane is gone (#707).
	OnSessionHandoff(sess *SessionState)
	// Sweep reconciles presentation state with reality (tmux: pane
	// migrate/cleanup/idle-detect) and reports core-state changes.
	Sweep(sessions map[string]*SessionState) []SweepAction
	// Cleanup restores all presentation state on daemon shutdown.
	Cleanup(sessions map[string]*SessionState)
	// WindowInfo returns display fields for the session's window, or nil
	// when the frontend has none (paneless sessions).
	WindowInfo(sessionKey string) *WindowInfo
	// RenderHeadroom pushes per-agent-type token-budget headroom (0.0-1.0,
	// keyed by agent type as reported by the reconciler) to the frontend's
	// presentation layer (tmux: session-scoped @cenci-headroom-<agent>
	// user variables). Agents absent from headroom that were previously
	// present must have their presentation state cleared.
	RenderHeadroom(headroom map[string]float64)
	// CheckPane proves pane is listed on the tmux server at socket and that
	// the server's live pid is pid, from one listing and with no tmux write
	// (#1529). On success it returns the pane's window target on that server
	// (session:window_index, the WindowInfo Session and WindowIndex joined by
	// ":"); otherwise an error matching ErrPaneListing, ErrPaneNotListed or
	// ErrServerPIDMismatch.
	CheckPane(socket, pane, pid string) (window string, err error)
}

// Observations carries facts a frontend learned while handling an event.
type Observations struct {
	UntrackedPaneMissing bool     // successful listing proved the supplied pane absent; no prior window was tracked
	ServerPIDMismatch    bool     // the pane is listed, but under a server pid other than sess.TmuxServerPID; nothing was written (#1529)
	TaskNameHint         string   // task name resolved from the pane (title-derived; codex path)
	AgentHint            string   // agent inferred from the pane's running command
	EvictedKeys          []string // core sessions whose presentation state was discarded (dead panes)
}

// SweepAction asks the core to update or remove a session after a sweep.
// A zero-valued action (only SessionKey set) records a display-only change
// (e.g. window renumbering) so the daemon still rebroadcasts.
type SweepAction struct {
	SessionKey      string
	Remove          bool
	NewStatus       detect.Status
	NewTask         string
	AttentionSource string
	// PaneGone is set when the sweep detected a tmux-backed session whose
	// pane no longer exists; it asks the daemon core to trigger the orphan
	// reaper, coalesced once per sweep pass.
	PaneGone bool
}

// WindowInfo is the tmux-specific slice of a snapshot entry.
type WindowInfo struct {
	Session       string
	WindowIndex   string
	WindowName    string
	ManuallyNamed bool
	// RepoDir is the raw, unvalidated value of the window's RepoDirOption, read
	// when the window was first tracked ("" when unset or unreadable). Only
	// ticketstate.Resolver validates it.
	RepoDir string
	// PaneID is the tmux pane id (e.g. "%5") of the tracked agent pane. Unlike
	// Session:WindowIndex it is never reused or shifted while the tmux server
	// runs, so a deferred kill bound to it cannot land on another window (#1251).
	// Daemon-internal: it is not copied into the IPC snapshot.
	PaneID string
	// Socket is the tmux server socket the window lives on; empty = the ambient
	// server.
	Socket string
}
