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

// TestDefaultSymbolEscalated_DistinctFromNeedInputAndFailed (#826) pins the
// escalated-status default glyph and, more importantly, that it is a
// distinct rune from both SymbolNeedInput ("!") and SymbolFailed ("✗") --
// the risk the plan calls out explicitly (label "Input Needed" vs.
// pre-existing "Need Input"/NeedInput is exactly the pair that must never
// render identically).
func TestDefaultSymbolEscalated_DistinctFromNeedInputAndFailed(t *testing.T) {
	d := Default()
	if d.SymbolEscalated != "?" {
		t.Errorf("SymbolEscalated = %q, want %q", d.SymbolEscalated, "?")
	}
	if d.SymbolEscalated == d.SymbolNeedInput {
		t.Errorf("SymbolEscalated (%q) must be distinct from SymbolNeedInput (%q)", d.SymbolEscalated, d.SymbolNeedInput)
	}
	if d.SymbolEscalated == d.SymbolFailed {
		t.Errorf("SymbolEscalated (%q) must be distinct from SymbolFailed (%q)", d.SymbolEscalated, d.SymbolFailed)
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
