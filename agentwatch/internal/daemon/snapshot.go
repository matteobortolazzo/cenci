package daemon

import (
	"sort"
	"strings"
	"time"

	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/detect"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/ipc"
)

func (d *Daemon) broadcast() {
	if d.ipc != nil {
		d.ipc.Broadcast(d.buildSnapshot())
	}
}

func (d *Daemon) buildSnapshot() ipc.StateSnapshot {
	snap := ipc.StateSnapshot{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	targets := make([]string, 0, len(d.windows))
	for wt := range d.windows {
		targets = append(targets, wt)
	}
	sort.Strings(targets)

	for _, wt := range targets {
		ws := d.windows[wt]
		parts := strings.SplitN(wt, ":", 2)
		session, winIdx := parts[0], parts[1]

		// Use clean names (without symbol prefix) for IPC output.
		winName := ws.OriginalName
		if !ws.ManuallyNamed && ws.TaskName != "" {
			winName = ws.TaskName
		}
		status := ws.Status.String()
		snap.Windows = append(snap.Windows, ipc.WindowState{
			Session:       session,
			WindowIndex:   winIdx,
			WindowName:    winName,
			TaskName:      ws.TaskName,
			Status:        status,
			Agent:         ws.Agent,
			ManuallyNamed: ws.ManuallyNamed,
		})

		snap.Summary.Total++
		countStatus(&snap.Summary, ws.Status)
	}

	// Paneless sessions render after tmux entries, with empty tmux fields.
	keys := make([]string, 0, len(d.sessions))
	for key, sess := range d.sessions {
		if sess.TmuxPane == "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		sess := d.sessions[key]
		snap.Windows = append(snap.Windows, ipc.WindowState{
			WindowName: sess.TaskName,
			TaskName:   sess.TaskName,
			Status:     sess.Status.String(),
			Agent:      sess.Agent,
		})
		snap.Summary.Total++
		countStatus(&snap.Summary, sess.Status)
	}
	return snap
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
