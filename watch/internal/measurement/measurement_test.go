package measurement_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/measurement"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// Parse is the validation/trust boundary shared by the daemon and (later) the
// notify command, so the per-field edge-case matrix runs through it directly:
// covering each case through the daemon socket would repeat the same assertion
// many times over a slower harness (plan Test Strategy).

var now = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func ts(t time.Time) string { return strconv.Quote(t.UTC().Format(time.RFC3339)) }

// ctxDoc builds a context object from raw JSON fragments; "" omits the key.
func ctxDoc(tokens, window, percent string) string {
	var parts []string
	for _, kv := range [][2]string{{"tokens", tokens}, {"window", window}, {"percent", percent}} {
		if kv[1] != "" {
			parts = append(parts, fmt.Sprintf("%q:%s", kv[0], kv[1]))
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// limit builds one rate-limit entry from raw JSON fragments; "" omits the key.
func limit(kind, pct, resets string) string {
	var parts []string
	for _, kv := range [][2]string{{"kind", kind}, {"percent_used", pct}, {"resets_at", resets}} {
		if kv[1] != "" {
			parts = append(parts, fmt.Sprintf("%q:%s", kv[0], kv[1]))
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func limits(entries ...string) string { return "[" + strings.Join(entries, ",") + "]" }

var (
	resetsFive = now.Add(2 * time.Hour)
	resetsWeek = now.Add(48 * time.Hour)

	fiveHour = limit(`"five_hour"`, `42.5`, ts(resetsFive))
	sevenDay = limit(`"seven_day"`, `10`, ts(resetsWeek))
)

// doc renders a measurement body. over replaces or (with "") removes a field;
// any extra key is added as-is. Keys are sorted so a failure reproduces.
func doc(over map[string]string) []byte {
	base := map[string]string{
		"session_id":     `"sess-1"`,
		"measured_at":    ts(now.Add(-time.Minute)),
		"source":         `"claude-mods"`,
		"claude_version": `"2.1.0"`,
		"context":        ctxDoc(`100000`, `200000`, `50`),
		"rate_limits":    limits(fiveHour, sevenDay),
	}
	for k, v := range over {
		base[k] = v
	}
	keys := make([]string, 0, len(base))
	for k, v := range base {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%q:%s", k, base[k]))
	}
	return []byte("{" + strings.Join(parts, ",") + "}")
}

func mustParse(t *testing.T, raw []byte) measurement.Sample {
	t.Helper()
	s, err := measurement.Parse(raw, now)
	if err != nil {
		t.Fatalf("Parse(%s) = %v, want a stored sample", raw, err)
	}
	return s
}

// sameSet compares two string lists ignoring order, and requires got non-nil
// (the snapshot encodes an empty list as [], never null).
func sameSet(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if got == nil {
		t.Errorf("%s is nil, want a non-nil list so it encodes as []", what)
	}
	g := append([]string{}, got...)
	w := append([]string{}, want...)
	sort.Strings(g)
	sort.Strings(w)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s = %v, want %v", what, g, w)
	}
}

func int64Is(t *testing.T, what string, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %v, want %d", what, got, want)
	}
}

func floatIs(t *testing.T, what string, got *float64, want float64) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// -- P13: a complete sample ---------------------------------------------------

func TestParse_CompleteSampleStoresEveryFieldWithEmptyMissing(t *testing.T) {
	s := mustParse(t, doc(nil))

	if s.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", s.SessionID)
	}
	if want := now.Add(-time.Minute); !s.MeasuredAt.Equal(want) {
		t.Errorf("MeasuredAt = %v, want %v", s.MeasuredAt, want)
	}
	m := s.Snapshot
	if want := now.Add(-time.Minute).Format(time.RFC3339); m.MeasuredAt != want {
		t.Errorf("snapshot MeasuredAt = %q, want %q", m.MeasuredAt, want)
	}
	if m.Source != "claude-mods" || m.ClaudeVersion != "2.1.0" {
		t.Errorf("source/version = %q/%q, want claude-mods/2.1.0", m.Source, m.ClaudeVersion)
	}
	if m.Context == nil {
		t.Fatal("context not stored")
	}
	int64Is(t, "tokens", m.Context.Tokens, 100000)
	int64Is(t, "window", m.Context.Window, 200000)
	floatIs(t, "percent", m.Context.Percent, 50)
	if m.Context.OverWindow {
		t.Error("OverWindow = true for 50%, want false")
	}
	if len(m.Quota) != 2 {
		t.Fatalf("quota = %+v, want the five-hour and weekly windows", m.Quota)
	}
	if m.Quota[0].Kind != "five_hour" || m.Quota[1].Kind != "weekly" {
		t.Errorf("kinds = %q, %q, want five_hour, weekly", m.Quota[0].Kind, m.Quota[1].Kind)
	}
	floatIs(t, "five-hour percent_used", m.Quota[0].PercentUsed, 42.5)
	floatIs(t, "weekly percent_used", m.Quota[1].PercentUsed, 10)
	if m.Quota[0].ResetsAt != resetsFive.Format(time.RFC3339) || m.Quota[1].ResetsAt != resetsWeek.Format(time.RFC3339) {
		t.Errorf("resets_at = %q, %q", m.Quota[0].ResetsAt, m.Quota[1].ResetsAt)
	}
	sameSet(t, "missing", m.Missing)
	sameSet(t, "invalid", m.Invalid)
}

// -- P14: rate_limits shapes --------------------------------------------------

func TestParse_AbsentRateLimitsStoresContextAndListsRateLimitsMissing(t *testing.T) {
	m := mustParse(t, doc(map[string]string{"rate_limits": ""})).Snapshot

	if m.Context == nil {
		t.Fatal("context must still be stored")
	}
	int64Is(t, "tokens", m.Context.Tokens, 100000)
	if m.Quota != nil {
		t.Errorf("quota = %+v, want nil for an absent rate_limits", m.Quota)
	}
	sameSet(t, "missing", m.Missing, "rate_limits")
	sameSet(t, "invalid", m.Invalid)
}

func TestParse_RateLimitsThatIsNotAnArrayIsRejectedAsInvalid(t *testing.T) {
	for _, bad := range []string{`"five_hour"`, `{}`, `5`, `true`} {
		t.Run(bad, func(t *testing.T) {
			m := mustParse(t, doc(map[string]string{"rate_limits": bad})).Snapshot
			if m.Quota != nil {
				t.Errorf("quota = %+v, want nil", m.Quota)
			}
			sameSet(t, "missing", m.Missing, "rate_limits")
			sameSet(t, "invalid", m.Invalid, "rate_limits")
			if m.Context == nil {
				t.Error("a bad rate_limits must not drop the context")
			}
		})
	}
}

func TestParse_EmptyRateLimitsArrayIsEmptyQuotaNotMissing(t *testing.T) {
	m := mustParse(t, doc(map[string]string{"rate_limits": "[]"})).Snapshot
	if m.Quota == nil || len(m.Quota) != 0 {
		t.Errorf("quota = %#v, want a non-nil empty slice (encodes [], not null)", m.Quota)
	}
	sameSet(t, "missing", m.Missing)
}

func TestParse_RateLimitEntryThatIsNotAnObjectIsRejectedAndTheRestKept(t *testing.T) {
	for _, bad := range []string{`"x"`, `5`, `null`, `[]`} {
		t.Run(bad, func(t *testing.T) {
			m := mustParse(t, doc(map[string]string{"rate_limits": limits(bad, fiveHour)})).Snapshot
			sameSet(t, "missing", m.Missing, "rate_limits[0]")
			sameSet(t, "invalid", m.Invalid, "rate_limits[0]")
			found := false
			for _, q := range m.Quota {
				if q.Kind == "five_hour" && q.PercentUsed != nil && *q.PercentUsed == 42.5 {
					found = true
				}
			}
			if !found {
				t.Errorf("quota = %+v, want the valid second entry kept", m.Quota)
			}
		})
	}
}

func TestParse_DuplicateRecognizedKindsAreAllStoredInOrder(t *testing.T) {
	first := limit(`"five_hour"`, `10`, ts(resetsFive))
	second := limit(`"five_hour"`, `20`, ts(resetsFive))
	m := mustParse(t, doc(map[string]string{"rate_limits": limits(first, second)})).Snapshot
	if len(m.Quota) != 2 {
		t.Fatalf("quota = %+v, want both entries", m.Quota)
	}
	floatIs(t, "first", m.Quota[0].PercentUsed, 10)
	floatIs(t, "second", m.Quota[1].PercentUsed, 20)
}

// -- P16, P29: kinds ----------------------------------------------------------

func TestParse_KindMapping(t *testing.T) {
	cases := []struct {
		name string
		kind string // raw JSON string literal
		want string
	}{
		{"five_hour is the five-hour window", `"five_hour"`, "five_hour"},
		{"seven_day is the weekly window", `"seven_day"`, "weekly"},
		{"unrecognized kind is unknown", `"monthly"`, "unknown:monthly"},
		{"case differs so it is unknown", `"Five_Hour"`, "unknown:Five_Hour"},
		{"weekly is not a raw kind", `"weekly"`, "unknown:weekly"},
		// A recognized kind is matched on the raw value before sanitizing, so a
		// kind that only becomes "five_hour" after stripping is never mapped.
		{"control byte after five_hour never maps", `"five_hour\u0001"`, "unknown:five_hour"},
		{"ANSI after seven_day never maps", `"seven_day\u001b[0m"`, "unknown:seven_day"},
		{"leading space never maps", `" five_hour"`, "unknown:five_hour"},
		// P29: escape sequences, C0, C1 and DEL are stripped.
		{"CSI OSC C0 DEL and C1 are stripped", `"\u001b[31mfoo\u001b]0;evil\u0007bar\u0001baz\u007f\u009b"`, "unknown:foobarbaz"},
		{"CSI with parameters", `"a\u001b[1;31mb"`, "unknown:ab"},
		{"OSC closed by ST", `"x\u001b]0;ttl\u001b\\y"`, "unknown:xy"},
		{"two-byte ESC sequence", `"p\u001bcq"`, "unknown:pq"},
		{"only characters outside the allowed set are dropped", `"a b/c:d"`, "unknown:abcd"},
		{"non-ASCII is dropped", `"kind-é–x"`, "unknown:kind-x"},
		{"dots and dashes are kept", `"a.b_c-d"`, "unknown:a.b_c-d"},
		{"capped at 32 runes", `"` + strings.Repeat("a", 40) + `"`, "unknown:" + strings.Repeat("a", 32)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := limit(tc.kind, `5`, ts(resetsFive))
			s := mustParse(t, doc(map[string]string{"rate_limits": limits(entry)}))
			if len(s.Snapshot.Quota) != 1 {
				t.Fatalf("quota = %+v, want 1 entry", s.Snapshot.Quota)
			}
			if got := s.Snapshot.Quota[0].Kind; got != tc.want {
				t.Errorf("kind = %q, want %q", got, tc.want)
			}
			sameSet(t, "missing", s.Snapshot.Missing)
			encoded, err := json.Marshal(s.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range []string{"\x1b", `\u001b`, `\u0007`, `\u0001`} {
				if strings.Contains(string(encoded), raw) {
					t.Errorf("stored snapshot %s still carries %q", encoded, raw)
				}
			}
		})
	}
}

func TestSanitizeKind(t *testing.T) {
	cases := map[string]string{
		"monthly":               "monthly",
		"":                      "unknown",
		"\x1b[31m":              "unknown",
		"\x01\x02\x7f":          "unknown",
		"\x1b[31mred\x1b[0m":    "red",
		strings.Repeat("z", 40): strings.Repeat("z", 32),
	}
	for in, want := range cases {
		if got := measurement.SanitizeKind(in); got != want {
			t.Errorf("SanitizeKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// P4, P16: an entry whose kind is absent or not a string cannot be classified,
// so it is stored under the fallback kind "unknown:unknown" (never a recognized
// window) and keeps its own valid percent_used and resets_at; the kind is named
// in missing, and in invalid too when it was present but of the wrong type.
func TestParse_RateLimitKindThatIsAbsentOrNotAStringIsStoredAsUnknownWithValidSiblings(t *testing.T) {
	assertKept := func(t *testing.T, m watch.NativeMeasurement) {
		t.Helper()
		if len(m.Quota) != 1 {
			t.Fatalf("quota = %+v, want the entry stored", m.Quota)
		}
		q := m.Quota[0]
		if q.Kind != "unknown:unknown" {
			t.Errorf("kind = %q, want unknown:unknown (never five_hour or weekly)", q.Kind)
		}
		floatIs(t, "percent_used", q.PercentUsed, 5)
		if q.ResetsAt != resetsFive.Format(time.RFC3339) {
			t.Errorf("resets_at = %q, want %q", q.ResetsAt, resetsFive.Format(time.RFC3339))
		}
	}

	t.Run("absent lists missing only", func(t *testing.T) {
		m := mustParse(t, doc(map[string]string{"rate_limits": limits(limit("", `5`, ts(resetsFive)))})).Snapshot
		sameSet(t, "missing", m.Missing, "rate_limits[0].kind")
		sameSet(t, "invalid", m.Invalid)
		assertKept(t, m)
	})
	for _, bad := range []string{`5`, `null`, `true`, `{}`} {
		t.Run("rejected "+bad, func(t *testing.T) {
			m := mustParse(t, doc(map[string]string{"rate_limits": limits(limit(bad, `5`, ts(resetsFive)))})).Snapshot
			sameSet(t, "missing", m.Missing, "rate_limits[0].kind")
			sameSet(t, "invalid", m.Invalid, "rate_limits[0].kind")
			assertKept(t, m)
		})
	}

	t.Run("kind-less entry beside a recognized one leaves the recognized window untouched", func(t *testing.T) {
		m := mustParse(t, doc(map[string]string{"rate_limits": limits(limit("", `5`, ts(resetsFive)), sevenDay)})).Snapshot
		sameSet(t, "missing", m.Missing, "rate_limits[0].kind")
		if len(m.Quota) != 2 {
			t.Fatalf("quota = %+v, want both entries", m.Quota)
		}
		if m.Quota[0].Kind != "unknown:unknown" || m.Quota[1].Kind != "weekly" {
			t.Errorf("kinds = %q, %q, want unknown:unknown, weekly", m.Quota[0].Kind, m.Quota[1].Kind)
		}
	})

	t.Run("other fields of the entry keep their own validation", func(t *testing.T) {
		m := mustParse(t, doc(map[string]string{"rate_limits": limits(limit(`7`, `500`, ts(resetsFive)))})).Snapshot
		sameSet(t, "missing", m.Missing, "rate_limits[0].kind", "rate_limits[0].percent_used")
		sameSet(t, "invalid", m.Invalid, "rate_limits[0].kind", "rate_limits[0].percent_used")
		if len(m.Quota) != 1 || m.Quota[0].Kind != "unknown:unknown" || m.Quota[0].PercentUsed != nil {
			t.Fatalf("quota = %+v, want an unknown entry with nil percent_used", m.Quota)
		}
		if m.Quota[0].ResetsAt != resetsFive.Format(time.RFC3339) {
			t.Errorf("resets_at = %q, want it stored", m.Quota[0].ResetsAt)
		}
	})
}

// -- P17: numeric fields ------------------------------------------------------

// badNumbers are tokens a producer can emit that must never be stored as a
// number: a bare NaN token is not valid JSON and is dropped by the receiver, so
// the string forms are what reaches Parse.
var badNumbers = []string{`"NaN"`, `"Infinity"`, `"-Infinity"`, `1e999`, `-1`, `"12"`, `null`, `true`, `[]`, `{}`}

func TestParse_BadNumberRejectsOnlyThatField(t *testing.T) {
	cases := []struct {
		field string
		body  func(bad string) []byte
		check func(t *testing.T, ctx *watch.NativeContext, quota []watch.NativeQuotaWindow)
	}{
		{
			"context.tokens",
			func(bad string) []byte {
				return doc(map[string]string{"context": ctxDoc(bad, `200000`, `50`)})
			},
			func(t *testing.T, ctx *watch.NativeContext, quota []watch.NativeQuotaWindow) {
				if ctx == nil || ctx.Tokens != nil {
					t.Fatalf("tokens = %v, want a stored context with nil tokens (a rejected value is never 0)", ctx)
				}
				int64Is(t, "window", ctx.Window, 200000)
				floatIs(t, "percent", ctx.Percent, 50)
			},
		},
		{
			"context.window",
			func(bad string) []byte {
				return doc(map[string]string{"context": ctxDoc(`100000`, bad, `50`)})
			},
			func(t *testing.T, ctx *watch.NativeContext, quota []watch.NativeQuotaWindow) {
				if ctx == nil || ctx.Window != nil {
					t.Fatalf("window = %v, want a stored context with nil window", ctx)
				}
				int64Is(t, "tokens", ctx.Tokens, 100000)
				floatIs(t, "percent", ctx.Percent, 50)
			},
		},
		{
			"context.percent",
			func(bad string) []byte {
				return doc(map[string]string{"context": ctxDoc(`100000`, `200000`, bad)})
			},
			func(t *testing.T, ctx *watch.NativeContext, quota []watch.NativeQuotaWindow) {
				if ctx == nil || ctx.Percent != nil {
					t.Fatalf("percent = %v, want a stored context with nil percent", ctx)
				}
				int64Is(t, "tokens", ctx.Tokens, 100000)
				int64Is(t, "window", ctx.Window, 200000)
			},
		},
		{
			"rate_limits[0].percent_used",
			func(bad string) []byte {
				return doc(map[string]string{"rate_limits": limits(limit(`"five_hour"`, bad, ts(resetsFive)), sevenDay)})
			},
			func(t *testing.T, ctx *watch.NativeContext, quota []watch.NativeQuotaWindow) {
				if len(quota) != 2 || quota[0].Kind != "five_hour" || quota[0].PercentUsed != nil {
					t.Errorf("quota = %+v, want the five-hour entry kept with a nil percent_used", quota)
					return
				}
				floatIs(t, "weekly percent_used", quota[1].PercentUsed, 10)
			},
		},
	}
	for _, tc := range cases {
		for _, bad := range badNumbers {
			t.Run(tc.field+"="+bad, func(t *testing.T) {
				m := mustParse(t, tc.body(bad)).Snapshot
				sameSet(t, "missing", m.Missing, tc.field)
				sameSet(t, "invalid", m.Invalid, tc.field)
				tc.check(t, m.Context, m.Quota)
			})
		}
	}
}

func TestParse_NumericBoundaries(t *testing.T) {
	type tc struct {
		name     string
		over     map[string]string
		rejected string // field expected in missing and invalid; "" = accepted
	}
	cases := []tc{
		{"percent_used above 100", map[string]string{"rate_limits": limits(limit(`"five_hour"`, `100.0001`, ts(resetsFive)))}, "rate_limits[0].percent_used"},
		{"percent_used below 0", map[string]string{"rate_limits": limits(limit(`"five_hour"`, `-0.1`, ts(resetsFive)))}, "rate_limits[0].percent_used"},
		{"percent_used of 0 is valid", map[string]string{"rate_limits": limits(limit(`"five_hour"`, `0`, ts(resetsFive)))}, ""},
		{"percent_used of 100 is valid", map[string]string{"rate_limits": limits(limit(`"five_hour"`, `100`, ts(resetsFive)))}, ""},
		{"negative percent", map[string]string{"context": ctxDoc(`1`, `200000`, `-0.1`)}, "context.percent"},
		{"fractional tokens", map[string]string{"context": ctxDoc(`1.5`, `200000`, `50`)}, "context.tokens"},
		{"zero tokens is valid", map[string]string{"context": ctxDoc(`0`, `200000`, `0`)}, ""},
		{"zero window", map[string]string{"context": ctxDoc(`1`, `0`, `50`)}, "context.window"},
		{"negative window", map[string]string{"context": ctxDoc(`1`, `-5`, `50`)}, "context.window"},
		{"fractional window", map[string]string{"context": ctxDoc(`1`, `200000.5`, `50`)}, "context.window"},
		{"window above 2^53", map[string]string{"context": ctxDoc(`1`, `9007199254740994`, `50`)}, "context.window"},
		{"window of exactly 2^53 is valid", map[string]string{"context": ctxDoc(`1`, `9007199254740992`, `50`)}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := mustParse(t, doc(c.over)).Snapshot
			if c.rejected == "" {
				sameSet(t, "missing", m.Missing)
				sameSet(t, "invalid", m.Invalid)
				return
			}
			sameSet(t, "missing", m.Missing, c.rejected)
			sameSet(t, "invalid", m.Invalid, c.rejected)
		})
	}
}

// -- P18: over-window context -------------------------------------------------

func TestParse_PercentAboveHundredIsStoredWithOverWindowNotClamped(t *testing.T) {
	cases := []struct {
		percent string
		want    float64
		over    bool
	}{
		{"150", 150, true},
		{"100.5", 100.5, true},
		{"100", 100, false},
		{"99.9", 99.9, false},
	}
	for _, c := range cases {
		t.Run(c.percent, func(t *testing.T) {
			m := mustParse(t, doc(map[string]string{"context": ctxDoc(`250000`, `200000`, c.percent)})).Snapshot
			if m.Context == nil {
				t.Fatal("context not stored")
			}
			floatIs(t, "percent", m.Context.Percent, c.want)
			if m.Context.OverWindow != c.over {
				t.Errorf("OverWindow = %v, want %v", m.Context.OverWindow, c.over)
			}
			sameSet(t, "missing", m.Missing)
			sameSet(t, "invalid", m.Invalid)
		})
	}
}

// -- context shapes -----------------------------------------------------------

func TestParse_AbsentContextListsOnlyContext(t *testing.T) {
	m := mustParse(t, doc(map[string]string{"context": ""})).Snapshot
	if m.Context != nil {
		t.Errorf("context = %+v, want nil", m.Context)
	}
	sameSet(t, "missing", m.Missing, "context")
	sameSet(t, "invalid", m.Invalid)
	if len(m.Quota) != 2 {
		t.Errorf("quota = %+v, want it kept", m.Quota)
	}
}

func TestParse_ContextThatIsNotAnObjectIsInvalid(t *testing.T) {
	for _, bad := range []string{`5`, `"x"`, `[]`, `true`} {
		t.Run(bad, func(t *testing.T) {
			m := mustParse(t, doc(map[string]string{"context": bad})).Snapshot
			if m.Context != nil {
				t.Errorf("context = %+v, want nil", m.Context)
			}
			sameSet(t, "missing", m.Missing, "context")
			sameSet(t, "invalid", m.Invalid, "context")
		})
	}
}

func TestParse_AbsentContextSubfieldsAreMissingButNotInvalid(t *testing.T) {
	m := mustParse(t, doc(map[string]string{"context": ctxDoc(`100000`, "", "")})).Snapshot
	sameSet(t, "missing", m.Missing, "context.window", "context.percent")
	sameSet(t, "invalid", m.Invalid)
	if m.Context == nil {
		t.Fatal("the valid tokens must be stored")
	}
	int64Is(t, "tokens", m.Context.Tokens, 100000)
}

// -- source, claude_version, resets_at ---------------------------------------

func TestParse_AbsentSourceAndVersionAreMissingNotInvalid(t *testing.T) {
	m := mustParse(t, doc(map[string]string{"source": "", "claude_version": ""})).Snapshot
	sameSet(t, "missing", m.Missing, "source", "claude_version")
	sameSet(t, "invalid", m.Invalid)
	if m.Source != "" || m.ClaudeVersion != "" {
		t.Errorf("source/version = %q/%q, want empty", m.Source, m.ClaudeVersion)
	}
}

func TestParse_SourceAndVersionOfTheWrongTypeAreInvalid(t *testing.T) {
	for _, field := range []string{"source", "claude_version"} {
		for _, bad := range []string{`5`, `null`, `true`, `{}`} {
			t.Run(field+"="+bad, func(t *testing.T) {
				m := mustParse(t, doc(map[string]string{field: bad})).Snapshot
				sameSet(t, "missing", m.Missing, field)
				sameSet(t, "invalid", m.Invalid, field)
			})
		}
	}
}

func TestParse_ResetsAtThatIsNotRFC3339RejectsOnlyThatField(t *testing.T) {
	for _, bad := range []string{`"tomorrow"`, `"2026-11-08"`, `"2026-11-08 15:00:00"`, `"2026-11-08T15:00:00"`, `12345`, `null`} {
		t.Run(bad, func(t *testing.T) {
			m := mustParse(t, doc(map[string]string{"rate_limits": limits(limit(`"five_hour"`, `42.5`, bad), sevenDay)})).Snapshot
			sameSet(t, "missing", m.Missing, "rate_limits[0].resets_at")
			sameSet(t, "invalid", m.Invalid, "rate_limits[0].resets_at")
			if len(m.Quota) != 2 {
				t.Fatalf("quota = %+v", m.Quota)
			}
			if m.Quota[0].ResetsAt != "" {
				t.Errorf("resets_at = %q, want empty for a rejected value", m.Quota[0].ResetsAt)
			}
			floatIs(t, "percent_used", m.Quota[0].PercentUsed, 42.5)
		})
	}
}

func TestParse_AbsentEntryFieldsAreMissingNotInvalidAndIndexed(t *testing.T) {
	m := mustParse(t, doc(map[string]string{"rate_limits": limits(fiveHour, limit(`"seven_day"`, "", ""))})).Snapshot
	sameSet(t, "missing", m.Missing, "rate_limits[1].percent_used", "rate_limits[1].resets_at")
	sameSet(t, "invalid", m.Invalid)
}

// P4: valid fields survive next to rejected and absent ones.
func TestParse_MixedSampleStoresEveryValidFieldAndNamesTheRest(t *testing.T) {
	m := mustParse(t, doc(map[string]string{
		"source":      "",
		"context":     ctxDoc(`"abc"`, `200000`, `50`),
		"rate_limits": limits(limit(`"five_hour"`, `42.5`, `"soon"`)),
	})).Snapshot

	sameSet(t, "missing", m.Missing, "source", "context.tokens", "rate_limits[0].resets_at")
	sameSet(t, "invalid", m.Invalid, "context.tokens", "rate_limits[0].resets_at")
	if m.ClaudeVersion != "2.1.0" {
		t.Errorf("claude_version = %q, want it stored", m.ClaudeVersion)
	}
	if m.Context == nil {
		t.Fatal("valid context fields must be stored")
	}
	int64Is(t, "window", m.Context.Window, 200000)
	floatIs(t, "percent", m.Context.Percent, 50)
	if len(m.Quota) != 1 {
		t.Fatalf("quota = %+v", m.Quota)
	}
	floatIs(t, "percent_used", m.Quota[0].PercentUsed, 42.5)
}

// -- whole-sample drops -------------------------------------------------------

func TestParse_WholeSampleErrors(t *testing.T) {
	longID := strings.Repeat("a", 129)
	cases := []struct {
		name string
		raw  []byte
		want error
	}{
		{"empty input", []byte(""), measurement.ErrMalformed},
		{"truncated JSON", []byte(`{"session_id":`), measurement.ErrMalformed},
		{"not JSON", []byte(`not json`), measurement.ErrMalformed},
		{"array", []byte(`[]`), measurement.ErrMalformed},
		{"null", []byte(`null`), measurement.ErrMalformed},
		{"string", []byte(`"x"`), measurement.ErrMalformed},

		{"session_id absent", doc(map[string]string{"session_id": ""}), measurement.ErrInvalidSession},
		{"session_id empty", doc(map[string]string{"session_id": `""`}), measurement.ErrInvalidSession},
		{"session_id number", doc(map[string]string{"session_id": `5`}), measurement.ErrInvalidSession},
		{"session_id null", doc(map[string]string{"session_id": `null`}), measurement.ErrInvalidSession},
		{"session_id path", doc(map[string]string{"session_id": `"../etc"`}), measurement.ErrInvalidSession},
		{"session_id leading dash", doc(map[string]string{"session_id": `"-lead"`}), measurement.ErrInvalidSession},
		{"session_id space", doc(map[string]string{"session_id": `"has space"`}), measurement.ErrInvalidSession},
		{"session_id pane key", doc(map[string]string{"session_id": `"pane:%5"`}), measurement.ErrInvalidSession},
		{"session_id control byte", doc(map[string]string{"session_id": `"a\u001bb"`}), measurement.ErrInvalidSession},
		{"session_id too long", doc(map[string]string{"session_id": strconv.Quote(longID)}), measurement.ErrInvalidSession},

		{"measured_at absent", doc(map[string]string{"measured_at": ""}), measurement.ErrInvalidMeasuredAt},
		{"measured_at not a time", doc(map[string]string{"measured_at": `"yesterday"`}), measurement.ErrInvalidMeasuredAt},
		{"measured_at date only", doc(map[string]string{"measured_at": `"2026-11-08"`}), measurement.ErrInvalidMeasuredAt},
		{"measured_at without zone", doc(map[string]string{"measured_at": `"2026-11-08T11:59:00"`}), measurement.ErrInvalidMeasuredAt},
		{"measured_at number", doc(map[string]string{"measured_at": `1762603140`}), measurement.ErrInvalidMeasuredAt},
		{"measured_at null", doc(map[string]string{"measured_at": `null`}), measurement.ErrInvalidMeasuredAt},
		{"measured_at beyond the future tolerance", doc(map[string]string{"measured_at": ts(now.Add(5*time.Minute + time.Second))}), measurement.ErrInvalidMeasuredAt},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := measurement.Parse(c.raw, now)
			if !errors.Is(err, c.want) {
				t.Fatalf("Parse(%s) error = %v, want %v", c.raw, err, c.want)
			}
			if s.SessionID != "" || s.Snapshot.Context != nil || s.Snapshot.Quota != nil {
				t.Errorf("a dropped sample must return nothing, got %+v", s)
			}
		})
	}
}

func TestParse_AcceptedSessionIDsAndTimestamps(t *testing.T) {
	for _, id := range []string{"a", "A1._-x", strings.Repeat("a", 128), "0b5f1c9e-1d2e-4f3a-9b8c-7d6e5f4a3b2c"} {
		s := mustParse(t, doc(map[string]string{"session_id": strconv.Quote(id)}))
		if s.SessionID != id {
			t.Errorf("SessionID = %q, want %q", s.SessionID, id)
		}
	}

	t.Run("exactly at the future tolerance", func(t *testing.T) {
		s := mustParse(t, doc(map[string]string{"measured_at": ts(now.Add(5 * time.Minute))}))
		if want := now.Add(5 * time.Minute); !s.MeasuredAt.Equal(want) {
			t.Errorf("MeasuredAt = %v, want %v", s.MeasuredAt, want)
		}
	})
	t.Run("an old sample is accepted and classified stale later", func(t *testing.T) {
		mustParse(t, doc(map[string]string{"measured_at": ts(now.Add(-72 * time.Hour))}))
	})
	t.Run("a zone offset names the same instant", func(t *testing.T) {
		s := mustParse(t, doc(map[string]string{"measured_at": `"2026-11-08T13:59:00+02:00"`}))
		if want := time.Date(2026, 11, 8, 11, 59, 0, 0, time.UTC); !s.MeasuredAt.Equal(want) {
			t.Errorf("MeasuredAt = %v, want %v", s.MeasuredAt, want)
		}
	})
}

// -- ignored fields -----------------------------------------------------------

func TestParse_CostAndUnknownKeysAreIgnoredAndNeverStored(t *testing.T) {
	plain := mustParse(t, doc(nil))
	extra := mustParse(t, doc(map[string]string{
		"cost":        `{"usd":1.5}`,
		"extra_key":   `"surprise"`,
		"context":     `{"tokens":100000,"window":200000,"percent":50,"cost":2.5}`,
		"rate_limits": limits(`{"kind":"five_hour","percent_used":42.5,"resets_at":`+ts(resetsFive)+`,"cost":3}`, sevenDay),
	}))

	if !reflect.DeepEqual(plain.Snapshot, extra.Snapshot) {
		t.Errorf("snapshot with extra keys = %+v, want it identical to %+v", extra.Snapshot, plain.Snapshot)
	}
	encoded, err := json.Marshal(extra.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"cost", "surprise", "extra_key"} {
		if strings.Contains(string(encoded), leaked) {
			t.Errorf("stored snapshot %s leaks %q", encoded, leaked)
		}
	}
}

// -- P20: classification ------------------------------------------------------

func TestStaleAfterIsFifteenMinutes(t *testing.T) {
	if measurement.StaleAfter != 15*time.Minute {
		t.Errorf("StaleAfter = %v, want 15m", measurement.StaleAfter)
	}
}

func TestFreshness(t *testing.T) {
	cases := []struct {
		name       string
		measuredAt time.Time
		want       string
	}{
		{"just measured", now, "fresh"},
		{"exactly 15 minutes old", now.Add(-15 * time.Minute), "fresh"},
		{"15 minutes and 1ns old", now.Add(-15*time.Minute - time.Nanosecond), "stale"},
		{"hours old", now.Add(-3 * time.Hour), "stale"},
		{"slightly in the future", now.Add(time.Minute), "fresh"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := measurement.Freshness(c.measuredAt, now); got != c.want {
				t.Errorf("Freshness = %q, want %q", got, c.want)
			}
		})
	}
}

func TestQuotaState(t *testing.T) {
	cases := []struct {
		name     string
		resetsAt time.Time
		want     string
	}{
		{"resets later", now.Add(time.Hour), "current"},
		{"resets exactly now", now, "current"},
		{"resets in 1ns", now.Add(time.Nanosecond), "current"},
		{"reset passed by 1ns", now.Add(-time.Nanosecond), "expired (reset passed)"},
		{"reset passed long ago", now.Add(-24 * time.Hour), "expired (reset passed)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := measurement.QuotaState(c.resetsAt, now); got != c.want {
				t.Errorf("QuotaState = %q, want %q", got, c.want)
			}
		})
	}
}

// -- P45: no identity ---------------------------------------------------------

// TestPackageImportsNoOSFacingPackage pins that the validator cannot read the
// environment, files, processes or users, so no account or profile identity can
// be derived inside it.
func TestPackageImportsNoOSFacingPackage(t *testing.T) {
	forbidden := map[string]bool{
		"os": true, "os/exec": true, "os/user": true, "syscall": true,
		"io/fs": true, "path/filepath": true, "net": true, "net/http": true,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if forbidden[path] {
				t.Errorf("%s imports %q; internal/measurement must not reach the OS", name, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no non-test source file to check")
	}
}

// -- rate_limits entry cap ------------------------------------------------------

const (
	// socketFrame is the receiver's per-line cap (ipc eventMaxBytes): the largest
	// body a producer can deliver in one line.
	socketFrame = 4096
	// encodedBudget bounds the stored measurement's JSON size so a snapshot of
	// many sessions stays far below the client's 64 KiB line limit.
	encodedBudget = 2048
)

func repeatEntry(entry string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = entry
	}
	return out
}

// maxFrameBody renders the largest body that fits socketFrame, filling
// rate_limits with entry.
func maxFrameBody(entry string) []byte {
	build := func(n int) []byte {
		return doc(map[string]string{"rate_limits": limits(repeatEntry(entry, n)...)})
	}
	n := 0
	for len(build(n+1)) <= socketFrame {
		n++
	}
	return build(n)
}

func TestParse_MoreThanMaxRateLimitEntriesRejectsOnlyRateLimits(t *testing.T) {
	entries := repeatEntry(fiveHour, measurement.MaxRateLimitEntries+1)
	m := mustParse(t, doc(map[string]string{"rate_limits": limits(entries...)})).Snapshot

	if m.Quota != nil {
		t.Errorf("quota = %+v, want nil for an over-long rate_limits", m.Quota)
	}
	sameSet(t, "missing", m.Missing, "rate_limits")
	sameSet(t, "invalid", m.Invalid, "rate_limits")
	if m.Context == nil || m.Source != "claude-mods" || m.ClaudeVersion != "2.1.0" {
		t.Errorf("context/source/version = %+v/%q/%q, want the other fields stored", m.Context, m.Source, m.ClaudeVersion)
	}
}

func TestParse_ExactlyMaxRateLimitEntriesIsAccepted(t *testing.T) {
	entries := repeatEntry(fiveHour, measurement.MaxRateLimitEntries)
	m := mustParse(t, doc(map[string]string{"rate_limits": limits(entries...)})).Snapshot

	if len(m.Quota) != measurement.MaxRateLimitEntries {
		t.Errorf("quota has %d windows, want %d", len(m.Quota), measurement.MaxRateLimitEntries)
	}
	sameSet(t, "missing", m.Missing)
	sameSet(t, "invalid", m.Invalid)
}

func TestParse_MaxFrameBodyStaysWithinTheEncodedBudget(t *testing.T) {
	for name, entry := range map[string]string{"empty objects": `{}`, "bare numbers": `1`} {
		t.Run(name, func(t *testing.T) {
			raw := maxFrameBody(entry)
			if len(raw) > socketFrame || len(raw) < socketFrame-16 {
				t.Fatalf("fixture is %d bytes, want it to fill the %d byte frame", len(raw), socketFrame)
			}
			m := mustParse(t, raw).Snapshot

			sameSet(t, "missing", m.Missing, "rate_limits")
			sameSet(t, "invalid", m.Invalid, "rate_limits")
			encoded, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) >= encodedBudget {
				t.Errorf("stored measurement encodes to %d bytes, want < %d", len(encoded), encodedBudget)
			}
		})
	}
}

// TestParseIgnoresIdentityEnvironment pins that the output depends on the
// input and the injected clock only: identity-bearing variables change nothing.
func TestParseIgnoresIdentityEnvironment(t *testing.T) {
	raw := doc(nil)
	before := mustParse(t, raw)

	for _, name := range []string{
		"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN",
		"CLAUDE_CONFIG_DIR", "USER", "LOGNAME", "HOME", "AWS_PROFILE", "GH_TOKEN",
	} {
		t.Setenv(name, "identity-"+strings.ToLower(name))
	}
	after := mustParse(t, raw)

	if !reflect.DeepEqual(before, after) {
		t.Errorf("output changed with identity env vars set: %+v vs %+v", before, after)
	}
	encoded, err := json.Marshal(after.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "identity-") {
		t.Errorf("snapshot %s carries an environment value", encoded)
	}
}
