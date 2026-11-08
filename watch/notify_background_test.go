package main_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// Exercise the real stdin -> notify -> socket path: losing the distinction
// between an active task and a future wakeup makes the daemon expire live work.
func TestNotifyDistinguishesActiveBackgroundTasksFromWakeups(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		active, work bool
	}{
		{"shell", `"background_tasks":[{"type":"shell","status":"running","command":"private command"}]`, true, true},
		{"subagent", `"background_tasks":[{"type":"subagent","status":"running","description":"private task"}]`, true, true},
		{"pending", `"background_tasks":[{"type":"shell","status":"pending"}]`, true, true},
		{"future status", `"background_tasks":[{"type":"subagent","status":"future"}]`, true, true},
		{"completed", `"background_tasks":[{"type":"shell","status":"completed"}]`, false, false},
		{"failed", `"background_tasks":[{"type":"shell","status":"failed"}]`, false, false},
		{"killed", `"background_tasks":[{"type":"subagent","status":"killed"}]`, false, false},
		{"paused", `"background_tasks":[{"type":"subagent","status":"paused"}]`, false, false},
		{"empty", `"background_tasks":[]`, false, false},
		{"cron only", `"session_crons":[{"id":"c1","prompt":"private wakeup"}]`, false, true},
		{"cron and finished shell", `"session_crons":[{"id":"c1"}],"background_tasks":[{"status":"completed"}]`, false, true},
		{"cron and active shell", `"session_crons":[{"id":"c1"}],"background_tasks":[{"status":"running"}]`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "events.sock")
			receiver, err := ipc.NewEventReceiver(socket)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = receiver.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go receiver.Accept(ctx)
			cmd := exec.Command(binaryPath, "notify", "-agent", "claude", "-event-socket", socket)
			cmd.Stdin = strings.NewReader(`{"hook_event_name":"Stop","session_id":"s1",` + tc.fields + `}`)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("notify: %v: %s", err, output)
			}
			select {
			case event := <-receiver.Events():
				encoded, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				if err := json.Unmarshal(encoded, &fields); err != nil {
					t.Fatal(err)
				}
				active, _ := fields["background_tasks_active"].(bool)
				if active != tc.active || event.BackgroundWork != tc.work {
					t.Fatalf("active/work = %v/%v, want %v/%v: %s", active, event.BackgroundWork, tc.active, tc.work, encoded)
				}
				if strings.Contains(string(encoded), "private") {
					t.Fatalf("task contents leaked: %s", encoded)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for notify event")
			}
		})
	}
}
