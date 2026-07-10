package dispatch

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TokenReader reports the total output tokens consumed since a point in time.
// Implementations scan their agent's local session data (JSONL files for Claude,
// SQLite for Codex). Tests inject fixture-backed readers.
type TokenReader interface {
	TokensInWindow(since time.Time) (int64, error)
}

// AgentLimit defines the per-window token caps for one agent. A zero limit
// disables that window's constraint (treated as unlimited).
type AgentLimit struct {
	FiveHourTokens int64 `json:"fiveHourTokens"`
	WeeklyTokens   int64 `json:"weeklyTokens"`
}

// UsageProvider is the real BudgetProvider shipped by #77. It reads live session
// data through TokenReaders, computes headroom against configured limits, and
// compares it to the per-agent floor. An agent without a configured reader or
// limit is Unlimited (same as the #45 stub).
type UsageProvider struct {
	Readers map[string]TokenReader
	Limits  map[string]AgentLimit
	// Floors carries a dual meaning per agent: when a reader+limit exist it is
	// subtracted as a threshold from the computed headroom; in the no-reader
	// fallback it is used directly as Remaining (matching FloorProvider).
	Floors map[string]float64
	Now    time.Time
	// cache memoizes Budget results within a single RunOnce pass. Budget does
	// live I/O (dir walks, sqlite3 spawns) and Decide calls it once per agent
	// per ticket; a fresh UsageProvider is built per pass so staleness is bounded
	// to the pass. Populated lazily; safe because Decide is single-threaded.
	cache map[string]Budget
}

func (p *UsageProvider) Budget(agent string) Budget {
	if b, ok := p.cache[agent]; ok {
		return b
	}
	b := p.computeBudget(agent)
	if p.cache != nil {
		p.cache[agent] = b
	}
	return b
}

func (p *UsageProvider) computeBudget(agent string) Budget {
	reader, hasReader := p.Readers[agent]
	limit, hasLimit := p.Limits[agent]
	if !hasReader || !hasLimit {
		if floor, ok := p.Floors[agent]; ok {
			return Budget{Agent: agent, Remaining: floor}
		}
		return Budget{Agent: agent, Unlimited: true}
	}

	fiveH := windowHeadroom(reader, p.Now.Add(-5*time.Hour), limit.FiveHourTokens)
	weekly := windowHeadroom(reader, p.Now.Add(-7*24*time.Hour), limit.WeeklyTokens)

	headroom := fiveH
	if weekly < headroom {
		headroom = weekly
	}

	floor := p.Floors[agent]
	return Budget{Agent: agent, Remaining: headroom - floor}
}

// windowHeadroom returns the fraction of the limit still available (0.0–1.0).
// A zero limit is unlimited (returns 1.0). Read errors return 0.0 (safe: assume
// exhausted rather than dispatching blind).
func windowHeadroom(r TokenReader, since time.Time, limit int64) float64 {
	if limit <= 0 {
		return 1.0
	}
	used, err := r.TokensInWindow(since)
	if err != nil {
		return 0
	}
	h := float64(limit-used) / float64(limit)
	if h < 0 {
		return 0
	}
	return h
}

// ClaudeTokenReader scans Claude Code session JSONL files under BaseDir
// (typically ~/.claude/projects). Each project subdirectory contains
// <session-id>.jsonl files; each line of type "assistant" carries
// message.usage.output_tokens.
type ClaudeTokenReader struct {
	BaseDir string
}

// claudeEntry is the minimal structure parsed from each JSONL line.
type claudeEntry struct {
	Type      string      `json:"type"`
	Timestamp string      `json:"timestamp"`
	Message   claudeUsage `json:"message"`
}

type claudeUsage struct {
	Usage struct {
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (r *ClaudeTokenReader) TokensInWindow(since time.Time) (int64, error) {
	sinceMS := since.UnixMilli()
	projectDirs, err := os.ReadDir(r.BaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading claude projects dir: %w", err)
	}

	var total int64
	for _, pd := range projectDirs {
		if !pd.IsDir() {
			continue
		}
		dir := filepath.Join(r.BaseDir, pd.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().UnixMilli() < sinceMS {
				continue
			}
			// scanClaudeJSONL returns its partial sum alongside any error; add
			// the partial rather than discarding it. Dropping already-counted
			// tokens would undercount usage and risk over-dispatching.
			tokens, _ := scanClaudeJSONL(filepath.Join(dir, e.Name()), since)
			total += tokens
		}
	}
	return total, nil
}

func scanClaudeJSONL(path string, since time.Time) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	var total int64
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var entry claudeEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Type != "assistant" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
		if err != nil {
			continue
		}
		if ts.Before(since) {
			continue
		}
		total += entry.Message.Usage.OutputTokens
	}
	return total, scanner.Err()
}

// CodexTokenReader queries the Codex CLI SQLite database (typically
// ~/.codex/state_5.sqlite) for per-thread token totals. Threads whose
// updated_at_ms falls within the window contribute their full tokens_used count.
// This is a conservative overcount for long-lived threads — the safe direction
// for budget enforcement. Uses the sqlite3 CLI to stay stdlib-only.
type CodexTokenReader struct {
	DBPath string
}

func (r *CodexTokenReader) TokensInWindow(since time.Time) (int64, error) {
	// Guard the missing-DB case explicitly: sqlite3 exits non-zero (not an
	// os.IsNotExist error) on a missing file and leaves a stray 0-byte DB
	// behind, which would mark codex permanently budget-exhausted.
	if _, err := os.Stat(r.DBPath); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat codex db: %w", err)
	}

	sinceMS := since.UnixMilli()
	query := fmt.Sprintf(
		"SELECT COALESCE(SUM(tokens_used), 0) FROM threads WHERE updated_at_ms >= %d",
		sinceMS,
	)
	out, err := exec.Command("sqlite3", r.DBPath, query).Output()
	if err != nil {
		return 0, fmt.Errorf("querying codex db: %w", err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing codex token sum: %w", err)
	}
	return n, nil
}
