package daemon

import (
	"time"

	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/config"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/ipc"
	"github.com/matteobortolazzo/claude-tools/agentwatch/internal/tmux"
)

// mockClient implements tmux.Client for testing.
type mockClient struct {
	panes           []tmux.PaneInfo
	listCalls       int
	renames         []renameCall
	windowOpts      []windowOptCall
	windowOptValues map[string]string // key: "target:key" → value
	listErr         error
	renameErr       error
	windowOptErr    error
	getOptErr       error
}

type renameCall struct {
	target string
	name   string
}

type windowOptCall struct {
	target string
	key    string
	value  string
}

func (m *mockClient) ListPanes() ([]tmux.PaneInfo, error) {
	m.listCalls++
	return m.panes, m.listErr
}

func (m *mockClient) RenameWindow(target string, name string) error {
	m.renames = append(m.renames, renameCall{target, name})
	if m.renameErr == nil {
		for i := range m.panes {
			if m.panes[i].WindowTarget() == target {
				m.panes[i].WindowName = name
			}
		}
	}
	return m.renameErr
}

func (m *mockClient) SetWindowOption(target string, key string, value string) error {
	m.windowOpts = append(m.windowOpts, windowOptCall{target, key, value})
	if m.windowOptValues == nil {
		m.windowOptValues = make(map[string]string)
	}
	m.windowOptValues[target+":"+key] = value
	return m.windowOptErr
}

func (m *mockClient) GetWindowOption(target string, key string) (string, error) {
	if m.getOptErr != nil {
		return "", m.getOptErr
	}
	k := target + ":" + key
	if v, ok := m.windowOptValues[k]; ok {
		return v, nil
	}
	if key == "automatic-rename" {
		return "on", nil
	}
	return "", nil
}

func testConfig() config.Config {
	cfg := config.Default()
	cfg.SweepInterval = 10 * time.Millisecond
	return cfg
}

// findWindowOpt returns the value of the last SetWindowOption call matching target and key.
func findWindowOpt(opts []windowOptCall, target, key string) (string, bool) {
	for i := len(opts) - 1; i >= 0; i-- {
		if opts[i].target == target && opts[i].key == key {
			return opts[i].value, true
		}
	}
	return "", false
}

// lastRename returns the last rename call for a given target.
func lastRename(renames []renameCall, target string) (string, bool) {
	for i := len(renames) - 1; i >= 0; i-- {
		if renames[i].target == target {
			return renames[i].name, true
		}
	}
	return "", false
}

// newTestDaemon creates a daemon with a mock client and event channel for testing.
// Tests call handleEvent directly for synchronous, deterministic testing.
func newTestDaemon(mc *mockClient) *Daemon {
	ch := make(chan ipc.HookEvent, 16)
	d := newDaemon(testConfig(), mc, ch)
	return d
}
