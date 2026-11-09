// Package measurement validates native Claude measurement samples (#1450) and
// classifies their freshness. Leaf package: stdlib and pkg/watch only, and it
// never reads the environment, credentials or config (no account or profile
// identity is derived here).
//
// Parse is the single validation boundary: the daemon calls it on every
// envelope that reaches the event socket, because containers write to the same
// socket as the host. A whole-sample failure (malformed body, bad session id,
// bad measured_at) drops the sample; any other bad field rejects only that
// field and is named in the stored sample's Missing and Invalid lists.
package measurement

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// StaleAfter is the age past which a stored sample is classified stale.
const StaleAfter = 15 * time.Minute

// FutureTolerance is how far ahead of the daemon clock a measured_at may be.
// Past it the whole sample is dropped, so a hostile writer cannot pin a
// session's measurement with a far-future timestamp.
const FutureTolerance = 5 * time.Minute

// MaxRateLimitEntries is the most rate_limits entries a sample may carry. A
// longer array is rejected as the single field "rate_limits": every entry adds
// missing/invalid paths, so without a cap one 4 KiB socket line would store a
// measurement large enough to push the broadcast snapshot past the client's
// line limit and blind every reader. Real producers send a handful of windows.
const MaxRateLimitEntries = 8

const (
	maxSafeInteger = 1 << 53 // largest whole number a JSON double holds exactly
	maxKindRunes   = 32
	maxLabelBytes  = 64

	allowedKindRunes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"
)

// Whole-sample failures: the sample is dropped, nothing is stored.
var (
	ErrMalformed         = errors.New("measurement: malformed sample")
	ErrInvalidSession    = errors.New("measurement: invalid session_id")
	ErrInvalidMeasuredAt = errors.New("measurement: invalid measured_at")
)

var (
	sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	labelPattern     = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)
)

// kindTable maps a raw rate-limit kind to its window label. It is closed. The
// kinds Claude Code 2.1.319's $.session.usage() produces are five_hour,
// seven_day and, on a gateway provider only, spend_limit; the last is a spend
// cap, not a time window, so it is deliberately not mapped
// (watch/docs/claude-native-measurements.md). Every other raw kind is stored as
// "unknown:<sanitized>", never as one of these.
var kindTable = map[string]string{
	"five_hour": "five_hour",
	"seven_day": "weekly",
}

// Sample is one validated measurement.
type Sample struct {
	SessionID  string
	MeasuredAt time.Time
	Snapshot   watch.NativeMeasurement
}

// parser accumulates the missing/invalid lists while one sample is walked.
type parser struct {
	missing []string
	invalid []string
}

// absent records a field that was not reported.
func (p *parser) absent(path string) { p.missing = append(p.missing, path) }

// reject records a field that was reported but failed validation.
func (p *parser) reject(path string) {
	p.missing = append(p.missing, path)
	p.invalid = append(p.invalid, path)
}

// Parse validates one raw measurement body against now.
func Parse(raw []byte, now time.Time) (Sample, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return Sample{}, ErrMalformed
	}

	var sessionID string
	if !stringField(top["session_id"], &sessionID) || !sessionIDPattern.MatchString(sessionID) {
		return Sample{}, ErrInvalidSession
	}
	measuredAt, ok := timeField(top["measured_at"])
	if !ok || measuredAt.After(now.Add(FutureTolerance)) {
		return Sample{}, ErrInvalidMeasuredAt
	}

	p := &parser{missing: make([]string, 0), invalid: make([]string, 0)}
	snap := watch.NativeMeasurement{MeasuredAt: formatTime(measuredAt)}
	snap.Source = p.label(top, "source")
	snap.ClaudeVersion = p.label(top, "claude_version")
	snap.Context = p.context(top["context"])
	snap.Quota = p.rateLimits(top["rate_limits"])
	snap.Missing = p.missing
	snap.Invalid = p.invalid
	return Sample{SessionID: sessionID, MeasuredAt: measuredAt, Snapshot: snap}, nil
}

// label validates a short free-text identifier (source, claude_version): a
// non-empty string of at most maxLabelBytes from a conservative character set.
func (p *parser) label(top map[string]json.RawMessage, key string) string {
	raw, present := top[key]
	if !present {
		p.absent(key)
		return ""
	}
	var s string
	if !stringField(raw, &s) || len(s) > maxLabelBytes || !labelPattern.MatchString(s) {
		p.reject(key)
		return ""
	}
	return s
}

// context validates the context object. An absent or non-object value stores
// nothing; inside an object each field is validated on its own.
func (p *parser) context(raw json.RawMessage) *watch.NativeContext {
	if raw == nil {
		p.absent("context")
		return nil
	}
	fields, ok := objectFields(raw)
	if !ok {
		p.reject("context")
		return nil
	}
	ctx := &watch.NativeContext{}
	ctx.Tokens = p.wholeNumber(fields, "tokens", "context.tokens", 0)
	ctx.Window = p.wholeNumber(fields, "window", "context.window", 1)
	ctx.Percent = p.number(fields, "percent", "context.percent", math.Inf(1))
	ctx.OverWindow = ctx.Percent != nil && *ctx.Percent > 100
	return ctx
}

// rateLimits validates the rate_limits array entry by entry. A nil result means
// the key was absent, not an array or longer than MaxRateLimitEntries (rejected
// as the single field "rate_limits"); an empty non-nil slice means [].
func (p *parser) rateLimits(raw json.RawMessage) []watch.NativeQuotaWindow {
	if raw == nil {
		p.absent("rate_limits")
		return nil
	}
	var entries []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &entries) != nil {
		p.reject("rate_limits")
		return nil
	}
	if len(entries) > MaxRateLimitEntries {
		p.reject("rate_limits")
		return nil
	}
	quota := make([]watch.NativeQuotaWindow, 0, len(entries))
	for i, entry := range entries {
		prefix := "rate_limits[" + strconv.Itoa(i) + "]"
		fields, ok := objectFields(entry)
		if !ok {
			p.reject(prefix)
			continue
		}
		quota = append(quota, p.quotaWindow(fields, prefix))
	}
	return quota
}

// quotaWindow validates one rate-limit entry. An entry whose kind is absent or
// not a string cannot be classified, but its other valid fields are still
// stored: it takes the fallback kind "unknown:unknown" (SanitizeKind of "") and
// is never mapped to a recognized window.
func (p *parser) quotaWindow(fields map[string]json.RawMessage, prefix string) watch.NativeQuotaWindow {
	var w watch.NativeQuotaWindow

	var kind string
	if rawKind, present := fields["kind"]; !present {
		p.absent(prefix + ".kind")
		w.Kind = "unknown:" + SanitizeKind("")
	} else if !stringField(rawKind, &kind) {
		p.reject(prefix + ".kind")
		w.Kind = "unknown:" + SanitizeKind("")
	} else if label, known := kindTable[kind]; known {
		w.Kind = label
	} else {
		w.Kind = "unknown:" + SanitizeKind(kind)
	}

	w.PercentUsed = p.number(fields, "percent_used", prefix+".percent_used", 100)
	w.ResetsAt = p.timestamp(fields, "resets_at", prefix+".resets_at")
	return w
}

// number validates a finite JSON number in [0, upper]. A rejected or absent
// value is nil, never zero.
func (p *parser) number(fields map[string]json.RawMessage, key, path string, upper float64) *float64 {
	raw, present := fields[key]
	if !present {
		p.absent(path)
		return nil
	}
	f, ok := numberToken(raw)
	if !ok || f < 0 || f > upper {
		p.reject(path)
		return nil
	}
	return &f
}

// wholeNumber validates a JSON number that is a whole value in [lowest, 2^53].
func (p *parser) wholeNumber(fields map[string]json.RawMessage, key, path string, lowest float64) *int64 {
	raw, present := fields[key]
	if !present {
		p.absent(path)
		return nil
	}
	f, ok := numberToken(raw)
	if !ok || f != math.Trunc(f) || f < lowest || f > maxSafeInteger {
		p.reject(path)
		return nil
	}
	n := int64(f)
	return &n
}

// timestamp validates an RFC 3339 string and returns it normalized to UTC; a
// rejected or absent value is "".
func (p *parser) timestamp(fields map[string]json.RawMessage, key, path string) string {
	raw, present := fields[key]
	if !present {
		p.absent(path)
		return ""
	}
	t, ok := timeField(raw)
	if !ok {
		p.reject(path)
		return ""
	}
	return formatTime(t)
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// objectFields decodes raw as a JSON object. null and every other type fail.
func objectFields(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, false
	}
	return fields, true
}

// stringField decodes raw as a JSON string. null and every other type fail.
func stringField(raw json.RawMessage, out *string) bool {
	if len(raw) == 0 || raw[0] != '"' {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}

// timeField decodes raw as an RFC 3339 timestamp string.
func timeField(raw json.RawMessage) (time.Time, bool) {
	var s string
	if !stringField(raw, &s) {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

// numberToken decodes raw as a JSON number token: a string, null or bool is
// not a number, and an overflow such as 1e999 (±Inf) is rejected.
func numberToken(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}

// SanitizeKind reduces an unrecognized rate-limit kind to a safe label: escape
// sequences are stripped, then everything outside [A-Za-z0-9._-] (which removes
// C0, C1 and DEL), capped at 32 runes. An empty result is "unknown". The caller
// adds the "unknown:" prefix.
func SanitizeKind(kind string) string {
	var out strings.Builder
	runes := []rune(kind)
	for i := 0; i < len(runes) && out.Len() < maxKindRunes; {
		r := runes[i]
		if r != 0x1b {
			if strings.ContainsRune(allowedKindRunes, r) {
				out.WriteRune(r)
			}
			i++
			continue
		}
		i = skipEscape(runes, i+1)
	}
	if out.Len() == 0 {
		return "unknown"
	}
	return out.String()
}

// skipEscape returns the index after the escape sequence whose introducer
// follows the ESC at i-1: CSI (ESC [ ... final), OSC (ESC ] ... BEL or ST) or
// any other two-byte sequence.
func skipEscape(runes []rune, i int) int {
	if i >= len(runes) {
		return i
	}
	switch runes[i] {
	case '[':
		i++
		for i < len(runes) && runes[i] >= 0x20 && runes[i] <= 0x3f {
			i++
		}
		if i < len(runes) && runes[i] >= 0x40 && runes[i] <= 0x7e {
			i++
		}
		return i
	case ']':
		i++
		for i < len(runes) {
			if runes[i] == 0x07 {
				return i + 1
			}
			if runes[i] == 0x1b && i+1 < len(runes) && runes[i+1] == '\\' {
				return i + 2
			}
			i++
		}
		return i
	default:
		return i + 1
	}
}

// Freshness classifies a stored sample as "fresh" or "stale": stale only when
// it is older than StaleAfter.
func Freshness(measuredAt, now time.Time) string {
	if now.Sub(measuredAt) > StaleAfter {
		return "stale"
	}
	return "fresh"
}

// QuotaState classifies a quota window as "current" or "expired (reset
// passed)": expired only once resetsAt is before now.
func QuotaState(resetsAt, now time.Time) string {
	if resetsAt.Before(now) {
		return "expired (reset passed)"
	}
	return "current"
}
