package dispatch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubReader is a test TokenReader that returns a fixed token count.
type stubReader struct {
	tokens int64
	err    error
}

func (s stubReader) TokensInWindow(time.Time) (int64, error) { return s.tokens, s.err }

// --- UsageProvider tests ---

func TestUsageProviderUnlimitedWhenNoReaderOrLimit(t *testing.T) {
	p := &UsageProvider{Now: time.Now()}
	b := p.Budget("claude")
	if !b.Unlimited {
		t.Errorf("expected unlimited when no reader/limit configured, got %+v", b)
	}
}

func TestUsageProviderFallsBackToFloorWhenNoReader(t *testing.T) {
	p := &UsageProvider{
		Floors: map[string]float64{"claude": 0.3},
		Now:    time.Now(),
	}
	b := p.Budget("claude")
	if b.Unlimited {
		t.Error("should not be unlimited when a floor is set")
	}
	if b.Remaining != 0.3 {
		t.Errorf("Remaining = %v, want 0.3", b.Remaining)
	}
}

func TestUsageProviderHeadroomComputation(t *testing.T) {
	tests := []struct {
		name      string
		tokens    int64
		fiveH     int64
		weekly    int64
		floor     float64
		wantAbove bool // Remaining > 0
	}{
		{
			name:      "well under limits",
			tokens:    1000,
			fiveH:     10000,
			weekly:    100000,
			floor:     0.3,
			wantAbove: true, // headroom 0.9, 0.99 → min 0.9 − 0.3 = 0.6
		},
		{
			name:      "at floor exactly",
			tokens:    7000,
			fiveH:     10000,
			weekly:    100000,
			floor:     0.3,
			wantAbove: false, // headroom 0.3, 0.93 → min 0.3 − 0.3 = 0.0
		},
		{
			name:      "below floor",
			tokens:    8000,
			fiveH:     10000,
			weekly:    100000,
			floor:     0.3,
			wantAbove: false, // headroom 0.2 − 0.3 = −0.1
		},
		{
			name:      "over limit",
			tokens:    15000,
			fiveH:     10000,
			weekly:    100000,
			floor:     0,
			wantAbove: false, // headroom 0 (clamped)
		},
		{
			name:      "zero floor passes at any headroom",
			tokens:    9999,
			fiveH:     10000,
			weekly:    100000,
			floor:     0,
			wantAbove: true, // headroom 0.0001 − 0 > 0
		},
		{
			name:      "weekly window tighter",
			tokens:    90000,
			fiveH:     200000,
			weekly:    100000,
			floor:     0.2,
			wantAbove: false, // 5h headroom 0.55, weekly 0.1 → min 0.1 − 0.2 < 0
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &UsageProvider{
				Readers: map[string]TokenReader{"claude": stubReader{tokens: tc.tokens}},
				Limits:  map[string]AgentLimit{"claude": {FiveHourTokens: tc.fiveH, WeeklyTokens: tc.weekly}},
				Floors:  map[string]float64{"claude": tc.floor},
				Now:     time.Now(),
			}
			b := p.Budget("claude")
			if b.Unlimited {
				t.Fatal("should not be unlimited with reader+limit configured")
			}
			if tc.wantAbove && b.Remaining <= 0 {
				t.Errorf("Remaining = %v, want > 0", b.Remaining)
			}
			if !tc.wantAbove && b.Remaining > 0 {
				t.Errorf("Remaining = %v, want <= 0", b.Remaining)
			}
		})
	}
}

func TestUsageProviderZeroLimitIsUnlimitedWindow(t *testing.T) {
	p := &UsageProvider{
		Readers: map[string]TokenReader{"claude": stubReader{tokens: 999999}},
		Limits:  map[string]AgentLimit{"claude": {FiveHourTokens: 0, WeeklyTokens: 0}},
		Now:     time.Now(),
	}
	b := p.Budget("claude")
	// Both windows disabled → headroom 1.0, no floor → Remaining 1.0
	if b.Remaining != 1.0 {
		t.Errorf("Remaining = %v, want 1.0 (both windows disabled)", b.Remaining)
	}
}

func TestUsageProviderReaderError(t *testing.T) {
	p := &UsageProvider{
		Readers: map[string]TokenReader{"claude": stubReader{err: os.ErrNotExist}},
		Limits:  map[string]AgentLimit{"claude": {FiveHourTokens: 10000, WeeklyTokens: 100000}},
		Now:     time.Now(),
	}
	b := p.Budget("claude")
	// Error → headroom 0 → Remaining 0 (safe: assume exhausted)
	if b.Remaining > 0 {
		t.Errorf("Remaining = %v, want <= 0 on reader error", b.Remaining)
	}
}

// --- ClaudeTokenReader tests with fixture JSONL ---

func writeFixtureJSONL(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeTokenReaderSumsOutputTokens(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "project-a")

	fixture := `{"type":"mode","sessionId":"s1"}
{"type":"assistant","timestamp":"2026-07-10T10:00:00Z","message":{"usage":{"output_tokens":100}}}
{"type":"user","timestamp":"2026-07-10T10:01:00Z"}
{"type":"assistant","timestamp":"2026-07-10T10:02:00Z","message":{"usage":{"output_tokens":200}}}
{"type":"assistant","timestamp":"2026-07-10T09:00:00Z","message":{"usage":{"output_tokens":50}}}
`
	writeFixtureJSONL(t, project, "session-1.jsonl", fixture)

	reader := &ClaudeTokenReader{BaseDir: base}
	since := time.Date(2026, 7, 10, 9, 30, 0, 0, time.UTC)
	tokens, err := reader.TokensInWindow(since)
	if err != nil {
		t.Fatal(err)
	}
	// Two entries at 10:00 (100) and 10:02 (200) are after 09:30; the 09:00 entry is before.
	if tokens != 300 {
		t.Errorf("tokens = %d, want 300", tokens)
	}
}

func TestClaudeTokenReaderMultipleProjects(t *testing.T) {
	base := t.TempDir()
	proj1 := filepath.Join(base, "project-a")
	proj2 := filepath.Join(base, "project-b")

	writeFixtureJSONL(t, proj1, "s1.jsonl",
		`{"type":"assistant","timestamp":"2026-07-10T12:00:00Z","message":{"usage":{"output_tokens":500}}}
`)
	writeFixtureJSONL(t, proj2, "s2.jsonl",
		`{"type":"assistant","timestamp":"2026-07-10T12:00:00Z","message":{"usage":{"output_tokens":300}}}
`)

	reader := &ClaudeTokenReader{BaseDir: base}
	tokens, err := reader.TokensInWindow(time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 800 {
		t.Errorf("tokens = %d, want 800", tokens)
	}
}

func TestClaudeTokenReaderMissingDir(t *testing.T) {
	reader := &ClaudeTokenReader{BaseDir: filepath.Join(t.TempDir(), "nonexistent")}
	tokens, err := reader.TokensInWindow(time.Now())
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if tokens != 0 {
		t.Errorf("tokens = %d, want 0", tokens)
	}
}

func TestClaudeTokenReaderSkipsNonAssistant(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "proj")

	fixture := `{"type":"user","timestamp":"2026-07-10T12:00:00Z","message":{"usage":{"output_tokens":999}}}
{"type":"system","timestamp":"2026-07-10T12:00:00Z"}
{"type":"assistant","timestamp":"2026-07-10T12:00:00Z","message":{"usage":{"output_tokens":42}}}
`
	writeFixtureJSONL(t, project, "s.jsonl", fixture)

	reader := &ClaudeTokenReader{BaseDir: base}
	tokens, err := reader.TokensInWindow(time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 42 {
		t.Errorf("tokens = %d, want 42 (only assistant entries)", tokens)
	}
}

func TestClaudeTokenReaderSkipsMalformedLines(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "proj")

	fixture := `not json at all
{"type":"assistant","timestamp":"bad-timestamp","message":{"usage":{"output_tokens":100}}}
{"type":"assistant","timestamp":"2026-07-10T12:00:00Z","message":{"usage":{"output_tokens":77}}}
`
	writeFixtureJSONL(t, project, "s.jsonl", fixture)

	reader := &ClaudeTokenReader{BaseDir: base}
	tokens, err := reader.TokensInWindow(time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 77 {
		t.Errorf("tokens = %d, want 77", tokens)
	}
}

func TestClaudeTokenReaderSkipsStaleFiles(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "proj")

	writeFixtureJSONL(t, project, "old.jsonl",
		`{"type":"assistant","timestamp":"2026-07-10T12:00:00Z","message":{"usage":{"output_tokens":100}}}
`)
	// Set mtime to well before the window
	old := filepath.Join(project, "old.jsonl")
	past := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	reader := &ClaudeTokenReader{BaseDir: base}
	tokens, err := reader.TokensInWindow(time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 0 {
		t.Errorf("tokens = %d, want 0 (stale file skipped)", tokens)
	}
}

// --- CodexTokenReader tests with a real SQLite DB ---

// buildCodexDB creates a threads table at path and inserts the given rows,
// each a (tokens_used, updated_at_ms) pair, via the sqlite3 CLI.
func buildCodexDB(t *testing.T, path string, rows [][2]int64) {
	t.Helper()
	stmts := "CREATE TABLE threads (tokens_used INTEGER, updated_at_ms INTEGER);\n"
	for _, r := range rows {
		stmts += fmt.Sprintf("INSERT INTO threads VALUES (%d, %d);\n", r[0], r[1])
	}
	cmd := exec.Command("sqlite3", path)
	cmd.Stdin = strings.NewReader(stmts)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building codex db: %v\n%s", err, out)
	}
}

func TestCodexTokenReaderSumsWithinWindow(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not on PATH")
	}
	since := time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC)
	sinceMS := since.UnixMilli()

	db := filepath.Join(t.TempDir(), "state.sqlite")
	buildCodexDB(t, db, [][2]int64{
		{100, sinceMS + 1000},   // in window
		{200, sinceMS + 60_000}, // in window
		{999, sinceMS - 60_000}, // before window → excluded
		{50, sinceMS},           // boundary (>=) → included
	})

	reader := &CodexTokenReader{DBPath: db}
	tokens, err := reader.TokensInWindow(since)
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 350 {
		t.Errorf("tokens = %d, want 350 (100+200+50, stale row excluded)", tokens)
	}
}

func TestCodexTokenReaderEmptyTable(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not on PATH")
	}
	db := filepath.Join(t.TempDir(), "state.sqlite")
	buildCodexDB(t, db, nil)

	reader := &CodexTokenReader{DBPath: db}
	tokens, err := reader.TokensInWindow(time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 0 {
		t.Errorf("tokens = %d, want 0 (empty table)", tokens)
	}
}

func TestCodexTokenReaderMissingDBReturnsZeroAndCreatesNoFile(t *testing.T) {
	db := filepath.Join(t.TempDir(), "nonexistent.sqlite")

	reader := &CodexTokenReader{DBPath: db}
	tokens, err := reader.TokensInWindow(time.Now())
	if err != nil {
		t.Fatalf("missing db should not error: %v", err)
	}
	if tokens != 0 {
		t.Errorf("tokens = %d, want 0 (missing db)", tokens)
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Errorf("missing-db path must not create a file, got stat err %v", err)
	}
}

// --- UsageProvider memoization ---

// countingReader records how many times TokensInWindow is invoked.
type countingReader struct {
	tokens int64
	calls  *int
}

func (c countingReader) TokensInWindow(time.Time) (int64, error) {
	*c.calls++
	return c.tokens, nil
}

func TestUsageProviderMemoizesBudget(t *testing.T) {
	calls := 0
	p := &UsageProvider{
		Readers: map[string]TokenReader{"claude": countingReader{tokens: 1000, calls: &calls}},
		Limits:  map[string]AgentLimit{"claude": {FiveHourTokens: 10000, WeeklyTokens: 100000}},
		Now:     time.Now(),
		cache:   make(map[string]Budget),
	}
	first := p.Budget("claude")
	second := p.Budget("claude")
	if first != second {
		t.Errorf("cached budget mismatch: %+v vs %+v", first, second)
	}
	// Two windows (5h + weekly) are read once on the first call; the second call
	// must be served from cache with no further reads.
	if calls != 2 {
		t.Errorf("reader called %d times, want 2 (cached after first Budget)", calls)
	}
}

// --- UsageProvider.Headroom tests (#169) ---
//
// Headroom() reports the pure remaining-budget fraction per configured agent
// type (min(fiveHour, weekly) from windowHeadroom), NOT the floor-adjusted
// value Budget().Remaining() returns. An agent with a reader+limit configured
// but both window caps at 0 ("unlimited window") is included as 1.0. An agent
// with no reader/limit configured at all is omitted entirely.

func TestUsageProviderHeadroomMultipleAgents(t *testing.T) {
	p := &UsageProvider{
		Readers: map[string]TokenReader{
			"claude": stubReader{tokens: 1000},
			"codex":  stubReader{tokens: 5000},
		},
		Limits: map[string]AgentLimit{
			"claude": {FiveHourTokens: 10000, WeeklyTokens: 100000},
			"codex":  {FiveHourTokens: 10000, WeeklyTokens: 100000},
		},
		Now: time.Now(),
	}
	h := p.Headroom()
	if got, want := h["claude"], 0.9; got != want {
		t.Errorf("claude headroom = %v, want %v", got, want)
	}
	if got, want := h["codex"], 0.5; got != want {
		t.Errorf("codex headroom = %v, want %v", got, want)
	}
}

func TestUsageProviderHeadroomReaderErrorDefaultsToZero(t *testing.T) {
	p := &UsageProvider{
		Readers: map[string]TokenReader{"claude": stubReader{err: os.ErrNotExist}},
		Limits:  map[string]AgentLimit{"claude": {FiveHourTokens: 10000, WeeklyTokens: 100000}},
		Now:     time.Now(),
	}
	h := p.Headroom()
	if got, want := h["claude"], 0.0; got != want {
		t.Errorf("claude headroom = %v, want %v (safe default on read error)", got, want)
	}
}

func TestUsageProviderHeadroomOmitsUnconfiguredAgent(t *testing.T) {
	p := &UsageProvider{
		Readers: map[string]TokenReader{"claude": stubReader{tokens: 1000}},
		Limits:  map[string]AgentLimit{"claude": {FiveHourTokens: 10000, WeeklyTokens: 100000}},
		Now:     time.Now(),
	}
	h := p.Headroom()
	if _, ok := h["codex"]; ok {
		t.Errorf("expected codex to be omitted (no reader/limit configured), got %v", h["codex"])
	}
	if len(h) != 1 {
		t.Errorf("expected exactly 1 agent in headroom map, got %d: %v", len(h), h)
	}
}

func TestUsageProviderHeadroomUnlimitedWindowIsOne(t *testing.T) {
	p := &UsageProvider{
		Readers: map[string]TokenReader{"claude": stubReader{tokens: 999999}},
		Limits:  map[string]AgentLimit{"claude": {FiveHourTokens: 0, WeeklyTokens: 0}},
		Now:     time.Now(),
	}
	h := p.Headroom()
	if got, want := h["claude"], 1.0; got != want {
		t.Errorf("claude headroom = %v, want %v (both windows unlimited)", got, want)
	}
}

func TestUsageProviderHeadroomIgnoresFloor(t *testing.T) {
	p := &UsageProvider{
		Readers: map[string]TokenReader{"claude": stubReader{tokens: 1000}},
		Limits:  map[string]AgentLimit{"claude": {FiveHourTokens: 10000, WeeklyTokens: 100000}},
		Floors:  map[string]float64{"claude": 0.5},
		Now:     time.Now(),
	}
	h := p.Headroom()
	// Pure headroom is 0.9 (min(0.9, 0.99)); Budget().Remaining() floor-adjusts
	// this down to 0.4. Headroom() must report the unadjusted value.
	if got, want := h["claude"], 0.9; got != want {
		t.Errorf("claude headroom = %v, want %v (floor must not be applied)", got, want)
	}
	b := p.Budget("claude")
	if b.Remaining != 0.4 {
		t.Errorf("sanity check: Budget().Remaining = %v, want 0.4 (floor-adjusted)", b.Remaining)
	}
}

// --- windowHeadroom tests ---

func TestWindowHeadroom(t *testing.T) {
	tests := []struct {
		name  string
		used  int64
		limit int64
		want  float64
	}{
		{"zero used", 0, 10000, 1.0},
		{"half used", 5000, 10000, 0.5},
		{"fully used", 10000, 10000, 0.0},
		{"over limit", 15000, 10000, 0.0},
		{"zero limit", 999, 0, 1.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := windowHeadroom(stubReader{tokens: tc.used}, time.Time{}, tc.limit)
			if got != tc.want {
				t.Errorf("windowHeadroom(%d, %d) = %v, want %v", tc.used, tc.limit, got, tc.want)
			}
		})
	}
}
