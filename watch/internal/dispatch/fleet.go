package dispatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// This file owns the CLI-writable fleet autonomy switches (#964):
// automerge.enabled (read by internal/babysit's loadFleetSwitch; not
// imported here — babysit sits below internal/daemon in the import graph,
// so importing it would close a cycle) and dispatch.planRefined (read by
// this package's own Decide gate chain). Both use the same raw-map
// read-modify-write pattern as SetLoopEnabled/EnrollRepo so every key this
// package doesn't understand survives round-tripping verbatim.

// SetAutomergeEnabled idempotently sets the fleet-wide automerge.enabled
// kill switch, preserving every other top-level key and every sibling key
// inside the automerge block verbatim. An empty path resolves
// run.DefaultConfigPath().
func SetAutomergeEnabled(path string, enabled bool) error {
	path, err := resolveConfigPath(path)
	if err != nil {
		return err
	}

	top, err := readRawConfig(path)
	if err != nil {
		return err
	}

	automergeRaw := map[string]json.RawMessage{}
	if raw, ok := top["automerge"]; ok {
		if err := json.Unmarshal(raw, &automergeRaw); err != nil {
			return fmt.Errorf("parsing automerge block: %w", err)
		}
	}

	enabledRaw, err := json.Marshal(enabled)
	if err != nil {
		return fmt.Errorf("marshaling automerge.enabled: %w", err)
	}
	automergeRaw["enabled"] = enabledRaw

	automergeBlockRaw, err := json.Marshal(automergeRaw)
	if err != nil {
		return fmt.Errorf("marshaling automerge block: %w", err)
	}
	top["automerge"] = automergeBlockRaw

	return writeRawConfig(path, top)
}

// QueryAutomergeEnabled reports the fleet-wide automerge.enabled kill
// switch with the same strict semantics as babysit's loadFleetSwitch: only
// a literal `"enabled": true` reads as enabled; a missing file or absent
// key is (false, nil). Unlike babysit's default-deny fold, a malformed file
// is an error — a status surface must never render a broken config as a
// confident "disabled". An empty path resolves run.DefaultConfigPath().
func QueryAutomergeEnabled(path string) (bool, error) {
	path, err := resolveConfigPath(path)
	if err != nil {
		return false, err
	}
	top, err := readRawConfig(path)
	if err != nil {
		return false, err
	}
	raw, ok := top["automerge"]
	if !ok {
		return false, nil
	}
	var block struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return false, fmt.Errorf("parsing automerge block: %w", err)
	}
	return block.Enabled != nil && *block.Enabled, nil
}

// SetPlanRefined idempotently sets dispatch.planRefined — the fleet-wide
// planning-pickup/autonomous-re-plan kill switch (#828/#851) — preserving
// every other key in the dispatch block (repos, loopEnabled, caps) and
// every other top-level block verbatim. An empty path resolves
// run.DefaultConfigPath().
func SetPlanRefined(path string, enabled bool) error {
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
		return fmt.Errorf("marshaling planRefined: %w", err)
	}
	dispatchRaw["planRefined"] = enabledRaw

	dispatchBlockRaw, err := json.Marshal(dispatchRaw)
	if err != nil {
		return fmt.Errorf("marshaling dispatch block: %w", err)
	}
	top["dispatch"] = dispatchBlockRaw

	return writeRawConfig(path, top)
}

// QueryPlanRefined reports dispatch.planRefined with QueryAutomergeEnabled's
// semantics: literal true only; missing file/key is (false, nil); malformed
// JSON is an error. An empty path resolves run.DefaultConfigPath().
func QueryPlanRefined(path string) (bool, error) {
	path, err := resolveConfigPath(path)
	if err != nil {
		return false, err
	}
	top, err := readRawConfig(path)
	if err != nil {
		return false, err
	}
	raw, ok := top["dispatch"]
	if !ok {
		return false, nil
	}
	var block struct {
		PlanRefined *bool `json:"planRefined"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return false, fmt.Errorf("parsing dispatch block: %w", err)
	}
	return block.PlanRefined != nil && *block.PlanRefined, nil
}

// SetPlanningAttended idempotently sets the fleet-wide planning.attended
// switch (#1086) -- a distinct top-level "planning" block from the
// repo-committed .cenci/config.json's own "planning.autonomy" key, which
// this file never reads or writes -- preserving every other top-level block
// (dispatch, automerge) and every sibling key inside the planning block
// verbatim, mirroring SetPlanRefined above. An empty path resolves
// run.DefaultConfigPath().
func SetPlanningAttended(path string, enabled bool) error {
	path, err := resolveConfigPath(path)
	if err != nil {
		return err
	}

	top, err := readRawConfig(path)
	if err != nil {
		return err
	}

	planningRaw := map[string]json.RawMessage{}
	if raw, ok := top["planning"]; ok {
		if err := json.Unmarshal(raw, &planningRaw); err != nil {
			return fmt.Errorf("parsing planning block: %w", err)
		}
	}

	enabledRaw, err := json.Marshal(enabled)
	if err != nil {
		return fmt.Errorf("marshaling planning.attended: %w", err)
	}
	planningRaw["attended"] = enabledRaw

	planningBlockRaw, err := json.Marshal(planningRaw)
	if err != nil {
		return fmt.Errorf("marshaling planning block: %w", err)
	}
	top["planning"] = planningBlockRaw

	return writeRawConfig(path, top)
}

// QueryPlanningAttended reports the fleet-wide planning.attended switch with
// QueryPlanRefined's exact strict semantics: literal true only; a missing
// file, a missing planning block, or a missing attended key is (false, nil);
// malformed whole-file JSON or a non-bool attended value is an error -- a
// status surface must never render a broken config as a confident "off". A
// literal JSON null is deliberately treated the same as any other non-bool
// value here (an explicit error), NOT the same as a missing key: a *bool
// field decode (QueryPlanRefined's own idiom) would silently fold null into
// "unset", which would let a config that explicitly nulled out attended
// render as a confident "off" -- exactly the failure mode this strict reader
// exists to prevent. An empty path resolves run.DefaultConfigPath().
func QueryPlanningAttended(path string) (bool, error) {
	path, err := resolveConfigPath(path)
	if err != nil {
		return false, err
	}
	top, err := readRawConfig(path)
	if err != nil {
		return false, err
	}
	raw, ok := top["planning"]
	if !ok {
		return false, nil
	}
	var planningRaw map[string]json.RawMessage
	if err := json.Unmarshal(raw, &planningRaw); err != nil {
		return false, fmt.Errorf("parsing planning block: %w", err)
	}
	attendedRaw, ok := planningRaw["attended"]
	if !ok {
		return false, nil
	}
	if bytes.Equal(bytes.TrimSpace(attendedRaw), []byte("null")) {
		return false, fmt.Errorf("parsing planning.attended: must be a bool, got null")
	}
	var attended bool
	if err := json.Unmarshal(attendedRaw, &attended); err != nil {
		return false, fmt.Errorf("parsing planning.attended: %w", err)
	}
	return attended, nil
}

// QueryRepoAutonomy reports dir's committed planning.autonomy verdict at
// the remote-confirmed ref (refs/remotes/origin/main) as of the last
// successful fetch — the same probe the dispatch gate chain authorizes
// planning pickups with (#851/#877), exported for the status surface. It
// runs no fetch itself, so the verdict can lag the remote until the next
// dispatch pass (which always fetches first). Fail-closed like the gate: a
// non-repo dir or unresolvable ref is RepoAutonomyUnreadable, never a
// guess.
func QueryRepoAutonomy(dir string) RepoAutonomy {
	return probeRepoAutonomy(dir, remoteMainAuthRef)
}

// AutomergePolicyScope summarizes one scope's automerge policy block from a
// repo's .cenci/config.json for status rendering: the top-level block
// (Scope "") or one projects[] entry (Scope = slug). Malformed mirrors
// babysit's deny rule — a present block missing a positive maxChangedFiles
// or maxDiffLines denies every PR it governs.
type AutomergePolicyScope struct {
	Scope           string `json:"scope"`
	Present         bool   `json:"present"`
	Malformed       bool   `json:"malformed,omitempty"`
	MaxChangedFiles int    `json:"max_changed_files,omitempty"`
	MaxDiffLines    int    `json:"max_diff_lines,omitempty"`
	MergeMethod     string `json:"merge_method,omitempty"`
	ProtectedPaths  int    `json:"protected_paths,omitempty"`
}

// RepoPolicySummary is SummarizeRepoPolicy's result: whether root has a
// .cenci/config.json at all, and one AutomergePolicyScope per policy scope
// when it does.
type RepoPolicySummary struct {
	Root          string                 `json:"root"`
	ConfigPresent bool                   `json:"config_present"`
	Scopes        []AutomergePolicyScope `json:"scopes,omitempty"`
}

// SummarizeRepoPolicy reads root's working-tree .cenci/config.json and
// summarizes each scope's automerge policy block. Informational only:
// babysit's enforcement reads this file from the PR's base branch, never
// the working tree, so this answers "what is being edited here", not "what
// will the next merge be judged by". A missing file is (ConfigPresent
// false, nil); an unreadable or malformed file is an error, never a
// confident summary. In a monorepo the top-level scope is always reported
// alongside the projects, since root-owned files fall back to it.
func SummarizeRepoPolicy(root string) (RepoPolicySummary, error) {
	sum := RepoPolicySummary{Root: root}
	data, err := os.ReadFile(filepath.Join(root, repoConfigPath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return sum, nil
		}
		return sum, fmt.Errorf("reading %s: %w", repoConfigPath, err)
	}

	var f struct {
		Automerge json.RawMessage `json:"automerge"`
		Projects  []struct {
			Slug      string          `json:"slug"`
			Automerge json.RawMessage `json:"automerge"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return sum, fmt.Errorf("parsing %s: %w", repoConfigPath, err)
	}
	sum.ConfigPresent = true

	for _, p := range f.Projects {
		sum.Scopes = append(sum.Scopes, summarizePolicyBlock(p.Slug, p.Automerge))
	}
	sum.Scopes = append(sum.Scopes, summarizePolicyBlock("", f.Automerge))
	return sum, nil
}

// summarizePolicyBlock classifies one raw automerge block. A `null` or
// absent block is not-present (denies under "policy absent"); a block that
// fails to decode, or decodes without both required positive caps, is
// present-but-malformed (denies under its own reason). mergeMethod defaults
// to "squash" for display, matching babysit's documented default.
func summarizePolicyBlock(scope string, raw json.RawMessage) AutomergePolicyScope {
	s := AutomergePolicyScope{Scope: scope}
	if len(raw) == 0 || string(raw) == "null" {
		return s
	}
	s.Present = true
	var block struct {
		ProtectedPaths  []string `json:"protectedPaths"`
		MaxChangedFiles int      `json:"maxChangedFiles"`
		MaxDiffLines    int      `json:"maxDiffLines"`
		MergeMethod     string   `json:"mergeMethod"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		s.Malformed = true
		return s
	}
	s.MaxChangedFiles = block.MaxChangedFiles
	s.MaxDiffLines = block.MaxDiffLines
	s.MergeMethod = block.MergeMethod
	if s.MergeMethod == "" {
		s.MergeMethod = "squash"
	}
	s.ProtectedPaths = len(block.ProtectedPaths)
	if block.MaxChangedFiles <= 0 || block.MaxDiffLines <= 0 {
		s.Malformed = true
	}
	return s
}
