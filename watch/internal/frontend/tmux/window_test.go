package tmux

import (
	"strings"
	"testing"
)

// TestInferAgentOpenCode pins inferAgent's OpenCode alias list (#488 Q&A #5).
// OpenCode's own pane_current_command is unknown/undocumented, so a
// conservative default is used: "opencode" itself plus the JS/Bun runtimes it
// may transiently report ("bun", "node"). Aliasing these to "opencode" (not
// leaving them unrecognized) is the safety property the exit-restore sweep
// depends on — an ambiguous runtime name must read as "still opencode", never
// as "opencode exited", mirroring the existing Claude npm/node-shim guard in
// frontend.go.
func TestInferAgentOpenCode(t *testing.T) {
	cases := map[string]string{
		"opencode":     "opencode",
		"OpenCode":     "opencode",
		"  opencode  ": "opencode",
		"bun":          "opencode",
		"node":         "opencode",
	}
	for cmd, want := range cases {
		if got := inferAgent(cmd); got != want {
			t.Errorf("inferAgent(%q) = %q, want %q", cmd, got, want)
		}
	}
}

// TestInferAgentUnrecognizedCommandStaysEmpty guards the narrow-exclusion
// convention (watch/AGENTS.md): adding OpenCode's alias list must not turn
// inferAgent into a silent "anything unknown -> some agent" catch-all. A
// genuinely unrelated command (not opencode/bun/node, not claude/codex) must
// still resolve to "" (unknown), same as before this ticket.
func TestInferAgentUnrecognizedCommandStaysEmpty(t *testing.T) {
	for _, cmd := range []string{"vim", "python3", "htop", ""} {
		if got := inferAgent(cmd); got != "" {
			t.Errorf("inferAgent(%q) = %q, want \"\" (unrecognized)", cmd, got)
		}
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
		{"multi-byte unicode truncates at rune boundary", strings.Repeat("世", 60), 50, strings.Repeat("世", 50) + "..."},
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
