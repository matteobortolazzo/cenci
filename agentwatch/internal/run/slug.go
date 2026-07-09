package run

import (
	"os/exec"
	"strings"
	"unicode/utf8"

	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/frontend"
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

// windowName computes the <number>-<slug> join key from the full workflow
// argument (which mirrors the skills' `<ticket-id | task description>
// [additional context]`). When the first token is a numeric ticket id, the slug
// is taken from --slug, else a gh title, else any trailing context; the result
// is `<number>-<slug>` or the bare number. A non-numeric argument (a free-text
// task description) is slugified whole. The result is sanitized and capped,
// keeping the leading number intact.
func windowName(ticketArg, slug, ghTitle string) string {
	fields := strings.Fields(ticketArg)
	id := ""
	if len(fields) > 0 {
		id = strings.TrimPrefix(fields[0], "#")
	}

	if isNumeric(id) {
		effSlug := slugify(slug)
		if effSlug == "" {
			effSlug = slugify(ghTitle)
		}
		if effSlug == "" && len(fields) > 1 {
			effSlug = slugify(strings.Join(fields[1:], " "))
		}
		name := id
		if effSlug != "" {
			name = id + "-" + effSlug
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

// ticketID returns the first whitespace token of a workflow argument with any
// leading '#' stripped — the ticket number the skills and gh operate on.
func ticketID(ticketArg string) string {
	fields := strings.Fields(ticketArg)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimPrefix(fields[0], "#")
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

// ghTitle best-effort fetches an issue title via the gh CLI. Any failure (gh
// missing, unauthenticated, no such issue) yields "" so the zero-arg board
// action still produces a usable bare-number window name.
func ghTitle(number string) string {
	out, err := exec.Command("gh", "issue", "view", number, "--json", "title", "-q", ".title").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
