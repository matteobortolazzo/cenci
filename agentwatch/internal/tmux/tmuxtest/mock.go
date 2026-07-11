// Package tmuxtest provides a mock tmux.Client for tests in the daemon and
// tmux-frontend packages.
package tmuxtest

import (
	"github.com/matteobortolazzo/agent-stack/agentwatch/internal/tmux"
)

// MockClient implements tmux.Client for testing.
type MockClient struct {
	Panes           []tmux.PaneInfo
	ListCalls       int
	Renames         []RenameCall
	WindowOpts      []WindowOptCall
	WindowOptValues map[string]string // key: "target:key" → value
	ListErr         error
	RenameErr       error
	WindowOptErr    error
	GetOptErr       error
}

type RenameCall struct {
	Target string
	Name   string
}

type WindowOptCall struct {
	Target string
	Key    string
	Value  string
}

func (m *MockClient) ListPanes() ([]tmux.PaneInfo, error) {
	m.ListCalls++
	return m.Panes, m.ListErr
}

func (m *MockClient) RenameWindow(target string, name string) error {
	m.Renames = append(m.Renames, RenameCall{target, name})
	if m.RenameErr == nil {
		for i := range m.Panes {
			if m.Panes[i].WindowTarget() == target {
				m.Panes[i].WindowName = name
			}
		}
	}
	return m.RenameErr
}

func (m *MockClient) SetWindowOption(target string, key string, value string) error {
	m.WindowOpts = append(m.WindowOpts, WindowOptCall{target, key, value})
	if m.WindowOptValues == nil {
		m.WindowOptValues = make(map[string]string)
	}
	m.WindowOptValues[target+":"+key] = value
	return m.WindowOptErr
}

func (m *MockClient) GetWindowOption(target string, key string) (string, error) {
	if m.GetOptErr != nil {
		return "", m.GetOptErr
	}
	k := target + ":" + key
	if v, ok := m.WindowOptValues[k]; ok {
		return v, nil
	}
	if key == "automatic-rename" {
		return "on", nil
	}
	return "", nil
}

// FindWindowOpt returns the value of the last SetWindowOption call matching target and key.
func FindWindowOpt(opts []WindowOptCall, target, key string) (string, bool) {
	for i := len(opts) - 1; i >= 0; i-- {
		if opts[i].Target == target && opts[i].Key == key {
			return opts[i].Value, true
		}
	}
	return "", false
}

// LastRename returns the last rename call for a given target.
func LastRename(renames []RenameCall, target string) (string, bool) {
	for i := len(renames) - 1; i >= 0; i-- {
		if renames[i].Target == target {
			return renames[i].Name, true
		}
	}
	return "", false
}
