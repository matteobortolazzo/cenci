package tmux

import (
	"strings"
	"testing"
)

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
