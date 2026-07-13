package dispatch

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -- SetLoopEnabled --------------------------------------------------------

// rawDaemonLoop decodes just the two dispatch keys SetLoopEnabled owns,
// straight off disk, so assertions target the literal persisted JSON rather
// than routing through mergeConfig's back-compat resolution.
type rawDaemonLoop struct {
	Dispatch struct {
		LoopEnabled    *bool  `json:"loopEnabled"`
		DaemonInterval string `json:"daemonInterval"`
	} `json:"dispatch"`
}

func mustDecodeRawDaemonLoop(t *testing.T, path string) rawDaemonLoop {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var got rawDaemonLoop
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decoding %s: %v\ncontent:\n%s", path, err, data)
	}
	return got
}

// TestSetLoopEnabled_True_SetsDefaultIntervalAndPreservesUnknownKeys locks in
// that turning the loop on: (1) sets dispatch.loopEnabled:true, (2) defaults
// dispatch.daemonInterval to "5m" since it was absent, and (3) preserves an
// unrelated top-level block plus unknown keys already inside the dispatch
// block (dispatch.repos, dispatch.concurrencyCap), matching enroll.go's
// raw-map read-modify-write pattern.
func TestSetLoopEnabled_True_SetsDefaultIntervalAndPreservesUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{
  "run": {"template": "custom-template"},
  "dispatch": {
    "repos": [{"repo": "owner/name", "dir": "/abs/dir"}],
    "concurrencyCap": 7
  }
}`)

	if err := SetLoopEnabled(path, true); err != nil {
		t.Fatalf("SetLoopEnabled: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"template": "custom-template"`)) {
		t.Errorf("unrelated top-level \"run\" block not preserved, got:\n%s", raw)
	}

	cfg := mustDecodeConfig(t, path)
	if cfg.Dispatch.ConcurrencyCap == nil || *cfg.Dispatch.ConcurrencyCap != 7 {
		t.Errorf("dispatch.concurrencyCap = %v, want preserved 7", cfg.Dispatch.ConcurrencyCap)
	}
	if !reposContains(cfg.Dispatch.Repos, "owner/name", "/abs/dir") {
		t.Errorf("dispatch.repos = %+v, want preserved entry owner/name -> /abs/dir", cfg.Dispatch.Repos)
	}

	got := mustDecodeRawDaemonLoop(t, path)
	if got.Dispatch.LoopEnabled == nil || !*got.Dispatch.LoopEnabled {
		t.Errorf("dispatch.loopEnabled = %v, want true", got.Dispatch.LoopEnabled)
	}
	if got.Dispatch.DaemonInterval != "5m" {
		t.Errorf("dispatch.daemonInterval = %q, want %q (defaulted since absent)", got.Dispatch.DaemonInterval, "5m")
	}
}

// TestSetLoopEnabled_True_KeepsExistingDaemonInterval locks in that turning
// the loop on never overwrites an already-configured daemonInterval.
func TestSetLoopEnabled_True_KeepsExistingDaemonInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{
  "dispatch": {
    "daemonInterval": "10m"
  }
}`)

	if err := SetLoopEnabled(path, true); err != nil {
		t.Fatalf("SetLoopEnabled: %v", err)
	}

	got := mustDecodeRawDaemonLoop(t, path)
	if got.Dispatch.DaemonInterval != "10m" {
		t.Errorf("dispatch.daemonInterval = %q, want preserved %q (must not overwrite an existing interval)", got.Dispatch.DaemonInterval, "10m")
	}
	if got.Dispatch.LoopEnabled == nil || !*got.Dispatch.LoopEnabled {
		t.Errorf("dispatch.loopEnabled = %v, want true", got.Dispatch.LoopEnabled)
	}
}

// TestSetLoopEnabled_False_PreservesOtherKeys locks in that turning the loop
// off writes dispatch.loopEnabled:false and preserves every other key,
// including an already-set daemonInterval (SetLoopEnabled(false) must not
// touch it).
func TestSetLoopEnabled_False_PreservesOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{
  "defaultAgent": "claude",
  "dispatch": {
    "repos": [{"repo": "owner/name", "dir": "/abs/dir"}],
    "concurrencyCap": 3,
    "daemonInterval": "5m"
  }
}`)

	if err := SetLoopEnabled(path, false); err != nil {
		t.Fatalf("SetLoopEnabled: %v", err)
	}

	cfg := mustDecodeConfig(t, path)
	if cfg.DefaultAgent != "claude" {
		t.Errorf("defaultAgent = %q, want preserved %q", cfg.DefaultAgent, "claude")
	}
	if cfg.Dispatch.ConcurrencyCap == nil || *cfg.Dispatch.ConcurrencyCap != 3 {
		t.Errorf("dispatch.concurrencyCap = %v, want preserved 3", cfg.Dispatch.ConcurrencyCap)
	}
	if !reposContains(cfg.Dispatch.Repos, "owner/name", "/abs/dir") {
		t.Errorf("dispatch.repos = %+v, want preserved entry owner/name -> /abs/dir", cfg.Dispatch.Repos)
	}

	got := mustDecodeRawDaemonLoop(t, path)
	if got.Dispatch.LoopEnabled == nil || *got.Dispatch.LoopEnabled {
		t.Errorf("dispatch.loopEnabled = %v, want false", got.Dispatch.LoopEnabled)
	}
	if got.Dispatch.DaemonInterval != "5m" {
		t.Errorf("dispatch.daemonInterval = %q, want preserved %q untouched", got.Dispatch.DaemonInterval, "5m")
	}
}

// -- ResolveDispatchState ---------------------------------------------------

// TestResolveDispatchState_FallsBackToConfigWhenSocketUnreachable locks in
// the config-fallback path (live daemon population is deferred to #220):
// with no daemon listening on socketPath, ResolveDispatchState must report
// DaemonRunning=false and derive Enabled/Interval from the resolved
// dispatch.Config (LoopEnabled and DaemonInterval respectively).
func TestResolveDispatchState_FallsBackToConfigWhenSocketUnreachable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{"dispatch": {"loopEnabled": true, "daemonInterval": "5m"}}`)

	unreachableSocket := filepath.Join(t.TempDir(), "no-daemon.sock")

	got := ResolveDispatchState(path, unreachableSocket, io.Discard)

	if got.DaemonRunning {
		t.Errorf("DaemonRunning = true, want false (no daemon listening on %s)", unreachableSocket)
	}
	if !got.Enabled {
		t.Errorf("Enabled = %v, want true (from resolved config's LoopEnabled)", got.Enabled)
	}
	if got.Interval != "5m" {
		t.Errorf("Interval = %q, want %q (resolved config's DaemonInterval)", got.Interval, "5m")
	}
}

// TestResolveDispatchState_FallbackDisabledLoopAndEmptyInterval locks in that
// the config-fallback path reports Enabled=false and an empty Interval when
// the resolved config has the loop disabled and no positive DaemonInterval.
func TestResolveDispatchState_FallbackDisabledLoopAndEmptyInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{"dispatch": {"loopEnabled": false}}`)

	unreachableSocket := filepath.Join(t.TempDir(), "no-daemon.sock")

	got := ResolveDispatchState(path, unreachableSocket, io.Discard)

	if got.DaemonRunning {
		t.Errorf("DaemonRunning = true, want false (no daemon listening on %s)", unreachableSocket)
	}
	if got.Enabled {
		t.Errorf("Enabled = %v, want false", got.Enabled)
	}
	if got.Interval != "" {
		t.Errorf("Interval = %q, want empty when DaemonInterval resolves to 0", got.Interval)
	}
}

// TestResolveDispatchState_LogsMalformedConfigError locks in that a
// LoadConfig error (malformed config.json) is surfaced via logf rather than
// silently discarded -- distinct from the intentional socket-unreachable
// fallback exercised above, a broken config.json must not masquerade as a
// silent "loop disabled" with no signal to the caller.
func TestResolveDispatchState_LogsMalformedConfigError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{not valid json`)

	unreachableSocket := filepath.Join(t.TempDir(), "no-daemon.sock")

	var buf bytes.Buffer
	got := ResolveDispatchState(path, unreachableSocket, &buf)

	if got.Enabled {
		t.Errorf("Enabled = %v, want false (fallback zero-value Config on load error)", got.Enabled)
	}
	if buf.Len() == 0 {
		t.Error("expected the LoadConfig error to be logged, got no output")
	}
	if !strings.Contains(buf.String(), "loading config") {
		t.Errorf("logged output = %q, want it to mention %q", buf.String(), "loading config")
	}
}
