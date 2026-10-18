package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

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
var goneMessages = []string{"can't find window: 7", "can't find session: work", "can't find pane: %7"}

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

// -- ListLivePanes (#1266) ---------------------------------------------------

// liveTmux installs a fake tmux that serves ListLivePanes and logs each argv
// line to argsFile. A listing is told apart by the format it asks for (the one
// naming window_name or the one naming pane_current_path), so the tests do not
// pin the exact format strings. The n-th window-name listing answers
// windows[n-1] and the n-th path listing paths[n-1]; the last answer repeats.
func liveTmux(t *testing.T, argsFile string, windows, paths []string) {
	t.Helper()
	dir := t.TempDir()
	for kind, answers := range map[string][]string{"w": windows, "p": paths} {
		for i, a := range answers {
			if err := os.WriteFile(filepath.Join(dir, kind+"."+strconv.Itoa(i+1)), []byte(a), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, kind+".last"), []byte(answers[len(answers)-1]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d, log := exectest.ShellQuote(dir), exectest.ShellQuote(argsFile)
	installFakeTmuxOnPath(t, "printf '%s\\n' \"$*\" >> "+log+"\n"+
		"kind=w; case \"$*\" in *pane_current_path*) kind=p;; esac\n"+
		"n=$(cat "+d+"/$kind.n 2>/dev/null || echo 0); n=$((n+1)); echo $n > "+d+"/$kind.n\n"+
		"f="+d+"/$kind.$n; [ -f \"$f\" ] || f="+d+"/$kind.last\n"+
		"cat \"$f\"\n")
}

func sortedPanes(p []LivePane) []LivePane {
	out := append([]LivePane(nil), p...)
	sort.Slice(out, func(i, j int) bool { return out[i].PaneID < out[j].PaneID })
	return out
}

// A pane's window name and cwd come from two listings joined on the pane id, in
// whatever order each listing prints its rows.
func TestListLivePanes_JoinsWindowNamesAndCwdsOnPaneID(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	liveTmux(t, argsFile,
		[]string{"%1\t42-implement\n%2\tscratch pad\n"},
		[]string{"%2\t/home/dev\n%1\t/repo/.worktrees/42-x\n"})

	got, err := ListLivePanes()
	if err != nil {
		t.Fatalf("ListLivePanes: %v", err)
	}

	want := []LivePane{
		{PaneID: "%1", WindowName: "42-implement", CurrentPath: "/repo/.worktrees/42-x"},
		{PaneID: "%2", WindowName: "scratch pad", CurrentPath: "/home/dev"},
	}
	if !slices.Equal(sortedPanes(got), want) {
		t.Errorf("panes = %+v, want %+v", got, want)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("fake tmux was not invoked: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(args)), "\n") {
		if !strings.HasPrefix(line, "list-panes -a ") {
			t.Errorf("tmux argv = %q, want a server-wide `list-panes -a` call", line)
		}
	}
}

// Only the trailing newline is trimmed and a row splits on its first TAB: an
// empty cwd keeps its row (TrimSpace would swallow the trailing TAB and break
// it), and a TAB inside the value belongs to the value (watch/docs/tmux.md).
func TestListLivePanes_KeepsEmptyAndTabbedValues(t *testing.T) {
	liveTmux(t, filepath.Join(t.TempDir(), "args"),
		[]string{"%1\t42-implement\n%2\t\n%3\tx\n"},
		[]string{"%1\t\n%2\t/tmp/a\tb\n%3\t/ok\n"})

	got, err := ListLivePanes()
	if err != nil {
		t.Fatalf("ListLivePanes: %v", err)
	}

	want := []LivePane{
		{PaneID: "%1", WindowName: "42-implement", CurrentPath: ""},
		{PaneID: "%2", WindowName: "", CurrentPath: "/tmp/a\tb"},
		{PaneID: "%3", WindowName: "x", CurrentPath: "/ok"},
	}
	if !slices.Equal(sortedPanes(got), want) {
		t.Errorf("panes = %+v, want %+v", got, want)
	}
}

func TestListLivePanes_NoPanesIsEmptyNotAnError(t *testing.T) {
	liveTmux(t, filepath.Join(t.TempDir(), "args"), []string{""}, []string{""})

	got, err := ListLivePanes()
	if err != nil || len(got) != 0 {
		t.Errorf("ListLivePanes = %+v, %v; want no panes and no error", got, err)
	}
}

func TestListLivePanes_NoServerIsErrNoServer(t *testing.T) {
	failingTmux(t, "no server running on /tmp/tmux-1000/default")

	got, err := ListLivePanes()

	if !errors.Is(err, ErrNoServer) {
		t.Fatalf("ListLivePanes err = %v, want errors.Is(err, ErrNoServer)", err)
	}
	if len(got) != 0 {
		t.Errorf("panes = %+v, want none", got)
	}
}

// Regression for the narrow match: only "no server running" is benign. Every
// other tmux failure, "error connecting to" (a socket that is missing or
// refused) included, stays a plain error so a real problem is never read as
// "no windows".
func TestListLivePanes_OtherFailuresStayPlainErrors(t *testing.T) {
	for _, msg := range []string{
		"error connecting to /tmp/tmux-1000/default (No such file or directory)",
		"protocol version mismatch (client 8, server 7)",
		"permission denied",
		"can't find pane: %7",
	} {
		t.Run(msg, func(t *testing.T) {
			failingTmux(t, msg)

			got, err := ListLivePanes()

			if err == nil {
				t.Fatal("ListLivePanes err = nil, want the tmux failure")
			}
			if errors.Is(err, ErrNoServer) {
				t.Errorf("ListLivePanes err = %v, must not be classified as no server", err)
			}
			if !strings.Contains(err.Error(), msg) {
				t.Errorf("ListLivePanes err = %q, want tmux's message %q in it", err, msg)
			}
			if len(got) != 0 {
				t.Errorf("panes = %+v, want none alongside an error", got)
			}
		})
	}
}

// A row tmux did not produce for a real pane, or two listings that disagree on
// which panes exist, make the whole read fail: the caller treats the live state
// as unreadable rather than trusting a partial or forged picture.
func TestListLivePanes_MalformedForgedOrMismatchedListingsFail(t *testing.T) {
	tests := []struct {
		name           string
		windows, paths string
	}{
		{"first column is not a pane id", "foo\t42-x\n", "foo\t/tmp\n"},
		{"bare percent sign", "%\t42-x\n", "%\t/tmp\n"},
		{"pane id with trailing text", "%12abc\t42-x\n", "%12abc\t/tmp\n"},
		{"non-ASCII digits", "%١٢\t42-x\n", "%١٢\t/tmp\n"},
		{"row without a TAB", "%1\n", "%1\t/tmp\n"},
		{"pane id listed twice", "%1\ta\n%1\tb\n", "%1\t/p\n"},
		{"window name forges an extra row", "%1\tx\n%9\t42-fake\n", "%1\t/p\n"},
		{"pane missing from the path listing", "%1\ta\n%2\tb\n", "%1\t/p\n"},
		{"pane missing from the window listing", "%1\ta\n", "%1\t/p\n%2\t/q\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			liveTmux(t, filepath.Join(t.TempDir(), "args"), []string{tt.windows}, []string{tt.paths})

			got, err := ListLivePanes()

			if err == nil {
				t.Fatalf("ListLivePanes = %+v, nil; want an error", got)
			}
			if errors.Is(err, ErrNoServer) {
				t.Errorf("err = %v, must not read as no server", err)
			}
			if len(got) != 0 {
				t.Errorf("panes = %+v, want none alongside an error", got)
			}
		})
	}
}

// A pane that opens or closes between the two listings makes their pane-id sets
// differ once; the read is retried and succeeds when the next pair agrees.
func TestListLivePanes_RetriesATransientSetMismatch(t *testing.T) {
	liveTmux(t, filepath.Join(t.TempDir(), "args"),
		[]string{"%1\ta\n%2\tb\n", "%1\ta\n%2\tb\n"},
		[]string{"%1\t/p\n", "%1\t/p\n%2\t/q\n"})

	got, err := ListLivePanes()
	if err != nil {
		t.Fatalf("ListLivePanes: %v", err)
	}

	want := []LivePane{
		{PaneID: "%1", WindowName: "a", CurrentPath: "/p"},
		{PaneID: "%2", WindowName: "b", CurrentPath: "/q"},
	}
	if !slices.Equal(sortedPanes(got), want) {
		t.Errorf("panes = %+v, want %+v", got, want)
	}
}

// A tmux that cannot be run at all, or that hangs past the call bound, is an
// unreadable window list, never "no server" and never an empty one.
func TestListLivePanes_MissingBinaryAndTimeoutFailClosed(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T)
		wantInErr error
	}{
		{name: "tmux is not installed", setup: func(t *testing.T) { t.Setenv("PATH", t.TempDir()) }, wantInErr: exec.ErrNotFound},
		{name: "tmux hangs", wantInErr: context.DeadlineExceeded, setup: func(t *testing.T) {
			installFakeTmuxOnPath(t, "exec sleep 30\n")
			old := windowCmdTimeout
			windowCmdTimeout = 100 * time.Millisecond
			t.Cleanup(func() { windowCmdTimeout = old })
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)

			got, err := ListLivePanes()

			if err == nil {
				t.Fatalf("ListLivePanes = %+v, nil; want an error", got)
			}
			if errors.Is(err, ErrNoServer) {
				t.Errorf("err = %v, must not read as no server", err)
			}
			if !errors.Is(err, tt.wantInErr) {
				t.Errorf("err = %v, want it to wrap %v", err, tt.wantInErr)
			}
			if len(got) != 0 {
				t.Errorf("panes = %+v, want none alongside an error", got)
			}
		})
	}
}
