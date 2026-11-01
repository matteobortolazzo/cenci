package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

// -- dispatch.memoryHeadroom (#1290) ----------------------------------------

// writeMemoryHeadroomConfig writes a config.json whose dispatch block carries
// the given raw JSON value under "memoryHeadroom" (or no such key when raw is
// empty) and returns its path.
func writeMemoryHeadroomConfig(t *testing.T, raw string) string {
	t.Helper()
	body := `{"dispatch": {"concurrencyCap": 2}}`
	if raw != "" {
		body = `{"dispatch": {"concurrencyCap": 2, "memoryHeadroom": ` + raw + `}}`
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

// TestLoadConfig_MemoryHeadroom_DefaultIs25: an unset key means 25 -- whether
// the dispatch block omits it, there is no dispatch block, or there is no
// config file at all -- so existing fleets get the gate on upgrade.
func TestLoadConfig_MemoryHeadroom_DefaultIs25(t *testing.T) {
	if got := DefaultConfig().MemoryHeadroom; got != 25 {
		t.Errorf("DefaultConfig().MemoryHeadroom = %d, want 25", got)
	}

	noKey := writeMemoryHeadroomConfig(t, "")
	noDispatchBlock := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(noDispatchBlock, []byte(`{"planning": {"attended": false}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	missingFile := filepath.Join(t.TempDir(), "absent.json")

	for name, path := range map[string]string{
		"dispatch block without the key": noKey,
		"no dispatch block":              noDispatchBlock,
		"no config file":                 missingFile,
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.MemoryHeadroom != 25 {
				t.Errorf("cfg.MemoryHeadroom = %d, want the default 25", cfg.MemoryHeadroom)
			}
		})
	}
}

// TestLoadConfig_MemoryHeadroom_Explicit40: AC2, a configured value moves the
// threshold, and the merge does not disturb neighbouring keys.
func TestLoadConfig_MemoryHeadroom_Explicit40(t *testing.T) {
	cfg, err := LoadConfig(writeMemoryHeadroomConfig(t, "40"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.MemoryHeadroom != 40 {
		t.Errorf("cfg.MemoryHeadroom = %d, want 40", cfg.MemoryHeadroom)
	}
	if cfg.ConcurrencyCap != 2 {
		t.Errorf("cfg.ConcurrencyCap = %d, want 2 (neighbouring key unaffected)", cfg.ConcurrencyCap)
	}
}

// TestLoadConfig_MemoryHeadroom_ZeroAccepted: an explicit 0 disables the gate
// and must not be mistaken for "unset" (which would resolve to 25).
func TestLoadConfig_MemoryHeadroom_ZeroAccepted(t *testing.T) {
	cfg, err := LoadConfig(writeMemoryHeadroomConfig(t, "0"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.MemoryHeadroom != 0 {
		t.Errorf("cfg.MemoryHeadroom = %d, want 0 (explicit zero disables the gate)", cfg.MemoryHeadroom)
	}
}

// TestLoadConfig_MemoryHeadroom_AcceptsUpperBound: 100 is the largest valid
// percent.
func TestLoadConfig_MemoryHeadroom_AcceptsUpperBound(t *testing.T) {
	cfg, err := LoadConfig(writeMemoryHeadroomConfig(t, "100"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.MemoryHeadroom != 100 {
		t.Errorf("cfg.MemoryHeadroom = %d, want 100", cfg.MemoryHeadroom)
	}
}

// TestLoadConfig_MemoryHeadroom_RejectsInvalid: AC2, a non-integer, null,
// negative or >100 value is an error that names the key and the config path --
// never a silent default.
func TestLoadConfig_MemoryHeadroom_RejectsInvalid(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"fractional", `40.5`},
		{"string", `"40"`},
		{"null", `null`},
		{"negative", `-1`},
		{"above 100", `101`},
		{"boolean", `true`},
		{"array", `[40]`},
		{"object", `{"percent": 40}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeMemoryHeadroomConfig(t, tc.raw)
			_, err := LoadConfig(path)
			if err == nil {
				t.Fatalf("LoadConfig with memoryHeadroom %s returned nil error, want an error", tc.raw)
			}
			if !strings.Contains(err.Error(), "memoryHeadroom") {
				t.Errorf("error %q must name the key memoryHeadroom", err)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q must name the config path %s", err, path)
			}
		})
	}
}

// -- dispatch.oomRetryBudget / dispatch.oomBackoff (#1291) -------------------

// writeOOMConfig writes a config.json whose dispatch block carries the given
// raw JSON values under oomRetryBudget and oomBackoff (a key is left out when
// its raw value is empty) and returns its path.
func writeOOMConfig(t *testing.T, budgetRaw, backoffRaw string) string {
	t.Helper()
	body := `{"dispatch": {"concurrencyCap": 2`
	if budgetRaw != "" {
		body += `, "oomRetryBudget": ` + budgetRaw
	}
	if backoffRaw != "" {
		body += `, "oomBackoff": ` + backoffRaw
	}
	body += `}}`
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

// TestLoadConfig_OOM_DefaultsApplyWhenUnset: an unset key means oomRetryBudget
// 3 and oomBackoff 10m -- whether the dispatch block omits them, there is no
// dispatch block, or there is no config file -- so existing fleets get the
// behavior on upgrade.
func TestLoadConfig_OOM_DefaultsApplyWhenUnset(t *testing.T) {
	const (
		wantBudget  = 3
		wantBackoff = 10 * time.Minute
	)
	def := DefaultConfig()
	if def.OOMRetryBudget != wantBudget || def.OOMBackoff != wantBackoff {
		t.Errorf("DefaultConfig() = (%d, %s), want (%d, %s)", def.OOMRetryBudget, def.OOMBackoff, wantBudget, wantBackoff)
	}

	noDispatchBlock := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(noDispatchBlock, []byte(`{"planning": {"attended": false}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	paths := map[string]string{
		"dispatch block without the keys": writeOOMConfig(t, "", ""),
		"no dispatch block":               noDispatchBlock,
		"no config file":                  filepath.Join(t.TempDir(), "absent.json"),
	}
	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.OOMRetryBudget != wantBudget {
				t.Errorf("cfg.OOMRetryBudget = %d, want the default %d", cfg.OOMRetryBudget, wantBudget)
			}
			if cfg.OOMBackoff != wantBackoff {
				t.Errorf("cfg.OOMBackoff = %s, want the default %s", cfg.OOMBackoff, wantBackoff)
			}
		})
	}
}

// TestLoadConfig_OOM_ExplicitValues: configured values win over the defaults,
// an explicit 0 / "0" differs from unset (0 sends the first OOM recovery
// straight to failed; "0" disables the backoff), and setting one key leaves
// the other at its default and the neighbouring keys untouched.
func TestLoadConfig_OOM_ExplicitValues(t *testing.T) {
	cases := []struct {
		name                  string
		budgetRaw, backoffRaw string
		wantBudget            int
		wantBackoff           time.Duration
	}{
		{"both set", `5`, `"30m"`, 5, 30 * time.Minute},
		{"budget only", `1`, ``, 1, 10 * time.Minute},
		{"backoff only", ``, `"90s"`, 3, 90 * time.Second},
		{"explicit zero budget is not unset", `0`, ``, 0, 10 * time.Minute},
		{`"0" backoff disables and is not unset`, ``, `"0"`, 3, 0},
		{"both explicit zero", `0`, `"0"`, 0, 0},
		{"zero with a unit", ``, `"0s"`, 3, 0},
		{"compound duration", ``, `"1h30m"`, 3, 90 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadConfig(writeOOMConfig(t, tc.budgetRaw, tc.backoffRaw))
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.OOMRetryBudget != tc.wantBudget {
				t.Errorf("cfg.OOMRetryBudget = %d, want %d", cfg.OOMRetryBudget, tc.wantBudget)
			}
			if cfg.OOMBackoff != tc.wantBackoff {
				t.Errorf("cfg.OOMBackoff = %s, want %s", cfg.OOMBackoff, tc.wantBackoff)
			}
			if cfg.ConcurrencyCap != 2 {
				t.Errorf("cfg.ConcurrencyCap = %d, want 2 (neighbouring key unaffected)", cfg.ConcurrencyCap)
			}
		})
	}
}

// TestLoadConfig_OOM_RejectsInvalid: AC8, a negative or non-integer
// oomRetryBudget, or a malformed or negative oomBackoff, is a config error that
// names the key and the config path -- never a silent default. A JSON null is
// rejected too (it would otherwise read as unset).
func TestLoadConfig_OOM_RejectsInvalid(t *testing.T) {
	cases := []struct{ name, key, raw string }{
		{"budget negative", "oomRetryBudget", `-1`},
		{"budget fractional", "oomRetryBudget", `1.5`},
		{"budget string", "oomRetryBudget", `"3"`},
		{"budget null", "oomRetryBudget", `null`},
		{"budget boolean", "oomRetryBudget", `true`},
		{"budget array", "oomRetryBudget", `[3]`},
		{"budget object", "oomRetryBudget", `{"n": 3}`},
		{"backoff malformed", "oomBackoff", `"soon"`},
		{"backoff missing unit", "oomBackoff", `"10"`},
		{"backoff empty string", "oomBackoff", `""`},
		{"backoff negative", "oomBackoff", `"-5m"`},
		{"backoff null", "oomBackoff", `null`},
		{"backoff bare number", "oomBackoff", `10`},
		{"backoff boolean", "oomBackoff", `false`},
		{"backoff array", "oomBackoff", `["10m"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			if tc.key == "oomRetryBudget" {
				path = writeOOMConfig(t, tc.raw, "")
			} else {
				path = writeOOMConfig(t, "", tc.raw)
			}
			_, err := LoadConfig(path)
			if err == nil {
				t.Fatalf("LoadConfig with %s %s returned nil error, want an error", tc.key, tc.raw)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error %q must name the key %s", err, tc.key)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q must name the config path %s", err, path)
			}
		})
	}
}

// -- dispatch.repos[].filters (#1385) -------------------------------------------

// TestLoadConfig_Filters_OldConfigKeepsCurrentBehavior: AC1, a config written
// before filters existed loads with no repo restricted, and a config with no
// repos at all still has none.
func TestLoadConfig_Filters_OldConfigKeepsCurrentBehavior(t *testing.T) {
	old := `{"dispatch": {"concurrencyCap": 4, "repos": [
		{"repo": "owner/a", "dir": "/a", "session": "s"},
		{"repo": "owner/b", "dir": "/b", "session": "s"}
	]}}`
	cfg, err := LoadConfig(writeFilterConfig(t, old))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Repos) != 2 || cfg.ConcurrencyCap != 4 {
		t.Fatalf("old config loaded as %+v, want 2 repos and cap 4", cfg)
	}
	for _, rc := range cfg.Repos {
		if rc.Filter.Restricted() {
			t.Errorf("repo %s without filters loaded as restricted: %+v", rc.Repo, rc.Filter)
		}
		if len(rc.Filters) != 0 {
			t.Errorf("repo %s without filters carries raw filters %s", rc.Repo, rc.Filters)
		}
	}

	noRepos, err := LoadConfig(writeFilterConfig(t, `{"dispatch": {"concurrencyCap": 4}}`))
	if err != nil {
		t.Fatalf("LoadConfig (no repos): %v", err)
	}
	if len(noRepos.Repos) != 0 {
		t.Errorf("config without repos loaded %d repos", len(noRepos.Repos))
	}
}

// TestLoadConfig_Filters_IndependentRepoEntries: each repo entry owns its
// filters; one repo's restriction never leaks to its neighbours.
func TestLoadConfig_Filters_IndependentRepoEntries(t *testing.T) {
	body := `{"dispatch": {"repos": [
		{"repo": "owner/a", "dir": "/a", "session": "s", "filters": {"milestones": [3]}},
		{"repo": "owner/b", "dir": "/b", "session": "s"},
		{"repo": "owner/c", "dir": "/c", "session": "s", "filters": {}},
		{"repo": "owner/d", "dir": "/d", "session": "s", "filters": {"labelsNone": ["manual-only"]}}
	]}}`
	cfg, err := LoadConfig(writeFilterConfig(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := FilterForRepo(cfg.Repos, "owner/a"); len(got.Milestones) != 1 || got.Milestones[0] != 3 || len(got.LabelsNone) != 0 {
		t.Errorf("repo a filter = %+v, want milestones [3] only", got)
	}
	if FilterForRepo(cfg.Repos, "owner/b").Restricted() {
		t.Error("repo b (no filters) must be unrestricted")
	}
	if FilterForRepo(cfg.Repos, "owner/c").Restricted() {
		t.Error("repo c (empty filters object) must be unrestricted")
	}
	if got := FilterForRepo(cfg.Repos, "owner/d"); len(got.LabelsNone) != 1 || len(got.Milestones) != 0 {
		t.Errorf("repo d filter = %+v, want labelsNone only", got)
	}
}

// TestLoadConfig_Filters_TwoConfigFilesSelectDifferentMilestones: AC1, the file
// named by the path (--config) is the one read, so two machines sharing a repo
// and an account select different queues.
func TestLoadConfig_Filters_TwoConfigFilesSelectDifferentMilestones(t *testing.T) {
	machineA := writeFilterConfig(t, filterConfigBody(`{"milestones": [3]}`))
	machineB := writeFilterConfig(t, filterConfigBody(`{"milestones": [7]}`))

	cfgA, err := LoadConfig(machineA)
	if err != nil {
		t.Fatalf("LoadConfig(A): %v", err)
	}
	cfgB, err := LoadConfig(machineB)
	if err != nil {
		t.Fatalf("LoadConfig(B): %v", err)
	}
	if cfgA.Path != machineA || cfgB.Path != machineB {
		t.Errorf("Config.Path = %q / %q, want the selected files %q / %q", cfgA.Path, cfgB.Path, machineA, machineB)
	}
	a := FilterForRepo(cfgA.Repos, "owner/name")
	b := FilterForRepo(cfgB.Repos, "owner/name")
	if len(a.Milestones) != 1 || a.Milestones[0] != 3 {
		t.Errorf("machine A milestones = %v, want [3]", a.Milestones)
	}
	if len(b.Milestones) != 1 || b.Milestones[0] != 7 {
		t.Errorf("machine B milestones = %v, want [7]", b.Milestones)
	}
	ticket3 := milestoneTicket("owner/name", 3)
	ticket7 := milestoneTicket("owner/name", 7)
	if _, skipped := filterSkip(a, ticket3); skipped {
		t.Error("machine A must take the milestone-3 ticket")
	}
	if _, skipped := filterSkip(a, ticket7); !skipped {
		t.Error("machine A must not take the milestone-7 ticket")
	}
	if _, skipped := filterSkip(b, ticket7); skipped {
		t.Error("machine B must take the milestone-7 ticket")
	}
	if _, skipped := filterSkip(b, ticket3); !skipped {
		t.Error("machine B must not take the milestone-3 ticket")
	}
}

// TestLoadConfig_Filters_CommittedRepoConfigIsIgnored: filters are read only
// from the selected user config, never from a repo's committed
// .cenci/config.json -- not even one sitting in the enrolled directory.
func TestLoadConfig_Filters_CommittedRepoConfigIsIgnored(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".cenci"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	committed := `{"dispatch": {"repos": [{"repo": "owner/name", "dir": "` + repoDir + `", "session": "s", "filters": {"milestones": [3], "labelsAll": ["x"]}}]}}`
	if err := os.WriteFile(filepath.Join(repoDir, ".cenci", "config.json"), []byte(committed), 0o600); err != nil {
		t.Fatalf("writing committed config: %v", err)
	}
	user := `{"dispatch": {"repos": [{"repo": "owner/name", "dir": "` + repoDir + `", "session": "s"}]}}`
	cfg, err := LoadConfig(writeFilterConfig(t, user))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if FilterForRepo(cfg.Repos, "owner/name").Restricted() {
		t.Error("a committed .cenci/config.json filters block must not restrict the repo")
	}

	// An invalid committed block must not fail the user config load either.
	bad := `{"dispatch": {"repos": [{"repo": "owner/name", "filters": null}]}}`
	if err := os.WriteFile(filepath.Join(repoDir, ".cenci", "config.json"), []byte(bad), 0o600); err != nil {
		t.Fatalf("writing bad committed config: %v", err)
	}
	if _, err := LoadConfig(writeFilterConfig(t, user)); err != nil {
		t.Errorf("an invalid committed filters block failed the user config load: %v", err)
	}
}

// TestLoadConfig_Filters_MissingConfigFileIsUnrestricted: no file at all is not
// an error and restricts nothing, as before.
func TestLoadConfig_Filters_MissingConfigFileIsUnrestricted(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if FilterForRepo(cfg.Repos, "owner/name").Restricted() {
		t.Error("a missing config must leave every repo unrestricted")
	}
}

// TestLoadConfig_Filters_FailedLoadReturnsUnmergedDefaults: a bad
// filters block returns the unmerged defaults (no repos), so a caller that
// ignores the error cannot read a half-validated, possibly widened repo list.
func TestLoadConfig_Filters_FailedLoadReturnsUnmergedDefaults(t *testing.T) {
	body := `{"dispatch": {"concurrencyCap": 9, "dailyQuota": 99, "repos": [
		{"repo": "owner/a", "dir": "/a", "session": "s", "filters": {"milestones": [3]}},
		{"repo": "owner/b", "dir": "/b", "session": "s", "filters": {"milestone": [3]}}
	]}}`
	cfg, err := LoadConfig(writeFilterConfig(t, body))
	if err == nil {
		t.Fatal("LoadConfig returned nil error for an unknown filters key")
	}
	def := DefaultConfig()
	if len(cfg.Repos) != 0 || cfg.ConcurrencyCap != def.ConcurrencyCap || cfg.DailyQuota != def.DailyQuota {
		t.Errorf("failed load returned repos=%d cap=%d quota=%d, want the unmerged defaults (0, %d, %d)",
			len(cfg.Repos), cfg.ConcurrencyCap, cfg.DailyQuota, def.ConcurrencyCap, def.DailyQuota)
	}
}

// TestLoadConfig_Filters_RawFiltersRoundTripAndResolvedFilterNeverMarshals: the
// raw bytes survive a Marshal (so enroll/toggle write-backs keep them), an
// unfiltered entry gains no filters key, and the resolved form is not
// serialized.
func TestLoadConfig_Filters_RawFiltersRoundTripAndResolvedFilterNeverMarshals(t *testing.T) {
	cfg, err := LoadConfig(writeFilterConfig(t, filterConfigBody(`{"milestones": [3, 3], "labelsAll": [" Area:Watch "]}`)))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	rc := cfg.Repos[0]
	out, err := json.Marshal(rc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// Marshal compacts insignificant whitespace (the config writer re-indents the
	// whole file anyway), but the values must stay the user's: the duplicate 3
	// and the padded label are not normalized away on write-back.
	if string(back["filters"]) != `{"milestones":[3,3],"labelsAll":[" Area:Watch "]}` {
		t.Errorf("marshaled filters = %s, want the user's values unnormalized", back["filters"])
	}
	if _, ok := back["Filter"]; ok {
		t.Errorf("the resolved Filter must not marshal, got %s", out)
	}

	plain, err := json.Marshal(RepoConfig{Repo: "owner/x", Dir: "/x", Session: "s"})
	if err != nil {
		t.Fatalf("Marshal plain: %v", err)
	}
	if strings.Contains(string(plain), "ilter") {
		t.Errorf("an unfiltered repo entry marshaled a filters key: %s", plain)
	}
}

// TestFilterDecideParityWithUnfilteredConfig: AC1, a config whose filters are
// absent, {} or only empty lists makes Decide return decisions identical to the
// unfiltered config for the same inputs.
func TestFilterDecideParityWithUnfilteredConfig(t *testing.T) {
	load := func(rawFilters string) Config {
		cfg, err := LoadConfig(writeFilterConfig(t, `{"dispatch": {"repos": [{"repo": "o/r", "dir": "/r", "session": "s"`+rawFilters+`}]}}`))
		if err != nil {
			t.Fatalf("LoadConfig(%q): %v", rawFilters, err)
		}
		return cfg
	}
	decide := func(cfg Config) []Decision {
		in := baseInputs()
		in.Config = cfg
		return Decide(in)
	}

	want := decide(load(``))
	if len(want) != 1 || want[0].Action != ActionDispatch {
		t.Fatalf("unfiltered baseline = %+v, want one dispatch", want)
	}
	for name, raw := range map[string]string{
		"empty object": `, "filters": {}`,
		"empty lists":  `, "filters": {"milestones": [], "labelsAll": [], "labelsAny": [], "labelsNone": []}`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := decide(load(raw)); !reflect.DeepEqual(got, want) {
				t.Errorf("decisions differ from the unfiltered config:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}
