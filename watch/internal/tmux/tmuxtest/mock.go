// Package tmuxtest provides a mock tmux.Client for tests in the daemon and
// tmux-frontend packages.
package tmuxtest

import (
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
)

// MockClient implements tmux.Client for testing.
//
// The empty socket is the ambient (default) server: Panes, ListErr and
// WindowOptValues describe it, exactly as before sockets existed. A non-empty
// socket is a separate server with its own pane list (PanesBySocket), listing
// error (ListErrBySocket) and window options (SocketWindowOptValues): a pane-id
// target and a rename resolve only inside the call's own socket's pane list, so
// the same pane id on two sockets is two panes, as in real tmux.
type MockClient struct {
	Panes           []tmux.PaneInfo
	ListCalls       int
	Renames         []RenameCall
	WindowOpts      []WindowOptCall
	WindowOptValues map[string]string // ambient server; key: "target:key" → value
	Opts            []OptCall
	// ListErr is the ambient server's listing error; ListErrBySocket's entry for
	// a non-empty socket takes the place of it for that socket.
	ListErr      error
	RenameErr    error
	WindowOptErr error
	GetOptErr    error
	OptErr       error
	// Sockets records the socket each call targeted, in call order across all
	// five methods ("" is the ambient server).
	Sockets []string

	// PanesBySocket is the pane list of each non-empty socket.
	PanesBySocket map[string][]tmux.PaneInfo
	// ListErrBySocket makes ListPanes on that socket fail.
	ListErrBySocket map[string]error
	// ListSockets records the socket of each ListPanes call, in call order.
	ListSockets []string
	// SocketWindowOptValues holds the window options of each non-empty socket;
	// key: socket → "target:key" → value. The ambient server's stay in
	// WindowOptValues.
	SocketWindowOptValues map[string]map[string]string
}

type RenameCall struct {
	Socket string
	Target string
	Name   string
}

type WindowOptCall struct {
	Socket string
	Target string
	Key    string
	Value  string
}

// OptCall records a call to SetOption (session-wide/global tmux option).
type OptCall struct {
	Socket string
	Key    string
	Value  string
}

// panesFor returns the pane list of the given socket's server.
func (m *MockClient) panesFor(socket string) []tmux.PaneInfo {
	if socket == "" {
		return m.Panes
	}
	return m.PanesBySocket[socket]
}

func (m *MockClient) ListPanes(socket string) ([]tmux.PaneInfo, error) {
	m.Sockets = append(m.Sockets, socket)
	m.ListSockets = append(m.ListSockets, socket)
	m.ListCalls++
	if socket == "" {
		return m.Panes, m.ListErr
	}
	return m.PanesBySocket[socket], m.ListErrBySocket[socket]
}

func (m *MockClient) RenameWindow(socket, target string, name string) error {
	m.Sockets = append(m.Sockets, socket)
	target = m.windowTarget(socket, target)
	m.Renames = append(m.Renames, RenameCall{Socket: socket, Target: target, Name: name})
	if m.RenameErr == nil {
		panes := m.panesFor(socket)
		for i := range panes {
			if panes[i].WindowTarget() == target {
				panes[i].WindowName = name
			}
		}
	}
	return m.RenameErr
}

func (m *MockClient) SetWindowOption(socket, target string, key string, value string) error {
	m.Sockets = append(m.Sockets, socket)
	target = m.windowTarget(socket, target)
	m.WindowOpts = append(m.WindowOpts, WindowOptCall{Socket: socket, Target: target, Key: key, Value: value})
	if socket == "" {
		if m.WindowOptValues == nil {
			m.WindowOptValues = make(map[string]string)
		}
		m.WindowOptValues[target+":"+key] = value
		return m.WindowOptErr
	}
	if m.SocketWindowOptValues == nil {
		m.SocketWindowOptValues = make(map[string]map[string]string)
	}
	if m.SocketWindowOptValues[socket] == nil {
		m.SocketWindowOptValues[socket] = make(map[string]string)
	}
	m.SocketWindowOptValues[socket][target+":"+key] = value
	return m.WindowOptErr
}

func (m *MockClient) GetWindowOption(socket, target string, key string) (string, error) {
	m.Sockets = append(m.Sockets, socket)
	target = m.windowTarget(socket, target)
	if m.GetOptErr != nil {
		return "", m.GetOptErr
	}
	values := m.WindowOptValues
	if socket != "" {
		values = m.SocketWindowOptValues[socket]
	}
	k := target + ":" + key
	if v, ok := values[k]; ok {
		return v, nil
	}
	if key == "automatic-rename" {
		return "on", nil
	}
	return "", nil
}

// Like tmux, window commands accept a pane ID as the stable identity of its
// owning window. Record the resolved window so existing behavior assertions
// continue to describe the window that was actually changed. The pane id is
// resolved inside the call's own socket's pane list only.
func (m *MockClient) windowTarget(socket, target string) string {
	for _, p := range m.panesFor(socket) {
		if p.PaneID == target {
			return p.WindowTarget()
		}
	}
	return target
}

// SetOption records a session-wide (global) tmux option set, mirroring
// SetWindowOption's recording pattern.
func (m *MockClient) SetOption(socket, key string, value string) error {
	m.Sockets = append(m.Sockets, socket)
	m.Opts = append(m.Opts, OptCall{Socket: socket, Key: key, Value: value})
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

// FindSocketOpt returns the value of the last SetOption call on socket matching key.
func FindSocketOpt(opts []OptCall, socket, key string) (string, bool) {
	for i := len(opts) - 1; i >= 0; i-- {
		if opts[i].Socket == socket && opts[i].Key == key {
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

// FindSocketWindowOpt returns the value of the last SetWindowOption call on
// socket matching target and key.
func FindSocketWindowOpt(opts []WindowOptCall, socket, target, key string) (string, bool) {
	for i := len(opts) - 1; i >= 0; i-- {
		if opts[i].Socket == socket && opts[i].Target == target && opts[i].Key == key {
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

// LastSocketRename returns the last rename call on socket for a given target.
func LastSocketRename(renames []RenameCall, socket, target string) (string, bool) {
	for i := len(renames) - 1; i >= 0; i-- {
		if renames[i].Socket == socket && renames[i].Target == target {
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
