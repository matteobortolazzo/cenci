package capacity

import (
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Window is one tmux window of an inventory: its name, the repo directory
// recorded on it (empty when none was set), the ids of its panes and whether
// every pane is dead.
type Window struct {
	Name    string
	Dir     string
	PaneIDs []string
	AllDead bool
}

// SocketInventory is one tmux socket's inventory. Complete means the listing
// succeeded with no dropped or malformed rows (a socket with no server is a
// complete, empty inventory); anything else must be reported as incomplete,
// and an incomplete inventory proves nothing. ReadAt is the time just before
// the listing started: a window created before then is in the listing, so an
// entry that changed after ReadAt cannot be judged by it.
type SocketInventory struct {
	Complete bool
	ReadAt   time.Time
	Windows  []Window
}

// Inventory maps a tmux socket (the empty string is the ambient server) to its
// inventory. A socket that was not read has no key, and its entries are kept.
type Inventory map[string]SocketInventory

// Session is one agent session of the daemon snapshot. Only Running sessions
// count.
type Session struct {
	Socket  string
	PaneID  string
	Window  string
	Running bool
}

// Observation is everything an admission reads before it takes the ledger lock,
// so no subprocess or socket I/O runs inside it: the daemon's sessions, the
// orphaned container session count and the tmux inventory.
type Observation struct {
	Sessions  []Session
	Orphans   int
	Inventory Inventory
}

// Usage is the machine's used worker slots.
//
// Machine = Held + Unjoined + Orphans: the ledger's entries, plus running
// sessions that no entry accounts for (manual workers, or a launch the ledger
// does not know), plus orphaned container sessions. A ledger-held worker that
// also shows up as a running session counts once. Sessions in a non-running
// status and babysit supervisor processes count zero.
type Usage struct {
	Machine  int
	Held     int
	Unjoined int
	Orphans  int
	repos    map[string]int
}

// Repo is the ledger entries held for repo. Manual and daemon-running sessions
// count against the machine cap only.
func (u Usage) Repo(repo string) int { return u.repos[strings.ToLower(repo)] }

// CountUsage is the one counting rule shared by admission and
// `cenci dispatch status`.
func CountUsage(entries []Entry, obs Observation) Usage {
	u := Usage{Held: len(entries), Orphans: max(obs.Orphans, 0), repos: map[string]int{}}
	for _, e := range entries {
		u.repos[strings.ToLower(e.Repo)]++
	}
	for _, s := range obs.Sessions {
		if s.Running && !sessionJoinsEntry(s, entries, obs.Inventory) {
			u.Unjoined++
		}
	}
	u.Machine = u.Held + u.Unjoined + u.Orphans
	return u
}

// sessionJoinsEntry reports whether a running session is the worker of a ledger
// entry: the same socket and either the entry's window name or a pane id that
// the inventory places in the entry's window.
func sessionJoinsEntry(s Session, entries []Entry, inv Inventory) bool {
	for _, e := range entries {
		if e.Window == "" || e.Socket != s.Socket {
			continue
		}
		if s.Window != "" && s.Window == e.Window {
			return true
		}
		if s.PaneID == "" {
			continue
		}
		for _, w := range inv[e.Socket].Windows {
			if windowMatches(e, w) && slices.Contains(w.PaneIDs, s.PaneID) {
				return true
			}
		}
	}
	return false
}

// windowMatches reports whether w may be entry e's worker window: the same name
// and the same cleaned repo directory. A window or entry with no recorded
// directory matches on the name alone, which errs towards keeping the entry.
func windowMatches(e Entry, w Window) bool {
	if e.Window == "" || w.Name != e.Window {
		return false
	}
	return w.Dir == "" || e.Dir == "" || filepath.Clean(w.Dir) == filepath.Clean(e.Dir)
}
