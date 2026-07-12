package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
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
	Text     string             `json:"text"`
	Tooltip  string             `json:"tooltip"`
	Class    string             `json:"class"`
	Alt      string             `json:"alt"`
	Headroom map[string]float64 `json:"headroom,omitempty"`
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

	out := output{
		Text:    text,
		Tooltip: tooltip,
		Class:   class,
		Alt:     "active",
	}

	if len(snap.Headroom) > 0 {
		headroomText := formatHeadroom(snap.Headroom)
		if out.Text != "" {
			out.Text += "  " + headroomText
		} else {
			out.Text = headroomText
		}
		if out.Tooltip != "" {
			out.Tooltip += "\n" + headroomText
		} else {
			out.Tooltip = headroomText
		}
		out.Headroom = snap.Headroom
	}

	return out
}

// formatHeadroom renders per-agent headroom percentages sorted by agent key
// for stable, deterministic output (map iteration order is nondeterministic).
// Invariant: agent keys are currently a fixed claude/codex whitelist from
// local config, not attacker/session-controllable — if that ever becomes
// dynamic/user-derived, re-check this (and the JXA mirror in
// agentwatch.5s.sh) for injection into downstream rendering.
func formatHeadroom(headroom map[string]float64) string {
	keys := make([]string, 0, len(headroom))
	for k := range headroom {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d%%", k, headroomPercent(headroom[k])))
	}
	return strings.Join(parts, "  ")
}

// headroomPercent converts a 0.0-1.0 headroom fraction to an integer percent,
// rounding half up (e.g. 0.156 -> 16).
func headroomPercent(frac float64) int {
	return int(math.Floor(frac*100 + 0.5))
}

// headroomClass returns the threshold class for a headroom percentage:
// >25% normal, 10-25% (inclusive) warning, <10% critical.
//
// Not called from Format(): per the plan's Q&A, waybar renders headroom as
// plain-text percentages only (no Pango markup, so no in-text coloring is
// possible there) — see formatHeadroom. Threshold coloring is a SwiftBar-only
// concern (colorForHeadroom in agentwatch.5s.sh), and JXA cannot call into Go,
// so that JS mirrors this logic rather than sharing it. This function exists
// as the canonical, independently-tested definition of the threshold rule
// (see TestHeadroomClass_Boundaries) that the JXA implementation is kept in
// sync with.
func headroomClass(pct int) string {
	switch {
	case pct > 25:
		return "normal"
	case pct >= 10:
		return "warning"
	default:
		return "critical"
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
