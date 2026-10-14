package ticketstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// maxConfigBytes caps how much of `.cenci/config.json` is read. A real config is
// a few hundred bytes; the cap only bounds a hostile or runaway file.
const maxConfigBytes = 64 << 10

// configRelPath is the per-repo config file, relative to the git toplevel.
var configRelPath = filepath.Join(".cenci", "config.json")

// CleanupEnabled reports whether window cleanup is enabled for the repo at
// toplevel, per `windowCleanup.enabled` in its working-tree
// `.cenci/config.json`. A missing file, block or key means enabled; an
// unreadable or malformed config, or a wrongly typed block or key (including a
// JSON null), is an error so the caller can fail open rather than guess.
func CleanupEnabled(toplevel string) (bool, error) {
	const block = "windowCleanup"
	path, fields, err := readConfigBlock(toplevel, block)
	if err != nil {
		return false, err
	}
	rawEnabled, ok := fields["enabled"]
	if !ok {
		return true, nil
	}
	return decodeBool(path, block, "enabled", rawEnabled)
}

// DefaultStaleAfter is the worktreeCleanup staleAfter used when the key is absent.
const DefaultStaleAfter = 7 * 24 * time.Hour

// WorktreeCleanup is the per-repo worktree-cleanup configuration.
type WorktreeCleanup struct {
	Enabled    bool
	StaleAfter time.Duration
}

// WorktreeCleanupConfig reads the `worktreeCleanup` block of the repo at
// toplevel's working-tree `.cenci/config.json`. A missing file, block or key
// means enabled with DefaultStaleAfter. Every known key is checked even when
// `enabled` is false, so a typo is reported rather than hidden; unknown keys are
// ignored. An unreadable or malformed config, a wrongly typed or null value, or a
// non-positive `staleAfter` is an error so the caller can fail open.
func WorktreeCleanupConfig(toplevel string) (WorktreeCleanup, error) {
	const block = "worktreeCleanup"
	cfg := WorktreeCleanup{Enabled: true, StaleAfter: DefaultStaleAfter}
	path, fields, err := readConfigBlock(toplevel, block)
	if err != nil {
		return WorktreeCleanup{}, err
	}
	if rawEnabled, ok := fields["enabled"]; ok {
		if cfg.Enabled, err = decodeBool(path, block, "enabled", rawEnabled); err != nil {
			return WorktreeCleanup{}, err
		}
	}
	if rawStale, ok := fields["staleAfter"]; ok {
		if cfg.StaleAfter, err = parseStaleAfter(path, block, rawStale); err != nil {
			return WorktreeCleanup{}, err
		}
	}
	return cfg, nil
}

// readConfigBlock reads the named top-level object block of toplevel's
// `.cenci/config.json` and returns the config path (for error messages) and the
// block's raw fields. The fields map is nil when the file or the block is absent;
// an unreadable or malformed file, or a block that is not an object (including a
// JSON null), is an error.
//
// The config is decoded in two stages through map[string]json.RawMessage so
// keys match exactly: struct-tag decoding would also accept a differently
// cased key (go-gotchas rule 15).
func readConfigBlock(toplevel, block string) (string, map[string]json.RawMessage, error) {
	path := filepath.Join(toplevel, configRelPath)
	data, err := readBoundedRegularFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil, nil
	}
	if err != nil {
		return path, nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return path, nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if top == nil {
		return path, nil, fmt.Errorf("parsing %s: top level is null, want an object", path)
	}
	rawBlock, ok := top[block]
	if !ok {
		return path, nil, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawBlock, &fields); err != nil {
		return path, nil, fmt.Errorf("%s: %s must be an object: %w", path, block, err)
	}
	if fields == nil {
		return path, nil, fmt.Errorf("%s: %s must be an object, got null", path, block)
	}
	return path, fields, nil
}

// decodeBool decodes a block's boolean key. A *bool distinguishes JSON null (nil
// pointer) from false: decoding null into a plain bool is a silent no-op that
// would read as "disabled" without an error (go-gotchas rule 27).
func decodeBool(path, block, key string, raw json.RawMessage) (bool, error) {
	var v *bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, fmt.Errorf("%s: %s.%s must be a boolean: %w", path, block, key, err)
	}
	if v == nil {
		return false, fmt.Errorf("%s: %s.%s must be a boolean, got null", path, block, key)
	}
	return *v, nil
}

// wholeDaysRE is the exact `<n>d` form: ASCII digits then a lowercase d.
var wholeDaysRE = regexp.MustCompile(`^[0-9]+d$`)

// parseStaleAfter decodes a block's `staleAfter` key: a JSON string that is
// either a whole-day count (`30d`) or a Go duration (`36h`), and positive. Mixed
// (`7d12h`), fractional (`1.5d`) and padded forms are errors. The bad value is
// quoted and length-limited in the error, never spliced in raw.
func parseStaleAfter(path, block string, raw json.RawMessage) (time.Duration, error) {
	var s *string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, fmt.Errorf("%s: %s.staleAfter must be a string: %w", path, block, err)
	}
	if s == nil {
		return 0, fmt.Errorf("%s: %s.staleAfter must be a string, got null", path, block)
	}
	d, ok := parseDuration(*s)
	if !ok || d <= 0 {
		return 0, fmt.Errorf("%s: %s.staleAfter %q must be a positive Go duration (e.g. \"36h\") or whole days (e.g. \"7d\")", path, block, collapse(*s))
	}
	return d, nil
}

// parseDuration parses `<n>d` (whole days, checked for overflow before the
// multiplication) or a Go duration. ok is false for anything else.
func parseDuration(s string) (time.Duration, bool) {
	if wholeDaysRE.MatchString(s) {
		const day = 24 * time.Hour
		days, err := strconv.ParseInt(strings.TrimSuffix(s, "d"), 10, 64)
		if err != nil || days > math.MaxInt64/int64(day) {
			return 0, false
		}
		return time.Duration(days) * day, true
	}
	d, err := time.ParseDuration(s)
	return d, err == nil
}

// readBoundedRegularFile reads path only when it is a regular file of at most
// maxConfigBytes. The config lives in a working tree, so it may be a committed
// FIFO or a symlink to a device: the open is non-blocking (a FIFO would
// otherwise block until a writer appears), the type is checked on the open
// handle rather than the path (no stat/open race), and the read is capped.
func readBoundedRegularFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only handle: a close error cannot lose data

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file (mode %s)", info.Mode().Type())
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("larger than the %d-byte limit", maxConfigBytes)
	}
	return data, nil
}
