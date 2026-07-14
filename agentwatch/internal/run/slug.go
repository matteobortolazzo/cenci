package run

import (
	"strings"
	"unicode/utf8"

	"github.com/matteobortolazzo/agent-stack/agentwatch/v2/internal/frontend"
)

// windowNameMaxLen caps the join key so it stays readable in tmux, board cards,
// and status snapshots.
const windowNameMaxLen = 40

// slugify lowercases, turns spaces and underscores into dashes, strips anything
// outside [a-z0-9-], and collapses/trims dashes.
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '_' || r == '-':
			b.WriteByte('-')
		default:
			// drop
		}
	}
	// Collapse runs of dashes and trim leading/trailing ones.
	parts := strings.FieldsFunc(b.String(), func(r rune) bool { return r == '-' })
	return strings.Join(parts, "-")
}

// windowName computes the window name from the full workflow argument (which
// mirrors the skills' `<ticket-id | task description> [additional context]`)
// and the running workflow (skill). When the first token is a numeric ticket
// id, the name is a short, uniform `<number>-<skill>` (e.g. `230-refine`):
// external tools join on the leading number, so the descriptive ticket title
// is deliberately omitted to keep tmux tabs short and predictable. A
// non-numeric argument (a free-text task description) keeps its descriptive
// slug — an explicit --slug wins, else the whole argument is slugified. The
// result is sanitized and capped, keeping the leading number intact.
func windowName(ticketArg, workflow, slug string) string {
	fields := strings.Fields(ticketArg)
	id := ""
	if len(fields) > 0 {
		id = strings.TrimPrefix(fields[0], "#")
	}

	if isNumeric(id) {
		name := id
		if s := slugify(workflow); s != "" {
			name = id + "-" + s
		}
		return capName(frontend.SanitizeName(name))
	}

	// Free-text task description: an explicit slug wins, else slugify it all.
	name := slugify(slug)
	if name == "" {
		name = slugify(ticketArg)
	}
	return capName(frontend.SanitizeName(name))
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func capName(s string) string {
	if utf8.RuneCountInString(s) <= windowNameMaxLen {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:windowNameMaxLen]), "-")
}
