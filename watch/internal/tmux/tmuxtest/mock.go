// Package tmuxtest provides a mock tmux.Client for tests in the daemon and
// tmux-frontend packages.
package tmuxtest

import (
	"fmt"
	"sync"

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

// RetainedPane is one pane of a RetainedWindow.
type RetainedPane struct {
	ID   string
	Dead bool
}

// RetainedWindow is one window of a RetainedServer: tmux's window id ("@N"), the
// user-writable name and @cenci-repo-dir, and its panes.
type RetainedWindow struct {
	ID, Name, RepoDir string
	Panes             []RetainedPane
}

// RetainedReadKind tells the two reads a RetainedServer serves apart.
type RetainedReadKind string

const (
	// InventoryRead is a ListCleanupInventory call (pane is empty).
	InventoryRead RetainedReadKind = "inventory"
	// WindowRead is a ReadCleanupWindow call (the revalidation of one window).
	WindowRead RetainedReadKind = "window"
)

// RetainedRead records one read of a RetainedServer.
type RetainedRead struct {
	Kind   RetainedReadKind
	Socket string
	// Pane is the pane id a WindowRead was asked for; empty for an InventoryRead.
	Pane string
}

// RetainedServer is a fake for the retained-window readers
// (tmux.ListCleanupInventory and tmux.ReadCleanupWindow): per-socket windows,
// per-socket read errors, a call record, and a hook that runs before each read.
// Its two methods are shaped to be installed in the daemon's cleanupInventory and
// cleanupRevalidate seams, and a window on one socket is invisible to the others,
// so the same %N on two sockets is two panes, as in real tmux. The empty socket
// is the default server.
//
// Set the exported fields before the first read. To change the server while a
// pass is running (a respawn, a rename), do it through Edit or AddWindow, which
// are safe to call from BeforeRead or from another goroutine: the fake
// snapshots a window under its own lock and never holds it across BeforeRead.
type RetainedServer struct {
	// Windows is the windows of each socket.
	Windows map[string][]RetainedWindow
	// Dropped is the per-socket list of dropped-row reports the inventory returns
	// next to its windows: what the real reader reports for a malformed row, whose
	// window it already left out of Windows.
	Dropped map[string][]string
	// InventoryErrs makes the inventory read of that socket fail (tmux.ErrNoServer
	// for a socket with no server, any other error for an unreadable one).
	InventoryErrs map[string]error
	// ReadErrs makes the revalidation read of that socket fail with the given
	// error (not tmux.ErrWindowGone: a missing pane already reports that).
	ReadErrs map[string]error
	// BeforeRead, when set, runs at the start of every read, outside the server's
	// lock, with the read and the 1-based count of reads of that kind so far
	// (across all sockets). It may mutate the server through Edit or block.
	BeforeRead func(read RetainedRead, n int)

	mu    sync.Mutex
	reads []RetainedRead
}

// Edit runs fn with the server locked, so a test can change its fields while a
// pass reads them.
func (s *RetainedServer) Edit(fn func(s *RetainedServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

// AddWindow appends w to the windows of socket.
func (s *RetainedServer) AddWindow(socket string, w RetainedWindow) {
	s.Edit(func(s *RetainedServer) {
		if s.Windows == nil {
			s.Windows = map[string][]RetainedWindow{}
		}
		s.Windows[socket] = append(s.Windows[socket], w)
	})
}

// Reads returns every read so far, in call order.
func (s *RetainedServer) Reads() []RetainedRead {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RetainedRead(nil), s.reads...)
}

// Count returns how many reads of kind were made so far.
func (s *RetainedServer) Count(kind RetainedReadKind) int {
	n := 0
	for _, r := range s.Reads() {
		if r.Kind == kind {
			n++
		}
	}
	return n
}

// InventorySockets returns the socket of each inventory read, in call order.
func (s *RetainedServer) InventorySockets() []string {
	var sockets []string
	for _, r := range s.Reads() {
		if r.Kind == InventoryRead {
			sockets = append(sockets, r.Socket)
		}
	}
	return sockets
}

// record notes a read and returns the count of reads of its kind so far.
func (s *RetainedServer) record(r RetainedRead) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, r)
	n := 0
	for _, prev := range s.reads {
		if prev.Kind == r.Kind {
			n++
		}
	}
	return n
}

func (s *RetainedServer) before(r RetainedRead, n int) {
	if s.BeforeRead != nil {
		s.BeforeRead(r, n)
	}
}

func toCleanupWindow(w RetainedWindow) tmux.CleanupWindow {
	out := tmux.CleanupWindow{WindowID: w.ID, Name: w.Name, RepoDir: w.RepoDir}
	for _, p := range w.Panes {
		out.Panes = append(out.Panes, tmux.CleanupPane{ID: p.ID, Dead: p.Dead})
	}
	return out
}

// ListCleanupInventory is the fake tmux.ListCleanupInventory for socket.
func (s *RetainedServer) ListCleanupInventory(socket string) (tmux.CleanupInventory, error) {
	read := RetainedRead{Kind: InventoryRead, Socket: socket}
	s.before(read, s.record(read))

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.InventoryErrs[socket]; err != nil {
		return tmux.CleanupInventory{}, err
	}
	var inv tmux.CleanupInventory
	for _, w := range s.Windows[socket] {
		inv.Windows = append(inv.Windows, toCleanupWindow(w))
	}
	inv.Dropped = append(inv.Dropped, s.Dropped[socket]...)
	return inv, nil
}

// ReadCleanupWindow is the fake tmux.ReadCleanupWindow: the window of socket
// that contains pane, or an error wrapping tmux.ErrWindowGone when no window of
// that socket does.
func (s *RetainedServer) ReadCleanupWindow(socket, pane string) (tmux.CleanupWindow, error) {
	read := RetainedRead{Kind: WindowRead, Socket: socket, Pane: pane}
	s.before(read, s.record(read))

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ReadErrs[socket]; err != nil {
		return tmux.CleanupWindow{}, err
	}
	for _, w := range s.Windows[socket] {
		for _, p := range w.Panes {
			if p.ID == pane {
				return toCleanupWindow(w), nil
			}
		}
	}
	return tmux.CleanupWindow{}, fmt.Errorf("tmux list-panes: can't find pane: %s: %w", pane, tmux.ErrWindowGone)
}
