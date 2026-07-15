package frontend

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const taskNameMaxLen = 30

// SanitizeName strips control characters and limits length so names are safe
// for every consumer: tmux renames, IPC broadcast, and status output.
func SanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7F {
			continue
		}
		b.WriteRune(r)
	}
	s := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(s) > 200 {
		runes := []rune(s)
		s = string(runes[:200])
	}
	return s
}

// CompactTaskName sanitizes a task name, collapses whitespace, and caps it to
// a display-friendly length.
func CompactTaskName(name string) string {
	s := strings.Join(strings.Fields(SanitizeName(name)), " ")
	if utf8.RuneCountInString(s) <= taskNameMaxLen {
		return s
	}

	runes := []rune(s)
	cutoff := taskNameMaxLen - 3
	if cutoff < 1 {
		cutoff = 1
	}
	short := strings.TrimRight(string(runes[:cutoff]), " \t\n\r.,;:-")
	if short == "" {
		short = string(runes[:cutoff])
	}
	return short + "..."
}

// PromptTaskName returns a compact label from the first non-empty prompt line.
// Callers should discard the raw prompt after deriving this value.
func PromptTaskName(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return ' '
			}
			return r
		}, line)
		if label := CompactTaskName(line); label != "" {
			return label
		}
	}
	return ""
}
