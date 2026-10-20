package daemon

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/closecmd"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// Decode the wire contract so these regressions also catch omitted identity
// fields, rather than requiring the Go types to have them before the red run.
func stableRegistration(t *testing.T, pane string) ipc.PendingClose {
	t.Helper()
	b, err := json.Marshal(map[string]string{"session": "main", "window_index": "0", "window_name": "782-implement", "pane_id": pane})
	if err != nil {
		t.Fatal(err)
	}
	var pc ipc.PendingClose
	if err := json.Unmarshal(b, &pc); err != nil {
		t.Fatal(err)
	}
	return pc
}

type callbackProcKiller struct{ kill func(string) error }

func (k callbackProcKiller) KillWindowProcesses(_ context.Context, target string) error {
	return k.kill(target)
}

func requireStableVictims(t *testing.T, m *tmuxModel, processes, windows []string) {
	t.Helper()
	if !slices.Equal(m.killedProcs, processes) || !slices.Equal(m.killedWindows, windows) {
		t.Fatalf("victims: processes=%v windows=%v; want processes=%v windows=%v; surviving windows=%v", m.killedProcs, m.killedWindows, processes, windows, m.windows)
	}
	if !slices.ContainsFunc(m.windows, func(w modelWindow) bool { return w.pane == "%90" }) {
		t.Fatal("bystander window did not survive")
	}
}

func TestStableClose_DirectDisappearanceDuringShutdown(t *testing.T) {
	model := &tmuxModel{windows: []modelWindow{{pane: "%0", index: 0}}}
	var snap watch.StateSnapshot
	if err := json.Unmarshal([]byte(`{"windows":[{"session":"main","window_index":"0","window_name":"782-implement","status":"done","pane_id":"%0"}]}`), &snap); err != nil {
		t.Fatal(err)
	}
	decisions, err := closecmd.Run(closecmd.Opts{
		Target: "782", ReadSnapshot: func(string) (*watch.StateSnapshot, error) { return &snap, nil }, Killer: model,
		KillProcesses: func(target string) error {
			_ = model.KillWindowProcesses(context.Background(), target)
			model.replace(0, "%90") // process shutdown makes the original window exit
			return nil
		},
	})
	if err != nil || len(decisions) != 1 || decisions[0].Action != closecmd.ActionClosed {
		t.Fatalf("decisions=%v err=%v", decisions, err)
	}
	requireStableVictims(t, model, []string{"%0"}, nil)
}

func TestStableClose_DeferredReplacement(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		for _, during := range []bool{false, true} {
			name := "before execution"
			if during {
				name = "during shutdown"
			}
			if blocked {
				name += " after babysit retry"
			}
			t.Run(name, func(t *testing.T) {
				d := newTestDaemon(babysitTrackedMock())
				model := &tmuxModel{windows: []modelWindow{{pane: "%0", index: 0}}}
				runner := &deferredRunner{}
				d.killer, d.procKiller, d.runOffLoop = model, model, runner.run
				if during {
					d.procKiller = callbackProcKiller{kill: func(target string) error {
						_ = model.KillWindowProcesses(context.Background(), target)
						model.replace(0, "%90")
						return nil
					}}
				}
				guard := &countingGuard{blocked: blocked}
				d.closeGuard = guard.guard
				now := time.Now()
				d.now = func() time.Time { return now }
				d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
				d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
				d.registerPendingClose(stableRegistration(t, "%0"))
				endSession(d)
				if !during {
					model.replace(0, "%90")
				}
				if blocked {
					guard.blocked = false
					now = now.Add(pendingCloseRecheckInterval)
					d.retryBlockedPendingCloses()
				}
				if len(runner.fns) != 1 {
					t.Fatalf("scheduled=%d, want 1", len(runner.fns))
				}
				runner.drain()
				var wantProcs []string
				if during {
					wantProcs = []string{"%0"}
				}
				requireStableVictims(t, model, wantProcs, nil)
			})
		}
	}
}

func TestStableClose_SequentialClosesSurviveRenumbering(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		name := "direct"
		if deferred {
			name = "deferred"
		}
		t.Run(name, func(t *testing.T) {
			fx := newWCFixture(t)
			first := fx.addWindow(782, "")
			second := fx.addWindow(782, "")
			model := &tmuxModel{renumber: true, windows: []modelWindow{{pane: first.pane, index: 0}, {pane: second.pane, index: 1}, {pane: "%90", index: 2}}}
			if deferred {
				runner := &deferredRunner{}
				fx.d.killer, fx.d.procKiller, fx.d.runOffLoop = model, model, runner.run
				for _, w := range []wcWindow{first, second} {
					pc := stableRegistration(t, w.pane)
					pc.WindowIndex = w.index
					fx.d.registerPendingClose(pc)
					fx.d.killPendingClose(fx.d.frontend.WindowInfo(w.key))
				}
				runner.drain()
			} else {
				var snap watch.StateSnapshot
				if err := json.Unmarshal([]byte(`{"windows":[{"session":"main","window_index":"0","window_name":"782-implement","status":"done","pane_id":"%0"},{"session":"main","window_index":"1","window_name":"782-implement","status":"done","pane_id":"%1"}]}`), &snap); err != nil {
					t.Fatal(err)
				}
				_, err := closecmd.Run(closecmd.Opts{Target: "782", ReadSnapshot: func(string) (*watch.StateSnapshot, error) { return &snap, nil }, Killer: model, KillProcesses: func(target string) error { return model.KillWindowProcesses(context.Background(), target) }})
				if err != nil {
					t.Fatal(err)
				}
			}
			requireStableVictims(t, model, []string{first.pane, second.pane}, []string{first.pane, second.pane})
		})
	}
}

func TestStableClose_InvalidIdentityFailsClosed(t *testing.T) {
	for _, pane := range []string{"", "main:0", "%", "%1:0", "%1\n%2", "%١"} {
		t.Run(pane, func(t *testing.T) {
			logs := captureLog(t)
			d := newTestDaemon(babysitTrackedMock())
			d.registerPendingClose(stableRegistration(t, pane))
			if len(d.pending) != 0 || !strings.Contains(logs.String(), "pane") {
				t.Fatalf("invalid registration retained or no diagnostic: pending=%v log=%q", d.pending, logs.String())
			}
			b, err := json.Marshal(map[string]any{"windows": []map[string]string{{"session": "main", "window_index": "0", "window_name": "782-implement", "status": "running", "pane_id": pane}}})
			if err != nil {
				t.Fatal(err)
			}
			var snap watch.StateSnapshot
			if err := json.Unmarshal(b, &snap); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []struct{ force, dryRun bool }{{}, {force: true}, {dryRun: true}, {force: true, dryRun: true}} {
				killer := &fakeKiller{}
				registered := false
				processes := false
				_, err := closecmd.Run(closecmd.Opts{Target: "782", Force: mode.force, DryRun: mode.dryRun, ReadSnapshot: func(string) (*watch.StateSnapshot, error) { return &snap, nil }, Killer: killer, Register: func(watch.WindowState) error { registered = true; return nil }, KillProcesses: func(string) error { processes = true; return nil }})
				if err == nil || !strings.Contains(err.Error(), "pane") || len(killer.killed) != 0 || registered || processes {
					t.Fatalf("mode=%+v invalid identity used: err=%v killed=%v registered=%v processes=%v", mode, err, killer.killed, registered, processes)
				}
			}
		})
	}
}

func TestStableClose_ReplacementSessionEndCannotJoinOldRegistration(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		for _, sweep := range []bool{false, true} {
			name := "session end"
			if sweep {
				name = "sweep prune"
			}
			if blocked {
				name += " with blocked original"
			}
			t.Run(name, func(t *testing.T) {
				mc := babysitTrackedMock()
				mc.Panes[0].PaneID = "%99"
				d := newTestDaemon(mc)
				d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "original", TmuxPane: "%99"})
				d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "original", TmuxPane: "%99"})
				d.registerPendingClose(stableRegistration(t, "%99"))
				if blocked {
					d.closeGuard = (&countingGuard{blocked: true}).guard
					d.handleEvent(ipc.HookEvent{EventType: "SessionEnd", SessionID: "original", TmuxPane: "%99"})
				}
				mc.Panes[0].PaneID = "%0" // a replacement takes the original index
				d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
				d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
				if sweep {
					mc.Panes = nil
					d.runSweep()
				} else {
					endSession(d)
				}
				if killed := d.killer.(*fakeKiller).killed; len(killed) != 0 {
					t.Fatalf("replacement teardown triggered old registration: %v", killed)
				}
				if entry := d.pending["%99"]; len(d.pending) != 1 || entry == nil || entry.pc.PaneID != "%99" || entry.blocked != blocked {
					t.Fatalf("replacement teardown removed or changed original pending close: %+v", d.pending)
				}
			})
		}
	}
}

func TestStableClose_SnapshotCarriesPaneIdentity(t *testing.T) {
	d := newTestDaemon(babysitTrackedMock())
	d.handleEvent(ipc.HookEvent{EventType: "SessionStart", SessionID: "sess1", TmuxPane: "%0"})
	d.handleEvent(ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "sess1", TmuxPane: "%0"})
	b, err := json.Marshal(d.buildSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"pane_id":"%0"`) {
		t.Fatalf("snapshot lacks stable identity: %s", b)
	}
}

func TestStableClose_RenumberedPendingEntryFollowsItsPane(t *testing.T) {
	for _, prune := range []bool{false, true} {
		name := "session end closes original pane"
		if prune {
			name = "sweep prunes vanished pane"
		}
		t.Run(name, func(t *testing.T) {
			mc := babysitTrackedMock()
			d := newTestDaemon(mc)
			startTrackedSession(d)
			mc.Panes[0].WindowIndex = "4"
			d.runSweep()
			if wi := d.frontend.WindowInfo("sess1"); wi == nil || wi.WindowIndex != "4" {
				t.Fatalf("fixture did not migrate window: %+v", wi)
			}
			if len(d.pending) != 1 {
				t.Fatal("pending close disappeared during renumbering")
			}
			if prune {
				mc.Panes = nil
				d.runSweep()
				if got := d.killer.(*fakeKiller).killed; len(got) != 0 {
					t.Fatalf("sweep should only prune: killed=%v", got)
				}
			} else {
				endSession(d)
				if got := d.killer.(*fakeKiller).killed; !slices.Equal(got, []string{"%0"}) {
					t.Fatalf("renumbered pending close killed=%v, want [%%0]", got)
				}
			}
			if len(d.pending) != 0 {
				t.Fatalf("pending entry leaked after window teardown: %+v", d.pending)
			}
		})
	}
}
