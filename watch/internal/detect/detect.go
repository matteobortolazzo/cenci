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

// TicketFromWindowName extracts the ticket number from a cenci window name,
// or "" when the name carries none. The convention is "<ticket>-<skill>" (see
// pkg/watch.WindowState's WindowName doc), and a bare "<ticket>" is also
// valid; anything else — a free-text/slug window like "add-dark-mode" — has no
// ticket to join on. The separating hyphen is required so "782x-implement"
// never yields "782". This lives beside TaskName rather than in either
// consumer because both internal/closecmd (the `cenci close` guard) and
// internal/daemon (its deferred pending-close re-check) need the same parse
// and both already depend on this package (#787).
func TicketFromWindowName(windowName string) string {
	i := 0
	for i < len(windowName) && windowName[i] >= '0' && windowName[i] <= '9' {
		i++
	}
	if i == 0 {
		return ""
	}
	if i < len(windowName) && windowName[i] != '-' {
		return ""
	}
	return windowName[:i]
}

// IsStatusSymbol reports whether r is a known Claude Code status symbol: a
// working marker (braille spinner or half-circle, see IsWorkingMarker) or one
// of the idle markers ✶ ✻ ✳. Every symbol here is stripped from pane titles by
// TaskName, so an unrecognised one leaks into the window name cenci sets and
// renders next to cenci's own status symbol (#1039).
func IsStatusSymbol(r rune) bool {
	return IsWorkingMarker(r) || r == '✶' || r == '✻' || r == '✳'
}

// IsWorkingMarker reports whether r is a status symbol that Claude Code shows
// only while the agent is working — the braille spinner, or the half-circle
// marker (◐◑◒◓) it heads the pane title with.
//
// The distinction from the idle markers ✶ ✻ ✳ is load-bearing, not cosmetic:
// internal/frontend/tmux.Frontend.Sweep reads a non-working status symbol in
// the title as "the user pressed ESC" and flips the session to stopped.
// Classifying a working marker as idle would flip every mid-turn session the
// moment its hook events went quiet (a long tool call). Callers that need the
// braille block specifically — Codex's own spinner — should keep using
// IsBraille.
func IsWorkingMarker(r rune) bool {
	return IsBraille(r) || isHalfCircle(r)
}

// IsBraille reports whether r is in the Unicode Braille Patterns block (U+2800–U+28FF).
func IsBraille(r rune) bool {
	return r >= 0x2800 && r <= 0x28FF
}

// isHalfCircle reports whether r is one of the four half-filled circles
// ◐ ◑ ◒ ◓ (U+25D0–U+25D3). The range stops short of U+25D4 (◔, quarter-filled)
// so the neighbouring circle glyphs in the Geometric Shapes block are not
// swept up as status symbols.
func isHalfCircle(r rune) bool {
	return r >= 0x25D0 && r <= 0x25D3
}
