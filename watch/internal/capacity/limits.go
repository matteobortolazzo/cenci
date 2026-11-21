package capacity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

// DefaultMachineCap is the machine worker limit when the config file or its
// dispatch.concurrencyCap key is absent. It must equal the default of the
// dispatch package's DefaultConfig.
const DefaultMachineCap = 3

// maxConfigBytes bounds the fleet config read; a larger file is unreadable.
const maxConfigBytes = 1 << 20

// repoCapKey names the per-repo cap in every error about it.
const repoCapKey = "dispatch.repos[].concurrencyCap"

// Limits are the configured worker ceilings: Machine is dispatch.concurrencyCap
// and Repos maps a repository ("owner/name") to its lower
// dispatch.repos[].concurrencyCap. A repo without a key has no repo ceiling.
type Limits struct {
	Machine int
	Repos   map[string]int
}

// RepoCap returns the configured cap of repo, matched ignoring case.
func (l Limits) RepoCap(repo string) (int, bool) {
	for name, c := range l.Repos {
		if strings.EqualFold(name, repo) {
			return c, true
		}
	}
	return 0, false
}

// EffectiveRepoCap is min(repo cap, machine cap). ok is false for a repo
// without a cap.
func (l Limits) EffectiveRepoCap(repo string) (int, bool) {
	c, ok := l.RepoCap(repo)
	if !ok {
		return 0, false
	}
	return min(c, l.Machine), true
}

// Stricter returns the limits a process admits under when two fleet config files
// both bind it: the lower machine cap, and per repo the lower cap, a cap present
// in only one of the two applying as it stands. A pause (a cap of 0) in either
// file therefore wins. Neither argument is modified.
func Stricter(a, b Limits) Limits {
	out := Limits{Machine: min(a.Machine, b.Machine), Repos: make(map[string]int, len(a.Repos)+len(b.Repos))}
	for _, src := range []map[string]int{a.Repos, b.Repos} {
		for repo, c := range src {
			key := strings.ToLower(repo)
			if prev, ok := out.Repos[key]; ok {
				c = min(c, prev)
			}
			out.Repos[key] = c
		}
	}
	return out
}

// ParseRepoCap validates one dispatch.repos[].concurrencyCap value: nil when
// the key is absent, else an integer >= 0 (0 admits nothing for that repo). A
// null, negative, fractional or non-numeric value is an error naming the key,
// never a silent default. Dispatch's LoadConfig and LoadLimits both call it, so
// the error text has one source.
func ParseRepoCap(raw json.RawMessage) (*int, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	const form = " must be an integer >= 0"
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New(repoCapKey + form + ", got null")
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%s%s: %w", repoCapKey, form, err)
	}
	if v < 0 {
		return nil, fmt.Errorf("%s%s, got %d", repoCapKey, form, v)
	}
	return &v, nil
}

// fleetLimits is the part of the fleet config LoadLimits reads. It decodes the
// way dispatch's own config does, so both read the same value from one file.
type fleetLimits struct {
	Dispatch *struct {
		ConcurrencyCap *int `json:"concurrencyCap"`
		Repos          []struct {
			Repo           string          `json:"repo"`
			ConcurrencyCap json.RawMessage `json:"concurrencyCap"`
		} `json:"repos"`
	} `json:"dispatch"`
}

// LoadLimits reads the limits from the fleet config at path. An absent file, or
// an absent dispatch.concurrencyCap, is DefaultMachineCap. A machine cap below
// zero is read as 0, the pause. An unreadable or invalid file is an error: the
// caller must admit nothing and report the reason, which is path-free.
func LoadLimits(path string) (Limits, error) {
	limits := Limits{Machine: DefaultMachineCap, Repos: map[string]int{}}
	if path == "" {
		return Limits{}, errors.New("the config path is unresolved")
	}
	data, err := readConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return limits, nil
	}
	if err != nil {
		return Limits{}, err
	}
	var f fleetLimits
	if err := json.Unmarshal(data, &f); err != nil {
		return Limits{}, fmt.Errorf("the config file does not hold valid concurrency limits: %w", err)
	}
	if f.Dispatch == nil {
		return limits, nil
	}
	if f.Dispatch.ConcurrencyCap != nil {
		limits.Machine = max(*f.Dispatch.ConcurrencyCap, 0)
	}
	for _, r := range f.Dispatch.Repos {
		c, err := ParseRepoCap(r.ConcurrencyCap)
		if err != nil {
			return Limits{}, err
		}
		if c == nil || r.Repo == "" {
			continue
		}
		if prev, ok := limits.RepoCap(r.Repo); ok {
			*c = min(*c, prev)
		}
		limits.Repos[strings.ToLower(r.Repo)] = *c
	}
	return limits, nil
}

// readConfig reads a regular file of at most maxConfigBytes. A symlink is
// followed (config files are often linked from a dotfiles repo), but the open
// handle is checked, so a FIFO or device cannot block or exhaust memory.
func readConfig(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, errors.New("the config file cannot be opened")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("the config file is not a regular file")
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, errors.New("the config file cannot be read")
	}
	if len(buf) > maxConfigBytes {
		return nil, errors.New("the config file is over the size bound")
	}
	return buf, nil
}
