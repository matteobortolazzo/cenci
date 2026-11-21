package capacity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadLimitsDefaultsToThree(t *testing.T) {
	for name, content := range map[string]string{
		"empty object":           `{}`,
		"no dispatch block":      `{"automerge":{"enabled":true}}`,
		"dispatch without a cap": `{"dispatch":{"repos":[{"repo":"o/a","dir":"/x"}]}}`,
		"null cap":               `{"dispatch":{"concurrencyCap":null}}`,
		"null dispatch":          `{"dispatch":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := LoadLimits(writeConfig(t, content))
			if err != nil {
				t.Fatalf("LoadLimits: %v", err)
			}
			if got.Machine != 3 || DefaultMachineCap != 3 {
				t.Fatalf("Machine = %d (default %d), want 3", got.Machine, DefaultMachineCap)
			}
			if _, ok := got.RepoCap("o/a"); ok {
				t.Fatalf("a repo without a cap must have none")
			}
		})
	}
}

func TestLoadLimitsAbsentFileDefaultsToThree(t *testing.T) {
	got, err := LoadLimits(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || got.Machine != 3 {
		t.Fatalf("LoadLimits(absent) = %+v, %v; want machine 3 and no error", got, err)
	}
}

func TestLoadLimitsReadsMachineAndRepoCaps(t *testing.T) {
	got, err := LoadLimits(writeConfig(t, `{"dispatch":{"concurrencyCap":5,"repos":[
		{"repo":"o/a","dir":"/a","concurrencyCap":1},
		{"repo":"o/b","dir":"/b"},
		{"repo":"o/z","dir":"/z","concurrencyCap":0}]}}`))
	if err != nil {
		t.Fatalf("LoadLimits: %v", err)
	}
	if got.Machine != 5 {
		t.Errorf("Machine = %d, want 5", got.Machine)
	}
	if c, ok := got.RepoCap("o/a"); !ok || c != 1 {
		t.Errorf("RepoCap(o/a) = %d, %v; want 1, true", c, ok)
	}
	if _, ok := got.RepoCap("o/b"); ok {
		t.Errorf("o/b has no repo cap")
	}
	if c, ok := got.RepoCap("o/z"); !ok || c != 0 {
		t.Errorf("RepoCap(o/z) = %d, %v; want 0, true (admit nothing)", c, ok)
	}
}

func TestLimitsEffectiveRepoCapNeverExceedsMachineCap(t *testing.T) {
	l := Limits{Machine: 3, Repos: map[string]int{"o/high": 10, "o/low": 1, "o/zero": 0}}
	for repo, want := range map[string]int{"o/high": 3, "o/low": 1, "o/zero": 0} {
		if got, ok := l.EffectiveRepoCap(repo); !ok || got != want {
			t.Errorf("EffectiveRepoCap(%s) = %d, %v; want %d", repo, got, ok, want)
		}
	}
	if _, ok := l.EffectiveRepoCap("o/none"); ok {
		t.Error("a repo without a cap has no effective cap")
	}
}

func TestLoadLimitsRepoMatchIgnoresCase(t *testing.T) {
	got, err := LoadLimits(writeConfig(t, `{"dispatch":{"repos":[{"repo":"Owner/Repo","concurrencyCap":2}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := got.RepoCap("owner/repo"); !ok || c != 2 {
		t.Fatalf("RepoCap(owner/repo) = %d, %v; want 2, true", c, ok)
	}
}

func TestLoadLimitsMachineCapZeroIsAPauseAndNegativeIsToo(t *testing.T) {
	for _, v := range []string{"0", "-2"} {
		got, err := LoadLimits(writeConfig(t, `{"dispatch":{"concurrencyCap":`+v+`}}`))
		if err != nil || got.Machine != 0 {
			t.Errorf("cap %s: Machine = %d, err %v; want 0 and no error", v, got.Machine, err)
		}
	}
}

func TestLoadLimitsUnreadableConfigIsAnErrorNotADefault(t *testing.T) {
	dir := t.TempDir()
	for name, path := range map[string]string{
		"invalid json":      writeConfig(t, `{"dispatch":`),
		"string cap":        writeConfig(t, `{"dispatch":{"concurrencyCap":"3"}}`),
		"fractional cap":    writeConfig(t, `{"dispatch":{"concurrencyCap":1.5}}`),
		"dispatch not obj":  writeConfig(t, `{"dispatch":[1]}`),
		"repo cap null":     writeConfig(t, `{"dispatch":{"repos":[{"repo":"o/a","concurrencyCap":null}]}}`),
		"repo cap negative": writeConfig(t, `{"dispatch":{"repos":[{"repo":"o/a","concurrencyCap":-1}]}}`),
		"repo cap string":   writeConfig(t, `{"dispatch":{"repos":[{"repo":"o/a","concurrencyCap":"1"}]}}`),
		"directory":         dir,
		"empty path":        "",
		"too large":         writeConfig(t, `{"x":"`+strings.Repeat("a", 2<<20)+`"}`),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := LoadLimits(path)
			if err == nil {
				t.Fatalf("LoadLimits = %+v, nil; want an error", got)
			}
			if path != "" && strings.Contains(err.Error(), path) {
				t.Errorf("error %q leaks the config path", err)
			}
			for _, banned := range []string{" dispatch ", " skip:"} {
				if strings.Contains(err.Error(), banned) {
					t.Errorf("error %q contains %q", err, banned)
				}
			}
		})
	}
}

func TestLoadLimitsInvalidRepoCapNamesTheKey(t *testing.T) {
	_, err := LoadLimits(writeConfig(t, `{"dispatch":{"repos":[{"repo":"o/a","concurrencyCap":-1}]}}`))
	if err == nil || !strings.Contains(err.Error(), "dispatch.repos[].concurrencyCap") {
		t.Fatalf("error = %v; want it to name dispatch.repos[].concurrencyCap", err)
	}
}

func TestLoadLimitsFollowsASymlinkedConfig(t *testing.T) {
	real := writeConfig(t, `{"dispatch":{"concurrencyCap":7}}`)
	link := filepath.Join(t.TempDir(), "config.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLimits(link)
	if err != nil || got.Machine != 7 {
		t.Fatalf("LoadLimits(symlink) = %+v, %v; want machine 7", got, err)
	}
}

func TestParseRepoCap(t *testing.T) {
	valid := map[string]int{"0": 0, "1": 1, "12": 12}
	for raw, want := range valid {
		got, err := ParseRepoCap(json.RawMessage(raw))
		if err != nil || got == nil || *got != want {
			t.Errorf("ParseRepoCap(%s) = %v, %v; want %d", raw, got, err, want)
		}
	}
	if got, err := ParseRepoCap(nil); err != nil || got != nil {
		t.Errorf("ParseRepoCap(absent) = %v, %v; want nil, nil", got, err)
	}
	for _, raw := range []string{"null", "-1", `"2"`, "1.5", "true", "[1]", "{}"} {
		got, err := ParseRepoCap(json.RawMessage(raw))
		if err == nil || got != nil {
			t.Errorf("ParseRepoCap(%s) = %v, %v; want an error", raw, got, err)
			continue
		}
		if !strings.Contains(err.Error(), "dispatch.repos[].concurrencyCap") {
			t.Errorf("ParseRepoCap(%s) error %q does not name the key", raw, err)
		}
	}
}

// Stricter is the one rule dispatch applies under `--config <alt>`: the lower of
// the two machine caps, and per repo the lower cap, a cap present in only one of
// the two applying as it stands. It is a pure function of two Limits, so the
// rule is pinned here in both directions; the admission tests in internal/dispatch
// drive it through real files.
func TestStricterTakesTheLowerMachineCapInBothDirections(t *testing.T) {
	for _, tc := range []struct{ a, b, want int }{{2, 5, 2}, {5, 2, 2}, {3, 1, 1}, {1, 3, 1}, {3, 3, 3}} {
		if got := Stricter(Limits{Machine: tc.a}, Limits{Machine: tc.b}).Machine; got != tc.want {
			t.Errorf("Stricter(machine %d, machine %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestStricterCombinesRepoCapsAsTheMinimumAndKeepsACapPresentInOneSideOnly(t *testing.T) {
	def := Limits{Machine: 5, Repos: map[string]int{"o/a": 2, "o/b": 1}}
	alt := Limits{Machine: 5, Repos: map[string]int{"o/a": 1, "o/c": 1}}
	for name, got := range map[string]Limits{"default, alt": Stricter(def, alt), "alt, default": Stricter(alt, def)} {
		for repo, want := range map[string]int{"o/a": 1, "o/b": 1, "o/c": 1} {
			if c, ok := got.RepoCap(repo); !ok || c != want {
				t.Errorf("%s: RepoCap(%s) = %d, %v; want %d, true", name, repo, c, ok, want)
			}
		}
		if _, ok := got.RepoCap("o/none"); ok {
			t.Errorf("%s: a repo capped in neither file must stay uncapped", name)
		}
		if len(got.Repos) != 3 {
			t.Errorf("%s: Repos = %v, want exactly the union of both files", name, got.Repos)
		}
	}
}

func TestStricterLeavesBothInputsUntouched(t *testing.T) {
	def := Limits{Machine: 5, Repos: map[string]int{"o/a": 2}}
	alt := Limits{Machine: 1, Repos: map[string]int{"o/a": 1, "o/c": 1}}
	_ = Stricter(def, alt)
	if def.Machine != 5 || len(def.Repos) != 1 || def.Repos["o/a"] != 2 {
		t.Errorf("Stricter mutated its first argument: %+v", def)
	}
	if alt.Machine != 1 || len(alt.Repos) != 2 || alt.Repos["o/a"] != 1 {
		t.Errorf("Stricter mutated its second argument: %+v", alt)
	}
}

func TestStricterWithNilRepoMapsDoesNotPanic(t *testing.T) {
	got := Stricter(Limits{Machine: 3}, Limits{Machine: 2, Repos: map[string]int{"o/a": 1}})
	if c, ok := got.RepoCap("o/a"); !ok || c != 1 || got.Machine != 2 {
		t.Fatalf("Stricter = %+v, want machine 2 and o/a capped at 1", got)
	}
	if got := Stricter(Limits{Machine: 3}, Limits{Machine: 3}); got.Machine != 3 || len(got.Repos) != 0 {
		t.Fatalf("Stricter of two bare limits = %+v, want machine 3 and no repo caps", got)
	}
}

func TestStricterAPauseInEitherFileWins(t *testing.T) {
	def := Limits{Machine: 3, Repos: map[string]int{}}
	alt := Limits{Machine: 0, Repos: map[string]int{"o/a": 2}}
	for name, got := range map[string]Limits{"default, alt": Stricter(def, alt), "alt, default": Stricter(alt, def)} {
		if got.Machine != 0 {
			t.Errorf("%s: Machine = %d, want the pause (0)", name, got.Machine)
		}
	}
	zeroRepo := Stricter(Limits{Machine: 3, Repos: map[string]int{"o/a": 0}}, Limits{Machine: 3, Repos: map[string]int{"o/a": 2}})
	if c, ok := zeroRepo.RepoCap("o/a"); !ok || c != 0 {
		t.Errorf("RepoCap(o/a) = %d, %v; a repo pause in one file must win", c, ok)
	}
}

func TestStricterRepoCapAboveTheEffectiveMachineCapIsClamped(t *testing.T) {
	// The alt file allows o/a 4 workers, but the default file's machine cap is 2.
	got := Stricter(Limits{Machine: 2}, Limits{Machine: 5, Repos: map[string]int{"o/a": 4}})
	if c, ok := got.EffectiveRepoCap("o/a"); !ok || c != 2 {
		t.Fatalf("EffectiveRepoCap(o/a) = %d, %v; want the effective machine cap 2", c, ok)
	}
}
