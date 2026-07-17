package dispatch

import "testing"

func TestParseFrontMatter(t *testing.T) {
	content := `---
version: 1
mode: ticket
ticketId: 42
ticketTitle: "Add dark mode support"
slug: add-dark-mode
isChild: false
isLastChild: false
parentId: null
status: planned
planCommitSha: abc123def
---

## Implementation Plan
do the thing
`
	m, ok := parseFrontMatter(content)
	if !ok {
		t.Fatal("expected front matter to parse")
	}
	cases := map[string]string{
		"ticketId":      "42",
		"ticketTitle":   "Add dark mode support", // quotes stripped
		"status":        "planned",
		"isChild":       "false",
		"parentId":      "null",
		"planCommitSha": "abc123def",
	}
	for k, want := range cases {
		if got := m[k]; got != want {
			t.Errorf("front matter[%q] = %q, want %q", k, got, want)
		}
	}
	// The body must not leak into the map.
	if _, ok := m["## Implementation Plan"]; ok {
		t.Error("body heading leaked into front matter")
	}
}

func TestParseFrontMatterNoFrontMatter(t *testing.T) {
	if _, ok := parseFrontMatter("just a plain file\nno front matter\n"); ok {
		t.Error("expected false for a file without front matter")
	}
	if _, ok := parseFrontMatter("---\nnever closed\n"); ok {
		t.Error("expected false for an unterminated front-matter block")
	}
	// The opening fence must stand on its own line, not run into a key.
	if _, ok := parseFrontMatter("---status: planned\n---\n"); ok {
		t.Error("expected false when --- is not a line of its own")
	}
}

func TestParseFrontMatterBOM(t *testing.T) {
	m, ok := parseFrontMatter("\ufeff---\nstatus: planned\n---\n")
	if !ok {
		t.Fatal("expected front matter to parse past a BOM")
	}
	if m["status"] != "planned" {
		t.Errorf("status = %q, want planned", m["status"])
	}
}

func TestAtoiSafe(t *testing.T) {
	cases := map[string]int{"42": 42, "": 0, "null": 0, " 7 ": 7, "abc": 0}
	for in, want := range cases {
		if got := atoiSafe(in); got != want {
			t.Errorf("atoiSafe(%q) = %d, want %d", in, got, want)
		}
	}
}
