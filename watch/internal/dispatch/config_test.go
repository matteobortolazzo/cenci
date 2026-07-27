package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadConfigMergesModel locks in that a "model" key in the "dispatch"
// block of config.json pins Config.Model, the persisted default that survives
// process restarts and every config reload (unlike a CLI-only override). This
// is the fix for a class of bug where a dispatched session silently inherited
// whatever ambient/account-level default model was active at spawn time.
func TestLoadConfigMergesModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dispatch": {"model": "claude-sonnet-5"}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Model != "claude-sonnet-5" {
		t.Errorf("cfg.Model = %q, want %q", cfg.Model, "claude-sonnet-5")
	}
}

// TestLoadConfigModelDefaultsEmpty locks in that an absent "model" key leaves
// Config.Model empty, so applyDispatch falls back to run's per-agent
// agents.*.model config (or the ambient default) exactly as before this field
// existed.
func TestLoadConfigModelDefaultsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dispatch": {"defaultAgent": "codex"}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Model != "" {
		t.Errorf("cfg.Model = %q, want empty when unset", cfg.Model)
	}
}

// -- LoopEnabled resolution (#219) -----------------------------------------

// TestLoadConfigLoopEnabled_ExplicitTrue locks in that an explicit
// "loopEnabled": true wins regardless of any daemonInterval setting.
func TestLoadConfigLoopEnabled_ExplicitTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dispatch": {"loopEnabled": true}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.LoopEnabled {
		t.Errorf("cfg.LoopEnabled = %v, want true", cfg.LoopEnabled)
	}
}

// TestLoadConfigLoopEnabled_ExplicitFalseWinsOverInterval locks in that an
// explicit "loopEnabled": false is honored even when a daemonInterval is
// configured (an interval alone must not force the loop on when the user has
// explicitly opted out).
func TestLoadConfigLoopEnabled_ExplicitFalseWinsOverInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dispatch": {"loopEnabled": false, "daemonInterval": "5m"}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.LoopEnabled {
		t.Errorf("cfg.LoopEnabled = %v, want false", cfg.LoopEnabled)
	}
}

// TestLoadConfigLoopEnabled_IntervalOnlyDefaultsOff locks in that an absent
// "loopEnabled" key with a positive daemonInterval does NOT implicitly enable
// the loop. The embedded fleet dispatch loop must default to off and only
// turn on via an explicit "loopEnabled": true; daemonInterval alone is no
// longer sufficient to opt in.
func TestLoadConfigLoopEnabled_IntervalOnlyDefaultsOff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dispatch": {"daemonInterval": "5m"}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.LoopEnabled {
		t.Errorf("cfg.LoopEnabled = %v, want false (daemonInterval alone must not opt in)", cfg.LoopEnabled)
	}
}

// TestLoadConfigLoopEnabled_DefaultsFalse locks in that with neither
// "loopEnabled" nor a positive "daemonInterval" set, the loop defaults to
// disabled.
func TestLoadConfigLoopEnabled_DefaultsFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dispatch": {"defaultAgent": "codex"}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.LoopEnabled {
		t.Errorf("cfg.LoopEnabled = %v, want false when neither loopEnabled nor daemonInterval is set", cfg.LoopEnabled)
	}
}

// -- PipelineStageGate resolution (#732) -------------------------------------

// TestLoadConfigPipelineStageGate_ExplicitTrue locks in that an explicit
// "pipelineStageGate": true round-trips through LoadConfig unchanged.
func TestLoadConfigPipelineStageGate_ExplicitTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dispatch": {"pipelineStageGate": true}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.PipelineStageGate {
		t.Errorf("cfg.PipelineStageGate = %v, want true", cfg.PipelineStageGate)
	}
}

// TestLoadConfigPipelineStageGate_ExplicitFalse locks in that an explicit
// "pipelineStageGate": false (the kill switch) is honored -- a pointer field
// so an explicit false is distinguishable from unset.
func TestLoadConfigPipelineStageGate_ExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dispatch": {"pipelineStageGate": false}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PipelineStageGate {
		t.Errorf("cfg.PipelineStageGate = %v, want false (explicit opt-out)", cfg.PipelineStageGate)
	}
}

// TestLoadConfigPipelineStageGate_KeyAbsent_DefaultsTrue locks in the
// default-on requirement: an absent "pipelineStageGate" key must resolve to
// true (DefaultConfig()'s value), not false.
func TestLoadConfigPipelineStageGate_KeyAbsent_DefaultsTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dispatch": {"defaultAgent": "codex"}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.PipelineStageGate {
		t.Errorf("cfg.PipelineStageGate = %v, want true (default-on when the key is absent)", cfg.PipelineStageGate)
	}
}

// TestPipelineStageGate_ConfigFalse_DispatchesFinalizedTicket is the AC's
// integration case: a "pipelineStageGate": false config, loaded end to end
// through LoadConfig (not just DefaultConfig()'s Go literal), must dispatch
// a finalized ticket that otherwise passes every other gate.
func TestPipelineStageGate_ConfigFalse_DispatchesFinalizedTicket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dispatch": {"pipelineStageGate": false}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PipelineStageGate {
		t.Fatalf("test setup sanity check: cfg.PipelineStageGate = %v, want false", cfg.PipelineStageGate)
	}

	in := baseInputs()
	in.Config = cfg
	in.Tickets[0].Stage = "finalized"
	in.Tickets[0].StageProbe = StageProbePresent

	got := Decide(in)
	if len(got) != 1 || got[0].Action != ActionDispatch {
		t.Fatalf("Decide with pipelineStageGate=false on a finalized ticket = %+v, want a single dispatch decision", got)
	}
}
