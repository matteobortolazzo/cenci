// Package logging is the command-layer structured-logging seam for
// daemon_cmd.go's own start/signal -v lines. Per the plan's Q2 decision, it
// covers only those lines — internal/daemon's own -v output is untouched.
package logging

import (
	"encoding/json"
	"io"
	stdlog "log"
	"time"
)

// Severity is the conventional info/warn/error log-level taxonomy (Q3
// decision) — deliberately distinct from
// internal/sandbox/launcher/diagnose.go's fatal/degraded/warning
// diagnose-finding taxonomy, which is a poor fit for general informational
// daemon-startup lines.
type Severity string

const (
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// Logger emits either plain-text output — byte-identical to today's
// log.Printf-based -v output (stdlib "log" package, Ldate|Ltime flags) — or
// single-line JSON records, selected once at construction time.
type Logger struct {
	json bool
	text *stdlog.Logger
	w    io.Writer
}

// New returns a Logger writing to w. jsonMode selects single-line JSON
// records; when false (the default), Log produces plain-text output
// byte-identical to stdlib log.Printf.
func New(w io.Writer, jsonMode bool) *Logger {
	return &Logger{
		json: jsonMode,
		text: stdlog.New(w, "", stdlog.LstdFlags),
		w:    w,
	}
}

// jsonLine is the wire schema for a single JSON log record:
// {timestamp, severity, code?, message}. Code is omitted entirely (not
// present-and-empty) when the caller supplies none.
type jsonLine struct {
	Timestamp string   `json:"timestamp"`
	Severity  Severity `json:"severity"`
	Code      string   `json:"code,omitempty"`
	Message   string   `json:"message"`
}

// Log emits one log record: severity, an optional registered errcode.Code
// (empty string for lines with no attached code), and message. In JSON mode
// this writes one newline-terminated JSON object. In text mode it writes a
// plain log.Printf-style line; severity and code are never rendered in text
// mode, preserving today's plain-text output exactly.
func (l *Logger) Log(severity Severity, code, message string) {
	if !l.json {
		l.text.Print(message)
		return
	}
	_ = json.NewEncoder(l.w).Encode(jsonLine{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Severity:  severity,
		Code:      code,
		Message:   message,
	})
}
