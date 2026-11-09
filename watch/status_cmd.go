package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/config"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/dispatch"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend/status"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/measurement"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/modelswitch"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// runWidgetJSON implements the hidden plumbing subcommand `widget-json`
// (alias `waybar`): prints a single line of Waybar custom-module JSON and
// exits. This is the exact behavior the old `cenci status` had before
// `status` became the human-readable overview below — every widget frontend
// (noctalia, dms, gnome, plasma, macOS/SwiftBar) and any real Waybar config
// should invoke this subcommand, not `status`.
func runWidgetJSON(args []string) {
	fs := flag.NewFlagSet("widget-json", flag.ExitOnError)
	defaults := config.Default()
	wcfg := status.Config{
		SymbolIdle:            defaults.SymbolIdle,
		SymbolRunning:         defaults.SymbolRunning,
		SymbolDone:            defaults.SymbolDone,
		SymbolNeedInput:       defaults.SymbolNeedInput,
		SymbolStopped:         defaults.SymbolStopped,
		SymbolFailed:          defaults.SymbolFailed,
		SymbolEscalated:       defaults.SymbolEscalated,
		SymbolDispatch:        status.DefaultSymbolDispatch,
		SymbolDispatchRunning: status.DefaultSymbolDispatchRunning,
	}
	fs.StringVar(&wcfg.SocketPath, "socket", ipc.DefaultSocketPath(), "IPC socket path")
	fs.StringVar(&wcfg.SymbolIdle, "symbol-idle", wcfg.SymbolIdle, "symbol for idle state")
	fs.StringVar(&wcfg.SymbolRunning, "symbol-running", wcfg.SymbolRunning, "symbol for running state")
	fs.StringVar(&wcfg.SymbolDone, "symbol-done", wcfg.SymbolDone, "symbol for done state")
	fs.StringVar(&wcfg.SymbolNeedInput, "symbol-input", wcfg.SymbolNeedInput, "symbol for need-input state")
	fs.StringVar(&wcfg.SymbolStopped, "symbol-stopped", wcfg.SymbolStopped, "symbol for stopped (interrupted) state")
	fs.StringVar(&wcfg.SymbolFailed, "symbol-failed", wcfg.SymbolFailed, "symbol for dispatch-failed state")
	fs.StringVar(&wcfg.SymbolEscalated, "symbol-escalated", wcfg.SymbolEscalated, "symbol for a planner-escalated (Input Needed) ticket")
	fs.StringVar(&wcfg.SymbolDispatch, "symbol-dispatch", wcfg.SymbolDispatch, "symbol for the fleet dispatch loop indicator")
	fs.StringVar(&wcfg.SymbolDispatchRunning, "symbol-dispatch-running", wcfg.SymbolDispatchRunning, "symbol for a fleet dispatch pass actively running")
	_ = fs.Parse(args)

	if err := status.Run(wcfg); err != nil {
		if errors.Is(err, status.ErrNoOutput) {
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "cenci widget-json: %v\n", err)
		os.Exit(1)
	}
}

// runStatus implements the human-readable `cenci status` overview:
// daemon running/pid, active sessions (read from the same broadcast state
// snapshot widget-json reads), and the embedded fleet dispatch loop's state
// (reusing renderDispatchState, the same renderer `dispatch loop status`
// uses). It degrades gracefully when the daemon is down — it still prints a
// report and always exits 0 (unlike `daemon status`, which exits 1 when not
// running; see the exit-code note in README.md).
func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "IPC broadcast socket path")
	eventSocketPath := fs.String("event-socket", ipc.DefaultEventSocketPath(), "event socket path")
	_ = fs.Parse(args)
	rejectExtra("cenci status", fs.Args())

	info := daemon.Status(*eventSocketPath, ipc.DefaultPIDPath())
	fmt.Print(renderHumanStatus(info, *socketPath))
}

// renderHumanStatus renders the report body for runStatus. Split out for
// unit testing without a subprocess.
func renderHumanStatus(info daemon.StatusInfo, socketPath string) string {
	var b strings.Builder

	if info.Running {
		if info.PID > 0 {
			fmt.Fprintf(&b, "daemon: running (pid %d)\n", info.PID)
		} else {
			fmt.Fprintf(&b, "daemon: running (pid unknown)\n")
		}
	} else {
		fmt.Fprintf(&b, "daemon: not running\n")
	}

	snap, err := dispatch.ReadSnapshot(socketPath)
	now := time.Now()
	switch {
	case errors.Is(err, watch.ErrDaemonUnreachable):
		fmt.Fprintf(&b, "sessions: unavailable (daemon not reachable)\n")
	case err != nil:
		fmt.Fprintf(&b, "sessions: unavailable (error reading snapshot: %v)\n", err)
	case snap == nil:
		fmt.Fprintf(&b, "sessions: unavailable (daemon not reachable)\n")
	case len(snap.Windows) == 0:
		fmt.Fprintf(&b, "sessions: none\n")
	default:
		fmt.Fprintf(&b, "sessions (%d):\n", len(snap.Windows))
		for _, w := range snap.Windows {
			fmt.Fprintf(&b, "  %s\n", status.FormatSessionLine(w, status.Identity))
			for _, sw := range w.ModelSwitches {
				// The snapshot comes from a socket, so the values are
				// sanitized again before they reach the terminal.
				fmt.Fprintf(&b, "    %s\n", modelswitch.FormatLine(
					modelswitch.SanitizeModel(sw.FromModel), modelswitch.SanitizeModel(sw.ToModel),
					sw.Subagent, modelswitch.SanitizeAgentType(sw.AgentType), sw.Fallback))
			}
			if w.ModelSwitchesMore > 0 {
				fmt.Fprintf(&b, "    %s\n", modelswitch.FormatMore(w.ModelSwitchesMore))
			}
			if w.NativeMeasurement != nil {
				renderNativeLines(&b, w.NativeMeasurement, now)
			}
		}
	}

	// The configured estimate is the dispatch input; it is printed on its own
	// line and never mixed into the native lines above.
	if err == nil && snap != nil && len(snap.Headroom) > 0 {
		b.WriteString(renderBudgetHeadroom(snap.Headroom))
	}

	dstate := dispatch.ResolveDispatchState("", socketPath, io.Discard)
	b.WriteString(renderDispatchState(dstate))

	return b.String()
}

const (
	nativeNotReported = "unknown (not reported)"
	nativeInvalid     = "unknown (invalid)"
	nativeExpired     = "expired (reset passed)"
	nativeStale       = "stale"
)

// renderNativeLines writes the two native-measurement lines under a session.
// The sample came from a socket, so every value is checked again here instead of
// trusting what the daemon stored: a nil value is never shown as zero, a value
// out of range is "unknown (invalid)", and free text goes through
// measurement.SanitizeKind. The quota line is labelled session-scoped plan
// quota and the context line carries no quota wording, so one is never read as
// the other.
func renderNativeLines(b *strings.Builder, m *watch.NativeMeasurement, now time.Time) {
	suffix := nativeSuffix(m, now)
	fmt.Fprintf(b, "    context: %s — %s\n", nativeContextText(m), suffix)
	fmt.Fprintf(b, "    plan quota (native, session-scoped): %s — %s\n", nativeQuotaText(m, now), suffix)
}

// nativeSuffix is the source and age tail of both native lines. The source is
// always claude: the measurement mode accepts only `-agent claude`. A measured_at
// that does not parse, or is past measurement.FutureTolerance ahead of now (the
// cap the parser applies), is "unknown (invalid)", never a fresh age.
func nativeSuffix(m *watch.NativeMeasurement, now time.Time) string {
	source := "source: claude " + measurement.SanitizeKind(m.Source)
	measuredAt, err := time.Parse(time.RFC3339, m.MeasuredAt)
	if err != nil || measuredAt.After(now.Add(measurement.FutureTolerance)) {
		return source + ", measured " + nativeInvalid
	}
	suffix := fmt.Sprintf("%s, measured %s ago", source, nativeAge(now.Sub(measuredAt)))
	if measurement.Freshness(measuredAt, now) == nativeStale {
		suffix += " (stale)"
	}
	return suffix
}

// nativeAge renders d in one compact unit, rounded down: s, m, h, then d from
// 48h. A negative d (a sample slightly ahead of this clock) is 0s.
func nativeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(int(d/time.Second), 0))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

// nativeMissingText is the placeholder for a field the sample has no value for:
// "unknown (invalid)" when the daemon rejected it, "unknown (not reported)" when
// it was never sent.
func nativeMissingText(m *watch.NativeMeasurement, path string) string {
	if slices.Contains(m.Invalid, path) {
		return nativeInvalid
	}
	return nativeNotReported
}

// nativePercent renders v as a percentage with at most one decimal (a trailing
// ".0" is dropped, so a real 0.4 never reads as 0), or the placeholder for a
// nil value or one outside [0, upper].
func nativePercent(m *watch.NativeMeasurement, v *float64, upper float64, path string) string {
	switch {
	case v == nil:
		return nativeMissingText(m, path)
	case math.IsNaN(*v) || *v < 0 || *v > upper:
		return nativeInvalid
	default:
		return strings.TrimSuffix(strconv.FormatFloat(*v, 'f', 1, 64), ".0") + "%"
	}
}

// nativeCount renders a whole-number context field, or the placeholder.
func nativeCount(m *watch.NativeMeasurement, v *int64, lowest int64, path string) string {
	switch {
	case v == nil:
		return nativeMissingText(m, path)
	case *v < lowest:
		return nativeInvalid
	default:
		return strconv.FormatInt(*v, 10)
	}
}

func nativeContextText(m *watch.NativeMeasurement) string {
	c := m.Context
	if c == nil {
		return nativeMissingText(m, "context")
	}
	percent := nativePercent(m, c.Percent, math.Inf(1), "context.percent")
	// Over-window is worked out from the percentage, not from the stored flag.
	if c.Percent != nil && *c.Percent > 100 {
		percent += " (over window)"
	}
	return fmt.Sprintf("percent %s, tokens %s, window %s", percent,
		nativeCount(m, c.Tokens, 0, "context.tokens"), nativeCount(m, c.Window, 1, "context.window"))
}

func nativeQuotaText(m *watch.NativeMeasurement, now time.Time) string {
	if m.Quota == nil {
		return nativeMissingText(m, "rate_limits")
	}
	if len(m.Quota) == 0 {
		return "none reported"
	}
	// rate_limits[<i>] paths index the input array, but Quota skips every entry
	// that was rejected as a non-object, so walk past those input indexes.
	windows := make([]string, 0, len(m.Quota))
	input := 0
	for _, w := range m.Quota {
		for slices.Contains(m.Invalid, rateLimitPath(input)) {
			input++
		}
		windows = append(windows, nativeWindowText(m, w, rateLimitPath(input), now))
		input++
	}
	return strings.Join(windows, ", ")
}

func rateLimitPath(i int) string {
	return "rate_limits[" + strconv.Itoa(i) + "]"
}

// nativeWindowText renders one quota window: `<kind> <p>% used, resets in <age>`,
// or `<kind> expired (reset passed)` with no percentage once the reset passed.
func nativeWindowText(m *watch.NativeMeasurement, w watch.NativeQuotaWindow, path string, now time.Time) string {
	kind := nativeKind(w.Kind)

	resets := "resets " + nativeMissingText(m, path+".resets_at")
	if w.ResetsAt != "" {
		resetsAt, err := time.Parse(time.RFC3339, w.ResetsAt)
		switch {
		case err != nil:
			resets = "resets " + nativeInvalid
		case measurement.QuotaState(resetsAt, now) == nativeExpired:
			return kind + " " + nativeExpired
		default:
			resets = "resets in " + nativeAge(resetsAt.Sub(now))
		}
	}

	used := nativePercent(m, w.PercentUsed, 100, path+".percent_used")
	if w.PercentUsed != nil && used != nativeInvalid {
		used += " used"
	} else {
		used = "used " + used
	}
	return kind + " " + used + ", " + resets
}

// nativeKind passes the two closed window labels through and reduces every
// other value to "unknown:" plus a sanitized label.
func nativeKind(kind string) string {
	if kind == "five_hour" || kind == "weekly" {
		return kind
	}
	return "unknown:" + measurement.SanitizeKind(strings.TrimPrefix(kind, "unknown:"))
}

// renderBudgetHeadroom renders the configured budget estimate (the dispatch
// input) as one separately labelled line: `<agent> <p>%` pairs in key order.
// Keys and values come from a socket, so keys are sanitized and a value outside
// [0, 1] is "unknown (invalid)".
func renderBudgetHeadroom(headroom map[string]float64) string {
	parts := make([]string, 0, len(headroom))
	for _, k := range slices.Sorted(maps.Keys(headroom)) {
		value := nativeInvalid
		if v := headroom[k]; !math.IsNaN(v) && v >= 0 && v <= 1 {
			value = fmt.Sprintf("%d%%", int(math.Floor(v*100+0.5)))
		}
		parts = append(parts, measurement.SanitizeKind(k)+" "+value)
	}
	return "budget headroom (configured estimate): " + strings.Join(parts, "  ") + "\n"
}
