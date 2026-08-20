package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// -- SetAutomergeEnabled / QueryAutomergeEnabled ---------------------------

// rawAutomerge decodes just the top-level automerge block straight off disk,
// so assertions target the literal persisted JSON (same idiom as
// rawDaemonLoop in loop_test.go).
type rawAutomerge struct {
	Automerge map[string]json.RawMessage `json:"automerge"`
}

func mustDecodeRawAutomerge(t *testing.T, path string) rawAutomerge {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var got rawAutomerge
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decoding %s: %v\ncontent:\n%s", path, err, data)
	}
	return got
}

func automergeEnabledOnDisk(t *testing.T, path string) *bool {
	t.Helper()
	raw := mustDecodeRawAutomerge(t, path)
	enabledRaw, ok := raw.Automerge["enabled"]
	if !ok {
		return nil
	}
	var enabled bool
	if err := json.Unmarshal(enabledRaw, &enabled); err != nil {
		t.Fatalf("decoding automerge.enabled: %v", err)
	}
	return &enabled
}

// TestSetAutomergeEnabled_CreatesFileAndParentDir locks in that arming the
// fleet switch on a machine with no config file yet creates the file (and
// its parent directory) rather than erroring — the first-run path for a
// fresh install.
func TestSetAutomergeEnabled_CreatesFileAndParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cenci", "config.json")

	if err := SetAutomergeEnabled(path, true); err != nil {
		t.Fatalf("SetAutomergeEnabled: %v", err)
	}

	got := automergeEnabledOnDisk(t, path)
	if got == nil || !*got {
		t.Errorf("automerge.enabled on disk = %v, want true", got)
	}
}

// TestSetAutomergeEnabled_PreservesUnrelatedAndSiblingKeys locks in the
// raw-map read-modify-write contract shared with SetLoopEnabled/EnrollRepo:
// toggling automerge.enabled must preserve unrelated top-level blocks
// (dispatch.repos) and any sibling key inside the automerge block verbatim.
func TestSetAutomergeEnabled_PreservesUnrelatedAndSiblingKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{
  "defaultAgent": "claude",
  "dispatch": {
    "repos": [{"repo": "owner/name", "dir": "/abs/dir"}]
  },
  "automerge": {
    "enabled": false,
    "note": "hand-written"
  }
}`)

	if err := SetAutomergeEnabled(path, true); err != nil {
		t.Fatalf("SetAutomergeEnabled: %v", err)
	}

	cfg := mustDecodeConfig(t, path)
	if cfg.DefaultAgent != "claude" {
		t.Errorf("defaultAgent = %q, want preserved %q", cfg.DefaultAgent, "claude")
	}
	if !reposContains(cfg.Dispatch.Repos, "owner/name", "/abs/dir") {
		t.Errorf("dispatch.repos = %+v, want preserved entry owner/name -> /abs/dir", cfg.Dispatch.Repos)
	}

	raw := mustDecodeRawAutomerge(t, path)
	var note string
	if noteRaw, ok := raw.Automerge["note"]; !ok {
		t.Errorf("automerge.note sibling key dropped, got automerge block %v", raw.Automerge)
	} else if err := json.Unmarshal(noteRaw, &note); err != nil || note != "hand-written" {
		t.Errorf("automerge.note = %q (err %v), want preserved %q", note, err, "hand-written")
	}
	got := automergeEnabledOnDisk(t, path)
	if got == nil || !*got {
		t.Errorf("automerge.enabled on disk = %v, want true", got)
	}

	// Turning it back off flips only the switch, still preserving siblings.
	if err := SetAutomergeEnabled(path, false); err != nil {
		t.Fatalf("SetAutomergeEnabled(false): %v", err)
	}
	got = automergeEnabledOnDisk(t, path)
	if got == nil || *got {
		t.Errorf("automerge.enabled on disk after off = %v, want false", got)
	}
	raw = mustDecodeRawAutomerge(t, path)
	if _, ok := raw.Automerge["note"]; !ok {
		t.Errorf("automerge.note sibling key dropped by off, got automerge block %v", raw.Automerge)
	}
}

// TestQueryAutomergeEnabled locks in the strict read semantics, mirroring
// babysit's loadFleetSwitch: only a literal "enabled": true reads as
// enabled; a missing file or absent key is disabled, not an error; malformed
// JSON is an error the caller must see (a status command must never render a
// broken config as a confident "disabled").
func TestQueryAutomergeEnabled(t *testing.T) {
	t.Run("missing file is disabled", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		enabled, err := QueryAutomergeEnabled(path)
		if err != nil {
			t.Fatalf("QueryAutomergeEnabled: %v", err)
		}
		if enabled {
			t.Error("enabled = true for a missing file, want false")
		}
	})
	t.Run("absent key is disabled", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, `{"dispatch": {}}`)
		enabled, err := QueryAutomergeEnabled(path)
		if err != nil || enabled {
			t.Errorf("QueryAutomergeEnabled = (%v, %v), want (false, nil)", enabled, err)
		}
	})
	t.Run("explicit true is enabled", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, `{"automerge": {"enabled": true}}`)
		enabled, err := QueryAutomergeEnabled(path)
		if err != nil || !enabled {
			t.Errorf("QueryAutomergeEnabled = (%v, %v), want (true, nil)", enabled, err)
		}
	})
	t.Run("malformed file is an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, `{not json`)
		if _, err := QueryAutomergeEnabled(path); err == nil {
			t.Error("QueryAutomergeEnabled on malformed JSON returned nil error, want error")
		}
	})
}

// -- SetPlanRefined / QueryPlanRefined -------------------------------------

// rawPlanRefined decodes just dispatch.planRefined straight off disk.
type rawPlanRefined struct {
	Dispatch struct {
		PlanRefined *bool `json:"planRefined"`
	} `json:"dispatch"`
}

func planRefinedOnDisk(t *testing.T, path string) *bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var got rawPlanRefined
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decoding %s: %v\ncontent:\n%s", path, err, data)
	}
	return got.Dispatch.PlanRefined
}

// TestSetPlanRefined_PreservesDispatchKeys locks in that toggling
// dispatch.planRefined preserves every other key in the dispatch block
// (repos, concurrencyCap, loopEnabled) and unrelated top-level blocks, and
// that a fresh file is created when none exists.
func TestSetPlanRefined_PreservesDispatchKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{
  "run": {"template": "custom-template"},
  "dispatch": {
    "repos": [{"repo": "owner/name", "dir": "/abs/dir"}],
    "concurrencyCap": 7,
    "loopEnabled": true
  }
}`)

	if err := SetPlanRefined(path, true); err != nil {
		t.Fatalf("SetPlanRefined: %v", err)
	}

	got := planRefinedOnDisk(t, path)
	if got == nil || !*got {
		t.Errorf("dispatch.planRefined on disk = %v, want true", got)
	}
	cfg := mustDecodeConfig(t, path)
	if cfg.Dispatch.ConcurrencyCap == nil || *cfg.Dispatch.ConcurrencyCap != 7 {
		t.Errorf("dispatch.concurrencyCap = %v, want preserved 7", cfg.Dispatch.ConcurrencyCap)
	}
	if !reposContains(cfg.Dispatch.Repos, "owner/name", "/abs/dir") {
		t.Errorf("dispatch.repos = %+v, want preserved entry owner/name -> /abs/dir", cfg.Dispatch.Repos)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig after SetPlanRefined: %v", err)
	}
	if !loaded.LoopEnabled {
		t.Error("dispatch.loopEnabled not preserved through SetPlanRefined")
	}
	if !loaded.PlanRefined {
		t.Error("LoadConfig.PlanRefined = false after SetPlanRefined(true), want true")
	}

	if err := SetPlanRefined(path, false); err != nil {
		t.Fatalf("SetPlanRefined(false): %v", err)
	}
	got = planRefinedOnDisk(t, path)
	if got == nil || *got {
		t.Errorf("dispatch.planRefined on disk after off = %v, want false", got)
	}
}

// TestSetPlanRefined_CreatesFile locks in the fresh-install path.
func TestSetPlanRefined_CreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cenci", "config.json")
	if err := SetPlanRefined(path, true); err != nil {
		t.Fatalf("SetPlanRefined: %v", err)
	}
	got := planRefinedOnDisk(t, path)
	if got == nil || !*got {
		t.Errorf("dispatch.planRefined on disk = %v, want true", got)
	}
}

// TestQueryPlanRefined mirrors TestQueryAutomergeEnabled's semantics for the
// planning-pickup switch.
func TestQueryPlanRefined(t *testing.T) {
	t.Run("missing file is disabled", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		enabled, err := QueryPlanRefined(path)
		if err != nil || enabled {
			t.Errorf("QueryPlanRefined = (%v, %v), want (false, nil)", enabled, err)
		}
	})
	t.Run("explicit true is enabled", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, `{"dispatch": {"planRefined": true}}`)
		enabled, err := QueryPlanRefined(path)
		if err != nil || !enabled {
			t.Errorf("QueryPlanRefined = (%v, %v), want (true, nil)", enabled, err)
		}
	})
	t.Run("malformed file is an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, `{not json`)
		if _, err := QueryPlanRefined(path); err == nil {
			t.Error("QueryPlanRefined on malformed JSON returned nil error, want error")
		}
	})
}

// -- SummarizeRepoPolicy ---------------------------------------------------

// TestSummarizeRepoPolicy_Monorepo locks in the per-scope summary over a
// monorepo .cenci/config.json: a valid project block reports its caps, a
// project with no block reports absent, a block missing a required cap
// reports malformed (babysit denies it), and the top-level scope is always
// reported so "PRs outside every project deny" is visible.
func TestSummarizeRepoPolicy_Monorepo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".cenci", "config.json"), `{
  "isMonorepo": true,
  "projects": [
    {
      "slug": "api",
      "path": "packages/api",
      "automerge": {
        "protectedPaths": ["*secret*", "packages/api/internal/"],
        "maxChangedFiles": 8,
        "maxDiffLines": 400,
        "mergeMethod": "squash"
      }
    },
    {"slug": "web", "path": "apps/web"},
    {
      "slug": "bad",
      "path": "packages/bad",
      "automerge": {"protectedPaths": ["*"], "maxChangedFiles": 8}
    }
  ]
}`)

	sum, err := SummarizeRepoPolicy(dir)
	if err != nil {
		t.Fatalf("SummarizeRepoPolicy: %v", err)
	}
	if !sum.ConfigPresent {
		t.Fatal("ConfigPresent = false, want true")
	}

	byScope := map[string]AutomergePolicyScope{}
	for _, s := range sum.Scopes {
		byScope[s.Scope] = s
	}

	api, ok := byScope["api"]
	if !ok {
		t.Fatalf("no scope %q in %+v", "api", sum.Scopes)
	}
	if !api.Present || api.Malformed {
		t.Errorf("api scope = %+v, want present and well-formed", api)
	}
	if api.MaxChangedFiles != 8 || api.MaxDiffLines != 400 || api.MergeMethod != "squash" || api.ProtectedPaths != 2 {
		t.Errorf("api scope = %+v, want caps 8/400 squash with 2 protected patterns", api)
	}

	if web, ok := byScope["web"]; !ok || web.Present {
		t.Errorf("web scope = %+v (found %v), want present=false", web, ok)
	}

	bad, ok := byScope["bad"]
	if !ok || !bad.Present || !bad.Malformed {
		t.Errorf("bad scope = %+v (found %v), want present and malformed (missing maxDiffLines)", bad, ok)
	}

	if top, ok := byScope[""]; !ok || top.Present {
		t.Errorf("top-level scope = %+v (found %v), want reported with present=false", top, ok)
	}
}

// TestSummarizeRepoPolicy_SingleRepoTopLevel locks in the single-repo shape:
// a top-level automerge block reports as the "" scope; mergeMethod defaults
// to squash when unset (matching babysit's documented default).
func TestSummarizeRepoPolicy_SingleRepoTopLevel(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".cenci", "config.json"), `{
  "automerge": {
    "protectedPaths": ["*.github/workflows/*"],
    "maxChangedFiles": 25,
    "maxDiffLines": 800
  }
}`)

	sum, err := SummarizeRepoPolicy(dir)
	if err != nil {
		t.Fatalf("SummarizeRepoPolicy: %v", err)
	}
	if len(sum.Scopes) != 1 {
		t.Fatalf("scopes = %+v, want exactly the top-level scope", sum.Scopes)
	}
	top := sum.Scopes[0]
	if top.Scope != "" || !top.Present || top.Malformed {
		t.Errorf("top-level scope = %+v, want present and well-formed", top)
	}
	if top.MaxChangedFiles != 25 || top.MaxDiffLines != 800 || top.MergeMethod != "squash" || top.ProtectedPaths != 1 {
		t.Errorf("top-level scope = %+v, want caps 25/800, defaulted squash, 1 pattern", top)
	}
}

// TestSummarizeRepoPolicy_AbsentAndMalformed locks in the edges: no
// .cenci/config.json reports ConfigPresent=false without error; a malformed
// file is an error, never a confident summary.
func TestSummarizeRepoPolicy_AbsentAndMalformed(t *testing.T) {
	t.Run("absent config", func(t *testing.T) {
		sum, err := SummarizeRepoPolicy(t.TempDir())
		if err != nil {
			t.Fatalf("SummarizeRepoPolicy: %v", err)
		}
		if sum.ConfigPresent {
			t.Errorf("ConfigPresent = true with no .cenci/config.json, want false")
		}
	})
	t.Run("malformed config", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".cenci", "config.json"), `{not json`)
		if _, err := SummarizeRepoPolicy(dir); err == nil {
			t.Error("SummarizeRepoPolicy on malformed JSON returned nil error, want error")
		}
	})
}

// -- SetPlanningAttended / QueryPlanningAttended (#1086) --------------------

// rawPlanningBlock decodes just the top-level planning block straight off
// disk, so assertions target the literal persisted JSON (same idiom as
// rawAutomerge/rawPlanRefined above).
type rawPlanningBlock struct {
	Planning map[string]json.RawMessage `json:"planning"`
}

func mustDecodeRawPlanningBlock(t *testing.T, path string) rawPlanningBlock {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var got rawPlanningBlock
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decoding %s: %v\ncontent:\n%s", path, err, data)
	}
	return got
}

func planningAttendedOnDisk(t *testing.T, path string) *bool {
	t.Helper()
	raw := mustDecodeRawPlanningBlock(t, path)
	attendedRaw, ok := raw.Planning["attended"]
	if !ok {
		return nil
	}
	var attended bool
	if err := json.Unmarshal(attendedRaw, &attended); err != nil {
		t.Fatalf("decoding planning.attended: %v", err)
	}
	return &attended
}

// TestSetPlanningAttended_CreatesFileAndParentDir locks in that arming the
// fleet switch on a machine with no config file yet creates the file (and
// its parent directory) rather than erroring — the first-run path for a
// fresh install, mirroring SetAutomergeEnabled/SetPlanRefined.
func TestSetPlanningAttended_CreatesFileAndParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cenci", "config.json")

	if err := SetPlanningAttended(path, true); err != nil {
		t.Fatalf("SetPlanningAttended: %v", err)
	}

	got := planningAttendedOnDisk(t, path)
	if got == nil || !*got {
		t.Errorf("planning.attended on disk = %v, want true", got)
	}
}

// TestSetPlanningAttended_PreservesDispatchAutomergeAndSiblingKeys locks in
// the raw-map read-modify-write contract shared with
// SetPlanRefined/SetAutomergeEnabled: toggling planning.attended must
// preserve an existing dispatch block (including repos), an existing
// automerge block, any sibling key inside planning, and every other
// top-level block verbatim.
func TestSetPlanningAttended_PreservesDispatchAutomergeAndSiblingKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{
  "defaultAgent": "claude",
  "dispatch": {
    "repos": [{"repo": "owner/name", "dir": "/abs/dir"}],
    "planRefined": true
  },
  "automerge": {
    "enabled": true
  },
  "planning": {
    "attended": false,
    "note": "hand-written"
  }
}`)

	if err := SetPlanningAttended(path, true); err != nil {
		t.Fatalf("SetPlanningAttended: %v", err)
	}

	cfg := mustDecodeConfig(t, path)
	if cfg.DefaultAgent != "claude" {
		t.Errorf("defaultAgent = %q, want preserved %q", cfg.DefaultAgent, "claude")
	}
	if !reposContains(cfg.Dispatch.Repos, "owner/name", "/abs/dir") {
		t.Errorf("dispatch.repos = %+v, want preserved entry owner/name -> /abs/dir", cfg.Dispatch.Repos)
	}
	if got := planRefinedOnDisk(t, path); got == nil || !*got {
		t.Errorf("dispatch.planRefined on disk = %v, want preserved true", got)
	}
	if got := automergeEnabledOnDisk(t, path); got == nil || !*got {
		t.Errorf("automerge.enabled on disk = %v, want preserved true", got)
	}

	raw := mustDecodeRawPlanningBlock(t, path)
	var note string
	if noteRaw, ok := raw.Planning["note"]; !ok {
		t.Errorf("planning.note sibling key dropped, got planning block %v", raw.Planning)
	} else if err := json.Unmarshal(noteRaw, &note); err != nil || note != "hand-written" {
		t.Errorf("planning.note = %q (err %v), want preserved %q", note, err, "hand-written")
	}

	got := planningAttendedOnDisk(t, path)
	if got == nil || !*got {
		t.Errorf("planning.attended on disk = %v, want true", got)
	}
}

// TestSetPlanningAttended_OnTwiceByteIdentical locks in the AC's idempotence
// requirement: running `on` a second time must leave the file byte-identical
// to the first run's result.
func TestSetPlanningAttended_OnTwiceByteIdentical(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{
  "dispatch": {"repos": [{"repo": "owner/name", "dir": "/abs/dir"}]},
  "automerge": {"enabled": true}
}`)

	if err := SetPlanningAttended(path, true); err != nil {
		t.Fatalf("SetPlanningAttended (first): %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	if err := SetPlanningAttended(path, true); err != nil {
		t.Fatalf("SetPlanningAttended (second): %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	if string(first) != string(second) {
		t.Errorf("second SetPlanningAttended(true) changed file content:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestSetPlanningAttended_Off locks in that off writes a literal false,
// distinguishable from unset (AC).
func TestSetPlanningAttended_Off(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := SetPlanningAttended(path, false); err != nil {
		t.Fatalf("SetPlanningAttended(false): %v", err)
	}
	got := planningAttendedOnDisk(t, path)
	if got == nil || *got {
		t.Errorf("planning.attended on disk = %v, want explicit false", got)
	}
}

// TestQueryPlanningAttended locks in the strict CLI/status reader semantics
// (#1086, exact QueryPlanRefined shape): literal true only; missing
// file/missing planning block/missing attended key are all (false, nil);
// malformed whole-file JSON and a non-bool attended (string, number, null)
// are all errors -- a status surface must never render a broken config as a
// confident "off".
func TestQueryPlanningAttended(t *testing.T) {
	t.Run("missing file is (false, nil)", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		attended, err := QueryPlanningAttended(path)
		if err != nil || attended {
			t.Errorf("QueryPlanningAttended = (%v, %v), want (false, nil)", attended, err)
		}
	})
	t.Run("no planning block is (false, nil)", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, `{"dispatch": {}}`)
		attended, err := QueryPlanningAttended(path)
		if err != nil || attended {
			t.Errorf("QueryPlanningAttended = (%v, %v), want (false, nil)", attended, err)
		}
	})
	t.Run("planning block with no attended key is (false, nil)", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, `{"planning": {"someOtherKey": true}}`)
		attended, err := QueryPlanningAttended(path)
		if err != nil || attended {
			t.Errorf("QueryPlanningAttended = (%v, %v), want (false, nil)", attended, err)
		}
	})
	t.Run("explicit true is attended", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, `{"planning": {"attended": true}}`)
		attended, err := QueryPlanningAttended(path)
		if err != nil || !attended {
			t.Errorf("QueryPlanningAttended = (%v, %v), want (true, nil)", attended, err)
		}
	})
	t.Run("explicit false is not attended", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, `{"planning": {"attended": false}}`)
		attended, err := QueryPlanningAttended(path)
		if err != nil || attended {
			t.Errorf("QueryPlanningAttended = (%v, %v), want (false, nil)", attended, err)
		}
	})
	t.Run("malformed whole-file JSON is an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, `{not json`)
		if _, err := QueryPlanningAttended(path); err == nil {
			t.Error("QueryPlanningAttended on malformed JSON returned nil error, want error")
		}
	})
	t.Run("non-bool attended string is an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, `{"planning": {"attended": "yes"}}`)
		if _, err := QueryPlanningAttended(path); err == nil {
			t.Error("QueryPlanningAttended on non-bool string attended returned nil error, want error")
		}
	})
	t.Run("non-bool attended number is an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, `{"planning": {"attended": 3}}`)
		if _, err := QueryPlanningAttended(path); err == nil {
			t.Error("QueryPlanningAttended on non-bool numeric attended returned nil error, want error")
		}
	})
	t.Run("non-bool attended null is an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		writeFile(t, path, `{"planning": {"attended": null}}`)
		if _, err := QueryPlanningAttended(path); err == nil {
			t.Error("QueryPlanningAttended on null attended returned nil error, want error")
		}
	})
}

// -- QueryRepoAutonomy -----------------------------------------------------

// TestQueryRepoAutonomy_NonRepoIsUnreadable locks in that the exported
// wrapper keeps the probe's fail-closed contract for a directory that isn't
// a git repository: unreadable, never a silent interactive/lean guess. The
// probe's full ref/parse behavior is pinned by autonomy_test.go; this only
// pins the wrapper's plumbing (dir + the remote-confirmed ref).
func TestQueryRepoAutonomy_NonRepoIsUnreadable(t *testing.T) {
	if got := QueryRepoAutonomy(t.TempDir()); got != RepoAutonomyUnreadable {
		t.Errorf("QueryRepoAutonomy(non-repo) = %q, want %q", got, RepoAutonomyUnreadable)
	}
}
