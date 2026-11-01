package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -- helpers ----------------------------------------------------------------

// writeFilterConfig writes body to a fresh config.json and returns its path.
func writeFilterConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

// filterConfigBody renders a user config with one enrolled repo carrying the
// given raw JSON under "filters" (no such key when rawFilters is empty). The
// concurrencyCap proves the neighbouring keys are untouched by a load.
func filterConfigBody(rawFilters string) string {
	entry := `{"repo": "owner/name", "dir": "/tmp/owner-name", "session": "work"`
	if rawFilters != "" {
		entry += `, "filters": ` + rawFilters
	}
	entry += `}`
	return `{"dispatch": {"concurrencyCap": 2, "repos": [` + entry + `]}}`
}

// loadFilterFor loads a one-repo config whose filters value is rawFilters and
// returns the resolved filter of owner/name.
func loadFilterFor(t *testing.T, rawFilters string) RepoFilter {
	t.Helper()
	cfg, err := LoadConfig(writeFilterConfig(t, filterConfigBody(rawFilters)))
	if err != nil {
		t.Fatalf("LoadConfig(filters %s): %v", rawFilters, err)
	}
	return FilterForRepo(cfg.Repos, "owner/name")
}

// filterFrom parses a filters object the way LoadConfig does, for matcher
// tests that need a normalized filter without a config file.
func filterFrom(t *testing.T, raw string) RepoFilter {
	t.Helper()
	f, err := parseRepoFilter(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("parseRepoFilter(%s): %v", raw, err)
	}
	return f
}

func milestoneTicket(repo string, number int) Ticket {
	return Ticket{Repo: repo, Number: 1, Milestone: number, MilestoneProbe: MilestoneProbePresent}
}

func labelTicket(labels ...string) Ticket {
	return Ticket{Repo: "owner/name", Number: 1, Labels: labels, MilestoneProbe: MilestoneProbeNone}
}

// -- MilestoneProbe closed set -----------------------------------------------

func TestFilterMilestoneProbe_ZeroValueIsUnknown(t *testing.T) {
	var tk Ticket
	if tk.MilestoneProbe != MilestoneProbeUnknown || MilestoneProbeUnknown != "" {
		t.Fatalf("zero Ticket.MilestoneProbe = %q, want the unknown zero value %q", tk.MilestoneProbe, MilestoneProbeUnknown)
	}
	if MilestoneProbeNone == MilestoneProbeUnknown || MilestoneProbePresent == MilestoneProbeUnknown || MilestoneProbeNone == MilestoneProbePresent {
		t.Fatalf("MilestoneProbe values must be distinct: %q %q %q", MilestoneProbeUnknown, MilestoneProbeNone, MilestoneProbePresent)
	}
}

// -- validation (through LoadConfig, the production entry point) --------------

// TestLoadConfig_Filters_RejectsInvalid_FullFieldPath covers AC2: each invalid
// shape fails the load with the config path, the repo and the full field path.
func TestLoadConfig_Filters_RejectsInvalid_FullFieldPath(t *testing.T) {
	cases := []struct {
		name    string
		filters string
		want    string // field path that must appear after "dispatch.repos[0] (owner/name)"
	}{
		// the filters value itself
		{"filters null", `null`, `.filters`},
		{"filters string", `"milestones"`, `.filters`},
		{"filters array", `[]`, `.filters`},
		{"filters number", `3`, `.filters`},
		{"filters bool", `true`, `.filters`},

		// unknown keys inside filters
		{"unknown key milestone typo", `{"milestone": [3]}`, `.filters.milestone`},
		{"unknown key label typo", `{"labels": ["bug"]}`, `.filters.labels`},
		{"wrong-case key", `{"Milestones": [3]}`, `.filters.Milestones`},

		// list-level type errors
		{"milestones null", `{"milestones": null}`, `.filters.milestones`},
		{"milestones number", `{"milestones": 3}`, `.filters.milestones`},
		{"milestones string", `{"milestones": "3"}`, `.filters.milestones`},
		{"milestones object", `{"milestones": {"n": 3}}`, `.filters.milestones`},
		{"labelsAll null", `{"labelsAll": null}`, `.filters.labelsAll`},
		{"labelsAll string", `{"labelsAll": "bug"}`, `.filters.labelsAll`},
		{"labelsAny null", `{"labelsAny": null}`, `.filters.labelsAny`},
		{"labelsAny object", `{"labelsAny": {}}`, `.filters.labelsAny`},
		{"labelsNone null", `{"labelsNone": null}`, `.filters.labelsNone`},
		{"labelsNone number", `{"labelsNone": 1}`, `.filters.labelsNone`},

		// milestone entries
		{"milestone zero", `{"milestones": [0]}`, `.filters.milestones[0]`},
		{"milestone zero second entry", `{"milestones": [3, 0]}`, `.filters.milestones[1]`},
		{"milestone negative", `{"milestones": [3, 7, -1]}`, `.filters.milestones[2]`},
		{"milestone fraction", `{"milestones": [1.5]}`, `.filters.milestones[0]`},
		{"milestone integral fraction 3.0", `{"milestones": [3.0]}`, `.filters.milestones[0]`},
		{"milestone exponent", `{"milestones": [1e2]}`, `.filters.milestones[0]`},
		{"milestone string", `{"milestones": [3, "7"]}`, `.filters.milestones[1]`},
		{"milestone title string", `{"milestones": ["v1.0"]}`, `.filters.milestones[0]`},
		{"milestone null entry", `{"milestones": [null]}`, `.filters.milestones[0]`},
		{"milestone bool", `{"milestones": [true]}`, `.filters.milestones[0]`},
		{"milestone object", `{"milestones": [{"number": 3}]}`, `.filters.milestones[0]`},
		{"milestone overflow", `{"milestones": [99999999999999999999]}`, `.filters.milestones[0]`},

		// label entries, per list
		{"labelsAll blank", `{"labelsAll": [""]}`, `.filters.labelsAll[0]`},
		{"labelsAll whitespace only", `{"labelsAll": ["bug", "   "]}`, `.filters.labelsAll[1]`},
		{"labelsAll number", `{"labelsAll": [1]}`, `.filters.labelsAll[0]`},
		{"labelsAll null entry", `{"labelsAll": [null]}`, `.filters.labelsAll[0]`},
		{"labelsAny blank", `{"labelsAny": ["bug", ""]}`, `.filters.labelsAny[1]`},
		{"labelsAny bool", `{"labelsAny": [false]}`, `.filters.labelsAny[0]`},
		{"labelsAny nested array", `{"labelsAny": [["bug"]]}`, `.filters.labelsAny[0]`},
		{"labelsNone whitespace only", `{"labelsNone": ["\t "]}`, `.filters.labelsNone[0]`},
		{"labelsNone null entry", `{"labelsNone": ["a", "b", null]}`, `.filters.labelsNone[2]`},
		{"labelsNone object", `{"labelsNone": [{"name": "x"}]}`, `.filters.labelsNone[0]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFilterConfig(t, filterConfigBody(tc.filters))
			cfg, err := LoadConfig(path)
			if err == nil {
				t.Fatalf("LoadConfig with filters %s returned nil error, want an error", tc.filters)
			}
			msg := err.Error()
			if !strings.Contains(msg, path) {
				t.Errorf("error %q must name the config path %s", msg, path)
			}
			if !strings.Contains(msg, "dispatch.repos[0] (owner/name)"+tc.want) {
				t.Errorf("error %q must carry the full field path %q", msg, "dispatch.repos[0] (owner/name)"+tc.want)
			}
			// Unrestricted defaults are never used for the bad file's repos: the
			// error path returns the unmerged defaults, exactly like the
			// memoryHeadroom errors, so no half-loaded repo list leaks out.
			if len(cfg.Repos) != 0 {
				t.Errorf("failed load returned %d repos, want none", len(cfg.Repos))
			}
			if cfg.ConcurrencyCap != DefaultConfig().ConcurrencyCap {
				t.Errorf("failed load returned concurrencyCap %d, want the unmerged default %d", cfg.ConcurrencyCap, DefaultConfig().ConcurrencyCap)
			}
		})
	}
}

// TestLoadConfig_Filters_UnknownKeyErrorNamesFieldPathAndKnownKeys: a typo such
// as `milestone` (for `milestones`) is rejected with an error that forms the
// full field path `filters.milestone` and lists the keys that are accepted.
func TestLoadConfig_Filters_UnknownKeyErrorNamesFieldPathAndKnownKeys(t *testing.T) {
	path := writeFilterConfig(t, filterConfigBody(`{"milestone": [3]}`))
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig with an unknown filters key returned nil error")
	}
	want := "dispatch.repos[0] (owner/name).filters.milestone: unknown key (known: milestones, labelsAll, labelsAny, labelsNone)"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q must contain %q", err, want)
	}
	if !strings.Contains(err.Error(), "filters.milestone") {
		t.Errorf("error %q must name the field path filters.milestone", err)
	}
}

// TestLoadConfig_Filters_UnknownKeyNamedDeterministically: with several unknown
// keys the sorted-first one is named, so the error never flips between runs.
func TestLoadConfig_Filters_UnknownKeyNamedDeterministically(t *testing.T) {
	for i := 0; i < 20; i++ {
		_, err := LoadConfig(writeFilterConfig(t, filterConfigBody(`{"zeta": 1, "milestones": [3], "alpha": 2, "mid": 3}`)))
		if err == nil {
			t.Fatal("LoadConfig with unknown filter keys returned nil error")
		}
		if !strings.Contains(err.Error(), `.filters.alpha:`) {
			t.Fatalf("error %q must name the sorted-first unknown key %q", err, "alpha")
		}
		if strings.Contains(err.Error(), `.filters.zeta`) || strings.Contains(err.Error(), `.filters.mid`) {
			t.Fatalf("error %q must name only the first unknown key", err)
		}
	}
}

// TestLoadConfig_Filters_InvalidSecondRepoNamesItsIndexAndRepo: the index and
// repo in the error identify the offending entry, not the first one.
func TestLoadConfig_Filters_InvalidSecondRepoNamesItsIndexAndRepo(t *testing.T) {
	body := `{"dispatch": {"repos": [
		{"repo": "owner/good", "dir": "/tmp/g", "session": "s", "filters": {"milestones": [3]}},
		{"repo": "owner/bad", "dir": "/tmp/b", "session": "s", "filters": {"labelsAll": ["ok", ""]}}
	]}}`
	path := writeFilterConfig(t, body)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig returned nil error, want a filters error")
	}
	want := "dispatch.repos[1] (owner/bad).filters.labelsAll[1]"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q must contain %q", err, want)
	}
	if !strings.Contains(err.Error(), "config "+path+":") {
		t.Errorf("error %q must be prefixed with the config path", err)
	}
}

// TestLoadConfig_Filters_ErrorBoundsEchoedValue: a hostile or huge value in a
// rejected entry cannot flood the error string.
func TestLoadConfig_Filters_ErrorBoundsEchoedValue(t *testing.T) {
	huge := strings.Repeat("9", 5000)
	_, err := LoadConfig(writeFilterConfig(t, filterConfigBody(`{"milestones": [`+huge+`]}`)))
	if err == nil {
		t.Fatal("LoadConfig with an overflowing milestone returned nil error")
	}
	if len(err.Error()) > 2*maxProbeLogDetailBytes {
		t.Errorf("error length %d is unbounded, want <= %d", len(err.Error()), 2*maxProbeLogDetailBytes)
	}
}

// -- normalization -------------------------------------------------------------

// TestLoadConfig_Filters_AcceptsDocumentedExample: AC1, the documented object
// loads and every list comes back.
func TestLoadConfig_Filters_AcceptsDocumentedExample(t *testing.T) {
	f := loadFilterFor(t, `{"milestones": [3, 7], "labelsAll": ["area:watch"], "labelsAny": ["bug", "enhancement"], "labelsNone": ["manual-only"]}`)
	if !f.Restricted() {
		t.Fatal("documented example loaded as unrestricted")
	}
	if got := f.Milestones; len(got) != 2 || got[0] != 3 || got[1] != 7 {
		t.Errorf("Milestones = %v, want [3 7]", got)
	}
	if got := f.LabelsAll; len(got) != 1 || got[0] != "area:watch" {
		t.Errorf("LabelsAll = %q, want [area:watch]", got)
	}
	if got := f.LabelsAny; len(got) != 2 || got[0] != "bug" || got[1] != "enhancement" {
		t.Errorf("LabelsAny = %q, want [bug enhancement]", got)
	}
	if got := f.LabelsNone; len(got) != 1 || got[0] != "manual-only" {
		t.Errorf("LabelsNone = %q, want [manual-only]", got)
	}
}

// TestLoadConfig_Filters_DuplicatesCollapse: AC3, duplicates (after trim and
// case-fold) collapse to the first spelling; order of first appearance holds.
func TestLoadConfig_Filters_DuplicatesCollapse(t *testing.T) {
	f := loadFilterFor(t, `{
		"milestones": [7, 3, 7, 3, 7],
		"labelsAll": ["Area:Watch", "  area:watch ", "AREA:WATCH", "bug"],
		"labelsAny": ["a", "A", " a", "b"],
		"labelsNone": ["x", "x"]
	}`)
	if got := f.Milestones; len(got) != 2 || got[0] != 7 || got[1] != 3 {
		t.Errorf("Milestones = %v, want [7 3]", got)
	}
	if got := f.LabelsAll; len(got) != 2 || got[0] != "Area:Watch" || got[1] != "bug" {
		t.Errorf("LabelsAll = %q, want [Area:Watch bug]", got)
	}
	if got := f.LabelsAny; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("LabelsAny = %q, want [a b]", got)
	}
	if got := f.LabelsNone; len(got) != 1 || got[0] != "x" {
		t.Errorf("LabelsNone = %q, want [x]", got)
	}
}

// TestLoadConfig_Filters_LabelsAreTrimmed: surrounding whitespace is not part of
// the stored name, interior whitespace is.
func TestLoadConfig_Filters_LabelsAreTrimmed(t *testing.T) {
	f := loadFilterFor(t, `{"labelsAll": ["  Area:Watch \t", " needs review "]}`)
	if got := f.LabelsAll; len(got) != 2 || got[0] != "Area:Watch" || got[1] != "needs review" {
		t.Errorf("LabelsAll = %q, want [Area:Watch \"needs review\"]", got)
	}
}

// TestLoadConfig_Filters_UnrestrictedShapes: AC1, an omitted filters key, an
// empty object, omitted lists and each empty list impose no restriction.
func TestLoadConfig_Filters_UnrestrictedShapes(t *testing.T) {
	cases := map[string]string{
		"filters omitted":    ``,
		"empty object":       `{}`,
		"milestones empty":   `{"milestones": []}`,
		"labelsAll empty":    `{"labelsAll": []}`,
		"labelsAny empty":    `{"labelsAny": []}`,
		"labelsNone empty":   `{"labelsNone": []}`,
		"all lists empty":    `{"milestones": [], "labelsAll": [], "labelsAny": [], "labelsNone": []}`,
		"whitespace padding": ` { } `,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			f := loadFilterFor(t, raw)
			if f.Restricted() {
				t.Errorf("filters %q loaded as restricted: %+v", raw, f)
			}
			if len(f.Milestones)+len(f.LabelsAll)+len(f.LabelsAny)+len(f.LabelsNone) != 0 {
				t.Errorf("filters %q loaded non-empty lists: %+v", raw, f)
			}
		})
	}
}

// TestLoadConfig_Filters_EachListAloneRestricts: a single nonempty dimension is
// enough to make the repo restricted, and only that dimension is populated.
func TestLoadConfig_Filters_EachListAloneRestricts(t *testing.T) {
	cases := map[string]string{
		"milestones": `{"milestones": [3]}`,
		"labelsAll":  `{"labelsAll": ["a"]}`,
		"labelsAny":  `{"labelsAny": ["a"]}`,
		"labelsNone": `{"labelsNone": ["a"]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if !loadFilterFor(t, raw).Restricted() {
				t.Errorf("filters %s loaded as unrestricted", raw)
			}
		})
	}
}

// TestLoadConfig_Filters_ShellLookingAndSpacedLabelsLoadVerbatim: AC literal
// names load as plain strings with no interpretation.
func TestLoadConfig_Filters_ShellLookingAndSpacedLabelsLoadVerbatim(t *testing.T) {
	f := loadFilterFor(t, `{"labelsAll": ["$(rm -rf)", "say \"hi\"", "needs review", "a:b", "*", "re.*gex", "-rf"]}`)
	want := []string{"$(rm -rf)", `say "hi"`, "needs review", "a:b", "*", "re.*gex", "-rf"}
	if len(f.LabelsAll) != len(want) {
		t.Fatalf("LabelsAll = %q, want %q", f.LabelsAll, want)
	}
	for i := range want {
		if f.LabelsAll[i] != want[i] {
			t.Errorf("LabelsAll[%d] = %q, want %q", i, f.LabelsAll[i], want[i])
		}
	}
}

// TestLoadConfig_Filters_LabelInAllAndNoneLoads: the collision is a valid config
// (it simply matches nothing that carries the label).
func TestLoadConfig_Filters_LabelInAllAndNoneLoads(t *testing.T) {
	f := loadFilterFor(t, `{"labelsAll": ["x"], "labelsNone": ["X"]}`)
	if !f.Restricted() || len(f.LabelsAll) != 1 || len(f.LabelsNone) != 1 {
		t.Errorf("collision filter = %+v, want both lists populated", f)
	}
}

// TestLoadConfig_Filters_NonexistentMilestoneNumberLoads: a number no issue
// carries is valid config; existence is never checked at load.
func TestLoadConfig_Filters_NonexistentMilestoneNumberLoads(t *testing.T) {
	f := loadFilterFor(t, `{"milestones": [999999]}`)
	if got := f.Milestones; len(got) != 1 || got[0] != 999999 {
		t.Errorf("Milestones = %v, want [999999]", got)
	}
}

// TestLoadConfig_Filters_ListsMarshalAsArraysNeverNull: nil slices must not
// serialize as null (watch/docs/go-gotchas.md), for a loaded filter, an
// unrestricted one and the unknown-repo fallback alike.
func TestLoadConfig_Filters_ListsMarshalAsArraysNeverNull(t *testing.T) {
	cfg, err := LoadConfig(writeFilterConfig(t, filterConfigBody(`{"labelsAll": ["a"]}`)))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	filters := map[string]RepoFilter{
		"partially configured": FilterForRepo(cfg.Repos, "owner/name"),
		"empty object":         loadFilterFor(t, `{}`),
		"omitted":              loadFilterFor(t, ``),
		"not enrolled":         FilterForRepo(cfg.Repos, "someone/else"),
	}
	for name, f := range filters {
		t.Run(name, func(t *testing.T) {
			out, err := json.Marshal(f)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if strings.Contains(string(out), "null") {
				t.Errorf("filter marshaled with null: %s", out)
			}
			for _, key := range []string{`"milestones":[`, `"labelsAll":[`, `"labelsAny":[`, `"labelsNone":[`} {
				if !strings.Contains(string(out), key) {
					t.Errorf("filter JSON %s missing array key %s", out, key)
				}
			}
		})
	}
}

// -- per-repo resolution --------------------------------------------------------

func TestFilterForRepo_LastEntryWinsAndMissIsUnrestricted(t *testing.T) {
	body := `{"dispatch": {"repos": [
		{"repo": "owner/a", "dir": "/a", "session": "s", "filters": {"milestones": [3]}},
		{"repo": "owner/b", "dir": "/b", "session": "s"},
		{"repo": "owner/a", "dir": "/a2", "session": "s", "filters": {"milestones": [9]}}
	]}}`
	cfg, err := LoadConfig(writeFilterConfig(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := FilterForRepo(cfg.Repos, "owner/a").Milestones; len(got) != 1 || got[0] != 9 {
		t.Errorf("duplicate repo a: Milestones = %v, want the last entry's [9]", got)
	}
	if FilterForRepo(cfg.Repos, "owner/b").Restricted() {
		t.Error("repo b without filters must be unrestricted")
	}
	if FilterForRepo(cfg.Repos, "owner/missing").Restricted() {
		t.Error("a repo that is not enrolled must be unrestricted")
	}
	byRepo := buildFilterByRepo(cfg.Repos)
	if got := byRepo["owner/a"].Milestones; len(got) != 1 || got[0] != 9 {
		t.Errorf("buildFilterByRepo last-wins: Milestones = %v, want [9]", got)
	}
}

// TestFilterForRepo_LaterUnrestrictedEntryOverridesEarlierRestricted: last-wins
// applies to the whole entry, an omitted filters block does not inherit.
func TestFilterForRepo_LaterUnrestrictedEntryOverridesEarlierRestricted(t *testing.T) {
	body := `{"dispatch": {"repos": [
		{"repo": "owner/a", "dir": "/a", "session": "s", "filters": {"milestones": [3]}},
		{"repo": "owner/a", "dir": "/a", "session": "s"}
	]}}`
	cfg, err := LoadConfig(writeFilterConfig(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if FilterForRepo(cfg.Repos, "owner/a").Restricted() {
		t.Error("the later entry without filters must win and be unrestricted")
	}
	if buildFilterByRepo(cfg.Repos)["owner/a"].Restricted() {
		t.Error("buildFilterByRepo: the later entry without filters must win")
	}
}

// -- matcher: milestones ----------------------------------------------------------

func TestFilterSkip_Milestones(t *testing.T) {
	cases := []struct {
		name       string
		filters    string
		ticket     Ticket
		wantSkip   bool
		wantReason string
	}{
		{"one allowed milestone matches", `{"milestones": [3]}`, milestoneTicket("owner/name", 3), false, ""},
		{"one of several allowed matches", `{"milestones": [3, 7, 9]}`, milestoneTicket("owner/name", 7), false, ""},
		{"wrong milestone", `{"milestones": [3]}`, milestoneTicket("owner/name", 7), true, "filter: milestone 7 not in [3]"},
		{"wrong milestone lists all allowed", `{"milestones": [3, 7]}`, milestoneTicket("owner/name", 5), true, "filter: milestone 5 not in [3 7]"},
		{"null milestone excluded", `{"milestones": [3]}`, Ticket{Repo: "owner/name", MilestoneProbe: MilestoneProbeNone}, true, "filter: no milestone assigned (milestones [3])"},
		{"null milestone excluded lists all", `{"milestones": [3, 7]}`, Ticket{Repo: "owner/name", MilestoneProbe: MilestoneProbeNone}, true, "filter: no milestone assigned (milestones [3 7])"},
		{"unknown milestone holds", `{"milestones": [3]}`, Ticket{Repo: "owner/name", MilestoneProbe: MilestoneProbeUnknown}, true, "filter: milestone metadata unavailable"},
		{"unset probe on a literal ticket holds", `{"milestones": [3]}`, Ticket{Repo: "owner/name", Number: 4}, true, "filter: milestone metadata unavailable"},
		{"unrecognized probe holds", `{"milestones": [3]}`, Ticket{Repo: "owner/name", MilestoneProbe: MilestoneProbe("bogus"), Milestone: 3}, true, "filter: milestone metadata unavailable"},
		{"present probe without a usable number holds", `{"milestones": [3]}`, Ticket{Repo: "owner/name", MilestoneProbe: MilestoneProbePresent, Milestone: 0}, true, "filter: milestone metadata unavailable"},
		{"nonexistent configured number matches nothing (assigned)", `{"milestones": [999]}`, milestoneTicket("owner/name", 3), true, "filter: milestone 3 not in [999]"},
		{"nonexistent configured number matches nothing (unassigned)", `{"milestones": [999]}`, Ticket{Repo: "owner/name", MilestoneProbe: MilestoneProbeNone}, true, "filter: no milestone assigned (milestones [999])"},
		{"empty milestones list admits an unassigned ticket", `{"milestones": []}`, Ticket{Repo: "owner/name", MilestoneProbe: MilestoneProbeNone}, false, ""},
		{"empty milestones list admits any milestone", `{"milestones": []}`, milestoneTicket("owner/name", 42), false, ""},
		{"no milestone constraint ignores unknown probe", `{"labelsAll": ["x"]}`, Ticket{Repo: "owner/name", Labels: []string{"x"}, MilestoneProbe: MilestoneProbeUnknown}, false, ""},
		{"no milestone constraint ignores unrecognized probe", `{"labelsAll": ["x"]}`, Ticket{Repo: "owner/name", Labels: []string{"x"}, MilestoneProbe: MilestoneProbe("bogus")}, false, ""},
		{"no milestone constraint ignores null milestone", `{"labelsAll": ["x"]}`, Ticket{Repo: "owner/name", Labels: []string{"x"}, MilestoneProbe: MilestoneProbeNone}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, skipped := filterSkip(filterFrom(t, tc.filters), tc.ticket)
			if skipped != tc.wantSkip {
				t.Fatalf("filterSkip skipped = %v (reason %q), want %v", skipped, reason, tc.wantSkip)
			}
			if reason != tc.wantReason {
				t.Errorf("filterSkip reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// TestFilterSkip_MilestoneKeysOnNumberNotTitle: AC, a renamed milestone keeps
// its decision because only the number is consulted.
func TestFilterSkip_MilestoneKeysOnNumberNotTitle(t *testing.T) {
	f := filterFrom(t, `{"milestones": [3]}`)
	before := milestoneTicket("owner/name", 3)
	before.Title = "Sprint 1"
	after := before
	after.Title = "Renamed: Sprint 1 (final)"
	b, bs := filterSkip(f, before)
	a, as := filterSkip(f, after)
	if bs || as || b != a {
		t.Errorf("renamed title changed the decision: before (%q, %v) after (%q, %v), want both unskipped", b, bs, a, as)
	}
	other := milestoneTicket("owner/name", 7)
	other.Title = "Sprint 1"
	if _, skipped := filterSkip(f, other); !skipped {
		t.Error("a title equal to the allowed milestone's title must not admit a different number")
	}
}

// TestFilterSkip_SameMilestoneNumberInTwoReposMatchesPerRepo: the number is
// repo-local, so each repo's own filter applies to its own tickets only.
func TestFilterSkip_SameMilestoneNumberInTwoReposMatchesPerRepo(t *testing.T) {
	byRepo := buildFilterByRepo([]RepoConfig{
		{Repo: "owner/a", Filter: filterFrom(t, `{"milestones": [3]}`)},
		{Repo: "owner/b", Filter: filterFrom(t, `{"milestones": [7]}`)},
	})
	cases := []struct {
		repo     string
		number   int
		wantSkip bool
	}{
		{"owner/a", 3, false},
		{"owner/a", 7, true},
		{"owner/b", 3, true}, // milestone 3 of repo b is not repo a's milestone 3
		{"owner/b", 7, false},
	}
	for _, tc := range cases {
		_, skipped := filterSkip(byRepo[tc.repo], milestoneTicket(tc.repo, tc.number))
		if skipped != tc.wantSkip {
			t.Errorf("%s milestone %d: skipped = %v, want %v", tc.repo, tc.number, skipped, tc.wantSkip)
		}
	}
}

// -- matcher: labels ---------------------------------------------------------------

func TestFilterSkip_Labels(t *testing.T) {
	cases := []struct {
		name       string
		filters    string
		labels     []string
		wantSkip   bool
		wantReason string
	}{
		// labelsAll
		{"all: every label present", `{"labelsAll": ["area:watch", "bug"]}`, []string{"Planned", "bug", "area:watch"}, false, ""},
		{"all: first missing label named", `{"labelsAll": ["area:watch", "bug"]}`, []string{"area:watch"}, true, `filter: required label "bug" missing`},
		{"all: no labels at all", `{"labelsAll": ["area:watch", "bug"]}`, nil, true, `filter: required label "area:watch" missing`},
		{"all: configured order decides which is reported", `{"labelsAll": ["b", "a"]}`, []string{}, true, `filter: required label "b" missing`},

		// labelsAny
		{"any: one of several", `{"labelsAny": ["bug", "enhancement"]}`, []string{"enhancement"}, false, ""},
		{"any: several present", `{"labelsAny": ["bug", "enhancement"]}`, []string{"bug", "enhancement"}, false, ""},
		{"any: none present", `{"labelsAny": ["bug", "enhancement"]}`, []string{"docs"}, true, `filter: no labelsAny match among ["bug" "enhancement"]`},
		{"any: no labels at all", `{"labelsAny": ["bug"]}`, nil, true, `filter: no labelsAny match among ["bug"]`},

		// labelsNone
		{"none: label absent", `{"labelsNone": ["manual-only"]}`, []string{"Planned"}, false, ""},
		{"none: label present", `{"labelsNone": ["manual-only"]}`, []string{"Planned", "manual-only"}, true, `filter: excluded label "manual-only" present`},
		{"none: first configured present label named", `{"labelsNone": ["a", "b"]}`, []string{"b", "a"}, true, `filter: excluded label "a" present`},
		{"none: no labels at all", `{"labelsNone": ["manual-only"]}`, nil, false, ""},

		// case and whitespace
		{"configured with case and spaces matches lowercase issue label", `{"labelsAll": ["  Area:Watch "]}`, []string{"area:watch"}, false, ""},
		{"issue label upper-case matches", `{"labelsAll": ["area:watch"]}`, []string{"AREA:WATCH"}, false, ""},
		{"exclusion is case-insensitive", `{"labelsNone": ["Manual-Only"]}`, []string{"manual-only"}, true, `filter: excluded label "Manual-Only" present`},
		{"any is case-insensitive", `{"labelsAny": ["BUG"]}`, []string{"bug"}, false, ""},

		// literal, never substring/glob/regex
		{"configured prefix does not match longer label", `{"labelsAll": ["area"]}`, []string{"area:watch"}, true, `filter: required label "area" missing`},
		{"configured longer name does not match its prefix", `{"labelsAll": ["area:watch"]}`, []string{"area"}, true, `filter: required label "area:watch" missing`},
		{"substring exclusion does not fire", `{"labelsNone": ["area"]}`, []string{"area:watch"}, false, ""},
		{"glob star is literal", `{"labelsAny": ["area:*"]}`, []string{"area:watch"}, true, `filter: no labelsAny match among ["area:*"]`},
		{"glob star matches only a literal star label", `{"labelsAny": ["area:*"]}`, []string{"area:*"}, false, ""},
		{"regex is literal", `{"labelsAny": ["bu."]}`, []string{"bug"}, true, `filter: no labelsAny match among ["bu."]`},

		// literal names with spaces, quotes, a colon or shell-looking text
		{"label with spaces matches", `{"labelsAll": ["needs review"]}`, []string{"needs review"}, false, ""},
		{"label with spaces does not match a hyphenated one", `{"labelsAll": ["needs review"]}`, []string{"needs-review"}, true, `filter: required label "needs review" missing`},
		{"label with quotes matches", `{"labelsAll": ["say \"hi\""]}`, []string{`say "hi"`}, false, ""},
		{"label with quotes is quoted in the reason", `{"labelsAll": ["say \"hi\""]}`, nil, true, `filter: required label "say \"hi\"" missing`},
		{"shell-looking label matches literally", `{"labelsAll": ["$(rm -rf)"]}`, []string{"$(rm -rf)"}, false, ""},
		{"shell-looking label is quoted in the reason", `{"labelsAll": ["$(rm -rf)"]}`, []string{"rm -rf"}, true, `filter: required label "$(rm -rf)" missing`},
		{"backtick label is quoted in the reason", `{"labelsNone": ["` + "`id`" + `"]}`, []string{"`id`"}, true, "filter: excluded label \"`id`\" present"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, skipped := filterSkip(filterFrom(t, tc.filters), labelTicket(tc.labels...))
			if skipped != tc.wantSkip {
				t.Fatalf("filterSkip skipped = %v (reason %q), want %v", skipped, reason, tc.wantSkip)
			}
			if reason != tc.wantReason {
				t.Errorf("filterSkip reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// -- matcher: composition and ordering ----------------------------------------------

func TestFilterSkip_DimensionsCombineWithAndInFixedOrder(t *testing.T) {
	const all = `{"milestones": [3], "labelsAll": ["area:watch"], "labelsAny": ["bug", "enhancement"], "labelsNone": ["manual-only"]}`
	f := filterFrom(t, all)
	pass := Ticket{Repo: "owner/name", Milestone: 3, MilestoneProbe: MilestoneProbePresent, Labels: []string{"area:watch", "enhancement"}}

	if reason, skipped := filterSkip(f, pass); skipped {
		t.Fatalf("a ticket satisfying every dimension was skipped: %q", reason)
	}

	cases := []struct {
		name       string
		mutate     func(*Ticket)
		wantReason string
	}{
		{"only the milestone fails", func(tk *Ticket) { tk.Milestone = 7 }, "filter: milestone 7 not in [3]"},
		{"only labelsAll fails", func(tk *Ticket) { tk.Labels = []string{"enhancement"} }, `filter: required label "area:watch" missing`},
		{"only labelsAny fails", func(tk *Ticket) { tk.Labels = []string{"area:watch"} }, `filter: no labelsAny match among ["bug" "enhancement"]`},
		{"only labelsNone fails", func(tk *Ticket) { tk.Labels = []string{"area:watch", "bug", "manual-only"} }, `filter: excluded label "manual-only" present`},
		{"milestone beats labelsAll", func(tk *Ticket) { tk.Milestone = 7; tk.Labels = nil }, "filter: milestone 7 not in [3]"},
		{"unknown milestone beats every label dimension", func(tk *Ticket) { tk.MilestoneProbe = MilestoneProbeUnknown; tk.Labels = []string{"manual-only"} }, "filter: milestone metadata unavailable"},
		{"labelsAll beats labelsAny", func(tk *Ticket) { tk.Labels = []string{"docs"} }, `filter: required label "area:watch" missing`},
		{"labelsAny beats labelsNone", func(tk *Ticket) { tk.Labels = []string{"area:watch", "manual-only"} }, `filter: no labelsAny match among ["bug" "enhancement"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tk := pass
			tc.mutate(&tk)
			reason, skipped := filterSkip(f, tk)
			if !skipped {
				t.Fatalf("expected a skip, got none for %+v", tk)
			}
			if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
			if !strings.HasPrefix(reason, "filter: ") {
				t.Errorf("reason %q must start with %q", reason, "filter: ")
			}
		})
	}
}

// TestFilterSkip_LabelInAllAndNone: AC edge case, the collision never matches.
func TestFilterSkip_LabelInAllAndNone(t *testing.T) {
	f := filterFrom(t, `{"labelsAll": ["x"], "labelsNone": ["x"]}`)
	if reason, skipped := filterSkip(f, labelTicket("x")); !skipped || reason != `filter: excluded label "x" present` {
		t.Errorf("ticket carrying the label: (%q, %v), want the excluded-label skip", reason, skipped)
	}
	if reason, skipped := filterSkip(f, labelTicket("y")); !skipped || reason != `filter: required label "x" missing` {
		t.Errorf("ticket without the label: (%q, %v), want the missing required label skip", reason, skipped)
	}
}

// TestFilterSkip_UnrestrictedNeverSkips: AC1 parity, an unrestricted filter
// decides nothing whatever the ticket looks like.
func TestFilterSkip_UnrestrictedNeverSkips(t *testing.T) {
	filters := map[string]RepoFilter{
		"zero value": {},
		"empty":      filterFrom(t, `{}`),
		"not found":  buildFilterByRepo(nil)["owner/name"],
	}
	tickets := []Ticket{
		{},
		milestoneTicket("owner/name", 3),
		{Repo: "owner/name", MilestoneProbe: MilestoneProbeNone, Labels: []string{"manual-only"}},
		{Repo: "owner/name", MilestoneProbe: MilestoneProbe("bogus")},
	}
	for name, f := range filters {
		for i, tk := range tickets {
			if reason, skipped := filterSkip(f, tk); skipped || reason != "" {
				t.Errorf("%s filter, ticket %d: skipped (%q), want no skip", name, i, reason)
			}
		}
	}
}

// -- matcher: reason bounding ---------------------------------------------------------

// TestFilterSkip_ReasonIsBoundedAndSingleLine: a huge or multi-line configured
// label cannot flood the one-record-per-line decision log.
func TestFilterSkip_ReasonIsBoundedAndSingleLine(t *testing.T) {
	long := strings.Repeat("x", 5000)
	cases := map[string]string{
		"labelsAll":  `{"labelsAll": ["` + long + `"]}`,
		"labelsNone": `{"labelsNone": ["` + long + `"]}`,
		"labelsAny":  `{"labelsAny": ["` + long + `", "` + long + `"]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			tk := labelTicket()
			if name == "labelsNone" {
				tk = labelTicket(long)
			}
			reason, skipped := filterSkip(filterFrom(t, raw), tk)
			if !skipped {
				t.Fatal("expected a skip")
			}
			if len(reason) > maxProbeLogDetailBytes {
				t.Errorf("reason length %d exceeds the bound %d", len(reason), maxProbeLogDetailBytes)
			}
			if !strings.HasPrefix(reason, "filter: ") {
				t.Errorf("truncation must keep the %q prefix, got %q", "filter: ", reason)
			}
		})
	}

	t.Run("newline in a configured label", func(t *testing.T) {
		reason, skipped := filterSkip(filterFrom(t, `{"labelsAll": ["a\nb\r\nc"]}`), labelTicket())
		if !skipped {
			t.Fatal("expected a skip")
		}
		if strings.ContainsAny(reason, "\r\n") {
			t.Errorf("reason %q must be a single line", reason)
		}
	})
}
