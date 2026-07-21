// Package logging_test exercises the command-layer structured-logging seam:
// a Logger that emits either plain-text (byte-identical to today's
// log.Printf-based -v output) or single-line JSON records, selected once at
// construction time. Per the plan's Q2 decision, this seam covers only
// daemon_cmd.go's own start/signal -v lines — internal/daemon is untouched.
package logging_test

import (
	"bytes"
	"encoding/json"
	stdlog "log"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/logging"
)

// decodeJSONLine parses a single JSON log line, failing the test if it
// isn't valid JSON at all — a hard requirement, not a soft assertion.
func decodeJSONLine(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &got); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\nraw: %s", err, raw)
	}
	return got
}

// TestLogger_JSON_SchemaWithoutCode pins the required JSON fields
// (timestamp, severity, message) for a log line with no attached errcode —
// the common case for daemon_cmd.go's plain start/signal lines — and
// asserts "code" is entirely absent (not present-and-empty) when none was
// supplied.
func TestLogger_JSON_SchemaWithoutCode(t *testing.T) {
	var buf bytes.Buffer
	l := logging.New(&buf, true)

	l.Log(logging.SeverityInfo, "", "cenci starting (event-driven, sweep every 1s)")

	got := decodeJSONLine(t, buf.Bytes())
	for _, field := range []string{"timestamp", "severity", "message"} {
		if _, ok := got[field]; !ok {
			t.Errorf("JSON log line missing required field %q: %s", field, buf.String())
		}
	}
	if got["severity"] != "info" {
		t.Errorf("severity = %v, want %q", got["severity"], "info")
	}
	if got["message"] != "cenci starting (event-driven, sweep every 1s)" {
		t.Errorf("message = %v, want the exact log message", got["message"])
	}
	if _, hasCode := got["code"]; hasCode {
		t.Errorf("did not expect a \"code\" field when no code was supplied: %s", buf.String())
	}

	ts, ok := got["timestamp"].(string)
	if !ok {
		t.Fatalf("timestamp field is not a string: %v", got["timestamp"])
	}
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("timestamp %q is not RFC3339-formatted: %v", ts, err)
	}
}

// TestLogger_JSON_IncludesCodeWhenSupplied covers the diagnose-style finding
// case: a line carrying a registered errcode.Code must surface it verbatim
// under "code".
func TestLogger_JSON_IncludesCodeWhenSupplied(t *testing.T) {
	var buf bytes.Buffer
	l := logging.New(&buf, true)

	l.Log(logging.SeverityError, "CENCI-DAEMON-SOCKET-001", "the cenci daemon's event socket does not exist")

	got := decodeJSONLine(t, buf.Bytes())
	if got["code"] != "CENCI-DAEMON-SOCKET-001" {
		t.Errorf("code = %v, want CENCI-DAEMON-SOCKET-001", got["code"])
	}
	if got["severity"] != "error" {
		t.Errorf("severity = %v, want error", got["severity"])
	}
}

// TestLogger_JSON_SeverityValues pins the conventional info/warn/error
// taxonomy (Q3 decision) — deliberately distinct from #572's
// fatal/degraded/warning diagnose-finding taxonomy, which is a poor fit for
// general informational daemon-startup lines.
func TestLogger_JSON_SeverityValues(t *testing.T) {
	cases := []struct {
		sev  logging.Severity
		want string
	}{
		{logging.SeverityInfo, "info"},
		{logging.SeverityWarn, "warn"},
		{logging.SeverityError, "error"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			var buf bytes.Buffer
			l := logging.New(&buf, true)
			l.Log(tc.sev, "", "message")

			got := decodeJSONLine(t, buf.Bytes())
			if got["severity"] != tc.want {
				t.Errorf("severity = %v, want %q", got["severity"], tc.want)
			}
		})
	}
}

// TestLogger_JSON_OneLinePerCall asserts each Log call emits exactly one
// newline-terminated JSON object — required for line-oriented JSON log
// consumers (jq, log shippers) to parse a stream of calls independently.
func TestLogger_JSON_OneLinePerCall(t *testing.T) {
	var buf bytes.Buffer
	l := logging.New(&buf, true)

	l.Log(logging.SeverityInfo, "", "first")
	l.Log(logging.SeverityInfo, "", "second")

	lines := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSON lines, got %d: %s", len(lines), buf.String())
	}
	for i, line := range lines {
		var got map[string]any
		if err := json.Unmarshal(line, &got); err != nil {
			t.Errorf("line %d is not valid JSON: %v\nraw: %s", i, err, line)
		}
	}
}

// TestLogger_Text_ByteIdenticalToStdlibLogOutput is the plain-text
// regression test: with JSON mode off (the default), Logger.Log must
// produce output byte-identical to what today's log.Printf call (the
// stdlib "log" package's default Ldate|Ltime flags, writing "<date> <time>
// <message>\n") already produces for the same message — the existing
// plain-text -v output must not change at all when --json/CENCI_LOG_JSON
// are unset.
func TestLogger_Text_ByteIdenticalToStdlibLogOutput(t *testing.T) {
	const message = "cenci starting (event-driven, sweep every 1s)"

	var refBuf bytes.Buffer
	stdlog.New(&refBuf, "", stdlog.LstdFlags).Print(message)
	want := refBuf.String()

	var buf bytes.Buffer
	l := logging.New(&buf, false)
	l.Log(logging.SeverityInfo, "", message)
	got := buf.String()

	if got != want {
		t.Errorf("plain-text Logger output = %q, want byte-identical stdlib log output %q", got, want)
	}
}

// TestLogger_Text_IgnoresSeverityAndCode further pins the regression
// contract: today's plain -v lines carry no severity/code prefix at all, so
// Text mode must never introduce one — even for a line with an attached
// code and a non-info severity — or the "byte-identical" guarantee above
// would only hold for the info/no-code case.
func TestLogger_Text_IgnoresSeverityAndCode(t *testing.T) {
	const message = "the cenci daemon's event socket does not exist"

	var refBuf bytes.Buffer
	stdlog.New(&refBuf, "", stdlog.LstdFlags).Print(message)
	want := refBuf.String()

	var buf bytes.Buffer
	l := logging.New(&buf, false)
	l.Log(logging.SeverityError, "CENCI-DAEMON-SOCKET-001", message)
	got := buf.String()

	if got != want {
		t.Errorf("plain-text Logger output = %q, want byte-identical stdlib log output %q (severity/code must not leak into text mode)", got, want)
	}
}
