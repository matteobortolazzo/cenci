package daemon

import (
	"sort"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/detect"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
)

func (d *Daemon) broadcast() {
	if d.ipc != nil {
		d.ipc.Broadcast(d.buildSnapshot())
	}
}

func (d *Daemon) buildSnapshot() ipc.StateSnapshot {
	snap := ipc.StateSnapshot{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Headroom:  d.headroom,
		Dispatch:  d.dispatch,
	}

	type entry struct {
		w        ipc.WindowState
		paneless bool
		sortKey  string
	}
	entries := make([]entry, 0, len(d.sessions))
	for key, sess := range d.sessions {
		w := ipc.WindowState{
			TaskName: sess.TaskName,
			Status:   sess.Status.String(),
			Agent:    sess.Agent,
		}
		e := entry{sortKey: key, paneless: true}
		if wi := d.frontend.WindowInfo(key); wi != nil {
			w.Session = wi.Session
			w.WindowIndex = wi.WindowIndex
			w.WindowName = wi.WindowName
			w.ManuallyNamed = wi.ManuallyNamed
			e.paneless = false
			e.sortKey = wi.Session + ":" + wi.WindowIndex
		} else {
			// Paneless (or window unknown): no tmux fields; the task name is
			// the only display name available.
			w.WindowName = sess.TaskName
		}
		e.w = w
		entries = append(entries, e)

		snap.Summary.Total++
		countStatus(&snap.Summary, sess.Status)
	}

	// tmux entries first, ordered by session:index; paneless entries after,
	// ordered by session key for stable output.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].paneless != entries[j].paneless {
			return !entries[i].paneless
		}
		return entries[i].sortKey < entries[j].sortKey
	})
	for _, e := range entries {
		snap.Windows = append(snap.Windows, e.w)
	}

	// Append the reconciler's synthetic attention overlay (#46, extended by
	// #826 to include "escalated"). These entries have no backing session,
	// so they are counted here, after the real ones.
	for _, w := range d.attention {
		snap.Windows = append(snap.Windows, w)
		snap.Summary.Total++
		countAttentionStatus(&snap.Summary, w.Status)
	}
	return snap
}

// countAttentionStatus increments the summary counter matching a synthetic
// attention entry's status. "escalated" (#826) is narrowly excluded from
// the old blanket Failed++ and counted into its own Escalated field instead
// -- every other value, including today's "failed" and any future/unknown
// status, keeps the original visible fallback (watch's match-miss rule:
// narrow the exclusion to the exact case being excluded, never broaden into
// a silent discard).
func countAttentionStatus(sum *ipc.StatusSummary, status string) {
	switch status {
	case "escalated":
		sum.Escalated++
	default:
		sum.Failed++
	}
}

func countStatus(sum *ipc.StatusSummary, s detect.Status) {
	switch s {
	case detect.StatusIdle:
		sum.Idle++
	case detect.StatusRunning:
		sum.Running++
	case detect.StatusDone:
		sum.Done++
	case detect.StatusStopped:
		sum.Stopped++
	case detect.StatusNeedInput:
		sum.NeedInput++
	}
}
