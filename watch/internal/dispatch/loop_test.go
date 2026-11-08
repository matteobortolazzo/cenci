package dispatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// failingProbeController is a goroutine-safe run.Controller whose HasSession
// always errors, counting its calls (RunLoop runs on its own goroutine).
type failingProbeController struct{ probes atomic.Int32 }

func (*failingProbeController) CurrentSession(string) (string, error) {
	return "", errors.New("the dispatch path must never consult CurrentSession")
}
func (*failingProbeController) IsGroupedSession(string, string) (bool, error) { return false, nil }
func (*failingProbeController) NewWindow(string, string, string, string) error {
	return errors.New("a probe-failed repo must never spawn")
}
func (*failingProbeController) SetWindowOption(string, string, string, string) error { return nil }
func (c *failingProbeController) HasSession(string, string) (bool, error) {
	c.probes.Add(1)
	return false, errors.New("tmux: lost server")
}

// TestRunLoop_SessionProbeFailedKeepsTicking (#1464): a pass that returns
// ErrSessionProbeFailed does not stop RunLoop -- the next tick runs. The fake gh
// fails the identity read on the second tick, a genuinely fatal error, so the
// loop's return proves that tick ran and the test needs no stop seam.
func TestRunLoop_SessionProbeFailedKeepsTicking(t *testing.T) {
	mainSyncGitEnv(t)
	serveChainSnapshot(t, watch.StateSnapshot{})
	local, _ := initOriginAndLocal(t)
	planSha := gitTest(t, local, "rev-parse", "HEAD")
	writePlan(t, local, "42-x.md", "---\nticketId: 42\nstatus: planned\nplanCommitSha: "+planSha+"\n---\nbody\n")
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, fmt.Sprintf(`{"dispatch": {"repos": [{"repo": "o/r", "dir": %q, "session": "ghost"}]}}`, local))

	marker := filepath.Join(t.TempDir(), "identity-read")
	installFakeGHOnPath(t, `
case "$1 $2" in
  "issue list") printf '[{"number":42,"title":"Fix thing","labels":[{"name":"Planned"}],"assignees":[{"login":"octocat"}]}]' ;;
  "api graphql") printf '`+emptyOpenPRPageJSON+`' ;;
  "api user")
    if [ -e '`+marker+`' ]; then printf 'token expired\n' >&2; exit 1; fi
    : > '`+marker+`'
    printf 'octocat\n' ;;
  *) exit 1 ;;
esac
`)
	stubRunFn(t, func(run.Opts, run.Controller) error { return errors.New("a probe-failed repo must never spawn") })

	ctrl := &failingProbeController{}
	done := make(chan error, 1)
	go func() { done <- RunLoop(path, ctrl, &fakeMutator{}, 10*time.Millisecond, io.Discard, "") }()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "detecting current GitHub user") {
			t.Fatalf("RunLoop returned %v, want the second tick's identity failure (the probe-failed first tick must not stop the loop)", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RunLoop did not reach its second tick")
	}
	if got := ctrl.probes.Load(); got != 1 {
		t.Errorf("HasSession probes = %d, want 1 (the first tick's)", got)
	}
}

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
	// #446 regression guard: a genuinely unreachable daemon (no listener at
	// all, i.e. errors.Is(err, watch.ErrDaemonUnreachable)) must stay silent
	// -- ResolveError is reserved for a daemon that was reached but then
	// failed (corrupt snapshot, permission denied, etc).
	if got.ResolveError != "" {
		t.Errorf("ResolveError = %q, want empty (socket-unreachable must stay silent, not surface as a resolve error)", got.ResolveError)
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

// TestResolveDispatchState_SurfacesCorruptSnapshotError covers #446: when a
// daemon is reachable (Dial succeeds -- the daemon IS reachable) but its
// first NDJSON line is malformed/truncated, ResolveDispatchState must not
// silently treat this the same as "daemon simply isn't running" -- the real
// ReadSnapshot error must surface via ResolveError, distinct from the
// intentional errors.Is(err, watch.ErrDaemonUnreachable) silence exercised by
// TestResolveDispatchState_FallsBackToConfigWhenSocketUnreachable above
// (mirrors the "sessions:" line pattern PR #445 established in
// watch/status_cmd.go for #412).
func TestResolveDispatchState_SurfacesCorruptSnapshotError(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, configPath, `{"dispatch": {"loopEnabled": true, "daemonInterval": "5m"}}`)

	socketPath := filepath.Join(t.TempDir(), "corrupt.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Malformed NDJSON: not valid JSON, and never newline-terminated
		// before the connection closes -- a corrupt/truncated line.
		_, _ = conn.Write([]byte("{this is not valid json"))
	}()

	got := ResolveDispatchState(configPath, socketPath, io.Discard)

	if got.ResolveError == "" {
		t.Error("ResolveError = \"\", want the real decode error surfaced (the daemon was reached, it just sent garbage)")
	}
	if !strings.Contains(got.ResolveError, "invalid character") {
		t.Errorf("ResolveError = %q, want it to contain the real JSON decode error (\"invalid character\"), not a generic placeholder", got.ResolveError)
	}
	if got.DaemonRunning {
		t.Error("DaemonRunning = true, want false (fallback path, even though the daemon was briefly reachable)")
	}
	if !got.Enabled {
		t.Errorf("Enabled = %v, want true (fallback still resolves config's LoopEnabled)", got.Enabled)
	}
	if got.Interval != "5m" {
		t.Errorf("Interval = %q, want %q (fallback still resolves config's DaemonInterval)", got.Interval, "5m")
	}
}

// TestResolveDispatchState_SurfacesPermissionDeniedError covers #446's
// permission-denied case: a socket file that exists (so the daemon may well
// be running) but is unreadable/unwritable by the current user must not be
// reported identically to "daemon not reachable" -- the real permission
// error must surface via ResolveError. Simulated via Unix socket-file
// permission bits (0000), which reliably yields EACCES on connect for a
// non-root caller; skipped when running as root since root bypasses Unix
// file permission checks entirely, making the simulation unreliable in that
// environment (same simulation watch/status_test.go's
// TestStatusSubcommand_HumanReadable_PermissionDeniedSocketShowsRealError
// uses for #412).
func TestResolveDispatchState_SurfacesPermissionDeniedError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses Unix socket permission checks; cannot simulate permission-denied")
	}

	configPath := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, configPath, `{"dispatch": {"loopEnabled": true, "daemonInterval": "5m"}}`)

	socketPath := filepath.Join(t.TempDir(), "denied.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	if err := os.Chmod(socketPath, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	got := ResolveDispatchState(configPath, socketPath, io.Discard)

	if got.ResolveError == "" {
		t.Error("ResolveError = \"\", want the real permission error surfaced (the socket file exists, the connection was just denied)")
	}
	if !strings.Contains(got.ResolveError, "permission denied") {
		t.Errorf("ResolveError = %q, want it to contain the real permission error (\"permission denied\"), not a generic placeholder", got.ResolveError)
	}
	if got.DaemonRunning {
		t.Error("DaemonRunning = true, want false (fallback path)")
	}
}
