package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// SetLoopEnabled idempotently sets dispatch.loopEnabled, preserving every
// other key verbatim (same raw-map read-modify-write pattern as
// EnrollRepo/UnenrollRepo). When enabled is true and dispatch.daemonInterval
// is absent, it is also defaulted to "5m" so the embedded loop has an interval
// to run on; an already-configured daemonInterval is never overwritten. An
// empty path resolves run.DefaultConfigPath().
func SetLoopEnabled(path string, enabled bool) error {
	path, err := resolveConfigPath(path)
	if err != nil {
		return err
	}

	top, err := readRawConfig(path)
	if err != nil {
		return err
	}

	dispatchRaw := map[string]json.RawMessage{}
	if raw, ok := top["dispatch"]; ok {
		if err := json.Unmarshal(raw, &dispatchRaw); err != nil {
			return fmt.Errorf("parsing dispatch block: %w", err)
		}
	}

	enabledRaw, err := json.Marshal(enabled)
	if err != nil {
		return fmt.Errorf("marshaling loopEnabled: %w", err)
	}
	dispatchRaw["loopEnabled"] = enabledRaw

	if enabled {
		if _, exists := dispatchRaw["daemonInterval"]; !exists {
			intervalRaw, err := json.Marshal("5m")
			if err != nil {
				return fmt.Errorf("marshaling daemonInterval: %w", err)
			}
			dispatchRaw["daemonInterval"] = intervalRaw
		}
	}

	dispatchBlockRaw, err := json.Marshal(dispatchRaw)
	if err != nil {
		return fmt.Errorf("marshaling dispatch block: %w", err)
	}
	top["dispatch"] = dispatchBlockRaw

	return writeRawConfig(path, top)
}

// ResolveDispatchState reports the embedded fleet dispatch loop's current
// state, socket-first: if a daemon is reachable at socketPath and its latest
// snapshot carries a non-nil Dispatch, that is returned verbatim. Otherwise it
// falls back to the resolved dispatch.Config, reporting DaemonRunning=false,
// Enabled from LoopEnabled, and Interval from DaemonInterval (best-effort,
// empty when DaemonInterval is 0). Since #220 the daemon always runs the
// embedded dispatch loop and populates StateSnapshot.Dispatch from its live
// state, so the socket-first path above fires whenever a daemon is reachable.
// A LoadConfig error (malformed/unreadable config.json) is logged to
// out via the same logf convention as reloadConfig -- unlike the intentional
// socket-unreachable fallback above, a broken config must not masquerade as a
// silent "loop disabled" with no signal to the caller. Similarly, when the
// fallback is triggered by a ReadSnapshot error other than
// watch.ErrDaemonUnreachable (for example a corrupt/truncated snapshot or a
// permission-denied socket -- the daemon was reached but then failed), the
// real error is surfaced via the returned state's ResolveError field (#446);
// a genuinely unreachable daemon (no listener at all) stays silent.
func ResolveDispatchState(configPath, socketPath string, out io.Writer) watch.DispatchState {
	snap, snapErr := ReadSnapshot(socketPath)
	if snapErr == nil && snap != nil && snap.Dispatch != nil {
		return *snap.Dispatch
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		logf(out, "dispatch: loading config: %v\n", err)
	}
	state := watch.DispatchState{
		Enabled:       cfg.LoopEnabled,
		DaemonRunning: false,
	}
	if cfg.DaemonInterval > 0 {
		state.Interval = formatInterval(cfg.DaemonInterval)
	}
	if snapErr != nil && !errors.Is(snapErr, watch.ErrDaemonUnreachable) {
		state.ResolveError = snapErr.Error()
	}
	return state
}

// formatInterval renders d the way it's typically written in config.json
// (e.g. "5m" rather than Go's canonical Duration.String() of "5m0s") for
// whole-unit durations, falling back to Duration.String() otherwise.
func formatInterval(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%ds", d/time.Second)
	default:
		return d.String()
	}
}
