package config

import "time"

type Config struct {
	Verbose         bool
	SocketPath      string // broadcast socket for waybar clients
	EventSocketPath string // event socket for hook notifications
	SweepInterval   time.Duration
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
}

func Default() Config {
	return Config{
		Verbose:         false,
		SweepInterval:   30 * time.Second,
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
	}
}
