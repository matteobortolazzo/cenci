package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
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
// happens to be running in), or a pane id ("%5") that tmux resolves to the
// pane's window and that survives index renumbering (the window-cleanup pass
// kills this way, #1251). A target tmux reports as missing comes back
// wrapping ErrWindowGone, so a caller can count a window that already closed
// itself as closed. Kept off the Client interface for the same
// reason as CurrentSession/IsGroupedSession/NewWindow above: this is a
// launcher/consumer-facing capability, not part of the daemon frontend seam.
// Consumers that only need to kill a window define their own small interface
// (see internal/closecmd.windowKiller) rather than growing Client.
func (c *ExecClient) KillWindow(target string) error {
	_, err := windowCmd("kill-window", "-t", target)
	return err
}

// ErrWindowGone marks a tmux target (window or session) that no longer exists:
// KillWindow and ListWindowPaneProcs wrap it when tmux's stderr says it cannot
// find the window, session or pane. Every other tmux failure (no server, a protocol
// mismatch, a timeout) stays a plain error so a real problem is never read as
// "already closed".
var ErrWindowGone = errors.New("tmux window is gone")

// missingTargetMessages are the tmux stderr texts for a window or session that
// does not exist (watch/docs/tmux.md).
var missingTargetMessages = []string{"can't find window", "can't find session", "can't find pane"}

func isMissingTarget(stderr string) bool {
	return slices.ContainsFunc(missingTargetMessages, func(m string) bool {
		return strings.Contains(stderr, m)
	})
}

// windowCmdTimeout bounds one window-scoped tmux call (list-panes, kill-window)
// so a wedged tmux server can never stall the close path indefinitely; the
// matching WaitDelay covers a lingering grandchild holding the output pipes.
const (
	windowCmdTimeout   = 5 * time.Second
	windowCmdWaitDelay = 2 * time.Second
)

// windowCmd runs `tmux <args...>` against the ambient server with
// windowCmdTimeout. A missing-target failure wraps ErrWindowGone; tmux's own
// message is kept in every error.
func windowCmd(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), windowCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tmux", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = windowCmdWaitDelay
	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}
	if ctx.Err() == context.DeadlineExceeded || errors.Is(err, exec.ErrWaitDelay) {
		err = errors.Join(err, context.DeadlineExceeded)
	}
	text := strings.TrimSpace(stderr.String())
	if isMissingTarget(text) {
		return "", fmt.Errorf("tmux %s: %w: %w: %s", args[0], err, ErrWindowGone, text)
	}
	return "", fmt.Errorf("tmux %s: %w: %s", args[0], err, text)
}

// PaneProc is one pane of a window as ListWindowPaneProcs reads it: the raw,
// unvalidated tmux fields. The launch options are empty for a pane that never
// had them set.
type PaneProc struct {
	PaneID, PanePID, LaunchID, Container, Runtime string
}

// paneProcFormat is the `list-panes -F` format ListWindowPaneProcs requests for
// the options: five TAB-separated columns, matching PaneProc's fields in order.
const paneProcFormat = "#{pane_id}\t#{pane_pid}\t#{@cenci-launch-id}\t#{@cenci-container}\t#{@cenci-runtime}"

// paneOwnedFormat lists only the fields tmux itself owns. No pane option can put
// a newline or tab into them, so this listing is the authority on which
// (pane_id, pane_pid) pairs exist.
const paneOwnedFormat = "#{pane_id}\t#{pane_pid}"

// paneProcFields is the column count of paneProcFormat.
const paneProcFields = 5

// ListWindowPaneProcs lists the panes of the window at target with their pid
// and recorded launch options, against the same ambient server KillWindow uses.
// It returns the raw fields: validating their values is the caller's job. A
// missing target wraps ErrWindowGone.
//
// The options are user-writable and printed raw, so a newline or tab inside one
// can forge extra, well-formed five-field rows with an attacker-chosen pane_pid.
// The window is therefore listed twice: once for tmux's own fields alone, then
// with the options. An option row is trusted only if its (pane_id, pane_pid)
// matches a tmux-owned pair exactly and its pane_id appears once in both
// listings. Every other row (forged, mismatched, duplicated, malformed) is
// dropped, and so is a tmux-owned pane left without a trusted row; each is
// reported in the returned error alongside the panes that did pass, so one bad
// row neither hides nor shields its siblings. Only the trailing newline is
// trimmed: a pane with unset options prints trailing TABs that TrimSpace would
// swallow (watch/docs/tmux.md).
func ListWindowPaneProcs(target string) ([]PaneProc, error) {
	ownedOut, err := windowCmd("list-panes", "-t", target, "-F", paneOwnedFormat)
	if err != nil {
		return nil, err
	}
	ownedOut = strings.TrimRight(ownedOut, "\n")
	if ownedOut == "" {
		return nil, nil
	}
	optionOut, err := windowCmd("list-panes", "-t", target, "-F", paneProcFormat)
	if err != nil {
		return nil, err
	}

	var errs []error
	ownedPID := map[string]string{}
	ownedCount := map[string]int{}
	var ownedOrder []string
	for _, line := range strings.Split(ownedOut, "\n") {
		id, pid, ok := strings.Cut(line, "\t")
		if !ok || strings.Contains(pid, "\t") {
			errs = append(errs, fmt.Errorf("tmux list-panes: unexpected line %q: want pane_id and pane_pid", line))
			continue
		}
		if ownedCount[id] == 0 {
			ownedOrder = append(ownedOrder, id)
		}
		ownedPID[id] = pid
		ownedCount[id]++
	}

	var rows []PaneProc
	rowCount := map[string]int{}
	for _, line := range strings.Split(strings.TrimRight(optionOut, "\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != paneProcFields {
			errs = append(errs, fmt.Errorf("tmux list-panes: unexpected line %q: want %d TAB-separated fields, got %d", line, paneProcFields, len(f)))
			continue
		}
		rows = append(rows, PaneProc{PaneID: f[0], PanePID: f[1], LaunchID: f[2], Container: f[3], Runtime: f[4]})
		rowCount[f[0]]++
	}

	var panes []PaneProc
	accepted := map[string]bool{}
	for _, r := range rows {
		switch {
		case ownedCount[r.PaneID] == 0:
			errs = append(errs, fmt.Errorf("tmux list-panes: ignoring row for pane %q (pane_pid %q): tmux does not list that pane", r.PaneID, r.PanePID))
		case ownedCount[r.PaneID] > 1 || rowCount[r.PaneID] > 1:
			errs = append(errs, fmt.Errorf("tmux list-panes: ignoring row for pane %q (pane_pid %q): the pane id is listed more than once", r.PaneID, r.PanePID))
		case ownedPID[r.PaneID] != r.PanePID:
			errs = append(errs, fmt.Errorf("tmux list-panes: ignoring row for pane %q: pane_pid %q does not match tmux's own %q", r.PaneID, r.PanePID, ownedPID[r.PaneID]))
		default:
			panes = append(panes, r)
			accepted[r.PaneID] = true
		}
	}
	for _, id := range ownedOrder {
		if ownedCount[id] == 1 && !accepted[id] && rowCount[id] == 0 {
			errs = append(errs, fmt.Errorf("tmux list-panes: pane %q has no matching option row; nothing signaled for it", id))
		}
	}
	return panes, errors.Join(errs...)
}

// setPaneOptionTimeout bounds one SetPaneOption call so a wedged tmux server
// can never stall a sandbox launch indefinitely.
const setPaneOptionTimeout = 5 * time.Second

// setPaneOptionWaitDelay bounds how long Cmd.Wait can block after the timeout
// has killed tmux, in case a lingering grandchild keeps the output pipes open
// (watch/docs/go-gotchas.md).
const setPaneOptionWaitDelay = 2 * time.Second

// SetPaneOption sets the pane-scoped user option key (e.g. @cenci-launch-id)
// on pane through the server at socket: `tmux -S <socket> set-option -p -t
// <pane> -- <key> <value>`. Pane options need tmux 3.0+; on older tmux the
// call fails and the caller decides how to degrade. The explicit -S and -t
// mean it never depends on the ambient $TMUX/$TMUX_PANE ownership resolution
// (watch/docs/tmux.md). The socket is the caller's own trusted $TMUX socket, so
// unlike the reaper's queries against container-supplied sockets the child env
// is inherited rather than allowlisted. A package function (not a method on
// ExecClient or the daemon-facing Client interface): the sandbox launcher holds
// no tmux client.
func SetPaneOption(socket, pane, key, value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), setPaneOptionTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tmux", "-S", socket, "set-option", "-p", "-t", pane, "--", key, value)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = setPaneOptionWaitDelay
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() == context.DeadlineExceeded || errors.Is(err, exec.ErrWaitDelay) {
		err = errors.Join(err, context.DeadlineExceeded)
	}
	return fmt.Errorf("tmux set-option %s: %w: %s", key, err, strings.TrimSpace(stderr.String()))
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
