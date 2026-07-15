package main_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
)

func TestNotifyCodexPromptSendsOnlyCompactTaskName(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "events.sock")
	receiver, err := ipc.NewEventReceiver(socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = receiver.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go receiver.Accept(ctx)

	rawPrompt := "\n  improve\t codex tmux names\x00  \nprivate second line"
	input := `{"hook_event_name":"UserPromptSubmit","session_id":"codex-sess","prompt":` + string(mustJSON(t, rawPrompt)) + `}`
	cmd := exec.Command(binaryPath, "notify", "-agent", "codex", "-event-socket", socket)
	cmd.Stdin = strings.NewReader(input)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("notify: %v: %s", err, output)
	}

	select {
	case event := <-receiver.Events():
		if event.TaskName != "improve codex tmux names" {
			t.Fatalf("task_name = %q", event.TaskName)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "private second line") || strings.Contains(string(encoded), "prompt") {
			t.Fatalf("raw prompt leaked into IPC: %s", encoded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notify event")
	}
}

// TestNotifyParsesAgentIDFromStdin covers ticket #277: the real runNotify
// stdin-parsing path must decode the "agent_id" JSON key into
// ipc.HookEvent.AgentID, since that's the field the daemon's subagent guard
// (internal/daemon/event.go) relies on.
func TestNotifyParsesAgentIDFromStdin(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "events.sock")
	receiver, err := ipc.NewEventReceiver(socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = receiver.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go receiver.Accept(ctx)

	input := `{"hook_event_name":"Stop","session_id":"s1","agent_id":"sub1"}`
	cmd := exec.Command(binaryPath, "notify", "-agent", "claude", "-event-socket", socket)
	cmd.Stdin = strings.NewReader(input)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("notify: %v: %s", err, output)
	}

	select {
	case event := <-receiver.Events():
		if event.AgentID != "sub1" {
			t.Fatalf("agent_id = %q, want %q", event.AgentID, "sub1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notify event")
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestCodexHooksJSONHasNoUnknownKeys guards against unsupported keys leaking
// into the Codex hooks file. In particular, Codex currently does not support
// asynchronous hooks, and emits a startup warning for each such hook.
func TestCodexHooksJSONHasNoUnknownKeys(t *testing.T) {
	// main_test runs with cwd = package dir (repo root), so this resolves.
	data, err := os.ReadFile(filepath.Join("plugin", "codex", "hooks.json"))
	if err != nil {
		t.Fatalf("read codex hooks.json: %v", err)
	}

	var root struct {
		Hooks map[string][]map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse codex hooks.json: %v", err)
	}
	if len(root.Hooks) == 0 {
		t.Fatal("expected at least one event group in codex hooks.json")
	}

	allowedGroupKeys := map[string]bool{"matcher": true, "hooks": true}
	allowedHookKeys := map[string]bool{"type": true, "command": true, "timeout": true}

	for event, groups := range root.Hooks {
		for i, group := range groups {
			for key := range group {
				if !allowedGroupKeys[key] {
					t.Errorf("%s[%d]: unexpected group key %q (allowed: matcher, hooks)", event, i, key)
				}
			}
			raw, ok := group["hooks"]
			if !ok {
				t.Errorf("%s[%d]: missing required \"hooks\" key", event, i)
				continue
			}
			var hooks []map[string]json.RawMessage
			if err := json.Unmarshal(raw, &hooks); err != nil {
				t.Errorf("%s[%d]: parse hooks array: %v", event, i, err)
				continue
			}
			for j, hook := range hooks {
				for key := range hook {
					if !allowedHookKeys[key] {
						t.Errorf("%s[%d].hooks[%d]: unexpected hook key %q (allowed: type, command, timeout)", event, i, j, key)
					}
				}
			}
		}
	}
}

func TestCodexHooksUsePluginLocalBinary(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("plugin", "codex", "hooks.json"))
	if err != nil {
		t.Fatalf("read codex hooks.json: %v", err)
	}

	var root struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse codex hooks.json: %v", err)
	}

	const localBinary = `"${PLUGIN_ROOT}/bin/cenci" notify`
	for event, groups := range root.Hooks {
		for i, group := range groups {
			for j, hook := range group.Hooks {
				if !strings.Contains(hook.Command, localBinary) {
					t.Errorf("%s[%d].hooks[%d] does not use plugin-local binary: %q", event, i, j, hook.Command)
				}
			}
		}
	}
}

func TestCodexStopHookEmitsJSON(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("plugin", "codex", "hooks.json"))
	if err != nil {
		t.Fatalf("read codex hooks.json: %v", err)
	}

	var root struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse codex hooks.json: %v", err)
	}
	stopGroups := root.Hooks["Stop"]
	if len(stopGroups) != 1 || len(stopGroups[0].Hooks) != 1 {
		t.Fatalf("expected exactly one Codex Stop hook, got %#v", stopGroups)
	}

	pluginRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pluginRoot, "bin"), 0o755); err != nil {
		t.Fatalf("create plugin bin dir: %v", err)
	}
	stub := filepath.Join(pluginRoot, "bin", "cenci")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatalf("write hook binary stub: %v", err)
	}

	cmd := exec.Command("sh", "-c", stopGroups[0].Hooks[0].Command)
	cmd.Env = append(os.Environ(), "PLUGIN_ROOT="+pluginRoot)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"Stop","session_id":"test"}`)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run Codex Stop hook: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("Codex Stop hook output must be JSON, got %q: %v", output, err)
	}
}
