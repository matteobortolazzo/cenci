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
		switch ws.Status {
		case detect.StatusIdle:
			snap.Summary.Idle++
		case detect.StatusRunning:
			snap.Summary.Running++
		case detect.StatusDone:
			snap.Summary.Done++
		case detect.StatusStopped:
			snap.Summary.Stopped++
		case detect.StatusNeedInput:
			snap.Summary.NeedInput++
		}
	}
	return snap
}
