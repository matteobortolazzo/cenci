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

// -- PlanRefined resolution (#828) -------------------------------------------

// TestLoadConfigPlanRefined_KeyAbsent_DefaultsFalse locks in the default-deny
// requirement: an absent "planRefined" key must resolve to false
// (DefaultConfig()'s value).
func TestLoadConfigPlanRefined_KeyAbsent_DefaultsFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dispatch": {"defaultAgent": "codex"}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PlanRefined {
		t.Errorf("cfg.PlanRefined = %v, want false (default-deny when the key is absent)", cfg.PlanRefined)
	}
}

// TestLoadConfigPlanRefined_ExplicitTrue locks in that an explicit
// "planRefined": true round-trips through LoadConfig unchanged.
func TestLoadConfigPlanRefined_ExplicitTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dispatch": {"planRefined": true}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.PlanRefined {
		t.Errorf("cfg.PlanRefined = %v, want true", cfg.PlanRefined)
	}
}

// TestLoadConfigPlanRefined_ExplicitFalse locks in that an explicit
// "planRefined": false round-trips through LoadConfig unchanged -- a pointer
// field so an explicit false is distinguishable from unset, mirroring
// PipelineStageGate.
func TestLoadConfigPlanRefined_ExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dispatch": {"planRefined": false}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PlanRefined {
		t.Errorf("cfg.PlanRefined = %v, want false (explicit opt-out)", cfg.PlanRefined)
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

// -- PlanningAttended lenient dispatch-pass reader (#1086) -------------------

// TestLoadConfigPlanningAttended_KeyAbsent_DefaultsFalse locks in the
// backward-compatible default: no top-level "planning" block at all resolves
// PlanningAttended false and PlanningAttendedUnparseable false -- additive
// only, every pre-#1086 config byte-identical in behavior.
func TestLoadConfigPlanningAttended_KeyAbsent_DefaultsFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dispatch": {"defaultAgent": "codex"}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PlanningAttended {
		t.Errorf("cfg.PlanningAttended = %v, want false when no planning block is present", cfg.PlanningAttended)
	}
	if cfg.PlanningAttendedUnparseable {
		t.Errorf("cfg.PlanningAttendedUnparseable = %v, want false when no planning block is present", cfg.PlanningAttendedUnparseable)
	}
}

// TestLoadConfigPlanningAttended_ExplicitTrue locks in that a well-formed
// "planning": {"attended": true} round-trips cleanly with no unparseable
// flag set.
func TestLoadConfigPlanningAttended_ExplicitTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"planning": {"attended": true}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.PlanningAttended {
		t.Errorf("cfg.PlanningAttended = %v, want true", cfg.PlanningAttended)
	}
	if cfg.PlanningAttendedUnparseable {
		t.Errorf("cfg.PlanningAttendedUnparseable = %v, want false for a well-formed value", cfg.PlanningAttendedUnparseable)
	}
}

// TestLoadConfigPlanningAttended_ExplicitFalse locks in that a well-formed
// "planning": {"attended": false} round-trips cleanly.
func TestLoadConfigPlanningAttended_ExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"planning": {"attended": false}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PlanningAttended {
		t.Errorf("cfg.PlanningAttended = %v, want false", cfg.PlanningAttended)
	}
	if cfg.PlanningAttendedUnparseable {
		t.Errorf("cfg.PlanningAttendedUnparseable = %v, want false for a well-formed value", cfg.PlanningAttendedUnparseable)
	}
}

// TestLoadConfigPlanningAttended_NonBoolAttendedFoldsToTrueAndFlagsUnparseable
// covers the ticket's headline lenient-reader AC: a non-bool "attended"
// value (e.g. the string "yes") must never fail the whole config load --
// LoadConfig succeeds, PlanningAttended resolves to true (the restrictive,
// safe direction), and PlanningAttendedUnparseable records that the value
// was not trustworthy so RunOnce can log exactly one line naming
// planning.attended.
func TestLoadConfigPlanningAttended_NonBoolAttendedFoldsToTrueAndFlagsUnparseable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"planning": {"attended": "yes"}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned an error for a non-bool planning.attended, want success: %v", err)
	}
	if !cfg.PlanningAttended {
		t.Errorf("cfg.PlanningAttended = %v, want true (unparseable folds to the restrictive direction)", cfg.PlanningAttended)
	}
	if !cfg.PlanningAttendedUnparseable {
		t.Error("cfg.PlanningAttendedUnparseable = false, want true for a non-bool attended value")
	}
}

// TestLoadConfigPlanningAttended_NullAttendedFoldsToTrueAndFlagsUnparseable
// covers the sibling AC case: "attended" is a literal JSON null. Go's
// encoding/json treats unmarshaling null into a non-pointer bool as a
// successful no-op (leaves it at zero value false), so without an explicit
// null guard this would silently resolve to (false, false) -- identical to
// no "planning" block at all -- instead of folding to the restrictive
// direction like every other unparseable value.
func TestLoadConfigPlanningAttended_NullAttendedFoldsToTrueAndFlagsUnparseable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"planning": {"attended": null}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned an error for a null planning.attended, want success: %v", err)
	}
	if !cfg.PlanningAttended {
		t.Errorf("cfg.PlanningAttended = %v, want true (null attended folds to the restrictive direction)", cfg.PlanningAttended)
	}
	if !cfg.PlanningAttendedUnparseable {
		t.Error("cfg.PlanningAttendedUnparseable = false, want true for a null attended value")
	}
}

// TestLoadConfigPlanningAttended_NonObjectPlanningFoldsToTrueAndFlagsUnparseable
// covers the sibling AC case: "planning" itself is not even an object (a bare
// number). LoadConfig must still succeed, PlanningAttended resolves true, and
// PlanningAttendedUnparseable is set.
func TestLoadConfigPlanningAttended_NonObjectPlanningFoldsToTrueAndFlagsUnparseable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"planning": 3}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned an error for a non-object planning block, want success: %v", err)
	}
	if !cfg.PlanningAttended {
		t.Errorf("cfg.PlanningAttended = %v, want true (unparseable folds to the restrictive direction)", cfg.PlanningAttended)
	}
	if !cfg.PlanningAttendedUnparseable {
		t.Error("cfg.PlanningAttendedUnparseable = false, want true for a non-object planning block")
	}
}

// TestLoadConfigPlanningAttended_DispatchBlockMergeUnaffected covers the AC
// that a malformed planning block in the same file must never perturb the
// unrelated dispatch block's ordinary merge.
func TestLoadConfigPlanningAttended_DispatchBlockMergeUnaffected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{
  "planning": {"attended": "yes"},
  "dispatch": {"defaultAgent": "codex", "concurrencyCap": 7}
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.DefaultAgent != "codex" {
		t.Errorf("cfg.DefaultAgent = %q, want %q (dispatch block merge unaffected by a malformed planning block)", cfg.DefaultAgent, "codex")
	}
	if cfg.ConcurrencyCap != 7 {
		t.Errorf("cfg.ConcurrencyCap = %d, want 7 (dispatch block merge unaffected by a malformed planning block)", cfg.ConcurrencyCap)
	}
	if !cfg.PlanningAttended || !cfg.PlanningAttendedUnparseable {
		t.Errorf("cfg.PlanningAttended = %v, cfg.PlanningAttendedUnparseable = %v, want (true, true)", cfg.PlanningAttended, cfg.PlanningAttendedUnparseable)
	}
}

// TestLoadConfigPlanningAttended_WholeFileCorruptionStillErrors covers the
// constraint that the new lenient planning reader must never widen
// reloadConfig's existing tick-skip abort path: whole-file JSON corruption
// keeps failing LoadConfig exactly as before this ticket.
func TestLoadConfigPlanningAttended_WholeFileCorruptionStillErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	if _, err := LoadConfig(path); err == nil {
		t.Error("LoadConfig on malformed whole-file JSON returned nil error, want error (unchanged reloadConfig tick-skip behavior)")
	}
}
