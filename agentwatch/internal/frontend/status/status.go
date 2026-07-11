package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/ipc"
)

// ErrNoOutput signals that the waybar module should be hidden (exit 1).
var ErrNoOutput = errors.New("no output")

// Config holds the symbol settings for waybar output.
type Config struct {
	SocketPath      string
	SymbolIdle      string
	SymbolRunning   string
	SymbolDone      string
	SymbolNeedInput string
	SymbolStopped   string
	SymbolFailed    string
}

// output is the Waybar custom module JSON protocol.
type output struct {
	Text    string `json:"text"`
	Tooltip string `json:"tooltip"`
	Class   string `json:"class"`
	Alt     string `json:"alt"`
}

// Run connects to the IPC socket, reads one snapshot, prints Waybar JSON, and exits.
func Run(cfg Config) error {
	client, err := ipc.Dial(cfg.SocketPath)
	if err != nil {
		// Daemon not running — tell caller to hide the module.
		return ErrNoOutput
	}
	defer func() { _ = client.Close() }()

	snap, err := client.ReadSnapshot()
	if err != nil {
		// Read error — tell caller to hide the module.
		return ErrNoOutput
	}

	out := Format(snap, cfg)
	if out.Class == "none" {
		// No sessions at all — tell caller to hide the module.
		return ErrNoOutput
	}
	return printJSON(out)
}

// Format converts a state snapshot into Waybar output.
func Format(snap *ipc.StateSnapshot, cfg Config) output {
	if len(snap.Windows) == 0 {
		return output{
			Text:    "",
			Tooltip: "no agent sessions",
			Class:   "none",
			Alt:     "none",
		}
	}

	// Build text: counts for non-zero statuses. Failed leads — it is the loudest
	// and highest-priority state.
	var parts []string
	if snap.Summary.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", cfg.SymbolFailed, snap.Summary.Failed))
	}
	if snap.Summary.Running > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", cfg.SymbolRunning, snap.Summary.Running))
	}
	if snap.Summary.NeedInput > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", cfg.SymbolNeedInput, snap.Summary.NeedInput))
	}
	if snap.Summary.Done > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", cfg.SymbolDone, snap.Summary.Done))
	}
	if snap.Summary.Stopped > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", cfg.SymbolStopped, snap.Summary.Stopped))
	}
	if snap.Summary.Idle > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", cfg.SymbolIdle, snap.Summary.Idle))
	}
	text := strings.Join(parts, "  ")

	// Build tooltip: one line per session.
	var lines []string
	for _, w := range snap.Windows {
		name := w.WindowName
		if !w.ManuallyNamed && w.TaskName != "" {
			name = w.TaskName
		}
		if name == "" {
			name = w.Agent
		}
		if w.Session == "" {
			// Paneless session (no tmux window) — no target prefix.
			lines = append(lines, fmt.Sprintf("%s (%s)", name, w.Status))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s:%s - %s (%s)", w.Session, w.WindowIndex, name, w.Status))
	}
	tooltip := strings.Join(lines, "\n")

	// Class: highest-priority status.
	class := highestClass(snap)

	return output{
		Text:    text,
		Tooltip: tooltip,
		Class:   class,
		Alt:     "active",
	}
}

// highestClass returns the CSS class for the highest-priority status.
func highestClass(snap *ipc.StateSnapshot) string {
	if snap.Summary.Failed > 0 {
		return "failed"
	}
	if snap.Summary.NeedInput > 0 {
		return "need-input"
	}
	if snap.Summary.Running > 0 {
		return "running"
	}
	if snap.Summary.Done > 0 {
		return "done"
	}
	if snap.Summary.Stopped > 0 {
		return "stopped"
	}
	if snap.Summary.Idle > 0 {
		return "idle"
	}
	return "none"
}

func printJSON(out output) error {
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(data))
	return err
}
