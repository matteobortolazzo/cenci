package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// PaneInfo holds the relevant fields from a tmux pane listing.
type PaneInfo struct {
	SessionName    string
	WindowIndex    string
	WindowName     string
	PaneIndex      string
	PaneCurrentCmd string
	PaneTitle      string
	PaneID         string // e.g. %5
}

// target returns the tmux target string for this pane (session:window.pane).
func (p PaneInfo) target() string {
	return fmt.Sprintf("%s:%s.%s", p.SessionName, p.WindowIndex, p.PaneIndex)
}

// WindowTarget returns the tmux target string for this pane's window.
func (p PaneInfo) WindowTarget() string {
	return fmt.Sprintf("%s:%s", p.SessionName, p.WindowIndex)
}

// Client is the interface for interacting with tmux.
type Client interface {
	ListPanes() ([]PaneInfo, error)
	RenameWindow(target string, name string) error
	SetWindowOption(target string, key string, value string) error
	GetWindowOption(target string, key string) (string, error)
	// SetOption sets a session-wide (global) tmux option, e.g. for user
	// variables like @cenci-headroom-<agent> that aren't scoped to a
	// single window.
	SetOption(key string, value string) error
}

// ExecClient implements Client by shelling out to the tmux binary.
type ExecClient struct{}

const listFormat = "#{session_name}\t#{window_index}\t#{window_name}\t#{pane_index}\t#{pane_current_command}\t#{pane_title}\t#{pane_id}"

func (c *ExecClient) ListPanes() ([]PaneInfo, error) {
	out, err := tmuxCmd("list-panes", "-a", "-F", listFormat)
	if err != nil {
		return nil, err
	}
	return parsePanes(out), nil
}

func (c *ExecClient) RenameWindow(target string, name string) error {
	_, err := tmuxCmd("rename-window", "-t", target, name)
	return err
}

func (c *ExecClient) SetWindowOption(target string, key string, value string) error {
	_, err := tmuxCmd("set-window-option", "-t", target, key, value)
	return err
}

func (c *ExecClient) GetWindowOption(target string, key string) (string, error) {
	out, err := tmuxCmd("show-window-option", "-t", target, "-v", key)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// SetOption sets a session-wide (global) tmux option.
func (c *ExecClient) SetOption(key string, value string) error {
	_, err := tmuxCmd("set-option", "-g", key, value)
	return err
}

// CurrentSession returns the name of the tmux session the caller's pane
// belongs to, resolved via the inherited $TMUX_PANE environment variable.
// It errors when $TMUX_PANE is unset/blank (no tmux pane context) or when
// it names a pane that no longer exists.
//
// These launcher-facing methods are intentionally kept OFF the daemon-facing
// Client interface so the frontend seam stays unchanged; the run package
// defines its own small consumer interface that *ExecClient satisfies.
func (c *ExecClient) CurrentSession() (string, error) {
	pane := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	if pane == "" {
		return "", fmt.Errorf("TMUX_PANE is not set; not running inside a tmux pane")
	}
	out, err := tmuxCmd("display-message", "-t", pane, "-p", "#{session_name}")
	if err != nil {
		return "", err
	}
	session := strings.TrimSpace(out)
	if session == "" {
		// tmux display-message -p exits 0 with empty output when -t names a
		// pane that no longer exists, rather than erroring like most other
		// tmux subcommands do with an invalid target.
		return "", fmt.Errorf("tmux could not resolve a session for pane %q", pane)
	}
	return session, nil
}

// IsGroupedSession reports whether the given exact session target is part of
// a session group. New windows propagate to every session in a group, so the
// launcher refuses to spawn into one.
func (c *ExecClient) IsGroupedSession(session string) (bool, error) {
	out, err := tmuxCmd("display-message", "-t", session, "-p", "#{session_grouped}")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "1", nil
}

// NewWindow creates a detached window named name in the given exact session
// target running shellCommand. shellCommand is passed to tmux as a single
// argument, so it must already be a valid shell command line (see
// run.shellJoin).
func (c *ExecClient) NewWindow(session, name, shellCommand string) error {
	_, err := tmuxCmd("new-window", "-d", "-t", session+":", "-n", name, shellCommand)
	return err
}

// HasSession reports whether session exists on the tmux server (#927), via
// `tmux has-session -t =<name>`. The leading `=` forces an exact match --
// without it, tmux prefix-matches, so a configured "work" would falsely
// resolve against an unrelated "work-2". A nonzero tmux exit (no such
// session, or no server running at all) classifies as (false, nil): a normal
// negative result, not a probe failure. A failure to run tmux at all (e.g.
// the binary isn't resolvable) is a distinct (false, non-nil error)
// classification, kept off the daemon-facing Client interface for the same
// launcher/consumer-facing reason as CurrentSession/IsGroupedSession/NewWindow
// above.
func (c *ExecClient) HasSession(session string) (bool, error) {
	_, err := tmuxCmd("has-session", "-t", "="+session)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// KillWindow kills the tmux window at target (typically an exact
// "=SESSION:INDEX" target resolved from the daemon's window registry, so it
// unambiguously identifies one window regardless of which session the caller
// happens to be running in). Kept off the Client interface for the same
// reason as CurrentSession/IsGroupedSession/NewWindow above: this is a
// launcher/consumer-facing capability, not part of the daemon frontend seam.
// Consumers that only need to kill a window define their own small interface
// (see internal/closecmd.windowKiller) rather than growing Client.
func (c *ExecClient) KillWindow(target string) error {
	_, err := tmuxCmd("kill-window", "-t", target)
	return err
}

func tmuxCmd(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// parsePanes parses the tab-separated output of tmux list-panes.
func parsePanes(output string) []PaneInfo {
	var panes []PaneInfo
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 7)
		if len(fields) < 7 {
			continue
		}
		panes = append(panes, PaneInfo{
			SessionName:    fields[0],
			WindowIndex:    fields[1],
			WindowName:     fields[2],
			PaneIndex:      fields[3],
			PaneCurrentCmd: fields[4],
			PaneTitle:      fields[5],
			PaneID:         fields[6],
		})
	}
	return panes
}
