package frontend

import (
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
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
			got := SanitizeName(tc.input)
			if got != tc.want {
				t.Errorf("SanitizeName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestCompactTaskNameCapsLabel(t *testing.T) {
	got := CompactTaskName("abcdefghijklmnopqrstuvwxyz0123456789")
	want := "abcdefghijklmnopqrstuvwxyz0..."
	if got != want {
		t.Errorf("CompactTaskName() = %q, want %q", got, want)
	}
}

func TestSessionStateKey(t *testing.T) {
	tests := []struct {
		name string
		sess SessionState
		want string
	}{
		{"session id wins", SessionState{SessionID: "s1", TmuxPane: "%5"}, "s1"},
		{"pane fallback", SessionState{TmuxPane: "%5"}, "pane:%5"},
		{"empty", SessionState{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sess.Key(); got != tc.want {
				t.Errorf("Key() = %q, want %q", got, tc.want)
			}
		})
	}
}
