package config

import "time"

type Config struct {
	Verbose         bool
	LogJSON         bool   // emit structured JSON log lines instead of plain text (--json / CENCI_LOG_JSON)
	SocketPath      string // broadcast socket for waybar clients
	EventSocketPath string // event socket for hook notifications
	SweepInterval   time.Duration
	SessionTTL      time.Duration // idle expiry for paneless sessions
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
