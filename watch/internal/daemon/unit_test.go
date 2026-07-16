package daemon

import (
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
)

func TestDaemon_MapEventToStatus(t *testing.T) {
	d := &Daemon{cfg: testConfig()}

	tests := []struct {
		name  string
		event ipc.HookEvent
		want  detect.Status
	}{
		{"SessionStart", ipc.HookEvent{EventType: "SessionStart"}, detect.StatusIdle},
		{"UserPromptSubmit", ipc.HookEvent{EventType: "UserPromptSubmit"}, detect.StatusRunning},
		{"Notification permission", ipc.HookEvent{EventType: "Notification", NotificationType: "permission_prompt"}, detect.StatusNeedInput},
		{"Notification agent_needs_input", ipc.HookEvent{EventType: "Notification", NotificationType: "agent_needs_input"}, detect.StatusNeedInput},
		{"Notification elicitation_dialog", ipc.HookEvent{EventType: "Notification", NotificationType: "elicitation_dialog"}, detect.StatusNeedInput},
		{"Notification agent_completed", ipc.HookEvent{EventType: "Notification", NotificationType: "agent_completed"}, detect.StatusDone},
		{"Notification other", ipc.HookEvent{EventType: "Notification", NotificationType: "other"}, detect.StatusUnknown},
		{"PreToolUse AskUserQuestion", ipc.HookEvent{EventType: "PreToolUse", ToolName: "AskUserQuestion"}, detect.StatusNeedInput},
		{"PreToolUse request_user_input", ipc.HookEvent{EventType: "PreToolUse", ToolName: "request_user_input"}, detect.StatusNeedInput},
		{"PreToolUse EnterPlanMode", ipc.HookEvent{EventType: "PreToolUse", ToolName: "EnterPlanMode"}, detect.StatusNeedInput},
		{"PreToolUse ExitPlanMode", ipc.HookEvent{EventType: "PreToolUse", ToolName: "ExitPlanMode"}, detect.StatusNeedInput},
		{"PreToolUse generic tool", ipc.HookEvent{EventType: "PreToolUse"}, detect.StatusRunning},
		{"PermissionRequest", ipc.HookEvent{EventType: "PermissionRequest"}, detect.StatusNeedInput},
		{"PermissionDenied", ipc.HookEvent{EventType: "PermissionDenied"}, detect.StatusRunning},
		{"PostToolUse", ipc.HookEvent{EventType: "PostToolUse"}, detect.StatusRunning},
		{"PostToolUseFailure interrupt", ipc.HookEvent{EventType: "PostToolUseFailure", IsInterrupt: true}, detect.StatusStopped},
		{"PostToolUseFailure no interrupt", ipc.HookEvent{EventType: "PostToolUseFailure", IsInterrupt: false}, detect.StatusRunning},
		{"Stop", ipc.HookEvent{EventType: "Stop"}, detect.StatusDone},
		{"StopFailure", ipc.HookEvent{EventType: "StopFailure"}, detect.StatusStopped},
		{"Unknown event", ipc.HookEvent{EventType: "Unknown"}, detect.StatusUnknown},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := d.mapEventToStatus(tc.event)
			if got != tc.want {
				t.Errorf("mapEventToStatus(%q) = %v, want %v", tc.event.EventType, got, tc.want)
			}
		})
	}
}

func TestAttentionSourceClassification(t *testing.T) {
	cases := []struct {
		event ipc.HookEvent
		want  string
	}{
		{ipc.HookEvent{EventType: "PermissionRequest"}, "permission-request"},
		{ipc.HookEvent{EventType: "PreToolUse", ToolName: "request_user_input"}, "input-tool:request_user_input"},
		{ipc.HookEvent{EventType: "Notification", NotificationType: "permission_prompt"}, "notification:permission_prompt"},
	}
	for _, tc := range cases {
		if got := attentionSource(tc.event); got != tc.want {
			t.Errorf("%#v = %q, want %q", tc.event, got, tc.want)
		}
	}
}
