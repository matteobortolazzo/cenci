package launcher

import (
	"io"
	"strings"
	"testing"
)

// CLI checks alone cannot protect internal callers or a stale supervisor.
func TestDisabledOpenCodeEngineRefusesActivity(t *testing.T) {
	eng := &Engine{Stdout: io.Discard, Stderr: io.Discard}
	for name, action := range map[string]func() error{
		"launch":            func() error { return eng.Launch(Options{Agent: "opencode"}) },
		"dry run":           func() error { _, err := eng.DryRun(Options{Agent: "opencode"}); return err },
		"update agent":      func() error { return eng.UpdateAgent("opencode", "") },
		"bulk agent update": func() error { return eng.UpdateAgentSkipIfPinned("opencode") },
		"unpin":             func() error { return eng.UnpinAgent("opencode") },
		"update plugins":    func() error { return eng.UpdatePlugins("opencode", Scope{}) },
	} {
		t.Run(name, func(t *testing.T) {
			err := action()
			if err == nil || !IsUsage(err) || !strings.Contains(err.Error(), "OpenCode is temporarily disabled") {
				t.Fatalf("expected pause refusal before engine setup, got %v", err)
			}
		})
	}
}
