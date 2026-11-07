// Package modelswitch is the bounded, per-user store of Claude Code model
// switches (#1179) and the read-only reader and line formatter over it.
//
// Claude Code fires a PostModelSwitch hook whenever the session model changes,
// including the automatic fallback it applies to a flagged request
// (`source: "auto"`). `cenci notify` records each switch here, on the hook
// side, so a later process in the same filesystem (the implement pipeline's
// Phase 9, through `cenci daemon model-switches`) can say that a run partly ran
// on another model without the daemon and without reading transcripts.
//
// Layout under <state>/cenci/model-switch/: one file per main session id,
// holding up to MaxSwitches lines `<epoch> <source> <from> <to>
// <main|subagent:<type>>` and an optional `more <n>` overflow line. A
// whole-store flock on `.lock` serializes writers and every write is a temp
// file plus rename. A write that fails after the session id validated leaves a
// `<session>.incomplete` marker, which the reader reports as unknown — a
// dropped switch must never read as "no switch".
//
// Every free-text value is validated before it is stored or rendered; an
// invalid model id or agent type is replaced by a placeholder, never kept.
//
// The writer (Record) swallows every error, because a hook must never break the
// calling agent session. The reader (Read) never creates, repairs or removes
// anything and separates "no record" from "cannot tell".
package modelswitch

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

const (
	// MaxSwitches is the number of switches kept per session; later ones only
	// raise the overflow count.
	MaxSwitches = 8
	// MaxSessions is the number of session files a writer leaves in the store.
	MaxSessions = 256
	// MaxAge is how long a session file survives without a write.
	MaxAge = 14 * 24 * time.Hour

	// InvalidModel renders a model id that failed validation.
	InvalidModel = "(invalid model id)"
	// UnknownAgentType renders a subagent whose type failed validation or was
	// missing.
	UnknownAgentType = "unknown"
	// UnknownSource is the stored source when the payload's was invalid or
	// missing; it never reads as a fallback.
	UnknownSource = "unknown"
	// UnknownLine is the exact line printed when the store cannot answer.
	UnknownLine = "Model switch status unknown"

	// autoSource is the one source value labelled a fallback: Claude Code
	// documents it as "an automatic fallback or other change Claude Code made
	// on its own".
	autoSource = "auto"

	// storedInvalid is the on-disk token for an invalid model id. It cannot be
	// a valid model id, so it never collides with a real value, and it contains
	// no space, which the record grammar needs.
	storedInvalid = "(invalid)"

	maxRecordBytes = 8192
	lockTimeout    = 3 * time.Second
	lockPoll       = 10 * time.Millisecond
	staleTempAge   = time.Hour
	incompleteExt  = ".incomplete"
)

var (
	modelRE     = regexp.MustCompile(`^[A-Za-z0-9._:\[\]-]{1,128}$`)
	agentTypeRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	sourceRE    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	sessionRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

	lineRE = regexp.MustCompile(`^([0-9]{1,12}) ([A-Za-z0-9_-]{1,32}) ` +
		`(\(invalid\)|[A-Za-z0-9._:\[\]-]{1,128}) (\(invalid\)|[A-Za-z0-9._:\[\]-]{1,128}) ` +
		`(main|subagent:[A-Za-z0-9._:-]{1,64})$`)
	moreRE = regexp.MustCompile(`^more ([0-9]{1,9})$`)
)

// Switch is one recorded model switch. From and To hold a valid model id or
// InvalidModel; Source holds a valid source or UnknownSource; AgentType is set
// only for a subagent.
type Switch struct {
	At        int64 // unix epoch seconds the hook recorded it
	Source    string
	From, To  string
	Subagent  bool
	AgentType string
}

// State is the reader's verdict.
type State string

const (
	// StateOK means the store holds at least one switch for the session.
	StateOK State = "ok"
	// StateNone means the store is healthy and has no record for the session.
	StateNone State = "none"
	// StateUnknown means the store cannot answer: an unresolvable session id or
	// store, an untrusted directory, a corrupt record or an .incomplete marker.
	StateUnknown State = "unknown"
)

// Result is Read's answer. Reason is set only with StateUnknown and carries no
// path.
type Result struct {
	State    State
	Switches []Switch
	More     int
	Reason   string
}

// ValidModel reports whether s is a model id that may be stored or rendered.
func ValidModel(s string) bool { return modelRE.MatchString(s) }

// SanitizeModel returns s when it is a valid model id, else InvalidModel.
func SanitizeModel(s string) string {
	if ValidModel(s) {
		return s
	}
	return InvalidModel
}

// SanitizeAgentType returns s when it is a valid agent type, else
// UnknownAgentType.
func SanitizeAgentType(s string) string {
	if agentTypeRE.MatchString(s) {
		return s
	}
	return UnknownAgentType
}

// SanitizeSource returns s when it is a valid source token, else UnknownSource.
func SanitizeSource(s string) string {
	if sourceRE.MatchString(s) {
		return s
	}
	return UnknownSource
}

// ValidSession reports whether s is usable as a store file name. It cannot
// start with a dot (the lock and temp files do) or end in the marker suffix
// (it would collide with another session's marker).
func ValidSession(s string) bool {
	return sessionRE.MatchString(s) && !strings.HasSuffix(s, incompleteExt)
}

// Fallback reports whether a switch with this source is labelled a fallback.
func Fallback(source string) bool { return source == autoSource }

// FormatLine renders the PR/status line for one switch. The label stays
// neutral unless fallback is set.
func FormatLine(from, to string, subagent bool, agentType string, fallback bool) string {
	attribution := "main session"
	if subagent {
		attribution = "subagent " + agentType
	}
	line := fmt.Sprintf("Model switch: %s → %s (%s)", from, to, attribution)
	if fallback {
		line += " — fallback"
	}
	return line
}

// FormatMore renders the overflow line for n switches past the cap.
func FormatMore(n int) string { return fmt.Sprintf("Model switch: +%d more", n) }

// Line renders the switch with FormatLine.
func (s Switch) Line() string {
	return FormatLine(s.From, s.To, s.Subagent, s.AgentType, Fallback(s.Source))
}

// Dir returns <S>/cenci/model-switch where <S> is $XDG_STATE_HOME when it is
// absolute, else $HOME/.local/state. It returns "" when neither resolves.
func Dir() string {
	state := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(state) {
		home := os.Getenv("HOME")
		if !filepath.IsAbs(home) {
			return ""
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "cenci", "model-switch")
}

type dirClass int

const (
	dirAbsent dirClass = iota
	dirTrusted
	dirUntrusted
)

// classifyDir classifies path with one Lstat. Only fs.ErrNotExist is absent;
// any other failure is untrusted. A present path is trusted when it is a real
// directory owned by the current uid and not group/other writable.
//
// These rules mirror hookhealth.classifyDir (internal/hookhealth), which in
// turn mirrors hardenDir in pkg/watch/socket.go; keep the three in sync.
func classifyDir(path string) dirClass {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return dirAbsent
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return dirUntrusted
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return dirUntrusted
	}
	if info.Mode().Perm()&0o022 != 0 {
		return dirUntrusted
	}
	return dirTrusted
}

// ensureDir creates path at 0700 when absent, then verifies it.
func ensureDir(path string) bool {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false
		}
		// A concurrent creator losing the race is fine; the check below
		// verifies whatever exists.
		_ = os.Mkdir(path, 0o700)
	}
	return classifyDir(path) == dirTrusted
}

// prepare creates and verifies the state root and the store directory.
func prepare(dir string) bool {
	return ensureDir(filepath.Dir(dir)) && ensureDir(dir)
}

// session is a parsed record file.
type session struct {
	switches []Switch
	more     int
}

func encodeModel(m string) string {
	if ValidModel(m) {
		return m
	}
	return storedInvalid
}

func decodeModel(m string) string {
	if m == storedInvalid {
		return InvalidModel
	}
	return m
}

func (s session) encode() []byte {
	var b strings.Builder
	for _, sw := range s.switches {
		attribution := "main"
		if sw.Subagent {
			attribution = "subagent:" + sw.AgentType
		}
		fmt.Fprintf(&b, "%d %s %s %s %s\n", sw.At, sw.Source, encodeModel(sw.From), encodeModel(sw.To), attribution)
	}
	if s.more > 0 {
		fmt.Fprintf(&b, "more %d\n", s.more)
	}
	return []byte(b.String())
}

// parse decodes a record file; any deviation from the grammar is an error.
func parse(buf []byte) (session, error) {
	text := string(buf)
	if !strings.HasSuffix(text, "\n") {
		return session{}, errors.New("record does not end in a newline")
	}
	var s session
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, line := range lines {
		if m := moreRE.FindStringSubmatch(line); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil || n < 1 || i != len(lines)-1 || len(s.switches) == 0 {
				return session{}, errors.New("malformed overflow line")
			}
			s.more = n
			continue
		}
		m := lineRE.FindStringSubmatch(line)
		if m == nil || len(s.switches) >= MaxSwitches {
			return session{}, errors.New("malformed record line")
		}
		at, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return session{}, err
		}
		sw := Switch{At: at, Source: m[2], From: decodeModel(m[3]), To: decodeModel(m[4])}
		if agentType, ok := strings.CutPrefix(m[5], "subagent:"); ok {
			sw.Subagent, sw.AgentType = true, agentType
		}
		s.switches = append(s.switches, sw)
	}
	if len(s.switches) == 0 {
		return session{}, errors.New("record holds no switch")
	}
	return s, nil
}

// readSession reads and parses dir/name. exists is false only when the file is
// absent; everything else that is not a valid regular record file is an error.
func readSession(path string) (s session, exists bool, err error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return session{}, false, nil
	}
	if err != nil {
		return session{}, true, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxRecordBytes {
		return session{}, true, errors.New("record is not a regular file of bounded size")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return session{}, true, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !os.SameFile(info, fi) {
		return session{}, true, errors.New("record changed while opening")
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil || len(buf) > maxRecordBytes {
		return session{}, true, errors.New("record unreadable or oversized")
	}
	s, err = parse(buf)
	return s, true, err
}

// Read reports the switches recorded for session in the store at dir. It is
// read-only. A missing store or record is StateNone; everything the store
// cannot answer for is StateUnknown with a path-free reason.
func Read(dir, session string) Result {
	unknown := func(reason string) Result { return Result{State: StateUnknown, Reason: reason} }
	if !ValidSession(session) {
		return unknown("session id is missing or invalid")
	}
	if dir == "" {
		return unknown("the state directory cannot be resolved")
	}
	for _, p := range []string{filepath.Dir(dir), dir} {
		switch classifyDir(p) {
		case dirAbsent:
			return Result{State: StateNone}
		case dirUntrusted:
			return unknown("the model-switch store is untrusted (symlink, not a directory, foreign-owned or group/other-writable)")
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, session+incompleteExt)); err == nil {
		return unknown("a write failed earlier, so the record is incomplete")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return unknown("the incomplete marker could not be checked")
	}
	rec, exists, err := readSession(filepath.Join(dir, session))
	switch {
	case err != nil:
		return unknown("the session record is unreadable or corrupt")
	case !exists:
		return Result{State: StateNone}
	}
	return Result{State: StateOK, Switches: rec.switches, More: rec.more}
}

// Record appends sw to the session's record at dir, stamped with now. It does
// nothing for an invalid session id, an unresolvable or untrusted store. Any
// failure after those checks leaves the session's .incomplete marker. It never
// returns an error and never prints.
func Record(dir, sessionID string, sw Switch, now time.Time) {
	if dir == "" || !ValidSession(sessionID) || !prepare(dir) {
		return
	}
	unlock, err := lockStore(dir)
	if err != nil {
		markIncomplete(dir, sessionID)
		return
	}
	defer unlock()

	sw.At = now.Unix()
	sw.Source = SanitizeSource(sw.Source)
	sw.From, sw.To = SanitizeModel(sw.From), SanitizeModel(sw.To)
	if sw.Subagent {
		sw.AgentType = SanitizeAgentType(sw.AgentType)
	} else {
		sw.AgentType = ""
	}

	if err := appendSwitch(dir, sessionID, sw); err != nil {
		markIncomplete(dir, sessionID)
		return
	}
	prune(dir, sessionID, now)
}

// lockStore takes the whole-store flock, polling so a stuck holder cannot hang
// a hook past its timeout.
func lockStore(dir string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			_ = f.Close()
			return nil, err
		}
		time.Sleep(lockPoll)
	}
}

// appendSwitch adds sw to dir/sessionID through a temp file and a rename. An
// existing record that does not parse is an error: it is never replaced.
func appendSwitch(dir, sessionID string, sw Switch) error {
	path := filepath.Join(dir, sessionID)
	rec, _, err := readSession(path)
	if err != nil {
		return err
	}
	if len(rec.switches) < MaxSwitches {
		rec.switches = append(rec.switches, sw)
	} else {
		rec.more++
	}

	f, err := os.CreateTemp(dir, fmt.Sprintf(".%s.%d.*.tmp", sessionID, os.Getpid()))
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(rec.encode())
	cerr := f.Close()
	if werr != nil || cerr != nil || os.Chmod(tmp, 0o600) != nil || os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
		return errors.Join(werr, cerr, errors.New("record write failed"))
	}
	return nil
}

// markIncomplete creates the session's .incomplete marker exclusively and
// without following symlinks, only inside a trusted store.
func markIncomplete(dir, sessionID string) {
	if classifyDir(dir) != dirTrusted {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, sessionID+incompleteExt), os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return
	}
	_ = f.Close()
}

// prune, under the store lock, removes record and marker files older than
// MaxAge and stale temp files, then the oldest session files beyond
// MaxSessions. keep (the session just written) is never removed. Only regular
// files are touched.
func prune(dir, keep string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type file struct {
		name string
		mod  time.Time
	}
	var sessions []file
	remove := func(name string) { _ = os.Remove(filepath.Join(dir, name)) }
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		name, age := e.Name(), now.Sub(info.ModTime())
		switch {
		case name == ".lock":
		case strings.HasPrefix(name, "."):
			if strings.HasSuffix(name, ".tmp") && age > staleTempAge {
				remove(name)
			}
		case name == keep || name == keep+incompleteExt:
		case age > MaxAge:
			remove(name)
		case !strings.HasSuffix(name, incompleteExt):
			sessions = append(sessions, file{name, info.ModTime()})
		}
	}
	// keep counts toward the cap but is not a removal candidate.
	excess := len(sessions) + 1 - MaxSessions
	if excess <= 0 {
		return
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].mod.Before(sessions[j].mod) })
	for _, f := range sessions[:excess] {
		remove(f.name)
		remove(f.name + incompleteExt)
	}
}
