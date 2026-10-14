package tmux

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

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

// -- ErrWindowGone and ListWindowPaneProcs (#1253) ---------------------------

// failingTmux installs a fake tmux that prints stderrText to stderr and exits 1.
func failingTmux(t *testing.T, stderrText string) {
	t.Helper()
	installFakeTmuxOnPath(t, "printf '%s\\n' "+exectest.ShellQuote(stderrText)+" >&2\nexit 1\n")
}

// goneMessages are tmux's stderr texts for a window or session that does not
// exist.
var goneMessages = []string{"can't find window: 7", "can't find session: work"}

// notGoneMessages are tmux failures that say nothing about the target being
// missing; they must stay plain errors so a real problem is never reported as
// "already closed".
var notGoneMessages = []string{
	"protocol version mismatch (client 8, server 7)",
	"no server running on /tmp/tmux-1000/default",
	"permission denied",
}

func TestKillWindow_MissingTargetIsErrWindowGone(t *testing.T) {
	for _, msg := range goneMessages {
		t.Run(msg, func(t *testing.T) {
			failingTmux(t, msg)

			err := (&ExecClient{}).KillWindow("=work:7")

			if !errors.Is(err, ErrWindowGone) {
				t.Fatalf("KillWindow err = %v, want errors.Is(err, ErrWindowGone)", err)
			}
			if !strings.Contains(err.Error(), msg) {
				t.Errorf("KillWindow err = %q, want tmux's own message %q kept in it", err, msg)
			}
		})
	}
}

func TestKillWindow_UnrelatedFailureIsNotErrWindowGone(t *testing.T) {
	for _, msg := range notGoneMessages {
		t.Run(msg, func(t *testing.T) {
			failingTmux(t, msg)

			err := (&ExecClient{}).KillWindow("=work:7")

			if err == nil {
				t.Fatal("KillWindow err = nil, want the tmux failure")
			}
			if errors.Is(err, ErrWindowGone) {
				t.Errorf("KillWindow err = %v, must not be classified as a gone window", err)
			}
			if !strings.Contains(err.Error(), msg) {
				t.Errorf("KillWindow err = %q, want tmux's message %q in it", err, msg)
			}
		})
	}
}

func TestKillWindow_SuccessIsNil(t *testing.T) {
	installFakeTmuxOnPath(t, "exit 0\n")

	if err := (&ExecClient{}).KillWindow("=work:7"); err != nil {
		t.Errorf("KillWindow err = %v, want nil", err)
	}
}

func TestListWindowPaneProcs_MissingTargetIsErrWindowGone(t *testing.T) {
	for _, msg := range goneMessages {
		t.Run(msg, func(t *testing.T) {
			failingTmux(t, msg)

			panes, err := ListWindowPaneProcs("=work:7")

			if !errors.Is(err, ErrWindowGone) {
				t.Fatalf("ListWindowPaneProcs err = %v, want errors.Is(err, ErrWindowGone)", err)
			}
			if len(panes) != 0 {
				t.Errorf("panes = %+v, want none for a gone window", panes)
			}
		})
	}
}

func TestListWindowPaneProcs_UnrelatedFailureIsNotErrWindowGone(t *testing.T) {
	for _, msg := range notGoneMessages {
		t.Run(msg, func(t *testing.T) {
			failingTmux(t, msg)

			_, err := ListWindowPaneProcs("=work:7")

			if err == nil {
				t.Fatal("ListWindowPaneProcs err = nil, want the tmux failure")
			}
			if errors.Is(err, ErrWindowGone) {
				t.Errorf("ListWindowPaneProcs err = %v, must not be classified as a gone window", err)
			}
			if !strings.Contains(err.Error(), msg) {
				t.Errorf("ListWindowPaneProcs err = %q, want tmux's message %q in it", err, msg)
			}
		})
	}
}

// ownedFormat / optionFormat are the two list-panes formats the listing issues:
// tmux's own fields alone, then the same fields plus the user-writable options.
const (
	ownedFormat  = "#{pane_id}\t#{pane_pid}"
	optionFormat = "#{pane_id}\t#{pane_pid}\t#{@cenci-launch-id}\t#{@cenci-container}\t#{@cenci-runtime}"
)

// twoListTmux installs a fake tmux that logs each argv (one block per call) to
// argsFile and answers the owned-fields listing with owned and the option
// listing with options.
func twoListTmux(t *testing.T, argsFile, owned, options string) {
	t.Helper()
	installFakeTmuxOnPath(t, "printf '%s\\n' \"$@\" >> "+exectest.ShellQuote(argsFile)+"\n"+
		"if [ \"$5\" = "+exectest.ShellQuote(ownedFormat)+" ]; then printf '%s' "+exectest.ShellQuote(owned)+"; else printf '%s' "+exectest.ShellQuote(options)+"; fi\n")
}

// The five columns are split on TAB with only the trailing newline trimmed: a
// pane whose launch options were never set prints trailing TABs, and a
// TrimSpace-based parser would collapse them and shift or drop the columns.
func TestListWindowPaneProcs_ParsesFiveTabFieldsKeepingEmptyColumns(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	const launchID = "0123456789abcdef0123456789abcdef"
	owned := "%1\t123\n%2\t456\n%3\t789\n"
	rows := "%1\t123\t\t\t\n" +
		"%2\t456\t" + launchID + "\tclaude-cenci-x\tdocker\n" +
		"%3\t789\t" + launchID + "\tcodex-cenci-y\t\n"
	twoListTmux(t, argsFile, owned, rows)

	got, err := ListWindowPaneProcs("=work:7")
	if err != nil {
		t.Fatalf("ListWindowPaneProcs: %v", err)
	}

	want := []PaneProc{
		{PaneID: "%1", PanePID: "123"},
		{PaneID: "%2", PanePID: "456", LaunchID: launchID, Container: "claude-cenci-x", Runtime: "docker"},
		{PaneID: "%3", PanePID: "789", LaunchID: launchID, Container: "codex-cenci-y"},
	}
	if len(got) != len(want) {
		t.Fatalf("panes = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pane[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("fake tmux was not invoked: %v", err)
	}
	wantArgs := "list-panes\n-t\n=work:7\n-F\n" + ownedFormat + "\n" +
		"list-panes\n-t\n=work:7\n-F\n" + optionFormat + "\n"
	if string(args) != wantArgs {
		t.Errorf("tmux argv = %q, want %q", string(args), wantArgs)
	}
}

// A user pane option is printed raw by list-panes, so a newline or tab inside
// one can forge extra, well-formed five-field rows with an attacker-chosen
// pane_pid. Only a row whose (pane_id, pane_pid) matches tmux's own listing
// exactly, for a pane_id listed once, is accepted; every other row is dropped
// and reported, while the genuine panes still come back.
func TestListWindowPaneProcs_ForgedOrMismatchedRowsAreDroppedAndReported(t *testing.T) {
	const launchID = "0123456789abcdef0123456789abcdef"
	genuine := PaneProc{PaneID: "%1", PanePID: "123"}
	tests := []struct {
		name    string
		owned   string
		options string
		want    []PaneProc
		errHas  []string
	}{
		{
			name:  "extra row for a pane id tmux never listed",
			owned: "%1\t123\n%2\t456\n",
			// %2's launch-id option holds "x\n%9\t777" (container and runtime unset), so
			// tmux prints a truncated row for %2 followed by a forged five-field %9 row.
			options: "%1\t123\t\t\t\n%2\t456\tx\n%9\t777\t\t\t\n",
			want:    []PaneProc{genuine},
			errHas:  []string{"%9", "777"},
		},
		{
			name:    "row whose pane_pid differs from tmux's own",
			owned:   "%1\t123\n",
			options: "%1\t999\t\t\t\n",
			want:    nil,
			errHas:  []string{"%1", "999"},
		},
		{
			name:    "pane id forged a second time with another pid",
			owned:   "%1\t123\n%2\t456\n",
			options: "%1\t123\t\t\t\n%2\t456\tx\n%1\t999\t" + launchID + "\tclaude-cenci-x\tdocker\n",
			want:    nil,
			errHas:  []string{"%1", "more than once"},
		},
		{
			name:    "genuine pane whose option row is missing",
			owned:   "%1\t123\n%2\t456\n",
			options: "%1\t123\t\t\t\n",
			want:    []PaneProc{genuine},
			errHas:  []string{"%2", "no matching option row"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			twoListTmux(t, filepath.Join(t.TempDir(), "args"), tt.owned, tt.options)

			got, err := ListWindowPaneProcs("=work:7")

			if err == nil {
				t.Fatal("ListWindowPaneProcs err = nil, want the dropped rows reported")
			}
			for _, sub := range tt.errHas {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("err = %q, want it to contain %q", err, sub)
				}
			}
			if len(got) != len(tt.want) {
				t.Fatalf("panes = %+v, want %+v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("pane[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestListWindowPaneProcs_NoPanesIsEmptyNotAnError(t *testing.T) {
	installFakeTmuxOnPath(t, "exit 0\n")

	got, err := ListWindowPaneProcs("=work:7")
	if err != nil {
		t.Fatalf("ListWindowPaneProcs err = %v, want nil for empty output", err)
	}
	if len(got) != 0 {
		t.Errorf("panes = %+v, want none", got)
	}
}
