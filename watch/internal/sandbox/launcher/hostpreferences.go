package launcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/pelletier/go-toml/v2"
)

type PreferencePosture struct {
	Source     string   `json:"source"`
	Selected   []string `json:"selected"`
	Skipped    []string `json:"skipped"`
	Imported   []string `json:"imported,omitempty"`
	Overridden []string `json:"overridden,omitempty"`
	Status     string   `json:"status"`
}

type hostPreferences struct {
	PreferencePosture
	values map[string]any
}

var modelID = regexp.MustCompile(`^[[:alnum:]][[:alnum:]_.-]{0,127}(\[1m\])?$`)
var safeKeyName = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9.-]{0,63}$`)

func allowedPreference(agent, key string, value any) (allowed, valid bool) {
	s, _ := value.(string)
	switch {
	case key == "model":
		return true, modelID.MatchString(s)
	case agent == "claude" && key == "effortLevel":
		return true, slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, s)
	case agent == "claude" && key == "showTurnDuration":
		_, ok := value.(bool)
		return true, ok
	case agent == "codex" && key == "model_reasoning_effort":
		return true, slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}, s)
	case agent == "codex" && key == "model_verbosity":
		return true, slices.Contains([]string{"low", "medium", "high"}, s)
	default:
		return false, false
	}
}

func resolvePreferences(raw json.RawMessage, agent string, scope Scope, home string) (hostPreferences, error) {
	var prefs hostPreferences
	if len(raw) == 0 {
		return prefs, nil
	}
	var selection struct {
		Source string `json:"source"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&selection); err != nil || selection.Source == "" {
		return prefs, errors.New("sandboxHostProfiles preferences must specify a source path")
	}
	path := selection.Source
	if err := checkHostProfilePath(path, home, scope); err != nil {
		return prefs, err
	}
	if agent == "claude" && filepath.Base(path) != "settings.json" || agent == "codex" && filepath.Base(path) != "config.toml" {
		return prefs, fmt.Errorf("invalid %s preference source filename %q", agent, path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return prefs, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return prefs, err
	}
	if err := validatePersonalFile(path, info); err != nil {
		return prefs, err
	}
	const limit = 4 << 20
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return prefs, err
	}
	if len(data) > limit {
		return prefs, fmt.Errorf("preference source %s exceeds 4 MiB", path)
	}
	var input map[string]any
	if agent == "claude" {
		err = json.Unmarshal(data, &input)
	} else {
		err = toml.Unmarshal(data, &input)
	}
	if err != nil || (agent == "claude" && input == nil) {
		return prefs, fmt.Errorf("%s: malformed %s preference configuration", path, agent)
	}
	prefs.Source = path
	prefs.Status = "planned"
	prefs.values = map[string]any{}
	prefs.Selected = []string{}
	prefs.Skipped = []string{}
	for key, value := range input {
		allowed, valid := allowedPreference(agent, key, value)
		if !allowed {
			if !safeKeyName.MatchString(key) {
				key = "<other>"
			}
			prefs.Skipped = append(prefs.Skipped, key)
			continue
		}
		if !valid {
			return hostPreferences{}, fmt.Errorf("%s: unsupported value for preference %s", path, key)
		}
		prefs.values[key] = value
		prefs.Selected = append(prefs.Selected, key)
	}
	slices.Sort(prefs.Selected)
	slices.Sort(prefs.Skipped)
	return prefs, nil
}

func (p *PreferencePosture) writeText(w io.Writer) {
	if p == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "\nPersonal preferences (%s) from %s\n", p.Status, p.Source)
	for _, group := range []struct {
		name string
		keys []string
	}{{"selected", p.Selected}, {"skipped", p.Skipped}, {"imported", p.Imported}, {"locally overridden", p.Overridden}} {
		if len(group.keys) > 0 {
			_, _ = fmt.Fprintf(w, "  %s  %s\n", group.name, strings.Join(group.keys, ", "))
		}
	}
}
