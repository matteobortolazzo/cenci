package detect

import (
	"strings"
	"unicode/utf8"
)

// Status represents the detected state of an agent session.
type Status int

const (
	StatusUnknown   Status = iota
	StatusIdle             // agent running but no active task (fresh prompt)
	StatusDone             // idle, waiting for next prompt
	StatusStopped          // user interrupted (ESC) mid-generation
	StatusRunning          // actively generating/thinking/tool use
	StatusNeedInput        // permission dialog or selection menu
)

func (s Status) String() string {
	switch s {
	case StatusIdle:
		return "idle"
	case StatusDone:
		return "done"
	case StatusStopped:
		return "stopped"
	case StatusRunning:
		return "running"
	case StatusNeedInput:
		return "need-input"
	default:
		return "unknown"
	}
}

// TaskName extracts the task description from a pane title.
// Claude Code prefixes pane titles with a status glyph; other agents may not.
func TaskName(title string) string {
	r, size := utf8.DecodeRuneInString(title)
	if size == 0 {
		return title
	}
	if !IsStatusSymbol(r) {
		return strings.TrimSpace(title)
	}
	rest := title[size:]
	return strings.TrimSpace(rest)
}

// IsStatusSymbol reports whether r is a known Claude Code status symbol
// (braille spinner characters, or the idle/running markers ✶ ✻ ✳).
func IsStatusSymbol(r rune) bool {
	return IsBraille(r) || r == '✶' || r == '✻' || r == '✳'
}

// IsBraille reports whether r is in the Unicode Braille Patterns block (U+2800–U+28FF).
func IsBraille(r rune) bool {
	return r >= 0x2800 && r <= 0x28FF
}
