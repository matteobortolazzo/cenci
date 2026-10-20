package tmux

import (
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/config"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	tmuxc "github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

func TestGraphicsProbeDoesNotBecomeWindowName(t *testing.T) {
	const probe = "Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA"
	for _, tc := range []struct {
		name, window, autoRename, want, title string
		manual                                bool
	}{
		{name: "startup", window: "docker", autoRename: "on", want: "Claude Code"},
		{name: "daemon restart with polluted name", window: probe, autoRename: "off", want: "Claude Code"},
		{name: "compact polluted name", window: "Gi=31,s=1,v=1,a=q,t=d,f=24...", autoRename: "off", want: "Claude Code"},
		{name: "polluted name with recovered pane title", window: probe, autoRename: "off", title: "◐ Fix login", want: "Fix login"},
		{name: "legacy status prefix", window: "~ " + probe, autoRename: "off", want: "Claude Code"},
		{name: "manual name", window: "my task", autoRename: "off", want: "my task", manual: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mc := &tmuxtest.MockClient{
				Panes: []tmuxc.PaneInfo{{SessionName: "main", WindowIndex: "0", WindowName: tc.window,
					PaneCurrentCmd: "docker", PaneTitle: probe, PaneID: "%0"}},
				WindowOptValues: map[string]string{"main:0:automatic-rename": tc.autoRename},
			}
			f := New(config.Default(), mc)
			if tc.title != "" {
				mc.Panes[0].PaneTitle = tc.title
			}
			sess := &frontend.SessionState{SessionID: "s", TmuxPane: "%0", Agent: "claude", Status: detect.StatusIdle}
			f.OnEvent(sess, ipc.HookEvent{EventType: "SessionStart", SessionID: "s"})
			assertGraphicsWindow(t, f, mc, tc.want, tc.manual)

			// Normal title updates must still reach auto-named windows after the probe.
			mc.Panes[0].PaneTitle = "◐ Fix login"
			sess.Status = detect.StatusRunning
			f.OnEvent(sess, ipc.HookEvent{EventType: "PreToolUse", SessionID: "s"})
			want := "Fix login"
			if tc.manual {
				want = tc.want
			}
			assertGraphicsWindow(t, f, mc, want, tc.manual)

			f.Cleanup(nil)
			want = tc.window
			if !tc.manual && tc.window != "docker" {
				want = "Claude Code"
			}
			if got, _ := tmuxtest.LastRename(mc.Renames, "main:0"); got != want {
				t.Errorf("cleanup restored %q, want %q", got, want)
			}
		})
	}
}

func TestGraphicsProbePreservesPreviousTask(t *testing.T) {
	mc := &tmuxtest.MockClient{Panes: []tmuxc.PaneInfo{{SessionName: "main", WindowIndex: "0", WindowName: "docker",
		PaneCurrentCmd: "docker", PaneTitle: "◐ Fix login", PaneID: "%0"}}}
	f := New(config.Default(), mc)
	sess := &frontend.SessionState{SessionID: "s", TmuxPane: "%0", Agent: "claude", Status: detect.StatusRunning}
	f.OnEvent(sess, ipc.HookEvent{EventType: "PreToolUse", SessionID: "s"})
	mc.Panes[0].PaneTitle = "Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA"
	sess.Status = detect.StatusDone
	obs := f.OnEvent(sess, ipc.HookEvent{EventType: "Stop", SessionID: "s"})
	assertGraphicsWindow(t, f, mc, "Fix login", false)
	if obs.TaskNameHint != "Fix login" {
		t.Errorf("task hint = %q, want Fix login", obs.TaskNameHint)
	}
}

func TestGraphicsProbeMatchIsNarrow(t *testing.T) {
	for _, title := range []string{"Graphics support", "Gi=31 debugging", "Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA explained"} {
		t.Run(title, func(t *testing.T) {
			mc := &tmuxtest.MockClient{Panes: []tmuxc.PaneInfo{{SessionName: "main", WindowIndex: "0", WindowName: "docker",
				PaneCurrentCmd: "docker", PaneTitle: title, PaneID: "%0"}}}
			f := New(config.Default(), mc)
			sess := &frontend.SessionState{SessionID: "s", TmuxPane: "%0", Agent: "claude", Status: detect.StatusIdle}
			f.OnEvent(sess, ipc.HookEvent{EventType: "SessionStart", SessionID: "s"})
			assertGraphicsWindow(t, f, mc, frontend.CompactTaskName(title), false)
		})
	}
}

func assertGraphicsWindow(t *testing.T, f *Frontend, mc *tmuxtest.MockClient, want string, manual bool) {
	t.Helper()
	if got, _ := tmuxtest.LastRename(mc.Renames, "main:0"); got != want {
		t.Errorf("window name = %q, want %q", got, want)
	}
	wi := f.WindowInfo("s")
	if wi == nil {
		t.Fatal("missing WindowInfo")
	}
	if wi.WindowName != want || wi.ManuallyNamed != manual {
		t.Errorf("WindowInfo = %+v, want name %q, manual %v", wi, want, manual)
	}
}
