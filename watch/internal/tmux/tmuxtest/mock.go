// Package tmuxtest provides a mock tmux.Client for tests in the daemon and
// tmux-frontend packages.
package tmuxtest

import (
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
)

// MockClient implements tmux.Client for testing.
type MockClient struct {
	Panes           []tmux.PaneInfo
	ListCalls       int
	Renames         []RenameCall
	WindowOpts      []WindowOptCall
	WindowOptValues map[string]string // key: "target:key" → value
	Opts            []OptCall
	ListErr         error
	RenameErr       error
	WindowOptErr    error
	GetOptErr       error
	OptErr          error
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

// OptCall records a call to SetOption (session-wide/global tmux option).
type OptCall struct {
	Key   string
	Value string
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

// SetOption records a session-wide (global) tmux option set, mirroring
// SetWindowOption's recording pattern.
func (m *MockClient) SetOption(key string, value string) error {
	m.Opts = append(m.Opts, OptCall{key, value})
	return m.OptErr
}

// FindOpt returns the value of the last SetOption call matching key.
func FindOpt(opts []OptCall, key string) (string, bool) {
	for i := len(opts) - 1; i >= 0; i-- {
		if opts[i].Key == key {
			return opts[i].Value, true
		}
	}
	return "", false
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

// LivePaneLister is a fake for the strict live-pane listing (tmux.ListLivePanes):
// it returns Panes and Err, and counts the reads in Calls so a test can prove a
// path never consulted tmux at all.
type LivePaneLister struct {
	Panes []tmux.LivePane
	Err   error
	Calls int
}

// ListLivePanes records the call and returns the configured panes and error.
func (l *LivePaneLister) ListLivePanes() ([]tmux.LivePane, error) {
	l.Calls++
	return l.Panes, l.Err
}

// PaneOptionCall records one SetPaneOption call (a pane-scoped user option
// written through the pane's own socket, #1252).
type PaneOptionCall struct {
	Socket string
	Pane   string
	Key    string
	Value  string
}

// PaneOptionRecorder is a recording stand-in for tmux.SetPaneOption, shaped to
// be installed in the launcher's setPaneOption seam. Every attempt is recorded
// (a failed set included, in call order); ErrByKey makes a set of that option
// key fail with the given error.
type PaneOptionRecorder struct {
	Calls    []PaneOptionCall
	ErrByKey map[string]error
}

// SetPaneOption records the call and returns the error configured for key, if
// any.
func (r *PaneOptionRecorder) SetPaneOption(socket, pane, key, value string) error {
	r.Calls = append(r.Calls, PaneOptionCall{Socket: socket, Pane: pane, Key: key, Value: value})
	return r.ErrByKey[key]
}

// Values returns the recorded value for each option key (the last write wins).
func (r *PaneOptionRecorder) Values() map[string]string {
	out := make(map[string]string, len(r.Calls))
	for _, c := range r.Calls {
		out[c.Key] = c.Value
	}
	return out
}
