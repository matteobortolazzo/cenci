// Package hookhealth is the bounded, per-user store of hook-delivery
// diagnostics (#1239) and the read-only reporter over it.
//
// Hooks always exit 0 and stay silent, so a failed delivery would otherwise be
// invisible. A failing hook leaves one tiny record per closed-set category
// (plus a last-ok record) under <state>/cenci/hook-health/. A record holds
// only `<unix-epoch> <client>\n` — never prompts, transcripts, tool arguments,
// secrets or environment. The same file contract is written by the POSIX-sh
// helper plugin/lib/hook-health.sh; the classification lives only here.
//
// The writer side (RecordFailure, RecordOK) swallows every error: a hook must
// never break the calling agent session. The reader side (Read) never creates
// anything and treats anything invalid as absent.
package hookhealth

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Category is a closed set of failure kinds; its string value is the record's
// file name. LastOK is the success record, not a failure.
type Category string

const (
	MissingBinary     Category = "missing-binary"
	BootstrapFailure  Category = "bootstrap-failure"
	MalformedInput    Category = "malformed-input"
	SocketUnavailable Category = "socket-unavailable"
	DeliveryFailure   Category = "delivery-failure"
	LastOK            Category = "last-ok"
)

// Clients are the only agents that ever record (OpenCode stays disabled).
const (
	ClientClaude = "claude"
	ClientCodex  = "codex"
)

// State is the reporter's verdict; its string value is the token on the first
// line of Render's output and is matched by `cenci doctor`.
type State string

const (
	StateHealthy        State = "healthy"
	StateRecovered      State = "recovered"
	StateNoEvidence     State = "no-evidence"
	StateCurrentFailure State = "current-failure"
)

const (
	maxRecordBytes     = 32
	failureMinInterval = 60 * time.Second
	okMinInterval      = 10 * time.Minute
	staleAfter         = 24 * time.Hour
	futureSkew         = 5 * time.Second
)

var failureCategories = []Category{MissingBinary, BootstrapFailure, MalformedInput, SocketUnavailable, DeliveryFailure}

var hints = map[Category]string{
	MissingBinary:     "the cenci binary was not found; reinstall or update the cenci-watch plugin (cenci update)",
	BootstrapFailure:  "installing the cenci binary failed; check the bootstrap log and network access, then restart the session",
	MalformedInput:    "a hook sent input that cenci notify could not parse; update the agent client and the cenci-watch plugin",
	SocketUnavailable: "the cenci daemon socket is not reachable; run cenci daemon start",
	DeliveryFailure:   "the daemon socket exists but delivery failed; check its permissions and run cenci daemon restart",
	LastOK:            "last successful event delivery",
}

// Categories returns the failure categories in stable display order, then
// LastOK.
func Categories() []Category {
	return append(append([]Category(nil), failureCategories...), LastOK)
}

// Hint is the static recovery hint of a category, "" for an unknown one.
func (c Category) Hint() string { return hints[c] }

func (c Category) isFailure() bool {
	for _, f := range failureCategories {
		if c == f {
			return true
		}
	}
	return false
}

// ValidClient reports whether client is in the closed set of recording agents.
func ValidClient(client string) bool {
	return client == ClientClaude || client == ClientCodex
}

// Record is one valid, non-stale record as the reporter saw it.
type Record struct {
	Category Category
	Client   string
	At       time.Time
}

// Report is the reporter's result. Failures holds the failures behind the
// state: for StateCurrentFailure the ones not yet superseded by LastOK, for
// StateRecovered the superseded ones, otherwise empty. LastOK is the valid
// non-stale success record, if any.
type Report struct {
	State    State
	Failures []Record
	LastOK   *Record
}

// ownerUID is the uid a pre-existing store directory must belong to; a var so
// tests can exercise the foreign-owner rejection without a second user.
var ownerUID = os.Getuid

var recordRE = regexp.MustCompile(`^[0-9]+ (claude|codex)\n$`)

// Dir returns <S>/cenci/hook-health where <S> is $XDG_STATE_HOME when it is
// absolute, else $HOME/.local/state. It returns "" when neither resolves: the
// writer then does nothing and the reader reports no evidence.
func Dir() string {
	state := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(state) {
		home := os.Getenv("HOME")
		if !filepath.IsAbs(home) {
			return ""
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "cenci", "hook-health")
}

// checkDir reports whether path is a directory the store may trust: not a
// symlink, a directory, owned by the current uid, and not group/other
// writable (the 0777 case is tested directly).
//
// These checks must stay in sync with hardenDir in watch/pkg/watch/socket.go
// (symlink, non-directory, foreign owner, group/other-writable), and with
// _hh_dir in plugin/lib/hook-health.sh. Unlike hardenDir, loose-but-not-
// writable modes (0750) are accepted silently: the store holds no secrets.
func checkDir(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != ownerUID() {
		return false
	}
	return info.Mode().Perm()&0o022 == 0
}

// ensureDir creates path at 0700 when absent, then verifies it with checkDir.
func ensureDir(path string) bool {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false
		}
		// Mkdir yields at most 0700 under any umask; a concurrent creator
		// losing the race is fine, checkDir below verifies whatever exists.
		_ = os.Mkdir(path, 0o700)
	}
	return checkDir(path)
}

// prepare creates and verifies the state root and the leaf for writing.
func prepare(dir string) bool {
	return ensureDir(filepath.Dir(dir)) && ensureDir(dir)
}

type record struct {
	ts     int64
	client string
}

// readRecord returns the record at path when it is a valid one: a regular
// file (never a symlink) of at most 32 bytes matching the record grammar and
// not dated later than now+5s. Anything else counts as absent.
func readRecord(path string, now time.Time) (record, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxRecordBytes {
		return record{}, false
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return record{}, false
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !os.SameFile(info, fi) {
		return record{}, false
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil || len(buf) > maxRecordBytes || !recordRE.Match(buf) {
		return record{}, false
	}
	epoch, client, _ := strings.Cut(strings.TrimSuffix(string(buf), "\n"), " ")
	ts, err := strconv.ParseInt(epoch, 10, 64)
	if err != nil || ts > now.Add(futureSkew).Unix() {
		return record{}, false
	}
	return record{ts: ts, client: client}, true
}

// writeRecord atomically replaces dir/name with `<now> <client>\n` through a
// PID-unique temp file in the same directory. Errors are swallowed; a failed
// attempt removes its temp file.
func writeRecord(dir string, name Category, client string, now time.Time) {
	f, err := os.CreateTemp(dir, fmt.Sprintf(".%s.%d.*.tmp", name, os.Getpid()))
	if err != nil {
		return
	}
	tmp := f.Name()
	_, werr := fmt.Fprintf(f, "%d %s\n", now.Unix(), client)
	cerr := f.Close()
	if werr != nil || cerr != nil ||
		os.Chmod(tmp, 0o600) != nil ||
		os.Rename(tmp, filepath.Join(dir, string(name))) != nil {
		_ = os.Remove(tmp)
	}
}

// RecordFailure records a failure of category for client. It does nothing for
// a category or client outside the closed sets, when no store directory
// resolves or passes the hardening checks, or when the category's existing
// valid record is younger than 60 s. It never returns an error and never
// prints.
func RecordFailure(category Category, client string) {
	if !category.isFailure() || !ValidClient(client) {
		return
	}
	dir := Dir()
	if dir == "" || !prepare(dir) {
		return
	}
	now := time.Now()
	if rec, ok := readRecord(filepath.Join(dir, string(category)), now); ok &&
		now.Unix()-rec.ts < int64(failureMinInterval/time.Second) {
		return
	}
	writeRecord(dir, category, client, now)
}

// RecordOK records a successful delivery for client. last-ok is written when
// it is absent or invalid, older than 10 minutes, or when any valid failure's
// timestamp is greater than or equal to its own. Same swallow-everything
// contract as RecordFailure.
func RecordOK(client string) {
	if !ValidClient(client) {
		return
	}
	dir := Dir()
	if dir == "" || !prepare(dir) {
		return
	}
	now := time.Now()
	if last, ok := readRecord(filepath.Join(dir, string(LastOK)), now); ok &&
		now.Unix()-last.ts < int64(okMinInterval/time.Second) {
		superseded := false
		for _, c := range failureCategories {
			if f, ok := readRecord(filepath.Join(dir, string(c)), now); ok && f.ts >= last.ts {
				superseded = true
				break
			}
		}
		if !superseded {
			return
		}
	}
	writeRecord(dir, LastOK, client, now)
}

// Read classifies the store at dir as of now. It is read-only: it never
// creates, repairs or removes anything. A missing store, a store that fails
// the directory checks, and invalid, oversized, symlinked, future-dated or
// older-than-24h records all count as absent.
func Read(dir string, now time.Time) Report {
	report := Report{State: StateNoEvidence, Failures: []Record{}}
	if dir == "" || !checkDir(filepath.Dir(dir)) || !checkDir(dir) {
		return report
	}

	load := func(c Category) (Record, bool) {
		rec, ok := readRecord(filepath.Join(dir, string(c)), now)
		if !ok || now.Unix()-rec.ts > int64(staleAfter/time.Second) {
			return Record{}, false
		}
		return Record{Category: c, Client: rec.client, At: time.Unix(rec.ts, 0).UTC()}, true
	}

	var failures []Record
	for _, c := range failureCategories {
		if r, ok := load(c); ok {
			failures = append(failures, r)
		}
	}
	sort.SliceStable(failures, func(i, j int) bool { return failures[i].At.After(failures[j].At) })

	if last, found := load(LastOK); found {
		report.LastOK = &last
	}

	if report.LastOK == nil {
		if len(failures) > 0 {
			report.State = StateCurrentFailure
			report.Failures = failures
		}
		return report
	}

	var current, superseded []Record
	for _, f := range failures {
		if !f.At.Before(report.LastOK.At) {
			current = append(current, f)
		} else {
			superseded = append(superseded, f)
		}
	}
	switch {
	case len(current) > 0:
		report.State = StateCurrentFailure
		report.Failures = current
	case len(superseded) > 0:
		report.State = StateRecovered
		report.Failures = superseded
	default:
		report.State = StateHealthy
	}
	return report
}

// Render formats a report. The first line is exactly `hook health: <state>`;
// detail lines follow as `  <category> (<client>, <RFC3339 UTC>): <hint>`.
func Render(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "hook health: %s\n", r.State)
	for _, f := range r.Failures {
		writeDetail(&b, f)
	}
	if r.LastOK != nil && (r.State == StateHealthy || r.State == StateRecovered) {
		writeDetail(&b, *r.LastOK)
	}
	if r.State == StateNoEvidence {
		b.WriteString("  no recent records: this is not proof of health (hooks may not have run, or records are older than 24h)\n")
	}
	return b.String()
}

func writeDetail(b *strings.Builder, r Record) {
	fmt.Fprintf(b, "  %s (%s, %s): %s\n", r.Category, r.Client, r.At.UTC().Format(time.RFC3339), r.Category.Hint())
}
