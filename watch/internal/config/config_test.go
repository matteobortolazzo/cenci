package config

import (
	"testing"
	"time"
)

func TestDefaultSweepInterval(t *testing.T) {
	if got := Default().SweepInterval; got != time.Second {
		t.Fatalf("SweepInterval = %s, want 1s", got)
	}
}

// TestDefaultLogJSON pins Config.LogJSON's zero-config default: structured
// JSON logging is opt-in (--json / CENCI_LOG_JSON), so Default() must
// report false unless the operator explicitly opts in. The env-var/flag
// precedence resolution itself happens in daemon_cmd.go, not here — see
// daemon_cmd_test.go.
func TestDefaultLogJSON(t *testing.T) {
	if got := Default().LogJSON; got != false {
		t.Fatalf("LogJSON = %v, want false", got)
	}
}
