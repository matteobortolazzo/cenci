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

// windowName computes the <number>-<slug> join key. For a numeric ticket the
// slug is used when present, otherwise a slugified gh title, otherwise the bare
// number. A non-numeric ticket is itself slugified. The result is sanitized and
// length-capped, keeping the leading number intact.
func windowName(ticket, slug, ghTitle string) string {
	effSlug := slugify(slug)
	if effSlug == "" {
		effSlug = slugify(ghTitle)
	}

	var name string
	switch {
	case isNumeric(ticket):
		if effSlug != "" {
			name = ticket + "-" + effSlug
		} else {
			name = ticket
		}
	default:
		name = slugify(ticket)
		if name == "" {
			name = effSlug
		}
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
