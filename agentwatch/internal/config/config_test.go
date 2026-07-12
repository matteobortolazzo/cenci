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
