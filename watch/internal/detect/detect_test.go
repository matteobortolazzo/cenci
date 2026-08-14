package detect

import "testing"

func TestTaskName(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{"⠋ writing tests", "writing tests"},
		{"✳ fixing auth bug", "fixing auth bug"},
		{"✶ reading files", "reading files"},
		{"◑ fixing auth bug", "fixing auth bug"},
		{"◐ writing tests", "writing tests"},
		{"◒ reading files", "reading files"},
		{"◓ running tests", "running tests"},
		{"plain title", "plain title"},
		{"Codex", "Codex"},
		{"", ""},
	}
	for _, tt := range tests {
		got := TaskName(tt.title)
		if got != tt.want {
			t.Errorf("TaskName(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestIsStatusSymbol(t *testing.T) {
	tests := []struct {
		r    rune
		want bool
	}{
		{'⠋', true},  // braille
		{'⠙', true},  // braille
		{'✶', true},  // six-pointed star
		{'✻', true},  // teardrop star
		{'✳', true},  // idle marker
		{'◐', true},  // half-circle working marker
		{'◑', true},  // half-circle working marker
		{'◒', true},  // half-circle working marker
		{'◓', true},  // half-circle working marker
		{'a', false}, // regular char
		{'!', false}, // punctuation
		{'~', false}, // tilde
		{'●', false}, // U+25CF — outside the half-circle range
	}
	for _, tt := range tests {
		got := IsStatusSymbol(tt.r)
		if got != tt.want {
			t.Errorf("IsStatusSymbol(%q) = %v, want %v", tt.r, got, tt.want)
		}
	}
}

// TestIsWorkingMarker pins the split that keeps the ESC backstop correct:
// braille and half-circle glyphs mean "the agent is working", while the star
// markers mean "idle at the prompt". Only the latter may be read as an idle
// title (see internal/frontend/tmux.Frontend.Sweep).
func TestIsWorkingMarker(t *testing.T) {
	tests := []struct {
		r    rune
		want bool
	}{
		{'⠋', true},     // braille spinner
		{0x2800, true},  // first braille character
		{0x28FF, true},  // last braille character
		{'◐', true},     // U+25D0 — first half-circle
		{'◑', true},     // U+25D1
		{'◒', true},     // U+25D2
		{'◓', true},     // U+25D3 — last half-circle
		{0x25CF, false}, // just below the half-circle range
		{0x25D4, false}, // just above the half-circle range
		{'✶', false},    // idle marker, not a working marker
		{'✻', false},    // idle marker, not a working marker
		{'✳', false},    // idle marker, not a working marker
		{'a', false},    // regular ASCII
	}
	for _, tt := range tests {
		got := IsWorkingMarker(tt.r)
		if got != tt.want {
			t.Errorf("IsWorkingMarker(%U) = %v, want %v", tt.r, got, tt.want)
		}
	}
}

func TestIsBraille(t *testing.T) {
	tests := []struct {
		r    rune
		want bool
	}{
		{0x2800, true},  // first braille character (⠀)
		{0x28FF, true},  // last braille character
		{0x2850, true},  // mid-range braille
		{'⠋', true},     // common spinner character
		{0x27FF, false}, // just below braille range
		{0x2900, false}, // just above braille range
		{'✶', false},    // star marker (not braille)
		{'a', false},    // regular ASCII
	}
	for _, tt := range tests {
		got := IsBraille(tt.r)
		if got != tt.want {
			t.Errorf("IsBraille(%U) = %v, want %v", tt.r, got, tt.want)
		}
	}
}

func TestStatusString(t *testing.T) {
	tests := []struct {
		s    Status
		want string
	}{
		{StatusUnknown, "unknown"},
		{StatusIdle, "idle"},
		{StatusDone, "done"},
		{StatusStopped, "stopped"},
		{StatusRunning, "running"},
		{StatusNeedInput, "need-input"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("Status(%d).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestTicketFromWindowName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"782-implement", "782"},
		{"782", "782"},
		{"782-implement-retry", "782"},
		{"add-dark-mode", ""},
		{"782x-implement", ""},
		{"", ""},
		{"-782", ""},
	}
	for _, tt := range tests {
		if got := TicketFromWindowName(tt.name); got != tt.want {
			t.Errorf("TicketFromWindowName(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
