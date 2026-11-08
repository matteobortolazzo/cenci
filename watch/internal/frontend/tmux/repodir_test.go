package tmux

import (
	"errors"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/config"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	tmuxc "github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
)

// WindowInfo.RepoDir exposes the @cenci-repo-dir window option `cenci run`
// wrote (#1250) so the daemon's window-cleanup pass (#1251) can resolve the
// window's repository. It is read when the window is first tracked and passed
// through raw: only ticketstate.Resolver validates it.
func TestWindowInfo_RepoDirReflectsTheRepoDirWindowOption(t *testing.T) {
	const repoDir = "/work/acme/alpha"
	tests := []struct {
		name string
		mock func(*tmuxtest.MockClient)
		want string
	}{
		{
			name: "option set",
			mock: func(mc *tmuxtest.MockClient) {
				mc.WindowOptValues = map[string]string{"main:0:" + frontend.RepoDirOption: repoDir}
			},
			want: repoDir,
		},
		{
			name: "option missing",
			mock: func(mc *tmuxtest.MockClient) {},
			want: "",
		},
		{
			name: "option unreadable",
			mock: func(mc *tmuxtest.MockClient) {
				mc.WindowOptValues = map[string]string{"main:0:" + frontend.RepoDirOption: repoDir}
				mc.GetOptErr = errors.New("tmux: no server running")
			},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc := &tmuxtest.MockClient{Panes: []tmuxc.PaneInfo{{
				SessionName: "main", WindowIndex: "0", WindowName: "782-implement", PaneIndex: "0",
				PaneCurrentCmd: "claude", PaneTitle: "✳ Claude Code", PaneID: "%0",
			}}}
			tc.mock(mc)
			f := New(config.Default(), mc)
			sess := &frontend.SessionState{SessionID: "sess1", TmuxPane: "%0", Status: detect.StatusIdle}

			f.OnEvent(sess, ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})

			wi := f.WindowInfo("sess1")
			if wi == nil {
				t.Fatal("WindowInfo = nil, want the tracked window (an unreadable option must not stop tracking)")
			}
			if wi.RepoDir != tc.want {
				t.Errorf("WindowInfo.RepoDir = %q, want %q", wi.RepoDir, tc.want)
			}
			// The pane id is the stable identity the window-cleanup kill targets.
			if wi.PaneID != "%0" {
				t.Errorf("WindowInfo.PaneID = %q, want the tracked pane %q", wi.PaneID, "%0")
			}
		})
	}
}

// The @cenci-repo-dir option is read from the window's own server: the ambient
// server has a different value at the same target, and a window on a server
// that never set the option reads empty.
func TestWindowInfo_RepoDirReadFromTheWindowsOwnSocket(t *testing.T) {
	const ownDir, ambientDir = "/work/acme/own", "/work/acme/ambient"
	mc := twoSocketMock()
	mc.WindowOptValues = map[string]string{"main:0:" + frontend.RepoDirOption: ambientDir}
	mc.SocketWindowOptValues = map[string]map[string]string{
		sockA: {"main:0:" + frontend.RepoDirOption: ownDir},
	}
	f := New(config.Default(), mc)

	touch(f, runningOn("a", "%5", sockA))
	touch(f, runningOn("b", "%5", sockB))

	wiA, wiB := f.WindowInfo("a"), f.WindowInfo("b")
	if wiA == nil || wiB == nil {
		t.Fatalf("WindowInfo a=%+v b=%+v, want both tracked", wiA, wiB)
	}
	if wiA.RepoDir != ownDir {
		t.Errorf("A's RepoDir = %q, want %q read from its own server", wiA.RepoDir, ownDir)
	}
	if wiB.RepoDir != "" {
		t.Errorf("B's RepoDir = %q, want empty: B's server never set the option", wiB.RepoDir)
	}
}
