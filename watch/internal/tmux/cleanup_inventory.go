package tmux

import (
	"fmt"
	"slices"
	"strings"
)

// The retained-window inventory (#1458): how the daemon's window-cleanup pass
// finds a window nothing tracks any more -- every pane dead, the session that
// launched it gone -- straight from tmux, and how it re-reads that window right
// before it acts on it.
//
// Like ListWindowPaneProcs and ListLivePanes it never trusts a user-writable
// field to say which rows exist. The window name and @cenci-repo-dir are free
// text a process in a container can set through the socket, and `-F` prints them
// raw, so a newline or tab inside one forges extra, well-formed rows. Two
// listings are therefore read: one of fields tmux itself owns (window_id,
// pane_id, pane_dead), which is the authority on which windows and panes exist
// and whether each pane is dead, and one of the user-writable fields keyed by
// window_id. A user-writable row is trusted only as the single, well-formed row
// of a window tmux itself lists. Only the trailing newline is trimmed and rows
// split on every TAB: an unset @cenci-repo-dir prints a trailing TAB that
// TrimSpace would swallow (watch/docs/tmux.md).

// MaxCleanupInventoryRows bounds each listing of one socket's inventory: more
// than this many rows is read as unreadable, never truncated, so a hostile or
// runaway server can neither stall a pass nor shrink the picture silently. The
// number is a safety ceiling, not a product limit.
const MaxCleanupInventoryRows = 1024

const (
	// cleanupOwnedFormat lists only tmux-owned fields, one row per pane.
	cleanupOwnedFormat = "#{window_id}\t#{pane_id}\t#{pane_dead}"
	// cleanupWindowFormat lists the user-writable fields, one row per window.
	cleanupWindowFormat = "#{window_id}\t#{window_name}\t#{@cenci-repo-dir}"
	// cleanupFields is the column count of both formats.
	cleanupFields = 3
)

// CleanupPane is one pane of a retained-window candidate. ID is "%<digits>" and
// Dead is tmux's #{pane_dead} == "1"; any other value never reaches this type.
type CleanupPane struct {
	ID   string
	Dead bool
}

// CleanupWindow is one window as the inventory reads it. WindowID is tmux's
// "@<digits>" id, Name and RepoDir the raw user-writable values (RepoDir is empty
// for a window that never had @cenci-repo-dir set), and Panes the panes in
// listing order.
type CleanupWindow struct {
	WindowID string
	Name     string
	RepoDir  string
	Panes    []CleanupPane
}

// AllDead reports whether the window has at least one pane and every pane is dead.
func (w CleanupWindow) AllDead() bool {
	return len(w.Panes) > 0 && !slices.ContainsFunc(w.Panes, func(p CleanupPane) bool { return !p.Dead })
}

// LowestPane returns the pane id with the lowest number ("%9" before "%10",
// which string order would reverse), or "" for a window with no panes. It is the
// stable kill target of a retained window: a pane id survives index renumbering
// and reuse.
func (w CleanupWindow) LowestPane() string {
	lowest := ""
	for _, p := range w.Panes {
		if lowest == "" || paneNumberLess(p.ID, lowest) {
			lowest = p.ID
		}
	}
	return lowest
}

// paneNumberLess compares two "%<digits>" ids by value without parsing them, so
// an id too long for any integer type still orders correctly.
func paneNumberLess(a, b string) bool {
	a, b = strings.TrimLeft(strings.TrimPrefix(a, "%"), "0"), strings.TrimLeft(strings.TrimPrefix(b, "%"), "0")
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// CleanupInventory is one socket's retained-window inventory: the windows whose
// rows all checked out, and one line per row or window that was dropped (and so
// kept open) with the reason, for the caller to log.
type CleanupInventory struct {
	Windows []CleanupWindow
	Dropped []string
}

// ListCleanupInventory reads the windows of the tmux server at socket (empty:
// the ambient server) from two listings joined on window_id (see the package
// comment above). A window is dropped, reported in Dropped and so never reaches a
// caller if any of these hold: its pane_dead is not exactly 0 or 1; a pane id or
// window id is malformed, or a pane id is listed twice; its user-writable row is
// missing, malformed or listed more than once; or a user-writable row names a
// window tmux does not list. Its siblings are unaffected.
//
// A tmux-owned row that cannot be tied to a window_id, or more than
// MaxCleanupInventoryRows rows in either listing, makes the whole socket
// unreadable: an error and no windows. tmux reporting no server returns
// ErrNoServer (the caller's "nothing is running"); every other failure, a
// missing binary and a timeout included, is a plain error that must be read as
// "unreadable".
func ListCleanupInventory(socket string) (CleanupInventory, error) {
	ownedOut, err := windowCmd(socket, "list-panes", "-a", "-F", cleanupOwnedFormat)
	if err != nil {
		return CleanupInventory{}, err
	}
	owned, err := parseOwnedRows(ownedOut)
	if err != nil {
		return CleanupInventory{}, err
	}
	if len(owned) == 0 {
		return CleanupInventory{}, nil
	}
	userOut, err := windowCmd(socket, "list-windows", "-a", "-F", cleanupWindowFormat)
	if err != nil {
		return CleanupInventory{}, err
	}
	user, err := parseWindowRows(userOut)
	if err != nil {
		return CleanupInventory{}, err
	}

	var inv CleanupInventory
	known := map[string]bool{}
	for _, w := range owned {
		known[w.id] = true
		if w.bad != "" {
			inv.Dropped = append(inv.Dropped, fmt.Sprintf("window %q: %s", w.id, w.bad))
			continue
		}
		row, why := user.row(w.id)
		if why != "" {
			inv.Dropped = append(inv.Dropped, fmt.Sprintf("window %q: %s", w.id, why))
			continue
		}
		inv.Windows = append(inv.Windows, CleanupWindow{WindowID: w.id, Name: row.name, RepoDir: row.repoDir, Panes: w.panes})
	}
	for _, id := range user.order {
		if !known[id] {
			inv.Dropped = append(inv.Dropped, fmt.Sprintf("window listing row for %q: tmux does not list that window", id))
		}
	}
	for _, line := range user.stray {
		inv.Dropped = append(inv.Dropped, fmt.Sprintf("window listing: unexpected line %q: want a window_id, a window name and a repo dir", line))
	}
	return inv, nil
}

// ReadCleanupWindow re-reads the one window that contains pane (a "%<digits>"
// id, never an index target) on the server at socket, for the caller to compare
// with what the inventory showed: the panes with their tmux-owned liveness come
// from `list-panes -t <pane>`, and the name and repo dir from the same
// server-wide window listing the inventory uses, taking the one row for that
// window_id. `display-message -p` is deliberately not used: it exits 0 with empty
// output on a missing target (watch/docs/tmux.md).
//
// A pane that no longer exists wraps ErrWindowGone, with nothing else read. Every
// other problem is a plain error that must be read as "unreadable": a malformed
// or duplicated pane row, a pane_dead outside 0/1, a listing that spans two
// windows or does not contain pane, a user-writable row that is missing, malformed
// or repeated for the window, an over-cap listing, a timeout.
func ReadCleanupWindow(socket, pane string) (CleanupWindow, error) {
	if !isPaneID(pane) {
		return CleanupWindow{}, fmt.Errorf("tmux: %q is not a pane id", pane)
	}
	ownedOut, err := windowCmd(socket, "list-panes", "-t", pane, "-F", cleanupOwnedFormat)
	if err != nil {
		return CleanupWindow{}, err
	}
	owned, err := parseOwnedRows(ownedOut)
	if err != nil {
		return CleanupWindow{}, err
	}
	switch {
	case len(owned) == 0:
		return CleanupWindow{}, fmt.Errorf("tmux list-panes: pane %q listed no panes", pane)
	case len(owned) > 1:
		return CleanupWindow{}, fmt.Errorf("tmux list-panes: pane %q listed panes of more than one window", pane)
	}
	w := owned[0]
	if w.bad != "" {
		return CleanupWindow{}, fmt.Errorf("tmux list-panes: window %q: %s", w.id, w.bad)
	}
	if !slices.ContainsFunc(w.panes, func(p CleanupPane) bool { return p.ID == pane }) {
		return CleanupWindow{}, fmt.Errorf("tmux list-panes: window %q does not list pane %q", w.id, pane)
	}

	userOut, err := windowCmd(socket, "list-windows", "-a", "-F", cleanupWindowFormat)
	if err != nil {
		return CleanupWindow{}, err
	}
	user, err := parseWindowRows(userOut)
	if err != nil {
		return CleanupWindow{}, err
	}
	row, why := user.row(w.id)
	if why != "" {
		return CleanupWindow{}, fmt.Errorf("tmux list-windows: window %q: %s", w.id, why)
	}
	return CleanupWindow{WindowID: w.id, Name: row.name, RepoDir: row.repoDir, Panes: w.panes}, nil
}

// ownedWindow is one window's panes as the tmux-owned listing reports them. A
// non-empty bad says why the window cannot be trusted; it is dropped, not the
// socket.
type ownedWindow struct {
	id    string
	panes []CleanupPane
	bad   string
}

// parseOwnedRows reads the tmux-owned `<window_id> <pane_id> <pane_dead>` rows
// into windows in first-appearance order. A row that is not three columns with a
// "@<digits>" first one cannot be tied to a window, so it is an error for the
// whole read, as is a listing over MaxCleanupInventoryRows. A row that does name a
// window but carries a malformed pane id or a pane_dead other than exactly "0" or
// "1", or repeats a pane id, marks that window bad instead.
func parseOwnedRows(out string) ([]*ownedWindow, error) {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return nil, nil
	}
	lines := strings.Split(out, "\n")
	if len(lines) > MaxCleanupInventoryRows {
		return nil, fmt.Errorf("tmux list-panes: %d rows, more than the %d allowed", len(lines), MaxCleanupInventoryRows)
	}
	var windows []*ownedWindow
	byID := map[string]*ownedWindow{}
	paneOwner := map[string]*ownedWindow{}
	for _, line := range lines {
		f := strings.Split(line, "\t")
		if len(f) != cleanupFields || !isWindowID(f[0]) {
			return nil, fmt.Errorf("tmux list-panes: unexpected line %q: want window_id, pane_id and pane_dead", line)
		}
		w := byID[f[0]]
		if w == nil {
			w = &ownedWindow{id: f[0]}
			byID[f[0]] = w
			windows = append(windows, w)
		}
		paneID, dead := f[1], f[2]
		switch {
		case !isPaneID(paneID):
			w.bad = fmt.Sprintf("pane id %q is not %%<digits>", paneID)
			continue
		case dead != "0" && dead != "1":
			w.bad = fmt.Sprintf("pane_dead %q of pane %q is not 0 or 1", dead, paneID)
			continue
		}
		if owner := paneOwner[paneID]; owner != nil {
			w.bad = fmt.Sprintf("pane %q is listed more than once", paneID)
			owner.bad = w.bad
			continue
		}
		paneOwner[paneID] = w
		w.panes = append(w.panes, CleanupPane{ID: paneID, Dead: dead == "1"})
	}
	return windows, nil
}

// windowRow is one user-writable window row.
type windowRow struct {
	name, repoDir string
}

// windowRows is the user-writable listing sorted by what it can be attributed
// to: rows are the well-formed three-column rows by window_id, bad the lines
// whose first column is a window id but that are not well formed, and stray every
// other line. order lists the attributable window ids in first-appearance order.
type windowRows struct {
	rows  map[string][]windowRow
	bad   map[string][]string
	order []string
	stray []string
}

// row returns the one well-formed row of window id, or why there is none: a
// malformed row attributed to it, more than one row, or no row at all.
func (w windowRows) row(id string) (windowRow, string) {
	switch {
	case len(w.bad[id]) > 0:
		return windowRow{}, fmt.Sprintf("malformed row %q in the window listing", w.bad[id][0])
	case len(w.rows[id]) > 1:
		return windowRow{}, "listed more than once in the window listing"
	case len(w.rows[id]) == 0:
		return windowRow{}, "no row in the window listing"
	}
	return w.rows[id][0], ""
}

// parseWindowRows reads the user-writable `<window_id> <name> <repo dir>` rows.
// Nothing here is an error but a listing over MaxCleanupInventoryRows: a
// malformed row only costs its own window, because a name or repo dir with a
// newline or tab breaks its own row, never another window's.
func parseWindowRows(out string) (windowRows, error) {
	w := windowRows{rows: map[string][]windowRow{}, bad: map[string][]string{}}
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return w, nil
	}
	lines := strings.Split(out, "\n")
	if len(lines) > MaxCleanupInventoryRows {
		return windowRows{}, fmt.Errorf("tmux list-windows: %d rows, more than the %d allowed", len(lines), MaxCleanupInventoryRows)
	}
	seen := map[string]bool{}
	note := func(id string) {
		if !seen[id] {
			seen[id] = true
			w.order = append(w.order, id)
		}
	}
	for _, line := range lines {
		f := strings.Split(line, "\t")
		switch {
		case !isWindowID(f[0]):
			w.stray = append(w.stray, line)
		case len(f) != cleanupFields:
			note(f[0])
			w.bad[f[0]] = append(w.bad[f[0]], line)
		default:
			note(f[0])
			w.rows[f[0]] = append(w.rows[f[0]], windowRow{name: f[1], repoDir: f[2]})
		}
	}
	return w, nil
}

// isWindowID reports whether s is a tmux window id: "@" and one or more ASCII
// digits.
func isWindowID(s string) bool { return isTmuxID(s, "@") }
