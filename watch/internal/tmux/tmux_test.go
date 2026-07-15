package tmux

import "testing"

func TestParsePanes(t *testing.T) {
	input := "main\t0\tbash\t0\tbash\t~\t%0\n" +
		"main\t1\tclaude\t0\tclaude\t✳ fixing auth bug\t%1\n" +
		"work\t0\tvim\t0\tvim\tmy-project\t%2\n" +
		"work\t0\tvim\t1\tclaude\t⠋ writing tests\t%3\n"

	panes := parsePanes(input)

	if len(panes) != 4 {
		t.Fatalf("expected 4 panes, got %d", len(panes))
	}

	tests := []struct {
		idx       int
		session   string
		winIdx    string
		winName   string
		paneIdx   string
		cmd       string
		title     string
		paneID    string
		target    string
		winTarget string
	}{
		{0, "main", "0", "bash", "0", "bash", "~", "%0", "main:0.0", "main:0"},
		{1, "main", "1", "claude", "0", "claude", "✳ fixing auth bug", "%1", "main:1.0", "main:1"},
		{2, "work", "0", "vim", "0", "vim", "my-project", "%2", "work:0.0", "work:0"},
		{3, "work", "0", "vim", "1", "claude", "⠋ writing tests", "%3", "work:0.1", "work:0"},
	}

	for _, tt := range tests {
		p := panes[tt.idx]
		if p.SessionName != tt.session {
			t.Errorf("pane[%d] session: got %q, want %q", tt.idx, p.SessionName, tt.session)
		}
		if p.WindowIndex != tt.winIdx {
			t.Errorf("pane[%d] windowIndex: got %q, want %q", tt.idx, p.WindowIndex, tt.winIdx)
		}
		if p.WindowName != tt.winName {
			t.Errorf("pane[%d] windowName: got %q, want %q", tt.idx, p.WindowName, tt.winName)
		}
		if p.PaneIndex != tt.paneIdx {
			t.Errorf("pane[%d] paneIndex: got %q, want %q", tt.idx, p.PaneIndex, tt.paneIdx)
		}
		if p.PaneCurrentCmd != tt.cmd {
			t.Errorf("pane[%d] cmd: got %q, want %q", tt.idx, p.PaneCurrentCmd, tt.cmd)
		}
		if p.PaneTitle != tt.title {
			t.Errorf("pane[%d] title: got %q, want %q", tt.idx, p.PaneTitle, tt.title)
		}
		if p.PaneID != tt.paneID {
			t.Errorf("pane[%d] paneID: got %q, want %q", tt.idx, p.PaneID, tt.paneID)
		}
		if p.target() != tt.target {
			t.Errorf("pane[%d] target: got %q, want %q", tt.idx, p.target(), tt.target)
		}
		if p.WindowTarget() != tt.winTarget {
			t.Errorf("pane[%d] winTarget: got %q, want %q", tt.idx, p.WindowTarget(), tt.winTarget)
		}
	}
}

func TestParsePanesEmpty(t *testing.T) {
	panes := parsePanes("")
	if len(panes) != 0 {
		t.Fatalf("expected 0 panes, got %d", len(panes))
	}
}

func TestParsePanesMalformed(t *testing.T) {
	// Lines with fewer than 7 fields should be skipped
	input := "main\t0\tbash\n" +
		"main\t0\tbash\t0\tbash\ttitle\t%0\n"
	panes := parsePanes(input)
	if len(panes) != 1 {
		t.Fatalf("expected 1 pane, got %d", len(panes))
	}
	if panes[0].PaneID != "%0" {
		t.Errorf("expected pane ID %%0, got %q", panes[0].PaneID)
	}
}
