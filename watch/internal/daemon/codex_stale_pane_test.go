package daemon

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

func TestDaemon_CodexStaleInheritedPaneLifecycle(t *testing.T) {
	mc := &tmuxtest.MockClient{}
	d := newTestDaemon(mc)
	event := ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "host-thread", Agent: "codex", TmuxPane: "%136", TaskName: "Fix host setup"}
	d.handleEvent(event)
	d.runSweep()
	snap := d.buildSnapshot()
	if len(snap.Windows) != 1 || snap.Windows[0].Status != "running" || snap.Windows[0].WindowName != "Fix host setup" || snap.Windows[0].PaneID != "" {
		t.Fatalf("stale inherited pane must remain visible without a window: %+v", snap)
	}
	event.EventType = "Stop"
	d.handleEvent(event)
	d.runSweep()
	snap = d.buildSnapshot()
	if len(snap.Windows) != 1 || snap.Windows[0].Status != "done" {
		t.Fatalf("Stop must update the retained session: %+v", snap)
	}
	// A subsequent event carrying a real pane can attach normally.
	mc.Panes = []tmux.PaneInfo{{SessionName: "main", WindowIndex: "1", PaneID: "%171", PaneCurrentCmd: "codex", PaneTitle: "Fix host setup"}}
	event.EventType, event.TmuxPane = "PreToolUse", "%171"
	d.handleEvent(event)
	if snap = d.buildSnapshot(); len(snap.Windows) != 1 || snap.Windows[0].PaneID != "%171" {
		t.Fatalf("fresh pane did not attach: %+v", snap)
	}
	event.EventType = "SessionEnd"
	d.handleEvent(event)
	if snap = d.buildSnapshot(); len(snap.Windows) != 0 {
		t.Fatalf("SessionEnd must remove the session: %+v", snap)
	}
	// Missing-pane fallback still follows the ordinary paneless TTL.
	event.EventType, event.TmuxPane = "PreToolUse", "%136"
	d.handleEvent(event)
	d.now = func() time.Time { return time.Now().Add(d.cfg.SessionTTL + time.Second) }
	d.runSweep()
	if snap = d.buildSnapshot(); len(snap.Windows) != 0 {
		t.Fatalf("stale paneless session did not expire: %+v", snap)
	}
}

func TestDaemon_StalePaneFallbackIsNarrow(t *testing.T) {
	for _, tc := range []struct {
		name, agent, session string
		tracked, listError   bool
		wantKept             bool
	}{
		{name: "Claude missing pane still expires", agent: "claude", session: "s"},
		{name: "Codex without a session ID still expires", agent: "codex"},
		{name: "formerly tracked Codex pane still expires", agent: "codex", session: "s", tracked: true},
		{name: "listing failure does not prove a stale pane", agent: "codex", session: "s", listError: true, wantKept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mc := &tmuxtest.MockClient{}
			d := newTestDaemon(mc)
			event := ipc.HookEvent{EventType: "PreToolUse", Agent: tc.agent, SessionID: tc.session, TmuxPane: "%1"}
			if tc.tracked {
				mc.Panes = []tmux.PaneInfo{{SessionName: "main", WindowIndex: "1", PaneID: "%1", PaneCurrentCmd: "codex"}}
				d.handleEvent(event)
				mc.Panes = nil
			}
			if tc.listError {
				mc.ListErr = errors.New("tmux unavailable")
			}
			d.handleEvent(event)
			d.runSweep()
			if got := len(d.buildSnapshot().Windows); (got == 1) != tc.wantKept {
				t.Fatalf("session count = %d, wantKept %v", got, tc.wantKept)
			}
			if tc.listError && d.sessions[tc.session].TmuxPane != "%1" {
				t.Fatal("a listing error must not detach the pane")
			}
		})
	}
}

// The %136 cases carry a trusted socket and the hook's server pid (#1529): the
// once-rejected pair re-attaches only when the live server pid matches it.
func TestDaemon_CodexStalePaneSurvivesIgnoredToolCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, freshPane string
		socket          bool
		livePID         string
		wantAttach      bool
	}{
		{name: "%171", freshPane: "%171", wantAttach: true},
		{name: "%136", freshPane: "%136", socket: true, livePID: "4242", wantAttach: true},
		{name: "%136 under another server pid", freshPane: "%136", socket: true, livePID: "7777"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mc := &tmuxtest.MockClient{}
			d := newTestDaemon(mc)
			event := ipc.HookEvent{EventType: "PreToolUse", SessionID: "waiting-thread", Agent: "codex", TmuxPane: "%136", ToolName: "request_user_input"}
			if tc.socket {
				event.TmuxSocket = filepath.Join(trustedSocketDir(t, d), "default")
				event.TmuxServerPID = "4242"
				mc.ServerPIDBySocket = map[string]string{event.TmuxSocket: tc.livePID}
			}
			d.handleEvent(event)
			// A parallel tool's delayed completion must preserve both the input
			// request and the already rejected pane association.
			event.EventType, event.ToolName = "PostToolUse", "exec_command"
			d.handleEvent(event)
			d.runSweep()
			snap := d.buildSnapshot()
			if len(snap.Windows) != 1 || snap.Windows[0].Status != "need-input" || snap.Windows[0].PaneID != "" {
				t.Fatalf("ignored completion lost the waiting paneless session: %+v", snap)
			}
			// Applied events recheck even a previously rejected pair, allowing
			// attachment after a server restart reuses the pane ID.
			fresh := []tmux.PaneInfo{{SessionName: "main", WindowIndex: "1", PaneID: tc.freshPane, PaneCurrentCmd: "codex"}}
			if tc.socket {
				mc.PanesBySocket = map[string][]tmux.PaneInfo{event.TmuxSocket: fresh}
			} else {
				mc.Panes = fresh
			}
			event.EventType, event.TmuxPane = "PreToolUse", tc.freshPane
			d.handleEvent(event)
			d.runSweep()
			snap = d.buildSnapshot()
			if !tc.wantAttach {
				if len(snap.Windows) != 1 || snap.Windows[0].PaneID != "" || len(mc.WindowOpts) != 0 {
					t.Fatalf("a pane under another server pid attached: %+v, window writes %+v", snap, mc.WindowOpts)
				}
				return
			}
			if len(snap.Windows) != 1 || snap.Windows[0].PaneID != tc.freshPane || snap.Windows[0].Status != "running" {
				t.Fatalf("valid pane did not attach after ignored completion: %+v", snap)
			}
		})
	}
}
