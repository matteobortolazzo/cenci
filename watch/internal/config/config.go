package config

import "time"

type Config struct {
	Verbose         bool
	LogJSON         bool   // emit structured JSON log lines instead of plain text (--json / CENCI_LOG_JSON)
	SocketPath      string // broadcast socket for waybar clients
	EventSocketPath string // event socket for hook notifications
	SweepInterval   time.Duration
	SessionTTL      time.Duration // idle expiry for paneless sessions
	// ReapInterval is the period of the daemon's orphan-reap backstop
	// (#1171). The pane-gone sweep only reaps when the daemon still holds a
	// pane binding at the moment the pane dies; every path that drops that
	// binding earlier (SessionEnd teardown, a handoff whose successor never
	// reports, a daemon-side restart of tracking) leaves the pane's death
	// unobservable and strands a live container process. This tick bounds
	// that accumulation regardless of which binding path was lost.
	// Non-positive disables the backstop.
	ReapInterval    time.Duration
	StyleIdle       string
	StyleRunning    string
	StyleDone       string
	StyleNeedInput  string
	StyleStopped    string
	SymbolIdle      string
	SymbolRunning   string
	SymbolDone      string
	SymbolNeedInput string
	SymbolStopped   string
	SymbolFailed    string
	// SymbolEscalated (#826) is the glyph for a ticket the unattended
	// planner escalated (Input Needed), deliberately distinct from
	// SymbolNeedInput ("!", a live session waiting mid-turn) and
	// SymbolFailed ("✗") so the three never render identically.
	SymbolEscalated string
}

func Default() Config {
	return Config{
		Verbose:         false,
		LogJSON:         false,
		SweepInterval:   time.Second,
		SessionTTL:      2 * time.Hour,
		ReapInterval:    5 * time.Minute,
		StyleIdle:       "dim",
		StyleRunning:    "fg=blue,dim",
		StyleDone:       "fg=green,dim",
		StyleNeedInput:  "fg=red,dim",
		StyleStopped:    "fg=yellow,dim",
		SymbolIdle:      "~",
		SymbolRunning:   "▶",
		SymbolDone:      "✓",
		SymbolNeedInput: "!",
		SymbolStopped:   "⏹",
		SymbolFailed:    "✗",
		SymbolEscalated: "?",
	}
}
