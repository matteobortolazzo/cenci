package daemon

import (
	"strings"
	"testing"

	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/detect"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/ipc"
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
		{"Notification other", ipc.HookEvent{EventType: "Notification", NotificationType: "other"}, detect.StatusUnknown},
		{"PreToolUse AskUserQuestion", ipc.HookEvent{EventType: "PreToolUse", ToolName: "AskUserQuestion"}, detect.StatusNeedInput},
		{"PreToolUse EnterPlanMode", ipc.HookEvent{EventType: "PreToolUse", ToolName: "EnterPlanMode"}, detect.StatusNeedInput},
		{"PreToolUse ExitPlanMode", ipc.HookEvent{EventType: "PreToolUse", ToolName: "ExitPlanMode"}, detect.StatusNeedInput},
		{"PreToolUse generic tool", ipc.HookEvent{EventType: "PreToolUse"}, detect.StatusRunning},
		{"PermissionRequest", ipc.HookEvent{EventType: "PermissionRequest"}, detect.StatusNeedInput},
		{"PostToolUse", ipc.HookEvent{EventType: "PostToolUse"}, detect.StatusRunning},
		{"PostToolUseFailure interrupt", ipc.HookEvent{EventType: "PostToolUseFailure", IsInterrupt: true}, detect.StatusStopped},
		{"PostToolUseFailure no interrupt", ipc.HookEvent{EventType: "PostToolUseFailure", IsInterrupt: false}, detect.StatusRunning},
		{"Stop", ipc.HookEvent{EventType: "Stop"}, detect.StatusDone},
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

func TestSanitizeWindowName(t *testing.T) {
	long := strings.Repeat("a", 250)

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"normal ASCII", "hello world", "hello world"},
		{"emoji preserved", "🚀 deploy", "🚀 deploy"},
		{"CJK preserved", "テスト", "テスト"},
		{"null stripped", "hello\x00world", "helloworld"},
		{"newline stripped", "hello\nworld", "helloworld"},
		{"tab stripped", "hello\tworld", "helloworld"},
		{"CR stripped", "hello\rworld", "helloworld"},
		{"escape stripped", "hello\x1bworld", "helloworld"},
		{"bell stripped", "hello\x07world", "helloworld"},
		{"DEL stripped", "hello\x7fworld", "helloworld"},
		{"exceeds 200 runes truncated", long, long[:200]},
		{"all control chars empty", "\x00\x01\x07\x1b\x7f", ""},
		{"leading/trailing whitespace trimmed", "  hello  ", "hello"},
		{"mixed control chars", "hello\x00world\x07", "helloworld"},
		{"tmux format string preserved", "#{session_name}", "#{session_name}"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeWindowName(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeWindowName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestTruncateForLog(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short string under limit", "hello", 50, "hello"},
		{"exactly at limit", strings.Repeat("a", 50), 50, strings.Repeat("a", 50)},
		{"over limit truncated with ellipsis", strings.Repeat("b", 60), 50, strings.Repeat("b", 50) + "..."},
		{"empty string unchanged", "", 50, ""},
		{"multi-byte unicode truncates at rune boundary", strings.Repeat("\u4e16", 60), 50, strings.Repeat("\u4e16", 50) + "..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateForLog(tc.input, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncateForLog(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
			}
		})
	}
}
