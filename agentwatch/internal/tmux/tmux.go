package tmux

import (
	"bytes"
	"fmt"
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
