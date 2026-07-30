package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/pkg/watch"
)

// ErrNoOutput signals that the waybar module should be hidden (exit 1).
var ErrNoOutput = errors.New("no output")

// DefaultSymbolDispatch is the default glyph shown when the fleet dispatch
// loop is enabled (U+27F3, per planning Q&A #1). It lives in this package
// (not internal/config) since SymbolDispatch is status-render-only, mirroring
// the SymbolFailed wiring precedent.
const DefaultSymbolDispatch = "⟳"

// DefaultSymbolDispatchRunning is the default glyph shown when a fleet
// dispatch pass is actively running (U+2699, per planning Q&A #1). The
// plain gear was chosen specifically to match the size/weight of the
// existing ⟳/▶ glyphs, avoiding the heavier-glyph size mismatch observed in
// DMS. Like DefaultSymbolDispatch, it lives in this package (not
// internal/config) since it is status-render-only, mirroring the
// DefaultSymbolDispatch precedent.
const DefaultSymbolDispatchRunning = "⚙"

// pangoReplacer escapes the three Pango markup-special characters for text
// interpolated into the Waybar tooltip, which Waybar renders as Pango
// markup. This is a single non-overlapping left-to-right pass, so the "&"
// it introduces when escaping "<"/">" is never itself re-escaped. This is
// distinct from sanitizeLastError in internal/dispatch/combined.go, which
// defends SwiftBar (a different renderer with a different threat model) —
// do not merge the two.
var pangoReplacer = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// escapePango escapes &, <, > for safe interpolation into Pango markup.
func escapePango(s string) string {
	return pangoReplacer.Replace(s)
}

// Identity is a no-op escape function for FormatSessionLine callers that
// render plain text (no markup) and so don't need Pango escaping — e.g.
// renderHumanStatus in status_cmd.go.
func Identity(s string) string { return s }

// FormatSessionLine renders the unified per-window session line shared by
// `cenci status` (human output, via renderHumanStatus in status_cmd.go) and
// `widget-json`/waybar (tooltip, via Format below), so the two render sites
// can't drift out of sync (#405):
//
//   - Tmux-backed:  "session:index - name (agent) (status)"
//   - Sessionless:  "(no session) - name (agent) (status)"
//
// A missing/blank name renders as the literal placeholder "(untitled)"
// (never blank); a missing/blank agent renders as "(unknown)" (never
// blank/silently dropped). status stays the trailing parenthesized token,
// unchanged in wording — several plugins parse tooltip lines by an
// end-anchored "(status)" regex, so status must never move out of the final
// position.
//
// escape is applied to every free-text field (session, name, agent) before
// interpolation. Pass escapePango for markup-rendering consumers (the waybar
// tooltip) or Identity for plain-text consumers (the human status line).
func FormatSessionLine(w ipc.WindowState, escape func(string) string) string {
	name := w.WindowName
	if !w.ManuallyNamed && w.TaskName != "" {
		name = w.TaskName
	}
	if name == "" {
		name = "(untitled)"
	}
	name = escape(name)

	agent := w.Agent
	if agent == "" {
		agent = "unknown"
	}
	agent = escape(agent)

	if w.Session == "" {
		return fmt.Sprintf("(no session) - %s (%s) (%s)", name, agent, w.Status)
	}
	return fmt.Sprintf("%s:%s - %s (%s) (%s)", escape(w.Session), w.WindowIndex, name, agent, w.Status)
}

// Config holds the symbol settings for waybar output.
type Config struct {
	SocketPath      string
	SymbolIdle      string
	SymbolRunning   string
	SymbolDone      string
	SymbolNeedInput string
	SymbolStopped   string
	SymbolFailed    string
	// SymbolEscalated (#826) is the glyph for Summary.Escalated -- a ticket
	// the unattended planner escalated (Input Needed), distinct from
	// SymbolNeedInput's live-session-mid-turn meaning. Deliberately a
	// different glyph from both SymbolNeedInput and SymbolFailed so the two
	// concepts never render identically.
	SymbolEscalated       string
	SymbolDispatch        string
	SymbolDispatchRunning string
}

// output is the Waybar custom module JSON protocol.
type output struct {
	Text     string               `json:"text"`
	Tooltip  string               `json:"tooltip"`
	Class    string               `json:"class"`
	Alt      string               `json:"alt"`
	Headroom map[string]float64   `json:"headroom,omitempty"`
	Dispatch *watch.DispatchState `json:"dispatch,omitempty"`
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
	if out.Alt == "none" {
		// No sessions at all and no dispatch loop — tell caller to hide the module.
		return ErrNoOutput
	}
	return printJSON(out)
}

// Format converts a state snapshot into Waybar output.
func Format(snap *ipc.StateSnapshot, cfg Config) output {
	dispatchGlyph, dispatchLine := formatDispatch(cfg, snap.Dispatch)
	dispatchEnabled := snap.Dispatch != nil && snap.Dispatch.Enabled

	if len(snap.Windows) == 0 {
		out := output{
			Text:    "",
			Tooltip: "no agent sessions",
			Class:   "none",
			Alt:     "none",
		}
		appendDispatch(&out, dispatchGlyph, dispatchLine)
		if dispatchEnabled {
			out.Alt = "dispatch-only"
			out.Dispatch = snap.Dispatch
		}
		return out
	}

	// Build text: counts for non-zero statuses. Failed leads — it is the
	// loudest and highest-priority state; Escalated (#826) is next, ahead of
	// need-input/running.
	var parts []string
	if snap.Summary.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", cfg.SymbolFailed, snap.Summary.Failed))
	}
	if snap.Summary.Escalated > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", cfg.SymbolEscalated, snap.Summary.Escalated))
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
		lines = append(lines, FormatSessionLine(w, escapePango))
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

	appendDispatch(&out, dispatchGlyph, dispatchLine)
	if dispatchEnabled {
		out.Dispatch = snap.Dispatch
	}

	return out
}

// appendDispatch appends the dispatch glyph/tooltip line to an in-progress
// output, following the same "append with separator, else set" pattern used
// for headroom text. No-op when glyph/line are empty (loop disabled/absent).
func appendDispatch(out *output, glyph, line string) {
	if glyph != "" {
		if out.Text != "" {
			out.Text += "  " + glyph
		} else {
			out.Text = glyph
		}
	}
	if line != "" {
		if out.Tooltip != "" {
			out.Tooltip += "\n" + line
		} else {
			out.Tooltip = line
		}
	}
}

// formatDispatch renders the fleet dispatch loop's compact glyph and tooltip
// summary line. Returns empty strings for both when the loop is nil or
// disabled, so callers can byte-compatibly skip the indicator entirely.
func formatDispatch(cfg Config, d *watch.DispatchState) (glyph string, line string) {
	if d == nil || !d.Enabled {
		return "", ""
	}

	summary := "dispatch: on"
	if d.Interval != "" {
		summary = fmt.Sprintf("dispatch: on (%s)", d.Interval)
	}
	switch {
	case d.LastError != "":
		summary += fmt.Sprintf(" — last run failed: %s", escapePango(d.LastError))
	case d.LastRunAt != "":
		summary += fmt.Sprintf(" — last run %s, %d dispatched / %d skipped", formatLastRunTime(d.LastRunAt), d.LastDispatched, d.LastSkipped)
	}

	glyph = cfg.SymbolDispatch
	if d.PassRunning {
		glyph = cfg.SymbolDispatchRunning
	}

	return glyph, summary
}

// formatLastRunTime renders an RFC 3339 timestamp as local HH:MM. Falls back
// to the raw string on a parse error (defensive; the daemon always emits
// RFC 3339).
func formatLastRunTime(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return t.Local().Format("15:04")
}

// formatHeadroom renders per-agent headroom percentages sorted by agent key
// for stable, deterministic output (map iteration order is nondeterministic).
// Invariant: agent keys are currently a fixed claude/codex whitelist from
// local config, not attacker/session-controllable — if that ever becomes
// dynamic/user-derived, re-check this (and the JXA mirror in
// cenci.5s.sh) for injection into downstream rendering.
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
// concern (colorForHeadroom in cenci.5s.sh), and JXA cannot call into Go,
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

// highestClass returns the CSS class for the highest-priority status:
// failed > escalated > need-input > running > done > stopped > idle (#826
// inserted escalated between failed and need-input).
func highestClass(snap *ipc.StateSnapshot) string {
	if snap.Summary.Failed > 0 {
		return "failed"
	}
	if snap.Summary.Escalated > 0 {
		return "escalated"
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
