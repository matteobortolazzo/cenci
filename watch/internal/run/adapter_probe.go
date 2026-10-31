package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// codexPluginID is the marketplace-qualified id of the flow plugin in Codex.
const codexPluginID = "cenci@cenci"

// adapterProbeWaitDelay bounds how long Wait can block after the probe's codex
// process has exited or been killed, mirroring gitWaitDelay in
// internal/dispatch/mainsync.go: a grandchild holding the pipes open must not
// defeat the timeout. A variable only so the WaitDelay test can shorten it.
var adapterProbeWaitDelay = 5 * time.Second

// maxProbeStderr bounds the stderr text copied into a refusal message.
const maxProbeStderr = 300

// adapterProbeTimeout bounds one `codex plugin list --json` call, so a hung
// Codex delays a host launch by at most this long and then fails closed. A
// variable only so the timeout test can shorten it.
var adapterProbeTimeout = 30 * time.Second

// codexPluginList is the decoded part of `codex plugin list --json`:
// `.installed[]` entries matched on pluginId, the same fields install.sh and
// sandbox/lib/migrate-settings.sh read. Installed and Enabled are pointers so
// an absent field is distinguishable from false: both must be explicitly true.
// Installed is a pointer to a slice so an absent (or null) top-level key, which
// means the JSON shape is unrecognised, is distinguishable from an empty array,
// which means nothing is installed.
type codexPluginList struct {
	Installed *[]codexPluginEntry `json:"installed"`
}

type codexPluginEntry struct {
	PluginID  string `json:"pluginId"`
	Version   string `json:"version"`
	Installed *bool  `json:"installed"`
	Enabled   *bool  `json:"enabled"`
}

// refuseAdapter builds the ErrAdapterUnavailable refusal: the class-specific
// detail, the plugin, the workflow and the remedy.
func refuseAdapter(workflow, format string, args ...any) error {
	return fmt.Errorf("%w: "+format+" (workflow %q); install or repair it with `codex plugin add %s`, "+
		"set plugins.\"%s\".enabled = true in the Codex config, or launch in the sandbox",
		append(append([]any{ErrAdapterUnavailable}, args...), workflow, codexPluginID, codexPluginID)...)
}

// probeCodexAdapter verifies, before a host Codex launch is spawned, that
// cenci@cenci is installed and enabled in the Codex that `command` runs and
// that its installed plugin root holds adapter (a non-empty regular file). It
// fails closed: every unresolvable fact refuses with ErrAdapterUnavailable.
func probeCodexAdapter(command, workflow, adapter string) error {
	list, err := listCodexPlugins(command, workflow)
	if err != nil {
		return err
	}

	if list.Installed == nil {
		return refuseAdapter(workflow, "unrecognised plugin list shape (no top-level `installed` array); "+
			"the Codex plugin-list JSON shape may have changed")
	}

	var matches []codexPluginEntry
	for _, e := range *list.Installed {
		if e.PluginID == codexPluginID {
			matches = append(matches, e)
		}
	}
	switch {
	case len(matches) == 0:
		return refuseAdapter(workflow, "%s is not installed in Codex", codexPluginID)
	case len(matches) > 1:
		return refuseAdapter(workflow, "codex plugin list reports multiple entries for %s, so the installed copy is ambiguous", codexPluginID)
	}
	entry := matches[0]
	switch {
	case entry.Installed == nil:
		return refuseAdapter(workflow, `the %s entry lacks an explicit "installed" field`, codexPluginID)
	case !*entry.Installed:
		return refuseAdapter(workflow, "%s is not installed in Codex", codexPluginID)
	case entry.Enabled == nil:
		return refuseAdapter(workflow, `the %s entry lacks an explicit "enabled" field`, codexPluginID)
	case !*entry.Enabled:
		return refuseAdapter(workflow, "%s is disabled in Codex", codexPluginID)
	}
	if !safeVersionSegment(entry.Version) {
		return refuseAdapter(workflow, "%s reports an unsafe version %q", codexPluginID, entry.Version)
	}
	if !filepath.IsLocal(adapter) {
		return refuseAdapter(workflow, "the declared adapter %q is not a path inside the plugin", adapter)
	}

	home, err := codexHome()
	if err != nil {
		return refuseAdapter(workflow, "%v", err)
	}
	root := filepath.Join(home, "plugins", "cache", "cenci", "cenci", entry.Version)
	info, err := os.Stat(root)
	if err == nil && !info.IsDir() {
		err = errors.New("not a directory")
	}
	if err != nil {
		return refuseAdapter(workflow, "the installed plugin root %s is not readable (%v)", root, err)
	}
	path := filepath.Join(root, adapter)
	info, err = os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return refuseAdapter(workflow, "adapter %s is missing from the installed plugin root %s", adapter, root)
	}
	if err != nil {
		return refuseAdapter(workflow, "cannot read adapter %s in %s: %v", adapter, root, err)
	}
	if !info.Mode().IsRegular() {
		return refuseAdapter(workflow, "adapter %s in %s is not a regular file", adapter, root)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return refuseAdapter(workflow, "cannot read adapter %s in %s: %v", adapter, root, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return refuseAdapter(workflow, "adapter %s in %s is empty", adapter, root)
	}
	return nil
}

// listCodexPlugins runs `<command> plugin list --json` under the probe timeout
// and decodes its stdout. Any failure -- a missing binary, a timeout, a
// non-zero exit, unparseable output -- is an ErrAdapterUnavailable refusal.
func listCodexPlugins(command, workflow string) (codexPluginList, error) {
	var list codexPluginList
	ctx, cancel := context.WithTimeout(context.Background(), adapterProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, "plugin", "list", "--json")
	cmd.WaitDelay = adapterProbeWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	// Capture the context state before the deferred cancel can change it: a
	// deadline kill surfaces as a bare "signal: killed" or ErrWaitDelay
	// (watch/docs/go-gotchas.md, bounded-subprocess rule).
	if ctx.Err() == context.DeadlineExceeded {
		return list, refuseAdapter(workflow, "`%s plugin list --json` timed out after %s (%w)",
			command, adapterProbeTimeout, context.DeadlineExceeded)
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		return list, refuseAdapter(workflow, "`%s plugin list --json` exited but a child process kept its output pipes open for %s",
			command, adapterProbeWaitDelay)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return list, refuseAdapter(workflow, "`%s plugin list --json` exited with status %d: %s",
			command, exitErr.ExitCode(), boundedStderr(stderr.String()))
	}
	if err != nil {
		return list, refuseAdapter(workflow, "could not run `%s plugin list --json`: %s", command, printable(err.Error()))
	}
	if err := json.Unmarshal(stdout.Bytes(), &list); err != nil {
		return list, refuseAdapter(workflow, "could not parse the output of `%s plugin list --json`: %s", command, printable(err.Error()))
	}
	return list, nil
}

// printable replaces every non-printable rune (control bytes, invalid UTF-8)
// with '?': text from the Codex process or its decoder ends up in a refusal
// printed to a terminal, so it must not carry escape sequences.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			return '?'
		}
		return r
	}, s)
}

// boundedStderr collapses s to one line, replaces non-printable runes and cuts
// it at a rune boundary.
func boundedStderr(s string) string {
	s = printable(strings.Join(strings.Fields(s), " "))
	if len(s) <= maxProbeStderr {
		return s
	}
	cut := maxProbeStderr
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "...(truncated)"
}

// codexHome resolves ${CODEX_HOME:-$HOME/.codex}. It must be absolute: a
// relative home would resolve against whatever cwd the launch has.
func codexHome() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		if !filepath.IsAbs(home) {
			return "", fmt.Errorf("CODEX_HOME must be an absolute path, got %q", home)
		}
		return filepath.Clean(home), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate the Codex home: CODEX_HOME is unset and the home directory is unavailable: %v", err)
	}
	if home == "" {
		return "", errors.New("cannot locate the Codex home: CODEX_HOME and HOME are unset")
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("cannot locate the Codex home: HOME must be an absolute path, got %q", home)
	}
	return filepath.Join(home, ".codex"), nil
}

// safeVersionSegment reports whether v is a single, plain path segment: it is
// spliced into the plugin cache path, so no separator, dot segment or other
// character outside a conservative version alphabet is accepted.
func safeVersionSegment(v string) bool {
	if v == "" || v == "." || v == ".." {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == '+':
		default:
			return false
		}
	}
	return true
}
