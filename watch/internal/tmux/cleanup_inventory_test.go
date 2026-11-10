package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

// Tests for the retained-window inventory (#1458). The parsers are the unit-test
// exception: a tmux-owned listing and a user-writable listing are joined and
// strictly validated, and the matrix of forged and malformed rows is far too
// large to drive through the daemon one window at a time.

// The two listing formats the readers must request, pinned here as the contract
// with tmux: the first holds only fields tmux owns, the second the user-writable
// window name and @cenci-repo-dir.
const (
	wantOwnedFormat  = "#{window_id}\t#{pane_id}\t#{pane_dead}"
	wantWindowFormat = "#{window_id}\t#{window_name}\t#{@cenci-repo-dir}"

	invSocket = "/tmp/tmux-1000/cenci"
	// controlOwned / controlUser are a healthy retained window @9 appended to a
	// failing case: the closing control that proves a bad sibling does not take a
	// good window down with it.
	controlOwned = "@9\t%9\t1\n"
	controlUser  = "@9\t9-implement\t/ok\n"
)

// inventoryTmux installs a fake tmux that logs every argv line (a leading
// `-S <socket>` kept) to argsFile and answers `list-panes` with owned and
// `list-windows` with user. The texts go through files, so a listing of a
// thousand rows stays a plain `cat`.
func inventoryTmux(t *testing.T, argsFile, owned, user string) {
	t.Helper()
	dir := t.TempDir()
	for name, text := range map[string]string{"owned": owned, "user": user} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d := exectest.ShellQuote(dir)
	installFakeTmuxOnPath(t, "printf '%s\\n' \"$*\" >> "+exectest.ShellQuote(argsFile)+"\n"+
		"if [ \"$1\" = -S ]; then shift 2; fi\n"+
		"case \"$1\" in\nlist-panes) cat "+d+"/owned ;;\nlist-windows) cat "+d+"/user ;;\nesac\n")
}

func windowIDs(inv CleanupInventory) []string {
	var ids []string
	for _, w := range inv.Windows {
		ids = append(ids, w.WindowID)
	}
	return ids
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fake tmux was not invoked: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func TestListCleanupInventory_JoinsTheTwoListingsOnWindowID(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	inventoryTmux(t, argsFile,
		"@1\t%1\t1\n@1\t%2\t1\n@2\t%3\t0\n",
		"@2\t7-refine\t/other\n@1\t42-implement\t/repo\n")

	inv, err := ListCleanupInventory("")
	if err != nil {
		t.Fatalf("ListCleanupInventory: %v", err)
	}

	want := []CleanupWindow{
		{WindowID: "@1", Name: "42-implement", RepoDir: "/repo", Panes: []CleanupPane{{ID: "%1", Dead: true}, {ID: "%2", Dead: true}}},
		{WindowID: "@2", Name: "7-refine", RepoDir: "/other", Panes: []CleanupPane{{ID: "%3", Dead: false}}},
	}
	if !reflect.DeepEqual(inv.Windows, want) {
		t.Errorf("windows = %+v, want %+v", inv.Windows, want)
	}
	if len(inv.Dropped) != 0 {
		t.Errorf("dropped = %v, want none for a clean listing", inv.Dropped)
	}
	wantArgs := []string{"list-panes -a -F " + wantOwnedFormat, "list-windows -a -F " + wantWindowFormat}
	if got := readLines(t, argsFile); !slices.Equal(got, wantArgs) {
		t.Errorf("tmux argv = %q, want %q", got, wantArgs)
	}
}

// Only the trailing newline is trimmed and rows split on TAB: an unset
// @cenci-repo-dir prints a trailing TAB, an empty name an empty middle column
// (TrimSpace would swallow the first and shift the second).
func TestListCleanupInventory_KeepsEmptyAndSpacedColumns(t *testing.T) {
	inventoryTmux(t, filepath.Join(t.TempDir(), "args"),
		"@1\t%1\t1\n@2\t%2\t1\n@3\t%3\t1\n",
		"@1\t42-implement\t\n@2\t\t/repo\n@3\tmy name\t/a b\n")

	inv, err := ListCleanupInventory("")
	if err != nil {
		t.Fatalf("ListCleanupInventory: %v", err)
	}

	got := map[string][2]string{}
	for _, w := range inv.Windows {
		got[w.WindowID] = [2]string{w.Name, w.RepoDir}
	}
	want := map[string][2]string{"@1": {"42-implement", ""}, "@2": {"", "/repo"}, "@3": {"my name", "/a b"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("name and repo dir columns = %q, want %q", got, want)
	}
}

// Every row that cannot be trusted drops its window and is reported; a healthy
// control window in the same listing still comes back.
func TestListCleanupInventory_UntrustworthyRowsDropTheirWindowAndAreReported(t *testing.T) {
	tests := []struct {
		name     string
		owned    string
		user     string
		wantIDs  []string
		wantDrop []string // substrings the report must contain
	}{
		{
			name:     "window_id listed twice in the user-writable listing",
			owned:    "@1\t%1\t1\n",
			user:     "@1\t42-implement\t/repo\n@1\t42-implement\t/repo\n",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"@1", "more than once"},
		},
		{
			name:     "user-writable row for a window tmux does not list",
			owned:    "@1\t%1\t1\n",
			user:     "@1\t42-implement\t/repo\n@5\t43-implement\t/repo\n",
			wantIDs:  []string{"@1", "@9"},
			wantDrop: []string{"@5"},
		},
		{
			name:     "window missing from the user-writable listing",
			owned:    "@1\t%1\t1\n",
			user:     "",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"@1"},
		},
		{
			name:     "pane_dead is 2",
			owned:    "@1\t%1\t2\n",
			user:     "@1\t42-implement\t/repo\n",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"@1", "pane_dead"},
		},
		{
			name:     "pane_dead is empty",
			owned:    "@1\t%1\t\n",
			user:     "@1\t42-implement\t/repo\n",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"@1", "pane_dead"},
		},
		{
			name:     "pane_dead is zero padded",
			owned:    "@1\t%1\t01\n",
			user:     "@1\t42-implement\t/repo\n",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"@1", "pane_dead"},
		},
		{
			name:     "pane_dead is text",
			owned:    "@1\t%1\tdead\n",
			user:     "@1\t42-implement\t/repo\n",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"@1", "pane_dead"},
		},
		{
			name:     "a live pane does not hide a second pane with a bad pane_dead",
			owned:    "@1\t%1\t1\n@1\t%2\t7\n",
			user:     "@1\t42-implement\t/repo\n",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"@1", "pane_dead"},
		},
		{
			name:     "pane id is not %digits",
			owned:    "@1\t%x\t1\n",
			user:     "@1\t42-implement\t/repo\n",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"@1", "pane"},
		},
		{
			name:     "pane id is empty",
			owned:    "@1\t\t1\n",
			user:     "@1\t42-implement\t/repo\n",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"@1", "pane"},
		},
		{
			name:     "pane id with a shell metacharacter",
			owned:    "@1\t%1;id\t1\n",
			user:     "@1\t42-implement\t/repo\n",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"@1", "pane"},
		},
		{
			name:     "the same pane id in two windows",
			owned:    "@1\t%1\t1\n@2\t%1\t1\n",
			user:     "@1\t42-implement\t/repo\n@2\t43-implement\t/repo\n",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"%1"},
		},
		{
			name:     "user-writable row with a name containing a TAB",
			owned:    "@1\t%1\t1\n",
			user:     "@1\ta\tb\t/repo\n",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"@1"},
		},
		{
			name:     "user-writable row with a missing column",
			owned:    "@1\t%1\t1\n",
			user:     "@1\t42-implement\n",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"@1"},
		},
		{
			name:     "user-writable row whose window id is malformed",
			owned:    "@1\t%1\t1\n",
			user:     "@x\t42-implement\t/repo\n",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"@1"},
		},
		{
			// A stray line is only ever the tail of a name that itself broke a real
			// row, so it never costs a window that kept a well-formed row of its own.
			name:     "stray line that no window id owns",
			owned:    "@1\t%1\t1\n",
			user:     "@1\t42-implement\t/repo\ngarbage\n",
			wantIDs:  []string{"@1", "@9"},
			wantDrop: []string{"garbage"},
		},
		{
			// @1's name is "x\n@2\t42-fake" and its repo dir "/repo": tmux prints a
			// truncated @1 row and a forged @2 row next to @2's genuine one.
			name:     "window name that forges a row for another real window",
			owned:    "@1\t%1\t1\n@2\t%2\t1\n",
			user:     "@1\tx\n@2\t42-fake\t/repo\n@2\t43-implement\t/repo2\n",
			wantIDs:  []string{"@9"},
			wantDrop: []string{"@1", "@2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inventoryTmux(t, filepath.Join(t.TempDir(), "args"), tt.owned+controlOwned, tt.user+controlUser)

			inv, err := ListCleanupInventory("")

			if err != nil {
				t.Fatalf("ListCleanupInventory err = %v, want the bad rows reported, not the whole read failed", err)
			}
			if got := windowIDs(inv); !slices.Equal(got, tt.wantIDs) {
				t.Errorf("windows = %v, want %v", got, tt.wantIDs)
			}
			report := strings.Join(inv.Dropped, "\n")
			for _, sub := range tt.wantDrop {
				if !strings.Contains(report, sub) {
					t.Errorf("dropped report = %q, want it to mention %q", report, sub)
				}
			}
		})
	}
}

// A tmux-owned row that cannot be tied to a window id means tmux did not print
// what this reader asked for, so no window of that socket can be trusted: the
// whole read is unreadable.
func TestListCleanupInventory_AnUnattributableOwnedRowMakesTheSocketUnreadable(t *testing.T) {
	for name, row := range map[string]string{
		"free text":                  "garbage\n",
		"missing the pane_dead":      "@1\t%1\n",
		"extra column":               "@1\t%1\t1\textra\n",
		"columns in the wrong order": "%1\t@1\t1\n",
		"window id not @digits":      "@x\t%1\t1\n",
		"empty window id":            "\t%1\t1\n",
		"bare at sign":               "@\t%1\t1\n",
	} {
		t.Run(name, func(t *testing.T) {
			inventoryTmux(t, filepath.Join(t.TempDir(), "args"), row+controlOwned, "@1\t42-implement\t/repo\n"+controlUser)

			inv, err := ListCleanupInventory("")

			if err == nil {
				t.Fatalf("ListCleanupInventory = %+v, nil; want an error", inv)
			}
			if errors.Is(err, ErrNoServer) {
				t.Errorf("err = %v, must not read as no server", err)
			}
			if len(inv.Windows) != 0 {
				t.Errorf("windows = %v, want none from an unreadable socket", windowIDs(inv))
			}
		})
	}
}

// inventoryOfRows builds an n-window listing, one pane per window.
func inventoryOfRows(n int) (owned, user string) {
	var o, u strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&o, "@%d\t%%%d\t1\n", i, i)
		fmt.Fprintf(&u, "@%d\t%d-implement\t/repo\n", i, i)
	}
	return o.String(), u.String()
}

func TestListCleanupInventory_RowCapIsAnInclusiveCeiling(t *testing.T) {
	t.Run("at the cap the inventory is read", func(t *testing.T) {
		owned, user := inventoryOfRows(MaxCleanupInventoryRows)
		inventoryTmux(t, filepath.Join(t.TempDir(), "args"), owned, user)

		inv, err := ListCleanupInventory("")

		if err != nil || len(inv.Windows) != MaxCleanupInventoryRows {
			t.Fatalf("ListCleanupInventory = %d windows, %v; want all %d", len(inv.Windows), err, MaxCleanupInventoryRows)
		}
	})
	t.Run("one pane row over the cap is unreadable", func(t *testing.T) {
		owned, user := inventoryOfRows(MaxCleanupInventoryRows + 1)
		inventoryTmux(t, filepath.Join(t.TempDir(), "args"), owned, user)

		inv, err := ListCleanupInventory("")

		if err == nil {
			t.Fatalf("ListCleanupInventory = %d windows, nil; want an error over the cap", len(inv.Windows))
		}
		if len(inv.Windows) != 0 {
			t.Errorf("windows = %d, want none from an over-cap socket", len(inv.Windows))
		}
	})
	t.Run("an over-cap user-writable listing is unreadable too", func(t *testing.T) {
		_, user := inventoryOfRows(MaxCleanupInventoryRows + 1)
		inventoryTmux(t, filepath.Join(t.TempDir(), "args"), "@1\t%1\t1\n", user)

		inv, err := ListCleanupInventory("")

		if err == nil {
			t.Fatalf("ListCleanupInventory = %+v, nil; want an error over the cap", inv)
		}
	})
}

// "no server running" is the one benign failure; the caller turns it into zero
// windows. Everything else, a missing binary and a timeout included, is
// unreadable and must never read as "no server".
func TestListCleanupInventory_NoServerIsErrNoServerOnEverySocket(t *testing.T) {
	for _, socket := range []string{"", invSocket} {
		t.Run(fmt.Sprintf("socket %q", socket), func(t *testing.T) {
			failingTmux(t, "no server running on /tmp/tmux-1000/default")

			inv, err := ListCleanupInventory(socket)

			if !errors.Is(err, ErrNoServer) {
				t.Fatalf("err = %v, want errors.Is(err, ErrNoServer)", err)
			}
			if len(inv.Windows) != 0 {
				t.Errorf("windows = %v, want none", windowIDs(inv))
			}
		})
	}
}

func TestListCleanupInventory_OtherFailuresStayPlainErrors(t *testing.T) {
	for _, msg := range []string{
		"error connecting to /tmp/tmux-1000/default (No such file or directory)",
		"protocol version mismatch (client 8, server 7)",
		"permission denied",
	} {
		t.Run(msg, func(t *testing.T) {
			failingTmux(t, msg)

			inv, err := ListCleanupInventory("")

			if err == nil {
				t.Fatal("err = nil, want the tmux failure")
			}
			if errors.Is(err, ErrNoServer) {
				t.Errorf("err = %v, must not be classified as no server", err)
			}
			if !strings.Contains(err.Error(), msg) {
				t.Errorf("err = %q, want tmux's message %q in it", err, msg)
			}
			if len(inv.Windows) != 0 {
				t.Errorf("windows = %v, want none alongside an error", windowIDs(inv))
			}
		})
	}
}

// A failure of the second listing alone is as unreadable as the first's.
func TestListCleanupInventory_SecondListingFailureIsUnreadable(t *testing.T) {
	installFakeTmuxOnPath(t, "case \"$1\" in\nlist-panes) printf '@1\\t%%1\\t1\\n' ;;\n"+
		"list-windows) printf '%s\\n' 'protocol version mismatch (client 8, server 7)' >&2; exit 1 ;;\nesac\n")

	inv, err := ListCleanupInventory("")

	if err == nil || len(inv.Windows) != 0 {
		t.Fatalf("ListCleanupInventory = %+v, %v; want an error and no windows", inv, err)
	}
}

func TestListCleanupInventory_MissingBinaryAndTimeoutFailClosed(t *testing.T) {
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
		for _, socket := range []string{"", invSocket} {
			t.Run(fmt.Sprintf("%s on socket %q", tt.name, socket), func(t *testing.T) {
				tt.setup(t)

				inv, err := ListCleanupInventory(socket)

				if err == nil {
					t.Fatalf("ListCleanupInventory = %+v, nil; want an error", inv)
				}
				if errors.Is(err, ErrNoServer) {
					t.Errorf("err = %v, must not read as no server", err)
				}
				if !errors.Is(err, tt.wantInErr) {
					t.Errorf("err = %v, want it to wrap %v", err, tt.wantInErr)
				}
				if len(inv.Windows) != 0 {
					t.Errorf("windows = %v, want none alongside an error", windowIDs(inv))
				}
			})
		}
	}
}

// Both listings go to the socket they were asked for: `-S <socket>` leads each
// call of a recorded socket, and the empty socket is bare tmux.
func TestListCleanupInventory_BothListingsCarryTheSocketOrNone(t *testing.T) {
	for _, socket := range []string{invSocket, ""} {
		t.Run(fmt.Sprintf("socket %q", socket), func(t *testing.T) {
			argsFile := filepath.Join(t.TempDir(), "args")
			inventoryTmux(t, argsFile, "@1\t%1\t1\n", "@1\t42-implement\t/repo\n")

			if _, err := ListCleanupInventory(socket); err != nil {
				t.Fatalf("ListCleanupInventory: %v", err)
			}

			prefix := ""
			if socket != "" {
				prefix = "-S " + socket + " "
			}
			want := []string{prefix + "list-panes -a -F " + wantOwnedFormat, prefix + "list-windows -a -F " + wantWindowFormat}
			if got := readLines(t, argsFile); !slices.Equal(got, want) {
				t.Errorf("tmux argv = %q, want %q", got, want)
			}
		})
	}
}

// A malformed recorded socket fails closed before anything is exec'd.
func TestListCleanupInventory_MalformedSocketFailsWithoutExec(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	inventoryTmux(t, argsFile, "@1\t%1\t1\n", "@1\t42-implement\t/repo\n")

	_, err := ListCleanupInventory("relative/sock")

	if !errors.Is(err, ErrMalformedSocket) {
		t.Fatalf("err = %v, want one wrapping ErrMalformedSocket", err)
	}
	if _, statErr := os.Stat(argsFile); !os.IsNotExist(statErr) {
		t.Errorf("tmux was invoked for a malformed socket (stat err %v)", statErr)
	}
}

func TestCleanupWindow_AllDeadAndLowestPaneByNumber(t *testing.T) {
	w := CleanupWindow{Panes: []CleanupPane{{ID: "%10", Dead: true}, {ID: "%9", Dead: true}, {ID: "%100", Dead: true}}}
	if !w.AllDead() {
		t.Error("AllDead = false, want true when every pane is dead")
	}
	if got := w.LowestPane(); got != "%9" {
		t.Errorf("LowestPane = %q, want %%9 (numeric, not string, order)", got)
	}

	w.Panes[1].Dead = false
	if w.AllDead() {
		t.Error("AllDead = true, want false once one pane is live")
	}
	if got := (CleanupWindow{}).LowestPane(); got != "" {
		t.Errorf("LowestPane of no panes = %q, want empty", got)
	}
	if (CleanupWindow{}).AllDead() {
		t.Error("AllDead of a window with no panes = true, want false")
	}
}

// -- revalidation (ReadCleanupWindow) ----------------------------------------

func TestReadCleanupWindow_ReadsOneWindowByItsPaneID(t *testing.T) {
	for _, socket := range []string{"", invSocket} {
		t.Run(fmt.Sprintf("socket %q", socket), func(t *testing.T) {
			argsFile := filepath.Join(t.TempDir(), "args")
			// The user-writable listing is server-wide; the other windows' rows,
			// stray text included, do not matter to the window that was asked for.
			inventoryTmux(t, argsFile,
				"@3\t%5\t1\n@3\t%7\t0\n",
				"@1\t42-implement\t/other\nstray\n@3\t7-refine\t/repo\n")

			got, err := ReadCleanupWindow(socket, "%5")
			if err != nil {
				t.Fatalf("ReadCleanupWindow: %v", err)
			}

			want := CleanupWindow{WindowID: "@3", Name: "7-refine", RepoDir: "/repo", Panes: []CleanupPane{{ID: "%5", Dead: true}, {ID: "%7", Dead: false}}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("window = %+v, want %+v", got, want)
			}
			prefix := ""
			if socket != "" {
				prefix = "-S " + socket + " "
			}
			wantArgs := []string{prefix + "list-panes -t %5 -F " + wantOwnedFormat, prefix + "list-windows -a -F " + wantWindowFormat}
			if gotArgs := readLines(t, argsFile); !slices.Equal(gotArgs, wantArgs) {
				t.Errorf("tmux argv = %q, want %q", gotArgs, wantArgs)
			}
		})
	}
}

// A pane that no longer exists is "already gone" and nothing else is read; the
// user-writable listing is never consulted for it.
func TestReadCleanupWindow_MissingPaneIsErrWindowGone(t *testing.T) {
	for _, msg := range goneMessages {
		t.Run(msg, func(t *testing.T) {
			argsFile := filepath.Join(t.TempDir(), "args")
			installFakeTmuxOnPath(t, "printf '%s\\n' \"$*\" >> "+exectest.ShellQuote(argsFile)+"\n"+
				"printf '%s\\n' "+exectest.ShellQuote(msg)+" >&2\nexit 1\n")

			got, err := ReadCleanupWindow("", "%7")

			if !errors.Is(err, ErrWindowGone) {
				t.Fatalf("err = %v, want errors.Is(err, ErrWindowGone)", err)
			}
			if got.WindowID != "" {
				t.Errorf("window = %+v, want the zero value for a gone window", got)
			}
			if lines := readLines(t, argsFile); len(lines) != 1 {
				t.Errorf("tmux calls = %q, want only the pane listing", lines)
			}
		})
	}
}

func TestReadCleanupWindow_UnrelatedFailuresAreNotErrWindowGone(t *testing.T) {
	for _, msg := range notGoneMessages {
		t.Run(msg, func(t *testing.T) {
			failingTmux(t, msg)

			_, err := ReadCleanupWindow("", "%7")

			if err == nil {
				t.Fatal("err = nil, want the tmux failure")
			}
			if errors.Is(err, ErrWindowGone) {
				t.Errorf("err = %v, must not be classified as a gone window", err)
			}
		})
	}
}

// Whatever cannot be read exactly is an error, never a gone window and never a
// best-effort window: the caller keeps it.
func TestReadCleanupWindow_UnreadableWindowIsAnErrorNotGone(t *testing.T) {
	const goodOwned = "@3\t%5\t1\n"
	const goodUser = "@3\t7-refine\t/repo\n"
	tests := []struct {
		name        string
		owned, user string
	}{
		{"no user-writable row for the window", goodOwned, "@1\t42-implement\t/other\n"},
		{"two user-writable rows for the window", goodOwned, goodUser + goodUser},
		{"a forged second row for the window", goodOwned, "@3\tx\n@3\t7-refine\t/repo\n"},
		{"a malformed row attributed to the window", goodOwned, goodUser + "@3\tonly-two\n"},
		{"pane_dead is neither 0 nor 1", "@3\t%5\t2\n", goodUser},
		{"pane_dead is empty", "@3\t%5\t\n", goodUser},
		{"the pane listing spans two windows", "@3\t%5\t1\n@4\t%6\t1\n", goodUser},
		{"an empty pane listing", "", goodUser},
		{"a malformed pane row", "@3\t%5\n", goodUser},
		{"a pane row with a bad pane id", "@3\t%x\t1\n", goodUser},
		{"a duplicated pane id", "@3\t%5\t1\n@3\t%5\t1\n", goodUser},
		{"a pane listing that omits the pane asked for", "@3\t%6\t1\n", goodUser},
		{"a pane row with a bad window id", "@x\t%5\t1\n", goodUser},
		{"an over-cap user-writable listing", goodOwned, func() string { _, u := inventoryOfRows(MaxCleanupInventoryRows + 1); return goodUser + u }()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inventoryTmux(t, filepath.Join(t.TempDir(), "args"), tt.owned, tt.user)

			got, err := ReadCleanupWindow("", "%5")

			if err == nil {
				t.Fatalf("ReadCleanupWindow = %+v, nil; want an error", got)
			}
			if errors.Is(err, ErrWindowGone) || errors.Is(err, ErrNoServer) {
				t.Errorf("err = %v, must be a plain unreadable error", err)
			}
			if got.WindowID != "" {
				t.Errorf("window = %+v, want the zero value alongside an error", got)
			}
		})
	}
}

func TestReadCleanupWindow_UserListingFailureIsUnreadable(t *testing.T) {
	installFakeTmuxOnPath(t, "case \"$1\" in\nlist-panes) printf '@3\\t%%5\\t1\\n' ;;\n"+
		"list-windows) printf '%s\\n' 'protocol version mismatch (client 8, server 7)' >&2; exit 1 ;;\nesac\n")

	got, err := ReadCleanupWindow("", "%5")

	if err == nil || errors.Is(err, ErrWindowGone) {
		t.Fatalf("ReadCleanupWindow = %+v, %v; want a plain error", got, err)
	}
}

// The pane id becomes a tmux -t target, so only %digits is ever exec'd.
func TestReadCleanupWindow_OnlyAPaneIDIsEverATarget(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	inventoryTmux(t, argsFile, "@3\t%5\t1\n", "@3\t7-refine\t/repo\n")

	for _, pane := range []string{"", "5", "%", "%x", "=main:0", "@3", "%5;id"} {
		if _, err := ReadCleanupWindow("", pane); err == nil {
			t.Errorf("ReadCleanupWindow(%q) = nil error, want a refusal", pane)
		}
	}
	if _, statErr := os.Stat(argsFile); !os.IsNotExist(statErr) {
		t.Errorf("tmux was invoked for a non-pane target (stat err %v)", statErr)
	}
	if _, err := ReadCleanupWindow("", "%5"); err != nil {
		t.Errorf("ReadCleanupWindow(%%5) = %v, want the control read to succeed", err)
	}
}

func TestReadCleanupWindow_TimeoutIsUnreadable(t *testing.T) {
	installFakeTmuxOnPath(t, "exec sleep 30\n")
	old := windowCmdTimeout
	windowCmdTimeout = 100 * time.Millisecond
	t.Cleanup(func() { windowCmdTimeout = old })

	_, err := ReadCleanupWindow("", "%5")

	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrWindowGone) {
		t.Fatalf("err = %v, want a deadline error that is not a gone window", err)
	}
}
