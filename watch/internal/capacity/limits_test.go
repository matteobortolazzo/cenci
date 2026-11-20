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
